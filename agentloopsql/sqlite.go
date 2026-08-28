package agentloopsql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/mind-vm/agentloop"
)

// timeLayout is how instants are written. RFC3339 with nanoseconds
// sorts lexicographically the same way it sorts chronologically, and
// stays readable to anyone who opens the file with the sqlite3 CLI.
const timeLayout = time.RFC3339Nano

// schema is applied on every Open. Every statement is idempotent, so
// opening an existing database is the same code path as creating one.
const schema = `
CREATE TABLE IF NOT EXISTS sessions (
    id                 TEXT PRIMARY KEY,
    model              TEXT    NOT NULL DEFAULT '',
    system_prompt      TEXT    NOT NULL DEFAULT '',
    data               BLOB,
    message_id         TEXT    NOT NULL DEFAULT '',
    created_at         TEXT    NOT NULL,
    updated_at         TEXT    NOT NULL,
    status             TEXT    NOT NULL DEFAULT '',
    prompt_tokens      INTEGER NOT NULL DEFAULT 0,
    completion_tokens  INTEGER NOT NULL DEFAULT 0,
    step_count         INTEGER NOT NULL DEFAULT 0,
    duration_ms        INTEGER NOT NULL DEFAULT 0,
    data_bytes_carried INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS steps (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    session_id         TEXT    NOT NULL,
    step_index         INTEGER NOT NULL,
    step_type          TEXT    NOT NULL,
    content            TEXT    NOT NULL DEFAULT '',
    tool_args          BLOB,
    duration_ms        INTEGER NOT NULL DEFAULT 0,
    prompt_tokens      INTEGER NOT NULL DEFAULT 0,
    completion_tokens  INTEGER NOT NULL DEFAULT 0,
    created_at         TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS steps_by_session ON steps(session_id, id);
`

// Store is a SQLite-backed SessionStore and StepStore over one database.
type Store struct {
	db    *sql.DB
	clock func() time.Time
}

// Open opens (creating if needed) the database at path and applies the
// schema. Pass ":memory:" for an ephemeral database — useful in tests,
// though every Store then starts empty.
//
// clock is a seam for tests; nil uses time.Now.
func Open(path string, clock func() time.Time) (*Store, error) {
	if path == "" {
		return nil, errors.New("agentloopsql: a database path is required")
	}
	if clock == nil {
		clock = time.Now
	}

	// busy_timeout is what makes a second agentloop process wait for the
	// lock instead of failing the run outright. It is per-connection and
	// never contends, so it belongs in the DSN where every new
	// connection picks it up. journal_mode deliberately does NOT — see
	// enableWAL.
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("agentloopsql: open %s: %w", path, err)
	}
	// SQLite takes one writer at a time. Serialising in the pool is
	// cheaper than discovering it as lock contention, and a CLI has no
	// concurrency to lose.
	db.SetMaxOpenConns(1)

	enableWAL(db)

	if _, err := db.Exec(schema + grantSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("agentloopsql: apply schema: %w", err)
	}
	return &Store{db: db, clock: clock}, nil
}

// enableWAL puts the database in write-ahead logging mode, where a
// reader and a writer can coexist.
//
// It is deliberately best-effort, and deliberately not a DSN pragma.
// Switching journal mode needs an exclusive lock, and SQLite refuses
// that one immediately rather than through the busy handler — so a DSN
// pragma applied on every new connection turns two processes opening the
// same database at once into an instant "database is locked", on the one
// statement busy_timeout cannot protect.
//
// WAL is a persistent property of the file, so it only has to be set
// once. The mode is read first, which is the common case and takes no
// lock at all; a losing race with another process leaves the database in
// rollback-journal mode, which is slower under contention but correct,
// and the next open will settle it.
func enableWAL(db *sql.DB) {
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		return
	}
	if strings.EqualFold(mode, "wal") {
		return
	}
	_ = db.QueryRow(`PRAGMA journal_mode = WAL`).Scan(&mode)
}

// Close releases the database handle.
func (s *Store) Close() error { return s.db.Close() }

// DB exposes the underlying handle for migrations or reporting an
// application wants to run itself.
func (s *Store) DB() *sql.DB { return s.db }

// --- SessionStore ---------------------------------------------------

// Get implements agentloop.SessionStore. An unknown id is not an error
// and does not create a row: the loop calls Get before every run and
// treats "no session yet" as normal, so it returns a zero Session and
// leaves Exists as the answer to whether one is really there.
func (s *Store) Get(ctx context.Context, id string) (agentloop.Session, error) {
	var (
		sess agentloop.Session
		data []byte
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, model, system_prompt, data, message_id FROM sessions WHERE id = ?`, id,
	).Scan(&sess.ID, &sess.Model, &sess.SystemPrompt, &data, &sess.MessageID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return agentloop.Session{}, nil
	case err != nil:
		return agentloop.Session{}, fmt.Errorf("agentloopsql: get session %s: %w", id, err)
	}
	if data != nil {
		sess.Data = json.RawMessage(data)
	}
	return sess, nil
}

// Exists implements agentloop.SessionStore, without creating anything.
func (s *Store) Exists(ctx context.Context, id string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM sessions WHERE id = ?`, id).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("agentloopsql: session exists %s: %w", id, err)
	}
	return true, nil
}

// UpdateData implements agentloop.SessionStore, replacing the session's
// data snapshot and creating the session if this is the first write.
func (s *Store) UpdateData(ctx context.Context, id string, snapshot json.RawMessage) error {
	now := s.now()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (id, data, created_at, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
		id, []byte(snapshot), now, now)
	if err != nil {
		return fmt.Errorf("agentloopsql: update session data %s: %w", id, err)
	}
	return nil
}

// Finalize implements agentloop.SessionStore, recording the summary of
// the run that just ended. A session finalizes once per Run, so later
// calls overwrite: the most recent run is what the session's status
// describes.
//
// It upserts rather than requiring the row to exist, so a run that
// answered without ever carrying data still leaves a session behind for
// List to find.
func (s *Store) Finalize(ctx context.Context, id string, sum agentloop.FinalizeSummary) error {
	now := s.now()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (id, created_at, updated_at, status, prompt_tokens, completion_tokens, step_count, duration_ms, data_bytes_carried)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		     updated_at         = excluded.updated_at,
		     status             = excluded.status,
		     prompt_tokens      = excluded.prompt_tokens,
		     completion_tokens  = excluded.completion_tokens,
		     step_count         = excluded.step_count,
		     duration_ms        = excluded.duration_ms,
		     data_bytes_carried = excluded.data_bytes_carried`,
		id, now, now, sum.Status, sum.PromptTokens, sum.CompletionTokens,
		sum.StepCount, sum.DurationMs, sum.DataBytesCarried)
	if err != nil {
		return fmt.Errorf("agentloopsql: finalize session %s: %w", id, err)
	}
	return nil
}

// --- StepStore ------------------------------------------------------

// Append implements agentloop.StepStore. The session row is touched in
// the same transaction so a session is never listed without the steps
// that belong to it, or vice versa.
func (s *Store) Append(ctx context.Context, step agentloop.RunStep) error {
	createdAt := step.CreatedAt
	if createdAt.IsZero() {
		createdAt = s.clock()
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("agentloopsql: append step: %w", err)
	}
	defer tx.Rollback()

	now := s.clock().UTC().Format(timeLayout)
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO sessions (id, created_at, updated_at) VALUES (?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET updated_at = excluded.updated_at`,
		step.SessionID, now, now); err != nil {
		return fmt.Errorf("agentloopsql: touch session %s: %w", step.SessionID, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO steps (session_id, step_index, step_type, content, tool_args, duration_ms, prompt_tokens, completion_tokens, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		step.SessionID, step.StepIndex, step.StepType, step.Content, []byte(step.ToolArgs),
		step.DurationMs, step.PromptTokens, step.CompletionTokens,
		createdAt.UTC().Format(timeLayout)); err != nil {
		return fmt.Errorf("agentloopsql: append step: %w", err)
	}
	return tx.Commit()
}

// LastN implements agentloop.StepStore, returning the most recent n
// steps in CHRONOLOGICAL order. The ordering is load-bearing — the loop
// replays these straight into the LLM context — so the rows are taken
// newest-first to apply the limit and then reversed.
func (s *Store) LastN(ctx context.Context, sessionID string, n int) ([]agentloop.RunStep, error) {
	if n <= 0 {
		// Mirrors the in-memory store: a non-positive window is "no cap",
		// not "no rows".
		n = -1
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT session_id, step_index, step_type, content, tool_args, duration_ms, prompt_tokens, completion_tokens, created_at
		 FROM steps WHERE session_id = ? ORDER BY id DESC LIMIT ?`, sessionID, n)
	if err != nil {
		return nil, fmt.Errorf("agentloopsql: last steps for %s: %w", sessionID, err)
	}
	defer rows.Close()

	var out []agentloop.RunStep
	for rows.Next() {
		step, err := scanStep(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, step)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("agentloopsql: last steps for %s: %w", sessionID, err)
	}
	reverse(out)
	return out, nil
}

// Steps returns a session's complete trace, oldest first.
func (s *Store) Steps(ctx context.Context, sessionID string) ([]agentloop.RunStep, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT session_id, step_index, step_type, content, tool_args, duration_ms, prompt_tokens, completion_tokens, created_at
		 FROM steps WHERE session_id = ? ORDER BY id ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("agentloopsql: steps for %s: %w", sessionID, err)
	}
	defer rows.Close()

	var out []agentloop.RunStep
	for rows.Next() {
		step, err := scanStep(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, step)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("agentloopsql: steps for %s: %w", sessionID, err)
	}
	return out, nil
}

func scanStep(rows *sql.Rows) (agentloop.RunStep, error) {
	var (
		step      agentloop.RunStep
		toolArgs  []byte
		createdAt string
	)
	if err := rows.Scan(&step.SessionID, &step.StepIndex, &step.StepType, &step.Content,
		&toolArgs, &step.DurationMs, &step.PromptTokens, &step.CompletionTokens, &createdAt); err != nil {
		return agentloop.RunStep{}, fmt.Errorf("agentloopsql: scan step: %w", err)
	}
	if toolArgs != nil {
		step.ToolArgs = json.RawMessage(toolArgs)
	}
	// A timestamp that will not parse is not worth failing a whole
	// history rehydrate over — the step's content is what the loop
	// replays.
	if t, err := time.Parse(timeLayout, createdAt); err == nil {
		step.CreatedAt = t
	}
	return step, nil
}

func reverse(steps []agentloop.RunStep) {
	for i, j := 0, len(steps)-1; i < j; i, j = i+1, j-1 {
		steps[i], steps[j] = steps[j], steps[i]
	}
}

func (s *Store) now() string { return s.clock().UTC().Format(timeLayout) }

var (
	_ agentloop.SessionStore = (*Store)(nil)
	_ agentloop.StepStore    = (*Store)(nil)
)
