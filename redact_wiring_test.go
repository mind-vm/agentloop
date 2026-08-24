package agentloop_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jryannel/agentloop"
	"github.com/jryannel/agentloop/llm"
	"github.com/jryannel/agentloop/redact"
	"github.com/jryannel/agentloop/sandbox"
)

const wiringSecret = "sk-live-super-secret"

// TestRun_RedactsRunEventContent confirms a secret value the model
// echoes back in its final answer never reaches OnEvent in the clear —
// the "response" RunEvent's Content, and the persisted step, must both
// carry the placeholder instead.
func TestRun_RedactsRunEventContent(t *testing.T) {
	fake := newFakeLLM("fake",
		llm.CompletionResponse{Content: "DONE\nhere is the key: " + wiringSecret},
	)
	sessions := newMemorySessions()
	sessions.put("s1", agentloop.Session{ID: "s1"})
	steps := newMemorySteps()

	loop := agentloop.New(agentloop.Config{
		LLM:            fake,
		Sessions:       sessions,
		Steps:          steps,
		SandboxBuilder: emptyBuilder{},
		Redactor:       redact.FromSecrets(map[string]string{"api_key": wiringSecret}),
	})

	var events []agentloop.RunEvent
	result, err := loop.Run(context.Background(), agentloop.RunRequest{
		SessionID: "s1",
		Message:   "hi",
		OnEvent:   func(e agentloop.RunEvent) { events = append(events, e) },
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if strings.Contains(result.FinalText, wiringSecret) {
		t.Fatalf("secret leaked into RunResult.FinalText: %q", result.FinalText)
	}
	if !strings.Contains(result.FinalText, "[redacted:secret.api_key]") {
		t.Fatalf("expected the placeholder in RunResult.FinalText: %q", result.FinalText)
	}

	found := false
	for _, e := range events {
		if e.Type == "response_chunk" {
			// Live streaming is suppressed when a Redactor is configured
			// — a secret can split across chunk boundaries, so it can't
			// be redacted safely per-chunk. See Config.Redactor's doc
			// comment.
			t.Fatalf("expected no response_chunk events with a Redactor configured, got one: %q", e.Content)
		}
		if strings.Contains(e.Content, wiringSecret) {
			t.Fatalf("secret leaked into a %q RunEvent's Content: %q", e.Type, e.Content)
		}
		if e.Type == "response" {
			found = true
			if !strings.Contains(e.Content, "[redacted:secret.api_key]") {
				t.Errorf("expected the placeholder in the response event: %q", e.Content)
			}
		}
	}
	if !found {
		t.Fatalf("no response event observed")
	}

	persisted, err := steps.LastN(context.Background(), "s1", 100)
	if err != nil {
		t.Fatalf("LastN: %v", err)
	}
	if len(persisted) == 0 {
		t.Fatalf("expected persisted steps")
	}
	for _, st := range persisted {
		if strings.Contains(st.Content, wiringSecret) {
			t.Fatalf("secret leaked into a persisted %q step: %q", st.StepType, st.Content)
		}
	}
}

// secretEventBuilder fires one sandbox_event carrying a secret in its
// Detail field as soon as the sandbox is built — standing in for a
// pack that logged a fetch response or similar into a sandbox.Event.
type secretEventBuilder struct{}

func (secretEventBuilder) Build(_ context.Context, _ agentloop.Session, _ agentloop.Scope, onEvent sandbox.OnEvent) (*sandbox.Sandbox, func(), error) {
	if onEvent != nil {
		onEvent(sandbox.Event{Kind: sandbox.EventFetch, Summary: "fetch", Detail: "Authorization: Bearer " + wiringSecret})
	}
	sb := sandbox.New()
	return sb, func() {}, nil
}

// TestRun_RedactsSandboxEventArgs confirms a secret surfaced through a
// sandbox.Event's Detail field (carried into RunEvent.Args, not
// Content) is redacted too — Args is redacted by marshal/ApplyBytes/
// unmarshal, a different code path from the plain Content string.
func TestRun_RedactsSandboxEventArgs(t *testing.T) {
	fake := newFakeLLM("fake", llm.CompletionResponse{Content: "DONE\nok"})
	sessions := newMemorySessions()
	sessions.put("s1", agentloop.Session{ID: "s1"})
	steps := newMemorySteps()

	loop := agentloop.New(agentloop.Config{
		LLM:            fake,
		Sessions:       sessions,
		Steps:          steps,
		SandboxBuilder: secretEventBuilder{},
		Redactor:       redact.FromSecrets(map[string]string{"api_key": wiringSecret}),
	})

	var events []agentloop.RunEvent
	if _, err := loop.Run(context.Background(), agentloop.RunRequest{
		SessionID: "s1",
		Message:   "hi",
		OnEvent:   func(e agentloop.RunEvent) { events = append(events, e) },
	}); err != nil {
		t.Fatalf("run: %v", err)
	}

	var sawSandboxEvent bool
	for _, e := range events {
		if e.Type != "sandbox_event" {
			continue
		}
		sawSandboxEvent = true
		detail, _ := e.Args["detail"].(string)
		if strings.Contains(detail, wiringSecret) {
			t.Fatalf("secret leaked into sandbox_event Args[detail]: %q", detail)
		}
		if !strings.Contains(detail, "[redacted:secret.api_key]") {
			t.Errorf("expected the placeholder in Args[detail]: %q", detail)
		}
	}
	if !sawSandboxEvent {
		t.Fatalf("no sandbox_event observed")
	}
}

// erroringSecretLLM always fails with an error whose message contains
// a secret value — standing in for an HTTP/LLM error that echoes a
// request detail (a header, a URL with a token) back verbatim.
type erroringSecretLLM struct{}

func (erroringSecretLLM) Name() string { return "erroring-secret" }
func (erroringSecretLLM) Complete(context.Context, llm.CompletionRequest) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{}, errors.New("upstream 401: token " + wiringSecret + " rejected")
}
func (erroringSecretLLM) Stream(context.Context, llm.CompletionRequest, func(string)) (llm.CompletionResponse, error) {
	return llm.CompletionResponse{}, errors.New("upstream 401: token " + wiringSecret + " rejected")
}

// TestRun_RedactsSpanErrorMessages confirms a secret embedded in an
// error message doesn't reach a span's recorded exception or status
// message — the tracing half of Config.Redactor, exercised against a
// real OTel SDK span recorder rather than asserting on unexported
// internals.
func TestRun_RedactsSpanErrorMessages(t *testing.T) {
	tp, sr := newRecordingTracerProvider()

	sessions := newMemorySessions()
	sessions.put("s1", agentloop.Session{ID: "s1"})
	steps := newMemorySteps()

	loop := agentloop.New(agentloop.Config{
		LLM:            erroringSecretLLM{},
		Sessions:       sessions,
		Steps:          steps,
		SandboxBuilder: emptyBuilder{},
		TracerProvider: tp,
		Redactor:       redact.FromSecrets(map[string]string{"token": wiringSecret}),
	})

	_, err := loop.Run(context.Background(), agentloop.RunRequest{SessionID: "s1", Message: "hi"})
	if err == nil {
		t.Fatalf("expected an error")
	}
	if strings.Contains(err.Error(), wiringSecret) {
		// Run's returned error is NOT redacted by design (redaction is
		// scoped to observability surfaces — spans/events — not the
		// Go error a caller programmatically inspects), so this isn't
		// itself a failure; it's here to document the boundary.
		t.Logf("Run's own returned error is unredacted by design: %v", err)
	}

	for _, s := range sr.Ended() {
		for _, ev := range s.Events() {
			for _, attr := range ev.Attributes {
				if strings.Contains(attr.Value.AsString(), wiringSecret) {
					t.Fatalf("secret leaked into span %q event attribute %s: %q", s.Name(), attr.Key, attr.Value.AsString())
				}
			}
		}
		if strings.Contains(s.Status().Description, wiringSecret) {
			t.Fatalf("secret leaked into span %q status description: %q", s.Name(), s.Status().Description)
		}
	}
}
