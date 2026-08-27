package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/mind-vm/agentloop"
)

// renderer is the output shape for one invocation. Subcommands drive
// the loop and hand everything they learn to a renderer, so the human
// and machine surfaces never diverge in what they report — only in how.
type renderer interface {
	// event renders one streamed run event.
	event(agentloop.RunEvent)
	// result renders a finished run.
	result(agentloop.RunResult)
	// fatal renders a failure that ended the invocation.
	fatal(error)
}

// --- human ---------------------------------------------------------

// humanRenderer writes the loop's activity to stderr and the agent's
// answer to stdout. Keeping the two streams apart is what makes
// `answer=$(agentloop run "...")` and `agentloop run "..." | jq` behave:
// stdout carries the answer and nothing else — no banner, no framing.
type humanRenderer struct {
	out    io.Writer
	errOut io.Writer
	quiet  bool

	// streamAnswer echoes response_chunk events to stderr as they
	// arrive. Off when stdout is the same terminal, where it would
	// print the answer twice.
	streamAnswer bool

	// wroteChunk tracks whether a partial answer is sitting unterminated
	// on stderr, so the next framed line starts on its own row.
	wroteChunk bool
}

// sandboxEventNoise are sandbox event kinds the loop already reports
// through its own richer event types. Rendering both would duplicate the
// script, its result, and the final answer on screen.
var sandboxEventNoise = map[string]bool{
	"script":        true,
	"script_result": true,
	"answer":        true,
}

func (h *humanRenderer) event(evt agentloop.RunEvent) {
	if h.quiet {
		return
	}
	switch evt.Type {
	case "execute_js":
		h.frame("run()", evt.Content)
	case "execute_js_result":
		h.frame("result", evt.Content)
	case "compacted":
		h.line("compacted history: %v → %v messages", evt.Args["messages_before"], evt.Args["messages_after"])
	case "sandbox_event":
		kind, _ := evt.Args["kind"].(string)
		if sandboxEventNoise[kind] || strings.TrimSpace(evt.Content) == "" {
			return
		}
		// A file the agent changed is the line a person scanning the run
		// most needs to catch, so it is marked rather than blending into
		// everything else the sandbox reports.
		if kind == "file_write" {
			h.line("✎ %s", evt.Content)
			return
		}
		h.line("%s: %s", kind, evt.Content)
	case "response_chunk":
		if !h.streamAnswer {
			return
		}
		fmt.Fprint(h.errOut, evt.Content)
		h.wroteChunk = true
	case "warning", "error":
		h.line("[%s] %s", evt.Type, evt.Content)
	}
}

func (h *humanRenderer) result(res agentloop.RunResult) {
	if res.FinalText != "" {
		h.endChunks()
		fmt.Fprintln(h.out, res.FinalText)
	}
	if h.quiet {
		return
	}
	// The status line goes to stderr because it is commentary on the
	// run, not part of the answer.
	h.line("%s · %d steps · %d+%d tokens", res.Status, res.Steps, res.Tokens.Prompt, res.Tokens.Completion)
}

func (h *humanRenderer) fatal(err error) {
	h.endChunks()
	fmt.Fprintln(h.errOut, prefixed(err))
}

// frame prints a labelled block of the agent's own output.
func (h *humanRenderer) frame(label, body string) {
	h.endChunks()
	fmt.Fprintf(h.errOut, "\n--- %s ---\n%s\n", label, strings.TrimRight(body, "\n"))
}

func (h *humanRenderer) line(format string, args ...any) {
	h.endChunks()
	fmt.Fprintf(h.errOut, "· "+format+"\n", args...)
}

// endChunks closes off a streamed answer still sitting mid-line.
func (h *humanRenderer) endChunks() {
	if h.wroteChunk {
		fmt.Fprintln(h.errOut)
		h.wroteChunk = false
	}
}

// --- json ----------------------------------------------------------

// jsonRenderer writes one JSON object per line to stdout: every run
// event as it streams, then a terminal object of type "result". This is
// the surface another program drives — agentloop.RunEvent already
// carries JSON tags, so an event is emitted as-is rather than through a
// second, drifting representation.
type jsonRenderer struct {
	out    io.Writer
	errOut io.Writer

	// chunks includes response_chunk events. Off by default: the
	// terminal "response" event carries the complete answer, chunks can
	// outnumber every other event combined, and a run configured with a
	// Redactor suppresses them entirely — so a consumer cannot depend on
	// them being there.
	chunks bool
}

func (j *jsonRenderer) event(evt agentloop.RunEvent) {
	if evt.Type == "response_chunk" && !j.chunks {
		return
	}
	j.write(evt, evt.Type)
}

// jsonResult is the terminal object. agentloop.RunResult carries no
// JSON tags of its own, and giving the wire format its own type keeps
// the CLI's output contract explicit rather than incidental.
type jsonResult struct {
	Type             string     `json:"type"`
	SessionID        string     `json:"session_id"`
	Status           string     `json:"status"`
	FinalText        string     `json:"final_text,omitempty"`
	Steps            int        `json:"steps"`
	Tokens           jsonTokens `json:"tokens"`
	DataBytesCarried int64      `json:"data_bytes_carried,omitempty"`
}

type jsonTokens struct {
	Prompt     int32 `json:"prompt"`
	Completion int32 `json:"completion"`
}

func (j *jsonRenderer) result(res agentloop.RunResult) {
	j.write(jsonResult{
		Type:             "result",
		SessionID:        res.RunID,
		Status:           res.Status,
		FinalText:        res.FinalText,
		Steps:            res.Steps,
		Tokens:           jsonTokens{Prompt: res.Tokens.Prompt, Completion: res.Tokens.Completion},
		DataBytesCarried: res.DataBytesCarried,
	}, "result")
}

func (j *jsonRenderer) fatal(err error) {
	j.write(struct {
		Type  string `json:"type"`
		Error string `json:"error"`
	}{"fatal", err.Error()}, "fatal")
	fmt.Fprintln(j.errOut, prefixed(err))
}

// prefixed names the program as the source of an error, without saying
// so twice — errors out of the engine are already prefixed "agentloop:".
func prefixed(err error) string {
	msg := err.Error()
	if strings.HasPrefix(msg, "agentloop:") {
		return msg
	}
	return "agentloop: " + msg
}

// write emits one NDJSON line. A value that will not marshal — an event
// whose Args carry a NaN or an infinity out of the JS runtime, say —
// degrades to a line naming the problem rather than silently vanishing
// or corrupting the stream.
func (j *jsonRenderer) write(v any, kind string) {
	b, err := json.Marshal(v)
	if err != nil {
		b, _ = json.Marshal(struct {
			Type  string `json:"type"`
			Error string `json:"error"`
		}{kind, "event is not representable as JSON: " + err.Error()})
	}
	fmt.Fprintf(j.out, "%s\n", b)
}
