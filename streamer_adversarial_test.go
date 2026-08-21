package agentloop

import (
	"strings"
	"testing"
)

// collectChunks drives a streamer with the given chunks and returns the
// concatenated response_chunk content the UI would have seen.
func collectChunks(chunks ...string) string {
	var got strings.Builder
	s := newResponseStreamer(func(e RunEvent) {
		if e.Type == "response_chunk" {
			got.WriteString(e.Content)
		}
	})
	for _, c := range chunks {
		s.onChunk(c)
	}
	return got.String()
}

// The streamer classifies a turn (js vs final) from the accumulated prefix,
// holding back the first slice so a partial "DON" or half a fence isn't
// misclassified. The adversarial case is token-by-token arrival where the
// deciding marker straddles two chunks.

func TestResponseStreamer_DoneMarkerStraddlesChunks(t *testing.T) {
	// "DONE\n" never arrives in a single chunk — the streamer must still
	// recognise it from the accumulated buffer and strip it.
	got := collectChunks("DO", "NE", "\nHere is ", "the answer.")
	if got != "Here is the answer." {
		t.Fatalf("straddled DONE marker: got %q, want %q", got, "Here is the answer.")
	}
}

func TestResponseStreamer_FenceStraddlesChunks(t *testing.T) {
	// The opening fence arrives across three chunks; the turn is a JS turn
	// and must stay silent for its entire duration.
	got := collectChunks("`", "`", "`js\ndata.x = 1\n```")
	if got != "" {
		t.Fatalf("straddled fence should stay silent, got %q", got)
	}
}

func TestResponseStreamer_HoldbackFlushesAt32Chars(t *testing.T) {
	// Free-form text with no newline: nothing emits until the 32-char
	// hold-back boundary is crossed, then the whole buffer flushes. We send
	// a 20-char chunk (held) then a chunk that crosses 32 (flushes all).
	first := "12345678901234567890" // 20 chars, no newline → held back
	if got := collectChunks(first); got != "" {
		t.Fatalf("under 32 chars with no newline should be held, got %q", got)
	}
	got := collectChunks(first, "abcdefghijkl") // 32 total → flush
	if !strings.HasPrefix(got, first) || !strings.Contains(got, "abcdefghijkl") {
		t.Fatalf("crossing 32 chars should flush the whole buffer, got %q", got)
	}
}

func TestResponseStreamer_NewlineFlushesBefore32(t *testing.T) {
	// A newline resolves the ambiguity before the 32-char cap, so short
	// free-form replies aren't held until the cap.
	got := collectChunks("The answer is 42.\n")
	if !strings.Contains(got, "The answer is 42.") {
		t.Fatalf("newline before 32 chars should flush, got %q", got)
	}
}

func TestResponseStreamer_LateFenceTreatedAsTextOnceClassifiedFinal(t *testing.T) {
	// Once the prefix has been classified as a final-text turn (here via the
	// 32-char boundary), a fence arriving later is just text — it does NOT
	// retroactively silence the turn.
	lead := "Here is a sufficiently long plain answer " // > 32 chars, no newline
	got := collectChunks(lead, "```js ignored```")
	if !strings.Contains(got, "Here is a sufficiently long plain answer") {
		t.Fatalf("leading text should be emitted, got %q", got)
	}
	if !strings.Contains(got, "```js ignored```") {
		t.Fatalf("a fence after final-classification is emitted as text, got %q", got)
	}
}

func TestResponseStreamer_DoneWithNoTrailingContentThenChunk(t *testing.T) {
	// "DONE\n" with the answer arriving only in later chunks: the marker
	// chunk emits nothing, subsequent chunks emit as final text.
	got := collectChunks("DONE\n", "answer ", "in pieces")
	if got != "answer in pieces" {
		t.Fatalf("got %q, want %q", got, "answer in pieces")
	}
}
