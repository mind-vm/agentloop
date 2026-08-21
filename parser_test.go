package agentloop

import "testing"

func TestExtractJSBlock(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "single javascript block",
			in:   "```javascript\nlog(\"hi\")\n```",
			want: `log("hi")`,
		},
		{
			name: "js alias",
			in:   "Sure.\n```js\nvar x = 1\n```",
			want: "var x = 1",
		},
		{
			name: "trims surrounding whitespace inside fence",
			in:   "```javascript\n\n   log(1)   \n\n```",
			want: "log(1)",
		},
		{
			name: "no fence returns empty",
			in:   "Just plain prose.",
			want: "",
		},
		{
			name: "wrong language returns empty",
			in:   "```python\nprint(1)\n```",
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := ExtractJSBlock(tc.in); got != tc.want {
				t.Errorf("ExtractJSBlock: got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestExtractDoneMarker(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantDone  bool
		wantFinal string
	}{
		{
			name:      "DONE alone",
			in:        "DONE",
			wantDone:  true,
			wantFinal: "",
		},
		{
			name:      "DONE then answer",
			in:        "DONE\nHere is the answer.",
			wantDone:  true,
			wantFinal: "Here is the answer.",
		},
		{
			name:      "leading whitespace tolerated",
			in:        "  DONE\nReply",
			wantDone:  true,
			wantFinal: "Reply",
		},
		{
			name:      "inline DONE in prose ignored",
			in:        "I will be DONE shortly.",
			wantDone:  false,
			wantFinal: "",
		},
		{
			name:      "no marker",
			in:        "Just talking",
			wantDone:  false,
			wantFinal: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			done, final := ExtractDoneMarker(tc.in)
			if done != tc.wantDone {
				t.Errorf("done: got %v, want %v", done, tc.wantDone)
			}
			if final != tc.wantFinal {
				t.Errorf("final: got %q, want %q", final, tc.wantFinal)
			}
		})
	}
}
