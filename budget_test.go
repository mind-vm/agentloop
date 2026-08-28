package agentloop

import (
	"fmt"
	"strings"
	"testing"

	"github.com/mind-vm/agentloop/llm"
)

// msg builds a message whose content is n bytes, so a test can state
// sizes in tokens (EstimateTokens divides bytes by 4).
func msg(role string, tokens int) llm.Message {
	return llm.Message{Role: role, Content: strings.Repeat("x", tokens*bytesPerToken)}
}

func TestContextBudgetZeroValueIsDisabled(t *testing.T) {
	messages := []llm.Message{msg("system", 1000), msg("user", 1000), msg("assistant", 1000), msg("user", 1000)}

	fit := ContextBudget{}.fit(messages)

	if len(fit.Messages) != len(messages) {
		t.Fatalf("zero-value budget trimmed %d messages; want none dropped", len(messages)-len(fit.Messages))
	}
	if fit.Dropped != 0 || fit.Tokens != 0 || fit.Over {
		t.Fatalf("zero-value budget reported work: %+v", fit)
	}
}

func TestContextBudgetKeepsEverythingWhenItFits(t *testing.T) {
	messages := []llm.Message{msg("system", 100), msg("user", 100), msg("assistant", 100), msg("user", 100)}

	fit := ContextBudget{MaxTokens: 8000}.fit(messages)

	if fit.Dropped != 0 {
		t.Fatalf("dropped %d messages that fit", fit.Dropped)
	}
	if len(fit.Messages) != len(messages) {
		t.Fatalf("message count changed: got %d, want %d", len(fit.Messages), len(messages))
	}
	// Tokens must exclude the drop marker that was charged for but
	// never inserted, or a caller watching headroom sees phantom usage.
	if want := 4 * (100 + messageFrameTokens); fit.Tokens != want {
		t.Fatalf("Tokens = %d, want %d (marker must not be counted when nothing was dropped)", fit.Tokens, want)
	}
}

func TestContextBudgetDropsOldestFirst(t *testing.T) {
	messages := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "oldest"},
		{Role: "assistant", Content: "middle"},
		{Role: "user", Content: "newest"},
	}
	// Allowance of ~13 tokens: the floor (system + newest + marker) eats
	// most of it, leaving room for neither of the two middle turns.
	budget := ContextBudget{MaxTokens: 100, Reserve: 87}

	fit := budget.fit(messages)

	if fit.Dropped != 2 {
		t.Fatalf("Dropped = %d, want 2", fit.Dropped)
	}
	if got := fit.Messages[0].Content; got != "sys" {
		t.Fatalf("system prompt not first: %q", got)
	}
	if got := fit.Messages[len(fit.Messages)-1].Content; got != "newest" {
		t.Fatalf("current turn not retained: %q", got)
	}
	for _, m := range fit.Messages {
		if m.Content == "oldest" || m.Content == "middle" {
			t.Fatalf("older turn %q survived a trim that should have dropped it", m.Content)
		}
	}
}

func TestContextBudgetMarksWhatItDropped(t *testing.T) {
	messages := []llm.Message{
		{Role: "system", Content: "sys"},
		msg("user", 500),
		msg("assistant", 500),
		{Role: "user", Content: "newest"},
	}

	fit := ContextBudget{MaxTokens: 400}.fit(messages)

	if fit.Dropped != 2 {
		t.Fatalf("Dropped = %d, want 2", fit.Dropped)
	}
	want := fmt.Sprintf(budgetDropMarker, 2)
	if fit.Messages[1].Content != want {
		t.Fatalf("marker = %q, want %q", fit.Messages[1].Content, want)
	}
	if fit.Messages[1].Role != "user" {
		t.Fatalf("marker role = %q, want user", fit.Messages[1].Role)
	}
}

// An execution result whose run() block was dropped reads to the model
// as output from a script it cannot see. compact.go avoids that by
// keeping one more message; a hard ceiling has no such option, so the
// orphan must go too.
func TestContextBudgetNeverStrandsAnExecutionResult(t *testing.T) {
	messages := []llm.Message{
		{Role: "system", Content: "sys"},
		msg("user", 500),
		{Role: "assistant", Content: "```javascript\nfunction run(args) { return 1; }\n```"},
		{Role: "user", Content: executionResultPrefix + "\nlogged something"},
		{Role: "user", Content: "newest"},
	}
	// Sized so the retained tail would otherwise start at the execution
	// result, with its run() block just past the cut: the floor
	// (system + newest + marker) is 48 tokens, the execution result
	// adds 13 and the run() block that produced it another 17, so a
	// 70-token allowance admits exactly the orphan.
	budget := ContextBudget{MaxTokens: 140, Reserve: 70}

	fit := budget.fit(messages)

	for _, m := range fit.Messages {
		if isExecutionResult(m) {
			t.Fatalf("execution result retained without its run() block:\n%#v", fit.Messages)
		}
	}
	if fit.Dropped != 3 {
		t.Fatalf("Dropped = %d, want 3 (the orphaned result counts)", fit.Dropped)
	}
	if got := fit.Messages[len(fit.Messages)-1].Content; got != "newest" {
		t.Fatalf("retained tail = %q, want it to resume at the current turn", got)
	}
}

func TestContextBudgetReportsOverWhenTheFloorDoesNotFit(t *testing.T) {
	messages := []llm.Message{msg("system", 5000), msg("user", 100), msg("user", 100)}

	fit := ContextBudget{MaxTokens: 2000}.fit(messages)

	if !fit.Over {
		t.Fatal("Over = false; a system prompt larger than the whole window must be reported")
	}
	// Sending a truncated-to-nothing request is worse than sending one
	// that may be rejected — the floor survives.
	if len(fit.Messages) < 2 {
		t.Fatalf("floor not preserved: %d messages", len(fit.Messages))
	}
	if fit.Messages[0].Role != "system" {
		t.Fatalf("system prompt dropped: %q", fit.Messages[0].Role)
	}
	if got := fit.Messages[len(fit.Messages)-1].Content; got != messages[2].Content {
		t.Fatal("current turn dropped")
	}
}

func TestContextBudgetShortRequestIsNeverTrimmed(t *testing.T) {
	// Two messages have nothing droppable between them, however far
	// over budget they are.
	messages := []llm.Message{msg("system", 5000), msg("user", 5000)}

	fit := ContextBudget{MaxTokens: 1000}.fit(messages)

	if fit.Dropped != 0 || len(fit.Messages) != 2 {
		t.Fatalf("trimmed an untrimmable request: %+v", fit)
	}
	if !fit.Over {
		t.Fatal("Over = false; the request plainly exceeds the window")
	}
}

func TestContextBudgetUsesTheSuppliedEstimator(t *testing.T) {
	calls := 0
	budget := ContextBudget{
		MaxTokens: 1000,
		Estimate: func(s string) int {
			calls++
			return len(s) // one token per byte
		},
	}
	messages := []llm.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: strings.Repeat("x", 900)},
		{Role: "user", Content: "newest"},
	}

	fit := budget.fit(messages)

	if calls == 0 {
		t.Fatal("custom estimator never called")
	}
	// Under EstimateTokens the 900-byte turn is ~225 tokens and fits
	// comfortably; at one token per byte it does not.
	if fit.Dropped != 1 {
		t.Fatalf("Dropped = %d, want 1 — the custom estimator was not what decided", fit.Dropped)
	}
}

func TestContextBudgetReserveDefaults(t *testing.T) {
	cases := []struct {
		name      string
		budget    ContextBudget
		reserve   int
		allowance int
	}{
		{"small local window clamps to the floor", ContextBudget{MaxTokens: 4096}, 512, 3584},
		{"large window clamps to the ceiling", ContextBudget{MaxTokens: 200_000}, 4096, 195_904},
		{"mid window uses the fraction", ContextBudget{MaxTokens: 16_384}, 2048, 14_336},
		{"tiny window never reserves past half", ContextBudget{MaxTokens: 600}, 300, 300},
		{"explicit reserve is honoured verbatim", ContextBudget{MaxTokens: 8192, Reserve: 6000}, 6000, 2192},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.budget.reserve(); got != tc.reserve {
				t.Errorf("reserve() = %d, want %d", got, tc.reserve)
			}
			if got := tc.budget.allowance(); got != tc.allowance {
				t.Errorf("allowance() = %d, want %d", got, tc.allowance)
			}
		})
	}
}

func TestContextBudgetCompletionCap(t *testing.T) {
	if _, ok := (ContextBudget{}).completionCap(); ok {
		t.Fatal("disabled budget offered a completion cap")
	}
	got, ok := ContextBudget{MaxTokens: 8192}.completionCap()
	if !ok {
		t.Fatal("enabled budget offered no completion cap")
	}
	if got != 1024 {
		t.Fatalf("completionCap() = %d, want 1024 (the resolved reserve)", got)
	}
}

func TestEstimateTokens(t *testing.T) {
	if got := EstimateTokens(""); got != 0 {
		t.Fatalf("EstimateTokens(\"\") = %d, want 0", got)
	}
	// Rounds up: a partial token still occupies one.
	if got := EstimateTokens("abcde"); got != 2 {
		t.Fatalf("EstimateTokens(5 bytes) = %d, want 2", got)
	}
}
