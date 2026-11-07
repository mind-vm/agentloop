package sandbox

import (
	"slices"
	"testing"

	"github.com/dop251/goja"
)

// TestAutoReturn_LastLineContinuesAnEarlierOne covers a final line that
// is the tail of a statement opened on an earlier line. Prefixing it
// injects `return` into the middle of that statement, which either
// breaks the script or — inside a template literal — silently changes
// the text a pack receives. Such code must come back unchanged.
func TestAutoReturn_LastLineContinuesAnEarlierOne(t *testing.T) {
	unchanged := map[string]string{
		"template spanning lines":           "f(`x\ny`)",
		"template line with a comment mark": "f(`a\nb // c`)",
		"template ending on its own line":   "f(`a\nb\n`)",
		"template line that looks blank":    "f(`a\n\n`)",
		"template in a substitution":        "f(`a ${g(`b\nc`)} d\ne`)",
		"multi-line call args":              "f(1,\n2)",
		"multi-line call, balanced quotes":  "f(`a`,\n`b`)",
		"multi-line object argument":        "f({\n a: 1,\n b: 2 })",
		"multi-line array":                  "[1,\n 2]",
		"string continued by backslash":     "f(\"a\\\nb\")",
		"block comment closing last":        "foo()\n/* note\n*/",
	}
	for name, code := range unchanged {
		t.Run(name, func(t *testing.T) {
			if got := autoReturn(code); got != code {
				t.Errorf("autoReturn(%q)\n got %q\nwant it unchanged", code, got)
			}
		})
	}

	// The scanner must not be so cautious that it stops returning a
	// final expression that follows a finished multi-line one.
	returned := map[string]struct{ in, want string }{
		"after a multi-line template": {"x = `a\nb`\nx", "x = `a\nb`\nreturn x"},
		"after a multi-line call":     {"f(1,\n2)\ng()", "f(1,\n2)\nreturn g()"},
		"after a substitution":        {"x = `${a}\n`\nx", "x = `${a}\n`\nreturn x"},
		"after a block comment":       {"/* a\nb */\nfoo()", "/* a\nb */\nreturn foo()"},
		"bracket inside a string":     {"x = \"(\"\nx", "x = \"(\"\nreturn x"},
		"bracket inside a comment":    {"a() // (\nb()", "a() // (\nreturn b()"},
	}
	for name, tc := range returned {
		t.Run(name, func(t *testing.T) {
			if got := autoReturn(tc.in); got != tc.want {
				t.Errorf("autoReturn(%q)\n got %q\nwant %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestExecute_MultiLineTemplateReachesThePackIntact drives the bug
// through Execute: a pack handed a multi-line template literal must see
// the text the script wrote, not one with `return` spliced into it.
func TestExecute_MultiLineTemplateReachesThePackIntact(t *testing.T) {
	cases := []struct {
		code string
		want []string
	}{
		{"f(`x\ny`)", []string{"x\ny"}},
		{"f(`x\ny // z`)", []string{"x\ny // z"}},
		{"f(`x`,\n`y`)", []string{"x", "y"}},
	}
	for _, tc := range cases {
		var got []string
		s := New(Pack{Name: "rec", Register: func(rt *goja.Runtime, _ *Sandbox) {
			_ = rt.Set("f", func(args ...string) { got = args })
		}})
		if _, err := s.Execute(tc.code); err != nil {
			t.Errorf("Execute(%q): %v", tc.code, err)
			continue
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("Execute(%q): f received %q, want %q", tc.code, got, tc.want)
		}
	}
}
