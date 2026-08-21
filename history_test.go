package agentloop

import "testing"

func TestRehydrateHistory_SkipsNonReplayableSteps(t *testing.T) {
	steps := []RunStep{
		{StepType: "user", Content: "hi"},
		{StepType: "execute_js", Content: "log(1)"},
		{StepType: "execute_js_result", Content: "1"},
		{StepType: "error", Content: "something blew up"}, // must be skipped
		{StepType: "response", Content: "Hello back."},
	}
	out := rehydrateHistory(steps, HistoryWindow)
	if len(out) != 4 {
		t.Fatalf("expected 4 replayable turns, got %d (%v)", len(out), out)
	}
	if out[0].Role != "user" || out[0].Content != "hi" {
		t.Errorf("first turn: got %+v", out[0])
	}
	if out[1].Role != "assistant" || out[1].Content != "```javascript\nlog(1)\n```" {
		t.Errorf("execute_js should re-fence: got %+v", out[1])
	}
	if out[2].Role != "user" || out[2].Content != "Execution result:\n1" {
		t.Errorf("execute_js_result should prefix: got %+v", out[2])
	}
	if out[3].Role != "assistant" || out[3].Content != "Hello back." {
		t.Errorf("response turn: got %+v", out[3])
	}
}

func TestRehydrateHistory_RespectsWindow(t *testing.T) {
	steps := make([]RunStep, HistoryWindow+5)
	for i := range steps {
		steps[i] = RunStep{StepType: "user", Content: "msg"}
	}
	out := rehydrateHistory(steps, HistoryWindow)
	if len(out) != HistoryWindow {
		t.Fatalf("expected window-clamped result of %d, got %d", HistoryWindow, len(out))
	}
}

func TestRehydrateHistory_CustomWindow(t *testing.T) {
	steps := make([]RunStep, 10)
	for i := range steps {
		steps[i] = RunStep{StepType: "user", Content: "msg"}
	}
	if out := rehydrateHistory(steps, 3); len(out) != 3 {
		t.Fatalf("expected custom window of 3, got %d", len(out))
	}
	// Non-positive falls back to the package default.
	if out := rehydrateHistory(steps, 0); len(out) != 10 {
		t.Fatalf("expected default-window replay of all 10, got %d", len(out))
	}
}
