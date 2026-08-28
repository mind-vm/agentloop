package agentloop

import "github.com/mind-vm/agentloop/sandbox"

// A model's context window sets four separate limits that the rest of
// this package exposes as unrelated knobs, in three different units:
// ContextBudget in tokens, HistoryWindow in steps, the compactor's
// Trigger and Keep in messages, log output in bytes. Each has a
// defensible default, and all of those defaults assume a frontier
// model. Against an 8k local model the defaults are not slightly wrong,
// they are wrong by an order of magnitude — and getting them right by
// hand means four separate unit conversions from one number the
// operator already knows.
//
// Profile is that one number, expanded.

const (
	// avgMessageTokens is what one conversational turn costs, used to
	// convert a token allowance into the MESSAGE counts the compactor
	// is configured in. Empirical and rough: a run() block, an
	// execution result, or a paragraph of answer all land near it, and
	// the figure only has to be right to within a factor of two for the
	// derived Trigger to be sensible.
	avgMessageTokens = 250

	// The compactor's derived Trigger is clamped to this range. The
	// floor keeps a session from compacting so often that it spends
	// more on summarizing than on answering; the ceiling is the
	// package default, which is already as much verbatim history as is
	// useful to send.
	minProfileTrigger = 12
	maxProfileTrigger = defaultCompactTrigger

	// HistoryWindow is derived as a multiple of Trigger: enough past
	// that compaction has real material to fold, bounded so a small
	// model does not pay to summarize a history it could never hold.
	historyPerTrigger = 3
	minProfileHistory = 24
	maxProfileHistory = 200

	// A single standing item — one turn's logs, one project-instruction
	// excerpt — may claim this fraction of the allowance. Anything
	// larger crowds out the conversation it is supposed to inform.
	standingItemDivisor = 8
	minStandingBytes    = 1024

	// defaultProjectInlineBytes mirrors projectctx.DefaultInlineBytes,
	// which this package cannot import: projectctx imports agentloop,
	// and the dependency does not run the other way. The two are pinned
	// together by a test in projectctx, which CAN see both.
	defaultProjectInlineBytes = 4 * 1024
)

// Profile is a coherent set of context settings derived from one
// model's context window.
//
// It is a starting point, not a constraint: read the fields, change
// what your workload justifies. Every value it produces is one you
// could have written by hand.
type Profile struct {
	// ContextBudget bounds each turn's prompt. Goes in Config.
	ContextBudget ContextBudget

	// HistoryWindow is how many steps the loop rehydrates. Goes in
	// Config.
	HistoryWindow int

	// CompactTrigger and CompactKeep configure a SummarizingCompactor,
	// in messages.
	CompactTrigger int
	CompactKeep    int

	// SummarizeBudget bounds one summarization call. Set it on
	// SummarizingCompactor.Budget when the summarizer runs on the same
	// model — the common case for a local setup. Point the compactor at
	// a larger hosted model and this is the field to override.
	SummarizeBudget ContextBudget

	// MaxLogBytes caps one turn's log() output. Goes in
	// DefaultSandboxBuilder.MaxLogBytes.
	MaxLogBytes int

	// ProjectInlineBytes is how much of each instruction file belongs in
	// the prompt. Goes in projectctx.Loader.InlineBytes — which this
	// package cannot set for you, since nothing in the core imports
	// projectctx.
	ProjectInlineBytes int
}

// ProfileFor derives context settings for a model with the given
// context window, in tokens.
//
// Use the window the model is actually SERVED with: for a local runtime
// that is the server's configured context size (llama.cpp's
// --ctx-size, Ollama's num_ctx), not the figure on the model card. A
// window the server will not honour produces a profile that fits
// nothing.
//
// A non-positive window returns the zero Profile, whose Apply is a
// no-op — so "I don't know the window" degrades to today's defaults
// rather than to a guess.
func ProfileFor(contextWindow int) Profile {
	if contextWindow <= 0 {
		return Profile{}
	}
	budget := ContextBudget{MaxTokens: contextWindow}
	allowance := budget.allowance()

	trigger := clamp(allowance/avgMessageTokens, minProfileTrigger, maxProfileTrigger)
	keep := trigger / 3
	if keep < 4 {
		keep = 4
	}

	standing := allowance / standingItemDivisor * bytesPerToken

	return Profile{
		ContextBudget:   budget,
		HistoryWindow:   clamp(trigger*historyPerTrigger, minProfileHistory, maxProfileHistory),
		CompactTrigger:  trigger,
		CompactKeep:     keep,
		SummarizeBudget: budget,
		// Both caps are the same quantity — one standing claim on the
		// prompt — differing only in the ceiling each package already
		// considers reasonable.
		MaxLogBytes:        clamp(standing, minStandingBytes, sandbox.DefaultMaxLogBytes),
		ProjectInlineBytes: clamp(standing, minStandingBytes, defaultProjectInlineBytes),
	}
}

// Apply writes the profile's loop-level settings into cfg: everything
// Config itself owns, plus the compactor's message counts and budget
// when cfg.Compactor is a *SummarizingCompactor.
//
// It does NOT touch the sandbox builder or the project-instruction
// loader, which are separate objects the application constructs —
// pass Profile.MaxLogBytes and Profile.ProjectInlineBytes to those
// yourself:
//
//	p := agentloop.ProfileFor(8192)
//	docs, _ := projectctx.Loader{InlineBytes: p.ProjectInlineBytes}.Load(cwd)
//	cfg := agentloop.Config{
//	    LLM: client, Sessions: sessions, Steps: steps,
//	    SandboxBuilder: &agentloop.DefaultSandboxBuilder{
//	        Capabilities: caps,
//	        MaxLogBytes:  p.MaxLogBytes,
//	    },
//	    Compactor: &agentloop.SummarizingCompactor{LLM: client},
//	}
//	p.Apply(&cfg)
//
// Apply overwrites rather than merges: it is the profile's settings
// that end up in cfg, not a blend. Call it BEFORE any hand-tuning you
// want to survive. A zero Profile (from ProfileFor(0)) writes nothing.
func (p Profile) Apply(cfg *Config) {
	if cfg == nil || p.ContextBudget.MaxTokens <= 0 {
		return
	}
	cfg.ContextBudget = p.ContextBudget
	cfg.HistoryWindow = p.HistoryWindow

	// Only the batteries-included compactor can be configured from
	// here; a custom Compactor is left alone, since its knobs are its
	// own and silently doing nothing is better than silently doing the
	// wrong thing.
	if sc, ok := cfg.Compactor.(*SummarizingCompactor); ok {
		sc.Trigger = p.CompactTrigger
		sc.Keep = p.CompactKeep
		sc.Budget = p.SummarizeBudget
	}
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
