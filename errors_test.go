package agentloop

import (
	"strings"
	"testing"
)

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
			name: "drops everything after the first newline",
			in:   "TypeError: Cannot read property 'x' of undefined\n    at <eval>:2:5(2)\n    at native",
			want: "TypeError: Cannot read property 'x' of undefined",
		},
		{
			name: "plain message with no plumbing is untouched",
			in:   "assert: rows.length is required",
			want: "assert: rows.length is required",
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
			wantSubstr: "help(",
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
