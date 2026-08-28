package agentloop

import (
	"context"
	"encoding/json"
	"time"
)

// Session is the minimum the loop needs to drive a Run.
type Session struct {
	ID           string
	Model        string // optional; loop falls back to Config.Model when empty
	SystemPrompt string // persona section appended to the platform prompt
	Data         json.RawMessage
	// MessageID is the inbound message that started this session, if any
	// — set once at session creation and read back here so a
	// SandboxBuilder can hand it to capabilities that need it. Empty for
	// a session with no originating message.
	MessageID string
}

// FinalizeSummary is what the loop hands to Finalize at the end of a run
// (success or failure).
type FinalizeSummary struct {
	Status           string
	PromptTokens     int32
	CompletionTokens int32
	StepCount        int32
	DurationMs       int32
	// DataBytesCarried sums, over every LLM call of this run, the bytes
	// of threaded working state held server-side minus the shape digest
	// actually sent.
	DataBytesCarried int64
}

// RunStep is one persisted row in the session's trace. StepType is the
// discriminator: user, execute_js, execute_js_result, response, error,
// summary. rehydrateHistory (history.go) only replays user / response /
// execute_js / execute_js_result back to the LLM — error rows stay in
// the trace but don't feed back.
//
// A StepTypeSummary row is a compaction checkpoint rather than a turn:
// its Content is replayed in place of the steps it covers, and its
// ToolArgs holds a CompactionCheckpoint naming where retained history
// resumes. Store implementations need do nothing special for it — it is
// an ordinary append — but they must preserve ToolArgs verbatim, since
// losing that marker turns the checkpoint into an unusable row.
type RunStep struct {
	SessionID        string
	StepIndex        int32
	StepType         string
	Content          string
	ToolArgs         json.RawMessage
	DurationMs       int32
	PromptTokens     int32
	CompletionTokens int32
	CreatedAt        time.Time
}

// StepStore persists the per-turn trace of a session.
//
// LastN's ordering is load-bearing: it must return the most recent n
// steps in CHRONOLOGICAL order (oldest first) — the loop replays this
// straight into the LLM context window.
type StepStore interface {
	Append(ctx context.Context, step RunStep) error
	LastN(ctx context.Context, sessionID string, n int) ([]RunStep, error)
}

// SessionStore exposes the per-session metadata the loop reads and the
// lifecycle hooks it writes.
type SessionStore interface {
	// Get returns the session for sessionID. An UNKNOWN id is NOT an
	// error — implementations may auto-create a fresh session shell,
	// because the loop treats "no session yet" as normal.
	Get(ctx context.Context, sessionID string) (Session, error)

	// Exists reports whether a session row already exists, WITHOUT
	// creating one.
	Exists(ctx context.Context, sessionID string) (bool, error)

	UpdateData(ctx context.Context, sessionID string, snapshot json.RawMessage) error
	Finalize(ctx context.Context, sessionID string, summary FinalizeSummary) error
}
