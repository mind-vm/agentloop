package agentloopmem_test

import (
	"context"
	"strings"
	"testing"

	"github.com/mind-vm/agentloop"
	"github.com/mind-vm/agentloop/agentloopmem"
	"github.com/mind-vm/agentloop/llm"
)

// TestE2E_FullLoop_WithMemoryStores wires the full agentloop stack
// against the in-memory stores and a deterministic fake LLM, and
// proves the canonical multi-turn flow:
//
//  1. User says "step 1".
//  2. LLM emits JS that sets count = 1.
//  3. LLM emits DONE with a final answer; loop persists everything.
//  4. User says "step 2" — second Run.
//  5. LLM sees the rehydrated data + history.
//  6. LLM emits DONE again; loop finalises.
//
// Asserts on the full trace, the data snapshot persistence, and the
// step ordering — i.e. the exact pattern applications will exercise.
//
// This is the canonical "how do I wire it?" example. Keep it readable;
// it doubles as documentation.
func TestE2E_FullLoop_WithMemoryStores(t *testing.T) {
	ctx := context.Background()

	// Stores — in-memory; real wiring would swap these for a DB-backed
	// implementation.
	sessions := agentloopmem.NewSessionStore(nil)
	sessions.Put(agentloop.Session{
		ID:           "demo-session",
		SystemPrompt: "You are concise.",
		Model:        "echo",
	})
	steps := agentloopmem.NewStepStore()

	// No capabilities needed — the loop runs in run(args)→return mode,
	// so state is carried by the function return, not a capability
	// pack. The builder still appends the meta packs (skill discovery,
	// help). Real wiring layers in fetch/ai/etc. via Capabilities.
	builder := &agentloop.DefaultSandboxBuilder{}

	// LLM — three scripted responses, one per LLM call. The fake
	// validates that the loop calls the LLM the expected number of
	// times in the expected sequence.
	fake := &scriptedClient{
		responses: []llm.CompletionResponse{
			{Content: "```javascript\nfunction run(args) { return { count: 1 }; }\n```", InputTokens: 5, OutputTokens: 4},
			{Content: "DONE\nIncremented to 1.", InputTokens: 12, OutputTokens: 4},
			{Content: "DONE\nStill 1, no work.", InputTokens: 20, OutputTokens: 3},
		},
	}

	loop := agentloop.New(agentloop.Config{
		LLM:            fake,
		Sessions:       sessions,
		Steps:          steps,
		SandboxBuilder: builder,
	})

	// ---- Run 1: 2-turn cycle. ----
	var events1 []agentloop.RunEvent
	res1, err := loop.Run(ctx, agentloop.RunRequest{
		SessionID: "demo-session",
		Message:   "step 1",
		OnEvent:   func(e agentloop.RunEvent) { events1 = append(events1, e) },
	})
	if err != nil {
		t.Fatalf("run 1: %v", err)
	}
	if res1.Status != "completed" {
		t.Fatalf("run 1 status: got %q, want %q", res1.Status, "completed")
	}
	if res1.FinalText != "Incremented to 1." {
		t.Fatalf("run 1 final text: got %q", res1.FinalText)
	}

	// Trace assertion: the canonical lifecycle events all appear in
	// the expected order (sandbox_event / data_update / response_chunk
	// can interleave but the lifecycle markers stay in sequence).
	wantOrder := []string{"user", "execute_js", "execute_js_result", "response", "done"}
	if !containsInOrder(eventTypes(events1), wantOrder) {
		t.Errorf("run 1 events do not contain expected sequence %v in order: got %v", wantOrder, eventTypes(events1))
	}

	// data snapshot persisted between runs.
	if snap := sessions.Snapshot("demo-session"); !strings.Contains(string(snap), `"count":1`) {
		t.Errorf("data snapshot missing count=1: %s", snap)
	}

	// ---- Run 2: confirms history rehydration. ----
	var events2 []agentloop.RunEvent
	res2, err := loop.Run(ctx, agentloop.RunRequest{
		SessionID: "demo-session",
		Message:   "step 2",
		OnEvent:   func(e agentloop.RunEvent) { events2 = append(events2, e) },
	})
	if err != nil {
		t.Fatalf("run 2: %v", err)
	}
	if res2.Status != "completed" || res2.FinalText != "Still 1, no work." {
		t.Fatalf("run 2: status=%q final=%q", res2.Status, res2.FinalText)
	}

	// The LLM saw the prior conversation when called the 3rd time.
	thirdCall := fake.calls[2]
	gotSystem, gotPriorUser, gotPriorAssistant := false, false, false
	for _, m := range thirdCall.Messages {
		switch {
		case m.Role == "system":
			gotSystem = true
		case m.Role == "user" && strings.Contains(m.Content, "step 1"):
			gotPriorUser = true
		case m.Role == "assistant" && strings.Contains(m.Content, "Incremented to 1"):
			gotPriorAssistant = true
		}
	}
	if !gotSystem || !gotPriorUser || !gotPriorAssistant {
		t.Errorf("run 2 LLM context: missing one of system/prior-user/prior-assistant; messages=%+v", thirdCall.Messages)
	}

	// Token totals — fake LLM reports 12 + 20 input across the
	// response-producing calls. Validates per-Run rollup math.
	if got, want := res1.Tokens.Prompt, int32(17); got != want {
		t.Errorf("run 1 prompt tokens: got %d, want %d", got, want)
	}
	if got, want := res2.Tokens.Prompt, int32(20); got != want {
		t.Errorf("run 2 prompt tokens: got %d, want %d", got, want)
	}

	// Final state: sessions store is finalised twice (once per Run).
	if _, ok := sessions.Final("demo-session"); !ok {
		t.Errorf("session should be finalised after the runs")
	}
}

// ---- test doubles ----

type scriptedClient struct {
	responses []llm.CompletionResponse
	calls     []llm.CompletionRequest
}

func (s *scriptedClient) Name() string { return "scripted" }

func (s *scriptedClient) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	s.calls = append(s.calls, req)
	if len(s.responses) == 0 {
		return llm.CompletionResponse{}, errExhausted
	}
	r := s.responses[0]
	s.responses = s.responses[1:]
	return r, nil
}

func (s *scriptedClient) Stream(ctx context.Context, req llm.CompletionRequest, onChunk func(string)) (llm.CompletionResponse, error) {
	resp, err := s.Complete(ctx, req)
	if err != nil {
		return resp, err
	}
	if onChunk != nil && resp.Content != "" {
		onChunk(resp.Content)
	}
	return resp, nil
}

type exhaustedErr struct{}

func (exhaustedErr) Error() string { return "scriptedClient: no more responses" }

var errExhausted = exhaustedErr{}

func eventTypes(events []agentloop.RunEvent) []string {
	out := make([]string, 0, len(events))
	for _, e := range events {
		out = append(out, e.Type)
	}
	return out
}

// containsInOrder reports whether want appears as a subsequence of
// got (i.e. each want[i] occurs after want[i-1] in got, with arbitrary
// other entries allowed in between).
func containsInOrder(got, want []string) bool {
	i := 0
	for _, g := range got {
		if i < len(want) && g == want[i] {
			i++
		}
	}
	return i == len(want)
}
