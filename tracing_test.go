package agentloop_test

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/mind-vm/agentloop"
	"github.com/mind-vm/agentloop/llm"
)

// newRecordingTracerProvider builds a real (non-noop) TracerProvider
// backed by an in-memory SpanRecorder — the same SDK a Jaeger/OTLP
// exporter would sit behind in production, minus the network. The
// recorder is a SpanProcessor, so OnEnd fires synchronously as each
// span closes; sr.Ended() is complete the instant Run returns.
func newRecordingTracerProvider() (trace.TracerProvider, *tracetest.SpanRecorder) {
	sr := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(sr))
	return tp, sr
}

func spanNames(spans []sdktrace.ReadOnlySpan) []string {
	names := make([]string, len(spans))
	for i, s := range spans {
		names[i] = s.Name()
	}
	return names
}

func findSpan(spans []sdktrace.ReadOnlySpan, name string) sdktrace.ReadOnlySpan {
	for _, s := range spans {
		if s.Name() == name {
			return s
		}
	}
	return nil
}

// TestRun_EmitsSpanTreeOnSuccess drives the two-turn JS-then-DONE path
// with a real TracerProvider wired in and checks the resulting span
// tree: one root agentloop.run, one agentloop.sandbox_build, and one
// agentloop.turn per LLM round-trip, each turn containing its own
// agentloop.llm_call and (turn 1 only) agentloop.execute_js.
func TestRun_EmitsSpanTreeOnSuccess(t *testing.T) {
	tp, sr := newRecordingTracerProvider()

	fake := newFakeLLM("fake",
		llm.CompletionResponse{Content: "```javascript\nfunction run(args) { return 1; }\n```", Model: "m", InputTokens: 10, OutputTokens: 4},
		llm.CompletionResponse{Content: "DONE\nfinal", Model: "m", InputTokens: 8, OutputTokens: 2},
	)
	sessions := newMemorySessions()
	sessions.put("s1", agentloop.Session{ID: "s1"})
	steps := newMemorySteps()

	loop := agentloop.New(agentloop.Config{
		LLM:            fake,
		Sessions:       sessions,
		Steps:          steps,
		SandboxBuilder: emptyBuilder{},
		TracerProvider: tp,
	})

	result, err := loop.Run(context.Background(), agentloop.RunRequest{SessionID: "s1", Message: "hi"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != "completed" {
		t.Fatalf("status: got %q, want completed", result.Status)
	}

	spans := sr.Ended()
	names := spanNames(spans)

	runSpan := findSpan(spans, "agentloop.run")
	if runSpan == nil {
		t.Fatalf("no agentloop.run span among %v", names)
	}
	// Unset, not Ok: per OTel convention, instrumentation records Error
	// on failure and otherwise leaves status alone — Ok is reserved for
	// the application to assert about a span, not for a library to set
	// on every success.
	if runSpan.Status().Code != codes.Unset {
		t.Errorf("agentloop.run status: got %v, want Unset", runSpan.Status().Code)
	}
	var gotSessionID string
	for _, a := range runSpan.Attributes() {
		if string(a.Key) == "agentloop.session_id" {
			gotSessionID = a.Value.AsString()
		}
	}
	if gotSessionID != "s1" {
		t.Errorf("agentloop.run session_id attribute: got %q, want s1", gotSessionID)
	}

	buildSpan := findSpan(spans, "agentloop.sandbox_build")
	if buildSpan == nil {
		t.Fatalf("no agentloop.sandbox_build span among %v", names)
	}
	if buildSpan.Parent().SpanID() != runSpan.SpanContext().SpanID() {
		t.Errorf("agentloop.sandbox_build should be a child of agentloop.run")
	}

	turnCount, llmCallCount, execJSCount := 0, 0, 0
	for _, s := range spans {
		switch s.Name() {
		case "agentloop.turn":
			turnCount++
			if s.Parent().SpanID() != runSpan.SpanContext().SpanID() {
				t.Errorf("agentloop.turn should be a child of agentloop.run")
			}
		case "agentloop.llm_call":
			llmCallCount++
		case "agentloop.execute_js":
			execJSCount++
		}
	}
	if turnCount != 2 {
		t.Errorf("agentloop.turn count: got %d, want 2 (one per LLM round-trip)", turnCount)
	}
	if llmCallCount != 2 {
		t.Errorf("agentloop.llm_call count: got %d, want 2", llmCallCount)
	}
	if execJSCount != 1 {
		t.Errorf("agentloop.execute_js count: got %d, want 1 (only turn 1 emits JS)", execJSCount)
	}

	// Every llm_call and execute_js span must nest under some turn span,
	// not directly under the root — confirms the parent chain is
	// run -> turn -> {llm_call, execute_js}, not run -> {turn, llm_call, execute_js} flat.
	turnSpanIDs := map[trace.SpanID]bool{}
	for _, s := range spans {
		if s.Name() == "agentloop.turn" {
			turnSpanIDs[s.SpanContext().SpanID()] = true
		}
	}
	for _, s := range spans {
		if s.Name() == "agentloop.llm_call" || s.Name() == "agentloop.execute_js" {
			if !turnSpanIDs[s.Parent().SpanID()] {
				t.Errorf("%s span's parent is not a agentloop.turn span", s.Name())
			}
		}
	}
}

// TestRun_RecordsErrorOnLLMFailure confirms a failing LLM call marks
// both its own agentloop.llm_call span and the enclosing agentloop.run
// span as errored, with the error recorded on each.
func TestRun_RecordsErrorOnLLMFailure(t *testing.T) {
	tp, sr := newRecordingTracerProvider()

	fake := &erroringLLM{err: errors.New("gateway unavailable")}
	sessions := newMemorySessions()
	sessions.put("s1", agentloop.Session{ID: "s1"})
	steps := newMemorySteps()

	loop := agentloop.New(agentloop.Config{
		LLM:            fake,
		Sessions:       sessions,
		Steps:          steps,
		SandboxBuilder: emptyBuilder{},
		TracerProvider: tp,
	})

	_, err := loop.Run(context.Background(), agentloop.RunRequest{SessionID: "s1", Message: "hi"})
	if err == nil {
		t.Fatalf("expected an error")
	}

	spans := sr.Ended()
	runSpan := findSpan(spans, "agentloop.run")
	if runSpan == nil {
		t.Fatalf("no agentloop.run span")
	}
	if runSpan.Status().Code != codes.Error {
		t.Errorf("agentloop.run status: got %v, want Error", runSpan.Status().Code)
	}

	llmSpan := findSpan(spans, "agentloop.llm_call")
	if llmSpan == nil {
		t.Fatalf("no agentloop.llm_call span")
	}
	if llmSpan.Status().Code != codes.Error {
		t.Errorf("agentloop.llm_call status: got %v, want Error", llmSpan.Status().Code)
	}
	if len(llmSpan.Events()) == 0 {
		t.Errorf("expected llm_call span to record the error as an event")
	}
}

// TestRun_NoTracerProviderIsFineToOmit confirms Run still works — same
// as every other test in this package — when Config.TracerProvider is
// left unset, i.e. the no-op default from resolveTracer.
func TestRun_NoTracerProviderIsFineToOmit(t *testing.T) {
	fake := newFakeLLM("fake", llm.CompletionResponse{Content: "DONE\nok", Model: "m"})
	sessions := newMemorySessions()
	sessions.put("s1", agentloop.Session{ID: "s1"})
	steps := newMemorySteps()

	loop := agentloop.New(agentloop.Config{
		LLM:            fake,
		Sessions:       sessions,
		Steps:          steps,
		SandboxBuilder: emptyBuilder{},
	})

	result, err := loop.Run(context.Background(), agentloop.RunRequest{SessionID: "s1", Message: "hi"})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != "completed" {
		t.Fatalf("status: got %q, want completed", result.Status)
	}
}

// erroringLLM is an llm.Client whose Stream always fails.
type erroringLLM struct{ err error }

func (e *erroringLLM) Name() string { return "erroring" }
func (e *erroringLLM) Complete(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{}, e.err
}
func (e *erroringLLM) Stream(context.Context, llm.CompletionRequest, func(string)) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{}, e.err
}
