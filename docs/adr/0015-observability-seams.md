# ADR-0015: Observability is a seam, and off unless wired

- **Status:** Working
- **Confidence:** Medium
- **Decided:** 2026-08-24
- **Last reviewed:** 2026-08-28
- **Consumers:** any deployment that runs this against real traffic; a no-op for
  everyone else.

## Context

agentloop had no tracing story at all, and a loop is exactly the kind of thing
that needs one — several LLM round-trips, each with a sandbox execution inside
it, any of which can be the slow or failing one.

It also has an unusual leak surface. Scripts can obtain secrets (`ext.SecretPack`),
and a `fetch()` response can echo a credential back. Those values reach a live
event stream, `RunResult.FinalText`, the persisted step trace, and a span's
error text — four places, each forwarding somewhere the secret should not be.

Both features have a hard constraint: a library must not force an OTel SDK, an
exporter, or a redaction policy on a caller who wants none of it.

## Decision

Two independent seams, both no-ops when unset.

- **`Config.TracerProvider`** takes a standard `trace.TracerProvider`. Unset, it
  installs a no-op tracer — a few allocations and no spans. Set, each `Run`
  produces a span tree: root `agentloop.run`, an `agentloop.sandbox_build`
  child, one `agentloop.turn` per LLM round-trip, each containing its own
  `agentloop.llm_call` and (when the model emits JS) `agentloop.execute_js`. A
  JS execution error is recorded on `execute_js` alone, **not** the enclosing
  turn — the turn recovers and feeds the error back
  ([ADR-0012](0012-a-thrown-turn-is-routine.md)), so marking it failed would be
  a lie. **Only `go.opentelemetry.io/otel/trace`** — the stable, SDK-free API
  package — is a dependency; the SDK, exporter and collector wiring are the
  application's.
- **`Config.Redactor`** takes a `*redact.Redactor`: a small ordered
  (value, placeholder) list, matched longest-value-first so overlapping secrets
  cannot carve into each other's placeholder. A **nil `*Redactor` is a safe
  pass-through everywhere**, so "no redaction" needs no caller-side special
  casing. It is applied to every `RunEvent` (`Content` and `Args`, before
  `OnEvent` sees it), `RunResult.FinalText`, persisted `RunStep.Content`, and
  span error messages. `eval.NewService` takes one separately, applied before
  the response reaches the judge's prompt — a third-party LLM call — or is
  persisted in `CaseResult.Response`.
- **Streaming is suppressed when a redactor is set.** `response_chunk` events
  cannot be redacted per-chunk: a secret can split across two boundaries with
  neither chunk containing the whole value to match. Rather than ship redaction
  with a silent partial hole, live streaming is turned off and the complete
  redacted text still arrives via the terminal `response` event.

## Consequences

**Buys.** A production deployment gets a span tree accurate enough to attribute
latency to a specific turn's LLM call or script, without agentloop choosing its
observability stack. Redaction covers the surfaces that actually matter — most
callers read `RunResult` rather than streaming, and a persisted trace outlives
any of them.

**Costs.** Redaction and live streaming are mutually exclusive, which is a real
product decision imposed by a library, and callers who want both get neither
until the chunk-boundary problem is solved properly. Redaction is also
substring matching over known values: it cannot catch a secret it was not told
about, or one the model paraphrased. And the tracing refactor pulled the turn
body into a closure so each span has one close point — worth it, but it means
the loop's control flow is shaped partly by the tracing requirement.

## What would change our mind

- **A caller needs streaming *and* redaction.** The fix is a buffered redacting
  writer that holds back the longest-secret's worth of tail before emitting —
  more machinery, but it removes an ugly either/or.
- **Metrics are asked for as often as traces.** They are deliberately absent;
  two requests make that the wrong call.
- **A secret leaks through a surface not on the list.** Then enumerating
  surfaces is the wrong strategy and redaction belongs at a single choke point
  further down.

## Cost of change

Low. Both are optional `Config` fields whose zero value is "off", so nothing
breaks for a caller who never set them. Widening redaction to a new surface is
additive; narrowing it is a security regression and should be treated as one.

## Revisions

- 2026-08-24 — Tracing seam added, verified against a real OTel SDK in-memory
  span recorder (span tree shape, parent/child, error propagation) rather than
  only compiling.
- 2026-08-24 — `redact` ported from studio-apps `core/redact` and wired in.
  Writing the wiring tests surfaced two real gaps a narrower pass would have
  shipped silently: `RunResult.FinalText` and persisted `RunStep.Content` were
  not redacted at all, and `response_chunk` could not be — which is why
  streaming is suppressed.
- 2026-08-28 — Recorded retroactively.
