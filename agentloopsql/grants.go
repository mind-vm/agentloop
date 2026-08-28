package agentloopsql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// grantSchema is applied alongside the main schema.
//
// A grant is keyed by the QUESTION that was approved, not by the thing
// it was about. The human-in-the-loop seam carries only a prompt string
// — there is no structured field naming the domain, file, or command at
// issue — so keying on the exact question is both the most robust
// reading available and one that generalises: any approval any
// capability asks for is remembered the same way, with nothing to parse.
const grantSchema = `
CREATE TABLE IF NOT EXISTS session_grants (
    session_id TEXT NOT NULL,
    prompt     TEXT NOT NULL,
    granted_at TEXT NOT NULL,
    PRIMARY KEY (session_id, prompt)
);
`

// Grant records that the user approved prompt for this session, so
// resuming the session does not ask again.
//
// Only approvals are recorded. A refusal is deliberately not persisted:
// remembering "no" forever would silently block a capability the user
// might well allow on a later run, and being asked twice is the lesser
// harm.
func (s *Store) Grant(ctx context.Context, sessionID, prompt string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO session_grants (session_id, prompt, granted_at) VALUES (?, ?, ?)
		 ON CONFLICT(session_id, prompt) DO UPDATE SET granted_at = excluded.granted_at`,
		sessionID, prompt, s.now())
	if err != nil {
		return fmt.Errorf("agentloopsql: record grant for %s: %w", sessionID, err)
	}
	return nil
}

// IsGranted reports whether this session has already approved prompt.
func (s *Store) IsGranted(ctx context.Context, sessionID, prompt string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM session_grants WHERE session_id = ? AND prompt = ?`, sessionID, prompt).Scan(&one)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	case err != nil:
		return false, fmt.Errorf("agentloopsql: read grant for %s: %w", sessionID, err)
	}
	return true, nil
}

// Grants returns every prompt this session has approved, oldest first.
func (s *Store) Grants(ctx context.Context, sessionID string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT prompt FROM session_grants WHERE session_id = ? ORDER BY granted_at ASC, prompt ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("agentloopsql: list grants for %s: %w", sessionID, err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var prompt string
		if err := rows.Scan(&prompt); err != nil {
			return nil, fmt.Errorf("agentloopsql: scan grant: %w", err)
		}
		out = append(out, prompt)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("agentloopsql: list grants for %s: %w", sessionID, err)
	}
	return out, nil
}

// Revoke drops every grant recorded for a session.
func (s *Store) Revoke(ctx context.Context, sessionID string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM session_grants WHERE session_id = ?`, sessionID); err != nil {
		return fmt.Errorf("agentloopsql: revoke grants for %s: %w", sessionID, err)
	}
	return nil
}
