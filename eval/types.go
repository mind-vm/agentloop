package eval

import "time"

// Suite is one collection of Cases run against the Service's
// configured agentloop.Loop.
type Suite struct {
	ID         string
	Name       string
	JudgeModel string // passed as llm.CompletionRequest.Model for every judge call; empty uses the judge client's default
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Case is one input + judge criteria + pass threshold.
//
// CriteriaItems is the per-criterion rubric: when non-empty, the
// judge rates each item separately and the case passes only when
// every item meets its MinScore. The legacy Criteria + PassThreshold
// pair stays for cases authored before the rubric existed; RunSuite
// falls back to the single-rubric path when CriteriaItems is empty.
type Case struct {
	ID            string
	SuiteID       string
	Name          string
	Input         string
	Criteria      string
	PassThreshold int32
	CriteriaItems []CriterionItem
	CreatedAt     time.Time
}

// CriterionItem is one row in a case's per-criterion rubric. Label is
// the human-readable assertion the judge rates; MinScore is the 0-10
// threshold the item must clear to count as passed. MinScore defaults
// to the case's PassThreshold when zero.
type CriterionItem struct {
	Label    string `json:"label"`
	MinScore int    `json:"min_score,omitempty"`
}

// CriterionResult is the judge's verdict for one rubric item on one
// case run. Recorded inside CaseResult.CriterionResults; empty when
// the case used the legacy single-rubric path.
type CriterionResult struct {
	Label     string `json:"label"`
	Score     int    `json:"score"`
	MinScore  int    `json:"min_score"`
	Rationale string `json:"rationale"`
	Passed    bool   `json:"passed"`
}

// CaseResult is the per-case judgment recorded on a run. Error is set
// (and every other field left at its zero value except CaseID/CaseName)
// when the agent or judge call failed — a failing case does not abort
// the rest of the suite.
type CaseResult struct {
	CaseID           string            `json:"case_id"`
	CaseName         string            `json:"case_name"`
	Response         string            `json:"response"`
	Score            int               `json:"score"`
	Rationale        string            `json:"rationale"`
	Passed           bool              `json:"passed"`
	Error            string            `json:"error,omitempty"`
	CriterionResults []CriterionResult `json:"criterion_results,omitempty"`
}

// RunSummary rolls up the per-case results.
type RunSummary struct {
	Total  int `json:"total"`
	Passed int `json:"passed"`
	Failed int `json:"failed"`
}

// Run is one execution of a suite — header fields plus the per-case
// results.
type Run struct {
	ID         string
	SuiteID    string
	Status     string
	StartedAt  time.Time
	FinishedAt *time.Time
	Results    []CaseResult
	Summary    RunSummary
}

// DefaultPassThreshold is used when AddCase is called with
// passThreshold <= 0.
const DefaultPassThreshold int32 = 7

// Run statuses persisted on the row.
const (
	StatusRunning   = "running"
	StatusCompleted = "completed"
	StatusFailed    = "failed"
)
