# ADR-0013: Three units of context control, derived from one number

- **Status:** Working
- **Confidence:** Medium
- **Decided:** 2026-08-26 (compactor), 2026-08-28 (budget, chunking, profile)
- **Last reviewed:** 2026-08-28
- **Consumers:** any caller running long sessions or a small local model — the
  two cases the defaults get wrong.

## Context

[ADR-0005](0005-carry-by-return-see-by-log.md) and
[ADR-0006](0006-elide-stale-code-per-run.md) keep the prompt from growing with
*data*. Neither keeps it from growing with *conversation*, and three separate
things went wrong there.

**Truncation loses meaning.** History was cut to `HistoryWindow` steps and the
overflow was simply gone — the model would abruptly forget what the user asked
at the start of a long conversation, with nothing in the trace saying so.

**Messages are the wrong unit.** `HistoryWindow` and the compactor both count
messages, which says nothing about tokens. The same 60-turn history is trivial
against a frontier model and a hard overflow against a locally hosted one.

**And the knobs are in three different units.** A model's window sets four
limits the package exposes separately: `ContextBudget` in tokens,
`HistoryWindow` in steps, the compactor's `Trigger`/`Keep` in messages, log
output in bytes. Every default assumes a frontier model, so against an 8k local
model all four are wrong by an order of magnitude — and fixing them by hand
means four unit conversions from one number the operator already knows.

## Decision

Three layers that do different jobs, plus one derivation over all of them.

- **`Config.Compactor` preserves meaning.** A seam receiving the rehydrated
  history and returning what to send. `SummarizingCompactor` is the
  batteries-included implementation: past `Trigger` messages it summarises
  everything but the most recent `Keep`. It **never splits a `run()` block from
  the execution result it produced** — stranding one from the other shows the
  model output from a script it cannot see. It is hooked **once per Run**, on
  the rehydrated history before the current message is appended, which keeps it
  clear of elision (compaction shifts indices; `baseLen` is computed after).
  Each compaction persists as a `summary` checkpoint step, so a long session
  pays for summarisation only when it has grown past `Trigger` since the last
  one, not on every Run.
- **A long transcript is summarised in chunks, then folded.** A session long
  enough to need compaction can overflow the context of the call meant to fix
  it. Chunks are sized by `SummarizingCompactor.Budget` — sized to the
  *summariser's* window, not the loop's — and partial summaries are folded until
  one remains. Nothing is discarded to make it fit; the only surviving elision
  is one individual message too large for a call on its own. A 242 KB transcript
  is one call against a 128k window and 21 against a 4k one, where before it was
  a single oversized call the small model would reject.
- **`Config.ContextBudget` guarantees sendability.** A token bound applied
  immediately before each request — after compaction, after elision, after this
  run's own turns have grown the history, which is the only point where the real
  size is knowable. The system prompt and current turn are never dropped, oldest
  turns go first, an execution result is never stranded from its `run()` block,
  and a marker tells the model how many turns are gone so it does not assume
  what they contained. Every trim emits `budget_trimmed`; every turn span
  carries estimated tokens against the allowance.
- **`ProfileFor(window)` derives all of it from one number.** `Apply(&cfg)` sets
  `ContextBudget`, `HistoryWindow` and the compactor's knobs; `MaxLogBytes` and
  `ProjectInlineBytes` are read off the profile for the two objects the caller
  constructs itself. **`ProfileFor(0)` is a no-op** — "I don't know the window"
  degrades to the defaults, not to a guess — and at a large window it lands
  exactly on the defaults, so adopting it changes nothing for a hosted model.

**These are complementary, not alternatives.** The compactor preserves meaning;
the budget only bounds the request. **It does not guarantee the request is
sendable**, and an early draft of this record claimed it did. `fit` protects a
floor — `messages[0]` (the system prompt, carrying the `run()`/`answer()`
protocol and the whole tool surface) and the final message — and when that floor
alone exceeds the allowance there is nothing left to drop. The loop sets
`budgetFit.Over`, sends anyway and warns, because the alternatives are a request
stripped of the protocol or no request at all. The fix is an operator's:
a bigger window, fewer registered packs, or a smaller persona — see
[ADR-0014](0014-retrieval-over-inlining.md) for the instruction-file case, where
excerpting is the only remedy. Configure both layers regardless.

## Consequences

**Buys.** A session gets long-term memory by raising `HistoryWindow` and letting
the compactor bound the prompt, rather than by forgetting. A local 8k model
becomes a supported target from one number. And headroom is observable —
`budget_trimmed` and the span attributes mean a session losing history says so.

**Costs.** Four knobs, a profile table and a chunk-and-fold summariser is a lot
of machinery in a small library, and its correctness is subtle: the
run-block/result pairing appears in three places now (elision, compaction,
trimming) and each has to get it right independently. Compaction costs LLM calls
against a second model, and its token cost is reported on the error path too —
a summarisation that returned something unusable was still billable.
Compaction is **best-effort**: a failure emits a `warning` and the run proceeds
on the uncompacted history, because a larger prompt beats no answer.
`ContextBudget` sizing is a bytes/4 estimate unless the caller supplies a real
tokenizer, so the fit is deliberately loose.

## What would change our mind

- **Callers set the profile and then override half of it.** The table would be
  wrong, and the fix is the table, not the mechanism.
- **The bytes/4 estimate over- or under-shoots enough to matter.** Then a real
  tokenizer stops being optional, at the cost of a dependency this library has
  so far refused.
- **The pairing rule breaks in a fourth place.** Three copies of "never strand
  an execution result from its `run()` block" is already one too many; a fourth
  means it belongs in one shared helper over the history representation.

## Cost of change

Low individually — every piece is off by default and adds nothing when unset.
Higher in aggregate: `Profile`, `ContextBudget` and `Compactor` are all exported
types now, so their field names are public contract.

## Revisions

- 2026-08-26 — `Compactor` seam and `SummarizingCompactor` added. Its split
  point is the agentloop analogue of `owainlewis/neo`'s `SafeSplitPoint` (MIT),
  re-derived rather than ported: neo pairs tool_use with tool_result, here the
  pair is a `run()` block and the "Execution result:" turn it produced.
- 2026-08-27 — Compaction persisted as a checkpoint step, so it is not redone.
- 2026-08-28 — `ContextBudget`, chunked-and-folded summarisation, and
  `ProfileFor` added; recorded the same day.
- 2026-08-28 — Corrected: the record said the budget "guarantees the request is
  sendable". It does not, and cannot — a floor it must not trim can exceed the
  allowance on its own, which is what `budgetFit.Over` reports. Caught by
  `cmd/agentloop` making the same wrong claim in its flag help and then fixing
  it.
