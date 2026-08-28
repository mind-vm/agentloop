package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mind-vm/agentloop"
	"github.com/mind-vm/agentloop/agentloopsql"
	"github.com/mind-vm/agentloop/projectctx"
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
	dbPath := filepath.Join(t.TempDir(), "sessions.db")
	t.Setenv("AGENTLOOP_DB", dbPath)

	var o options
	if err := o.resolve(); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if o.cwd == "" {
		t.Error("cwd should default to the working directory")
	}
	if o.db != dbPath {
		t.Errorf("db = %q, want AGENTLOOP_DB %q", o.db, dbPath)
	}
}

// An ephemeral run writes nowhere, so it must not silently acquire a
// database path it would then be asked to create.
func TestOptionsResolveEphemeralTakesNoDatabase(t *testing.T) {
	t.Setenv("AGENTLOOP_DB", filepath.Join(t.TempDir(), "sessions.db"))
	o := options{ephemeral: true}
	if err := o.resolve(); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if o.db != "" {
		t.Errorf("db = %q, want empty for an ephemeral run", o.db)
	}
}

func TestOptionsResolveRejectsNonsense(t *testing.T) {
	cases := map[string]options{
		"json-stream without json": {jsonStream: true},
		"negative max steps":       {maxSteps: -1},
		"negative history window":  {historyWindow: -5},
		"negative timeout":         {timeout: -1},
		// --session names a session and --continue picks one; asking for
		// both is a contradiction, not a preference to be guessed at.
		"session with continue":   {session: "s-1", resume: true},
		"continue with ephemeral": {resume: true, ephemeral: true},
		"db with ephemeral":       {db: "x.db", ephemeral: true},
	}
	for name, o := range cases {
		t.Run(name, func(t *testing.T) {
			if err := o.resolve(); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

// --- session selection ----------------------------------------------

func TestResolveStoresEphemeralGeneratesAnId(t *testing.T) {
	o := options{ephemeral: true}
	if err := o.resolve(); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	_, _, store, id, err := resolveStores(&o)
	if err != nil {
		t.Fatalf("resolveStores: %v", err)
	}
	if store != nil {
		t.Error("an ephemeral run must not open a database")
	}
	if id == "" {
		t.Error("want a generated session id")
	}
}

func TestResolveStoresHonoursAnExplicitSession(t *testing.T) {
	o := freshDBOptions(t)
	o.session = "chosen"
	_, _, store, id, err := resolveStores(&o)
	if err != nil {
		t.Fatalf("resolveStores: %v", err)
	}
	defer store.Close()
	if id != "chosen" {
		t.Errorf("id = %q, want %q", id, "chosen")
	}
}

// --continue with nothing stored must say so rather than quietly
// starting a session the user did not ask for.
func TestResolveStoresContinueWithNothingStored(t *testing.T) {
	o := freshDBOptions(t)
	o.resume = true
	_, _, store, _, err := resolveStores(&o)
	if store != nil {
		store.Close()
	}
	if err == nil {
		t.Fatal("want an error when there is nothing to continue")
	}
	if !strings.Contains(err.Error(), "nothing to continue") {
		t.Errorf("error should explain itself, got %q", err)
	}
}

func TestResolveStoresContinuePicksTheMostRecent(t *testing.T) {
	o := freshDBOptions(t)

	seed, err := agentloopsql.Open(o.db, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, id := range []string{"older", "newer"} {
		if err := seed.Append(context.Background(), agentloop.RunStep{
			SessionID: id, StepType: "user", Content: "hi", ToolArgs: []byte("{}"),
		}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	seed.Close()

	o.resume = true
	_, _, store, id, err := resolveStores(&o)
	if err != nil {
		t.Fatalf("resolveStores: %v", err)
	}
	defer store.Close()
	if id != "newer" {
		t.Errorf("id = %q, want the most recently updated session", id)
	}
}

// freshDBOptions returns resolved options pointing at an empty database
// in the test's own temp directory.
func freshDBOptions(t *testing.T) options {
	t.Helper()
	o := options{db: filepath.Join(t.TempDir(), "sessions.db")}
	if err := o.resolve(); err != nil {
		t.Fatalf("resolve: %v", err)
	}
	return o
}

func TestOptionsBindParsesFlags(t *testing.T) {
	var o options
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(&bytes.Buffer{})
	o.bind(fs)

	err := fs.Parse([]string{
		"--model", "gpt-4o", "--max-steps", "7", "--timeout", "90s",
		"--history-window", "12", "--context-window", "8192",
		"--session", "s-1", "--json", "hello", "world",
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if o.model != "gpt-4o" || o.maxSteps != 7 || o.timeout.String() != "1m30s" ||
		o.historyWindow != 12 || o.contextWindow != 8192 || o.session != "s-1" || !o.asJSON {
		t.Fatalf("flags not bound as expected: %+v", o)
	}
	// Everything after the flags is the message, left for resolveMessage.
	if got := strings.Join(fs.Args(), " "); got != "hello world" {
		t.Fatalf("positional args = %q, want %q", got, "hello world")
	}
}

// --- context settings -----------------------------------------------

// The flag's whole point: one number reaches every context setting,
// including the two Profile.Apply cannot write itself.
func TestContextWindowSizesEverything(t *testing.T) {
	const window = 8192
	want := agentloop.ProfileFor(window)

	o := options{contextWindow: window}
	cfg := agentloop.Config{HistoryWindow: o.historyWindow}
	o.applyContext(agentloop.ProfileFor(o.contextWindow), &cfg, nil)

	if cfg.ContextBudget.MaxTokens != window {
		t.Errorf("ContextBudget.MaxTokens = %d, want %d", cfg.ContextBudget.MaxTokens, window)
	}
	if cfg.HistoryWindow != want.HistoryWindow {
		t.Errorf("HistoryWindow = %d, want the profile's %d", cfg.HistoryWindow, want.HistoryWindow)
	}
	sc, ok := cfg.Compactor.(*agentloop.SummarizingCompactor)
	if !ok {
		t.Fatalf("Compactor = %T, want a *SummarizingCompactor installed for the profile to configure", cfg.Compactor)
	}
	if sc.Trigger != want.CompactTrigger || sc.Keep != want.CompactKeep {
		t.Errorf("compactor bounds = %d/%d, want the profile's %d/%d",
			sc.Trigger, sc.Keep, want.CompactTrigger, want.CompactKeep)
	}
	if sc.Budget.MaxTokens != want.SummarizeBudget.MaxTokens {
		t.Errorf("compactor budget = %d, want the profile's %d", sc.Budget.MaxTokens, want.SummarizeBudget.MaxTokens)
	}
	// The two the profile hands back rather than writing: they are read
	// at the call site, so this only asserts they are non-zero to hold on
	// to — newSession is what puts them on the builder and the loader.
	if want.MaxLogBytes <= 0 || want.ProjectInlineBytes <= 0 {
		t.Errorf("profile has nothing to pass on: MaxLogBytes=%d ProjectInlineBytes=%d",
			want.MaxLogBytes, want.ProjectInlineBytes)
	}
}

// An explicit --history-window is a number the operator typed; the
// profile's is one derived on their behalf. The typed one wins, which
// means the profile has to be applied first and then overridden.
func TestExplicitHistoryWindowBeatsTheProfile(t *testing.T) {
	o := options{contextWindow: 8192, historyWindow: 7}
	cfg := agentloop.Config{HistoryWindow: o.historyWindow}
	o.applyContext(agentloop.ProfileFor(o.contextWindow), &cfg, nil)

	if cfg.HistoryWindow != 7 {
		t.Fatalf("HistoryWindow = %d, want the explicit 7 rather than the profile's %d",
			cfg.HistoryWindow, agentloop.ProfileFor(8192).HistoryWindow)
	}
	// The rest of the profile still applies — one override is not a
	// reason to discard the other three settings.
	if cfg.ContextBudget.MaxTokens != 8192 {
		t.Errorf("ContextBudget.MaxTokens = %d, want 8192", cfg.ContextBudget.MaxTokens)
	}
	if cfg.Compactor == nil {
		t.Error("no compactor installed")
	}
}

// Without the flag the CLI must behave exactly as it did before it
// existed: no budget, no compactor, no touched history window.
func TestNoContextWindowLeavesTheDefaults(t *testing.T) {
	o := options{historyWindow: 12}
	cfg := agentloop.Config{HistoryWindow: o.historyWindow}
	o.applyContext(agentloop.ProfileFor(o.contextWindow), &cfg, nil)

	if cfg.ContextBudget.MaxTokens != 0 {
		t.Errorf("ContextBudget.MaxTokens = %d, want it left disabled", cfg.ContextBudget.MaxTokens)
	}
	if cfg.Compactor != nil {
		t.Errorf("Compactor = %T, want none installed without a stated window", cfg.Compactor)
	}
	if cfg.HistoryWindow != 12 {
		t.Errorf("HistoryWindow = %d, want the flag's 12", cfg.HistoryWindow)
	}
}

// A compactor already on the Config is the caller's choice; the profile
// configures it if it can, but never replaces it.
func TestContextWindowKeepsAConfiguredCompactor(t *testing.T) {
	mine := &agentloop.SummarizingCompactor{Model: "small-summarizer"}
	o := options{contextWindow: 8192}
	cfg := agentloop.Config{Compactor: mine}
	o.applyContext(agentloop.ProfileFor(o.contextWindow), &cfg, nil)

	if cfg.Compactor != agentloop.Compactor(mine) {
		t.Fatalf("Compactor = %#v, want the one already configured", cfg.Compactor)
	}
	if mine.Model != "small-summarizer" {
		t.Errorf("Model = %q, want the caller's choice untouched", mine.Model)
	}
}

func TestNegativeContextWindowIsRejected(t *testing.T) {
	t.Setenv("AGENTLOOP_DB", filepath.Join(t.TempDir(), "sessions.db"))
	o := options{contextWindow: -1}
	if err := o.resolve(); err == nil {
		t.Fatal("want an error for a negative --context-window")
	}
}

// A stated window bounds the project instructions too: they are excerpted
// rather than inlined whole, and projectGet is offered for the rest.
// Unbounded, they would sit in a system prompt the budget trimmer is not
// allowed to touch.
func TestContextWindowExcerptsProjectInstructions(t *testing.T) {
	docs := []projectctx.Doc{{
		Name:    "AGENTS.md",
		Content: strings.Repeat("x", 40_000),
		Inline:  strings.Repeat("x", 1792),
	}}

	sized := renderProjectContext(docs, agentloop.ProfileFor(4096))
	if len(sized) >= 40_000 {
		t.Errorf("rendered %d bytes against a 4k window — the whole file went in", len(sized))
	}
	if !strings.Contains(sized, "projectGet") {
		t.Error("excerpted instructions with no pointer at the rest")
	}

	whole := renderProjectContext(docs, agentloop.Profile{})
	if !strings.Contains(whole, docs[0].Content) {
		t.Error("without a stated window the file must still go in whole")
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
