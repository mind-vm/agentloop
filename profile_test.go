package agentloop

import (
	"context"
	"testing"

	"github.com/mind-vm/agentloop/llm"
	"github.com/mind-vm/agentloop/sandbox"
)

func TestProfileForUnknownWindowChangesNothing(t *testing.T) {
	p := ProfileFor(0)
	if p.ContextBudget.enabled() || p.HistoryWindow != 0 || p.CompactTrigger != 0 || p.MaxLogBytes != 0 {
		t.Fatalf("ProfileFor(0) = %+v, want the zero Profile", p)
	}

	// And applying it leaves a Config exactly as it was — "I don't know
	// the window" must degrade to the package defaults, not to a guess.
	cfg := Config{HistoryWindow: 80}
	p.Apply(&cfg)
	if cfg.HistoryWindow != 80 || cfg.ContextBudget.enabled() {
		t.Fatalf("zero Profile wrote settings: %+v", cfg)
	}
	ProfileFor(-1).Apply(&cfg)
	if cfg.HistoryWindow != 80 {
		t.Fatal("negative window wrote settings")
	}
}

// The knobs a profile sets have to be consistent with each other, not
// merely individually plausible: a Trigger the history window cannot
// reach never fires, and a summarize budget larger than the window
// re-introduces the overflow the budget exists to prevent.
func TestProfileIsInternallyConsistent(t *testing.T) {
	for _, window := range []int{2048, 4096, 8192, 32_768, 128_000, 1_000_000} {
		p := ProfileFor(window)

		if p.ContextBudget.MaxTokens != window {
			t.Errorf("window %d: budget MaxTokens = %d", window, p.ContextBudget.MaxTokens)
		}
		if p.CompactKeep >= p.CompactTrigger {
			t.Errorf("window %d: Keep %d >= Trigger %d — compaction would fold nothing away",
				window, p.CompactKeep, p.CompactTrigger)
		}
		if p.HistoryWindow < p.CompactTrigger {
			t.Errorf("window %d: HistoryWindow %d below Trigger %d — compaction could never fire",
				window, p.HistoryWindow, p.CompactTrigger)
		}
		if p.SummarizeBudget.MaxTokens > window {
			t.Errorf("window %d: summarize budget %d exceeds the window", window, p.SummarizeBudget.MaxTokens)
		}
		// One standing item must not be able to crowd out the
		// conversation it exists to inform.
		allowance := p.ContextBudget.allowance()
		if EstimateTokens(string(make([]byte, p.MaxLogBytes))) > allowance/2 {
			t.Errorf("window %d: MaxLogBytes %d is over half the %d-token allowance", window, p.MaxLogBytes, allowance)
		}
		if p.MaxLogBytes > sandbox.DefaultMaxLogBytes {
			t.Errorf("window %d: MaxLogBytes %d above the package default", window, p.MaxLogBytes)
		}
	}
}

// A small window must actually produce small settings — the whole point
// is that the frontier-model defaults are wrong by an order of
// magnitude, not slightly.
func TestProfileScalesWithTheWindow(t *testing.T) {
	small := ProfileFor(4096)
	large := ProfileFor(200_000)

	if small.CompactTrigger >= large.CompactTrigger {
		t.Errorf("Trigger did not scale: 4k -> %d, 200k -> %d", small.CompactTrigger, large.CompactTrigger)
	}
	if small.HistoryWindow >= large.HistoryWindow {
		t.Errorf("HistoryWindow did not scale: 4k -> %d, 200k -> %d", small.HistoryWindow, large.HistoryWindow)
	}
	if small.MaxLogBytes >= large.MaxLogBytes {
		t.Errorf("MaxLogBytes did not scale: 4k -> %d, 200k -> %d", small.MaxLogBytes, large.MaxLogBytes)
	}

	// At a large window the profile should land on the package
	// defaults, so adopting it changes nothing for a hosted model.
	if large.CompactTrigger != defaultCompactTrigger {
		t.Errorf("large-window Trigger = %d, want the package default %d", large.CompactTrigger, defaultCompactTrigger)
	}
	if large.CompactKeep != defaultCompactKeep {
		t.Errorf("large-window Keep = %d, want the package default %d", large.CompactKeep, defaultCompactKeep)
	}
	if large.MaxLogBytes != sandbox.DefaultMaxLogBytes {
		t.Errorf("large-window MaxLogBytes = %d, want the package default %d", large.MaxLogBytes, sandbox.DefaultMaxLogBytes)
	}
}

func TestProfileAppliesToConfigAndCompactor(t *testing.T) {
	sc := &SummarizingCompactor{Trigger: 999, Keep: 999}
	cfg := Config{Compactor: sc}
	p := ProfileFor(8192)

	p.Apply(&cfg)

	if cfg.ContextBudget.MaxTokens != p.ContextBudget.MaxTokens {
		t.Errorf("ContextBudget.MaxTokens = %d, want %d", cfg.ContextBudget.MaxTokens, p.ContextBudget.MaxTokens)
	}
	if cfg.HistoryWindow != p.HistoryWindow {
		t.Errorf("HistoryWindow = %d, want %d", cfg.HistoryWindow, p.HistoryWindow)
	}
	if sc.Trigger != p.CompactTrigger || sc.Keep != p.CompactKeep {
		t.Errorf("compactor = %d/%d, want %d/%d", sc.Trigger, sc.Keep, p.CompactTrigger, p.CompactKeep)
	}
	if sc.Budget.MaxTokens != p.SummarizeBudget.MaxTokens {
		t.Errorf("compactor Budget.MaxTokens = %d, want %d", sc.Budget.MaxTokens, p.SummarizeBudget.MaxTokens)
	}
}

// A custom Compactor has its own knobs; writing this profile's into it
// is not possible, and pretending otherwise would be worse than leaving
// it alone.
func TestProfileLeavesACustomCompactorAlone(t *testing.T) {
	custom := &passthroughCompactor{}
	cfg := Config{Compactor: custom}

	ProfileFor(8192).Apply(&cfg)

	if cfg.Compactor != custom {
		t.Fatal("Apply replaced a custom compactor")
	}
	if !cfg.ContextBudget.enabled() {
		t.Fatal("Apply skipped the Config-level settings because of the custom compactor")
	}
}

func TestProfileApplyToleratesANilConfig(t *testing.T) {
	ProfileFor(8192).Apply(nil) // must not panic
}

// passthroughCompactor is a Compactor that is not a
// *SummarizingCompactor — enough to prove Apply leaves one alone.
type passthroughCompactor struct{}

func (passthroughCompactor) Compact(context.Context, []llm.Message) (CompactResult, error) {
	return CompactResult{}, nil
}
