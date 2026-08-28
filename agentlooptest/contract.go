// Package agentlooptest provides reusable conformance harnesses for the
// agentloop store contracts. An application implementing agentloop.StepStore
// / SessionStore against its own schema runs these from its own _test.go
// as an acceptance gate, so every implementation is held to the same
// behavioural guarantees the loop relies on.
//
// It imports "testing" because it is consumed only from tests.
package agentlooptest

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/mind-vm/agentloop"
)

// StepStoreHarness configures the StepStoreContract for a particular
// implementation.
type StepStoreHarness struct {
	// NewStore returns a FRESH, empty StepStore on each call.
	NewStore func() agentloop.StepStore

	// EnsureSession is called once per session id before any step is
	// appended for it. Relational stores whose steps table has a
	// foreign key to a sessions table use it to create the prerequisite
	// session row; in-memory stores leave it nil. It receives the
	// session ids below.
	EnsureSession func(sessionID string)
}

// The contract drives these session ids. They are valid UUIDs so the
// harness works against UUID-keyed relational stores, not just
// string-keyed in-memory ones.
const (
	sessMain  = "11111111-1111-1111-1111-111111111111"
	sessA     = "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa"
	sessB     = "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"
	sessEmpty = "99999999-9999-9999-9999-999999999999" // never has steps appended
)

// StepStoreContract asserts the behavioural contract every
// agentloop.StepStore must satisfy.
//
//	func TestPgxStepStore_Contract(t *testing.T) {
//	    agentlooptest.StepStoreContract(t, agentlooptest.StepStoreHarness{
//	        NewStore:      func() agentloop.StepStore { return freshStore(t) },
//	        EnsureSession: func(id string) { seedSession(t, id) }, // FK prerequisite
//	    })
//	}
//
// The load-bearing guarantee is LastN's chronological (oldest-first)
// order: the loop replays it straight into the LLM context, so a store
// that returns rows newest-first would silently corrupt multi-turn
// history. The second is that ToolArgs survives a round trip: a
// compaction checkpoint keeps its marker there, and a store that drops
// the field turns every checkpoint into an unusable row — which shows
// up not as an error but as a session quietly paying to re-summarize
// itself on every run.
func StepStoreContract(t *testing.T, h StepStoreHarness) {
	t.Helper()
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	appendN := func(t *testing.T, s agentloop.StepStore, sessionID string, n int) {
		t.Helper()
		if h.EnsureSession != nil {
			h.EnsureSession(sessionID)
		}
		for i := 0; i < n; i++ {
			step := agentloop.RunStep{
				SessionID: sessionID,
				StepIndex: int32(i),
				StepType:  "response",
				Content:   "content",
				// The loop always writes a non-nil ToolArgs ("{}"); mirror
				// that so the contract matches the real shape a NOT NULL
				// column sees.
				ToolArgs:  []byte("{}"),
				CreatedAt: base.Add(time.Duration(i) * time.Second),
			}
			if err := s.Append(ctx, step); err != nil {
				t.Fatalf("Append #%d: %v", i, err)
			}
		}
	}

	t.Run("LastN on an empty session returns no rows and no error", func(t *testing.T) {
		s := h.NewStore()
		got, err := s.LastN(ctx, sessEmpty, 10)
		if err != nil {
			t.Fatalf("LastN empty: %v", err)
		}
		if len(got) != 0 {
			t.Fatalf("want 0 rows for an unknown session, got %d", len(got))
		}
	})

	t.Run("Append then LastN returns rows oldest-first with fields preserved", func(t *testing.T) {
		s := h.NewStore()
		appendN(t, s, sessMain, 4)
		got, err := s.LastN(ctx, sessMain, 10)
		if err != nil {
			t.Fatalf("LastN: %v", err)
		}
		if len(got) != 4 {
			t.Fatalf("want 4 rows, got %d", len(got))
		}
		for i, row := range got {
			if row.StepIndex != int32(i) {
				t.Errorf("row %d: StepIndex got %d, want %d (rows must be oldest-first)", i, row.StepIndex, i)
			}
			if row.Content != "content" || row.StepType != "response" {
				t.Errorf("row %d: fields not preserved: %+v", i, row)
			}
		}
	})

	t.Run("ToolArgs round-trips, so a compaction checkpoint stays usable", func(t *testing.T) {
		s := h.NewStore()
		if h.EnsureSession != nil {
			h.EnsureSession(sessMain)
		}
		marker, err := json.Marshal(agentloop.CompactionCheckpoint{RetainFromStep: 42})
		if err != nil {
			t.Fatalf("marshal checkpoint: %v", err)
		}
		step := agentloop.RunStep{
			SessionID: sessMain,
			StepIndex: 0,
			StepType:  agentloop.StepTypeSummary,
			Content:   "[summary] earlier turns",
			ToolArgs:  marker,
			CreatedAt: base,
		}
		if err := s.Append(ctx, step); err != nil {
			t.Fatalf("Append checkpoint: %v", err)
		}

		got, err := s.LastN(ctx, sessMain, 10)
		if err != nil {
			t.Fatalf("LastN: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("want 1 row, got %d", len(got))
		}
		if got[0].StepType != agentloop.StepTypeSummary || got[0].Content != step.Content {
			t.Errorf("checkpoint fields not preserved: %+v", got[0])
		}
		// Compared as JSON rather than bytes: a store that keeps this in
		// a JSONB column may legitimately re-serialize it.
		var want, have agentloop.CompactionCheckpoint
		if err := json.Unmarshal(marker, &want); err != nil {
			t.Fatalf("unmarshal want: %v", err)
		}
		if err := json.Unmarshal(got[0].ToolArgs, &have); err != nil {
			t.Fatalf("ToolArgs did not survive the round trip (%q): %v", got[0].ToolArgs, err)
		}
		if have != want {
			t.Errorf("ToolArgs round-tripped as %+v, want %+v", have, want)
		}
	})

	t.Run("LastN caps to the most recent n, still oldest-first", func(t *testing.T) {
		s := h.NewStore()
		appendN(t, s, sessMain, 5) // indices 0..4
		got, err := s.LastN(ctx, sessMain, 2)
		if err != nil {
			t.Fatalf("LastN: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("want 2 rows, got %d", len(got))
		}
		// The two most recent are indices 3 and 4, returned oldest-first.
		if got[0].StepIndex != 3 || got[1].StepIndex != 4 {
			t.Errorf("LastN(2) indices: got [%d %d], want [3 4]", got[0].StepIndex, got[1].StepIndex)
		}
	})

	t.Run("n larger than the row count returns all rows", func(t *testing.T) {
		s := h.NewStore()
		appendN(t, s, sessMain, 3)
		got, err := s.LastN(ctx, sessMain, 100)
		if err != nil {
			t.Fatalf("LastN: %v", err)
		}
		if len(got) != 3 {
			t.Fatalf("want all 3 rows, got %d", len(got))
		}
	})

	t.Run("sessions are isolated", func(t *testing.T) {
		s := h.NewStore()
		appendN(t, s, sessA, 2)
		appendN(t, s, sessB, 3)
		gotA, err := s.LastN(ctx, sessA, 10)
		if err != nil {
			t.Fatalf("LastN a: %v", err)
		}
		gotB, err := s.LastN(ctx, sessB, 10)
		if err != nil {
			t.Fatalf("LastN b: %v", err)
		}
		if len(gotA) != 2 {
			t.Errorf("session a: want 2 rows, got %d", len(gotA))
		}
		if len(gotB) != 3 {
			t.Errorf("session b: want 3 rows, got %d", len(gotB))
		}
		for _, row := range gotA {
			if row.SessionID != sessA {
				t.Errorf("session a leaked a row from %q", row.SessionID)
			}
		}
	})
}
