package agentloopmem

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/mind-vm/agentloop"
)

// SessionStore is an in-memory agentloop.SessionStore. New constructs
// it; use Put to seed an initial Session before the first Run.
type SessionStore struct {
	mu        sync.Mutex
	sessions  map[string]agentloop.Session
	snapshots map[string]json.RawMessage
	finals    map[string]agentloop.FinalizeSummary
	clock     func() time.Time
}

// NewSessionStore returns an empty in-memory SessionStore. clock is
// optional; nil falls back to time.Now.
func NewSessionStore(clock func() time.Time) *SessionStore {
	if clock == nil {
		clock = time.Now
	}
	return &SessionStore{
		sessions:  make(map[string]agentloop.Session),
		snapshots: make(map[string]json.RawMessage),
		finals:    make(map[string]agentloop.FinalizeSummary),
		clock:     clock,
	}
}

// Put seeds a Session by ID. Used to set up a persona / model before
// the first Run — Get() would otherwise return a zero-value Session.
func (s *SessionStore) Put(sess agentloop.Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sess.ID] = sess
}

// Get implements agentloop.SessionStore. Unknown IDs return a
// zero-value Session (empty ID) with a nil error — the loop treats
// that as a fresh session. Callers deciding "does this session exist?"
// must test Exists, never Get's error (which is always nil).
func (s *SessionStore) Get(_ context.Context, id string) (agentloop.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id], nil
}

// Exists reports whether a session has been Put, without creating one
// — the unambiguous existence check (Get can't tell "fresh" from
// "found" here).
func (s *SessionStore) Exists(_ context.Context, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.sessions[id]
	return ok, nil
}

// UpdateData stores the latest data snapshot under id. Each call
// replaces the previous snapshot; Snapshot returns the most recent.
func (s *SessionStore) UpdateData(_ context.Context, id string, snap json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshots[id] = snap
	// Mirror the snapshot onto the Session.Data so a subsequent Get
	// rehydrates the agent's working memory across Run boundaries.
	sess := s.sessions[id]
	sess.ID = id
	sess.Data = snap
	s.sessions[id] = sess
	return nil
}

// Snapshot returns the most recent data snapshot persisted for id, or
// nil when none. Useful for tests asserting on the agent's state
// without going through Get().
func (s *SessionStore) Snapshot(id string) json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshots[id]
}

// Finalize records the run-completion summary under id. Later calls
// for the same id overwrite — a session finalizes once per Run, so the
// most recent is the right semantics.
func (s *SessionStore) Finalize(_ context.Context, id string, summary agentloop.FinalizeSummary) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finals[id] = summary
	return nil
}

// Final returns the most recent FinalizeSummary for id, or (zero,
// false) when no Finalize has been called for it.
func (s *SessionStore) Final(id string) (agentloop.FinalizeSummary, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f, ok := s.finals[id]
	return f, ok
}

// StepStore is an in-memory agentloop.StepStore. Steps are stored in
// insertion order per session; LastN returns the trailing window.
type StepStore struct {
	mu   sync.Mutex
	rows map[string][]agentloop.RunStep
}

// NewStepStore returns an empty in-memory StepStore.
func NewStepStore() *StepStore {
	return &StepStore{rows: make(map[string][]agentloop.RunStep)}
}

// Append implements agentloop.StepStore.
func (s *StepStore) Append(_ context.Context, step agentloop.RunStep) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[step.SessionID] = append(s.rows[step.SessionID], step)
	return nil
}

// LastN implements agentloop.StepStore.
func (s *StepStore) LastN(_ context.Context, sessionID string, n int) ([]agentloop.RunStep, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := s.rows[sessionID]
	if n <= 0 || len(rows) <= n {
		out := make([]agentloop.RunStep, len(rows))
		copy(out, rows)
		return out, nil
	}
	out := make([]agentloop.RunStep, n)
	copy(out, rows[len(rows)-n:])
	return out, nil
}

// All returns every step recorded for sessionID in insertion order.
// Useful for tests asserting on the full trace.
func (s *StepStore) All(sessionID string) []agentloop.RunStep {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]agentloop.RunStep, len(s.rows[sessionID]))
	copy(out, s.rows[sessionID])
	return out
}

var (
	_ agentloop.SessionStore = (*SessionStore)(nil)
	_ agentloop.StepStore    = (*StepStore)(nil)
)
