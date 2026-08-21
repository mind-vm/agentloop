package agentloop

import (
	"strings"
	"testing"
)

// TestResponseStreamer_JSStaysSilent confirms a stream that opens with
// a fenced JS block emits NO response_chunk events — the loop UI
// shouldn't see half-typed code from a turn that's about to be
// executed silently.
func TestResponseStreamer_JSStaysSilent(t *testing.T) {
	var got []string
	s := newResponseStreamer(func(e RunEvent) {
		if e.Type == "response_chunk" {
			got = append(got, e.Content)
		}
	})

	for _, chunk := range []string{"```", "javascript\n", "log(", "\"hi\"", ")\n```"} {
		s.onChunk(chunk)
	}
	if len(got) != 0 {
		t.Fatalf("expected no response_chunk events for JS turn, got %v", got)
	}
}

// TestResponseStreamer_DoneMarkerStripped confirms the leading
// "DONE\n" marker is removed and only the answer is streamed.
func TestResponseStreamer_DoneMarkerStripped(t *testing.T) {
	var got []string
	s := newResponseStreamer(func(e RunEvent) {
		if e.Type == "response_chunk" {
			got = append(got, e.Content)
		}
	})

	s.onChunk("DONE\nHere is ")
	s.onChunk("the answer.")
	joined := strings.Join(got, "")
	if joined != "Here is the answer." {
		t.Fatalf("response chunks: got %q, want %q", joined, "Here is the answer.")
	}
}

// TestResponseStreamer_FreeformFallback confirms a stream that doesn't
// start with ``` or DONE\n is treated as a final-text turn and
// streamed in full.
func TestResponseStreamer_FreeformFallback(t *testing.T) {
	var got []string
	s := newResponseStreamer(func(e RunEvent) {
		if e.Type == "response_chunk" {
			got = append(got, e.Content)
		}
	})

	// Free-form text past the newline threshold triggers fallback.
	s.onChunk("The answer is\n")
	s.onChunk("42.")
	joined := strings.Join(got, "")
	if !strings.Contains(joined, "The answer is") || !strings.Contains(joined, "42.") {
		t.Fatalf("response chunks should include freeform text: %q", joined)
	}
}

// TestResponseStreamer_NilCallback confirms a nil callback is legal —
// the streamer still accumulates internally without panicking.
func TestResponseStreamer_NilCallback(t *testing.T) {
	s := newResponseStreamer(nil)
	s.onChunk("DONE\nanswer")
	// No assertion needed — the test passes if onChunk doesn't panic.
}
