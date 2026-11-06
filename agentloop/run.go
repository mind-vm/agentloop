package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/mind-vm/thinking-core/llm"
	"github.com/mind-vm/thinking-core/redact"
	"github.com/mind-vm/thinking-core/sandbox"
)

// MaxIterations is the default cap on LLM round-trips a single Run will
// make (override via Config.MaxIterations).
const MaxIterations = 20

// maxEmptyRetries bounds how many times a turn that produced NO content
// at all (e.g. a provider returning a 0-token completion on a large
// prompt) is re-issued before the run gives up with ErrEmptyResponse.
// Such empties are usually transient; retrying is cheap and self-heals
// them. Counted against the same Run's MaxIterations budget — no
// separate cap to configure.
const maxEmptyRetries = 2

// ErrEmptyResponse is returned when the model yields no content across
// the allowed retries. Distinct from a normal completion so callers can
// treat it as a retryable failure instead of silently finishing with an
// empty answer.
var ErrEmptyResponse = errors.New("agentloop: model returned an empty response")

// ErrNoScript is returned when a RequireScript loop's corrections run out
// and the model still has not written a script. Nothing ran, so the reply
// is not an answer and must not be reported as a completed run.
var ErrNoScript = errors.New("agentloop: model replied without a script after every correction")

// RunTimeout is the default wall-clock cap on a single Run call
// (override via Config.RunTimeout). It bounds a whole run, every turn of
// it; a single stalled call is the llm client's to catch (see
// llm.Config.IdleTimeout). labs/codeloop found five minutes cutting off
// runs that were working — a thinking model on a host producing 20
// tokens a second spends minutes on one turn — and passed 15 on every
// benchmark run after.
const RunTimeout = 15 * time.Minute

// ErrRunTimeout is returned when a Run reaches Config.RunTimeout. It is
// distinct from the caller's own cancellation, and from a model call
// that failed, so a run cut off by the cap is not read as a stall.
var ErrRunTimeout = errors.New("agentloop: run timeout reached")

// Config wires the dependencies the loop needs.
type Config struct {
	// LLM is the per-Run chat client.
	LLM llm.Client

	// Sessions persists session metadata. Required.
	Sessions SessionStore

	// Steps persists the per-turn trace. Required.
	Steps StepStore

	// SandboxBuilder constructs the sandbox for a Run. Required.
	SandboxBuilder SandboxBuilder

	// Policy gates side-effecting primitives. Optional; nil installs
	// sandbox.DefaultPolicy (conservative: deny by default).
	Policy sandbox.PolicyChecker

	// Model is the default chat model when the session has none pinned.
	// Optional; falls back to the LLM client's own default when empty.
	Model string

	// MaxIterations caps LLM round-trips per Run. Zero uses the package
	// default.
	MaxIterations int

	// RunTimeout is the wall-clock cap per Run. Zero uses the package
	// default.
	RunTimeout time.Duration

	// HistoryWindow caps how many prior steps are rehydrated into the
	// LLM context. Zero uses the package default.
	//
	// With a Compactor configured this becomes how much of the past the
	// loop CONSIDERS rather than how much it sends: everything loaded
	// is handed to the Compactor, which decides what survives verbatim.
	// Raising it is how a session gets long-term memory — the prompt
	// stays bounded by the compactor, not by this.
	HistoryWindow int

	// Compactor folds the older part of a long session's history into a
	// summary before the run's turns begin, so early context survives
	// in compressed form instead of falling off the end of
	// HistoryWindow unnoticed.
	//
	// A compaction that reports a CompactResult.Summary is persisted as
	// a StepTypeSummary checkpoint, and later runs rehydrate from that
	// rather than summarizing the same turns again — see
	// rehydrateHistory. The covered steps remain in the trace; only
	// what is replayed to the model changes.
	//
	// Optional; nil keeps the default behaviour — history is truncated
	// to HistoryWindow and the overflow is simply gone. Compaction is
	// best-effort: a Compactor that fails warns and the run proceeds on
	// the uncompacted history.
	Compactor Compactor

	// ContextBudget bounds each turn's prompt in TOKENS, as a hard
	// backstop under HistoryWindow and Compactor — both of which count
	// MESSAGES and so cannot tell a forty-token turn from a
	// four-thousand-token one. Optional; the zero value is disabled and
	// preserves the prior behaviour exactly.
	//
	// Enabling it is what turns a context overflow from a provider
	// error (or a silent server-side truncation) into a deliberate,
	// observable trim: the oldest turns are dropped, the model is told
	// how many, and a "budget_trimmed" event reports it. Set
	// MaxTokens to the window the model is actually SERVED with — for a
	// local runtime that is the server's configured context size, not
	// the figure on the model card.
	ContextBudget ContextBudget

	// RetryHint, when set, is asked first for the hint that follows a
	// script's error: the host knows its own API (its names, how they are
	// called) and the generic hints do not. An empty answer falls back to
	// them.
	RetryHint func(errMsg string) string

	// FinalError, when set, names the script errors that are an answer
	// rather than a mistake: a refusal the host made on purpose (the user
	// declined, no one is there to approve). For those the model is given
	// the error alone, with no retry hint and no "fix and retry" footer, so
	// the host's "do not retry" is not contradicted. Nil treats every
	// error as one to fix.
	FinalError func(errMsg string) bool

	// Now is a clock seam for tests. Nil uses time.Now.
	Now func() time.Time

	// TracerProvider produces spans for each Run — one root span per
	// call plus child spans for sandbox build, each turn, its LLM call,
	// and its JS execution. Optional; nil installs a no-op tracer, so
	// leaving this unset costs a few allocations and produces no spans.
	// Wire in an OTel SDK TracerProvider (e.g. configured with an OTLP
	// exporter pointed at a Jaeger collector) to observe Run calls in
	// production — nothing else in this package needs to change.
	TracerProvider trace.TracerProvider

	// Redactor strips known secret values out of every surface this
	// package writes free text to: RunEvent (Content and Args, before
	// OnEvent sees it), RunResult.FinalText, persisted RunStep.Content
	// (via Steps.Append), the execution result the model reads next turn,
	// and error messages recorded on a span.
	// Optional; nil is a safe no-op (see redact.Redactor) — the
	// defense-in-depth case this exists for is a script that logs a
	// fetched credential (log(secret("KEY")), or a fetch() response
	// that echoes one back) and would otherwise carry it into
	// whatever OnEvent forwards to, the persisted trace, or a trace
	// backend. Build one with redact.FromSecrets over the secret
	// values your capabilities can return this session.
	//
	// One trade-off: setting this suppresses "response_chunk" events
	// (live token-by-token streaming). A secret can split across two
	// chunk boundaries with neither chunk containing the whole value to
	// match against, so per-chunk redaction can't be made safe — the
	// complete, redacted text still arrives via the terminal "response"
	// event instead.
	Redactor *redact.Redactor

	// RequireScript sends a reply with neither a code fence nor a DONE
	// marker back to the model as a correction instead of accepting it
	// as the answer, up to maxProseRetries times, then fails the run with
	// ErrNoScript. An agent whose work is in its workspace otherwise
	// "finishes" by describing what it would do. Optional; false keeps
	// prose-as-final.
	RequireScript bool

	// Protocol replaces TextEmissionSystemPrompt at the head of the
	// composed system prompt, for an agent that states the reply contract
	// in its own words. The sandbox API is still appended after it.
	// Optional; empty keeps the default.
	Protocol string

	// UserOutput gives scripts a channel to the user and a way to finish:
	// print(...) writes the task's result, console.log stays the model's own
	// view, and done(message?) or exit(message?) ends the run after the
	// script, answering with everything printed plus the message. A done()
	// with nothing to say is refused with a notice. Optional; false keeps
	// print as an alias of log.
	UserOutput bool

	// BudgetNotice tells the model, in the result of the script that leaves
	// two calls, that the run is about to be cut off, so work that is done
	// gets reported instead of lost to the cap. Optional.
	BudgetNotice bool
}

// maxProseRetries bounds how many times a RequireScript loop corrects a
// reply that carried no script before failing the run.
const maxProseRetries = 2

// finishInstruction is how a correction tells the model to end the task:
// the documented way for this loop, so a correction never teaches a
// primitive the prompt no longer mentions.
func finishInstruction(cfg Config) string {
	switch {
	case cfg.UserOutput:
		return "print(...) what the user should see and call exit()"
	}
	return "call answer(result) when you have the result"
}

// proseCorrection is the user turn a RequireScript loop sends back after
// a reply with no script. It names what was wrong and what to do instead.
func proseCorrection(finish string) string {
	return "Your reply contained no ```javascript block, so nothing ran and nothing was looked at. " +
		"Do not describe what you would do and do not ask for the files: the workspace is already reachable through the Sandbox API. " +
		"Reply with one ```javascript block, and " + finish + "."
}

// emptyReplyCorrection follows a reply that used output tokens but arrived
// with no text.
const emptyReplyCorrection = "Your reply arrived empty: nothing you wrote reached the sandbox. Write the script as text, in one ```javascript fenced block, and nothing else."

// staleCodeCorrection is sent after a reply that copied the placeholder
// standing in for an earlier script.
func staleCodeCorrection(finish string) string {
	return "Your reply repeated the placeholder that stands in for an earlier script, so nothing ran. " +
		"That script's output is already above and what it kept is in state. " +
		"Write the next script, the one that moves the task forward, and " + finish + "."
}

// toolCallRe recognises the native tool-call formats models are trained
// on — Qwen/Hermes XML, and function-call JSON — capturing the name.
var toolCallRe = regexp.MustCompile(`<function=([A-Za-z_$][\w$.]*)>|<tool_call>\s*\{\s*"name"\s*:\s*"([A-Za-z_$][\w$.]*)"|"(?:name|function)"\s*:\s*"([A-Za-z_$][\w$.]*)"\s*,\s*"(?:arguments|parameters)"`)

// correctionFor is the correction for a reply with no script. A reply that
// tried to make a native tool call is told so by name, because the model
// believes it acted: a generic "no script" reads to it as a broken tool.
func correctionFor(reply string, finish string) string {
	m := toolCallRe.FindStringSubmatch(reply)
	if m == nil && !strings.Contains(reply, "<tool_call>") {
		return proseCorrection(finish)
	}
	name := "the function"
	for i := 1; i < len(m); i++ {
		if m[i] != "" {
			name = "`" + m[i] + "`"
			break
		}
	}
	return "Your reply was a tool call to " + name + ", and there is no tool-calling channel here, so nothing ran. " +
		"Sandbox API functions are ordinary JavaScript: call " + name + " inside one ```javascript block, " +
		"console.log what you need to see, and " + finish + "."
}

// SandboxBuilder produces the sandbox for one Run. The loop calls it
// once per Run with the per-run scope so capabilities can resolve
// scoped state.
type SandboxBuilder interface {
	// Build returns the sandbox + a cleanup func the loop defers.
	Build(ctx context.Context, sess Session, scope Scope, onEvent sandbox.OnEvent) (*sandbox.Sandbox, func(), error)
}

// loop is the default Loop implementation.
type loop struct {
	cfg    Config
	tracer trace.Tracer
}

// New constructs the default Loop from Config. Required fields: LLM,
// Sessions, Steps, SandboxBuilder. Missing fields panic at construction
// so misconfigurations surface at boot, not on the first request.
func New(cfg Config) Loop {
	if cfg.LLM == nil {
		panic("agentloop: Config.LLM is required")
	}
	if cfg.Sessions == nil {
		panic("agentloop: Config.Sessions is required")
	}
	if cfg.Steps == nil {
		panic("agentloop: Config.Steps is required")
	}
	if cfg.SandboxBuilder == nil {
		panic("agentloop: Config.SandboxBuilder is required")
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.MaxIterations <= 0 {
		cfg.MaxIterations = MaxIterations
	}
	if cfg.RunTimeout <= 0 {
		cfg.RunTimeout = RunTimeout
	}
	if cfg.HistoryWindow <= 0 {
		cfg.HistoryWindow = HistoryWindow
	}
	// A reserve that swallows the whole window leaves no room for the
	// prompt, so every request would be "over budget" from the first
	// turn. That is a configuration error, not a degraded mode, and it
	// surfaces at boot like the missing-dependency panics above.
	if cfg.ContextBudget.enabled() && cfg.ContextBudget.allowance() <= 0 {
		panic("agentloop: Config.ContextBudget.Reserve leaves no room for the prompt")
	}
	if cfg.Policy == nil {
		cfg.Policy = sandbox.DefaultPolicy{}
		slog.Info("agentloop: no PolicyChecker configured — installing conservative sandbox.DefaultPolicy")
	}
	return &loop{cfg: cfg, tracer: resolveTracer(cfg.TracerProvider)}
}

// Run drives the reasoning loop for one user message. See the package
// doc for the protocol.
func (l *loop) Run(ctx context.Context, req RunRequest) (result RunResult, err error) {
	ctx, cancel := context.WithTimeoutCause(ctx, l.cfg.RunTimeout,
		fmt.Errorf("%w after %s — raise Config.RunTimeout for a slow host", ErrRunTimeout, l.cfg.RunTimeout))
	defer cancel()

	ctx, span := l.tracer.Start(ctx, spanRun, trace.WithAttributes(
		attribute.String(attrSessionID, req.SessionID),
	))
	defer func() {
		if err != nil {
			rerr := redactErr(l.cfg.Redactor, err)
			span.RecordError(rerr)
			span.SetStatus(codes.Error, rerr.Error())
		}
		span.SetAttributes(
			attribute.String(attrStatus, result.Status),
			attribute.Int(attrSteps, result.Steps),
			attribute.Int64(attrPromptTok, int64(result.Tokens.Prompt)),
			attribute.Int64(attrCompleteTok, int64(result.Tokens.Completion)),
		)
		span.End()
	}()

	runStart := l.cfg.Now()
	usage := &runTokenUsage{}

	sess, err := l.cfg.Sessions.Get(ctx, req.SessionID)
	if err != nil {
		return RunResult{}, fmt.Errorf("agentloop: load session: %w", err)
	}
	// A store may answer an unknown id with a zero Session, as the
	// SessionStore contract allows and agentloopmem does. The run is that
	// session regardless, and BuildContext.SessionID is read from here:
	// left empty, a capability keyed on the session — a per-session
	// InputRequester — could not be built on any fresh session's first turn.
	if sess.ID == "" {
		sess.ID = req.SessionID
	}

	priorSteps, err := l.cfg.Steps.LastN(ctx, req.SessionID, l.cfg.HistoryWindow)
	if err != nil {
		// History is best-effort: a flaky read shouldn't deny the run
		// its turn. Surface via the event stream and continue with an
		// empty history.
		l.emit(req.OnEvent, RunEvent{Type: "warning", Content: "history rehydrate failed: " + err.Error()})
		priorSteps = nil
	}
	stepIdx := int32(len(priorSteps))

	writeStep := func(step RunStep) {
		step.StepIndex = stepIdx
		step.SessionID = req.SessionID
		step.CreatedAt = l.cfg.Now()
		// Persisted, unlike the live RunEvent stream — a durable trace a
		// human might review later is exactly the kind of surface
		// Config.Redactor exists to protect, arguably more than the
		// transient callback.
		step.Content = l.cfg.Redactor.Apply(step.Content)
		if err := l.cfg.Steps.Append(ctx, step); err != nil {
			l.emit(req.OnEvent, RunEvent{Type: "warning", Content: "persist step failed: " + err.Error()})
		}
		stepIdx++
	}

	// Persist the user turn first so multi-turn rehydrate sees it on
	// the next Run.
	writeStep(RunStep{
		StepType: "user",
		Content:  req.Message,
		ToolArgs: json.RawMessage(`{}`),
	})
	l.emit(req.OnEvent, RunEvent{Type: "user", Content: req.Message})

	var sysPromptCaptured string
	var dataBytesCarried int64
	finalize := func(status string) RunResult {
		summary := FinalizeSummary{
			Status:           status,
			PromptTokens:     usage.prompt.Load(),
			CompletionTokens: usage.completion.Load(),
			StepCount:        stepIdx,
			DurationMs:       int32(time.Since(runStart).Milliseconds()),
			DataBytesCarried: dataBytesCarried,
		}
		if err := l.cfg.Sessions.Finalize(ctx, req.SessionID, summary); err != nil {
			l.emit(req.OnEvent, RunEvent{Type: "warning", Content: "finalize failed: " + err.Error()})
		}
		return RunResult{
			RunID:        req.SessionID,
			FinalText:    l.cfg.Redactor.Apply(usage.lastFinal),
			Steps:        int(stepIdx),
			Status:       status,
			SystemPrompt: sysPromptCaptured,
			Tokens: TokenUsage{
				Prompt:     summary.PromptTokens,
				Completion: summary.CompletionTokens,
			},
			DataBytesCarried: dataBytesCarried,
		}
	}

	buildCtx, buildSpan := l.tracer.Start(ctx, spanSandboxBuild)
	sb, cleanup, err := l.cfg.SandboxBuilder.Build(buildCtx, sess, req.Scope, func(evt sandbox.Event) {
		l.emit(req.OnEvent, RunEvent{
			Type:    "sandbox_event",
			Content: evt.Summary,
			Args: map[string]any{
				"kind":    string(evt.Kind),
				"summary": evt.Summary,
				"detail":  evt.Detail,
				"result":  evt.Result,
				"payload": evt.Payload,
			},
		})
	})
	if err != nil {
		rerr := redactErr(l.cfg.Redactor, err)
		buildSpan.RecordError(rerr)
		buildSpan.SetStatus(codes.Error, rerr.Error())
	}
	buildSpan.End()
	if err != nil {
		return finalize("error"), fmt.Errorf("agentloop: build sandbox: %w", err)
	}
	defer cleanup()
	sb.SetPolicy(l.cfg.Policy)
	// Before the prompt is composed, so the declarations name the finish
	// this run honours rather than one it ignores.
	if l.cfg.UserOutput {
		sb.EnableUserOutput()
	}

	sandboxAPI := sb.SystemPrompt()
	sysPrompt := composeSystemPrompt(l.cfg.Protocol, l.cfg.UserOutput, sess.SystemPrompt, sandboxAPI)
	if req.Context != "" {
		sysPrompt = sysPrompt + "\n\n" + req.Context
	}
	sysPromptCaptured = sysPrompt

	model := sess.Model
	if model == "" {
		model = l.cfg.Model
	}

	history, stepOf := rehydrateHistory(priorSteps, l.cfg.HistoryWindow)
	history = l.compactHistory(ctx, req, usage, history, stepOf, writeStep)
	history = append(history, llm.Message{Role: "user", Content: req.Message})
	// baseLen marks the end of the prior conversation, before this Run
	// appends anything of its own — elideStaleCode uses it so eliding
	// stale script bodies never touches an earlier run's real final
	// answer (see shape.go).
	baseLen := len(history)

	// prevState carries the previous turn's return value into the next as
	// `state`. Held in-process across iterations of this Run; full
	// fidelity, never serialised into the prompt.
	prevState := json.RawMessage("{}")
	if len(req.InitialState) > 0 {
		prevState = req.InitialState
	}
	emptyRetries := 0
	proseRetries := 0
	lastError := ""

	// runTurn is one LLM round-trip plus its optional JS execution,
	// pulled out of the loop below so its turn span (opened first thing,
	// closed via defer on every exit) can wrap the whole thing. done
	// means Run should return (res, turnErr) now; done == false means
	// keep looping — res and turnErr are meaningless in that case.
	runTurn := func(iter int) (res RunResult, turnErr error, done bool) {
		tctx, turnSpan := l.tracer.Start(ctx, spanTurn, trace.WithAttributes(
			attribute.Int(attrIteration, iter),
		))
		defer func() {
			if turnErr != nil {
				rerr := redactErr(l.cfg.Redactor, turnErr)
				turnSpan.RecordError(rerr)
				turnSpan.SetStatus(codes.Error, rerr.Error())
			}
			turnSpan.End()
		}()

		// response_chunk events bypass l.emit (they're forwarded straight
		// from the LLM client's streaming callback, chunk by chunk) and
		// so can't go through the same redaction path — and can't be
		// redacted chunk-by-chunk at all, since a secret value can split
		// across two chunk boundaries with neither chunk containing the
		// full substring to match against. When a Redactor is
		// configured, suppress live streaming rather than risk a partial
		// leak: newResponseStreamer(nil) still accumulates the full text
		// for the post-stream DONE/JS-fence parsing below, it just emits
		// no incremental events. The complete, redacted text still
		// reaches the caller via the terminal "response" RunEvent.
		streamOnEvent := req.OnEvent
		if l.cfg.Redactor != nil {
			streamOnEvent = nil
		}
		streamer := newResponseStreamer(streamOnEvent)
		// Elide stale script bodies: their effect already lives in `state`
		// (state) and the logs (observations), so re-sending the verbatim
		// source every turn is pure triangular accumulation. Keep only
		// the most recent block (for error-retry continuity) plus all logs.
		messages := append([]llm.Message{{Role: "system", Content: sysPrompt}}, elideStaleCode(history, baseLen)...)
		// The shape of the state this turn's script will receive is
		// injected EPHEMERALLY — built fresh each call, never stored in
		// history — so the digest appears exactly once.
		if shape := dataShape(prevState); shape != "" {
			messages = append(messages, llm.Message{
				Role:    "user",
				Content: "state (shape only):\n" + shape,
			})
			if delta := int64(len(prevState)) - int64(len(shape)); delta > 0 {
				dataBytesCarried += delta
			}
		}
		// The single chokepoint where a prompt is bounded in tokens.
		// Everything that grows it has already happened by here —
		// rehydration, compaction, this run's own turns, elision, the
		// args digest — so this is the only place the real size is
		// knowable.
		messages = l.fitBudget(messages, req, turnSpan)

		llmReq := llm.CompletionRequest{
			Messages: messages,
			Model:    model,
		}
		// Reserve is only a guarantee if the completion is actually
		// held to it; left unset, a long answer overruns the room the
		// trim just made for it.
		if outCap, ok := l.cfg.ContextBudget.completionCap(); ok {
			llmReq.MaxTokens = &outCap
		}

		llmCtx, llmSpan := l.tracer.Start(tctx, spanLLMCall, trace.WithAttributes(
			attribute.String(attrModel, model),
		))
		resp, err := l.cfg.LLM.Stream(llmCtx, llmReq, streamer.onChunk)
		if err != nil {
			rerr := redactErr(l.cfg.Redactor, err)
			llmSpan.RecordError(rerr)
			llmSpan.SetStatus(codes.Error, rerr.Error())
		} else {
			llmSpan.SetAttributes(
				attribute.Int64(attrPromptTok, int64(resp.InputTokens)),
				attribute.Int64(attrCompleteTok, int64(resp.OutputTokens)),
			)
		}
		llmSpan.End()
		if err != nil {
			// A call cut short by the run's own cap reports the cap: "context
			// deadline exceeded" from inside a model call reads as a host
			// that stopped answering, and it was read that way.
			if cause := context.Cause(ctx); errors.Is(cause, ErrRunTimeout) {
				err = cause
			}
			writeStep(RunStep{
				StepType: "error",
				Content:  err.Error(),
				ToolArgs: json.RawMessage(`{}`),
			})
			l.emit(req.OnEvent, RunEvent{Type: "error", Content: err.Error()})
			if errors.Is(err, ErrRunTimeout) {
				return finalize("error"), err, true
			}
			return finalize("error"), fmt.Errorf("agentloop: llm: %w", err), true
		}
		usage.prompt.Add(int32(resp.InputTokens))
		usage.completion.Add(int32(resp.OutputTokens))
		usage.lastPrompt.Store(int32(resp.InputTokens))
		usage.lastCompletion.Store(int32(resp.OutputTokens))

		// Every call is reported before its reply is judged, so an empty or
		// scriptless reply still leaves why it stopped and what it thought.
		l.emit(req.OnEvent, RunEvent{
			Type:    "llm_call",
			Content: resp.Reasoning,
			Args: map[string]any{
				"finish_reason":    resp.FinishReason,
				"reasoning_tokens": resp.ReasoningTokens,
				"content_bytes":    len(resp.Content),
			},
			Tokens: &CallTokens{Prompt: int32(resp.InputTokens), Completion: int32(resp.OutputTokens)},
		})

		replyText := resp.Content
		sigs := resp.ThoughtSignatures

		// An empty completion (e.g. a reasoning model returning a 0-token
		// reply on a large prompt) is usually transient. Don't mislabel it
		// as a completed answer via the no-fence fallback below — retry a
		// bounded number of times, then surface a real error. Tokens are
		// still counted above: the call happened and (if billed) cost
		// something even though it produced nothing.
		if strings.TrimSpace(replyText) == "" {
			if emptyRetries < maxEmptyRetries {
				emptyRetries++
				l.emit(req.OnEvent, RunEvent{Type: "warning", Content: "empty model turn, retrying"})
				turnSpan.AddEvent("empty model turn, retrying")
				// Output tokens with no text means the model answered in a
				// form the provider did not pass on as text — Gemini behind
				// an OpenAI-compatible proxy does this with code it emits as
				// a native executable-code part. Resending the same prompt
				// gets the same reply (proxies cache, and the model is
				// close to deterministic), so say what arrived instead.
				if resp.OutputTokens > 0 {
					history = append(history,
						llm.Message{Role: "assistant", Content: "(no text arrived)", ThoughtSignatures: sigs},
						llm.Message{Role: "user", Content: emptyReplyCorrection},
					)
				}
				return RunResult{}, nil, false
			}
			writeStep(RunStep{
				StepType: "error",
				Content:  ErrEmptyResponse.Error(),
				ToolArgs: json.RawMessage(`{}`),
			})
			l.emit(req.OnEvent, RunEvent{Type: "error", Content: ErrEmptyResponse.Error()})
			return finalize("error"), ErrEmptyResponse, true
		}

		// Legacy terminator: a "DONE" marker = final answer. answer()
		// (handled after JS execution below) is the documented way to
		// finish; this is a defensive fallback for a model that emits
		// the old marker. Checked before JS extraction so an accidental
		// ```javascript inside the final answer doesn't loop.
		if done, final := ExtractDoneMarker(replyText); done {
			return l.respond(req, writeStep, finalize, usage, final, sigs), nil, true
		}

		jsCode := ExtractJSBlock(replyText)
		// A copied marker runs as a no-op, so under RequireScript it is
		// corrected like a reply with no script rather than executed.
		copiedMarker := l.cfg.RequireScript && isStaleCodeCopy(jsCode)
		if jsCode == "" || copiedMarker {
			if l.cfg.RequireScript && proseRetries < maxProseRetries {
				proseRetries++
				warning, correction := "reply had no script, asking again", correctionFor(replyText, finishInstruction(l.cfg))
				if copiedMarker {
					warning, correction = "reply copied an elided earlier script, asking again", staleCodeCorrection(finishInstruction(l.cfg))
				}
				l.emit(req.OnEvent, RunEvent{Type: "warning", Content: warning, Args: map[string]any{"reply": replyText}})
				history = append(history,
					llm.Message{Role: "assistant", Content: replyText, ThoughtSignatures: sigs},
					llm.Message{Role: "user", Content: correction},
				)
				return RunResult{}, nil, false
			}
			if l.cfg.RequireScript {
				writeStep(RunStep{
					StepType: "error",
					Content:  ErrNoScript.Error(),
					ToolArgs: json.RawMessage(`{}`),
				})
				l.emit(req.OnEvent, RunEvent{Type: "error", Content: ErrNoScript.Error()})
				return finalize("error"), ErrNoScript, true
			}
			// Model answered directly without a fence or a DONE marker.
			// Treat the whole reply as the final response rather than
			// looping pointlessly.
			return l.respond(req, writeStep, finalize, usage, replyText, sigs), nil, true
		}

		// Run the JS. Persist before AND after so the trace shows the
		// in-flight script even if execution panics.
		start := time.Now()
		writeStep(RunStep{
			StepType: "execute_js",
			Content:  jsCode,
			ToolArgs: assistantArgs(sigs),
		})
		l.emit(req.OnEvent, RunEvent{Type: "execute_js", Content: jsCode})

		var resultContent string
		var persistBlob json.RawMessage

		_, jsSpan := l.tracer.Start(tctx, spanExecuteJS)
		sb.SetBlocks(ExtractDataBlocks(replyText))
		logs, ret, execErr := sb.ExecuteTurn(jsCode, prevState)
		if execErr != nil {
			rerr := redactErr(l.cfg.Redactor, execErr)
			jsSpan.RecordError(rerr)
			jsSpan.SetStatus(codes.Error, rerr.Error())
		}
		jsSpan.End()
		resultContent = logs
		if execErr != nil {
			// A failing turn must NOT destroy the carry — preserve
			// prevState so the model can fix its code and retry with the
			// state it already built. The error is sanitized (goja's
			// internal stack noise stripped) and, where a common failure
			// pattern is recognised, given a concrete retry hint — both
			// aimed at breaking the "model repeats the same mistake"
			// attractor rather than reasoning from a raw Go error string.
			// This does NOT end the turn — the model gets to see the error
			// and retry next iteration — so it's recorded on jsSpan above,
			// not returned as turnErr here.
			clean, location, hint := describeScriptError(execErr.Error(), jsCode, 0)
			resultContent = "Error: " + clean
			if location != "" {
				resultContent += "\n" + location
			}
			final := l.cfg.FinalError != nil && l.cfg.FinalError(clean)
			if hint == "" && !final && l.cfg.RetryHint != nil {
				hint = l.cfg.RetryHint(clean)
			}
			if hint == "" && !final {
				hint = retryHintForError(clean)
			}
			if hint != "" && !final {
				resultContent += "\n\nRETRY HINT: " + hint
			}
			if clean == lastError && !final {
				resultContent += "\n\nThis is the same error as your previous script. Change the approach instead of retrying the same code."
			}
			lastError = clean
			if !final {
				resultContent += "\n(state is unchanged — fix and retry.)"
			}
		} else {
			lastError = ""
			prevState = ret // thread full-fidelity state into the next turn
			persistBlob = ret
		}
		finalOutput, doneCalled := "", false
		if l.cfg.UserOutput && execErr == nil {
			finalOutput, doneCalled = sb.TakeDone()
			if doneCalled && strings.TrimSpace(finalOutput) == "" {
				doneCalled = false
				resultContent += "\n\nNOTICE: exit() was called with nothing printed, so the task is still open. print(...) the result for the user, or pass it as exit(\"message\")."
			}
		}
		if l.cfg.BudgetNotice && l.cfg.MaxIterations-iter-1 == 2 {
			resultContent += "\n\nNOTICE: two replies are left before this task is cut off. If the work is done or nearly done, finish now (" +
				finishInstruction(l.cfg) + "), saying what is finished and what is not."
		}
		// Redacted before the model reads it, not only before it is stored:
		// a fetch() whose response echoes a credential back would otherwise
		// hand that credential to the model, and so to whatever it writes.
		userTurnContent := executionResultPrefix + "\n" + l.cfg.Redactor.Apply(resultContent)

		callPrompt := usage.lastPrompt.Load()
		callCompletion := usage.lastCompletion.Load()
		writeStep(RunStep{
			StepType:         "execute_js_result",
			Content:          resultContent,
			ToolArgs:         json.RawMessage(`{}`),
			DurationMs:       int32(time.Since(start).Milliseconds()),
			PromptTokens:     callPrompt,
			CompletionTokens: callCompletion,
		})
		l.emit(req.OnEvent, RunEvent{
			Type:    "execute_js_result",
			Content: resultContent,
			Tokens:  &CallTokens{Prompt: callPrompt, Completion: callCompletion},
		})

		if len(persistBlob) > 0 && string(persistBlob) != "{}" {
			if err := l.cfg.Sessions.UpdateData(ctx, req.SessionID, persistBlob); err != nil {
				l.emit(req.OnEvent, RunEvent{Type: "warning", Content: "data snapshot persist failed: " + err.Error()})
			}
			var parsed any
			if err := json.Unmarshal(persistBlob, &parsed); err == nil {
				if m, ok := parsed.(map[string]any); ok {
					l.emit(req.OnEvent, RunEvent{Type: "data_update", Args: m})
				}
			}
		}

		if doneCalled {
			return l.respond(req, writeStep, finalize, usage, finalOutput, nil), nil, true
		}

		// Terminal: answer() inside the script ends the run with its
		// value as the final response — the in-script equivalent of the
		// DONE marker. Checked after the data snapshot so `data` is
		// persisted before the run returns.
		if final, ok := sb.TakeAnswer(); ok {
			writeStep(RunStep{
				StepType:         "response",
				Content:          final,
				ToolArgs:         json.RawMessage(`{}`),
				PromptTokens:     callPrompt,
				CompletionTokens: callCompletion,
			})
			l.emit(req.OnEvent, RunEvent{
				Type:    "response",
				Content: final,
				Tokens:  &CallTokens{Prompt: callPrompt, Completion: callCompletion},
			})
			usage.lastFinal = final
			out := finalize("completed")
			l.emitDone(req, out)
			return out, nil, true
		}

		history = append(history,
			llm.Message{Role: "assistant", Content: replyText, ThoughtSignatures: sigs},
			llm.Message{Role: "user", Content: userTurnContent},
		)
		return RunResult{}, nil, false
	}

	for iter := 0; iter < l.cfg.MaxIterations; iter++ {
		if ctx.Err() != nil {
			if cause := context.Cause(ctx); errors.Is(cause, ErrRunTimeout) {
				l.emit(req.OnEvent, RunEvent{Type: "error", Content: cause.Error()})
				return finalize("error"), cause
			}
			l.emit(req.OnEvent, RunEvent{Type: "error", Content: "context cancelled: " + ctx.Err().Error()})
			res := finalize("error")
			return res, ctx.Err()
		}

		if res, turnErr, done := runTurn(iter); done {
			return res, turnErr
		}
	}

	errMsg := fmt.Sprintf("agent exceeded max iterations (%d) without emitting DONE", l.cfg.MaxIterations)
	writeStep(RunStep{
		StepType: "error",
		Content:  errMsg,
		ToolArgs: json.RawMessage(`{}`),
	})
	l.emit(req.OnEvent, RunEvent{Type: "error", Content: errMsg})
	return finalize("max_iterations"), fmt.Errorf("agentloop: %s", errMsg)
}

// respond persists+emits a final response step and finalizes the run —
// the two direct-answer exits (a DONE marker, or a reply with no code
// fence) share this rather than repeating the four-event sequence twice.
// sigs are the thought signatures of the reply text is, when text is
// the model's own reply rather than output a script printed.
func (l *loop) respond(req RunRequest, writeStep func(RunStep), finalize func(string) RunResult, usage *runTokenUsage, text string, sigs []string) RunResult {
	writeStep(RunStep{
		StepType:         "response",
		Content:          text,
		ToolArgs:         assistantArgs(sigs),
		PromptTokens:     usage.lastPrompt.Load(),
		CompletionTokens: usage.lastCompletion.Load(),
	})
	l.emit(req.OnEvent, RunEvent{
		Type:    "response",
		Content: text,
		Tokens:  &CallTokens{Prompt: usage.lastPrompt.Load(), Completion: usage.lastCompletion.Load()},
	})
	usage.lastFinal = text
	result := finalize("completed")
	l.emitDone(req, result)
	return result
}

func (l *loop) emitDone(req RunRequest, result RunResult) {
	l.emit(req.OnEvent, RunEvent{
		Type: "done",
		Summary: &RunSummary{
			SessionID:        req.SessionID,
			Steps:            result.Steps,
			DataBytesCarried: result.DataBytesCarried,
			Tokens: struct {
				Prompt     int32 `json:"prompt"`
				Completion int32 `json:"completion"`
			}{Prompt: result.Tokens.Prompt, Completion: result.Tokens.Completion},
		},
	})
}

// runTokenUsage is the per-run rolling counter used by Run. Atomic in
// case a future change fans out streamer/sandbox callbacks onto a
// goroutine.
type runTokenUsage struct {
	prompt         atomic.Int32
	completion     atomic.Int32
	lastPrompt     atomic.Int32
	lastCompletion atomic.Int32
	lastFinal      string
}

// emit forwards a RunEvent to the request's callback when non-nil.
// emit redacts evt (a no-op when Config.Redactor is nil) and forwards
// it to fn when non-nil. The one choke point every RunEvent passes
// through, so this is the one place redaction needs to be wired in.
func (l *loop) emit(fn func(RunEvent), evt RunEvent) {
	if fn == nil {
		return
	}
	fn(redactEvent(l.cfg.Redactor, evt))
}

// redactEvent applies r to evt's free-text surfaces: Content directly,
// and Args by marshaling to JSON, redacting the bytes, and
// unmarshaling back — catching a secret at any nesting depth without
// walking Args field by field (see the redact package doc comment).
// Falls back to the original Args on any marshal/unmarshal error
// rather than dropping the event's data.
func redactEvent(r *redact.Redactor, evt RunEvent) RunEvent {
	if r == nil {
		return evt
	}
	evt.Content = r.Apply(evt.Content)
	if evt.Args != nil {
		if raw, err := json.Marshal(evt.Args); err == nil {
			var out map[string]any
			if json.Unmarshal(r.ApplyBytes(raw), &out) == nil {
				evt.Args = out
			}
		}
	}
	return evt
}

// fitBudget applies Config.ContextBudget to one turn's assembled
// request, returning what to send.
//
// A trim is reported, never silent: dropping the early conversation is
// exactly the kind of degradation that otherwise shows up much later
// as an agent that has forgotten what it was asked. The event carries
// the numbers an operator needs to decide whether to raise the window,
// register fewer packs, or lower HistoryWindow so compaction — which
// preserves meaning, unlike this — does the work instead.
func (l *loop) fitBudget(messages []llm.Message, req RunRequest, span trace.Span) []llm.Message {
	fit := l.cfg.ContextBudget.fit(messages)
	if !l.cfg.ContextBudget.enabled() {
		return fit.Messages
	}
	span.SetAttributes(
		attribute.Int(attrBudgetTokens, fit.Tokens),
		attribute.Int(attrBudgetAllowance, l.cfg.ContextBudget.allowance()),
		attribute.Int(attrBudgetDropped, fit.Dropped),
	)
	if fit.Dropped > 0 {
		l.emit(req.OnEvent, RunEvent{
			Type: "budget_trimmed",
			Args: map[string]any{
				"messages_dropped": fit.Dropped,
				"estimated_tokens": fit.Tokens,
				"allowance":        l.cfg.ContextBudget.allowance(),
			},
		})
	}
	if fit.Over {
		// Nothing droppable is left, so this warns and sends anyway
		// rather than failing the run: the request may still succeed
		// (the estimate is deliberately pessimistic), and a provider
		// error is a better outcome than refusing to try.
		l.emit(req.OnEvent, RunEvent{
			Type: "warning",
			Content: fmt.Sprintf(
				"context budget: prompt is ~%d tokens against an allowance of %d with nothing left to drop — the system prompt and current turn alone exceed the window",
				fit.Tokens, l.cfg.ContextBudget.allowance()),
		})
	}
	return fit.Messages
}

// compactHistory hands the rehydrated history to Config.Compactor,
// returning what the run should actually send.
//
// Compaction is best-effort in both directions. A failure warns and
// returns the history untouched rather than denying the run its turn —
// a larger prompt beats no answer — and a Compactor that returns
// nothing is treated the same way, since dropping the conversation
// silently is the one outcome worse than not compacting.
//
// The compactor's own token spend is folded into the run's usage on
// every path, including the error path: a provider call that produced
// an unusable summary still cost what it cost, and a Run whose reported
// usage omitted it would understate the session.
func (l *loop) compactHistory(ctx context.Context, req RunRequest, usage *runTokenUsage, history []llm.Message, stepOf []int32, writeStep func(RunStep)) []llm.Message {
	if l.cfg.Compactor == nil {
		return history
	}
	ctx, span := l.tracer.Start(ctx, spanCompact)
	defer span.End()

	before := len(history)
	res, err := l.cfg.Compactor.Compact(ctx, history)
	usage.prompt.Add(res.Tokens.Prompt)
	usage.completion.Add(res.Tokens.Completion)
	if err != nil {
		rerr := redactErr(l.cfg.Redactor, err)
		span.RecordError(rerr)
		span.SetStatus(codes.Error, rerr.Error())
		l.emit(req.OnEvent, RunEvent{Type: "warning", Content: "history compaction failed: " + err.Error()})
		return history
	}
	if len(res.Messages) == 0 && before > 0 {
		l.emit(req.OnEvent, RunEvent{Type: "warning", Content: "history compaction returned nothing; keeping history"})
		return history
	}

	// Persist the summary as a checkpoint so the NEXT Run rehydrates
	// from it instead of paying to summarize the same turns again. A
	// compactor that left Summary empty compacts for this run only; a
	// failure to record the checkpoint costs that saving and nothing
	// else, so it warns rather than failing the run.
	persisted := false
	if res.Summary != "" {
		if retain, ok := retainFromStep(res.RetainedFrom, stepOf); ok {
			marker, err := json.Marshal(CompactionCheckpoint{RetainFromStep: retain})
			if err != nil {
				l.emit(req.OnEvent, RunEvent{Type: "warning", Content: "compaction checkpoint not persisted: " + err.Error()})
			} else {
				writeStep(RunStep{
					StepType: StepTypeSummary,
					Content:  res.Summary,
					ToolArgs: marker,
				})
				persisted = true
				span.SetAttributes(attribute.Int64(attrCompactRetainFrom, int64(retain)))
			}
		}
	}

	after := len(res.Messages)
	span.SetAttributes(
		attribute.Int(attrCompactBefore, before),
		attribute.Int(attrCompactAfter, after),
	)
	if after < before {
		l.emit(req.OnEvent, RunEvent{
			Type: "compacted",
			Args: map[string]any{"messages_before": before, "messages_after": after, "persisted": persisted},
		})
	}
	return res.Messages
}

// retainFromStep translates a compactor's message-space RetainedFrom
// into the StepIndex a checkpoint must name, using the step each
// rehydrated message came from.
//
// It reports false when there is no checkpoint worth writing: a
// RetainedFrom of zero or less means the compaction folded nothing
// away, and recording a checkpoint that covers nothing would replay a
// summary alongside the very history it summarizes.
func retainFromStep(retainedFrom int, stepOf []int32) (int32, bool) {
	if retainedFrom <= 0 || len(stepOf) == 0 {
		return 0, false
	}
	if retainedFrom >= len(stepOf) {
		// Everything was folded away, so the retained history starts
		// after the last step that had a message — one past it.
		return stepOf[len(stepOf)-1] + 1, true
	}
	return stepOf[retainedFrom], true
}
