// Package agentloop is a small reasoning-loop engine for LLM agents that
// think by writing JavaScript instead of calling named tools.
//
// Each turn the model emits one fenced ```javascript block defining
// `function run(args) { ... }`. The loop executes it in a sandboxed goja
// runtime (package sandbox) and threads its return value into the next
// turn as `args` — full fidelity, server-side, never serialised back into
// the prompt. The model sees only its own log() output plus a compact
// structural digest of what it returned. A run finishes when the script
// calls answer(result).
//
// This design — return-threading instead of a growing tool-call
// transcript — keeps the prompt small even for many-step runs: stale
// code is elided from history (only the most recent turn's script is
// kept verbatim) and large data never appears in the context window
// twice.
//
// A minimal wiring looks like:
//
//	client, _ := llm.NewOpenAI(llm.ConfigFromEnv())
//	caps := agentloop.DefaultCapabilities(client, "")
//	loop := agentloop.New(agentloop.Config{
//	    LLM:            client,
//	    Sessions:       mySessionStore,
//	    Steps:          myStepStore,
//	    SandboxBuilder: &agentloop.DefaultSandboxBuilder{Capabilities: caps},
//	})
//	result, err := loop.Run(ctx, agentloop.RunRequest{
//	    SessionID: "session-1",
//	    Message:   "What's 2+2, and say it back as markdown?",
//	})
//
// See examples/cli for a complete runnable program with in-memory stores.
package agentloop

import "context"

// Scope is the tenant boundary a run executes under. Every capability's
// Build receives it and should scope its reads/writes accordingly.
// ProjectID is optional — leave it empty when your application has no
// sub-workspace narrowing.
type Scope struct {
	WorkspaceID string
	ProjectID   string // empty = not narrowed to one project
}

// Loop is the reasoning-loop contract. One call to Run drives a
// multi-turn rehydrate-execute-respond cycle for one user message,
// finishing when the model calls answer() (or emits a legacy DONE
// marker, or answers with no code fence at all).
type Loop interface {
	Run(ctx context.Context, req RunRequest) (RunResult, error)
}

// RunRequest is the input to Loop.Run.
type RunRequest struct {
	// SessionID identifies the agent session this run extends. The loop
	// loads prior steps from StepStore using this ID; new steps are
	// appended under the same ID.
	SessionID string

	// Scope is the tenant boundary the run executes under. Passed
	// through to the PolicyChecker and each Capability's Build.
	Scope Scope

	// UserID is the invoking user, empty for system-initiated runs.
	UserID string

	// Message is the user turn that triggered the run. The loop appends
	// it to history before the first LLM call.
	Message string

	// Context is optional per-run context (e.g. a webhook payload,
	// prefetched) folded into the system prompt for this run only. Not
	// persisted as a step — the user turn in the trace stays the raw
	// Message.
	Context string

	// OnEvent receives every observability emission as it happens. Nil
	// is acceptable — events still land in the step trace.
	OnEvent func(RunEvent)
}

// RunResult is the summary populated when Loop.Run returns.
type RunResult struct {
	// RunID is the session ID this run extended (mirrors RunRequest.SessionID).
	RunID string

	// FinalText is the agent's last `response` step content. Empty when
	// Status != "completed".
	FinalText string

	// Steps is the number of steps persisted by this Run call.
	Steps int

	// Status is one of "completed" | "error" | "max_iterations".
	Status string

	// Tokens is the aggregate prompt + completion token usage across
	// every LLM call this Run made.
	Tokens TokenUsage

	// SystemPrompt is the fully composed system prompt sent to the
	// model, for an inspectable turn trace. Empty if the run failed
	// before composing it.
	SystemPrompt string

	// DataBytesCarried is the context-economy measurement: bytes of
	// threaded working state withheld from prompts, summed per LLM
	// call, net of the shape digests sent.
	DataBytesCarried int64
}

// TokenUsage is the per-Run aggregate.
type TokenUsage struct {
	Prompt     int32
	Completion int32
}
