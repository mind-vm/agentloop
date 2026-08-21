package agentloop

import (
	"testing"

	"github.com/jryannel/agentloop/llm"
)

// TestElideStaleCode_PreservesPriorConversationVerbatim locks in a real
// cross-run bug: a naive "elide every assistant message except the
// last" pass, once THIS run appends its own assistant turn, would
// overwrite an EARLIER run's actual final answer (index 1 below — real
// prose the user already saw, not a code block) with the stale-code
// marker. fromIndex must keep everything before it — the prior
// conversation — untouched, even an old code block from a completed
// run (index 3): losing a little elision efficiency across runs beats
// corrupting what the model remembers telling the user.
func TestElideStaleCode_PreservesPriorConversationVerbatim(t *testing.T) {
	history := []llm.Message{
		{Role: "user", Content: "first"},                             // idx0 — prior run
		{Role: "assistant", Content: "The answer to your question."}, // idx1 — prior run's REAL final answer
		{Role: "user", Content: "second"},                            // idx2 — this run's user turn
		{Role: "assistant", Content: "```javascript\ncode-A\n```"},   // idx3 — this run's turn 1 (now stale)
		{Role: "user", Content: "Execution result:\nok"},             // idx4
		{Role: "assistant", Content: "```javascript\ncode-B\n```"},   // idx5 — this run's most recent turn
	}
	fromIndex := 2 // where THIS run's own additions begin

	out := elideStaleCode(history, fromIndex)

	if out[1].Content != "The answer to your question." {
		t.Fatalf("prior run's final answer must survive verbatim, got %q", out[1].Content)
	}
	if out[3].Content != staleCodeMarker {
		t.Fatalf("this run's stale (non-final) code block should be elided, got %q", out[3].Content)
	}
	if out[5].Content != "```javascript\ncode-B\n```" {
		t.Fatalf("this run's most recent code block must survive verbatim, got %q", out[5].Content)
	}
	// Non-assistant messages are always untouched.
	for _, i := range []int{0, 2, 4} {
		if out[i].Content != history[i].Content {
			t.Errorf("message %d should be untouched, got %q", i, out[i].Content)
		}
	}
}

// TestElideStaleCode_FromIndexAtEnd_NoElision confirms that a fromIndex
// past every existing message (the state on a fresh Run's first
// iteration, before it has appended any turns of its own) elides
// nothing — there's nothing yet that belongs to "this run".
func TestElideStaleCode_FromIndexAtEnd_NoElision(t *testing.T) {
	history := []llm.Message{
		{Role: "user", Content: "first"},
		{Role: "assistant", Content: "```javascript\nold code\n```"},
		{Role: "user", Content: "Execution result:\nok"},
		{Role: "assistant", Content: "Prior final answer."},
		{Role: "user", Content: "second"},
	}
	out := elideStaleCode(history, len(history))
	for i := range history {
		if out[i].Content != history[i].Content {
			t.Errorf("message %d should be untouched when fromIndex is past the end, got %q", i, out[i].Content)
		}
	}
}
