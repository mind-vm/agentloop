package eval_test

import (
	"context"
	"testing"
	"time"

	"github.com/jryannel/agentloop"
	"github.com/jryannel/agentloop/agentloopmem"
	"github.com/jryannel/agentloop/eval"
	"github.com/jryannel/agentloop/evalmem"
	"github.com/jryannel/agentloop/llm"
	"github.com/jryannel/agentloop/sandbox"
)

// scriptedLLM is a minimal llm.Client used by both the agent loop
// (answers with DONE + a fixed reply) and the judge (always scores a
// pass) in TestRunSuite_AgainstARealLoop.
type scriptedLLM struct {
	reply string
}

func (s *scriptedLLM) Name() string { return "scripted" }

func (s *scriptedLLM) Complete(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{Content: "Score: 9\nRationale: matches expectations"}, nil
}

func (s *scriptedLLM) Stream(_ context.Context, _ llm.CompletionRequest, onChunk func(string)) (llm.CompletionResponse, error) {
	if onChunk != nil {
		onChunk(s.reply)
	}
	return llm.CompletionResponse{Content: "DONE\n" + s.reply, InputTokens: 3, OutputTokens: 2}, nil
}

// TestRunSuite_AgainstARealLoop is the integration test for the
// design claim in the eval package doc comment: RunSuite needs no
// separate session-creation step because agentloop.SessionStore.Get
// auto-creates a shell for an unknown ID. This wires a real
// agentloop.Loop (agentloopmem stores, DefaultSandboxBuilder, no
// pre-seeded sessions) rather than a test double for the runner.
func TestRunSuite_AgainstARealLoop(t *testing.T) {
	agentLLM := &scriptedLLM{reply: "the answer is 4"}
	loop := agentloop.New(agentloop.Config{
		LLM:            agentLLM,
		Sessions:       agentloopmem.NewSessionStore(time.Now),
		Steps:          agentloopmem.NewStepStore(),
		SandboxBuilder: &agentloop.DefaultSandboxBuilder{Capabilities: agentloop.DefaultCapabilities(agentLLM, "")},
		Policy:         sandbox.AllowAll,
	})

	judgeLLM := &scriptedLLM{}
	store := evalmem.New()
	ctx := context.Background()

	suite, err := store.CreateSuite(ctx, "arithmetic", "")
	if err != nil {
		t.Fatalf("CreateSuite: %v", err)
	}
	if _, err := store.AddCase(ctx, suite.ID, "sums", "what is 2+2?", "must say 4", 7, nil); err != nil {
		t.Fatalf("AddCase: %v", err)
	}

	svc := eval.NewService(store, loop, judgeLLM, nil)
	run, err := svc.RunSuite(ctx, suite.ID)
	if err != nil {
		t.Fatalf("RunSuite: %v", err)
	}
	if run.Status != eval.StatusCompleted {
		t.Fatalf("status: got %q, want %q", run.Status, eval.StatusCompleted)
	}
	if len(run.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(run.Results))
	}
	r := run.Results[0]
	if r.Error != "" {
		t.Fatalf("unexpected error: %s", r.Error)
	}
	if r.Response != "the answer is 4" {
		t.Errorf("Response: got %q", r.Response)
	}
	if !r.Passed || r.Score != 9 {
		t.Errorf("expected a passing judged score, got %+v", r)
	}
}
