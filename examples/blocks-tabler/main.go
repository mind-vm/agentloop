// Command blocks-tabler is ext.BlocksPack drawn on the server: the same
// agent, dataset and ask() flow as examples/blocks, but every block reaches
// the browser as a Tabler HTML fragment rendered with tabler-templ, so there
// is no Node, no bundle and no client-side chart code beyond ApexCharts.
//
// Usage:
//
//	export OPENAI_API_KEY=sk-...    # or OPENAI_BASE_URL for any OpenAI-wire endpoint
//	cd examples/blocks-tabler
//	go run .                        # http://localhost:8096
//
// It is a module of its own so that templ and tabler-templ, and the newer
// Go they need, stay out of the core's go.mod: the core promises to be
// dependency-light, and a renderer is the application's choice.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"slices"
	"sync"

	"github.com/a-h/templ"
	"github.com/google/uuid"

	"github.com/mind-vm/thinking-core/agentloop"
	"github.com/mind-vm/thinking-core/agentloopmem"
	"github.com/mind-vm/thinking-core/agentrun"
	"github.com/mind-vm/thinking-core/examples/blocks-tabler/view"
	"github.com/mind-vm/thinking-core/ext"
	"github.com/mind-vm/thinking-core/llm"
	"github.com/mind-vm/thinking-core/sandbox"
)

//go:embed static
var static embed.FS

// guidance is examples/blocks' word for word: the comparison between the two
// renderers is only fair if the agent is told the same thing.
const guidance = `// Present results as blocks the UI renders, then finish with answer() and a short text summary.
// answer() ENDS your turn: never call it in a script that is still reading or exploring — only after the blocks are out.
// The data is in the workspace (listDir(), readFile()). Every number in a block must come from it —
// never invent one. Parse CSV in a script, keep the rows in state, and build blocks from state.
// When the request leaves real choices open (period, regions, which view), ask them all at once with
// ask({type: "questions"}) before computing. When revising a block from an earlier answer, reuse its id.`

var examples = []string{
	"Build me a sales report.",
	"Give me an overview of orders.csv: revenue, open and overdue amounts, and a table of the overdue orders.",
	"Show revenue per month as a line chart and per region as a bar chart.",
	"Which products sell best? Show the share of revenue per product as a donut chart.",
}

func main() {
	addr := flag.String("addr", defaultAddr(), "listen address ($PORT picks the port)")
	data := flag.String("data", "../blocks/data", "directory the agent may read")
	flag.Parse()

	client, err := llm.NewOpenAI(llm.ConfigFromEnv())
	if err != nil {
		log.Fatalf("blocks-tabler: %v", err)
	}
	root, err := os.OpenRoot(*data)
	if err != nil {
		log.Fatalf("blocks-tabler: open data directory: %v", err)
	}

	broker := agentrun.NewBroker()
	l := agentloop.New(agentloop.Config{
		LLM:            client,
		Sessions:       agentloopmem.NewSessionStore(nil),
		Steps:          agentloopmem.NewStepStore(),
		SandboxBuilder: &agentloop.DefaultSandboxBuilder{Capabilities: capabilities(client, root, broker)},
	})

	log.Printf("blocks-tabler: http://%s (data: %s)", *addr, *data)
	log.Fatal(http.ListenAndServe(*addr, newServer(l, broker)))
}

func defaultAddr() string {
	if p := os.Getenv("PORT"); p != "" {
		return "localhost:" + p
	}
	return "localhost:8096"
}

// capabilities is examples/blocks' bundle: the defaults without the network,
// the dataset read-only, ask() through the broker, and the blocks.
func capabilities(client llm.Client, root *os.Root, broker *agentrun.Broker) []agentloop.Capability {
	caps := slices.DeleteFunc(agentloop.DefaultCapabilities(client, ""), func(c agentloop.Capability) bool {
		return c.Name == "fetch" || c.Name == "http"
	})
	return append(caps,
		agentloop.Capability{
			Name: "workspace",
			Build: func(bc agentloop.BuildContext) ([]sandbox.Pack, error) {
				return []sandbox.Pack{ext.WorkspacePack(bc.Ctx, root, ext.WorkspaceOptions{
					Refuse: func(string) string {
						return "the demo dataset is read-only; keep derived data in state instead"
					},
				})}, nil
			},
		},
		agentloop.Capability{
			Name: "ask",
			Build: func(bc agentloop.BuildContext) ([]sandbox.Pack, error) {
				sid, err := uuid.Parse(bc.SessionID)
				if err != nil {
					return nil, fmt.Errorf("ask: session id %q is not a uuid", bc.SessionID)
				}
				return []sandbox.Pack{sandbox.InputPack(broker.Requester(sid))}, nil
			},
		},
		agentloop.Capability{
			Name: "blocks",
			Build: func(agentloop.BuildContext) ([]sandbox.Pack, error) {
				return []sandbox.Pack{ext.BlocksPack(ext.BlocksOptions{Guidance: guidance})}, nil
			},
		},
	)
}

type server struct {
	loop   agentloop.Loop
	broker *agentrun.Broker

	mu       sync.Mutex
	sessions map[uuid.UUID]*session
	asks     map[string]view.AskRequest // pending, by request id, to render the answer
}

// session is what the renderer remembers between turns: which turn last
// showed each block id, so a block revised from an earlier turn can say so.
// The Mantine page keeps this in the browser; here the server draws the
// block, so the server has to know.
type session struct {
	turn   int
	blocks map[string]int
}

func newServer(l agentloop.Loop, broker *agentrun.Broker) http.Handler {
	s := &server{loop: l, broker: broker, sessions: map[uuid.UUID]*session{}, asks: map[string]view.AskRequest{}}
	mux := http.NewServeMux()
	sub, _ := fs.Sub(static, "static")
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServerFS(sub)))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = view.Page(examples).Render(r.Context(), w)
	})
	mux.HandleFunc("POST /api/run", s.run)
	mux.HandleFunc("POST /api/reply", s.reply)
	return mux
}

// fragment is one frame of the page's stream: HTML for a slot of the newest
// turn. With ID, the page replaces the element of that id instead of
// appending, wherever on the page it is — see app.js for the rules.
type fragment struct {
	Slot string `json:"slot"` // "turn" for a new turn, else a data-slot of it
	ID   string `json:"id,omitempty"`
	Mode string `json:"mode,omitempty"` // "set" empties the slot first
	HTML string `json:"html"`
}

type runRequest struct {
	Message   string `json:"message"`
	SessionID string `json:"sessionId"`
}

// run streams one turn as HTML. It rides agentrun's SSE writer — an Event
// whose Type is "html" and whose Args are the fragment — because the writer
// already serialises the loop's goroutines and the broker's emits, which is
// the part worth not writing twice.
func (s *server) run(w http.ResponseWriter, r *http.Request) {
	var req runRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Message == "" {
		http.Error(w, `want {"message": "...", "sessionId": "..."}`, http.StatusBadRequest)
		return
	}
	sid, err := uuid.Parse(req.SessionID)
	if err != nil {
		http.Error(w, "sessionId must be a uuid the page chose", http.StatusBadRequest)
		return
	}
	stream := agentrun.Open(w)
	if stream == nil {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	t := s.beginTurn(sid)
	ctx := r.Context()
	send := func(f fragment) {
		stream.Send(agentrun.Event{Type: "html", Args: map[string]any{"slot": f.Slot, "id": f.ID, "mode": f.Mode, "html": f.HTML}})
	}
	html := func(slot, id, mode string, c templ.Component) {
		out, err := view.Render(ctx, c)
		if err != nil {
			out, _ = view.Render(ctx, view.Failure("rendering failed: "+err.Error()))
		}
		send(fragment{Slot: slot, ID: id, Mode: mode, HTML: out})
	}

	html("turn", "", "", view.Turn(req.Message))

	tr := &turnRenderer{s: s, sid: sid, turn: t, html: html}
	// The broker's input_request goes through the same translation as the
	// loop's events, so a question lands in the turn like everything else.
	end := s.broker.Begin(sid, uuid.Nil, func(e agentrun.Event) { tr.event(e) })
	defer end()
	res, err := s.loop.Run(ctx, agentloop.RunRequest{
		SessionID: req.SessionID,
		Scope:     agentloop.Scope{WorkspaceID: "demo"},
		Message:   req.Message,
		OnEvent:   func(e agentloop.RunEvent) { tr.event(agentrun.FromRunEvent(e)) },
	})
	if errors.Is(err, context.Canceled) {
		return
	}
	tr.finish(res, err)
	stream.Finish(agentrun.SummaryOf(res), nil)
}

func (s *server) beginTurn(sid uuid.UUID) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[sid]
	if sess == nil {
		sess = &session{blocks: map[string]int{}}
		s.sessions[sid] = sess
	}
	sess.turn++
	return sess.turn
}

// placeBlock records that this turn shows the block and reports whether an
// earlier turn showed it first.
func (s *server) placeBlock(sid uuid.UUID, turn int, id string) (revised bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess := s.sessions[sid]
	prev, seen := sess.blocks[id]
	sess.blocks[id] = turn
	return seen && prev < turn
}

type replyRequest struct {
	SessionID string `json:"sessionId"`
	RequestID string `json:"requestId"`
	Value     any    `json:"value"`
}

// reply answers a pending ask() and returns the form's replacement: the
// answer as the reader gave it.
func (s *server) reply(w http.ResponseWriter, r *http.Request) {
	var req replyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RequestID == "" {
		http.Error(w, `want {"sessionId", "requestId", "value"}`, http.StatusBadRequest)
		return
	}
	sid, err := uuid.Parse(req.SessionID)
	if err != nil {
		http.Error(w, "sessionId is not a uuid", http.StatusBadRequest)
		return
	}
	if err := s.broker.Reply(sid, uuid.Nil, req.RequestID, req.Value); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	s.mu.Lock()
	ask := s.asks[req.RequestID]
	delete(s.asks, req.RequestID)
	s.mu.Unlock()
	if ask.RequestID == "" {
		ask.RequestID = req.RequestID
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = view.Answered(ask, req.Value).Render(r.Context(), w)
}
