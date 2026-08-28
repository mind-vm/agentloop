# ADR-0006: Elide stale code, scoped to the current run

- **Status:** Working
- **Confidence:** High
- **Decided:** 2026-06 (elision), 2026-08-21 (the run scoping fix)
- **Last reviewed:** 2026-08-28
- **Consumers:** the loop's internal history assembly (`history.go`); no
  exported surface.

## Context

In any multi-turn loop every assistant message is re-sent in every later turn's
prompt. With `run()` bodies as the assistant messages, that is triangular —
**O(n²)** in turn count — and script bodies are the largest thing in the
transcript.

A past turn's *effect* already survives elsewhere: its state is in `args`
([ADR-0005](0005-carry-by-return-see-by-log.md)) and its observations are in the
logs. The source text of a script two turns old buys nothing except the ability
to repair a script that just threw, which only applies to the most recent one.

## Decision

Only the **most recent** `run()` block is kept verbatim; older ones collapse to
a one-line marker. This flattens prompt growth to O(n).

The scoping is the part that took a bug to get right. Elision applies **only to
the turns this `Run` call appended** — `elideStaleCode` takes a `fromIndex`, the
length of history before this run started, and everything earlier is preserved
verbatim.

## Consequences

**Buys.** Per-turn prompt size stops rising and plateaus; on the verification
run almost all of the ~7.4k prompt was the static, cacheable system prompt. This
is the single largest context lever in the loop — larger than the data echo,
which is the more obvious target.

**Costs.** The model cannot re-read its own earlier scripts. If a task genuinely
needs turn 2's exact source at turn 9, it is gone and the marker is all there
is. In practice the state and the logs cover it, but this is a real loss of
fidelity, not a free win.

## What would change our mind

- **A run needs an older script back.** If the model starts asking for code it
  wrote earlier, or repeats work because it cannot see what it already did,
  keeping the last *k* rather than the last one is the cheap adjustment.
- **A third implementation gets the scoping wrong.** The `fromIndex` boundary is
  subtle and has been botched once. If it breaks again, the fix is to make
  history assembly structurally unable to reach prior runs, not to patch the
  index arithmetic a third time.

## Cost of change

Low — this is one internal function and its tests. Nothing exported depends on
it, and the covered steps stay in the persisted trace either way; only what is
replayed to the model changes.

## Revisions

- 2026-06 — Elision built in `core/agentloop`.
- 2026-08-21 — **Bug fix, and the reason this record exists.** `elideStaleCode`
  elided every assistant message except the last in the *whole* history. Across
  multiple `Run` calls on one session, once a new run appended its own assistant
  turn, an earlier run's real final answer — a plain text response, not a script
  — was no longer last and got overwritten with `[earlier run() omitted...]`, a
  placeholder that was simply false, corrupting the model's memory of what it
  had already told the user. Found by comparing against valiro-go's
  `internal/agent`, an independent reimplementation of the same loop. Fixed by
  threading `fromIndex`.
- 2026-08-28 — Recorded here retroactively.
