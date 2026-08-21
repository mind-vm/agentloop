package agentloop

import "testing"

// Adversarial coverage for the pure parsers. These are the cheapest,
// highest-leverage tests in the package — the whole protocol's correctness
// rests on ExtractJSBlock / ExtractDoneMarker classifying model output
// correctly, and the model output is adversarial by nature (truncation,
// stray fences, CRLF from some providers, markers buried in prose).

func TestExtractJSBlock_Adversarial(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "trailing prose after the closing fence is ignored",
			in:   "```js\ndata.x = 1\n```\nNow I'll explain what I did.",
			want: "data.x = 1",
		},
		{
			name: "unterminated fence is not a block",
			in:   "```js\ndata.x = 1\n(no closing fence)",
			want: "",
		},
		{
			name: "first of multiple fences wins",
			in:   "```js\nfirst\n```\nthen\n```js\nsecond\n```",
			want: "first",
		},
		{
			name: "CRLF line endings inside the fence",
			in:   "```js\r\ndata.x = 1\r\n```",
			want: "data.x = 1",
		},
		{
			name: "leading prose before the fence",
			in:   "Sure, here's the code:\n```javascript\nrun()\n```",
			want: "run()",
		},
		{
			// Defensive/known limitation: a token after the language on the
			// fence line isn't recognised. The model emits a clean
			// "```javascript\n" in practice; documenting the boundary.
			name: "trailing token on the fence line is not recognised",
			in:   "```js extra\ndata.x = 1\n```",
			want: "",
		},
		{
			name: "empty fenced block",
			in:   "```js\n\n```",
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExtractJSBlock(tc.in); got != tc.want {
				t.Errorf("ExtractJSBlock(%q): got %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestExtractDoneMarker_Adversarial(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantDone  bool
		wantFinal string
	}{
		{
			name:     "lowercase done is not the marker",
			in:       "done\nthe answer",
			wantDone: false,
		},
		{
			name:     "DONE at the end of a prose line does not terminate",
			in:       "I am almost DONE\nstill working",
			wantDone: false,
		},
		{
			name:      "DONE followed by a multi-line answer captures all of it",
			in:        "DONE\nStep 1: foo\nStep 2: bar",
			wantDone:  true,
			wantFinal: "Step 1: foo\nStep 2: bar",
		},
		{
			name:      "answer after DONE may itself contain a code fence",
			in:        "DONE\nHere is the code:\n```js\nx\n```",
			wantDone:  true,
			wantFinal: "Here is the code:\n```js\nx\n```",
		},
		{
			name:      "CRLF after the marker",
			in:        "DONE\r\nThe answer.",
			wantDone:  true,
			wantFinal: "The answer.",
		},
		{
			name:      "DONE on a later line still terminates",
			in:        "Let me wrap up.\nDONE\nFinal answer.",
			wantDone:  true,
			wantFinal: "Final answer.",
		},
		{
			name:      "indented DONE with trailing spaces",
			in:        "   DONE   \nReply",
			wantDone:  true,
			wantFinal: "Reply",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			done, final := ExtractDoneMarker(tc.in)
			if done != tc.wantDone {
				t.Errorf("done: got %v, want %v (in=%q)", done, tc.wantDone, tc.in)
			}
			if final != tc.wantFinal {
				t.Errorf("final: got %q, want %q", final, tc.wantFinal)
			}
		})
	}
}
