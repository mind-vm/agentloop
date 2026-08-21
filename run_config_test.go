package agentloop_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jryannel/agentloop"
	"github.com/jryannel/agentloop/llm"
	"github.com/jryannel/agentloop/sandbox"
)

// TestRun_MaxIterationsConfigurable verifies Config.MaxIterations
// overrides the package default: with a cap of 2 and an LLM that
// always emits JS, the run terminates as max_iterations after exactly
// two round-trips.
func TestRun_MaxIterationsConfigurable(t *testing.T) {
	js := "```javascript\nfunction run(args) { return {}; }\n```"
	fake := newFakeLLM("fake",
		llm.CompletionResponse{Content: js},
		llm.CompletionResponse{Content: js},
	)
	sessions := newMemorySessions()
	sessions.put("s1", agentloop.Session{ID: "s1"})

	loop := agentloop.New(agentloop.Config{
		LLM:            fake,
		Sessions:       sessions,
		Steps:          newMemorySteps(),
		SandboxBuilder: emptyBuilder{},
		MaxIterations:  2,
	})

	result, err := loop.Run(context.Background(), agentloop.RunRequest{
		SessionID: "s1",
		Message:   "go",
	})
	if err == nil {
		t.Fatalf("expected max-iterations error, got nil")
	}
	if result.Status != "max_iterations" {
		t.Fatalf("status: got %q, want %q", result.Status, "max_iterations")
	}
	if !strings.Contains(err.Error(), "(2)") {
		t.Errorf("error should name the configured cap: %v", err)
	}
}

// TestRun_HistoryWindowConfigurable verifies Config.HistoryWindow
// bounds the LastN load: with a window of 7 the StepStore is asked
// for at most 7 prior steps.
func TestRun_HistoryWindowConfigurable(t *testing.T) {
	fake := newFakeLLM("fake",
		llm.CompletionResponse{Content: "DONE\nok"},
	)
	sessions := newMemorySessions()
	sessions.put("s1", agentloop.Session{ID: "s1"})
	steps := newMemorySteps()

	loop := agentloop.New(agentloop.Config{
		LLM:            fake,
		Sessions:       sessions,
		Steps:          steps,
		SandboxBuilder: emptyBuilder{},
		HistoryWindow:  7,
	})

	if _, err := loop.Run(context.Background(), agentloop.RunRequest{SessionID: "s1", Message: "hi"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if got := steps.lastNRequested(); got != 7 {
		t.Fatalf("LastN window: got %d, want 7", got)
	}
}

// failingCapability always errors at Build time.
func failingCapability() agentloop.Capability {
	return agentloop.Capability{
		Name:        "flaky",
		Description: "always fails to build",
		Build: func(agentloop.BuildContext) ([]sandbox.Pack, error) {
			return nil, context.DeadlineExceeded
		},
	}
}

// TestDefaultSandboxBuilder_WarnsOnCapabilityFailure verifies a
// capability whose Build fails surfaces a warning event in the run
// trace (not just the server log), so operators can see the agent ran
// with a smaller tool surface.
func TestDefaultSandboxBuilder_WarnsOnCapabilityFailure(t *testing.T) {
	b := &agentloop.DefaultSandboxBuilder{Capabilities: []agentloop.Capability{failingCapability()}}

	var warnings []sandbox.Event
	sb, cleanup, err := b.Build(context.Background(), agentloop.Session{ID: "s1"}, agentloop.Scope{}, func(evt sandbox.Event) {
		if evt.Kind == sandbox.EventWarning {
			warnings = append(warnings, evt)
		}
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer cleanup()
	if sb == nil {
		t.Fatalf("sandbox should still build without the flaky capability")
	}
	if len(warnings) != 1 {
		t.Fatalf("expected 1 warning event, got %d", len(warnings))
	}
	if !strings.Contains(warnings[0].Summary, "flaky") {
		t.Errorf("warning should name the lost capability: %q", warnings[0].Summary)
	}
}
