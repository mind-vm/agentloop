package agentloopsql_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mind-vm/agentloop"
	"github.com/mind-vm/agentloop/agentloopsql"
	"github.com/mind-vm/agentloop/agentlooptest"
)

// fresh returns an empty Store backed by a file in the test's temp
// directory. Each call gets its own database.
func fresh(t *testing.T) *agentloopsql.Store {
	t.Helper()
	return open(t, filepath.Join(t.TempDir(), "sessions.db"))
}

func open(t *testing.T, path string) *agentloopsql.Store {
	t.Helper()
	s, err := agentloopsql.Open(path, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestStepStore_Contract(t *testing.T) {
	agentlooptest.StepStoreContract(t, agentlooptest.StepStoreHarness{
		NewStore: func() agentloop.StepStore { return fresh(t) },
		// Steps carry no foreign key to sessions — the loop has no
		// session-creation call to hang one off — so nothing to seed.
	})
}

func TestSessionStore_Contract(t *testing.T) {
	agentlooptest.SessionStoreContract(t, agentlooptest.SessionStoreHarness{
		NewStore: func() agentloop.SessionStore { return fresh(t) },
	})
}

// The loop derives each step's index from the size of the history window
// it just read, so a session that outgrows that window reuses indices it
// has already written. The store has to accept that; a (session_id,
// step_index) key would reject every write past the window.
func TestAppendAcceptsReusedStepIndices(t *testing.T) {
	ctx := context.Background()
	s := fresh(t)

	for _, idx := range []int32{0, 1, 2, 0, 1, 2} {
		if err := s.Append(ctx, agentloop.RunStep{
			SessionID: "s1", StepIndex: idx, StepType: "user",
			Content: "turn", ToolArgs: json.RawMessage(`{}`),
		}); err != nil {
			t.Fatalf("Append index %d: %v", idx, err)
		}
	}

	got, err := s.LastN(ctx, "s1", 100)
	if err != nil {
		t.Fatalf("LastN: %v", err)
	}
	if len(got) != 6 {
		t.Fatalf("want all 6 steps, got %d", len(got))
	}
	// Insertion order, not index order.
	want := []int32{0, 1, 2, 0, 1, 2}
	for i, step := range got {
		if step.StepIndex != want[i] {
			t.Errorf("step %d: index %d, want %d — rows must come back in insertion order", i, step.StepIndex, want[i])
		}
	}
}

func TestAppendPreservesEveryField(t *testing.T) {
	ctx := context.Background()
	s := fresh(t)
	created := time.Date(2026, 3, 4, 5, 6, 7, 890000000, time.UTC)

	want := agentloop.RunStep{
		SessionID: "s1", StepIndex: 3, StepType: "execute_js_result",
		Content: "the result", ToolArgs: json.RawMessage(`{"k":"v"}`),
		DurationMs: 1234, PromptTokens: 55, CompletionTokens: 6,
		CreatedAt: created,
	}
	if err := s.Append(ctx, want); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := s.LastN(ctx, "s1", 1)
	if err != nil {
		t.Fatalf("LastN: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 step, got %d", len(got))
	}
	g := got[0]
	if g.StepType != want.StepType || g.Content != want.Content || g.StepIndex != want.StepIndex ||
		g.DurationMs != want.DurationMs || g.PromptTokens != want.PromptTokens || g.CompletionTokens != want.CompletionTokens {
		t.Errorf("fields not round-tripped:\n got %+v\nwant %+v", g, want)
	}
	if string(g.ToolArgs) != `{"k":"v"}` {
		t.Errorf("ToolArgs = %q, want %q", g.ToolArgs, `{"k":"v"}`)
	}
	if !g.CreatedAt.Equal(created) {
		t.Errorf("CreatedAt = %s, want %s", g.CreatedAt, created)
	}
}

// The whole point of the package: a session read back through a second
// Store over the same file.
func TestSessionSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "sessions.db")

	first := open(t, path)
	if err := first.Append(ctx, agentloop.RunStep{
		SessionID: "s1", StepType: "user", Content: "remember this",
		ToolArgs: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := first.UpdateData(ctx, "s1", json.RawMessage(`{"carried":true}`)); err != nil {
		t.Fatalf("UpdateData: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	second := open(t, path)
	steps, err := second.LastN(ctx, "s1", 10)
	if err != nil {
		t.Fatalf("LastN after reopen: %v", err)
	}
	if len(steps) != 1 || steps[0].Content != "remember this" {
		t.Fatalf("trace did not survive reopen: %+v", steps)
	}
	sess, err := second.Get(ctx, "s1")
	if err != nil {
		t.Fatalf("Get after reopen: %v", err)
	}
	if string(sess.Data) != `{"carried":true}` {
		t.Fatalf("data did not survive reopen: %q", sess.Data)
	}
	if sess.ID != "s1" {
		t.Errorf("Session.ID = %q, want %q", sess.ID, "s1")
	}
}

// --- listing --------------------------------------------------------

func TestListOrdersByRecencyAndCarriesTheOpening(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	s, err := agentloopsql.Open(filepath.Join(t.TempDir(), "s.db"), func() time.Time {
		now = now.Add(time.Minute)
		return now
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	for _, tc := range []struct{ id, opening string }{
		{"older", "the first question"},
		{"newer", "the second question"},
	} {
		if err := s.Append(ctx, agentloop.RunStep{
			SessionID: tc.id, StepType: "user", Content: tc.opening, ToolArgs: json.RawMessage(`{}`),
		}); err != nil {
			t.Fatalf("Append %s: %v", tc.id, err)
		}
		// A later, non-user step must not become the listed opening.
		if err := s.Append(ctx, agentloop.RunStep{
			SessionID: tc.id, StepType: "response", Content: "an answer", ToolArgs: json.RawMessage(`{}`),
		}); err != nil {
			t.Fatalf("Append response: %v", err)
		}
	}
	if err := s.Finalize(ctx, "newer", agentloop.FinalizeSummary{
		Status: "completed", StepCount: 2, PromptTokens: 10, CompletionTokens: 2,
	}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	got, err := s.List(ctx, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 sessions, got %d", len(got))
	}
	if got[0].ID != "newer" {
		t.Errorf("most recently updated should sort first, got %q", got[0].ID)
	}
	if got[0].Opening != "the second question" {
		t.Errorf("Opening = %q, want the first USER step", got[0].Opening)
	}
	if got[0].Status != "completed" || got[0].Steps != 2 || got[0].Tokens.Prompt != 10 {
		t.Errorf("finalize summary not surfaced: %+v", got[0])
	}
}

func TestListRespectsLimit(t *testing.T) {
	ctx := context.Background()
	s := fresh(t)
	for _, id := range []string{"a", "b", "c"} {
		if err := s.UpdateData(ctx, id, json.RawMessage(`{}`)); err != nil {
			t.Fatalf("UpdateData %s: %v", id, err)
		}
	}
	got, err := s.List(ctx, 2)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 sessions, got %d", len(got))
	}
}

// A run that answers without ever carrying data still has to leave a
// session behind, or it is invisible to `sessions ls`.
func TestFinalizeAloneCreatesAListableSession(t *testing.T) {
	ctx := context.Background()
	s := fresh(t)
	if err := s.Finalize(ctx, "s1", agentloop.FinalizeSummary{Status: "completed"}); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	got, err := s.List(ctx, 0)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].ID != "s1" {
		t.Fatalf("want the finalized session listed, got %+v", got)
	}
}

func TestMostRecent(t *testing.T) {
	ctx := context.Background()
	s := fresh(t)

	if _, err := s.MostRecent(ctx); !errors.Is(err, agentloopsql.ErrNoSession) {
		t.Fatalf("empty database: got %v, want ErrNoSession", err)
	}

	now := time.Now()
	for i, id := range []string{"first", "second"} {
		if err := s.Append(ctx, agentloop.RunStep{
			SessionID: id, StepType: "user", Content: "hi",
			ToolArgs: json.RawMessage(`{}`), CreatedAt: now.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	got, err := s.MostRecent(ctx)
	if err != nil {
		t.Fatalf("MostRecent: %v", err)
	}
	if got != "second" {
		t.Errorf("MostRecent = %q, want %q", got, "second")
	}
}

func TestDelete(t *testing.T) {
	ctx := context.Background()
	s := fresh(t)

	if err := s.Append(ctx, agentloop.RunStep{
		SessionID: "s1", StepType: "user", Content: "hi", ToolArgs: json.RawMessage(`{}`),
	}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := s.Delete(ctx, "s1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	exists, err := s.Exists(ctx, "s1")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if exists {
		t.Error("session should be gone")
	}
	steps, err := s.LastN(ctx, "s1", 10)
	if err != nil {
		t.Fatalf("LastN: %v", err)
	}
	if len(steps) != 0 {
		t.Errorf("the trace should go with the session, got %d steps", len(steps))
	}
}

func TestDeleteUnknownSessionSaysSo(t *testing.T) {
	if err := fresh(t).Delete(context.Background(), "nope"); !errors.Is(err, agentloopsql.ErrNoSession) {
		t.Fatalf("got %v, want ErrNoSession", err)
	}
}

func TestOpenRequiresAPath(t *testing.T) {
	if _, err := agentloopsql.Open("", nil); err == nil {
		t.Fatal("want an error for an empty path")
	}
}

// Two agentloop processes pointed at one database is ordinary usage —
// a second terminal, or a script fanning out runs. Each process has its
// own connection pool, so this opens several Stores over one file and
// writes through all of them at once. WAL plus busy_timeout is what has
// to make that work; without them this deadlocks or returns
// "database is locked".
func TestConcurrentStoresOverOneFile(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "shared.db")

	const writers, perWriter = 6, 12
	errs := make(chan error, writers)

	for w := 0; w < writers; w++ {
		go func(w int) {
			store, err := agentloopsql.Open(path, nil)
			if err != nil {
				errs <- fmt.Errorf("writer %d: open: %w", w, err)
				return
			}
			defer store.Close()

			id := fmt.Sprintf("session-%d", w)
			for i := 0; i < perWriter; i++ {
				if err := store.Append(ctx, agentloop.RunStep{
					SessionID: id, StepIndex: int32(i), StepType: "user",
					Content: "turn", ToolArgs: json.RawMessage(`{}`),
				}); err != nil {
					errs <- fmt.Errorf("writer %d step %d: %w", w, i, err)
					return
				}
			}
			errs <- nil
		}(w)
	}

	for w := 0; w < writers; w++ {
		select {
		case err := <-errs:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(30 * time.Second):
			t.Fatal("a concurrent writer never finished — the database is locked against itself")
		}
	}

	store := open(t, path)
	for w := 0; w < writers; w++ {
		steps, err := store.LastN(ctx, fmt.Sprintf("session-%d", w), 100)
		if err != nil {
			t.Fatalf("LastN: %v", err)
		}
		if len(steps) != perWriter {
			t.Errorf("session-%d: got %d steps, want %d", w, len(steps), perWriter)
		}
	}
}

// WAL is best-effort, but it should actually happen in the ordinary
// single-opener case — otherwise every reader blocks behind the writer.
func TestOpenEnablesWAL(t *testing.T) {
	s := fresh(t)
	var mode string
	if err := s.DB().QueryRow(`PRAGMA journal_mode`).Scan(&mode); err != nil {
		t.Fatalf("PRAGMA journal_mode: %v", err)
	}
	if !strings.EqualFold(mode, "wal") {
		t.Errorf("journal_mode = %q, want wal", mode)
	}
}

// busy_timeout is the setting that makes a second process wait rather
// than fail, so it has to reach every connection.
func TestOpenSetsBusyTimeout(t *testing.T) {
	s := fresh(t)
	var timeout int
	if err := s.DB().QueryRow(`PRAGMA busy_timeout`).Scan(&timeout); err != nil {
		t.Fatalf("PRAGMA busy_timeout: %v", err)
	}
	if timeout <= 0 {
		t.Errorf("busy_timeout = %d, want a positive wait", timeout)
	}
}
