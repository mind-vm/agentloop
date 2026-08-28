package sandbox

import "testing"

// TestHasTopLevelSemicolon covers the distinction the auto-return
// heuristic rests on: a semicolon that separates two statements versus
// one that lives inside an expression.
func TestHasTopLevelSemicolon(t *testing.T) {
	cases := []struct {
		name string
		line string
		want bool
	}{
		// Genuine statement separators.
		{"two calls", `foo(); bar()`, true},
		{"assignment then call", `x = 1; log(x)`, true},

		// Semicolons that belong to an expression.
		{"function body", `marks.map(function (m) { return m.id; })`, false},
		{"nested function bodies", `a(function () { b(function () { c(); }); })`, false},
		{"object literal in call", `plan({ a: 1 })`, false},
		{"string literal", `parts.join(";")`, false},
		{"string with escape", `x.split("a\";b")`, false},
		{"template literal", "tag(`a;b`)", false},
		{"single quotes", `x.split('a;b')`, false},

		// Comments.
		{"trailing line comment", `value() // done; really`, false},
		{"statement before comment", `a(); b() // x`, true},
		{"block comment", `a(/* one; two */ b)`, false},

		// Conservative fallbacks: the line does not stand on its own.
		{"unterminated string", "x(\"abc", true},
		{"unterminated block comment", `a(/* one; two`, true},

		// Unbalanced closers must not drive the depth negative and
		// swallow a later separator.
		{"closer then separator", `}); foo()`, true},

		{"no semicolon at all", `browser.text("h1")`, false},
		{"empty", ``, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := hasTopLevelSemicolon(c.line); got != c.want {
				t.Errorf("hasTopLevelSemicolon(%q) = %v, want %v", c.line, got, c.want)
			}
		})
	}
}

// TestIsStatement_BlockContinuations covers the lines that continue a
// block opened earlier. They are not expressions, and prepending
// `return` to one is a syntax error that takes the whole run with it —
// so they need naming explicitly now that a semicolon nested inside
// their braces no longer marks them as statements.
func TestIsStatement_BlockContinuations(t *testing.T) {
	for _, line := range []string{
		`else { log("x"); }`,
		`else log("x")`,
		`finally { cleanup(); }`,
		`case 2: log("two"); break`,
		`default: log("other")`,
		`} else {`,
		`});`,
	} {
		if !isStatement(line) {
			t.Errorf("isStatement(%q) = false, want true", line)
		}
	}
}

// TestAutoReturn_ChainedFinalExpression is the regression this fix is
// for: a final line whose only semicolon sits inside an inline function
// body used to be read as two statements, so the IIFE returned nothing
// and Execute reported an empty result for a script that computed one.
func TestAutoReturn_ChainedFinalExpression(t *testing.T) {
	cases := []struct {
		name string
		code string
		want string
	}{
		{
			name: "function body on the final line",
			code: "var xs = [1, 2];\nxs.map(function (x) { return x * 2; }).join(\",\");",
			want: "2,4",
		},
		{
			name: "semicolon inside a string",
			code: "var parts = [\"a\", \"b\"];\nparts.join(\";\");",
			want: "a;b",
		},
		{
			name: "statements stay statements",
			code: "var x = 1;\nlog(\"one\"); log(\"two\")",
			want: "one\ntwo",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := New()
			got, err := s.Execute(c.code)
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			if got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}
