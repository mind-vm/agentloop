package agentloop

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/jryannel/agentloop/llm"
	"github.com/jryannel/agentloop/sandbox"
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

// RunTimeout is the default wall-clock cap on a single Run call
// (override via Config.RunTimeout).
const RunTimeout = 5 * time.Minute

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
	HistoryWindow int

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
	if cfg.Policy == nil {
		cfg.Policy = sandbox.DefaultPolicy{}
		slog.Info("agentloop: no PolicyChecker configured — installing conservative sandbox.DefaultPolicy")
	}
	return &loop{cfg: cfg, tracer: resolveTracer(cfg.TracerProvider)}
}

// Run drives the reasoning loop for one user message. See the package
// doc for the protocol.
func (l *loop) Run(ctx context.Context, req RunRequest) (result RunResult, err error) {
	ctx, cancel := context.WithTimeout(ctx, l.cfg.RunTimeout)
	defer cancel()

	ctx, span := l.tracer.Start(ctx, spanRun, trace.WithAttributes(
		attribute.String(attrSessionID, req.SessionID),
	))
	defer func() {
		if err != nil {
			span.RecordError(err)
			span.SetStatus(codes.Error, err.Error())
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

	priorSteps, err := l.cfg.Steps.LastN(ctx, req.SessionID, l.cfg.HistoryWindow)
	if err != nil {
		// History is best-effort: a flaky read shouldn't deny the run
		// its turn. Surface via the event stream and continue with an
		// empty history.
		emit(req.OnEvent, RunEvent{Type: "warning", Content: "history rehydrate failed: " + err.Error()})
		priorSteps = nil
	}
	stepIdx := int32(len(priorSteps))

	writeStep := func(step RunStep) {
		step.StepIndex = stepIdx
		step.SessionID = req.SessionID
		step.CreatedAt = l.cfg.Now()
		if err := l.cfg.Steps.Append(ctx, step); err != nil {
			emit(req.OnEvent, RunEvent{Type: "warning", Content: "persist step failed: " + err.Error()})
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
	emit(req.OnEvent, RunEvent{Type: "user", Content: req.Message})

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
			emit(req.OnEvent, RunEvent{Type: "warning", Content: "finalize failed: " + err.Error()})
		}
		return RunResult{
			RunID:        req.SessionID,
			FinalText:    usage.lastFinal,
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
		emit(req.OnEvent, RunEvent{
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
		buildSpan.RecordError(err)
		buildSpan.SetStatus(codes.Error, err.Error())
	}
	buildSpan.End()
	if err != nil {
		return finalize("error"), fmt.Errorf("agentloop: build sandbox: %w", err)
	}
	defer cleanup()
	sb.SetPolicy(l.cfg.Policy)

	sandboxAPI := sb.SystemPrompt()
	sysPrompt := ComposeSystemPrompt(sess.SystemPrompt, sandboxAPI)
	if req.Context != "" {
		sysPrompt = sysPrompt + "\n\n" + req.Context
	}
	sysPromptCaptured = sysPrompt

	model := sess.Model
	if model == "" {
		model = l.cfg.Model
	}

	history := rehydrateHistory(priorSteps, l.cfg.HistoryWindow)
	history = append(history, llm.Message{Role: "user", Content: req.Message})
	// baseLen marks the end of the prior conversation, before this Run
	// appends anything of its own — elideStaleCode uses it so eliding
	// stale run() bodies never touches an earlier run's real final
	// answer (see shape.go).
	baseLen := len(history)

	// prevArgs carries the previous turn's return value into the next as
	// `args`. Held in-process across iterations of this Run; full
	// fidelity, never serialised into the prompt.
	prevArgs := json.RawMessage("{}")
	emptyRetries := 0

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
				turnSpan.RecordError(turnErr)
				turnSpan.SetStatus(codes.Error, turnErr.Error())
			}
			turnSpan.End()
		}()

		streamer := newResponseStreamer(req.OnEvent)
		// Elide stale run() bodies: their effect already lives in `args`
		// (state) and the logs (observations), so re-sending the verbatim
		// source every turn is pure triangular accumulation. Keep only
		// the most recent block (for error-retry continuity) plus all logs.
		messages := append([]llm.Message{{Role: "system", Content: sysPrompt}}, elideStaleCode(history, baseLen)...)
		// The shape of the args this turn's run() will receive is
		// injected EPHEMERALLY — built fresh each call, never stored in
		// history — so the digest appears exactly once.
		if shape := dataShape(prevArgs); shape != "" {
			messages = append(messages, llm.Message{
				Role:    "user",
				Content: "args shape (what run(args) receives this turn):\n" + shape,
			})
			if delta := int64(len(prevArgs)) - int64(len(shape)); delta > 0 {
				dataBytesCarried += delta
			}
		}
		llmReq := llm.CompletionRequest{
			Messages: messages,
			Model:    model,
		}

		llmCtx, llmSpan := l.tracer.Start(tctx, spanLLMCall, trace.WithAttributes(
			attribute.String(attrModel, model),
		))
		resp, err := l.cfg.LLM.Stream(llmCtx, llmReq, streamer.onChunk)
		if err != nil {
			llmSpan.RecordError(err)
			llmSpan.SetStatus(codes.Error, err.Error())
		} else {
			llmSpan.SetAttributes(
				attribute.Int64(attrPromptTok, int64(resp.InputTokens)),
				attribute.Int64(attrCompleteTok, int64(resp.OutputTokens)),
			)
		}
		llmSpan.End()
		if err != nil {
			writeStep(RunStep{
				StepType: "error",
				Content:  err.Error(),
				ToolArgs: json.RawMessage(`{}`),
			})
			emit(req.OnEvent, RunEvent{Type: "error", Content: err.Error()})
			return finalize("error"), fmt.Errorf("agentloop: llm: %w", err), true
		}
		usage.prompt.Add(int32(resp.InputTokens))
		usage.completion.Add(int32(resp.OutputTokens))
		usage.lastPrompt.Store(int32(resp.InputTokens))
		usage.lastCompletion.Store(int32(resp.OutputTokens))

		replyText := resp.Content

		// An empty completion (e.g. a reasoning model returning a 0-token
		// reply on a large prompt) is usually transient. Don't mislabel it
		// as a completed answer via the no-fence fallback below — retry a
		// bounded number of times, then surface a real error. Tokens are
		// still counted above: the call happened and (if billed) cost
		// something even though it produced nothing.
		if strings.TrimSpace(replyText) == "" {
			if emptyRetries < maxEmptyRetries {
				emptyRetries++
				emit(req.OnEvent, RunEvent{Type: "warning", Content: "empty model turn, retrying"})
				turnSpan.AddEvent("empty model turn, retrying")
				return RunResult{}, nil, false
			}
			writeStep(RunStep{
				StepType: "error",
				Content:  ErrEmptyResponse.Error(),
				ToolArgs: json.RawMessage(`{}`),
			})
			emit(req.OnEvent, RunEvent{Type: "error", Content: ErrEmptyResponse.Error()})
			return finalize("error"), ErrEmptyResponse, true
		}

		// Legacy terminator: a "DONE" marker = final answer. answer()
		// (handled after JS execution below) is the documented way to
		// finish; this is a defensive fallback for a model that emits
		// the old marker. Checked before JS extraction so an accidental
		// ```javascript inside the final answer doesn't loop.
		if done, final := ExtractDoneMarker(replyText); done {
			return l.respond(req, writeStep, finalize, usage, final), nil, true
		}

		jsCode := ExtractJSBlock(replyText)
		if jsCode == "" {
			// Model answered directly without a fence or a DONE marker.
			// Treat the whole reply as the final response rather than
			// looping pointlessly.
			return l.respond(req, writeStep, finalize, usage, replyText), nil, true
		}

		// Run the JS. Persist before AND after so the trace shows the
		// in-flight script even if execution panics.
		start := time.Now()
		writeStep(RunStep{
			StepType: "execute_js",
			Content:  jsCode,
			ToolArgs: json.RawMessage(`{}`),
		})
		emit(req.OnEvent, RunEvent{Type: "execute_js", Content: jsCode})

		var resultContent string
		var persistBlob json.RawMessage

		_, jsSpan := l.tracer.Start(tctx, spanExecuteJS)
		logs, ret, execErr := sb.ExecuteTurn(jsCode, prevArgs)
		if execErr != nil {
			jsSpan.RecordError(execErr)
			jsSpan.SetStatus(codes.Error, execErr.Error())
		}
		jsSpan.End()
		resultContent = logs
		if execErr != nil {
			// A failing turn must NOT destroy the carry — preserve
			// prevArgs so the model can fix its code and retry with the
			// state it already built. The error is sanitized (goja's
			// internal stack noise stripped) and, where a common failure
			// pattern is recognised, given a concrete retry hint — both
			// aimed at breaking the "model repeats the same mistake"
			// attractor rather than reasoning from a raw Go error string.
			// This does NOT end the turn — the model gets to see the error
			// and retry next iteration — so it's recorded on jsSpan above,
			// not returned as turnErr here.
			clean := sanitizeErrorForModel(execErr.Error())
			resultContent = "Error: " + clean
			if hint := retryHintForError(clean); hint != "" {
				resultContent += "\n\nRETRY HINT: " + hint
			}
			resultContent += "\n(args from the previous turn are preserved — fix and retry.)"
		} else {
			prevArgs = ret // thread full-fidelity return into next turn's args
			persistBlob = ret
		}
		userTurnContent := "Execution result:\n" + resultContent

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
		emit(req.OnEvent, RunEvent{
			Type:    "execute_js_result",
			Content: resultContent,
			Tokens:  &CallTokens{Prompt: callPrompt, Completion: callCompletion},
		})

		if len(persistBlob) > 0 && string(persistBlob) != "{}" {
			if err := l.cfg.Sessions.UpdateData(ctx, req.SessionID, persistBlob); err != nil {
				emit(req.OnEvent, RunEvent{Type: "warning", Content: "data snapshot persist failed: " + err.Error()})
			}
			var parsed any
			if err := json.Unmarshal(persistBlob, &parsed); err == nil {
				if m, ok := parsed.(map[string]any); ok {
					emit(req.OnEvent, RunEvent{Type: "data_update", Args: m})
				}
			}
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
			emit(req.OnEvent, RunEvent{
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
			llm.Message{Role: "assistant", Content: replyText},
			llm.Message{Role: "user", Content: userTurnContent},
		)
		return RunResult{}, nil, false
	}

	for iter := 0; iter < l.cfg.MaxIterations; iter++ {
		if ctx.Err() != nil {
			emit(req.OnEvent, RunEvent{Type: "error", Content: "context cancelled: " + ctx.Err().Error()})
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
	emit(req.OnEvent, RunEvent{Type: "error", Content: errMsg})
	return finalize("max_iterations"), fmt.Errorf("agentloop: %s", errMsg)
}

// respond persists+emits a final response step and finalizes the run —
// the two direct-answer exits (a DONE marker, or a reply with no code
// fence) share this rather than repeating the four-event sequence twice.
func (l *loop) respond(req RunRequest, writeStep func(RunStep), finalize func(string) RunResult, usage *runTokenUsage, text string) RunResult {
	writeStep(RunStep{
		StepType:         "response",
		Content:          text,
		ToolArgs:         json.RawMessage(`{}`),
		PromptTokens:     usage.lastPrompt.Load(),
		CompletionTokens: usage.lastCompletion.Load(),
	})
	emit(req.OnEvent, RunEvent{
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
	emit(req.OnEvent, RunEvent{
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
func emit(fn func(RunEvent), evt RunEvent) {
	if fn != nil {
		fn(evt)
	}
}
