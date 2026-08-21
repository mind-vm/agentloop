package agentloopmem_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jryannel/agentloop"
	"github.com/jryannel/agentloop/agentloopmem"
)

func TestSessionStore_PutGet(t *testing.T) {
	s := agentloopmem.NewSessionStore(nil)
	s.Put(agentloop.Session{ID: "x", Model: "test"})
	got, err := s.Get(context.Background(), "x")
	if err != nil || got.Model != "test" {
		t.Fatalf("get: %+v err=%v", got, err)
	}
}

func TestSessionStore_Exists(t *testing.T) {
	s := agentloopmem.NewSessionStore(nil)
	// Unknown id: Get returns a zero session with NIL error (the footgun);
	// Exists is the unambiguous channel.
	if got, err := s.Get(context.Background(), "missing"); err != nil || got.ID != "" {
		t.Fatalf("get unknown: %+v err=%v (must be zero session, nil error)", got, err)
	}
	if ok, err := s.Exists(context.Background(), "missing"); err != nil || ok {
		t.Fatalf("exists(missing) = %v, %v; want false, nil", ok, err)
	}
	s.Put(agentloop.Session{ID: "here"})
	if ok, err := s.Exists(context.Background(), "here"); err != nil || !ok {
		t.Fatalf("exists(here) = %v, %v; want true, nil", ok, err)
	}
}

func TestSessionStore_UpdateData_RehydratesOnGet(t *testing.T) {
	s := agentloopmem.NewSessionStore(nil)
	s.Put(agentloop.Session{ID: "x"})
	snap := json.RawMessage(`{"counter":3}`)
	if err := s.UpdateData(context.Background(), "x", snap); err != nil {
		t.Fatalf("UpdateData: %v", err)
	}
	got, err := s.Get(context.Background(), "x")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got.Data) != `{"counter":3}` {
		t.Fatalf("Data not rehydrated: got %q", got.Data)
	}
	if string(s.Snapshot("x")) != `{"counter":3}` {
		t.Fatalf("Snapshot accessor: got %q", s.Snapshot("x"))
	}
}

func TestSessionStore_Finalize(t *testing.T) {
	s := agentloopmem.NewSessionStore(nil)
	f := agentloop.FinalizeSummary{Status: "completed", PromptTokens: 10}
	_ = s.Finalize(context.Background(), "x", f)
	got, ok := s.Final("x")
	if !ok {
		t.Fatal("Final should return true after Finalize")
	}
	if got.Status != "completed" || got.PromptTokens != 10 {
		t.Errorf("Final: got %+v, want %+v", got, f)
	}
}

func TestStepStore_AppendAndLastN(t *testing.T) {
	store := agentloopmem.NewStepStore()
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_ = store.Append(ctx, agentloop.RunStep{SessionID: "x", StepType: "user", Content: "msg", StepIndex: int32(i)})
	}
	got, err := store.LastN(ctx, "x", 3)
	if err != nil {
		t.Fatalf("LastN: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("LastN(3) length: got %d", len(got))
	}
	if got[0].StepIndex != 2 || got[2].StepIndex != 4 {
		t.Errorf("LastN order: got indices %d..%d", got[0].StepIndex, got[2].StepIndex)
	}
	if all := store.All("x"); len(all) != 5 {
		t.Errorf("All: got %d, want 5", len(all))
	}
}

func TestStepStore_LastN_LargerThanContent(t *testing.T) {
	store := agentloopmem.NewStepStore()
	_ = store.Append(context.Background(), agentloop.RunStep{SessionID: "x", StepType: "user"})
	got, _ := store.LastN(context.Background(), "x", 10)
	if len(got) != 1 {
		t.Fatalf("LastN(10) on 1-row store: got %d", len(got))
	}
}
