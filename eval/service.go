package eval

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/jryannel/agentloop"
	"github.com/jryannel/agentloop/llm"
)

// Service is the eval-harness entry point. CRUD methods are thin
// pass-throughs to the Store; RunSuite is the real logic: dispatch
// each case through runner, have judge rate the response, persist the
// run.
type Service struct {
	store  Store
	runner agentloop.Loop
	judge  llm.Client // may be nil: cases then record "no judge configured" rather than a score
}

// NewService wires the harness. store and runner are required and
// panic if nil — a misconfigured harness should fail at construction,
// not on the first RunSuite call. judge may be nil if the harness is
// only used for Suite/Case CRUD (e.g. an admin UI) and RunSuite is
// never called.
func NewService(store Store, runner agentloop.Loop, judge llm.Client) *Service {
	if store == nil {
		panic("eval: store is required")
	}
	if runner == nil {
		panic("eval: runner is required")
	}
	return &Service{store: store, runner: runner, judge: judge}
}

// ---- CRUD ----

func (s *Service) CreateSuite(ctx context.Context, name, judgeModel string) (Suite, error) {
	if name == "" {
		return Suite{}, errors.New("eval: name required")
	}
	return s.store.CreateSuite(ctx, name, judgeModel)
}

func (s *Service) ListSuites(ctx context.Context) ([]Suite, error) {
	return s.store.ListSuites(ctx)
}

func (s *Service) DeleteSuite(ctx context.Context, id string) error {
	return s.store.DeleteSuite(ctx, id)
}

// AddCase defaults passThreshold to DefaultPassThreshold when <= 0,
// and defaults each item's MinScore to that same threshold when
// unset — a caller can leave most items at the case default and
// override only the strict or lenient ones.
func (s *Service) AddCase(ctx context.Context, suiteID, name, input, criteria string, passThreshold int32, items []CriterionItem) (Case, error) {
	if passThreshold <= 0 {
		passThreshold = DefaultPassThreshold
	}
	for i, it := range items {
		if it.MinScore <= 0 {
			items[i].MinScore = int(passThreshold)
		}
	}
	return s.store.AddCase(ctx, suiteID, name, input, criteria, passThreshold, items)
}

func (s *Service) ListCases(ctx context.Context, suiteID string) ([]Case, error) {
	return s.store.ListCases(ctx, suiteID)
}

func (s *Service) DeleteCase(ctx context.Context, id string) error {
	return s.store.DeleteCase(ctx, id)
}

func (s *Service) ListRuns(ctx context.Context, suiteID string) ([]Run, error) {
	return s.store.ListRuns(ctx, suiteID)
}

func (s *Service) GetRun(ctx context.Context, runID string) (Run, error) {
	return s.store.GetRun(ctx, runID)
}

// ---- run ----

// RunSuite dispatches every case in the suite through runner and has
// judge rate each response. Synchronous — returns the finished run.
// For long suites the caller should set a generous ctx timeout.
// Errors during a single case are surfaced inside that case's result
// (Error field) rather than aborting the suite, so the rest of the
// cases still run and RunSuite still returns a completed Run.
func (s *Service) RunSuite(ctx context.Context, suiteID string) (Run, error) {
	suite, err := s.store.GetSuite(ctx, suiteID)
	if err != nil {
		return Run{}, fmt.Errorf("eval: get suite: %w", err)
	}
	cases, err := s.store.ListCases(ctx, suiteID)
	if err != nil {
		return Run{}, fmt.Errorf("eval: list cases: %w", err)
	}
	run, err := s.store.CreateRun(ctx, suiteID)
	if err != nil {
		return Run{}, fmt.Errorf("eval: create run: %w", err)
	}

	results := make([]CaseResult, 0, len(cases))
	for _, c := range cases {
		results = append(results, s.runCase(ctx, suite, c))
	}
	summary := computeSummary(results)

	if err := s.store.FinishRun(ctx, run.ID, StatusCompleted, results, summary); err != nil {
		return Run{}, fmt.Errorf("eval: finish run: %w", err)
	}

	now := time.Now().UTC()
	run.Status = StatusCompleted
	run.FinishedAt = &now
	run.Results = results
	run.Summary = summary
	return run, nil
}

// runCase dispatches one case through runner and judges the response.
// Always returns a fully-populated CaseResult — a failure (agent or
// judge) is captured in Error rather than propagated, so the caller
// (RunSuite) keeps moving.
//
// Each case gets a fresh, never-reused session ID. agentloop's
// SessionStore.Get is documented to auto-create a shell for an
// unknown ID, so this needs no separate session-creation step — but
// it does mean cases within a suite never share conversation history
// with each other, by design: an eval case is a single-turn probe of
// the agent, not a multi-turn scenario.
func (s *Service) runCase(ctx context.Context, suite Suite, c Case) CaseResult {
	out := CaseResult{CaseID: c.ID, CaseName: c.Name}
	res, err := s.runner.Run(ctx, agentloop.RunRequest{
		SessionID: uuid.NewString(),
		Message:   c.Input,
	})
	if err != nil {
		out.Error = err.Error()
		return out
	}
	out.Response = res.FinalText

	if s.judge == nil {
		out.Error = "eval: no judge llm.Client configured"
		return out
	}

	// Per-criterion path: judge each rubric item separately, roll up
	// the verdicts. The case passes only when every item meets its
	// MinScore; the overall Score is the average across items.
	if len(c.CriteriaItems) > 0 {
		results := make([]CriterionResult, 0, len(c.CriteriaItems))
		total := 0
		allPassed := true
		for _, it := range c.CriteriaItems {
			min := it.MinScore
			if min <= 0 {
				min = int(c.PassThreshold)
			}
			score, rationale, jerr := judgeCriterion(ctx, s.judge, suite.JudgeModel, c, res.FinalText, it)
			if jerr != nil {
				out.Error = jerr.Error()
				return out
			}
			passed := score >= min
			if !passed {
				allPassed = false
			}
			total += score
			results = append(results, CriterionResult{
				Label: it.Label, Score: score, MinScore: min, Rationale: rationale, Passed: passed,
			})
		}
		out.CriterionResults = results
		out.Score = total / len(results)
		out.Rationale = rollupRationale(results)
		out.Passed = allPassed
		return out
	}

	// Legacy single-rubric path: kept for cases authored before the
	// per-criterion rubric existed (CriteriaItems empty).
	score, rationale, jerr := judgeResponse(ctx, s.judge, suite.JudgeModel, c, res.FinalText)
	if jerr != nil {
		out.Error = jerr.Error()
		return out
	}
	out.Score = score
	out.Rationale = rationale
	out.Passed = score >= int(c.PassThreshold)
	return out
}

// rollupRationale composes a one-line summary of which criteria
// passed and failed. The full per-criterion breakdown lives on
// CriterionResults.
func rollupRationale(results []CriterionResult) string {
	if len(results) == 0 {
		return ""
	}
	passed, failed := 0, 0
	for _, r := range results {
		if r.Passed {
			passed++
		} else {
			failed++
		}
	}
	if failed == 0 {
		return "All criteria passed."
	}
	if passed == 0 {
		return "All criteria failed."
	}
	return fmt.Sprintf("%d of %d criteria passed.", passed, len(results))
}

func computeSummary(results []CaseResult) RunSummary {
	out := RunSummary{Total: len(results)}
	for _, r := range results {
		if r.Passed {
			out.Passed++
		} else {
			out.Failed++
		}
	}
	return out
}
