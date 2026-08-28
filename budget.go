package agentloop

import (
	"fmt"

	"github.com/mind-vm/agentloop/llm"
)

// Everything else in this package bounds the prompt by COUNTING
// MESSAGES — HistoryWindow in steps, SummarizingCompactor.Trigger and
// .Keep in messages. That works when the model's context dwarfs the
// conversation, and fails quietly when it does not: a message can be
// forty tokens or four thousand, so the same 60-message history is
// comfortably small against a frontier model and a hard overflow
// against a locally hosted one. ContextBudget is the backstop that
// makes the ceiling explicit and the overflow deterministic.
//
// It is a BACKSTOP, not the primary mechanism. Compaction is what
// preserves meaning; this only guarantees the request is sendable.
// Configure both: the compactor folds the old turns into a summary,
// and the budget drops whatever still does not fit.

const (
	// bytesPerToken is the divisor EstimateTokens uses. Deliberately
	// low for English prose (~4.5 bytes/token in practice), because
	// over-estimating costs a little unused context and
	// under-estimating costs a rejected request.
	bytesPerToken = 4

	// messageFrameTokens charges each message for the role framing a
	// chat template adds around its content. Providers differ; this is
	// the usual ballpark and matters mainly for histories made of many
	// short turns, where the framing is a real share of the total.
	messageFrameTokens = 4

	// defaultReserveFraction of MaxTokens is held back for the
	// completion when ContextBudget.Reserve is zero, clamped to the
	// bounds below. A fraction rather than a constant so the default
	// stays sane across a 4k local model and a 200k hosted one.
	defaultReserveFraction = 8
	minDefaultReserve      = 512
	maxDefaultReserve      = 4096
)

// budgetDropMarker replaces the messages a trim removed. The model is
// told the turns are gone rather than left to infer it from a jump in
// the conversation — an agent that quietly assumes what it can no
// longer see is exactly the failure this package's prompt spends a
// paragraph warning against.
const budgetDropMarker = "[%d earlier messages in this session were dropped to fit the context window. They are gone — do not assume what they contained.]"

// EstimateTokens approximates how many tokens s occupies, without a
// tokenizer: byte length over a fixed divisor.
//
// It is intentionally crude. A real tokenizer is per-model, pulls in a
// vocabulary, and would have to be kept in step with providers this
// package deliberately knows nothing about — while the decision it
// feeds ("does one more message fit?") tolerates a wide margin, since
// the reserve absorbs the error. Supply ContextBudget.Estimate when
// you have the real thing and want the context filled tighter.
func EstimateTokens(s string) int {
	if s == "" {
		return 0
	}
	return (len(s) + bytesPerToken - 1) / bytesPerToken
}

// TokenEstimator reports how many tokens a string occupies.
type TokenEstimator func(string) int

// ContextBudget bounds a turn's prompt in TOKENS rather than messages.
// The zero value is disabled, which is why adding one to an existing
// Config changes nothing until MaxTokens is set.
//
// When enabled, the loop trims each turn's request immediately before
// sending it — after compaction, after stale-code elision, and after
// this run's own turns have grown the history. That placement is the
// point: history grows DURING a Run (every run() block and execution
// result appends to it), so a check performed once at rehydrate time
// would pass and then overflow three turns later.
//
// The system prompt and the most recent message are never dropped; the
// oldest conversational turns go first, and a marker records how many.
type ContextBudget struct {
	// MaxTokens is the model's context window. Zero disables budgeting
	// entirely — the loop then behaves exactly as it did before, with
	// the prompt bounded only by HistoryWindow and the Compactor.
	//
	// Set it to the SERVED window, which for a locally hosted model is
	// what the server was started with (llama.cpp's --ctx-size, Ollama's
	// num_ctx), not what the model card advertises.
	MaxTokens int

	// Reserve is how much of MaxTokens is held back for the completion.
	// Zero uses MaxTokens/8, clamped to [512, 4096] and never more than
	// half the window.
	//
	// It doubles as the request's output cap: when a budget is enabled
	// and CompletionRequest.MaxTokens would otherwise be unset, the
	// loop sends Reserve. Otherwise the reserve is a wish rather than a
	// guarantee — nothing would stop the model from generating past the
	// room left for it.
	Reserve int

	// Estimate overrides EstimateTokens. Supply a real tokenizer to
	// fill the window tighter; the default's error is absorbed by
	// Reserve.
	Estimate TokenEstimator
}

// enabled reports whether this budget does anything.
func (b ContextBudget) enabled() bool { return b.MaxTokens > 0 }

// reserve resolves Reserve, defaulting as documented on the field.
//
// An explicit Reserve is honoured exactly as given, including one too
// large to leave room for a prompt — New rejects that at construction
// rather than silently handing back half the window under a number the
// operator chose deliberately. Only the DEFAULT is clamped.
func (b ContextBudget) reserve() int {
	if b.Reserve > 0 {
		return b.Reserve
	}
	r := b.MaxTokens / defaultReserveFraction
	if r < minDefaultReserve {
		r = minDefaultReserve
	}
	if r > maxDefaultReserve {
		r = maxDefaultReserve
	}
	if half := b.MaxTokens / 2; r > half {
		r = half
	}
	return r
}

// allowance is how many tokens the prompt may occupy.
func (b ContextBudget) allowance() int { return b.MaxTokens - b.reserve() }

func (b ContextBudget) estimator() TokenEstimator {
	if b.Estimate != nil {
		return b.Estimate
	}
	return EstimateTokens
}

// completionCap is the output cap to send when the caller set none.
func (b ContextBudget) completionCap() (int, bool) {
	if !b.enabled() {
		return 0, false
	}
	return b.reserve(), true
}

// budgetFit is one trim's outcome.
type budgetFit struct {
	// Messages is the request to send.
	Messages []llm.Message

	// Dropped is how many conversational messages were removed.
	Dropped int

	// Tokens is the estimated size of Messages, zero when no budget is
	// configured (nothing was measured).
	Tokens int

	// Over reports that Messages STILL exceeds the allowance after
	// dropping everything droppable — the system prompt plus the
	// current message alone do not fit. Nothing more can be trimmed
	// without making the request meaningless, so the loop sends it
	// anyway and warns: the fix is a bigger window, fewer registered
	// packs, or a smaller persona, and the operator is the only one who
	// can apply it.
	Over bool
}

// fit trims messages to the budget, dropping the OLDEST conversational
// turns first.
//
// messages[0] must be the system prompt and is never dropped: without
// it the model loses the run()/answer() protocol and the entire tool
// surface, so a request that fits by discarding it is worse than one
// that overflows. The final message — the current user turn, or the
// args-shape digest that follows it — is likewise mandatory, since
// dropping what was just asked leaves nothing to answer.
func (b ContextBudget) fit(messages []llm.Message) budgetFit {
	if !b.enabled() {
		return budgetFit{Messages: messages}
	}
	est := b.estimator()
	allowance := b.allowance()

	// The floor: what survives no matter how tight the budget is.
	last := len(messages) - 1
	used := cost(est, messages[0])
	if last > 0 {
		used += cost(est, messages[last])
	}
	if len(messages) < 3 {
		// Nothing between the system prompt and the current turn, so
		// there is nothing this can drop.
		return budgetFit{Messages: messages, Tokens: used, Over: used > allowance}
	}

	// Charge the marker up front, since a trim will insert one. Sized
	// against len(messages), an upper bound on the count it will
	// report, so the estimate can only be conservative. Costing a few
	// tokens when nothing ends up being dropped is the right way round.
	used += est(fmt.Sprintf(budgetDropMarker, len(messages))) + messageFrameTokens

	keepFrom := last
	for i := last - 1; i >= 1; i-- {
		c := cost(est, messages[i])
		if used+c > allowance {
			break
		}
		used += c
		keepFrom = i
	}

	// Never begin the retained history with an execution result whose
	// run() block was just dropped — orphaned output reads to the model
	// as the result of a script it cannot see. compact.go solves the
	// same problem by moving the cut BACK to include the block; here
	// the cut moves FORWARD and drops the result too, because at a hard
	// ceiling "keep one more message" is not available.
	for keepFrom < last && isExecutionResult(messages[keepFrom]) {
		used -= cost(est, messages[keepFrom])
		keepFrom++
	}

	if keepFrom <= 1 {
		// Everything fit. Re-measure without the marker that was
		// charged for but never inserted.
		return budgetFit{
			Messages: messages,
			Tokens:   used - est(fmt.Sprintf(budgetDropMarker, len(messages))) - messageFrameTokens,
			Over:     false,
		}
	}

	dropped := keepFrom - 1
	out := make([]llm.Message, 0, len(messages)-dropped+1)
	out = append(out, messages[0])
	out = append(out, llm.Message{Role: "user", Content: fmt.Sprintf(budgetDropMarker, dropped)})
	out = append(out, messages[keepFrom:]...)
	return budgetFit{
		Messages: out,
		Dropped:  dropped,
		Tokens:   used,
		Over:     used > allowance,
	}
}

// cost is one message's token estimate, role framing included.
func cost(est TokenEstimator, m llm.Message) int {
	return est(m.Content) + messageFrameTokens
}
