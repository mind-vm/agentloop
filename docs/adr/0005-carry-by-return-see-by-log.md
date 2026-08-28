# ADR-0005: Carry by `return`, see by `log()` — and show shape, not values

- **Status:** Working
- **Confidence:** High
- **Decided:** 2026-06 (in `core/agentloop`), carried over 2026-08-21
- **Last reviewed:** 2026-08-28
- **Consumers:** the library's public API — `RunResult.DataBytesCarried` exists
  to make the saving observable to a caller.

## Context

The obvious way to run a multi-turn loop is to put working state in the
conversation: the model queries 10k rows, the rows are serialised into the
prompt as a tool result, and every subsequent turn re-sends them. Context cost
then tracks *data size*, and a large intermediate result is unaffordable
regardless of how little of it the model actually needs to read.

But the model rarely needs to *see* data in order to *operate* on it. It
references `args.customers[i].annual_spend_usd` symbolically and computes in JS.
It needs a concrete value only when it must branch on content, or state the
value in the answer.

## Decision

Two channels, separated on purpose:

- **`return` carries data forward.** The value is held server-side at full
  fidelity, threaded into the next turn as `args`, and **never serialised into
  the prompt**. The loop is a reducer: `stateₙ₊₁ = runₙ(stateₙ)`.
- **`log()` is the observation channel** — the only thing the model sees back.
  It logs the handful of facts it must read with its own eyes, not whole
  datasets. `MaxLogBytes` bounds it.
- **A structural digest of `args` is injected instead of its values** —
  keys, types, array lengths, rendered as a TypeScript type literal (`shape.go`)
  — so the model knows which fields it can reach without the values entering
  context. It is **ephemeral**: shown for the current turn only, never
  accumulated across turns.
- **The saving is measured, not assumed.** Each turn accumulates
  `len(prevArgs) − len(shape)` — the exact bytes withheld from that prompt, net
  of the digest actually sent — and the per-run total rides
  `RunResult.DataBytesCarried`. Bytes, not tokens: bytes are a measured fact,
  token conversion is model-specific and belongs to the display layer.

## Consequences

**Buys.** Context cost decouples from data size. Cross-source joins happen in
code over carried arrays — computed and verifiable — rather than as facts the
model holds in working memory and can fabricate; on the spend-cap eval that is
the difference between passing and a confident wrong answer. And the digest
removes the classic weak-model failure of guessing a field name.

**Costs.** Nothing bounds the carry. A script can return an arbitrarily large
object and the loop will hold it and thread it, run after run;
`DataBytesCarried` gives a caller the observability to notice but not a limit.
The carry is also **in memory between turns**, which is the direct cause of the
no-durable-execution gap in [ADR-0007](0007-goja-synchronous-runtime.md): a
process restart mid-run loses it.

## What would change our mind

- **A consumer OOMs on a carried value.** That is the cue to add a configurable
  cap with a clear error, rather than to keep documenting the hazard.
- **The digest is not enough to write correct code against.** If scripts
  routinely fail because the model needed a *value* the shape did not give it,
  the digest is under-specified — sampling a few representative values would be
  the first thing to try, at a cost this record deliberately does not pay today.

## Cost of change

Low to extend, high to remove. Adding a cap or richer digest is internal.
Removing the split is not: `RunResult.DataBytesCarried` is exported,
`SessionStore.UpdateData` persists the snapshot, and the entire prompt contract
in `prompts.go` teaches `return`-to-carry / `log`-to-see.

## Revisions

- 2026-06 — Built in `core/agentloop`, replacing a re-serialised-global design
  that cost ~27k tokens and a wrong answer on the case this one gets right in
  ~11k.
- 2026-07-04 — The measurement channel (`DataBytesCarried`) added, per
  studio-apps ADR-0039: only the loop knows both the carried size and the digest
  it injected, so it cannot be measured after the fact.
- 2026-08-28 — Recorded here retroactively.
