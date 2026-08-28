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

	// defaultSummarizeTokens bounds ONE summarization call's transcript
	// when SummarizingCompactor.Budget is unset. Roughly what the byte
	// cap this replaced worked out to, so an existing configuration
	// keeps chunking at about the size it used to elide at.
	defaultSummarizeTokens = 50_000

	// minChunkTokens floors the per-call allowance. A budget whose
	// window is smaller than its own instruction prompt would otherwise
	// compute a non-positive allowance and chunk forever; making no
	// progress is worse than one oversized call the provider may still
	// accept.
	minChunkTokens = 1_000

	// maxFoldRounds bounds how many times partial summaries are merged
	// into fewer, shorter ones. Each round strictly shrinks its input,
	// so hitting this means the model is returning summaries as long as
	// what it was given — a runaway to stop, not to keep paying for.
	maxFoldRounds = 5
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

// DefaultMergePrompt is the instruction sent when several partial
// summaries have to be folded into one, i.e. when the transcript did
// not fit a single summarization call. Overridable via
// SummarizingCompactor.MergePrompt.
//
// It is a separate instruction from DefaultCompactPrompt because the
// input is a different kind of thing: already-summarized prose rather
// than a transcript of turns, where the risk is concatenating
// superseded facts rather than losing specifics.
const DefaultMergePrompt = `You are merging several partial summaries of ONE agent session into a single summary.

The inputs are consecutive stretches of the same session, in order. Later parts continue the work of earlier ones: a fact established early is still true later unless a later part says otherwise, and a value a later part corrects supersedes the earlier one.

Write one summary in the same shape as its inputs, covering, in this order:
- what the user asked for, in their own terms, including any constraint they stated
- what has actually been established: findings, values, identifiers, and decisions the later turns will need
- what failed and why, so the agent does not retry a dead end
- what remains unfinished

Reconcile rather than concatenate. Drop what a later part superseded, keep every specific still in play — names, ids, numbers, paths — and never introduce a detail no input states. No preamble, no closing remark, no markdown headings. Prose and short bullets only.`

// SummarizingCompactor folds the older part of a long history into one
// summary message, keeping the most recent turns verbatim.
//
// A session long enough to need compaction can be long enough to
// overflow the context of the call meant to fix that, so the
// transcript is summarized in CHUNKS that each fit one call and the
// partial summaries are then folded together — repeatedly, until one
// remains. Nothing is dropped to make the transcript fit: a stretch of
// the session that a single-call summarizer would have elided is
// summarized like any other. The only surviving elision is for one
// individual message too large for a whole call on its own.
//
// Budget is what sizes a chunk. Left unset it defaults to roughly the
// old single-call bound, which is the right answer against a hosted
// model; point it at a small local model's window and the same
// transcript is simply summarized in more, smaller pieces.
//
// Cost: a summarization happens when the rehydrated history passes
// Trigger. Because the loop persists each result as a checkpoint (see
// CompactResult.Summary), that is once per Trigger-worth of NEW
// conversation rather than once per Run — a session compacted at turn
// 60 does not pay again until it has grown back past Trigger. Trigger
// is the knob: raise it to pay less often and send more verbatim
// history, lower it for the reverse. Chunking multiplies the calls one
// compaction makes, so a small Budget against a large Trigger is the
// combination that costs most.
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

	// MergePrompt overrides DefaultMergePrompt, the instruction used
	// when partial summaries have to be folded together. Unused when
	// the transcript fits a single call.
	MergePrompt string

	// Budget bounds ONE summarization call against the window of the
	// model doing the summarizing — which is not the loop's model, and
	// is often deliberately smaller and cheaper. Its MaxTokens is that
	// window and its Reserve the room left for the summary itself.
	//
	// Zero uses an internal default sized to the bound this replaced,
	// which suits a hosted summarizer. Set it when the summarizer is
	// locally hosted: it is the difference between chunking to fit and
	// sending one call the server will reject.
	Budget ContextBudget
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

	// Usage accumulates across every call a chunked compaction makes and
	// is reported on both paths — those calls were billable either way.
	var tokens TokenUsage
	summary, err := c.summarize(ctx, older, &tokens)
	if err != nil {
		return CompactResult{Messages: history, Tokens: tokens}, err
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

// summarize folds a transcript into one summary, in as many calls as
// it takes.
//
// The transcript is split into chunks that each fit one call, every
// chunk is summarized, and the partial summaries are folded together
// until one remains. A transcript that fits in a single chunk takes the
// single-call path unchanged — no part headers, no merge pass — so the
// common case is exactly what it was before chunking existed.
//
// A failed chunk fails the whole compaction. Continuing without it
// would erase that stretch of the session while presenting the result
// as a complete summary, and the loop's fallback (proceed on the
// uncompacted history) is strictly better than a summary with a
// silent hole in it.
func (c *SummarizingCompactor) summarize(ctx context.Context, messages []llm.Message, tokens *TokenUsage) (string, error) {
	allowance, est := c.chunkBounds()
	chunks := transcriptChunks(messages, allowance, est)

	if len(chunks) == 1 {
		return c.call(ctx, c.compactPrompt(), chunks[0], tokens)
	}

	parts := make([]string, 0, len(chunks))
	for i, chunk := range chunks {
		// Say which stretch this is, so the model does not read the end
		// of a middle chunk as the end of the session and report work
		// as abandoned when the next chunk finishes it.
		body := fmt.Sprintf("Part %d of %d of the earlier transcript.\n\n%s", i+1, len(chunks), chunk)
		part, err := c.call(ctx, c.compactPrompt(), body, tokens)
		if err != nil {
			return "", err
		}
		parts = append(parts, part)
	}
	return c.fold(ctx, parts, allowance, est, tokens)
}

// fold merges partial summaries until one remains, in rounds, so a
// session whose summaries alone overflow a call still converges.
func (c *SummarizingCompactor) fold(ctx context.Context, parts []string, allowance int, est TokenEstimator, tokens *TokenUsage) (string, error) {
	for round := 1; len(parts) > 1; round++ {
		if round > maxFoldRounds {
			return "", fmt.Errorf("agentloop: compact: %d partial summaries still do not fit one call after %d fold rounds", len(parts), maxFoldRounds)
		}
		groups := groupParts(parts, allowance, est)
		next := make([]string, 0, len(groups))
		for _, group := range groups {
			merged, err := c.call(ctx, c.mergePrompt(), joinParts(group), tokens)
			if err != nil {
				return "", err
			}
			next = append(next, merged)
		}
		parts = next
	}
	return parts[0], nil
}

// call performs one summarization round-trip, charging its usage to
// tokens whether or not it succeeded.
func (c *SummarizingCompactor) call(ctx context.Context, prompt, body string, tokens *TokenUsage) (string, error) {
	resp, err := c.LLM.Complete(ctx, llm.CompletionRequest{
		Model: c.Model,
		Messages: []llm.Message{
			{Role: "system", Content: prompt},
			{Role: "user", Content: body},
		},
	})
	tokens.Prompt += int32(resp.InputTokens)
	tokens.Completion += int32(resp.OutputTokens)
	if err != nil {
		return "", fmt.Errorf("agentloop: compact: %w", err)
	}
	summary := strings.TrimSpace(resp.Content)
	if summary == "" {
		return "", fmt.Errorf("agentloop: compact: empty summary")
	}
	return summary, nil
}

func (c *SummarizingCompactor) compactPrompt() string {
	if c.Prompt != "" {
		return c.Prompt
	}
	return DefaultCompactPrompt
}

func (c *SummarizingCompactor) mergePrompt() string {
	if c.MergePrompt != "" {
		return c.MergePrompt
	}
	return DefaultMergePrompt
}

// chunkBounds resolves how much TRANSCRIPT one call may carry: the
// budget's allowance less the instruction that shares the request with
// it. The larger of the two prompts is subtracted, since a chunk sized
// for the compact instruction would not fit alongside the merge one.
func (c *SummarizingCompactor) chunkBounds() (int, TokenEstimator) {
	est := c.Budget.estimator()

	allowance := defaultSummarizeTokens
	if c.Budget.enabled() {
		allowance = c.Budget.allowance()
	}

	overhead := est(c.compactPrompt())
	if merge := est(c.mergePrompt()); merge > overhead {
		overhead = merge
	}
	allowance -= overhead + 2*messageFrameTokens

	if allowance < minChunkTokens {
		allowance = minChunkTokens
	}
	return allowance, est
}

// transcriptChunks renders messages into labelled plain text, split
// into pieces that each fit allowance tokens.
//
// Splits fall on message boundaries: a chunk is whole turns, so the
// summarizing model never reads half a run() block. The one exception
// is a single message too large for a chunk of its own — that one gets
// its middle elided, which is the last remaining place this package
// discards transcript rather than summarizing it.
//
// Always returns at least one chunk, so the caller's single-chunk fast
// path is reachable for an empty transcript too.
func transcriptChunks(messages []llm.Message, allowance int, est TokenEstimator) []string {
	var chunks []string
	var cur strings.Builder
	curTokens := 0

	flush := func() {
		if cur.Len() > 0 {
			chunks = append(chunks, cur.String())
			cur.Reset()
			curTokens = 0
		}
	}

	for _, m := range messages {
		part := renderMessage(m)
		n := est(part)
		if n > allowance {
			flush()
			chunks = append(chunks, elideMiddle(part, allowance, est))
			continue
		}
		if curTokens+n > allowance {
			flush()
		}
		cur.WriteString(part)
		curTokens += n
	}
	flush()

	if len(chunks) == 0 {
		return []string{""}
	}
	return chunks
}

// renderMessage is one turn as the summarizing model reads it.
func renderMessage(m llm.Message) string {
	label := m.Role
	if isExecutionResult(m) {
		label = "execution result"
	}
	return fmt.Sprintf("[%s]\n%s\n\n", label, m.Content)
}

// renderTranscript flattens messages into the labelled plain text the
// summarizing model reads, unbounded. Bounding is transcriptChunks'
// job — this is what a chunk is made of.
func renderTranscript(messages []llm.Message) string {
	var b strings.Builder
	for _, m := range messages {
		b.WriteString(renderMessage(m))
	}
	return b.String()
}

// groupParts batches partial summaries into merge calls, each group
// small enough to fit one.
//
// A single part too large for a group of its own still gets one, alone:
// re-summarizing it shrinks it, which is what lets the next fold round
// make progress where this one could not.
func groupParts(parts []string, allowance int, est TokenEstimator) [][]string {
	var groups [][]string
	var cur []string
	curTokens := 0
	for _, p := range parts {
		n := est(p) + messageFrameTokens
		if len(cur) > 0 && curTokens+n > allowance {
			groups = append(groups, cur)
			cur, curTokens = nil, 0
		}
		cur = append(cur, p)
		curTokens += n
	}
	if len(cur) > 0 {
		groups = append(groups, cur)
	}
	return groups
}

// joinParts labels each partial summary with its position, so the merge
// instruction's "later parts supersede earlier ones" has something to
// bind to.
func joinParts(parts []string) string {
	if len(parts) == 1 {
		return parts[0]
	}
	var b strings.Builder
	for i, p := range parts {
		fmt.Fprintf(&b, "[part %d of %d]\n%s\n\n", i+1, len(parts), p)
	}
	return strings.TrimSpace(b.String())
}

// elideMiddle shrinks s to at most max tokens by keeping its head and
// its tail and replacing the middle with a marker saying how much went.
// Head and tail because, for the one message big enough to need this,
// how it starts and how it ends are what a summary can still use.
//
// The size is found by binary search over the bytes kept rather than
// computed, so it holds for a supplied TokenEstimator as well as for
// the default's fixed bytes-per-token.
func elideMiddle(s string, max int, est TokenEstimator) string {
	if est(s) <= max {
		return s
	}
	lo, hi := 0, len(s)
	best := elideTo(s, 0)
	for lo <= hi {
		mid := (lo + hi) / 2
		candidate := elideTo(s, mid)
		if est(candidate) <= max {
			best = candidate
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return best
}

// elideTo keeps the first and last halves of keep bytes of s.
func elideTo(s string, keep int) string {
	if keep >= len(s) {
		return s
	}
	half := keep / 2
	return s[:half] +
		fmt.Sprintf("\n\n[... %d bytes of the middle of this transcript omitted ...]\n\n", len(s)-keep) +
		s[len(s)-(keep-half):]
}
