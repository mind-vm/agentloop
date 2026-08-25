package agentloop_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mind-vm/agentloop"
	"github.com/mind-vm/agentloop/llm"
	"github.com/mind-vm/agentloop/sandbox"
)

// This file pins the failure-path branches of run.go. Each has a named
// test so the documented best-effort-vs-fatal semantics (see the
// package doc / README) stay true across changes.

// ---- additional test doubles ----

// alwaysJSClient is an LLM that never emits DONE — every turn is a JS
// fence. Drives the MaxIterations branch.
type alwaysJSClient struct{}

func (alwaysJSClient) Name() string { return "always-js" }

func (alwaysJSClient) Complete(_ context.Context, _ llm.CompletionRequest) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{Content: "```js\n1\n```", InputTokens: 1, OutputTokens: 1}, nil
}

func (c alwaysJSClient) Stream(ctx context.Context, req llm.CompletionRequest, onChunk func(string)) (llm.CompletionResponse, error) {
	resp, err := c.Complete(ctx, req)
	if err == nil && onChunk != nil {
		onChunk(resp.Content)
	}
	return resp, err
}

// failBuilder fails to construct a sandbox. Drives the SandboxBuilder.Build
// failure branch.
type failBuilder struct{ err error }

func (b failBuilder) Build(_ context.Context, _ agentloop.Session, _ agentloop.Scope, _ sandbox.OnEvent) (*sandbox.Sandbox, func(), error) {
	return nil, nil, b.err
}

// eventWithContent reports whether any event of the given type carries
// content containing substr.
func eventWithContent(events []agentloop.RunEvent, typ, substr string) bool {
	for _, e := range events {
		if e.Type == typ && strings.Contains(e.Content, substr) {
			return true
		}
	}
	return false
}

// ---- construction: required deps panic at New (fail at boot, not request) ----

func TestNew_PanicsWhenRequiredDepMissing(t *testing.T) {
	full := func() agentloop.Config {
		return agentloop.Config{
			LLM:            newFakeLLM("fake"),
			Sessions:       newMemorySessions(),
			Steps:          newMemorySteps(),
			SandboxBuilder: emptyBuilder{},
		}
	}
	cases := map[string]func(*agentloop.Config){
		"missing LLM":            func(c *agentloop.Config) { c.LLM = nil },
		"missing Sessions":       func(c *agentloop.Config) { c.Sessions = nil },
		"missing Steps":          func(c *agentloop.Config) { c.Steps = nil },
		"missing SandboxBuilder": func(c *agentloop.Config) { c.SandboxBuilder = nil },
	}
	for name, drop := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := full()
			drop(&cfg)
			defer func() {
				if recover() == nil {
					t.Errorf("New should panic when a required dep is nil")
				}
			}()
			agentloop.New(cfg)
		})
	}
}

// ---- branch: MaxIterations exhausted without DONE ----

func TestRun_MaxIterations(t *testing.T) {
	sessions := newMemorySessions()
	sessions.put("s1", agentloop.Session{ID: "s1"})
	steps := newMemorySteps()

	loop := agentloop.New(agentloop.Config{
		LLM:            alwaysJSClient{},
		Sessions:       sessions,
		Steps:          steps,
		SandboxBuilder: emptyBuilder{},
	})

	var events []agentloop.RunEvent
	result, err := loop.Run(context.Background(), agentloop.RunRequest{
		SessionID: "s1", Message: "loop forever",
		OnEvent: func(e agentloop.RunEvent) { events = append(events, e) },
	})
	if err == nil {
		t.Fatalf("expected an error when max iterations is hit")
	}
	if result.Status != "max_iterations" {
		t.Fatalf("status: got %q, want %q", result.Status, "max_iterations")
	}
	if !contains(steps.types("s1"), "error") {
		t.Errorf("expected a terminal error step, got: %v", steps.types("s1"))
	}
	if !eventWithContent(events, "error", "max iterations") {
		t.Errorf("expected a max-iterations error event")
	}
	if fin := sessions.finalised("s1"); fin == nil || fin.Status != "max_iterations" {
		t.Errorf("session should be finalised with max_iterations, got %+v", fin)
	}
}

// ---- branch: context cancelled before/between turns ----

func TestRun_ContextCancelled(t *testing.T) {
	sessions := newMemorySessions()
	sessions.put("s1", agentloop.Session{ID: "s1"})
	steps := newMemorySteps()

	loop := agentloop.New(agentloop.Config{
		LLM:            newFakeLLM("fake", llm.CompletionResponse{Content: "DONE\nunreached"}),
		Sessions:       sessions,
		Steps:          steps,
		SandboxBuilder: emptyBuilder{},
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancelled before Run — the loop's top-of-iteration guard catches it.

	result, err := loop.Run(ctx, agentloop.RunRequest{SessionID: "s1", Message: "hi"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
	if result.Status != "error" {
		t.Fatalf("status: got %q, want %q", result.Status, "error")
	}
	if fin := sessions.finalised("s1"); fin == nil || fin.Status != "error" {
		t.Errorf("session should be finalised error, got %+v", fin)
	}
}

// ---- branch: Sessions.Get fails → early return, fatal ----

func TestRun_SessionGetFails(t *testing.T) {
	sessions := newMemorySessions()
	sessions.getErr = errors.New("db down")
	steps := newMemorySteps()

	loop := agentloop.New(agentloop.Config{
		LLM:            newFakeLLM("fake", llm.CompletionResponse{Content: "DONE\nx"}),
		Sessions:       sessions,
		Steps:          steps,
		SandboxBuilder: emptyBuilder{},
	})

	_, err := loop.Run(context.Background(), agentloop.RunRequest{SessionID: "s1", Message: "hi"})
	if err == nil || !strings.Contains(err.Error(), "load session") {
		t.Fatalf("expected a load-session error, got %v", err)
	}
	if len(steps.types("s1")) != 0 {
		t.Errorf("no steps should be written when session load fails, got %v", steps.types("s1"))
	}
	if sessions.finalised("s1") != nil {
		t.Errorf("session should not be finalised on early load failure")
	}
}

// ---- branch: Steps.LastN fails → best-effort, run continues ----

func TestRun_HistoryReadFails_ContinuesWithEmptyHistory(t *testing.T) {
	sessions := newMemorySessions()
	sessions.put("s1", agentloop.Session{ID: "s1"})
	steps := newMemorySteps()
	steps.lastNErr = errors.New("flaky read")

	loop := agentloop.New(agentloop.Config{
		LLM:            newFakeLLM("fake", llm.CompletionResponse{Content: "DONE\nstill answered"}),
		Sessions:       sessions,
		Steps:          steps,
		SandboxBuilder: emptyBuilder{},
	})

	var events []agentloop.RunEvent
	result, err := loop.Run(context.Background(), agentloop.RunRequest{
		SessionID: "s1", Message: "hi",
		OnEvent: func(e agentloop.RunEvent) { events = append(events, e) },
	})
	if err != nil {
		t.Fatalf("history read failure must NOT abort the run, got %v", err)
	}
	if result.Status != "completed" {
		t.Fatalf("status: got %q, want %q", result.Status, "completed")
	}
	if !eventWithContent(events, "warning", "history rehydrate failed") {
		t.Errorf("expected a history-rehydrate warning event")
	}
}

// ---- branch: SandboxBuilder.Build fails → finalize(error), fatal ----

func TestRun_SandboxBuildFails(t *testing.T) {
	sessions := newMemorySessions()
	sessions.put("s1", agentloop.Session{ID: "s1"})
	steps := newMemorySteps()

	loop := agentloop.New(agentloop.Config{
		LLM:            newFakeLLM("fake", llm.CompletionResponse{Content: "DONE\nx"}),
		Sessions:       sessions,
		Steps:          steps,
		SandboxBuilder: failBuilder{err: errors.New("pack registry empty")},
	})

	result, err := loop.Run(context.Background(), agentloop.RunRequest{SessionID: "s1", Message: "hi"})
	if err == nil || !strings.Contains(err.Error(), "build sandbox") {
		t.Fatalf("expected a build-sandbox error, got %v", err)
	}
	if result.Status != "error" {
		t.Fatalf("status: got %q, want %q", result.Status, "error")
	}
	if fin := sessions.finalised("s1"); fin == nil || fin.Status != "error" {
		t.Errorf("session should be finalised error after build failure, got %+v", fin)
	}
}

// ---- branch: Steps.Append fails → best-effort warning, continues ----

func TestRun_StepAppendFails_WarnsAndContinues(t *testing.T) {
	sessions := newMemorySessions()
	sessions.put("s1", agentloop.Session{ID: "s1"})
	steps := newMemorySteps()
	steps.appendErr = errors.New("write failed")

	loop := agentloop.New(agentloop.Config{
		LLM:            newFakeLLM("fake", llm.CompletionResponse{Content: "DONE\nanswer"}),
		Sessions:       sessions,
		Steps:          steps,
		SandboxBuilder: emptyBuilder{},
	})

	var events []agentloop.RunEvent
	result, err := loop.Run(context.Background(), agentloop.RunRequest{
		SessionID: "s1", Message: "hi",
		OnEvent: func(e agentloop.RunEvent) { events = append(events, e) },
	})
	if err != nil {
		t.Fatalf("step persistence failure must NOT abort, got %v", err)
	}
	if result.Status != "completed" {
		t.Fatalf("status: got %q, want %q", result.Status, "completed")
	}
	if !eventWithContent(events, "warning", "persist step failed") {
		t.Errorf("expected a persist-step warning event")
	}
}

// ---- branch: Sessions.Finalize fails → warning, result still returned ----

func TestRun_FinalizeFails_WarnsAndReturnsResult(t *testing.T) {
	sessions := newMemorySessions()
	sessions.put("s1", agentloop.Session{ID: "s1"})
	sessions.finalizeErr = errors.New("finalize write failed")
	steps := newMemorySteps()

	loop := agentloop.New(agentloop.Config{
		LLM:            newFakeLLM("fake", llm.CompletionResponse{Content: "DONE\nthe answer"}),
		Sessions:       sessions,
		Steps:          steps,
		SandboxBuilder: emptyBuilder{},
	})

	var events []agentloop.RunEvent
	result, err := loop.Run(context.Background(), agentloop.RunRequest{
		SessionID: "s1", Message: "hi",
		OnEvent: func(e agentloop.RunEvent) { events = append(events, e) },
	})
	if err != nil {
		t.Fatalf("finalize failure must NOT abort, got %v", err)
	}
	if result.Status != "completed" || result.FinalText != "the answer" {
		t.Fatalf("result should still be returned: status=%q text=%q", result.Status, result.FinalText)
	}
	if !eventWithContent(events, "warning", "finalize failed") {
		t.Errorf("expected a finalize-failed warning event")
	}
}

// ---- branch: Sessions.UpdateData fails → warning, loop continues ----

func TestRun_UpdateDataFails_WarnsAndContinues(t *testing.T) {
	sessions := newMemorySessions()
	sessions.put("s1", agentloop.Session{ID: "s1"})
	sessions.updateDataErr = errors.New("snapshot write failed")
	steps := newMemorySteps()

	loop := agentloop.New(agentloop.Config{
		LLM: newFakeLLM("fake",
			llm.CompletionResponse{Content: "```js\nfunction run(args) { return { x: 1 }; }\n```"},
			llm.CompletionResponse{Content: "DONE\ndone"},
		),
		Sessions:       sessions,
		Steps:          steps,
		SandboxBuilder: dataBuilder{},
	})

	var events []agentloop.RunEvent
	result, err := loop.Run(context.Background(), agentloop.RunRequest{
		SessionID: "s1", Message: "set x",
		OnEvent: func(e agentloop.RunEvent) { events = append(events, e) },
	})
	if err != nil {
		t.Fatalf("data snapshot failure must NOT abort, got %v", err)
	}
	if result.Status != "completed" {
		t.Fatalf("status: got %q, want %q", result.Status, "completed")
	}
	if !eventWithContent(events, "warning", "data snapshot persist failed") {
		t.Errorf("expected a data-snapshot warning event")
	}
}

// ---- branch: JS execution error fed back into history, loop continues ----

func TestRun_JSExecError_FedBackAndContinues(t *testing.T) {
	sessions := newMemorySessions()
	sessions.put("s1", agentloop.Session{ID: "s1"})
	steps := newMemorySteps()

	loop := agentloop.New(agentloop.Config{
		LLM: newFakeLLM("fake",
			llm.CompletionResponse{Content: "```js\nthrow new Error(\"boom\")\n```"},
			llm.CompletionResponse{Content: "DONE\nrecovered"},
		),
		Sessions:       sessions,
		Steps:          steps,
		SandboxBuilder: emptyBuilder{},
	})

	var events []agentloop.RunEvent
	result, err := loop.Run(context.Background(), agentloop.RunRequest{
		SessionID: "s1", Message: "do a bad thing",
		OnEvent: func(e agentloop.RunEvent) { events = append(events, e) },
	})
	if err != nil {
		t.Fatalf("a thrown JS error must NOT kill the run, got %v", err)
	}
	if result.Status != "completed" || result.FinalText != "recovered" {
		t.Fatalf("run should recover and complete: status=%q text=%q", result.Status, result.FinalText)
	}
	// The error surfaces as the execute_js_result content, fed back to the model.
	if !eventWithContent(events, "execute_js_result", "Error:") {
		t.Errorf("expected the JS error surfaced in execute_js_result")
	}
	want := []string{"user", "execute_js", "execute_js_result", "response"}
	if got := steps.types("s1"); !equalSlice(got, want) {
		t.Errorf("step types: got %v, want %v", got, want)
	}
}

// ---- branch: empty LLM completions are retried, then a typed error ----

func TestRun_EmptyResponse_RetriesThenSucceeds(t *testing.T) {
	sessions := newMemorySessions()
	sessions.put("s1", agentloop.Session{ID: "s1"})
	steps := newMemorySteps()

	loop := agentloop.New(agentloop.Config{
		LLM: newFakeLLM("fake",
			llm.CompletionResponse{Content: "  \n  "}, // whitespace-only counts as empty
			llm.CompletionResponse{Content: ""},
			llm.CompletionResponse{Content: "DONE\nfinally"},
		),
		Sessions:       sessions,
		Steps:          steps,
		SandboxBuilder: emptyBuilder{},
	})

	var events []agentloop.RunEvent
	result, err := loop.Run(context.Background(), agentloop.RunRequest{
		SessionID: "s1", Message: "hi",
		OnEvent: func(e agentloop.RunEvent) { events = append(events, e) },
	})
	if err != nil {
		t.Fatalf("run should recover from transient empty completions: %v", err)
	}
	if result.Status != "completed" || result.FinalText != "finally" {
		t.Fatalf("status=%q final=%q", result.Status, result.FinalText)
	}
	if !eventWithContent(events, "warning", "empty model turn") {
		t.Errorf("expected a warning event for each retried empty turn")
	}
}

func TestRun_EmptyResponse_ExhaustsRetriesThenErrors(t *testing.T) {
	sessions := newMemorySessions()
	sessions.put("s1", agentloop.Session{ID: "s1"})
	steps := newMemorySteps()

	loop := agentloop.New(agentloop.Config{
		LLM: newFakeLLM("fake",
			llm.CompletionResponse{Content: ""},
			llm.CompletionResponse{Content: ""},
			llm.CompletionResponse{Content: ""},
		),
		Sessions:       sessions,
		Steps:          steps,
		SandboxBuilder: emptyBuilder{},
	})

	result, err := loop.Run(context.Background(), agentloop.RunRequest{SessionID: "s1", Message: "hi"})
	if !errors.Is(err, agentloop.ErrEmptyResponse) {
		t.Fatalf("expected ErrEmptyResponse, got %v", err)
	}
	if result.Status != "error" {
		t.Fatalf("status: got %q, want %q", result.Status, "error")
	}
	if !contains(steps.types("s1"), "error") {
		t.Errorf("expected a terminal error step, got: %v", steps.types("s1"))
	}
}

// ---- branch: token aggregation sums across turns ----

func TestRun_TokenAggregation_SumsAcrossTurns(t *testing.T) {
	sessions := newMemorySessions()
	sessions.put("s1", agentloop.Session{ID: "s1"})
	steps := newMemorySteps()

	loop := agentloop.New(agentloop.Config{
		LLM: newFakeLLM("fake",
			llm.CompletionResponse{Content: "```js\n1\n```", InputTokens: 4, OutputTokens: 2},
			llm.CompletionResponse{Content: "DONE\nfinished", InputTokens: 7, OutputTokens: 1},
		),
		Sessions:       sessions,
		Steps:          steps,
		SandboxBuilder: emptyBuilder{},
	})

	result, err := loop.Run(context.Background(), agentloop.RunRequest{SessionID: "s1", Message: "go"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if got, want := result.Tokens.Prompt, int32(11); got != want {
		t.Errorf("summed prompt tokens: got %d, want %d", got, want)
	}
	if got, want := result.Tokens.Completion, int32(3); got != want {
		t.Errorf("summed completion tokens: got %d, want %d", got, want)
	}
}
