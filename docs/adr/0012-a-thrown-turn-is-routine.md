# ADR-0012: A thrown turn is routine, not a failure

- **Status:** Working
- **Confidence:** Medium
- **Decided:** 2026-06 (the recovery model), 2026-08-21 (sanitising and hints)
- **Last reviewed:** 2026-08-28
- **Consumers:** every caller — this is what makes a code-writing agent usable
  rather than brittle.

## Context

When the only action is executing code, the model **will** emit code that
throws: a typo'd field, a null deref, a hallucinated API, a wrong SQL
placeholder. This is normal traffic, not an exceptional path
([ADR-0004](0004-code-as-action.md)).

Two ways to handle it badly were both observed. **Wiping the carried state on an
error** sends the model into "I seem to have lost everything" spirals — fixing
that was the difference between a stuck run and a one-turn correction. And
**feeding goja's raw error back** hands the model a message full of internal Go
symbols (`FetchPack.func3 (native)`, `sandbox:`/`GoError:` prefixes) that mean
nothing to it and dilute the one line that does.

There is a third, quieter failure: an LLM call that returns **empty content** —
a reasoning model on a large prompt, say. That used to fall through the
no-code-fence path and silently complete the run with an empty final answer.

## Decision

An exception is feedback. The turn does not fail; the run continues.

- **A failing turn never destroys the carry.** The previous `args` are preserved
  and the error is fed back, so the model repairs its code next turn with all
  the state it built intact.
- **`sanitizeErrorForModel` strips runtime plumbing** — goja's stack frames, the
  `sandbox:` and `GoError:` wrappers — while deliberately preserving real JS
  error kinds (`ReferenceError`, `TypeError`), which both the model and the hint
  matcher rely on.
- **`retryHintForError` appends a `RETRY HINT:` line** from a small,
  provider-agnostic set of pattern matches (transient timeout, hallucinated API,
  type error on undefined), so the model self-corrects rather than repeating the
  same mistake.
- **An empty completion is retried** a bounded number of times, then fails the
  run with the exported `ErrEmptyResponse` — a loud, catchable error rather than
  a successful-looking empty answer.
- **`MaxIterations` bounds the whole thing**, so a model that genuinely cannot
  recover fails cleanly instead of looping forever.
- **Degradation is announced.** A capability that fails to build, or a
  compaction that fails, emits a `warning` event and the run proceeds — visible,
  not silent.

## Consequences

**Buys.** The common failure — the model wrote nearly-right code — costs one
extra turn instead of a dead run, and the model gets a message it can actually
act on. Errors are also a designed part of the prompt contract, not leakage.

**Costs.** Sanitising discards information: the frames removed are real, and a
`sandbox`-side bug is harder to diagnose from the model's view of it than from
the raw text. The hints are pattern matches against error strings, which is
brittle by nature — a provider or goja wording change silently stops matching,
and nothing tests that it still does in the field. And a model that fails the
same way repeatedly burns `MaxIterations` worth of tokens before stopping.

## What would change our mind

- **A hint fires wrongly and misleads the model.** A wrong hint is worse than no
  hint; that is the cue to shrink the set rather than grow it.
- **Operators cannot debug a sandbox error from the trace.** The fix is to
  persist the raw error alongside the sanitised one — the model's view and the
  operator's view are different needs and should stop sharing a string.
- **Errors dominate the turn budget.** If real runs spend most turns recovering,
  the problem is upstream in the prompt or the primitive surface, not here.

## Cost of change

Low. All of it is internal to the loop, except `ErrEmptyResponse`, which is
exported and may be matched by callers. The recovery model itself is the
expensive part to change, since the prompt teaches the model to expect it.

## Revisions

- 2026-06 — Preserve-state-on-error established in `core/agentloop`, after runs
  that spiralled when state was wiped.
- 2026-08-21 — `sanitizeErrorForModel`, `retryHintForError` and the bounded
  empty-response retry added during the extraction hardening pass.
- 2026-08-28 — Recorded retroactively.
