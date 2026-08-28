package agentloopsql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/mind-vm/agentloop"
)

// ErrNoSession reports that no session matched. Callers distinguish "you
// asked for a session that isn't here" from a database failure with
// errors.Is.
var ErrNoSession = errors.New("agentloopsql: no such session")

// SessionInfo is one row of a session listing: the metadata a person
// needs to recognise a session, without loading its trace.
type SessionInfo struct {
	ID        string
	Model     string
	Status    string
	Steps     int32
	Tokens    agentloop.TokenUsage
	CreatedAt time.Time
	UpdatedAt time.Time

	// Opening is the session's first user message, which is how a person
	// actually recognises a session — an id tells them nothing.
	Opening string
}

// List returns sessions most recently updated first, capped at limit
// (non-positive means no cap).
func (s *Store) List(ctx context.Context, limit int) ([]SessionInfo, error) {
	if limit <= 0 {
		limit = -1
	}
	// The opening message comes from the session's first user step. A
	// session whose steps were never written — or a run that failed
	// before persisting one — simply has none.
	rows, err := s.db.QueryContext(ctx,
		`SELECT s.id, s.model, s.status, s.step_count, s.prompt_tokens, s.completion_tokens,
		        s.created_at, s.updated_at,
		        COALESCE((SELECT content FROM steps
		                  WHERE session_id = s.id AND step_type = 'user'
		                  ORDER BY id ASC LIMIT 1), '')
		 FROM sessions s ORDER BY s.updated_at DESC, s.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("agentloopsql: list sessions: %w", err)
	}
	defer rows.Close()

	var out []SessionInfo
	for rows.Next() {
		var (
			info                 SessionInfo
			createdAt, updatedAt string
		)
		if err := rows.Scan(&info.ID, &info.Model, &info.Status, &info.Steps,
			&info.Tokens.Prompt, &info.Tokens.Completion,
			&createdAt, &updatedAt, &info.Opening); err != nil {
			return nil, fmt.Errorf("agentloopsql: scan session: %w", err)
		}
		info.CreatedAt, _ = time.Parse(timeLayout, createdAt)
		info.UpdatedAt, _ = time.Parse(timeLayout, updatedAt)
		out = append(out, info)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("agentloopsql: list sessions: %w", err)
	}
	return out, nil
}

// MostRecent returns the id of the most recently updated session — what
// "continue where I left off" resolves to. It returns ErrNoSession when
// the database is empty, so a caller can say so rather than starting a
// session the user did not ask for.
func (s *Store) MostRecent(ctx context.Context) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx,
		`SELECT id FROM sessions ORDER BY updated_at DESC, id DESC LIMIT 1`).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return "", ErrNoSession
	case err != nil:
		return "", fmt.Errorf("agentloopsql: most recent session: %w", err)
	}
	return id, nil
}

// Delete removes a session and its whole trace. Deleting a session that
// is not there returns ErrNoSession rather than succeeding quietly — a
// mistyped id should say so.
func (s *Store) Delete(ctx context.Context, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("agentloopsql: delete session %s: %w", id, err)
	}
	defer tx.Rollback()

	res, err := tx.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("agentloopsql: delete session %s: %w", id, err)
	}
	removed, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("agentloopsql: delete session %s: %w", id, err)
	}
	// Steps and grants are deleted regardless: rows whose session is
	// already gone are orphaned, and a stale grant is worse than
	// orphaned — it would silently pre-approve a later session that
	// happened to reuse the id.
	if _, err := tx.ExecContext(ctx, `DELETE FROM steps WHERE session_id = ?`, id); err != nil {
		return fmt.Errorf("agentloopsql: delete steps for %s: %w", id, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM session_grants WHERE session_id = ?`, id); err != nil {
		return fmt.Errorf("agentloopsql: delete grants for %s: %w", id, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("agentloopsql: delete session %s: %w", id, err)
	}
	if removed == 0 {
		return fmt.Errorf("%w: %s", ErrNoSession, id)
	}
	return nil
}
