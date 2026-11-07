package agentloop

import (
	"errors"
	"strings"
	"testing"

	"github.com/dop251/goja"

	"github.com/mind-vm/thinking-core/sandbox"
)

// TestSanitizeErrorForModel_RealGojaThrow runs the shape a pack actually
// produces rather than a hand-written string: the table above encodes what
// goja was believed to emit, and that belief is what kept a pack's list of
// problems from ever reaching the model.
func TestSanitizeErrorForModel_RealGojaThrow(t *testing.T) {
	msg := "ui: 2 problems. Fix and call ui() again:\n  - line 1: unknown component Foo\n  - line 3: missing prop"
	sb := sandbox.New(sandbox.Pack{Name: "probe", Register: func(rt *goja.Runtime, _ *sandbox.Sandbox) {
		_ = rt.Set("ui", func(goja.FunctionCall) goja.Value { panic(rt.NewGoError(errors.New(msg))) })
	}})
	for _, code := range []string{"ui()", "function f() { ui() }\nf()"} {
		_, _, err := sb.ExecuteTurn(code, nil)
		if err == nil {
			t.Fatalf("%q: expected an error", code)
		}
		if got := sanitizeErrorForModel(err.Error()); got != msg {
			t.Errorf("%q via Error():\n got %q\nwant %q", code, got, msg)
		}
		var ex *goja.Exception
		if errors.As(err, &ex) {
			if got := sanitizeErrorForModel(ex.String()); got != msg {
				t.Errorf("%q via String():\n got %q\nwant %q", code, got, msg)
			}
		}
	}
}

func TestSanitizeErrorForModel(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "strips sandbox and GoError prefixes",
			in:   "sandbox: GoError: fetch: dial tcp: i/o timeout",
			want: "fetch: dial tcp: i/o timeout",
		},
		{
			name: "drops a native stack frame",
			in:   "sandbox: GoError: fetch: boom at agentloop/sandbox.doFetchRequest.func1 (native)",
			want: "fetch: boom",
		},
		{
			name: "drops an <eval> stack frame",
			in:   "ReferenceError: foo is not defined at <eval>:3:1(4)",
			want: "ReferenceError: foo is not defined",
		},
		{
			name: "drops frame lines after a one-line message",
			in:   "TypeError: Cannot read property 'x' of undefined\n    at <eval>:2:5(2)\n    at native",
			want: "TypeError: Cannot read property 'x' of undefined",
		},
		{
			name: "plain message with no plumbing is untouched",
			in:   "assert: rows.length is required",
			want: "assert: rows.length is required",
		},
		{
			name: "a message holding ' at ' keeps it, up to the frame",
			in:   "GoError: edit: a Line does not stand at the top of a Quote; there stand (Note | Table | Task) at github.com/x/assist.(*pack).edit-fm (native)",
			want: "edit: a Line does not stand at the top of a Quote; there stand (Note | Table | Task)",
		},
		{
			name: "an <eval> frame after a message holding ' at '",
			in:   "Error: look at it first at <eval>:3:1(4)",
			want: "Error: look at it first",
		},
		{
			name: "keeps a multi-line message, dropping the inline frame on its last line",
			in:   "GoError: ui: 2 problems. Fix and call ui() again:\n  - line 1: unknown component Foo\n  - line 3: missing prop at root at github.com/x/ext.(*pack).ui-fm (native)",
			want: "ui: 2 problems. Fix and call ui() again:\n  - line 1: unknown component Foo\n  - line 3: missing prop at root",
		},
		{
			name: "keeps a multi-line message, dropping Exception.String() frame lines",
			in:   "GoError: ui: 1 problem:\n  - bad\n\tat github.com/x/ext.(*pack).ui-fm (native)\n\tat f (<eval>:2:17(2))\n\tat <eval>:4:2(7)\n",
			want: "ui: 1 problem:\n  - bad",
		},
		{
			name: "a message line that begins with 'at' is content, not a frame",
			in:   "Error: rows rejected:\n  at least one row is required at <eval>:2:7(3)",
			want: "Error: rows rejected:\n  at least one row is required",
		},
		{
			name: "a real ' at ' with no frame marker is left as content",
			in:   "look at this value",
			want: "look at this value",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sanitizeErrorForModel(tc.in); got != tc.want {
				t.Errorf("sanitizeErrorForModel(%q):\n got %q\nwant %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRetryHintForError(t *testing.T) {
	tests := []struct {
		name       string
		in         string
		wantSubstr string // "" means no hint expected
	}{
		{
			name:       "context deadline exceeded",
			in:         "fetch: context deadline exceeded",
			wantSubstr: "transient timeout",
		},
		{
			name:       "timed out",
			in:         "http.get: request timed out",
			wantSubstr: "transient timeout",
		},
		{
			name:       "ReferenceError",
			in:         "ReferenceError: fooBar is not defined",
			wantSubstr: "Sandbox API",
		},
		{
			name:       "TypeError of undefined",
			in:         "TypeError: Cannot read properties of undefined (reading 'x')",
			wantSubstr: "return shape",
		},
		{
			name:       "unrecognised error has no hint",
			in:         "assert: rows.length is required",
			wantSubstr: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := retryHintForError(tc.in)
			if tc.wantSubstr == "" {
				if got != "" {
					t.Errorf("retryHintForError(%q) = %q, want no hint", tc.in, got)
				}
				return
			}
			if !strings.Contains(got, tc.wantSubstr) {
				t.Errorf("retryHintForError(%q) = %q, want it to contain %q", tc.in, got, tc.wantSubstr)
			}
		})
	}
}

// TestDescribeScriptError: the location is the model's own line, not the
// wrapper's, and the two common literal failures carry their hint.
func TestDescribeScriptError(t *testing.T) {
	tests := []struct {
		name, raw, code            string
		wantMsg, wantLoc, wantHint string
	}{
		{
			name:     "backtick closes a template literal",
			raw:      "SyntaxError: SyntaxError: (anonymous): Line 3:15 Unexpected identifier",
			code:     "const a = 1;\nconst b = `x `y` z`;\nlog(b)",
			wantMsg:  "Line 2:15",
			wantLoc:  "at line 2 of your script:\n  const b = `x `y` z`;\n                ^",
			wantHint: "blocks[0]",
		},
		{
			name:     "unterminated literal",
			raw:      "SyntaxError: SyntaxError: (anonymous): Line 5:5 Unexpected end of input (and 3 more errors)",
			code:     "const a = `unterminated\nlog(a)",
			wantLoc:  "at the end of your script",
			wantHint: "still open",
		},
		{
			name:    "runtime error",
			raw:     "TypeError: Cannot read property 'x' of undefined at <eval>:4:7(7)",
			code:    "const a = 1;\nconst o = undefined;\nlog(o.x)",
			wantMsg: "TypeError: Cannot read property 'x' of undefined",
			wantLoc: "at line 3 of your script:\n  log(o.x)",
		},
		{
			name:    "no location",
			raw:     "fetch: dial tcp: i/o timeout",
			code:    "fetch('x')",
			wantMsg: "fetch: dial tcp: i/o timeout",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msg, loc, hint := describeScriptError(tc.raw, tc.code, 0)
			if !strings.Contains(msg, tc.wantMsg) {
				t.Errorf("message = %q, want %q in it", msg, tc.wantMsg)
			}
			if tc.wantLoc == "" && loc != "" || !strings.Contains(loc, tc.wantLoc) {
				t.Errorf("location = %q, want %q", loc, tc.wantLoc)
			}
			if tc.wantHint == "" && hint != "" || !strings.Contains(hint, tc.wantHint) {
				t.Errorf("hint = %q, want %q in it", hint, tc.wantHint)
			}
		})
	}
}
