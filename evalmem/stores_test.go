package evalmem_test

import (
	"context"
	"testing"

	"github.com/jryannel/agentloop/eval"
	"github.com/jryannel/agentloop/evalmem"
)

func TestInMemoryStore_SuiteCaseRunRoundTrip(t *testing.T) {
	ctx := context.Background()
	s := evalmem.New()

	suite, err := s.CreateSuite(ctx, "my suite", "gpt-4o")
	if err != nil {
		t.Fatalf("CreateSuite: %v", err)
	}
	if suite.ID == "" || suite.Name != "my suite" || suite.JudgeModel != "gpt-4o" {
		t.Fatalf("unexpected suite: %+v", suite)
	}

	got, err := s.GetSuite(ctx, suite.ID)
	if err != nil || got != suite {
		t.Fatalf("GetSuite: got %+v, %v", got, err)
	}

	c, err := s.AddCase(ctx, suite.ID, "case1", "input", "criteria", 7, nil)
	if err != nil {
		t.Fatalf("AddCase: %v", err)
	}
	cases, err := s.ListCases(ctx, suite.ID)
	if err != nil || len(cases) != 1 || cases[0].ID != c.ID {
		t.Fatalf("ListCases: got %+v, %v", cases, err)
	}

	run, err := s.CreateRun(ctx, suite.ID)
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if run.Status != eval.StatusRunning {
		t.Errorf("new run status: got %q, want %q", run.Status, eval.StatusRunning)
	}

	results := []eval.CaseResult{{CaseID: c.ID, CaseName: c.Name, Score: 8, Passed: true}}
	summary := eval.RunSummary{Total: 1, Passed: 1}
	if err := s.FinishRun(ctx, run.ID, eval.StatusCompleted, results, summary); err != nil {
		t.Fatalf("FinishRun: %v", err)
	}

	finished, err := s.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun: %v", err)
	}
	if finished.Status != eval.StatusCompleted || finished.FinishedAt == nil || finished.Summary != summary {
		t.Errorf("unexpected finished run: %+v", finished)
	}

	runs, err := s.ListRuns(ctx, suite.ID)
	if err != nil || len(runs) != 1 {
		t.Fatalf("ListRuns: got %+v, %v", runs, err)
	}
}

func TestInMemoryStore_DeleteSuiteCascades(t *testing.T) {
	ctx := context.Background()
	s := evalmem.New()

	suite, _ := s.CreateSuite(ctx, "s", "")
	_, _ = s.AddCase(ctx, suite.ID, "c", "in", "", 7, nil)
	run, _ := s.CreateRun(ctx, suite.ID)
	_ = s.FinishRun(ctx, run.ID, eval.StatusCompleted, nil, eval.RunSummary{})

	if err := s.DeleteSuite(ctx, suite.ID); err != nil {
		t.Fatalf("DeleteSuite: %v", err)
	}

	if _, err := s.GetSuite(ctx, suite.ID); err == nil {
		t.Errorf("expected GetSuite to fail after delete")
	}
	cases, _ := s.ListCases(ctx, suite.ID)
	if len(cases) != 0 {
		t.Errorf("expected cases to cascade-delete, got %v", cases)
	}
	runs, _ := s.ListRuns(ctx, suite.ID)
	if len(runs) != 0 {
		t.Errorf("expected runs to cascade-delete, got %v", runs)
	}
}

func TestInMemoryStore_GetSuiteUnknownID(t *testing.T) {
	ctx := context.Background()
	s := evalmem.New()
	if _, err := s.GetSuite(ctx, "does-not-exist"); err == nil {
		t.Errorf("expected an error for an unknown suite ID")
	}
	if _, err := s.GetRun(ctx, "does-not-exist"); err == nil {
		t.Errorf("expected an error for an unknown run ID")
	}
}

var _ eval.Store = (*evalmem.InMemoryStore)(nil)
