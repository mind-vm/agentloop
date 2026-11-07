package main

import (
	"fmt"
	"sync"
	"sync/atomic"

	"github.com/a-h/templ"
	"github.com/google/uuid"

	"github.com/mind-vm/thinking-core/agentloop"
	"github.com/mind-vm/thinking-core/agentrun"
	"github.com/mind-vm/thinking-core/examples/blocks-tabler/view"
	"github.com/mind-vm/thinking-core/sandbox"
)

// chartSeq names every chart drawn by this process. Process-wide rather
// than per turn because a page can hold charts from many turns, and a
// replaced block's new chart must not reuse the old one's id.
var chartSeq atomic.Int64

// turnRenderer turns one run's events into fragments. Events arrive from the
// loop and, for ask(), from the broker, so it locks: the step counter and
// the last script's code are shared between them.
type turnRenderer struct {
	s    *server
	sid  uuid.UUID
	turn int
	html func(slot, id, mode string, c templ.Component)

	mu       sync.Mutex
	steps    int
	code     string
	blocks   int
	answered bool // a terminal fragment (footer or failure) is out
}

func (t *turnRenderer) stepID() string { return fmt.Sprintf("step-%d-%d", t.turn, t.steps) }

func (t *turnRenderer) event(e agentrun.Event) {
	t.mu.Lock()
	defer t.mu.Unlock()
	switch e.Type {
	case "execute_js":
		t.steps++
		t.code = e.Content
		t.html("steps", t.stepID(), "", view.Step(t.stepID(), t.steps, e.Content, nil))
	case "execute_js_result":
		if t.steps == 0 {
			return
		}
		result := e.Content
		t.html("steps", t.stepID(), "", view.Step(t.stepID(), t.steps, t.code, &result))
	case "sandbox_event":
		if kind, _ := e.Args["kind"].(string); kind == string(sandbox.EventBlock) {
			t.block(e)
		}
	case "input_request":
		ask, err := view.AskFromEvent(e.Content, e.Args)
		if err != nil {
			t.html("asks", "", "", view.Failure("could not read the question: "+err.Error()))
			return
		}
		t.s.mu.Lock()
		t.s.asks[ask.RequestID] = ask
		t.s.mu.Unlock()
		t.html("asks", "ask-"+ask.RequestID, "", view.Ask(ask))
	case "response":
		t.html("answer", "", "set", view.Answer(e.Content))
	case "error":
		t.answered = true
		t.html("status", "", "set", view.Failure(e.Content))
	case "done":
		if e.Summary != nil && !t.answered {
			t.answered = true
			t.html("status", "", "set", view.Footer(e.Summary.Steps, e.Summary.Tokens.Prompt, e.Summary.Tokens.Completion, t.blocks))
		}
	}
}

func (t *turnRenderer) block(e agentrun.Event) {
	summary, _ := e.Args["summary"].(string)
	b := view.Block{
		Type:    summary,
		Payload: view.Normalize(e.Args["payload"]),
		ChartID: fmt.Sprintf("c%d", chartSeq.Add(1)),
	}
	t.blocks++
	if id, _ := b.Payload["id"].(string); id != "" {
		b.DOMID = "block-" + id
		b.Revised = t.s.placeBlock(t.sid, t.turn, id)
	} else {
		b.DOMID = fmt.Sprintf("block-%d-%d", t.turn, t.blocks)
	}
	t.html("blocks", b.DOMID, "", view.BlockView(b))
}

// finish closes the turn on the paths where the loop sent no terminal event
// of its own: an error returned rather than emitted, or a run that ended
// without a done.
func (t *turnRenderer) finish(res agentloop.RunResult, err error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.answered {
		return
	}
	t.answered = true
	if err != nil {
		t.html("status", "", "set", view.Failure(err.Error()))
		return
	}
	t.html("status", "", "set", view.Footer(res.Steps, res.Tokens.Prompt, res.Tokens.Completion, t.blocks))
}
