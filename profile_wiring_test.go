package agentloop_test

import (
	"testing"

	"github.com/mind-vm/agentloop"
	"github.com/mind-vm/agentloop/llm"
)

// A derived profile must survive New's own validation — a Reserve that
// left no room for the prompt would panic at boot, which is exactly the
// failure a profile exists to prevent the operator from hand-rolling.
func TestProfileConfigsConstruct(t *testing.T) {
	for _, window := range []int{1024, 4096, 8192, 128_000} {
		sessions := newMemorySessions()
		cfg := agentloop.Config{
			LLM:            newFakeLLM("fake"),
			Sessions:       sessions,
			Steps:          newMemorySteps(),
			SandboxBuilder: &agentloop.DefaultSandboxBuilder{},
			Compactor:      &agentloop.SummarizingCompactor{},
		}
		p := agentloop.ProfileFor(window)
		p.Apply(&cfg)

		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("window %d: New panicked on a derived profile: %v", window, r)
				}
			}()
			agentloop.New(cfg)
		}()
	}
}

// The end the profile exists for: a small window keeps a long session's
// prompt inside it, where the package defaults would not.
func TestProfileKeepsASmallWindowInBudget(t *testing.T) {
	const window = 8192

	steps := newMemorySteps()
	seedBulkHistory(t, steps, "s1", 40, 2000)

	sessions := newMemorySessions()
	sessions.put("s1", agentloop.Session{ID: "s1"})
	fake := newFakeLLM("fake", llm.CompletionResponse{Content: "DONE\nok"})
	cfg := agentloop.Config{
		LLM:            fake,
		Sessions:       sessions,
		Steps:          steps,
		SandboxBuilder: &agentloop.DefaultSandboxBuilder{},
	}
	agentloop.ProfileFor(window).Apply(&cfg)

	if _, err := agentloop.New(cfg).Run(t.Context(), agentloop.RunRequest{SessionID: "s1", Message: "the new ask"}); err != nil {
		t.Fatalf("run: %v", err)
	}

	sent := fake.Calls()[0]
	total := 0
	for _, m := range sent.Messages {
		total += agentloop.EstimateTokens(m.Content) + 4
	}
	if total > window {
		t.Fatalf("sent ~%d tokens to a %d-token model", total, window)
	}
	if sent.MaxTokens == nil {
		t.Error("no output cap — the profile's reserve is not enforced")
	}
}
