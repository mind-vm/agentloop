package eval

import "context"

// Store is the persistence contract an application implements to back
// the harness with whatever it already uses — Postgres, SQLite,
// evalmem.InMemoryStore for local dev/CI. All methods return domain
// types, never a driver-specific row.
type Store interface {
	CreateSuite(ctx context.Context, name, judgeModel string) (Suite, error)
	ListSuites(ctx context.Context) ([]Suite, error)
	GetSuite(ctx context.Context, id string) (Suite, error)
	DeleteSuite(ctx context.Context, id string) error

	AddCase(ctx context.Context, suiteID, name, input, criteria string, passThreshold int32, items []CriterionItem) (Case, error)
	ListCases(ctx context.Context, suiteID string) ([]Case, error)
	DeleteCase(ctx context.Context, id string) error

	CreateRun(ctx context.Context, suiteID string) (Run, error)
	FinishRun(ctx context.Context, id, status string, results []CaseResult, summary RunSummary) error
	ListRuns(ctx context.Context, suiteID string) ([]Run, error)
	GetRun(ctx context.Context, id string) (Run, error)
}
