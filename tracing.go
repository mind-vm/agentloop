package agentloop

import (
	"errors"

	"go.opentelemetry.io/otel/trace"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/jryannel/agentloop/redact"
)

// instrumentationName identifies this package as the span source, per
// OTel convention (Tracer's first argument).
const instrumentationName = "github.com/jryannel/agentloop"

// Span names. One per stage Run breaks into — see run.go for where each
// is opened. agentloop.run is the root; the rest are its children (plus
// agentloop.llm_call and agentloop.execute_js nest under agentloop.turn).
const (
	spanRun          = "agentloop.run"
	spanSandboxBuild = "agentloop.sandbox_build"
	spanTurn         = "agentloop.turn"
	spanLLMCall      = "agentloop.llm_call"
	spanExecuteJS    = "agentloop.execute_js"
)

// Attribute keys shared across the spans above.
const (
	attrSessionID   = "agentloop.session_id"
	attrStatus      = "agentloop.status"
	attrSteps       = "agentloop.steps"
	attrIteration   = "agentloop.iteration"
	attrModel       = "agentloop.model"
	attrPromptTok   = "agentloop.tokens.prompt"
	attrCompleteTok = "agentloop.tokens.completion"
)

// resolveTracer returns tp's "github.com/jryannel/agentloop" Tracer, or a
// no-op Tracer when tp is nil. Called once in New — this is the only
// place a nil TracerProvider is handled; everywhere else in this package
// just calls l.tracer.Start, exactly as it would against a real one.
//
// A no-op Tracer's spans cost a few allocations and do nothing else, so
// leaving Config.TracerProvider unset has no observable effect beyond
// that — this package's behavior never depends on whether tracing is
// wired up.
func resolveTracer(tp trace.TracerProvider) trace.Tracer {
	if tp == nil {
		tp = noop.NewTracerProvider()
	}
	return tp.Tracer(instrumentationName)
}

// redactErr applies r to err's message before it reaches a span. A
// span's recorded exception message is exactly the kind of free text
// redactEvent already covers for RunEvent — an LLM/HTTP error can echo
// a request detail (a header, a URL with a token in it) back verbatim.
// Returns err unchanged when r is nil or the message needed no
// changes, so the common case allocates nothing extra.
func redactErr(r *redact.Redactor, err error) error {
	if r == nil || err == nil {
		return err
	}
	msg := err.Error()
	redacted := r.Apply(msg)
	if redacted == msg {
		return err
	}
	return errors.New(redacted)
}
