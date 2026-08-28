package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/mind-vm/agentloop/llm"
)

// stubSummarizer is a minimal llm.Client for compaction tests: it
// records what it was asked to summarize and replies with a canned
// answer (or an error).
type stubSummarizer struct {
	reply  string
	err    error
	tokens [2]int // input, output
	calls  []llm.CompletionRequest
	// replyFor, when set, overrides reply per call (0-based index), so
	// a test can watch a fold round shrink its input.
	replyFor func(call int, req llm.CompletionRequest) string
}

func (s *stubSummarizer) Name() string { return "stub" }

func (s *stubSummarizer) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	reply := s.reply
	if s.replyFor != nil {
		reply = s.replyFor(len(s.calls), req)
	}
	s.calls = append(s.calls, req)
	return llm.CompletionResponse{
		Content:      reply,
		InputTokens:  s.tokens[0],
		OutputTokens: s.tokens[1],
	}, s.err
}

// systems returns the system prompt of each recorded call.
func (s *stubSummarizer) systems() []string {
	out := make([]string, len(s.calls))
	for i, c := range s.calls {
		out[i] = c.Messages[0].Content
	}
	return out
}

// bodies returns the user content of each recorded call.
func (s *stubSummarizer) bodies() []string {
	out := make([]string, len(s.calls))
	for i, c := range s.calls {
		out[i] = c.Messages[1].Content
	}
	return out
}

func (s *stubSummarizer) Stream(ctx context.Context, req llm.CompletionRequest, onChunk func(string)) (llm.CompletionResponse, error) {
	return s.Complete(ctx, req)
}

// turns builds a history of n exchanges: a user ask followed by
// alternating run() blocks and their execution results.
func turns(n int) []llm.Message {
	out := []llm.Message{{Role: "user", Content: "do the thing"}}
	for i := range n {
		out = append(out,
			llm.Message{Role: "assistant", Content: fmt.Sprintf("```javascript\nfunction run(args) { return %d; }\n```", i)},
			llm.Message{Role: "user", Content: fmt.Sprintf("%s\nlogged %d", executionResultPrefix, i)},
		)
	}
	return out
}

// fatTurns builds n exchanges whose bodies are size bytes each — a
// transcript too large for one summarization call, which is what the
// chunking path needs to be exercised at all.
func fatTurns(n, size int) []llm.Message {
	body := strings.Repeat("w", size)
	out := []llm.Message{{Role: "user", Content: "do the thing " + body}}
	for i := range n {
		out = append(out,
			llm.Message{Role: "assistant", Content: fmt.Sprintf("```javascript\nfunction run(args) { return %d; }\n```\n%s", i, body)},
			llm.Message{Role: "user", Content: fmt.Sprintf("%s\nlogged %d\n%s", executionResultPrefix, i, body)},
		)
	}
	return out
}

// ---- split boundary ----

func TestSafeSplitPointKeepsScriptWithItsResult(t *testing.T) {
	h := turns(5) // [user, (assistant, result) x5] = 11 messages

	// Index 2 is an execution result; splitting there would strand it
	// from the assistant turn at index 1 that produced it.
	if got := safeSplitPoint(h, 2); got != 1 {
		t.Fatalf("split = %d, want 1 (step back onto the assistant turn)", got)
	}
	if !isExecutionResult(h[2]) {
		t.Fatal("fixture wrong: index 2 should be an execution result")
	}
	// An assistant turn is already a clean boundary.
	if got := safeSplitPoint(h, 1); got != 1 {
		t.Fatalf("split = %d, want 1 (assistant turns need no adjustment)", got)
	}
}

func TestSafeSplitPointEdges(t *testing.T) {
	h := turns(3)
	if got := safeSplitPoint(h, 0); got != 0 {
		t.Errorf("split(0) = %d, want 0", got)
	}
	if got := safeSplitPoint(h, -5); got != 0 {
		t.Errorf("split(-5) = %d, want 0", got)
	}
	if got := safeSplitPoint(h, len(h)+3); got != len(h) {
		t.Errorf("split(over) = %d, want %d", got, len(h))
	}
}

// ---- bounds ----

func TestCompactorBounds(t *testing.T) {
	tests := []struct {
		name                  string
		trigger, keep         int
		wantTrigger, wantKeep int
	}{
		{name: "defaults", wantTrigger: defaultCompactTrigger, wantKeep: defaultCompactKeep},
		{name: "explicit", trigger: 30, keep: 10, wantTrigger: 30, wantKeep: 10},
		// Keeping at least as much as triggers compaction would fold
		// nothing away while still paying for a summarization.
		{name: "keep clamped below trigger", trigger: 20, keep: 20, wantTrigger: 20, wantKeep: 10},
		{name: "keep over trigger clamped", trigger: 20, keep: 999, wantTrigger: 20, wantKeep: 10},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &SummarizingCompactor{Trigger: tt.trigger, Keep: tt.keep}
			trigger, keep := c.bounds()
			if trigger != tt.wantTrigger || keep != tt.wantKeep {
				t.Fatalf("bounds() = (%d, %d), want (%d, %d)", trigger, keep, tt.wantTrigger, tt.wantKeep)
			}
			if keep >= trigger {
				t.Fatalf("keep %d must stay below trigger %d", keep, trigger)
			}
		})
	}
}

// ---- Compact ----

func TestCompactBelowTriggerMakesNoCall(t *testing.T) {
	stub := &stubSummarizer{reply: "should not be used"}
	c := &SummarizingCompactor{LLM: stub, Trigger: 20, Keep: 5}
	h := turns(3) // 7 messages, well under the trigger

	res, err := c.Compact(context.Background(), h)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if len(res.Messages) != len(h) {
		t.Fatalf("messages = %d, want %d unchanged", len(res.Messages), len(h))
	}
	if len(stub.calls) != 0 {
		t.Fatalf("made %d provider calls under the trigger, want 0", len(stub.calls))
	}
}

func TestCompactWithoutClientIsNoOp(t *testing.T) {
	c := &SummarizingCompactor{Trigger: 4, Keep: 2}
	h := turns(10)
	res, err := c.Compact(context.Background(), h)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if len(res.Messages) != len(h) {
		t.Fatalf("messages = %d, want %d — a missing client must degrade, not fail", len(res.Messages), len(h))
	}
}

func TestCompactReplacesOlderTurnsWithSummary(t *testing.T) {
	stub := &stubSummarizer{reply: "  user wanted the thing; steps 0-3 done  ", tokens: [2]int{120, 30}}
	c := &SummarizingCompactor{LLM: stub, Trigger: 10, Keep: 4}
	h := turns(8) // 17 messages

	res, err := c.Compact(context.Background(), h)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if len(res.Messages) >= len(h) {
		t.Fatalf("messages = %d, want fewer than %d", len(res.Messages), len(h))
	}

	summary := res.Messages[0]
	if summary.Role != "user" {
		t.Errorf("summary role = %q, want %q", summary.Role, "user")
	}
	if !strings.Contains(summary.Content, "user wanted the thing") {
		t.Errorf("summary content missing the model's text: %q", summary.Content)
	}
	if strings.Contains(summary.Content, "  user wanted") {
		t.Errorf("summary should be trimmed, got %q", summary.Content)
	}

	// The retained tail must be the ORIGINAL messages, untouched.
	tail := res.Messages[1:]
	if want := h[len(h)-len(tail):]; !equalMessages(tail, want) {
		t.Errorf("tail was modified:\n got %v\nwant %v", tail, want)
	}
	if res.Tokens.Prompt != 120 || res.Tokens.Completion != 30 {
		t.Errorf("tokens = %+v, want {120 30}", res.Tokens)
	}
}

// The tail must never open with an execution result whose script was
// summarized away — the model would read output from a script it cannot
// see.
func TestCompactNeverStrandsAnExecutionResult(t *testing.T) {
	stub := &stubSummarizer{reply: "summary"}
	// Keep of 4 on a 17-message history lands the natural split on an
	// execution result, so the compactor has to step back one.
	c := &SummarizingCompactor{LLM: stub, Trigger: 10, Keep: 4}

	res, err := c.Compact(context.Background(), turns(8))
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	first := res.Messages[1] // [0] is the summary
	if isExecutionResult(first) {
		t.Fatalf("tail opens with an orphaned execution result: %q", first.Content)
	}
	if first.Role != "assistant" {
		t.Fatalf("tail should open with the run() block, got role %q", first.Role)
	}
}

func TestCompactSurvivesProviderFailure(t *testing.T) {
	stub := &stubSummarizer{err: errors.New("upstream down"), tokens: [2]int{90, 0}}
	c := &SummarizingCompactor{LLM: stub, Trigger: 10, Keep: 4}
	h := turns(8)

	res, err := c.Compact(context.Background(), h)
	if err == nil {
		t.Fatal("Compact: want error")
	}
	if !equalMessages(res.Messages, h) {
		t.Error("a failed compaction must hand back the original history")
	}
	// A billable call that produced nothing usable still has to be counted.
	if res.Tokens.Prompt != 90 {
		t.Errorf("tokens = %+v, want the failed call's 90 prompt tokens", res.Tokens)
	}
}

func TestCompactRejectsEmptySummary(t *testing.T) {
	stub := &stubSummarizer{reply: "   ", tokens: [2]int{50, 1}}
	c := &SummarizingCompactor{LLM: stub, Trigger: 10, Keep: 4}
	h := turns(8)

	res, err := c.Compact(context.Background(), h)
	if err == nil {
		t.Fatal("Compact: want error for a blank summary")
	}
	if !equalMessages(res.Messages, h) {
		t.Error("a blank summary must leave the history intact")
	}
	if res.Tokens.Prompt != 50 {
		t.Errorf("tokens = %+v, want the call counted", res.Tokens)
	}
}

func TestCompactSendsLabelledTranscript(t *testing.T) {
	stub := &stubSummarizer{reply: "summary"}
	c := &SummarizingCompactor{LLM: stub, Trigger: 10, Keep: 4, Prompt: "custom instruction"}

	if _, err := c.Compact(context.Background(), turns(8)); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("provider calls = %d, want 1", len(stub.calls))
	}
	req := stub.calls[0]
	if req.Messages[0].Role != "system" || req.Messages[0].Content != "custom instruction" {
		t.Errorf("Prompt override not used: %+v", req.Messages[0])
	}
	if !strings.Contains(req.Messages[1].Content, "[execution result]") {
		t.Errorf("transcript should label execution results, got:\n%s", req.Messages[1].Content)
	}
}

// ---- chunk and fold, end to end ----

// The behaviour this whole mechanism exists for: a transcript too large
// for one call is summarized in pieces and the pieces merged, rather
// than having its middle thrown away.
func TestCompactChunksAndFoldsAnOversizedTranscript(t *testing.T) {
	stub := &stubSummarizer{
		replyFor: func(call int, _ llm.CompletionRequest) string {
			return fmt.Sprintf("partial summary %d", call)
		},
		tokens: [2]int{100, 20},
	}
	c := &SummarizingCompactor{
		LLM:     stub,
		Trigger: 10,
		Keep:    4,
		// Small enough that turns(20) cannot possibly fit one call.
		Budget: ContextBudget{MaxTokens: 4000, Reserve: 500},
	}

	res, err := c.Compact(context.Background(), fatTurns(20, 2000))
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if len(stub.calls) < 3 {
		t.Fatalf("made %d calls, want several chunk calls plus a merge", len(stub.calls))
	}

	// Chunk calls carry the compact instruction and say which part they
	// are; the last call merges and carries the merge instruction.
	systems := stub.systems()
	last := len(systems) - 1
	for i, sys := range systems[:last] {
		if sys != DefaultCompactPrompt {
			t.Errorf("call %d used the merge prompt for a chunk", i)
		}
		if !strings.Contains(stub.bodies()[i], "Part ") {
			t.Errorf("chunk call %d has no part header — the model reads a middle chunk as the end of the session", i)
		}
	}
	if systems[last] != DefaultMergePrompt {
		t.Errorf("final call used the compact prompt, want the merge prompt")
	}
	mergeBody := stub.bodies()[last]
	for i := 0; i < last; i++ {
		if !strings.Contains(mergeBody, fmt.Sprintf("partial summary %d", i)) {
			t.Errorf("merge call is missing partial summary %d — a chunk's work was dropped", i)
		}
	}

	// Nothing of the transcript was elided on the way.
	if strings.Contains(strings.Join(stub.bodies()[:last], ""), "omitted") {
		t.Error("ordinary turns were elided instead of chunked")
	}
	// Every call's spend is accounted for, not just the last.
	if want := int32(100 * len(stub.calls)); res.Tokens.Prompt != want {
		t.Errorf("Tokens.Prompt = %d, want %d across all %d calls", res.Tokens.Prompt, want, len(stub.calls))
	}
	if !strings.Contains(res.Summary, fmt.Sprintf("partial summary %d", last)) {
		t.Errorf("result does not carry the merged summary: %q", res.Summary)
	}
}

// A transcript that fits one call must take exactly the path it took
// before chunking existed — one call, no part header, no merge.
func TestCompactSingleChunkPathIsUnchanged(t *testing.T) {
	stub := &stubSummarizer{reply: "the summary"}
	c := &SummarizingCompactor{LLM: stub, Trigger: 10, Keep: 4}

	if _, err := c.Compact(context.Background(), turns(8)); err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if len(stub.calls) != 1 {
		t.Fatalf("made %d calls, want 1", len(stub.calls))
	}
	if stub.systems()[0] != DefaultCompactPrompt {
		t.Error("single-chunk call did not use the compact prompt")
	}
	if strings.Contains(stub.bodies()[0], "Part 1 of") {
		t.Error("single-chunk call carries a part header it has no need for")
	}
}

// One failed chunk fails the compaction. The loop then proceeds on the
// uncompacted history, which beats a summary with a silent hole where
// that stretch of the session used to be — and the calls already made
// are still charged.
func TestCompactFailsWhenAChunkFails(t *testing.T) {
	stub := &stubSummarizer{reply: "", err: errors.New("provider down"), tokens: [2]int{70, 0}}
	c := &SummarizingCompactor{
		LLM:     stub,
		Trigger: 10,
		Keep:    4,
		Budget:  ContextBudget{MaxTokens: 4000, Reserve: 500},
	}
	history := fatTurns(20, 2000)

	res, err := c.Compact(context.Background(), history)

	if err == nil {
		t.Fatal("Compact succeeded with a failing provider")
	}
	if !equalMessages(res.Messages, history) {
		t.Error("failed compaction should return the history untouched")
	}
	if res.Tokens.Prompt != 70 {
		t.Errorf("Tokens.Prompt = %d, want the failed call's 70 still charged", res.Tokens.Prompt)
	}
	if res.Summary != "" {
		t.Error("a failed compaction must not report a checkpoint to persist")
	}
}

// A model that returns summaries as long as what it was given never
// converges. That is a runaway to stop, not to keep paying for.
func TestCompactGivesUpWhenFoldingMakesNoProgress(t *testing.T) {
	never := strings.Repeat("still enormous. ", 2000)
	stub := &stubSummarizer{reply: never, tokens: [2]int{10, 10}}
	c := &SummarizingCompactor{
		LLM:     stub,
		Trigger: 10,
		Keep:    4,
		Budget:  ContextBudget{MaxTokens: 4000, Reserve: 500},
	}

	_, err := c.Compact(context.Background(), fatTurns(20, 2000))

	if err == nil {
		t.Fatal("Compact succeeded despite summaries that never shrink")
	}
	if !strings.Contains(err.Error(), "fold rounds") {
		t.Errorf("error = %v, want it to name the fold-round limit", err)
	}
}

// ---- chunking ----

func TestTranscriptChunksSplitOnMessageBoundaries(t *testing.T) {
	// Three turns of ~25 tokens each against a 60-token allowance:
	// two per chunk, and never a turn cut in half.
	body := strings.Repeat("y", 88)
	messages := []llm.Message{
		{Role: "user", Content: "FIRST-ASK " + body},
		{Role: "assistant", Content: "MIDDLE-WORK " + body},
		{Role: "user", Content: "LAST-THING " + body},
	}

	chunks := transcriptChunks(messages, 60, EstimateTokens)

	if len(chunks) < 2 {
		t.Fatalf("got %d chunk(s), want the transcript split", len(chunks))
	}
	joined := strings.Join(chunks, "")
	// The whole point of chunking over eliding: nothing is discarded.
	for _, want := range []string{"FIRST-ASK", "MIDDLE-WORK", "LAST-THING"} {
		if !strings.Contains(joined, want) {
			t.Errorf("chunking dropped %q — it should summarize the middle, not discard it", want)
		}
	}
	if strings.Contains(joined, "omitted") {
		t.Error("a transcript of ordinary turns should chunk, never elide")
	}
	for i, c := range chunks {
		if got := strings.Count(c, "[user]") + strings.Count(c, "[assistant]"); got == 0 {
			t.Errorf("chunk %d has no turn header — a chunk should be whole turns:\n%s", i, c)
		}
	}
}

func TestTranscriptChunksElideOnlyAnOversizedSingleMessage(t *testing.T) {
	huge := "HEAD-MARKER " + strings.Repeat("x", 4000) + " TAIL-MARKER"
	messages := []llm.Message{
		{Role: "user", Content: "FIRST-ASK"},
		{Role: "assistant", Content: huge},
		{Role: "user", Content: "LAST-THING"},
	}

	chunks := transcriptChunks(messages, 200, EstimateTokens)

	joined := strings.Join(chunks, "")
	// A message that cannot fit a call of its own is the one place
	// transcript is still discarded — head and tail survive.
	if !strings.Contains(joined, "HEAD-MARKER") || !strings.Contains(joined, "TAIL-MARKER") {
		t.Error("elision should keep the oversized message's head and tail")
	}
	if !strings.Contains(joined, "omitted") {
		t.Error("elision should be marked so the model knows it is partial")
	}
	// The turns around it are ordinary and must survive intact.
	if !strings.Contains(joined, "FIRST-ASK") || !strings.Contains(joined, "LAST-THING") {
		t.Error("neighbouring turns were dropped along with the oversized one")
	}
	for i, c := range chunks {
		if got := EstimateTokens(c); got > 200 {
			t.Errorf("chunk %d is ~%d tokens against a 200 allowance", i, got)
		}
	}
}

func TestTranscriptChunksFitTheAllowance(t *testing.T) {
	messages := turns(40)

	for _, allowance := range []int{50, 200, 1000} {
		chunks := transcriptChunks(messages, allowance, EstimateTokens)
		for i, c := range chunks {
			if got := EstimateTokens(c); got > allowance {
				t.Errorf("allowance %d: chunk %d is ~%d tokens", allowance, i, got)
			}
		}
	}
}

func TestElideMiddleRespectsACustomEstimator(t *testing.T) {
	s := strings.Repeat("z", 4000)
	// One token per byte, so the default's bytes/4 would overshoot by 4x.
	est := func(x string) int { return len(x) }

	got := elideMiddle(s, 500, est)

	if est(got) > 500 {
		t.Fatalf("elided to %d tokens against a 500 cap — the supplied estimator was not what bounded it", est(got))
	}
}

func TestChunkBoundsUsesTheBudget(t *testing.T) {
	// A window far too small for the instruction prompt must still
	// yield a workable allowance rather than a non-positive one.
	tiny := &SummarizingCompactor{Budget: ContextBudget{MaxTokens: 600}}
	if got, _ := tiny.chunkBounds(); got < minChunkTokens {
		t.Errorf("tiny budget gave an allowance of %d, want the %d floor", got, minChunkTokens)
	}

	// A real local window: the allowance is the budget less the
	// instruction that shares the request with the transcript.
	local := &SummarizingCompactor{Budget: ContextBudget{MaxTokens: 8192}}
	got, _ := local.chunkBounds()
	if got >= local.Budget.allowance() {
		t.Errorf("allowance %d does not account for the instruction prompt", got)
	}
	if got < 4000 {
		t.Errorf("allowance %d is far below the 8192-token window it came from", got)
	}

	// Unset budget keeps the pre-chunking bound.
	unset := &SummarizingCompactor{}
	if got, _ := unset.chunkBounds(); got > defaultSummarizeTokens || got < defaultSummarizeTokens/2 {
		t.Errorf("default allowance = %d, want near %d", got, defaultSummarizeTokens)
	}
}

func equalMessages(a, b []llm.Message) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---- checkpoint replay ----

// checkpoint builds a StepTypeSummary step covering everything before
// retainFrom.
func checkpoint(t *testing.T, index, retainFrom int32, summary string) RunStep {
	t.Helper()
	marker, err := json.Marshal(CompactionCheckpoint{RetainFromStep: retainFrom})
	if err != nil {
		t.Fatalf("marshal checkpoint: %v", err)
	}
	return RunStep{StepIndex: index, StepType: StepTypeSummary, Content: summary, ToolArgs: marker}
}

func userStep(index int32, content string) RunStep {
	return RunStep{StepIndex: index, StepType: "user", Content: content}
}

func contents(msgs []llm.Message) []string {
	out := make([]string, len(msgs))
	for i, m := range msgs {
		out[i] = m.Content
	}
	return out
}

func TestRehydrateReplaysCheckpointInsteadOfCoveredSteps(t *testing.T) {
	steps := []RunStep{
		userStep(0, "ancient one"),
		userStep(1, "ancient two"),
		userStep(2, "retained one"),
		userStep(3, "retained two"),
		checkpoint(t, 4, 2, "[summary] the ancient turns"),
		userStep(5, "after the checkpoint"),
	}

	msgs, stepOf := rehydrateHistory(steps, HistoryWindow)
	got := contents(msgs)
	want := []string{"[summary] the ancient turns", "retained one", "retained two", "after the checkpoint"}
	if len(got) != len(want) {
		t.Fatalf("replayed %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("replayed %q, want %q", got, want)
		}
	}
	// The step indices must line up with the messages, since a later
	// compaction translates its split back through them.
	if len(stepOf) != len(msgs) {
		t.Fatalf("stepOf has %d entries for %d messages", len(stepOf), len(msgs))
	}
	if stepOf[1] != 2 || stepOf[3] != 5 {
		t.Errorf("stepOf = %v, want the retained messages to carry their own indices", stepOf)
	}
}

func TestRehydrateReplaysOnlyTheNewestCheckpoint(t *testing.T) {
	steps := []RunStep{
		userStep(0, "oldest"),
		checkpoint(t, 1, 0, "[summary] first pass"),
		userStep(2, "middle"),
		checkpoint(t, 3, 2, "[summary] second pass"),
		userStep(4, "newest"),
	}

	got := contents(mustRehydrate(t, steps))
	for _, unwanted := range []string{"[summary] first pass", "oldest"} {
		for _, c := range got {
			if c == unwanted {
				t.Errorf("replayed %q; the newer checkpoint already subsumes it", unwanted)
			}
		}
	}
	if got[0] != "[summary] second pass" {
		t.Fatalf("replayed %q, want the newest summary first", got)
	}
}

// An unusable checkpoint must be ignored rather than half-honoured:
// replaying nothing in place of the steps it claims to cover would
// erase that stretch of the conversation.
func TestRehydrateIgnoresUnusableCheckpoints(t *testing.T) {
	tests := []struct {
		name string
		step RunStep
	}{
		{
			name: "no marker",
			step: RunStep{StepIndex: 2, StepType: StepTypeSummary, Content: "[summary] x"},
		},
		{
			name: "unparseable marker",
			step: RunStep{StepIndex: 2, StepType: StepTypeSummary, Content: "[summary] x", ToolArgs: []byte("not json")},
		},
		{
			name: "empty summary",
			step: checkpoint(t, 2, 1, "   "),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			steps := []RunStep{userStep(0, "first"), userStep(1, "second"), tt.step, userStep(3, "third")}
			got := contents(mustRehydrate(t, steps))
			want := []string{"first", "second", "third"}
			if len(got) != len(want) {
				t.Fatalf("replayed %q, want the full history %q", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("replayed %q, want %q", got, want)
				}
			}
		})
	}
}

func mustRehydrate(t *testing.T, steps []RunStep) []llm.Message {
	t.Helper()
	msgs, _ := rehydrateHistory(steps, HistoryWindow)
	return msgs
}

// ---- retainFromStep ----

func TestRetainFromStep(t *testing.T) {
	stepOf := []int32{0, 3, 7, 9}
	tests := []struct {
		name         string
		retainedFrom int
		stepOf       []int32
		want         int32
		wantOK       bool
	}{
		{name: "normal split", retainedFrom: 2, stepOf: stepOf, want: 7, wantOK: true},
		// Nothing folded away means there is no checkpoint worth writing.
		{name: "retained everything", retainedFrom: 0, stepOf: stepOf},
		{name: "negative", retainedFrom: -1, stepOf: stepOf},
		{name: "no messages", retainedFrom: 1},
		// Folding everything leaves the retained history starting one
		// past the last replayed step.
		{name: "folded everything", retainedFrom: 4, stepOf: stepOf, want: 10, wantOK: true},
		{name: "beyond the end", retainedFrom: 99, stepOf: stepOf, want: 10, wantOK: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := retainFromStep(tt.retainedFrom, tt.stepOf)
			if ok != tt.wantOK || (ok && got != tt.want) {
				t.Fatalf("retainFromStep(%d, %v) = (%d, %v), want (%d, %v)",
					tt.retainedFrom, tt.stepOf, got, ok, tt.want, tt.wantOK)
			}
		})
	}
}

func TestCompactReportsWhatToPersist(t *testing.T) {
	stub := &stubSummarizer{reply: "the summary"}
	c := &SummarizingCompactor{LLM: stub, Trigger: 10, Keep: 4}
	h := turns(8)

	res, err := c.Compact(context.Background(), h)
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if res.Summary == "" {
		t.Fatal("Summary must be set for the loop to persist a checkpoint")
	}
	// The persisted text must be the message verbatim, so a replayed
	// checkpoint is indistinguishable from the compaction that made it.
	if res.Summary != res.Messages[0].Content {
		t.Errorf("Summary = %q, want the exact replayed message %q", res.Summary, res.Messages[0].Content)
	}
	if res.RetainedFrom <= 0 || res.RetainedFrom >= len(h) {
		t.Errorf("RetainedFrom = %d, want a real split within 0..%d", res.RetainedFrom, len(h))
	}
	// Everything from RetainedFrom on is what stayed verbatim.
	if !equalMessages(res.Messages[1:], h[res.RetainedFrom:]) {
		t.Error("RetainedFrom does not identify the messages that were kept")
	}
}

func TestCompactBelowTriggerReportsNothingToPersist(t *testing.T) {
	c := &SummarizingCompactor{LLM: &stubSummarizer{reply: "unused"}, Trigger: 100, Keep: 5}
	res, err := c.Compact(context.Background(), turns(3))
	if err != nil {
		t.Fatalf("Compact: %v", err)
	}
	if res.Summary != "" || res.RetainedFrom != 0 {
		t.Errorf("got Summary=%q RetainedFrom=%d; a no-op compaction has no checkpoint to write", res.Summary, res.RetainedFrom)
	}
}
