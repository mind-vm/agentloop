package agentloop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mind-vm/agentloop/llm"
)

// StepTypeSummary marks a compaction checkpoint in a session's step
// trace: a RunStep whose Content is the summary to replay in place of
// the steps it covers, and whose ToolArgs carries a
// CompactionCheckpoint saying where the retained history resumes.
//
// The steps a checkpoint covers stay in the trace — this changes only
// what is replayed to the model, never what a human reviewing the
// session can see.
const StepTypeSummary = "summary"

// CompactionCheckpoint is the ToolArgs payload of a StepTypeSummary
// step.
type CompactionCheckpoint struct {
	// RetainFromStep is the StepIndex the retained history resumes at.
	// Every earlier step is represented by the summary instead of being
	// replayed, so a session that has been compacted once does not pay
	// to summarize the same turns again on the next Run.
	RetainFromStep int32 `json:"retain_from_step"`
}

// decodeCheckpoint reads a checkpoint marker. A marker that will not
// parse is reported as an error rather than defaulted, because a
// zero-valued RetainFromStep would silently mean "retain everything" —
// replaying the full history AND the summary that was meant to replace
// it.
func decodeCheckpoint(raw json.RawMessage) (CompactionCheckpoint, error) {
	var cp CompactionCheckpoint
	if len(raw) == 0 {
		return cp, fmt.Errorf("agentloop: compaction checkpoint has no marker")
	}
	if err := json.Unmarshal(raw, &cp); err != nil {
		return cp, fmt.Errorf("agentloop: compaction checkpoint marker: %w", err)
	}
	return cp, nil
}

// executionResultPrefix labels the user turn that carries a run()'s
// logs back to the model. The loop writes it (run.go), history replay
// reconstructs it (history.go), and compaction recognises it to avoid
// splitting a script away from its own output — so it lives here, in
// one place, rather than as three copies of a magic string.
const executionResultPrefix = "Execution result:"

// Compactor shrinks a session's rehydrated history before the loop
// starts its turns, so a long conversation survives as a summary
// instead of being silently truncated.
//
// Compact receives the prior conversation in chronological order,
// WITHOUT the current user message (which the loop appends afterwards
// and always sends verbatim). It returns the history to actually send.
// Returning the input unchanged is always valid and is what an
// implementation should do when there is nothing worth compacting —
// the loop makes no assumption that the result is shorter.
//
// Tokens must report what the implementation spent, and must be
// populated even when Compact returns an error: a provider call that
// produced an unusable summary is still billable, and a Run's reported
// usage would otherwise understate what the session cost.
type Compactor interface {
	Compact(ctx context.Context, history []llm.Message) (CompactResult, error)
}

// CompactResult is one compaction attempt's output.
type CompactResult struct {
	// Messages is the history to send, chronological.
	Messages []llm.Message

	// Tokens is what producing it cost, zero for an implementation that
	// makes no provider call.
	Tokens TokenUsage

	// Summary, when non-empty, is the exact message content to replay
	// in place of the folded-away history on FUTURE runs. Setting it
	// (together with RetainedFrom) is what makes a compaction durable:
	// the loop persists it as a StepTypeSummary checkpoint, and the
	// next Run rehydrates from that instead of summarizing again.
	//
	// Leave it empty to compact for this run only. That costs a
	// summarization per Run, so an implementation that can express its
	// result as "one message replacing a prefix" should set it.
	Summary string

	// RetainedFrom is the index, in the history passed to Compact, of
	// the first message kept verbatim — everything before it is what
	// Summary stands for. Ignored when Summary is empty, and a value
	// outside (0, len(history)] disables persistence: a compaction that
	// folded nothing away has no checkpoint worth writing.
	RetainedFrom int
}

const (
	// defaultCompactTrigger is the history length (in messages) past
	// which SummarizingCompactor does any work at all.
	defaultCompactTrigger = 60

	// defaultCompactKeep is how many of the most recent messages stay
	// verbatim; everything older folds into the summary.
	defaultCompactKeep = 20

	// maxSummarizeBytes caps the transcript handed to the summarizing
	// model. A session long enough to need compaction can also be long
	// enough to overflow the context of the call meant to fix that, so
	// an oversized transcript keeps its head (the original ask) and its
	// tail (the most recent work) and elides the middle.
	maxSummarizeBytes = 200_000
)

// DefaultCompactPrompt is the instruction SummarizingCompactor sends
// when Prompt is empty. It is exported so an application can extend it
// rather than rewrite it from scratch.
const DefaultCompactPrompt = `You are compacting the earlier part of an agent session so it can be dropped from the context window without losing what matters.

The transcript interleaves three kinds of turn: what the user asked, JavaScript the agent wrote, and the logs that script produced. The agent's working data is threaded separately and is NOT in this transcript — do not claim a value you cannot see here.

Write a compact summary covering, in this order:
- what the user asked for, in their own terms, including any constraint they stated
- what has actually been established: findings, values, identifiers, and decisions the later turns will need
- what failed and why, so the agent does not retry a dead end
- what remains unfinished

Be specific — names, ids, numbers, paths — and omit anything the agent can trivially recompute. No preamble, no closing remark, no markdown headings. Prose and short bullets only.`

// SummarizingCompactor folds the older part of a long history into one
// summary message, keeping the most recent turns verbatim.
//
// Cost: a summarization happens when the rehydrated history passes
// Trigger. Because the loop persists each result as a checkpoint (see
// CompactResult.Summary), that is once per Trigger-worth of NEW
// conversation rather than once per Run — a session compacted at turn
// 60 does not pay again until it has grown back past Trigger. Trigger
// is the knob: raise it to pay less often and send more verbatim
// history, lower it for the reverse.
type SummarizingCompactor struct {
	// LLM performs the summarization. Required; a nil client makes
	// Compact a no-op that returns the history unchanged, so a missing
	// provider degrades to today's truncation rather than failing runs.
	//
	// Pointing this at a small, cheap model is usually right — the work
	// is extractive, and it keeps the per-Run cost noted above low.
	LLM llm.Client

	// Model overrides the client's default for the summarization call.
	Model string

	// Trigger is the history length, in messages, past which compaction
	// runs at all. Zero uses the package default (60).
	Trigger int

	// Keep is how many of the most recent messages stay verbatim. Zero
	// uses the package default (20). Clamped to half of Trigger when it
	// would otherwise leave nothing to summarize.
	Keep int

	// Prompt overrides DefaultCompactPrompt.
	Prompt string
}

// Compact implements Compactor.
func (c *SummarizingCompactor) Compact(ctx context.Context, history []llm.Message) (CompactResult, error) {
	trigger, keep := c.bounds()
	if c.LLM == nil || len(history) <= trigger {
		return CompactResult{Messages: history}, nil
	}

	split := safeSplitPoint(history, len(history)-keep)
	if split <= 0 {
		// Everything is one indivisible tail — nothing to fold away.
		return CompactResult{Messages: history}, nil
	}
	older, tail := history[:split], history[split:]

	prompt := c.Prompt
	if prompt == "" {
		prompt = DefaultCompactPrompt
	}
	resp, err := c.LLM.Complete(ctx, llm.CompletionRequest{
		Model: c.Model,
		Messages: []llm.Message{
			{Role: "system", Content: prompt},
			{Role: "user", Content: renderTranscript(older)},
		},
	})
	// Usage is reported on both paths — the call was billable either way.
	tokens := TokenUsage{Prompt: int32(resp.InputTokens), Completion: int32(resp.OutputTokens)}
	if err != nil {
		return CompactResult{Messages: history, Tokens: tokens}, fmt.Errorf("agentloop: compact: %w", err)
	}
	summary := strings.TrimSpace(resp.Content)
	if summary == "" {
		return CompactResult{Messages: history, Tokens: tokens}, fmt.Errorf("agentloop: compact: empty summary")
	}

	// The persisted Summary is the message content verbatim, so a
	// replayed checkpoint is indistinguishable from the compaction that
	// produced it.
	content := fmt.Sprintf("[summary of the %d earlier messages in this session, which are no longer shown in full]\n%s",
		len(older), summary)

	out := make([]llm.Message, 0, len(tail)+1)
	out = append(out, llm.Message{Role: "user", Content: content})
	return CompactResult{
		Messages:     append(out, tail...),
		Tokens:       tokens,
		Summary:      content,
		RetainedFrom: split,
	}, nil
}

// bounds resolves Trigger and Keep, guaranteeing keep < trigger so a
// compacted history is always strictly shorter than the one that
// triggered it — otherwise every Run would pay for a summarization that
// folds nothing away.
func (c *SummarizingCompactor) bounds() (trigger, keep int) {
	trigger, keep = c.Trigger, c.Keep
	if trigger <= 0 {
		trigger = defaultCompactTrigger
	}
	if keep <= 0 {
		keep = defaultCompactKeep
	}
	if keep >= trigger {
		keep = trigger / 2
	}
	return trigger, keep
}

// safeSplitPoint adjusts a desired split so the retained tail never
// begins with an execution result whose run() block is on the other
// side of the cut. An orphaned result reads to the model as output from
// a script it cannot see, which is worse than sending one more message.
//
// At most one step back is ever needed: an execution result is always
// immediately preceded by the assistant turn that produced it.
func safeSplitPoint(history []llm.Message, desired int) int {
	if desired <= 0 {
		return 0
	}
	if desired >= len(history) {
		return len(history)
	}
	if isExecutionResult(history[desired]) {
		return desired - 1
	}
	return desired
}

func isExecutionResult(m llm.Message) bool {
	return m.Role == "user" && strings.HasPrefix(m.Content, executionResultPrefix)
}

// renderTranscript flattens messages into the labelled plain text the
// summarizing model reads, bounded by maxSummarizeBytes: over the cap,
// the head and tail are kept and the middle is replaced by a marker
// saying how much was dropped.
func renderTranscript(messages []llm.Message) string {
	var b strings.Builder
	for _, m := range messages {
		label := m.Role
		if isExecutionResult(m) {
			label = "execution result"
		}
		fmt.Fprintf(&b, "[%s]\n%s\n\n", label, m.Content)
	}
	full := b.String()
	if len(full) <= maxSummarizeBytes {
		return full
	}
	half := maxSummarizeBytes / 2
	return full[:half] +
		fmt.Sprintf("\n\n[... %d bytes of the middle of this transcript omitted ...]\n\n", len(full)-maxSummarizeBytes) +
		full[len(full)-half:]
}
