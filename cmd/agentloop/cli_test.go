package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mind-vm/agentloop"
)

func TestExitCodeFor(t *testing.T) {
	cases := []struct {
		name string
		res  agentloop.RunResult
		err  error
		want int
	}{
		{"completed", agentloop.RunResult{Status: "completed"}, nil, exitOK},
		{"error status", agentloop.RunResult{Status: "error"}, nil, exitError},
		{"max iterations", agentloop.RunResult{Status: "max_iterations"}, nil, exitMaxSteps},
		{"unknown status", agentloop.RunResult{Status: "wat"}, nil, exitError},
		{"no status at all", agentloop.RunResult{}, nil, exitError},
		// A returned error outranks the status: a run that produced an
		// answer and then failed is not a success to a caller scripting it.
		{"error outranks status", agentloop.RunResult{Status: "completed"}, errors.New("boom"), exitError},
		// The loop reports exhaustion as a populated result AND an error
		// saying the same thing. The status has to win, or "needed more
		// steps" is indistinguishable from "something broke".
		{"max iterations as the loop reports it",
			agentloop.RunResult{Status: "max_iterations", Steps: 1},
			errors.New("agentloop: agent exceeded max iterations (1) without emitting DONE"),
			exitMaxSteps},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := exitCodeFor(tc.res, tc.err); got != tc.want {
				t.Fatalf("exitCodeFor = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestResolveMessageFromArgs(t *testing.T) {
	got, err := resolveMessage([]string{"what's", "12 * 7?"}, nil)
	if err != nil {
		t.Fatalf("resolveMessage: %v", err)
	}
	if want := "what's 12 * 7?"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestResolveMessageFromStdin(t *testing.T) {
	got, err := resolveMessage(nil, pipeWith(t, "  summarise this\n"))
	if err != nil {
		t.Fatalf("resolveMessage: %v", err)
	}
	if want := "summarise this"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// Arguments win over stdin: an explicit message is never overridden by
// whatever happens to be piped in.
func TestResolveMessageArgsBeatStdin(t *testing.T) {
	got, err := resolveMessage([]string{"from args"}, pipeWith(t, "from stdin"))
	if err != nil {
		t.Fatalf("resolveMessage: %v", err)
	}
	if got != "from args" {
		t.Fatalf("got %q, want %q", got, "from args")
	}
}

func TestResolveMessageEmpty(t *testing.T) {
	if _, err := resolveMessage(nil, pipeWith(t, "   \n\n")); err == nil {
		t.Fatal("want an error when neither args nor stdin carry a message")
	}
}

// pipeWith returns a non-terminal *os.File holding content, standing in
// for redirected stdin.
func pipeWith(t *testing.T, content string) *os.File {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdin")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { f.Close() })
	return f
}

func TestOptionsResolveDefaults(t *testing.T) {
	var o options
	if err := o.resolve(); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if o.cwd == "" {
		t.Error("cwd should default to the working directory")
	}
	if o.session == "" {
		t.Error("session should default to a generated id")
	}

	var other options
	if err := other.resolve(); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if other.session == o.session {
		t.Error("generated session ids should not repeat")
	}
}

func TestOptionsResolveRejectsNonsense(t *testing.T) {
	cases := map[string]options{
		"json-stream without json": {jsonStream: true},
		"negative max steps":       {maxSteps: -1},
		"negative history window":  {historyWindow: -5},
		"negative timeout":         {timeout: -1},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			if err := o.resolve(); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestOptionsBindParsesFlags(t *testing.T) {
	var o options
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(&bytes.Buffer{})
	o.bind(fs)

	err := fs.Parse([]string{
		"--model", "gpt-4o", "--max-steps", "7", "--timeout", "90s",
		"--history-window", "12", "--session", "s-1", "--json", "hello", "world",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if o.model != "gpt-4o" || o.maxSteps != 7 || o.timeout.String() != "1m30s" ||
		o.historyWindow != 12 || o.session != "s-1" || !o.asJSON {
		t.Fatalf("flags not bound as expected: %+v", o)
	}
	// Everything after the flags is the message, left for resolveMessage.
	if got := strings.Join(fs.Args(), " "); got != "hello world" {
		t.Fatalf("positional args = %q, want %q", got, "hello world")
	}
}

// --- renderers ------------------------------------------------------

func TestHumanRendererSeparatesStreams(t *testing.T) {
	var out, errOut bytes.Buffer
	h := &humanRenderer{out: &out, errOut: &errOut}

	h.event(agentloop.RunEvent{Type: "execute_js", Content: "function run() {}"})
	h.event(agentloop.RunEvent{Type: "execute_js_result", Content: "42"})
	h.result(agentloop.RunResult{Status: "completed", FinalText: "84", Steps: 2})

	// stdout carries the answer and nothing else — no banner, no framing.
	if got := out.String(); got != "84\n" {
		t.Fatalf("stdout = %q, want %q", got, "84\n")
	}
	stderr := errOut.String()
	for _, want := range []string{"run()", "function run() {}", "result", "42", "completed · 2 steps"} {
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr missing %q\ngot:\n%s", want, stderr)
		}
	}
}

func TestHumanRendererQuietWritesOnlyTheAnswer(t *testing.T) {
	var out, errOut bytes.Buffer
	h := &humanRenderer{out: &out, errOut: &errOut, quiet: true}

	h.event(agentloop.RunEvent{Type: "execute_js", Content: "noisy"})
	h.event(agentloop.RunEvent{Type: "warning", Content: "also noisy"})
	h.result(agentloop.RunResult{Status: "completed", FinalText: "84"})

	if got := out.String(); got != "84\n" {
		t.Fatalf("stdout = %q, want %q", got, "84\n")
	}
	if errOut.Len() != 0 {
		t.Fatalf("stderr should be silent under --quiet, got %q", errOut.String())
	}
}

// A failed run has no answer, so stdout stays empty and a script reading
// it gets nothing rather than a status string it might mistake for one.
func TestHumanRendererFailedRunWritesNoAnswer(t *testing.T) {
	var out, errOut bytes.Buffer
	h := &humanRenderer{out: &out, errOut: &errOut}
	h.result(agentloop.RunResult{Status: "max_iterations", Steps: 20})

	if out.Len() != 0 {
		t.Fatalf("stdout should be empty for an answerless run, got %q", out.String())
	}
	if !strings.Contains(errOut.String(), "max_iterations") {
		t.Errorf("stderr should report the status, got %q", errOut.String())
	}
}

// Streamed chunks leave the cursor mid-line; the next framed line has to
// start on a row of its own.
func TestHumanRendererTerminatesStreamedChunks(t *testing.T) {
	var out, errOut bytes.Buffer
	h := &humanRenderer{out: &out, errOut: &errOut, streamAnswer: true}

	h.event(agentloop.RunEvent{Type: "response_chunk", Content: "par"})
	h.event(agentloop.RunEvent{Type: "response_chunk", Content: "tial"})
	h.event(agentloop.RunEvent{Type: "warning", Content: "after"})

	if got := errOut.String(); !strings.Contains(got, "partial\n") {
		t.Fatalf("streamed chunks not terminated before the next line: %q", got)
	}
}

// The sandbox emits its own script events; the loop reports the same
// script through execute_js. Rendering both shows it twice.
func TestHumanRendererDropsDuplicateSandboxEvents(t *testing.T) {
	var out, errOut bytes.Buffer
	h := &humanRenderer{out: &out, errOut: &errOut}

	h.event(agentloop.RunEvent{Type: "sandbox_event", Content: "ran a script", Args: map[string]any{"kind": "script"}})
	h.event(agentloop.RunEvent{Type: "sandbox_event", Content: "fetched example.com", Args: map[string]any{"kind": "fetch"}})

	stderr := errOut.String()
	if strings.Contains(stderr, "ran a script") {
		t.Errorf("script events duplicate execute_js and should be dropped, got %q", stderr)
	}
	if !strings.Contains(stderr, "fetched example.com") {
		t.Errorf("informative sandbox events should render, got %q", stderr)
	}
}

func TestJSONRendererEmitsOnePerLine(t *testing.T) {
	var out, errOut bytes.Buffer
	j := &jsonRenderer{out: &out, errOut: &errOut}

	j.event(agentloop.RunEvent{Type: "execute_js", Content: "function run() {}"})
	j.event(agentloop.RunEvent{Type: "response_chunk", Content: "dropped by default"})
	j.result(agentloop.RunResult{
		RunID: "s-1", Status: "completed", FinalText: "84", Steps: 2,
		Tokens: agentloop.TokenUsage{Prompt: 10, Completion: 3},
	})

	lines := decodeNDJSON(t, out.String())
	if len(lines) != 2 {
		t.Fatalf("want 2 lines (chunk dropped), got %d: %v", len(lines), lines)
	}
	if lines[0]["type"] != "execute_js" || lines[0]["content"] != "function run() {}" {
		t.Errorf("first line wrong: %v", lines[0])
	}

	res := lines[1]
	if res["type"] != "result" || res["session_id"] != "s-1" || res["status"] != "completed" ||
		res["final_text"] != "84" || res["steps"] != float64(2) {
		t.Errorf("result line wrong: %v", res)
	}
	tokens, ok := res["tokens"].(map[string]any)
	if !ok || tokens["prompt"] != float64(10) || tokens["completion"] != float64(3) {
		t.Errorf("tokens wrong: %v", res["tokens"])
	}
}

func TestJSONRendererIncludesChunksWhenAsked(t *testing.T) {
	var out, errOut bytes.Buffer
	j := &jsonRenderer{out: &out, errOut: &errOut, chunks: true}
	j.event(agentloop.RunEvent{Type: "response_chunk", Content: "tok"})

	lines := decodeNDJSON(t, out.String())
	if len(lines) != 1 || lines[0]["type"] != "response_chunk" {
		t.Fatalf("want the chunk emitted, got %v", lines)
	}
}

// An Args value out of the JS runtime can be unmarshalable (a NaN, say).
// That must not corrupt the stream or silently drop the event.
func TestJSONRendererSurvivesUnmarshalableEvent(t *testing.T) {
	var out, errOut bytes.Buffer
	j := &jsonRenderer{out: &out, errOut: &errOut}

	j.event(agentloop.RunEvent{Type: "sandbox_event", Args: map[string]any{"value": math.NaN()}})

	lines := decodeNDJSON(t, out.String())
	if len(lines) != 1 {
		t.Fatalf("want exactly one line, got %d", len(lines))
	}
	if lines[0]["type"] != "sandbox_event" {
		t.Errorf("the line should still name the event type, got %v", lines[0])
	}
	if _, ok := lines[0]["error"]; !ok {
		t.Errorf("the line should explain why the event is missing, got %v", lines[0])
	}
}

func TestJSONRendererFatalGoesToBothStreams(t *testing.T) {
	var out, errOut bytes.Buffer
	j := &jsonRenderer{out: &out, errOut: &errOut}
	j.fatal(errors.New("endpoint unreachable"))

	lines := decodeNDJSON(t, out.String())
	if len(lines) != 1 || lines[0]["type"] != "fatal" || lines[0]["error"] != "endpoint unreachable" {
		t.Fatalf("stdout fatal line wrong: %v", lines)
	}
	if !strings.Contains(errOut.String(), "endpoint unreachable") {
		t.Errorf("stderr should carry the message too, got %q", errOut.String())
	}
}

func decodeNDJSON(t *testing.T, s string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("line is not valid JSON (%v): %q", err, line)
		}
		out = append(out, m)
	}
	return out
}

// --- routing --------------------------------------------------------

func TestRoute(t *testing.T) {
	cases := []struct {
		name     string
		argv     []string
		wantCmd  string
		wantRest []string
	}{
		{"no arguments", nil, "run", nil},
		{"bare message", []string{"hello"}, "run", []string{"hello"}},
		// Flags before an implicit message stay with the message, so the
		// run flag set still sees them.
		{"flags before an implicit message", []string{"--json", "hello"}, "run", []string{"--json", "hello"}},
		{"explicit run", []string{"run", "hello"}, "run", []string{"hello"}},
		{"chat", []string{"chat", "--json"}, "chat", []string{"--json"}},
		{"doctor", []string{"doctor", "--offline"}, "doctor", []string{"--offline"}},
		{"help", []string{"help"}, "help", []string{}},
		{"help flag", []string{"--help"}, "help", []string{}},
		{"version", []string{"version"}, "version", []string{}},
		// A message that happens to start with a subcommand's name is
		// still routed to that subcommand — documented, not accidental.
		{"message shaped like a subcommand", []string{"chat", "about", "go"}, "chat", []string{"about", "go"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd, rest := route(tc.argv)
			if cmd != tc.wantCmd {
				t.Errorf("command = %q, want %q", cmd, tc.wantCmd)
			}
			if strings.Join(rest, "\x00") != strings.Join(tc.wantRest, "\x00") {
				t.Errorf("rest = %v, want %v", rest, tc.wantRest)
			}
		})
	}
}

// Every name route can return must have a handler, or dispatch panics
// on a nil map entry.
func TestEverySubcommandIsDispatchable(t *testing.T) {
	for _, name := range []string{"run", "chat", "doctor"} {
		if subcommands[name] == nil {
			t.Errorf("no handler registered for %q", name)
		}
	}
}
