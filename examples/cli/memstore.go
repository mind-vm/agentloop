package main

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/jryannel/agentloop"
)

// memSessionStore is a trivial in-memory agentloop.SessionStore. Session
// rows are created on first Get and live only for the process lifetime —
// fine for a demo, not for anything you'd want to survive a restart.
type memSessionStore struct {
	mu       sync.Mutex
	sessions map[string]*agentloop.Session
}

func newMemSessionStore() *memSessionStore {
	return &memSessionStore{sessions: make(map[string]*agentloop.Session)}
}

func (m *memSessionStore) Get(_ context.Context, sessionID string) (agentloop.Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	sess, ok := m.sessions[sessionID]
	if !ok {
		sess = &agentloop.Session{ID: sessionID}
		m.sessions[sessionID] = sess
	}
	return *sess, nil
}

func (m *memSessionStore) Exists(_ context.Context, sessionID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.sessions[sessionID]
	return ok, nil
}

func (m *memSessionStore) UpdateData(_ context.Context, sessionID string, snapshot json.RawMessage) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sess, ok := m.sessions[sessionID]; ok {
		sess.Data = snapshot
	}
	return nil
}

func (m *memSessionStore) Finalize(_ context.Context, _ string, _ agentloop.FinalizeSummary) error {
	return nil
}

var _ agentloop.SessionStore = (*memSessionStore)(nil)

// memStepStore is a trivial in-memory agentloop.StepStore — the full
// per-turn trace of every session, kept only for the process lifetime.
type memStepStore struct {
	mu    sync.Mutex
	steps map[string][]agentloop.RunStep
}

func newMemStepStore() *memStepStore {
	return &memStepStore{steps: make(map[string][]agentloop.RunStep)}
}

func (m *memStepStore) Append(_ context.Context, step agentloop.RunStep) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.steps[step.SessionID] = append(m.steps[step.SessionID], step)
	return nil
}

func (m *memStepStore) LastN(_ context.Context, sessionID string, n int) ([]agentloop.RunStep, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	all := m.steps[sessionID]
	if n <= 0 || len(all) <= n {
		out := make([]agentloop.RunStep, len(all))
		copy(out, all)
		return out, nil
	}
	out := make([]agentloop.RunStep, n)
	copy(out, all[len(all)-n:])
	return out, nil
}

var _ agentloop.StepStore = (*memStepStore)(nil)
