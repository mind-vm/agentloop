package evalmem

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/jryannel/agentloop/eval"
)

// InMemoryStore is a thread-safe, map-backed eval.Store. Cases and
// runs cascade on DeleteSuite.
type InMemoryStore struct {
	mu     sync.Mutex
	suites map[string]eval.Suite
	cases  map[string]eval.Case
	runs   map[string]eval.Run
}

// New returns an empty InMemoryStore.
func New() *InMemoryStore {
	return &InMemoryStore{
		suites: make(map[string]eval.Suite),
		cases:  make(map[string]eval.Case),
		runs:   make(map[string]eval.Run),
	}
}

func (s *InMemoryStore) CreateSuite(_ context.Context, name, judgeModel string) (eval.Suite, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	out := eval.Suite{
		ID: uuid.NewString(), Name: name, JudgeModel: judgeModel,
		CreatedAt: now, UpdatedAt: now,
	}
	s.suites[out.ID] = out
	return out, nil
}

func (s *InMemoryStore) ListSuites(_ context.Context) ([]eval.Suite, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]eval.Suite, 0, len(s.suites))
	for _, su := range s.suites {
		out = append(out, su)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, nil
}

func (s *InMemoryStore) GetSuite(_ context.Context, id string) (eval.Suite, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	su, ok := s.suites[id]
	if !ok {
		return eval.Suite{}, errors.New("evalmem: suite not found")
	}
	return su, nil
}

func (s *InMemoryStore) DeleteSuite(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.suites[id]; !ok {
		return nil
	}
	delete(s.suites, id)
	for cid, c := range s.cases {
		if c.SuiteID == id {
			delete(s.cases, cid)
		}
	}
	for rid, r := range s.runs {
		if r.SuiteID == id {
			delete(s.runs, rid)
		}
	}
	return nil
}

func (s *InMemoryStore) AddCase(_ context.Context, suiteID, name, input, criteria string, passThreshold int32, items []eval.CriterionItem) (eval.Case, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := eval.Case{
		ID: uuid.NewString(), SuiteID: suiteID, Name: name,
		Input: input, Criteria: criteria, PassThreshold: passThreshold,
		CriteriaItems: items, CreatedAt: time.Now().UTC(),
	}
	s.cases[c.ID] = c
	return c, nil
}

func (s *InMemoryStore) ListCases(_ context.Context, suiteID string) ([]eval.Case, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]eval.Case, 0)
	for _, c := range s.cases {
		if c.SuiteID == suiteID {
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out, nil
}

func (s *InMemoryStore) DeleteCase(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cases, id)
	return nil
}

func (s *InMemoryStore) CreateRun(_ context.Context, suiteID string) (eval.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := eval.Run{
		ID: uuid.NewString(), SuiteID: suiteID,
		Status: eval.StatusRunning, StartedAt: time.Now().UTC(),
	}
	s.runs[r.ID] = r
	return r, nil
}

func (s *InMemoryStore) FinishRun(_ context.Context, id, status string, results []eval.CaseResult, summary eval.RunSummary) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	if !ok {
		return errors.New("evalmem: run not found")
	}
	now := time.Now().UTC()
	r.Status = status
	r.FinishedAt = &now
	r.Results = results
	r.Summary = summary
	s.runs[id] = r
	return nil
}

func (s *InMemoryStore) ListRuns(_ context.Context, suiteID string) ([]eval.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]eval.Run, 0)
	for _, r := range s.runs {
		if r.SuiteID == suiteID {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	return out, nil
}

func (s *InMemoryStore) GetRun(_ context.Context, id string) (eval.Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.runs[id]
	if !ok {
		return eval.Run{}, errors.New("evalmem: run not found")
	}
	return r, nil
}

var _ eval.Store = (*InMemoryStore)(nil)
