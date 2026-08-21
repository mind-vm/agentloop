// Package sandbox is agentloop's JS executor — a goja sandbox the model
// writes run(args) → return turns against, one capability per registered
// Pack.
package sandbox

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/dop251/goja"
)

// LogEntry is a captured log() call retained for the current Execute
// call so we can surface the joined log lines as the return value when
// the script didn't produce one.
type LogEntry struct {
	Message string
	Time    time.Time
}

// Sandbox wraps a Goja JavaScript runtime with registered Packs.
//
// Construction order matters: New() registers core primitives first,
// then each Pack in order, then snapshots the globalThis key set as
// baseKeys. Every Execute()/ExecuteTurn() rolls globalThis back to
// baseKeys before running the next script — cross-turn working memory
// lives in the threaded `args` (ExecuteTurn) or chat history, never in
// stray globals.
type Sandbox struct {
	runtime      *goja.Runtime
	logs         []LogEntry
	helpEntries  map[string]string
	packs        []Pack
	execTimeout  time.Duration
	onEvent      OnEvent
	requireCache map[string]goja.Value // cached require() results per session
	skillCode    map[string]string     // name → JS code, looked up by require()
	policy       PolicyChecker         // nil = fail-open; see policy.go
	answered     bool                  // answer() was called during this Execute
	answerText   string                // the value passed to answer(), stringified
	// console state, scoped to the runtime's lifetime (a turn): counters for
	// console.count, start times for console.time, and the current group nesting
	// depth for console.group/groupEnd indentation.
	consoleCounts map[string]int
	consoleTimers map[string]time.Time
	consoleGroup  int
	// baseKeys is the set of globalThis keys that exist after core
	// primitives + packs are registered. Each Execute() resets any key
	// not in this set — see resetGlobals(). Stray state on globalThis
	// from one Execute should not survive into the next.
	baseKeys map[string]struct{}
}

// New creates a fresh Sandbox with core primitives and the given Packs.
//
// Core primitives (log / print / console / text / plan / step /
// parseUrl) are always registered first. Each Pack's Register function
// is then called in order to install its functions, and its
// HelpEntries are merged into the help system.
func New(packs ...Pack) *Sandbox {
	rt := goja.New()
	s := &Sandbox{
		runtime:      rt,
		helpEntries:  make(map[string]string),
		execTimeout:  120 * time.Second,
		requireCache: make(map[string]goja.Value),
		skillCode:    make(map[string]string),
	}

	// Core primitives — always present.
	s.registerCorePrimitives()

	// Register packs.
	for _, p := range packs {
		if p.Register != nil {
			p.Register(rt, s)
		}
		for name, help := range p.HelpEntries {
			s.helpEntries[name] = help
		}
		s.packs = append(s.packs, p)
	}

	// Snapshot the globalThis keys that exist after registration so
	// Execute() can roll back any user-added keys afterwards.
	s.baseKeys = snapshotGlobalKeys(rt)

	return s
}

// SetExecTimeout overrides the default 120s per-Execute wall-clock
// cap. Useful for tests (smaller) and for trusted operator scripts
// (larger). Zero disables the timeout entirely — fine for fakes.
func (s *Sandbox) SetExecTimeout(d time.Duration) {
	s.execTimeout = d
}

// snapshotGlobalKeys returns the set of enumerable keys currently on
// globalThis. Non-enumerable built-ins (Object, Array, console, ...)
// aren't in the enumerable set and never need to be reset.
func snapshotGlobalKeys(rt *goja.Runtime) map[string]struct{} {
	keys := rt.GlobalObject().Keys()
	set := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		set[k] = struct{}{}
	}
	return set
}

// resetGlobals deletes any globalThis key that wasn't present after
// pack registration. Called before each Execute so a previous run's
// `globalThis.foo = ...` doesn't leak into the next one.
func (s *Sandbox) resetGlobals() {
	if len(s.baseKeys) == 0 {
		return
	}
	global := s.runtime.GlobalObject()
	for _, k := range global.Keys() {
		if _, kept := s.baseKeys[k]; !kept {
			_ = global.Delete(k)
		}
	}
}

// registerCorePrimitives installs the always-on primitives: log,
// print, console, text, plan, step, parseUrl.
func (s *Sandbox) registerCorePrimitives() {
	rt := s.runtime

	// log / print / console — every argument is JSON-stringified
	// unless it's a string (in which case it passes through).
	logFn := func(call goja.FunctionCall) goja.Value {
		s.emitConsole("", argStrings(call.Arguments)...)
		return goja.Undefined()
	}
	_ = rt.Set("log", logFn)
	_ = rt.Set("print", logFn)
	_ = rt.Set("console", s.buildConsole(rt, logFn))

	// text — multiline string builder.
	_ = rt.Set("text", func(call goja.FunctionCall) goja.Value {
		var lines []string
		for _, arg := range call.Arguments {
			lines = append(lines, arg.String())
		}
		return rt.ToValue(strings.Join(lines, "\n"))
	})

	// assert(cond, message?) — fail the run LOUDLY when an invariant is
	// violated, instead of computing on bad data. A failed assertion throws,
	// surfacing to the model as an error event next turn (the same channel a
	// failed sql.query uses), so it self-corrects rather than silently
	// producing a wrong answer. This is the one dev affordance the loop lacked:
	// a way for the model to turn its own logic errors into recoverable signals.
	_ = rt.Set("assert", func(call goja.FunctionCall) goja.Value {
		if call.Argument(0).ToBoolean() {
			return goja.Undefined()
		}
		msg := "assertion failed"
		if len(call.Arguments) > 1 {
			if m := strings.TrimSpace(call.Argument(1).String()); m != "" {
				msg = "assertion failed: " + m
			}
		}
		panic(rt.NewGoError(fmt.Errorf("%s", msg)))
	})

	// answer — deliver the run's final result and end it. Accepts a
	// string (rendered as the answer) or any JSON value (stringified).
	// The loop reads this after Execute and terminates, so the model
	// finishes from inside its script rather than emitting a DONE marker.
	// Calling it more than once keeps the last value.
	_ = rt.Set("answer", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) == 0 {
			return goja.Undefined()
		}
		out := formatValue(call.Argument(0))
		s.answered = true
		s.answerText = out
		s.Emit(Event{Kind: EventAnswer, Summary: "answer", Result: out})
		return goja.Undefined()
	})

	// parseUrl(url) — Go-backed URL parser. Browser URL API is
	// unavailable in Goja; this is the replacement. Uses
	// ParseRequestURI so relative URLs are rejected.
	_ = rt.Set("parseUrl", func(call goja.FunctionCall) goja.Value {
		rawURL := call.Argument(0).String()
		if rawURL == "" {
			return goja.Null()
		}
		parsed, err := url.ParseRequestURI(rawURL)
		if err != nil || parsed.Host == "" {
			return goja.Null()
		}
		return rt.ToValue(map[string]any{
			"href":     parsed.String(),
			"protocol": parsed.Scheme + ":",
			"hostname": parsed.Hostname(),
			"port":     parsed.Port(),
			"pathname": parsed.Path,
			"search":   parsed.RawQuery,
			"hash":     parsed.Fragment,
		})
	})

	// Core help entries.
	s.helpEntries["log"] = "log(...args) — Output to console. Objects are JSON-serialized."
	s.helpEntries["assert"] = "assert(cond, message?) — throw if cond is falsy; the message surfaces as an error next turn so you can self-correct. Guard inputs/results instead of computing on bad data."
	s.helpEntries["console"] = "console.{log,info,debug,warn,error,dir,trace,assert,table,count,countReset,time,timeEnd,group,groupEnd} — standard debugging console; output is visible next turn. console.table(rows) prints objects as a text table; console.assert logs (does not throw)."
	s.helpEntries["text"] = "text(...lines) — Join arguments with newlines. Useful for building multiline strings."
	s.helpEntries["parseUrl"] = "parseUrl(url) — Parse an absolute URL string. Returns {href, protocol, hostname, port, pathname, search, hash} or null for relative URLs/invalid input."
}

// SystemPrompt builds the primitives documentation from all registered
// packs. Appended to the agent loop's base instructions so the LLM
// knows what's available. Pack prompts use TypeScript .d.ts format
// wrapped in a single code fence; the "context" pack (dynamic
// session data) is appended as prose after the fence.
func (s *Sandbox) SystemPrompt() string {
	var dts []string
	dts = append(dts, `// --- Core ---
// You implement: function run(args) { ... } — one per turn. Everything below is provided by the sandbox.

/** Deliver the final result and END the run — pass a string (your answer, as markdown) or a JSON value (structured data). Call it once, when you have the answer; nothing after it runs in a new turn. This is how you finish. */
declare function answer(result: string | object): void;
/** Output to console. Objects are JSON-serialized. */
declare function log(...args: any[]): void;
/** Assert an invariant. If cond is falsy, throws with message — surfacing as an error you see next turn, so you can fix it. Guard your inputs and results: assert(args.caps, "caps missing"), assert(rows.length, "no rows"). A loud failure you can recover from beats a silent wrong answer. */
declare function assert(cond: any, message?: string): void;
/** Join arguments with newlines. */
declare function text(...lines: string[]): string;
/** Parse an absolute URL string. Returns null for relative URLs or invalid input. Use instead of new URL() which is not available. */
declare function parseUrl(url: string): { href: string; protocol: string; hostname: string; port: string; pathname: string; search: string; hash: string } | null;
/** List all primitives; help(name) for details. */
declare function help(name?: string): string;
/** Standard console for debugging — every method's output is visible to YOU next turn (same channel as log). */
declare const console: {
  log(...args: any[]): void; info(...args: any[]): void; debug(...args: any[]): void;
  warn(...args: any[]): void; error(...args: any[]): void; dir(obj: any): void; trace(...args: any[]): void;
  /** Logs "Assertion failed: …" if cond is falsy and CONTINUES (does not throw). Use the global assert() when you want to STOP and self-correct. */
  assert(cond: any, ...msg: any[]): void;
  /** Print an array of objects (or one object) as a fixed-width table — the fastest way to eyeball query/extraction rows. */
  table(rows: object[] | object): void;
  count(label?: string): void; countReset(label?: string): void;
  /** Wall-clock timing in ms between time(label) and timeEnd(label). */
  time(label?: string): void; timeEnd(label?: string): void;
  group(label?: string): void; groupCollapsed(label?: string): void; groupEnd(): void;
};`)

	var prose []string
	for _, p := range s.packs {
		if p.Prompt == "" {
			continue
		}
		if p.Name == "context" {
			prose = append(prose, p.Prompt)
		} else {
			dts = append(dts, p.Prompt)
		}
	}

	var out strings.Builder
	out.WriteString("```typescript\n")
	out.WriteString(strings.Join(dts, "\n\n"))
	out.WriteString("\n```")

	if len(prose) > 0 {
		out.WriteString("\n\n")
		out.WriteString(strings.Join(prose, "\n\n"))
	}

	return out.String()
}

// SetOnEvent registers a callback that receives every sandbox event.
// Nil clears the callback. The loop uses this to relay events into
// its public RunEvent stream.
func (s *Sandbox) SetOnEvent(fn OnEvent) {
	s.onEvent = fn
}

// Emit sends an event to the callback if one is registered.
func (s *Sandbox) Emit(evt Event) {
	if s.onEvent != nil {
		s.onEvent(evt)
	}
}

// Packs returns the registered pack list (for inspection / debugging).
func (s *Sandbox) Packs() []Pack {
	return s.packs
}

// TakeAnswer reports the value passed to answer() during the most recent
// Execute and whether answer() was called. The loop uses it as a terminal
// signal: a called answer() ends the run with this value as the final
// response. State is reset at the start of each Execute.
func (s *Sandbox) TakeAnswer() (value string, ok bool) {
	return s.answerText, s.answered
}

// Execute runs JavaScript in the sandbox and returns the auto-return
// value (the last expression). Code is wrapped in an IIFE so each
// call gets its own scope. Logs from this call are surfaced as the
// return value when the script didn't produce one.
//
// Panics from host pack handlers are recovered into a sandbox error
// so a malformed exposure doesn't crash the agent goroutine.
func (s *Sandbox) Execute(code string) (result string, err error) {
	s.logs = nil
	s.answered = false
	s.answerText = ""
	s.resetGlobals()
	s.Emit(Event{Kind: EventScript, Summary: "execute", Detail: code})

	defer func() {
		if r := recover(); r != nil {
			s.runtime.ClearInterrupt()
			s.Emit(Event{Kind: EventError, Summary: "pack panic", Detail: fmt.Sprintf("%v", r)})
			err = fmt.Errorf("sandbox: recovered from pack panic: %v", r)
		}
	}()

	if s.execTimeout > 0 {
		timer := time.AfterFunc(s.execTimeout, func() {
			s.runtime.Interrupt("execution timeout exceeded")
		})
		defer timer.Stop()
	}

	wrapped := wrapIIFE(code)
	val, err := s.runtime.RunString(wrapped)
	if err != nil {
		s.runtime.ClearInterrupt()
		s.Emit(Event{Kind: EventError, Summary: "script error", Detail: err.Error()})
		return "", err
	}

	if val == nil || goja.IsUndefined(val) || goja.IsNull(val) {
		if len(s.logs) > 0 {
			var lines []string
			for _, l := range s.logs {
				lines = append(lines, l.Message)
			}
			output := strings.Join(lines, "\n")
			s.Emit(Event{Kind: EventScriptResult, Summary: "result", Result: output})
			return output, nil
		}
		s.Emit(Event{Kind: EventScriptResult, Summary: "result", Result: ""})
		return "", nil
	}

	output := formatValue(val)
	const maxOutputLen = 4000
	if len(output) > maxOutputLen {
		output = output[:maxOutputLen] + "\n... (truncated, " + fmt.Sprintf("%d", len(output)) + " chars total)"
	}
	s.Emit(Event{Kind: EventScriptResult, Summary: "result", Result: output})
	return output, nil
}

// ExecuteTurn runs one turn under the run(args)→return contract used by the
// return-threading loop. argsJSON (the previous turn's return value) is injected
// as the global `args`; if the code defines `function run(args) { ... }` it is
// called with args and its return value is captured. The captured value is
// marshalled to JSON and returned as ret for the loop to thread into the next
// turn — it is NOT serialised into the LLM-visible result. logs holds the joined
// log() output, which (plus a structural digest the loop derives from ret) is the
// only thing the model sees back. A run that defines no run() function returns
// empty ret: loose statements carry nothing forward, only their side effects
// (log/answer/emit).
func (s *Sandbox) ExecuteTurn(code string, argsJSON json.RawMessage) (logs string, ret json.RawMessage, err error) {
	s.logs = nil
	s.answered = false
	s.answerText = ""
	s.resetGlobals()
	s.Emit(Event{Kind: EventScript, Summary: "execute", Detail: code})

	defer func() {
		if r := recover(); r != nil {
			s.runtime.ClearInterrupt()
			s.Emit(Event{Kind: EventError, Summary: "pack panic", Detail: fmt.Sprintf("%v", r)})
			err = fmt.Errorf("sandbox: recovered from pack panic: %v", r)
		}
	}()

	if s.execTimeout > 0 {
		timer := time.AfterFunc(s.execTimeout, func() {
			s.runtime.Interrupt("execution timeout exceeded")
		})
		defer timer.Stop()
	}

	// Inject `args` (the prior return). Hydrate via JSON.parse for an
	// ES5.1-safe round-trip; an empty/invalid blob yields {}.
	if len(argsJSON) == 0 || string(argsJSON) == "null" {
		_ = s.runtime.Set("args", s.runtime.NewObject())
	} else if escaped, mErr := json.Marshal(string(argsJSON)); mErr == nil {
		if _, pErr := s.runtime.RunString("args = JSON.parse(" + string(escaped) + ");"); pErr != nil {
			_ = s.runtime.Set("args", s.runtime.NewObject())
		}
	} else {
		_ = s.runtime.Set("args", s.runtime.NewObject())
	}

	// Define the code, then call run(args) if the model declared it. Loose
	// scripts (no run) execute for their side effects and return nothing.
	wrapped := "(function(){\n" + code + "\nif (typeof run === 'function') { return run(args); }\n})()"
	val, runErr := s.runtime.RunString(wrapped)
	if runErr != nil {
		s.runtime.ClearInterrupt()
		s.Emit(Event{Kind: EventError, Summary: "script error", Detail: runErr.Error()})
		return s.joinedLogs(), nil, runErr
	}

	logs = s.joinedLogs()
	ret = exportJSON(val)
	s.Emit(Event{Kind: EventScriptResult, Summary: "result", Result: logs})
	return logs, ret, nil
}

// joinedLogs returns the captured log lines joined by newlines.
func (s *Sandbox) joinedLogs() string {
	if len(s.logs) == 0 {
		return ""
	}
	lines := make([]string, 0, len(s.logs))
	for _, l := range s.logs {
		lines = append(lines, l.Message)
	}
	return strings.Join(lines, "\n")
}

// exportJSON marshals a returned Goja value to JSON, or nil for
// undefined/null/unmarshalable values (e.g. a function).
func exportJSON(val goja.Value) json.RawMessage {
	if val == nil || goja.IsUndefined(val) || goja.IsNull(val) {
		return nil
	}
	b, err := json.Marshal(val.Export())
	if err != nil || len(b) == 0 || string(b) == "null" {
		return nil
	}
	return b
}

// formatValue converts a Goja value to the string the LLM sees back
// as the execution result.
func formatValue(val goja.Value) string {
	exported := val.Export()
	if exported == nil {
		return ""
	}
	if s, ok := exported.(string); ok {
		return s
	}
	if _, ok := exported.(float64); ok {
		return fmt.Sprintf("%v", exported)
	}
	if _, ok := exported.(int64); ok {
		return fmt.Sprintf("%v", exported)
	}
	if _, ok := exported.(bool); ok {
		return fmt.Sprintf("%v", exported)
	}
	b, err := json.Marshal(exported)
	if err != nil {
		return fmt.Sprintf("%v", exported)
	}
	return string(b)
}

// wrapIIFE wraps user code in an immediately-invoked function
// expression so each Execute call has its own scope. Auto-returns
// the last expression (REPL-style).
func wrapIIFE(code string) string {
	return "(function(){\n" + autoReturn(code) + "\n})()"
}

// autoReturn prepends "return" to the last expression statement so
// the IIFE returns its value. Statements that can't be returned
// (var/let/const, control flow, function/class declarations) pass
// through.
func autoReturn(code string) string {
	lines := strings.Split(code, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		trimmed := strings.TrimSpace(lines[i])
		if trimmed == "" || strings.HasPrefix(trimmed, "//") {
			continue
		}
		if isStatement(trimmed) {
			break
		}
		lines[i] = "return " + strings.TrimSpace(lines[i])
		break
	}
	return strings.Join(lines, "\n")
}

// isStatement reports whether the trimmed line is a statement that
// can't be auto-returned. Conservative on purpose — when in doubt
// don't prepend `return`, because invalid JS aborts the whole run.
func isStatement(line string) bool {
	// Multi-statement line (semicolon not at end) — too risky to
	// auto-return.
	stripped := strings.TrimRight(line, "; \t")
	if strings.Contains(stripped, ";") {
		return true
	}

	prefixes := []string{
		"return ", "return;",
		"var ", "let ", "const ",
		"if ", "if(",
		"for ", "for(",
		"while ", "while(",
		"do ", "do{",
		"switch ", "switch(",
		"try ", "try{",
		"function ", "class ",
		"throw ", "break", "continue",
	}
	for _, p := range prefixes {
		if strings.HasPrefix(line, p) {
			return true
		}
	}

	// Closing brackets are continuations of multi-line expressions.
	// `]);` closes `plan([...])`; `});` closes `foo({...})`. We can't
	// auto-return one of these — it'd produce invalid JS.
	if len(line) > 0 && (line[0] == '}' || line[0] == ')' || line[0] == ']') {
		return true
	}
	return false
}

// Logs returns the captured log entries from the last Execute call.
func (s *Sandbox) Logs() []LogEntry {
	return s.logs
}

// Runtime exposes the underlying Goja runtime. Used by packs that
// need to call rt methods directly (the markdown / require packs are
// the primary callers).
func (s *Sandbox) Runtime() *goja.Runtime {
	return s.runtime
}

// AddSkillCode registers a JS module body that the require(name)
// primitive can load. Packs that ship companion JS modules call
// this from their Register function.
func (s *Sandbox) AddSkillCode(name, code string) {
	s.skillCode[name] = code
}

// SkillCode returns the JS body registered under name, or "" when
// no such skill is registered. Used by the require pack.
func (s *Sandbox) SkillCode(name string) (string, bool) {
	code, ok := s.skillCode[name]
	return code, ok
}

// RequireCacheGet returns a previously-cached require() result, or
// nil when none. Used by the require pack to avoid re-running a
// module body within a single session.
func (s *Sandbox) RequireCacheGet(name string) (goja.Value, bool) {
	v, ok := s.requireCache[name]
	return v, ok
}

// RequireCacheSet caches a require() result for the rest of the
// session. The require pack calls this after a successful first
// load; subsequent require(name) calls return the cached value.
func (s *Sandbox) RequireCacheSet(name string, v goja.Value) {
	s.requireCache[name] = v
}

// AddHelpEntry registers a help entry. Pack Register functions can
// call this to grow the help() listing after registration without
// having to mutate the Pack struct.
func (s *Sandbox) AddHelpEntry(name, description string) {
	s.helpEntries[name] = description
}

// HelpEntry returns the help text registered under name and a
// found-flag. Used by the help pack to format help() output and by
// the skill discovery pack to format skillGet().
func (s *Sandbox) HelpEntry(name string) (string, bool) {
	v, ok := s.helpEntries[name]
	return v, ok
}

// HelpEntries returns a sorted slice of (name, description) pairs
// suitable for rendering the help() overview. The map is internal;
// the slice is a copy callers can iterate safely.
func (s *Sandbox) HelpEntries() []HelpEntry {
	names := make([]string, 0, len(s.helpEntries))
	for name := range s.helpEntries {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]HelpEntry, len(names))
	for i, name := range names {
		out[i] = HelpEntry{Name: name, Description: s.helpEntries[name]}
	}
	return out
}

// HelpEntry is one (name, description) pair returned by HelpEntries.
type HelpEntry struct {
	Name        string
	Description string
}
