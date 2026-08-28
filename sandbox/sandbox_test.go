package sandbox

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/dop251/goja"
)

// turn runs one script through ExecuteTurn with no incoming args.
func turn(t *testing.T, s *Sandbox, code string) (logs string, ret json.RawMessage) {
	t.Helper()
	logs, ret, err := s.ExecuteTurn(code, nil)
	if err != nil {
		t.Fatalf("ExecuteTurn: %v\ncode:\n%s", err, code)
	}
	return logs, ret
}

// --- the return-threading contract ----------------------------------

// The whole design rests on this: what run() returns comes back as
// JSON, and arrives as `args` next turn without passing through a
// prompt.
func TestExecuteTurnThreadsTheReturnValue(t *testing.T) {
	s := New()

	_, ret := turn(t, s, `function run(args) { return { n: 6 * 7, items: ["a", "b"] }; }`)
	if got := string(ret); got != `{"items":["a","b"],"n":42}` && got != `{"n":42,"items":["a","b"]}` {
		t.Fatalf("returned %s", got)
	}

	_, ret2, err := s.ExecuteTurn(`function run(args) { return { doubled: args.n * 2, first: args.items[0] }; }`, ret)
	if err != nil {
		t.Fatalf("second turn: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(ret2, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["doubled"] != float64(84) || got["first"] != "a" {
		t.Errorf("args did not arrive intact: %v", got)
	}
}

// An absent or unusable args blob has to become an empty object, not
// undefined — every script written for this loop dereferences args.
func TestArgsFallsBackToAnEmptyObject(t *testing.T) {
	for name, blob := range map[string]json.RawMessage{
		"nil":     nil,
		"empty":   json.RawMessage(``),
		"null":    json.RawMessage(`null`),
		"invalid": json.RawMessage(`{not json`),
	} {
		t.Run(name, func(t *testing.T) {
			s := New()
			_, ret, err := s.ExecuteTurn(`function run(args) { return { type: typeof args, keys: Object.keys(args).length }; }`, blob)
			if err != nil {
				t.Fatalf("ExecuteTurn: %v", err)
			}
			if !strings.Contains(string(ret), `"type":"object"`) || !strings.Contains(string(ret), `"keys":0`) {
				t.Errorf("args = %s, want an empty object", ret)
			}
		})
	}
}

// A script with no run() is not an error: it executes for its side
// effects and returns nothing.
func TestScriptWithoutRunExecutesForSideEffects(t *testing.T) {
	s := New()
	logs, ret := turn(t, s, `log("side effect");`)
	if !strings.Contains(logs, "side effect") {
		t.Errorf("logs = %q", logs)
	}
	if string(ret) != "" && string(ret) != "null" {
		t.Errorf("ret = %q, want nothing", ret)
	}
}

// --- answer ---------------------------------------------------------

func TestAnswerEndsTheRun(t *testing.T) {
	s := New()
	turn(t, s, `function run(args) { answer("the result"); }`)

	value, ok := s.TakeAnswer()
	if !ok {
		t.Fatal("TakeAnswer reported no answer")
	}
	if value != "the result" {
		t.Errorf("answer = %q", value)
	}
}

// Documented behaviour: calling it more than once keeps the last value.
func TestAnswerKeepsTheLastValue(t *testing.T) {
	s := New()
	turn(t, s, `function run(args) { answer("first"); answer("second"); }`)

	value, ok := s.TakeAnswer()
	if !ok || value != "second" {
		t.Errorf("answer = %q (ok=%v), want %q", value, ok, "second")
	}
}

func TestNoAnswerReportsNone(t *testing.T) {
	s := New()
	turn(t, s, `function run(args) { return 1; }`)
	if _, ok := s.TakeAnswer(); ok {
		t.Error("a run that never called answer() should report none")
	}
}

// A turn that answers must not leave that answer visible to the next
// one, or a later turn inherits a conclusion it never reached.
func TestAnswerDoesNotSurviveIntoTheNextTurn(t *testing.T) {
	s := New()
	turn(t, s, `function run(args) { answer("from the first turn"); }`)
	if _, ok := s.TakeAnswer(); !ok {
		t.Fatal("first turn should have answered")
	}
	turn(t, s, `function run(args) { return 1; }`)
	if value, ok := s.TakeAnswer(); ok {
		t.Errorf("second turn reported an answer of %q", value)
	}
}

// --- isolation between turns ----------------------------------------

// Each turn starts from the same globals the packs registered. Without
// this a script's stray assignment becomes state the next turn reads as
// if it had computed it.
func TestGlobalsDoNotLeakBetweenTurns(t *testing.T) {
	s := New()
	turn(t, s, `globalThis.leaked = "from the first turn"; var alsoLeaked = 1;`)

	_, ret := turn(t, s, `function run(args) { return { leaked: typeof globalThis.leaked }; }`)
	if !strings.Contains(string(ret), `"leaked":"undefined"`) {
		t.Errorf("a global survived the turn boundary: %s", ret)
	}
}

func TestLogsAreClearedBetweenTurns(t *testing.T) {
	s := New()
	turn(t, s, `log("first");`)
	logs, _ := turn(t, s, `log("second");`)

	if strings.Contains(logs, "first") {
		t.Errorf("logs from a previous turn carried over: %q", logs)
	}
	if !strings.Contains(logs, "second") {
		t.Errorf("logs = %q", logs)
	}
}

// --- failure modes --------------------------------------------------

func TestScriptErrorIsReturnedWithItsLogs(t *testing.T) {
	s := New()
	logs, ret, err := s.ExecuteTurn(`function run(args) { log("got this far"); throw new Error("boom"); }`, nil)
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error = %v", err)
	}
	// The logs leading up to the failure are how the model works out
	// what happened, so they survive the error.
	if !strings.Contains(logs, "got this far") {
		t.Errorf("logs = %q", logs)
	}
	if ret != nil {
		t.Errorf("ret = %s, want nothing", ret)
	}
}

// A pack that panics must not take the process with it. The run fails,
// the sandbox stays usable.
func TestPackPanicIsRecovered(t *testing.T) {
	exploding := Pack{
		Name: "exploding",
		Register: func(rt *goja.Runtime, s *Sandbox) {
			_ = rt.Set("explode", func(goja.FunctionCall) goja.Value {
				panic("a pack panicked")
			})
		},
	}
	s := New(exploding)

	_, _, err := s.ExecuteTurn(`function run(args) { return explode(); }`, nil)
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "panic") {
		t.Errorf("error = %v", err)
	}

	// Still usable afterwards.
	if _, ret := turn(t, s, `function run(args) { return 1; }`); string(ret) != "1" {
		t.Errorf("the sandbox did not survive the panic: %s", ret)
	}
}

// goja has no pre-emption of its own; the timeout works by interrupting
// the runtime, and without it a runaway loop is unbounded.
func TestExecutionTimeoutInterruptsARunawayLoop(t *testing.T) {
	s := New()
	s.SetExecTimeout(100 * time.Millisecond)

	start := time.Now()
	_, _, err := s.ExecuteTurn(`function run(args) { while (true) {} }`, nil)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("an infinite loop should not complete")
	}
	if elapsed > 10*time.Second {
		t.Errorf("the timeout took %s to take effect", elapsed)
	}
}

// The interrupt has to be cleared afterwards, or every later turn dies
// instantly on a signal meant for one that already ended.
func TestSandboxIsUsableAfterATimeout(t *testing.T) {
	s := New()
	s.SetExecTimeout(100 * time.Millisecond)
	if _, _, err := s.ExecuteTurn(`function run(args) { while (true) {} }`, nil); err == nil {
		t.Fatal("want a timeout")
	}

	s.SetExecTimeout(5 * time.Second)
	if _, ret := turn(t, s, `function run(args) { return "recovered"; }`); string(ret) != `"recovered"` {
		t.Errorf("ret = %s", ret)
	}
}

// --- events ---------------------------------------------------------

func TestTurnEmitsScriptAndResultEvents(t *testing.T) {
	var kinds []EventKind
	s := New()
	s.SetOnEvent(func(e Event) { kinds = append(kinds, e.Kind) })

	turn(t, s, `function run(args) { return 1; }`)

	if len(kinds) < 2 || kinds[0] != EventScript || kinds[len(kinds)-1] != EventScriptResult {
		t.Errorf("kinds = %v, want script … script_result", kinds)
	}
}

func TestFailedTurnEmitsAnErrorEvent(t *testing.T) {
	var kinds []EventKind
	s := New()
	s.SetOnEvent(func(e Event) { kinds = append(kinds, e.Kind) })

	if _, _, err := s.ExecuteTurn(`function run(args) { throw new Error("boom"); }`, nil); err == nil {
		t.Fatal("want an error")
	}
	var sawError bool
	for _, k := range kinds {
		if k == EventError {
			sawError = true
		}
	}
	if !sawError {
		t.Errorf("kinds = %v, want an error event", kinds)
	}
}

// --- auto-return heuristics -----------------------------------------

// isStatement decides whether Execute may prepend "return" to a line.
// It is deliberately conservative: a wrong "yes" produces invalid JS
// and loses the whole run, while a wrong "no" only loses the value.
func TestIsStatement(t *testing.T) {
	statements := []string{
		"var x = 1", "let x = 1", "const x = 1",
		"if (x) {", "if(x) {", "for (;;) {", "for(;;) {",
		"while (x) {", "while(x) {", "do {", "switch (x) {",
		"try {", "function f() {", "class C {",
		"throw new Error()", "break", "continue", "return x", "return;",
		"}", ")", "]", "});", "]);",
		"a(); b()", // two statements on one line
	}
	for _, line := range statements {
		if !isStatement(line) {
			t.Errorf("isStatement(%q) = false, want true — prepending return here would produce invalid JS", line)
		}
	}

	expressions := []string{
		"x", "x + 1", "foo()", "foo(1, 2)", `{ a: 1 }`,
		"x.y.z", "[1, 2, 3]", "await1", // a name merely starting with a keyword
		"variable", "constant", "iffy",
	}
	for _, line := range expressions {
		if isStatement(line) {
			t.Errorf("isStatement(%q) = true, want false", line)
		}
	}
}

func TestAutoReturn(t *testing.T) {
	cases := map[string]struct{ in, want string }{
		"bare expression":       {"foo()", "return foo()"},
		"last line of several":  {"var a = 1\na + 1", "var a = 1\nreturn a + 1"},
		"trailing blank lines":  {"foo()\n\n", "return foo()\n\n"},
		"trailing comment":      {"foo()\n// done", "return foo()\n// done"},
		"already returns":       {"return foo()", "return foo()"},
		"ends in a declaration": {"const a = 1", "const a = 1"},
		"ends in a closing brace": {
			"function f() {\n  return 1\n}", "function f() {\n  return 1\n}",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := autoReturn(tc.in); got != tc.want {
				t.Errorf("autoReturn(%q)\n got %q\nwant %q", tc.in, got, tc.want)
			}
		})
	}
}

// Execute is the other entry point, and its whole value is that a bare
// expression comes back rather than vanishing.
func TestExecuteReturnsTheLastExpression(t *testing.T) {
	s := New()
	got, err := s.Execute("var a = 20\na + 22")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(got, "42") {
		t.Errorf("Execute = %q, want it to carry 42", got)
	}
}
