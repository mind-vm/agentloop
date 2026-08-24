package eval_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/jryannel/agentloop"
	"github.com/jryannel/agentloop/eval"
	"github.com/jryannel/agentloop/evalmem"
	"github.com/jryannel/agentloop/llm"
	"github.com/jryannel/agentloop/redact"
)

// fakeRunner is an agentloop.Loop whose response is scripted by
// input substring, and which records every SessionID/Message it was
// called with — used to assert each case gets a fresh session.
type fakeRunner struct {
	mu       sync.Mutex
	byInput  map[string]string // input -> final text
	err      error             // returned for every call when set
	sessions []string
}

func (f *fakeRunner) Run(_ context.Context, req agentloop.RunRequest) (agentloop.RunResult, error) {
	f.mu.Lock()
	f.sessions = append(f.sessions, req.SessionID)
	f.mu.Unlock()
	if f.err != nil {
		return agentloop.RunResult{}, f.err
	}
	return agentloop.RunResult{FinalText: f.byInput[req.Message], Status: "completed"}, nil
}

// fakeJudge is an llm.Client whose Complete reply is scripted by a
// callback so tests can return different scores per call (e.g. one
// score per criterion, in call order).
type fakeJudge struct {
	mu    sync.Mutex
	calls int
	reply func(call int, req llm.CompletionRequest) string
	err   error
}

func (f *fakeJudge) Name() string { return "fake-judge" }

func (f *fakeJudge) Complete(_ context.Context, req llm.CompletionRequest) (llm.CompletionResponse, error) {
	if f.err != nil {
		return llm.CompletionResponse{}, f.err
	}
	f.mu.Lock()
	call := f.calls
	f.calls++
	f.mu.Unlock()
	return llm.CompletionResponse{Content: f.reply(call, req)}, nil
}

func (f *fakeJudge) Stream(ctx context.Context, req llm.CompletionRequest, _ func(string)) (llm.CompletionResponse, error) {
	return f.Complete(ctx, req)
}

func scoreReply(score int, rationale string) string {
	return fmt.Sprintf("Score: %d\nRationale: %s", score, rationale)
}

func TestService_RunSuite_LegacySingleRubric(t *testing.T) {
	store := evalmem.New()
	ctx := context.Background()

	suite, err := store.CreateSuite(ctx, "s1", "judge-model")
	if err != nil {
		t.Fatalf("CreateSuite: %v", err)
	}
	if _, err := store.AddCase(ctx, suite.ID, "case1", "what is 2+2?", "must say 4", 7, nil); err != nil {
		t.Fatalf("AddCase: %v", err)
	}

	runner := &fakeRunner{byInput: map[string]string{"what is 2+2?": "4"}}
	judge := &fakeJudge{reply: func(int, llm.CompletionRequest) string { return scoreReply(9, "correct and concise") }}

	svc := eval.NewService(store, runner, judge, nil)
	run, err := svc.RunSuite(ctx, suite.ID)
	if err != nil {
		t.Fatalf("RunSuite: %v", err)
	}
	if run.Status != eval.StatusCompleted {
		t.Errorf("status: got %q, want %q", run.Status, eval.StatusCompleted)
	}
	if run.Summary != (eval.RunSummary{Total: 1, Passed: 1, Failed: 0}) {
		t.Errorf("summary: got %+v", run.Summary)
	}
	if len(run.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(run.Results))
	}
	r := run.Results[0]
	if r.Response != "4" || r.Score != 9 || !r.Passed || r.Rationale != "correct and concise" {
		t.Errorf("unexpected result: %+v", r)
	}
}

func TestService_RunSuite_PerCriterionRubric_AllMustPass(t *testing.T) {
	store := evalmem.New()
	ctx := context.Background()

	suite, _ := store.CreateSuite(ctx, "s1", "")
	items := []eval.CriterionItem{
		{Label: "mentions the answer", MinScore: 7},
		{Label: "is polite", MinScore: 7},
	}
	if _, err := store.AddCase(ctx, suite.ID, "case1", "hi", "", 7, items); err != nil {
		t.Fatalf("AddCase: %v", err)
	}

	runner := &fakeRunner{byInput: map[string]string{"hi": "hello there"}}
	// First criterion scores 9 (pass), second scores 3 (fail) — case
	// must fail overall even though the average (6) would round to
	// "close" under a naive threshold check.
	scores := []int{9, 3}
	judge := &fakeJudge{reply: func(call int, _ llm.CompletionRequest) string {
		return scoreReply(scores[call], "rationale")
	}}

	svc := eval.NewService(store, runner, judge, nil)
	run, err := svc.RunSuite(ctx, suite.ID)
	if err != nil {
		t.Fatalf("RunSuite: %v", err)
	}
	r := run.Results[0]
	if r.Passed {
		t.Errorf("expected case to fail when one criterion fails, got Passed=true: %+v", r)
	}
	if r.Score != 6 { // (9+3)/2 integer division
		t.Errorf("Score: got %d, want 6 (average of criteria)", r.Score)
	}
	if len(r.CriterionResults) != 2 {
		t.Fatalf("expected 2 criterion results, got %d", len(r.CriterionResults))
	}
	if !r.CriterionResults[0].Passed || r.CriterionResults[1].Passed {
		t.Errorf("criterion pass/fail mismatch: %+v", r.CriterionResults)
	}
	if r.Rationale != "1 of 2 criteria passed." {
		t.Errorf("rollup rationale: got %q", r.Rationale)
	}
	if run.Summary.Failed != 1 {
		t.Errorf("summary: got %+v, want 1 failed", run.Summary)
	}
}

func TestService_RunSuite_AgentErrorDoesNotAbortSuite(t *testing.T) {
	store := evalmem.New()
	ctx := context.Background()

	suite, _ := store.CreateSuite(ctx, "s1", "")
	if _, err := store.AddCase(ctx, suite.ID, "broken", "trigger", "", 7, nil); err != nil {
		t.Fatalf("AddCase: %v", err)
	}
	if _, err := store.AddCase(ctx, suite.ID, "ok", "fine", "", 7, nil); err != nil {
		t.Fatalf("AddCase: %v", err)
	}

	// fakeRunner.err applies to every call, but only one of these two
	// cases should error — use a small closure-based runner instead so
	// the failure is keyed off the input.
	calls := 0
	custom := runnerFunc(func(_ context.Context, req agentloop.RunRequest) (agentloop.RunResult, error) {
		calls++
		if req.Message == "trigger" {
			return agentloop.RunResult{}, errors.New("agent exploded")
		}
		return agentloop.RunResult{FinalText: "all good"}, nil
	})
	judge := &fakeJudge{reply: func(int, llm.CompletionRequest) string { return scoreReply(8, "fine") }}

	svc := eval.NewService(store, custom, judge, nil)
	run, err := svc.RunSuite(ctx, suite.ID)
	if err != nil {
		t.Fatalf("RunSuite: %v", err)
	}
	if calls != 2 {
		t.Fatalf("expected both cases to run despite the first erroring, got %d calls", calls)
	}
	if run.Summary.Total != 2 {
		t.Fatalf("expected both cases in the summary, got %+v", run.Summary)
	}
	var brokenResult, okResult eval.CaseResult
	for _, r := range run.Results {
		if r.CaseName == "broken" {
			brokenResult = r
		} else {
			okResult = r
		}
	}
	if brokenResult.Error == "" {
		t.Errorf("expected the broken case to record an Error")
	}
	if okResult.Error != "" || !okResult.Passed {
		t.Errorf("expected the ok case to succeed: %+v", okResult)
	}
}

type runnerFunc func(ctx context.Context, req agentloop.RunRequest) (agentloop.RunResult, error)

func (f runnerFunc) Run(ctx context.Context, req agentloop.RunRequest) (agentloop.RunResult, error) {
	return f(ctx, req)
}

func TestService_RunSuite_NoJudgeConfigured(t *testing.T) {
	store := evalmem.New()
	ctx := context.Background()
	suite, _ := store.CreateSuite(ctx, "s1", "")
	if _, err := store.AddCase(ctx, suite.ID, "case1", "hi", "", 7, nil); err != nil {
		t.Fatalf("AddCase: %v", err)
	}
	runner := &fakeRunner{byInput: map[string]string{"hi": "hello"}}

	svc := eval.NewService(store, runner, nil, nil)
	run, err := svc.RunSuite(ctx, suite.ID)
	if err != nil {
		t.Fatalf("RunSuite: %v", err)
	}
	if run.Results[0].Error == "" {
		t.Errorf("expected an Error when no judge is configured")
	}
}

func TestService_RunSuite_EachCaseGetsAFreshSession(t *testing.T) {
	store := evalmem.New()
	ctx := context.Background()
	suite, _ := store.CreateSuite(ctx, "s1", "")
	for i := 0; i < 3; i++ {
		if _, err := store.AddCase(ctx, suite.ID, fmt.Sprintf("case%d", i), fmt.Sprintf("input%d", i), "", 7, nil); err != nil {
			t.Fatalf("AddCase: %v", err)
		}
	}
	runner := &fakeRunner{byInput: map[string]string{"input0": "a", "input1": "b", "input2": "c"}}
	judge := &fakeJudge{reply: func(int, llm.CompletionRequest) string { return scoreReply(8, "ok") }}

	svc := eval.NewService(store, runner, judge, nil)
	if _, err := svc.RunSuite(ctx, suite.ID); err != nil {
		t.Fatalf("RunSuite: %v", err)
	}
	if len(runner.sessions) != 3 {
		t.Fatalf("expected 3 Run calls, got %d", len(runner.sessions))
	}
	seen := map[string]bool{}
	for _, id := range runner.sessions {
		if id == "" {
			t.Errorf("empty session ID")
		}
		if seen[id] {
			t.Errorf("session ID %q reused across cases", id)
		}
		seen[id] = true
	}
}

func TestService_AddCase_DefaultsPassThresholdAndItemMinScore(t *testing.T) {
	store := evalmem.New()
	ctx := context.Background()
	suite, _ := store.CreateSuite(ctx, "s1", "")
	runner := &fakeRunner{}
	svc := eval.NewService(store, runner, nil, nil)

	c, err := svc.AddCase(ctx, suite.ID, "case1", "in", "", 0, []eval.CriterionItem{
		{Label: "a"},              // MinScore 0 -> should default
		{Label: "b", MinScore: 9}, // explicit -> should stay
	})
	if err != nil {
		t.Fatalf("AddCase: %v", err)
	}
	if c.PassThreshold != eval.DefaultPassThreshold {
		t.Errorf("PassThreshold: got %d, want %d", c.PassThreshold, eval.DefaultPassThreshold)
	}
	if c.CriteriaItems[0].MinScore != int(eval.DefaultPassThreshold) {
		t.Errorf("item[0].MinScore: got %d, want %d", c.CriteriaItems[0].MinScore, eval.DefaultPassThreshold)
	}
	if c.CriteriaItems[1].MinScore != 9 {
		t.Errorf("item[1].MinScore: got %d, want 9 (explicit value preserved)", c.CriteriaItems[1].MinScore)
	}
}

// TestService_RunSuite_RedactsResponseBeforeJudgeAndPersistence proves
// the secret value never reaches the judge's prompt and never lands in
// the persisted CaseResult.Response — the two leak vectors NewService's
// redactor parameter exists to close.
func TestService_RunSuite_RedactsResponseBeforeJudgeAndPersistence(t *testing.T) {
	store := evalmem.New()
	ctx := context.Background()
	suite, _ := store.CreateSuite(ctx, "s1", "")
	if _, err := store.AddCase(ctx, suite.ID, "case1", "fetch the key", "", 7, nil); err != nil {
		t.Fatalf("AddCase: %v", err)
	}

	const secretValue = "sk-live-super-secret"
	runner := &fakeRunner{byInput: map[string]string{
		"fetch the key": "here is the key: " + secretValue,
	}}

	var judgePrompt string
	judge := &fakeJudge{reply: func(_ int, req llm.CompletionRequest) string {
		for _, m := range req.Messages {
			judgePrompt += m.Content
		}
		return scoreReply(9, "fine")
	}}

	redactor := redact.FromSecrets(map[string]string{"api_key": secretValue})
	svc := eval.NewService(store, runner, judge, redactor)

	run, err := svc.RunSuite(ctx, suite.ID)
	if err != nil {
		t.Fatalf("RunSuite: %v", err)
	}

	if strings.Contains(judgePrompt, secretValue) {
		t.Fatalf("secret leaked into the judge prompt: %q", judgePrompt)
	}
	if !strings.Contains(judgePrompt, "[redacted:secret.api_key]") {
		t.Fatalf("expected the redaction placeholder in the judge prompt: %q", judgePrompt)
	}

	r := run.Results[0]
	if strings.Contains(r.Response, secretValue) {
		t.Fatalf("secret leaked into the persisted CaseResult.Response: %q", r.Response)
	}
	if !strings.Contains(r.Response, "[redacted:secret.api_key]") {
		t.Fatalf("expected the redaction placeholder in CaseResult.Response: %q", r.Response)
	}
}

func TestNewService_PanicsOnNilStoreOrRunner(t *testing.T) {
	store := evalmem.New()
	runner := &fakeRunner{}

	assertPanics(t, "nil store", func() { eval.NewService(nil, runner, nil, nil) })
	assertPanics(t, "nil runner", func() { eval.NewService(store, nil, nil, nil) })
}

func assertPanics(t *testing.T, name string, fn func()) {
	t.Helper()
	defer func() {
		if recover() == nil {
			t.Errorf("%s: expected a panic", name)
		}
	}()
	fn()
}
