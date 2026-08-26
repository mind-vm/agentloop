package agentloop

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/mind-vm/agentloop/llm"
)

// stubSummarizer is a minimal llm.Client for compaction tests: it
// records what it was asked to summarize and replies with a canned
// answer (or an error).
type stubSummarizer struct {
	reply  string
	err    error
	tokens [2]int // input, output
	calls  []llm.CompletionRequest
}

func (s *stubSummarizer) Name() string { return "stub" }

func (s *stubSummarizer) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	s.calls = append(s.calls, req)
	return llm.CompletionResponse{
		Content:      s.reply,
		InputTokens:  s.tokens[0],
		OutputTokens: s.tokens[1],
	}, s.err
}

func (s *stubSummarizer) Stream(ctx context.Context, req llm.CompletionRequest, onChunk func(string)) (llm.CompletionResponse, error) {
	return s.Complete(ctx, req)
}

// turns builds a history of n exchanges: a user ask followed by
// alternating run() blocks and their execution results.
func turns(n int) []llm.Message {
	out := []llm.Message{{Role: "user", Content: "do the thing"}}
	for i := range n {
		out = append(out,
			llm.Message{Role: "assistant", Content: fmt.Sprintf("```javascript\nfunction run(args) { return %d; }\n```", i)},
			llm.Message{Role: "user", Content: fmt.Sprintf("%s\nlogged %d", executionResultPrefix, i)},
		)
	}
	return out
}

// ---- split boundary ----

func TestSafeSplitPointKeepsScriptWithItsResult(t *testing.T) {
	h := turns(5) // [user, (assistant, result) x5] = 11 messages

	// Index 2 is an execution result; splitting there would strand it
	// from the assistant turn at index 1 that produced it.
	if got := safeSplitPoint(h, 2); got != 1 {
		t.Fatalf("split = %d, want 1 (step back onto the assistant turn)", got)
	}
	if !isExecutionResult(h[2]) {
		t.Fatal("fixture wrong: index 2 should be an execution result")
	}
	// An assistant turn is already a clean boundary.
	if got := safeSplitPoint(h, 1); got != 1 {
		t.Fatalf("split = %d, want 1 (assistant turns need no adjustment)", got)
	}
}

func TestSafeSplitPointEdges(t *testing.T) {
	h := turns(3)
	if got := safeSplitPoint(h, 0); got != 0 {
		t.Errorf("split(0) = %d, want 0", got)
	}
	if got := safeSplitPoint(h, -5); got != 0 {
		t.Errorf("split(-5) = %d, want 0", got)
	}
	if got := safeSplitPoint(h, len(h)+3); got != len(h) {
		t.Errorf("split(over) = %d, want %d", got, len(h))
	}
}

// ---- bounds ----

func TestCompactorBounds(t *testing.T) {
	tests := []struct {
		name                  string
		trigger, keep         int
		wantTrigger, wantKeep int
	}{
		{name: "defaults", wantTrigger: defaultCompactTrigger, wantKeep: defaultCompactKeep},
		{name: "explicit", trigger: 30, keep: 10, wantTrigger: 30, wantKeep: 10},
		// Keeping at least as much as triggers compaction would fold
		// nothing away while still paying for a summarization.
		{name: "keep clamped below trigger", trigger: 20, keep: 20, wantTrigger: 20, wantKeep: 10},
		{name: "keep over trigger clamped", trigger: 20, keep: 999, wantTrigger: 20, wantKeep: 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &SummarizingCompactor{Trigger: tt.trigger, Keep: tt.keep}
			trigger, keep := c.bounds()
			if trigger != tt.wantTrigger || keep != tt.wantKeep {
				t.Fatalf("bounds() = (%d, %d), want (%d, %d)", trigger, keep, tt.wantTrigger, tt.wantKeep)
			}
			if keep >= trigger {
				t.Fatalf("keep %d must stay below trigger %d", keep, trigger)
			}
		})
	}
}

// ---- Compact ----

func TestCompactBelowTriggerMakesNoCall(t *testing.T) {
	stub := &stubSummarizer{reply: "should not be used"}
	c := &SummarizingCompactor{LLM: stub, Trigger: 20, Keep: 5}
	h := turns(3) // 7 messages, well under the trigger

	res, err := c.Compact(context.Background(), h)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if len(res.Messages) != len(h) {
		t.Fatalf("messages = %d, want %d unchanged", len(res.Messages), len(h))
	}
	if len(stub.calls) != 0 {
		t.Fatalf("made %d provider calls under the trigger, want 0", len(stub.calls))
	}
}

func TestCompactWithoutClientIsNoOp(t *testing.T) {
	c := &SummarizingCompactor{Trigger: 4, Keep: 2}
	h := turns(10)
	res, err := c.Compact(context.Background(), h)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if len(res.Messages) != len(h) {
		t.Fatalf("messages = %d, want %d — a missing client must degrade, not fail", len(res.Messages), len(h))
	}
}

func TestCompactReplacesOlderTurnsWithSummary(t *testing.T) {
	stub := &stubSummarizer{reply: "  user wanted the thing; steps 0-3 done  ", tokens: [2]int{120, 30}}
	c := &SummarizingCompactor{LLM: stub, Trigger: 10, Keep: 4}
	h := turns(8) // 17 messages

	res, err := c.Compact(context.Background(), h)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if len(res.Messages) >= len(h) {
		t.Fatalf("messages = %d, want fewer than %d", len(res.Messages), len(h))
	}

	summary := res.Messages[0]
	if summary.Role != "user" {
		t.Errorf("summary role = %q, want %q", summary.Role, "user")
	}
	if !strings.Contains(summary.Content, "user wanted the thing") {
		t.Errorf("summary content missing the model's text: %q", summary.Content)
	}
	if strings.Contains(summary.Content, "  user wanted") {
		t.Errorf("summary should be trimmed, got %q", summary.Content)
	}

	// The retained tail must be the ORIGINAL messages, untouched.
	tail := res.Messages[1:]
	if want := h[len(h)-len(tail):]; !equalMessages(tail, want) {
		t.Errorf("tail was modified:\n got %v\nwant %v", tail, want)
	}
	if res.Tokens.Prompt != 120 || res.Tokens.Completion != 30 {
		t.Errorf("tokens = %+v, want {120 30}", res.Tokens)
	}
}

// The tail must never open with an execution result whose script was
// summarized away — the model would read output from a script it cannot
// see.
func TestCompactNeverStrandsAnExecutionResult(t *testing.T) {
	stub := &stubSummarizer{reply: "summary"}
	// Keep of 4 on a 17-message history lands the natural split on an
	// execution result, so the compactor has to step back one.
	c := &SummarizingCompactor{LLM: stub, Trigger: 10, Keep: 4}

	res, err := c.Compact(context.Background(), turns(8))
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	first := res.Messages[1] // [0] is the summary
	if isExecutionResult(first) {
		t.Fatalf("tail opens with an orphaned execution result: %q", first.Content)
	}
	if first.Role != "assistant" {
		t.Fatalf("tail should open with the run() block, got role %q", first.Role)
	}
}

func TestCompactSurvivesProviderFailure(t *testing.T) {
	stub := &stubSummarizer{err: errors.New("upstream down"), tokens: [2]int{90, 0}}
	c := &SummarizingCompactor{LLM: stub, Trigger: 10, Keep: 4}
	h := turns(8)

	res, err := c.Compact(context.Background(), h)
	if err == nil {
		t.Fatal("Compact: want error")
	}
	if !equalMessages(res.Messages, h) {
		t.Error("a failed compaction must hand back the original history")
	}
	// A billable call that produced nothing usable still has to be counted.
	if res.Tokens.Prompt != 90 {
		t.Errorf("tokens = %+v, want the failed call's 90 prompt tokens", res.Tokens)
	}
}

func TestCompactRejectsEmptySummary(t *testing.T) {
	stub := &stubSummarizer{reply: "   ", tokens: [2]int{50, 1}}
	c := &SummarizingCompactor{LLM: stub, Trigger: 10, Keep: 4}
	h := turns(8)

	res, err := c.Compact(context.Background(), h)
	if err == nil {
		t.Fatal("Compact: want error for a blank summary")
	}
	if !equalMessages(res.Messages, h) {
		t.Error("a blank summary must leave the history intact")
	}
	if res.Tokens.Prompt != 50 {
		t.Errorf("tokens = %+v, want the call counted", res.Tokens)
	}
}

func TestCompactSendsLabelledTranscript(t *testing.T) {
	stub := &stubSummarizer{reply: "summary"}
	c := &SummarizingCompactor{LLM: stub, Trigger: 10, Keep: 4, Prompt: "custom instruction"}

	if _, err := c.Compact(context.Background(), turns(8)); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(stub.calls))
	}
	req := stub.calls[0]
	if req.Messages[0].Role != "system" || req.Messages[0].Content != "custom instruction" {
		t.Errorf("Prompt override not used: %+v", req.Messages[0])
	}
	if !strings.Contains(req.Messages[1].Content, "[execution result]") {
		t.Errorf("transcript should label execution results, got:\n%s", req.Messages[1].Content)
	}
}

// ---- transcript rendering ----

func TestRenderTranscriptElidesTheMiddleWhenOversized(t *testing.T) {
	huge := strings.Repeat("x", maxSummarizeBytes)
	out := renderTranscript([]llm.Message{
		{Role: "user", Content: "FIRST-ASK"},
		{Role: "assistant", Content: huge},
		{Role: "user", Content: "LAST-THING"},
	})
	if len(out) > maxSummarizeBytes+512 {
		t.Fatalf("rendered %d bytes, want roughly %d", len(out), maxSummarizeBytes)
	}
	// The original ask and the most recent turn are the two things
	// worth preserving when the middle has to go.
	if !strings.Contains(out, "FIRST-ASK") {
		t.Error("head of the transcript was dropped")
	}
	if !strings.Contains(out, "LAST-THING") {
		t.Error("tail of the transcript was dropped")
	}
	if !strings.Contains(out, "omitted") {
		t.Error("elision should be marked so the model knows it is partial")
	}
}

func equalMessages(a, b []llm.Message) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
