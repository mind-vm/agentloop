package agentloop_test

import (
	"context"
	"strings"
	"testing"

	"github.com/jryannel/agentloop"
	"github.com/jryannel/agentloop/llm"
	"github.com/jryannel/agentloop/sandbox"
)

// Config.Policy must reach the sandbox (run.go installs it via
// sb.SetPolicy), so a denied primitive surfaces as an execution error fed
// back to the model; a nil policy fails open. The sandbox-level enforcement
// itself is covered in package sandbox; these tests pin the loop wiring.

type denyChecker struct{ reason string }

func (d denyChecker) Check(_ context.Context, _ string, _ map[string]any) (sandbox.PolicyCheckResult, error) {
	return sandbox.PolicyCheckResult{Allowed: false, Reason: d.reason}, nil
}

// policyBuilder builds a sandbox with a require-gated skill so a JS turn can
// trigger a PolicyChecker.Check.
type policyBuilder struct{}

func (policyBuilder) Build(ctx context.Context, _ agentloop.Session, _ agentloop.Scope, onEvent sandbox.OnEvent) (*sandbox.Sandbox, func(), error) {
	sb := sandbox.New(
		sandbox.SkillPack("blocked", "exports.greet = function() { return 'ran' }", "blocked skill"),
		sandbox.RequirePack(ctx),
	)
	if onEvent != nil {
		sb.SetOnEvent(onEvent)
	}
	return sb, func() {}, nil
}

func TestRun_PolicyDeniesPrimitive(t *testing.T) {
	sessions := newMemorySessions()
	sessions.put("s1", agentloop.Session{ID: "s1"})
	steps := newMemorySteps()

	loop := agentloop.New(agentloop.Config{
		LLM: newFakeLLM("fake",
			llm.CompletionResponse{Content: "```js\nlog(require(\"blocked\").greet())\n```"},
			llm.CompletionResponse{Content: "DONE\nthe policy stopped me"},
		),
		Sessions:       sessions,
		Steps:          steps,
		SandboxBuilder: policyBuilder{},
		Policy:         denyChecker{reason: "blocked by policy"},
	})

	var events []agentloop.RunEvent
	result, err := loop.Run(context.Background(), agentloop.RunRequest{
		SessionID: "s1", Message: "use the blocked skill",
		OnEvent: func(e agentloop.RunEvent) { events = append(events, e) },
	})
	if err != nil {
		t.Fatalf("a policy denial should surface to the model, not abort: %v", err)
	}
	if result.Status != "completed" {
		t.Fatalf("status: got %q, want %q", result.Status, "completed")
	}
	// The denial is fed back as the execution result (an error mentioning the
	// blocked skill), so the model can react.
	if !eventWithContent(events, "execute_js_result", "blocked") {
		t.Errorf("expected the policy denial surfaced in execute_js_result; events=%v", events)
	}
}

func TestRun_NilPolicyFailsOpen(t *testing.T) {
	sessions := newMemorySessions()
	sessions.put("s1", agentloop.Session{ID: "s1"})
	steps := newMemorySteps()

	loop := agentloop.New(agentloop.Config{
		LLM: newFakeLLM("fake",
			llm.CompletionResponse{Content: "```js\nlog(require(\"blocked\").greet())\n```"},
			llm.CompletionResponse{Content: "DONE\nok"},
		),
		Sessions:       sessions,
		Steps:          steps,
		SandboxBuilder: policyBuilder{},
		// Policy is unset → New installs sandbox.DefaultPolicy, which
		// allows require() (only sendEmail/secret/private-network fetch
		// are denied by default), so the skill call succeeds.
	})

	var events []agentloop.RunEvent
	result, err := loop.Run(context.Background(), agentloop.RunRequest{
		SessionID: "s1", Message: "use the skill",
		OnEvent: func(e agentloop.RunEvent) { events = append(events, e) },
	})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Status != "completed" {
		t.Fatalf("status: got %q, want %q", result.Status, "completed")
	}
	var jsResult string
	for _, e := range events {
		if e.Type == "execute_js_result" {
			jsResult = e.Content
		}
	}
	if strings.Contains(jsResult, "Error:") {
		t.Errorf("default policy should allow require(), but the op errored: %q", jsResult)
	}
	if !strings.Contains(jsResult, "ran") {
		t.Errorf("expected the skill output %q to contain 'ran'", jsResult)
	}
}
