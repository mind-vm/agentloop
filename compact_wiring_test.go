package agentloop_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/mind-vm/agentloop"
	"github.com/mind-vm/agentloop/llm"
)

// stubCompactor is a scripted agentloop.Compactor: it records the
// history it was handed and returns a fixed result.
type stubCompactor struct {
	mu     sync.Mutex
	result agentloop.CompactResult
	err    error
	seen   [][]llm.Message
}

func (s *stubCompactor) Compact(_ context.Context, history []llm.Message) (agentloop.CompactResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seen = append(s.seen, history)
	if s.err != nil {
		return s.result, s.err
	}
	return s.result, nil
}

func (s *stubCompactor) lastSeen() []llm.Message {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.seen) == 0 {
		return nil
	}
	return s.seen[len(s.seen)-1]
}

// seedHistory writes a prior exchange into the step store so the next
// Run rehydrates something worth compacting.
func seedHistory(t *testing.T, steps *memorySteps, id string) {
	t.Helper()
	prior := []agentloop.RunStep{
		{StepType: "user", Content: "the original ask"},
		{StepType: "execute_js", Content: "function run(args) { return 1; }"},
		{StepType: "execute_js_result", Content: "logged something"},
		{StepType: "response", Content: "the earlier answer"},
	}
	for i, step := range prior {
		step.SessionID = id
		step.StepIndex = int32(i)
		step.ToolArgs = json.RawMessage(`{}`)
		if err := steps.Append(context.Background(), step); err != nil {
			t.Fatalf("seed step %d: %v", i, err)
		}
	}
}

func compactorLoop(t *testing.T, c agentloop.Compactor, fake *fakeLLM, steps *memorySteps, id string) agentloop.Loop {
	t.Helper()
	sessions := newMemorySessions()
	sessions.put(id, agentloop.Session{ID: id})
	return agentloop.New(agentloop.Config{
		LLM:            fake,
		Sessions:       sessions,
		Steps:          steps,
		SandboxBuilder: emptyBuilder{},
		Compactor:      c,
	})
}

func TestRunSendsCompactedHistory(t *testing.T) {
	steps := newMemorySteps()
	seedHistory(t, steps, "s1")

	compactor := &stubCompactor{result: agentloop.CompactResult{
		Messages: []llm.Message{{Role: "user", Content: "[summary] earlier work happened"}},
		Tokens:   agentloop.TokenUsage{Prompt: 200, Completion: 40},
	}}
	fake := newFakeLLM("fake", llm.CompletionResponse{Content: "DONE\nok", InputTokens: 5, OutputTokens: 3})

	loop := compactorLoop(t, compactor, fake, steps, "s1")
	res, err := loop.Run(context.Background(), agentloop.RunRequest{SessionID: "s1", Message: "the new ask"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	// The compactor sees the prior conversation only — the message that
	// started THIS run is appended afterwards and always sent verbatim.
	seen := compactor.lastSeen()
	if len(seen) != 4 {
		t.Fatalf("compactor saw %d messages, want the 4 prior turns", len(seen))
	}
	for _, m := range seen {
		if m.Content == "the new ask" {
			t.Fatal("compactor was handed the current user message; it should only see prior history")
		}
	}

	calls := fake.Calls()
	if len(calls) == 0 {
		t.Fatal("no LLM calls")
	}
	sent := calls[0].Messages
	if len(sent) != 3 {
		t.Fatalf("sent %d messages, want system + summary + new ask: %+v", len(sent), sent)
	}
	if sent[0].Role != "system" {
		t.Errorf("messages[0] role = %q, want system", sent[0].Role)
	}
	if !strings.Contains(sent[1].Content, "[summary]") {
		t.Errorf("messages[1] = %q, want the compacted summary", sent[1].Content)
	}
	if sent[2].Content != "the new ask" {
		t.Errorf("messages[2] = %q, want the verbatim new ask", sent[2].Content)
	}

	// The summarization call is part of what the run cost.
	if res.Tokens.Prompt != 205 || res.Tokens.Completion != 43 {
		t.Errorf("tokens = %+v, want compaction (200/40) plus the turn (5/3)", res.Tokens)
	}
}

func TestRunProceedsWhenCompactionFails(t *testing.T) {
	steps := newMemorySteps()
	seedHistory(t, steps, "s2")

	compactor := &stubCompactor{
		err:    errors.New("summarizer unavailable"),
		result: agentloop.CompactResult{Tokens: agentloop.TokenUsage{Prompt: 70}},
	}
	fake := newFakeLLM("fake", llm.CompletionResponse{Content: "DONE\nstill fine", InputTokens: 5, OutputTokens: 2})

	var warnings []string
	loop := compactorLoop(t, compactor, fake, steps, "s2")
	res, err := loop.Run(context.Background(), agentloop.RunRequest{
		SessionID: "s2",
		Message:   "carry on",
		OnEvent: func(e agentloop.RunEvent) {
			if e.Type == "warning" {
				warnings = append(warnings, e.Content)
			}
		},
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if res.Status != "completed" {
		t.Fatalf("status = %q, want completed — compaction is best-effort", res.Status)
	}

	// Falling back means the full, uncompacted history is sent.
	sent := fake.Calls()[0].Messages
	if len(sent) != 6 { // system + 4 prior + the new ask
		t.Errorf("sent %d messages, want the uncompacted 6: %+v", len(sent), sent)
	}
	if !hasWarning(warnings, "compaction failed") {
		t.Errorf("warnings = %q, want one naming the compaction failure", warnings)
	}
	// Even a failed summarization was billed.
	if res.Tokens.Prompt != 75 {
		t.Errorf("prompt tokens = %d, want the failed compaction's 70 counted too", res.Tokens.Prompt)
	}
}

func TestRunKeepsHistoryWhenCompactorReturnsNothing(t *testing.T) {
	steps := newMemorySteps()
	seedHistory(t, steps, "s3")

	compactor := &stubCompactor{result: agentloop.CompactResult{Messages: nil}}
	fake := newFakeLLM("fake", llm.CompletionResponse{Content: "DONE\nok"})

	var warnings []string
	loop := compactorLoop(t, compactor, fake, steps, "s3")
	if _, err := loop.Run(context.Background(), agentloop.RunRequest{
		SessionID: "s3",
		Message:   "hello",
		OnEvent: func(e agentloop.RunEvent) {
			if e.Type == "warning" {
				warnings = append(warnings, e.Content)
			}
		},
	}); err != nil {
		t.Fatalf("run: %v", err)
	}

	sent := fake.Calls()[0].Messages
	if len(sent) != 6 {
		t.Fatalf("sent %d messages, want the original 6 kept: %+v", len(sent), sent)
	}
	if !hasWarning(warnings, "returned nothing") {
		t.Errorf("warnings = %q, want one about the empty compaction", warnings)
	}
}

func TestRunEmitsCompactedEvent(t *testing.T) {
	steps := newMemorySteps()
	seedHistory(t, steps, "s4")

	compactor := &stubCompactor{result: agentloop.CompactResult{
		Messages: []llm.Message{{Role: "user", Content: "[summary]"}},
	}}
	fake := newFakeLLM("fake", llm.CompletionResponse{Content: "DONE\nok"})

	var got map[string]any
	loop := compactorLoop(t, compactor, fake, steps, "s4")
	if _, err := loop.Run(context.Background(), agentloop.RunRequest{
		SessionID: "s4",
		Message:   "hello",
		OnEvent: func(e agentloop.RunEvent) {
			if e.Type == "compacted" {
				got = e.Args
			}
		},
	}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got == nil {
		t.Fatal("no compacted event emitted")
	}
	if got["messages_before"] != 4 || got["messages_after"] != 1 {
		t.Errorf("compacted event args = %v, want before 4 / after 1", got)
	}
}

func TestRunWithoutCompactorIsUnchanged(t *testing.T) {
	steps := newMemorySteps()
	seedHistory(t, steps, "s5")
	fake := newFakeLLM("fake", llm.CompletionResponse{Content: "DONE\nok"})

	loop := compactorLoop(t, nil, fake, steps, "s5")
	if _, err := loop.Run(context.Background(), agentloop.RunRequest{SessionID: "s5", Message: "hello"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if sent := fake.Calls()[0].Messages; len(sent) != 6 {
		t.Fatalf("sent %d messages, want the full 6 when no Compactor is configured", len(sent))
	}
}

func hasWarning(warnings []string, substr string) bool {
	for _, w := range warnings {
		if strings.Contains(w, substr) {
			return true
		}
	}
	return false
}
