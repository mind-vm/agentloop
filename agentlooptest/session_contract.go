package agentlooptest

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/mind-vm/agentloop"
)

// SessionStoreHarness configures the SessionStoreContract for a
// particular implementation.
type SessionStoreHarness struct {
	// NewStore returns a FRESH, empty SessionStore on each call.
	NewStore func() agentloop.SessionStore
}

// SessionStoreContract asserts the behavioural contract every
// agentloop.SessionStore must satisfy.
//
//	func TestPgxSessionStore_Contract(t *testing.T) {
//	    agentlooptest.SessionStoreContract(t, agentlooptest.SessionStoreHarness{
//	        NewStore: func() agentloop.SessionStore { return freshStore(t) },
//	    })
//	}
//
// The load-bearing guarantees are that an unknown id is not an error and
// does not bring a session into being — the loop calls Get before every
// run and treats "no session yet" as normal, so a store that created one
// there would make Exists meaningless — and that Get reflects the most
// recent UpdateData, which is how an agent's working memory survives
// across Run boundaries.
//
// Deliberately unspecified: whether Finalize creates a session that does
// not exist. The interface offers no way to read a summary back, so the
// contract can only require that recording one succeeds; a store free to
// upsert there (to make its own session listing complete, say) stays
// conformant.
func SessionStoreContract(t *testing.T, h SessionStoreHarness) {
	t.Helper()
	ctx := context.Background()

	t.Run("Get on an unknown session is not an error", func(t *testing.T) {
		s := h.NewStore()
		if _, err := s.Get(ctx, sessEmpty); err != nil {
			t.Fatalf("Get unknown: %v", err)
		}
	})

	t.Run("Get does not bring a session into being", func(t *testing.T) {
		s := h.NewStore()
		if _, err := s.Get(ctx, sessEmpty); err != nil {
			t.Fatalf("Get: %v", err)
		}
		exists, err := s.Exists(ctx, sessEmpty)
		if err != nil {
			t.Fatalf("Exists: %v", err)
		}
		if exists {
			t.Fatal("Get must not create a session — Exists is the only creation-free existence check the loop has")
		}
	})

	t.Run("Exists is false for an unknown session", func(t *testing.T) {
		s := h.NewStore()
		exists, err := s.Exists(ctx, sessEmpty)
		if err != nil {
			t.Fatalf("Exists: %v", err)
		}
		if exists {
			t.Fatal("want false for a session never written to")
		}
	})

	t.Run("UpdateData makes the session exist", func(t *testing.T) {
		s := h.NewStore()
		if err := s.UpdateData(ctx, sessMain, json.RawMessage(`{"a":1}`)); err != nil {
			t.Fatalf("UpdateData: %v", err)
		}
		exists, err := s.Exists(ctx, sessMain)
		if err != nil {
			t.Fatalf("Exists: %v", err)
		}
		if !exists {
			t.Fatal("a session written to must exist")
		}
	})

	t.Run("Get returns the most recent snapshot", func(t *testing.T) {
		s := h.NewStore()
		if err := s.UpdateData(ctx, sessMain, json.RawMessage(`{"step":1}`)); err != nil {
			t.Fatalf("UpdateData: %v", err)
		}
		if err := s.UpdateData(ctx, sessMain, json.RawMessage(`{"step":2}`)); err != nil {
			t.Fatalf("UpdateData again: %v", err)
		}
		sess, err := s.Get(ctx, sessMain)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if string(sess.Data) != `{"step":2}` {
			t.Fatalf("Session.Data = %q, want the latest snapshot %q", sess.Data, `{"step":2}`)
		}
	})

	t.Run("a nil snapshot is accepted", func(t *testing.T) {
		s := h.NewStore()
		if err := s.UpdateData(ctx, sessMain, nil); err != nil {
			t.Fatalf("UpdateData(nil): %v", err)
		}
		if _, err := s.Get(ctx, sessMain); err != nil {
			t.Fatalf("Get after a nil snapshot: %v", err)
		}
	})

	t.Run("Finalize records a summary", func(t *testing.T) {
		s := h.NewStore()
		summary := agentloop.FinalizeSummary{
			Status: "completed", PromptTokens: 120, CompletionTokens: 34,
			StepCount: 6, DurationMs: 1200, DataBytesCarried: 4096,
		}
		if err := s.Finalize(ctx, sessMain, summary); err != nil {
			t.Fatalf("Finalize: %v", err)
		}
		// A session finalizes once per Run, so a second call for the same
		// id is normal and must not be rejected as a duplicate.
		summary.Status = "error"
		if err := s.Finalize(ctx, sessMain, summary); err != nil {
			t.Fatalf("Finalize again: %v", err)
		}
	})

	t.Run("Finalize after UpdateData leaves the snapshot readable", func(t *testing.T) {
		s := h.NewStore()
		if err := s.UpdateData(ctx, sessMain, json.RawMessage(`{"keep":true}`)); err != nil {
			t.Fatalf("UpdateData: %v", err)
		}
		if err := s.Finalize(ctx, sessMain, agentloop.FinalizeSummary{Status: "completed"}); err != nil {
			t.Fatalf("Finalize: %v", err)
		}
		sess, err := s.Get(ctx, sessMain)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if string(sess.Data) != `{"keep":true}` {
			t.Fatalf("finalizing a run must not discard its data snapshot: got %q", sess.Data)
		}
	})

	t.Run("sessions are isolated", func(t *testing.T) {
		s := h.NewStore()
		if err := s.UpdateData(ctx, sessA, json.RawMessage(`{"who":"a"}`)); err != nil {
			t.Fatalf("UpdateData a: %v", err)
		}
		if err := s.UpdateData(ctx, sessB, json.RawMessage(`{"who":"b"}`)); err != nil {
			t.Fatalf("UpdateData b: %v", err)
		}
		gotA, err := s.Get(ctx, sessA)
		if err != nil {
			t.Fatalf("Get a: %v", err)
		}
		gotB, err := s.Get(ctx, sessB)
		if err != nil {
			t.Fatalf("Get b: %v", err)
		}
		if string(gotA.Data) != `{"who":"a"}` || string(gotB.Data) != `{"who":"b"}` {
			t.Fatalf("sessions leaked into each other: a=%q b=%q", gotA.Data, gotB.Data)
		}
	})
}
