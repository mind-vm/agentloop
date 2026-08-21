package sandbox

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/dop251/goja"
)

// argStrings stringifies console/log arguments the way the bare log() does:
// strings pass through; everything else is JSON-encoded (with a %v fallback).
func argStrings(args []goja.Value) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		switch v := arg.Export().(type) {
		case nil:
			out = append(out, "undefined")
		case string:
			out = append(out, v)
		default:
			if b, err := json.Marshal(v); err == nil {
				out = append(out, string(b))
			} else {
				out = append(out, fmt.Sprintf("%v", v))
			}
		}
	}
	return out
}

// emitConsole writes one console line to the log buffer (visible next turn),
// applying group indentation and an optional level tag ("warn"/"error"/…).
func (s *Sandbox) emitConsole(level string, parts ...string) {
	msg := strings.Join(parts, " ")
	if level != "" {
		msg = "[" + level + "] " + msg
	}
	if s.consoleGroup > 0 {
		msg = strings.Repeat("  ", s.consoleGroup) + msg
	}
	s.logs = append(s.logs, LogEntry{Message: msg, Time: time.Now()})
	s.Emit(Event{Kind: EventLog, Summary: msg})
}

// consoleLabel returns the first argument as a label, or def when absent.
func consoleLabel(call goja.FunctionCall, def string) string {
	if len(call.Arguments) > 0 {
		if l := call.Argument(0).String(); l != "" {
			return l
		}
	}
	return def
}

// buildConsole assembles a faithful console object: the standard developer
// surface (log/info/debug/warn/error/assert/dir/table/count/group/time/trace),
// all routed to the same log channel the model reads next turn. console.assert
// is the SOFT variant (logs, never throws) — the global assert() is the throwing
// one. Columns in console.table are sorted for deterministic eval output.
func (s *Sandbox) buildConsole(rt *goja.Runtime, logFn func(goja.FunctionCall) goja.Value) *goja.Object {
	if s.consoleCounts == nil {
		s.consoleCounts = map[string]int{}
	}
	if s.consoleTimers == nil {
		s.consoleTimers = map[string]time.Time{}
	}
	c := rt.NewObject()

	// Plain printing levels.
	_ = c.Set("log", logFn)
	_ = c.Set("info", logFn)
	_ = c.Set("debug", logFn)
	_ = c.Set("dir", logFn)
	_ = c.Set("warn", func(call goja.FunctionCall) goja.Value {
		s.emitConsole("warn", argStrings(call.Arguments)...)
		return goja.Undefined()
	})
	_ = c.Set("error", func(call goja.FunctionCall) goja.Value {
		s.emitConsole("error", argStrings(call.Arguments)...)
		return goja.Undefined()
	})
	_ = c.Set("trace", func(call goja.FunctionCall) goja.Value {
		s.emitConsole("trace", argStrings(call.Arguments)...)
		return goja.Undefined()
	})

	// assert(cond, ...msg) — soft: log if falsy, never throw.
	_ = c.Set("assert", func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) > 0 && call.Argument(0).ToBoolean() {
			return goja.Undefined()
		}
		var rest []goja.Value
		if len(call.Arguments) > 1 {
			rest = call.Arguments[1:]
		}
		s.emitConsole("error", append([]string{"Assertion failed:"}, argStrings(rest)...)...)
		return goja.Undefined()
	})

	// table(rows) — render an array of objects (or a single object) as text.
	_ = c.Set("table", func(call goja.FunctionCall) goja.Value {
		s.emitConsole("", consoleTable(call.Argument(0)))
		return goja.Undefined()
	})

	// count / countReset — keyed tallies.
	_ = c.Set("count", func(call goja.FunctionCall) goja.Value {
		label := consoleLabel(call, "default")
		s.consoleCounts[label]++
		s.emitConsole("", fmt.Sprintf("%s: %d", label, s.consoleCounts[label]))
		return goja.Undefined()
	})
	_ = c.Set("countReset", func(call goja.FunctionCall) goja.Value {
		delete(s.consoleCounts, consoleLabel(call, "default"))
		return goja.Undefined()
	})

	// time / timeEnd — real wall-clock (Go-backed).
	_ = c.Set("time", func(call goja.FunctionCall) goja.Value {
		s.consoleTimers[consoleLabel(call, "default")] = time.Now()
		return goja.Undefined()
	})
	_ = c.Set("timeEnd", func(call goja.FunctionCall) goja.Value {
		label := consoleLabel(call, "default")
		if t0, ok := s.consoleTimers[label]; ok {
			s.emitConsole("", fmt.Sprintf("%s: %dms", label, time.Since(t0).Milliseconds()))
			delete(s.consoleTimers, label)
		}
		return goja.Undefined()
	})

	// group / groupCollapsed / groupEnd — indentation nesting.
	openGroup := func(call goja.FunctionCall) goja.Value {
		if len(call.Arguments) > 0 {
			s.emitConsole("", argStrings(call.Arguments)...)
		}
		s.consoleGroup++
		return goja.Undefined()
	}
	_ = c.Set("group", openGroup)
	_ = c.Set("groupCollapsed", openGroup)
	_ = c.Set("groupEnd", func(call goja.FunctionCall) goja.Value {
		if s.consoleGroup > 0 {
			s.consoleGroup--
		}
		return goja.Undefined()
	})

	return c
}

// consoleTable renders a value as a fixed-width text table, the way a developer
// expects console.table to read: one row per array element, a column per key
// (sorted for determinism), plus an (index) column. Scalars and non-arrays fall
// back to a compact form.
func consoleTable(v goja.Value) string {
	exported := v.Export()
	rows, isArray := exported.([]interface{})
	if !isArray {
		if m, ok := exported.(map[string]interface{}); ok {
			rows = []interface{}{m}
		} else {
			b, _ := json.Marshal(exported)
			return string(b)
		}
	}

	cols := []string{}
	seen := map[string]bool{}
	hasScalar := false
	for _, r := range rows {
		if m, ok := r.(map[string]interface{}); ok {
			for k := range m {
				if !seen[k] {
					seen[k] = true
					cols = append(cols, k)
				}
			}
		} else {
			hasScalar = true
		}
	}
	sort.Strings(cols)

	cell := func(val interface{}) string {
		if s, ok := val.(string); ok {
			return s
		}
		b, _ := json.Marshal(val)
		return string(b)
	}

	if hasScalar || len(cols) == 0 {
		var b strings.Builder
		for i, r := range rows {
			fmt.Fprintf(&b, "%d | %s\n", i, cell(r))
		}
		return strings.TrimRight(b.String(), "\n")
	}

	lines := [][]string{append([]string{"(index)"}, cols...)}
	for i, r := range rows {
		m, _ := r.(map[string]interface{})
		row := []string{fmt.Sprintf("%d", i)}
		for _, c := range cols {
			if val, ok := m[c]; ok {
				row = append(row, cell(val))
			} else {
				row = append(row, "")
			}
		}
		lines = append(lines, row)
	}

	widths := make([]int, len(lines[0]))
	for _, ln := range lines {
		for j, c := range ln {
			if len(c) > widths[j] {
				widths[j] = len(c)
			}
		}
	}
	var b strings.Builder
	for li, ln := range lines {
		for j, c := range ln {
			if j > 0 {
				b.WriteString(" | ")
			}
			fmt.Fprintf(&b, "%-*s", widths[j], c)
		}
		b.WriteString("\n")
		if li == 0 {
			for j := range ln {
				if j > 0 {
					b.WriteString("-+-")
				}
				b.WriteString(strings.Repeat("-", widths[j]))
			}
			b.WriteString("\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
