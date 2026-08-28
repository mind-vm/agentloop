package agentloop_test

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"github.com/mind-vm/agentloop"
	"github.com/mind-vm/agentloop/llm"
)

// seedBulkHistory writes prior turns large enough that a small budget
// must drop some of them.
func seedBulkHistory(t *testing.T, steps *memorySteps, id string, turns, bytesEach int) {
	t.Helper()
	body := strings.Repeat("x", bytesEach)
	for i := 0; i < turns; i++ {
		step := agentloop.RunStep{
			SessionID: id,
			StepIndex: int32(i),
			StepType:  "user",
			Content:   body,
			ToolArgs:  json.RawMessage(`{}`),
		}
		if i%2 == 1 {
			step.StepType = "response"
		}
		if err := steps.Append(context.Background(), step); err != nil {
			t.Fatalf("seed step %d: %v", i, err)
		}
	}
}

func budgetLoop(t *testing.T, budget agentloop.ContextBudget, fake *fakeLLM, steps *memorySteps, id string) agentloop.Loop {
	t.Helper()
	sessions := newMemorySessions()
	sessions.put(id, agentloop.Session{ID: id})
	return agentloop.New(agentloop.Config{
		LLM:            fake,
		Sessions:       sessions,
		Steps:          steps,
		SandboxBuilder: emptyBuilder{},
		ContextBudget:  budget,
	})
}

// collectEvents returns an OnEvent that accumulates every emission.
func collectEvents() (func(agentloop.RunEvent), func() []agentloop.RunEvent) {
	var mu sync.Mutex
	var got []agentloop.RunEvent
	return func(e agentloop.RunEvent) {
			mu.Lock()
			defer mu.Unlock()
			got = append(got, e)
		}, func() []agentloop.RunEvent {
			mu.Lock()
			defer mu.Unlock()
			out := make([]agentloop.RunEvent, len(got))
			copy(out, got)
			return out
		}
}

func TestRunTrimsPromptToBudget(t *testing.T) {
	steps := newMemorySteps()
	seedBulkHistory(t, steps, "s1", 20, 4000) // ~1000 tokens per turn

	fake := newFakeLLM("fake", llm.CompletionResponse{Content: "DONE\nok"})
	loop := budgetLoop(t, agentloop.ContextBudget{MaxTokens: 8192}, fake, steps, "s1")

	if _, err := loop.Run(context.Background(), agentloop.RunRequest{SessionID: "s1", Message: "the new ask"}); err != nil {
		t.Fatalf("run: %v", err)
	}

	calls := fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("got %d LLM calls, want 1", len(calls))
	}
	sent := calls[0].Messages

	if len(sent) >= 22 {
		t.Fatalf("sent %d messages — the budget trimmed nothing", len(sent))
	}
	if sent[0].Role != "system" {
		t.Fatalf("first message is %q, want the system prompt", sent[0].Role)
	}
	if got := sent[len(sent)-1].Content; got != "the new ask" {
		t.Fatalf("last message = %q, want the current user turn", got)
	}
	if !strings.Contains(sent[1].Content, "earlier messages in this session were dropped") {
		t.Fatalf("no drop marker after the system prompt; got %q", sent[1].Content)
	}

	// The whole point: the request actually fits the window it was
	// given, reserve included.
	total := 0
	for _, m := range sent {
		total += agentloop.EstimateTokens(m.Content) + 4
	}
	if allowance := 8192 - 1024; total > allowance {
		t.Fatalf("sent ~%d tokens against an allowance of %d", total, allowance)
	}
}

func TestRunEmitsBudgetTrimmedEvent(t *testing.T) {
	steps := newMemorySteps()
	seedBulkHistory(t, steps, "s1", 20, 4000)

	fake := newFakeLLM("fake", llm.CompletionResponse{Content: "DONE\nok"})
	loop := budgetLoop(t, agentloop.ContextBudget{MaxTokens: 8192}, fake, steps, "s1")

	onEvent, events := collectEvents()
	if _, err := loop.Run(context.Background(), agentloop.RunRequest{
		SessionID: "s1", Message: "the new ask", OnEvent: onEvent,
	}); err != nil {
		t.Fatalf("run: %v", err)
	}

	var trimmed *agentloop.RunEvent
	for i, e := range events() {
		if e.Type == "budget_trimmed" {
			trimmed = &events()[i]
			break
		}
	}
	if trimmed == nil {
		t.Fatal("no budget_trimmed event — a trim must never be silent")
	}
	dropped, ok := trimmed.Args["messages_dropped"].(int)
	if !ok || dropped <= 0 {
		t.Fatalf("messages_dropped = %v, want a positive count", trimmed.Args["messages_dropped"])
	}
	if _, ok := trimmed.Args["estimated_tokens"]; !ok {
		t.Error("event carries no estimated_tokens")
	}
	if _, ok := trimmed.Args["allowance"]; !ok {
		t.Error("event carries no allowance")
	}
}

func TestRunWarnsWhenNothingLeftToDrop(t *testing.T) {
	steps := newMemorySteps()
	fake := newFakeLLM("fake", llm.CompletionResponse{Content: "DONE\nok"})
	// The composed system prompt alone is thousands of tokens, so a
	// 600-token window cannot fit the floor however much is dropped.
	loop := budgetLoop(t, agentloop.ContextBudget{MaxTokens: 600}, fake, steps, "s1")

	onEvent, events := collectEvents()
	if _, err := loop.Run(context.Background(), agentloop.RunRequest{
		SessionID: "s1", Message: "the new ask", OnEvent: onEvent,
	}); err != nil {
		t.Fatalf("run: %v", err)
	}

	found := false
	for _, e := range events() {
		if e.Type == "warning" && strings.Contains(e.Content, "context budget") {
			found = true
		}
	}
	if !found {
		t.Fatal("no warning when the prompt exceeds the window with nothing left to drop")
	}

	// Over budget or not, the run still gets its turn — the estimate is
	// approximate, and refusing to try is worse than a provider error.
	if len(fake.Calls()) != 1 {
		t.Fatalf("got %d LLM calls, want the request sent anyway", len(fake.Calls()))
	}
}

func TestRunCapsCompletionAtTheReserve(t *testing.T) {
	steps := newMemorySteps()
	fake := newFakeLLM("fake", llm.CompletionResponse{Content: "DONE\nok"})
	loop := budgetLoop(t, agentloop.ContextBudget{MaxTokens: 8192, Reserve: 1500}, fake, steps, "s1")

	if _, err := loop.Run(context.Background(), agentloop.RunRequest{SessionID: "s1", Message: "hi"}); err != nil {
		t.Fatalf("run: %v", err)
	}

	got := fake.Calls()[0].MaxTokens
	if got == nil {
		t.Fatal("MaxTokens unset — the reserve is a wish unless the completion is held to it")
	}
	if *got != 1500 {
		t.Fatalf("MaxTokens = %d, want the reserve (1500)", *got)
	}
}

func TestRunWithoutBudgetIsUnchanged(t *testing.T) {
	steps := newMemorySteps()
	seedBulkHistory(t, steps, "s1", 20, 4000)

	fake := newFakeLLM("fake", llm.CompletionResponse{Content: "DONE\nok"})
	loop := budgetLoop(t, agentloop.ContextBudget{}, fake, steps, "s1")

	onEvent, events := collectEvents()
	if _, err := loop.Run(context.Background(), agentloop.RunRequest{
		SessionID: "s1", Message: "the new ask", OnEvent: onEvent,
	}); err != nil {
		t.Fatalf("run: %v", err)
	}

	sent := fake.Calls()[0].Messages
	// System prompt + 20 rehydrated turns + the current message.
	if len(sent) != 22 {
		t.Fatalf("sent %d messages, want 22 — the zero-value budget must not trim", len(sent))
	}
	if fake.Calls()[0].MaxTokens != nil {
		t.Fatalf("MaxTokens = %d, want unset without a budget", *fake.Calls()[0].MaxTokens)
	}
	for _, e := range events() {
		if e.Type == "budget_trimmed" {
			t.Fatal("budget_trimmed emitted with no budget configured")
		}
	}
}

func TestNewRejectsAReserveThatLeavesNoRoom(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("New accepted a reserve larger than the window")
		}
	}()
	sessions := newMemorySessions()
	agentloop.New(agentloop.Config{
		LLM:            newFakeLLM("fake"),
		Sessions:       sessions,
		Steps:          newMemorySteps(),
		SandboxBuilder: emptyBuilder{},
		ContextBudget:  agentloop.ContextBudget{MaxTokens: 4096, Reserve: 4096},
	})
}
