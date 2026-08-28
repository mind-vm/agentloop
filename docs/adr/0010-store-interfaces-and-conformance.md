# ADR-0010: Small store interfaces, plus a conformance kit to hold them

- **Status:** Working
- **Confidence:** Medium
- **Decided:** 2026-08-21
- **Last reviewed:** 2026-08-28
- **Consumers:** every application — persistence is the one thing agentloop
  deliberately does not provide.

## Context

The loop needs two things persisted: session metadata plus the carried data
snapshot, and the per-turn step trace. Shipping a Postgres implementation would
put a driver, a migration story and a schema opinion into a library whose whole
premise is that it has none ([ADR-0009](0009-thin-core-optional-packages.md)).

But small interfaces have unwritten contracts, and this pair has two that are
genuinely load-bearing and impossible to infer from the signatures:
`StepStore.LastN` must return the most recent *n* steps in **chronological**
order because the loop replays them straight into the context window, and
`SessionStore.Get` must treat an **unknown id as a fresh session, not an
error**. Four independent implementations existed across the ancestors; a
subtly wrong one fails as a corrupted transcript, not as a compile error.

The `Get` contract had already caused a real, invisible bug in a consumer: a
create-if-absent setup tested `if err != nil` to decide whether to attach the
agent's persona. The error is always nil, so the persona was never applied and
the agent answered from memory instead of retrieving. The code compiled, ran,
and looked right.

## Decision

`SessionStore` and `StepStore` stay small interfaces the caller implements, and
the contract they carry ships as executable tests.

- **`agentlooptest.StepStoreContract`** is a reusable conformance harness. Point
  it at your own implementation and it holds it to the behaviour the loop
  relies on. `agentloopmem`'s `contract_test.go` is the worked example.
- **`agentloopmem`** ships in-memory implementations to start from — for local
  dev, one-shot CLIs and eval suites, explicitly not for production traffic.
- **Absence is not an error.** `Get` documents that an unknown id may
  auto-create a shell, and **`Exists(ctx, id) (bool, error)`** is a separate,
  unambiguous existence channel that never creates. Callers that need to
  distinguish get a channel that can actually say so, instead of an error return
  that never fires.
- **The contract lives in the doc comments**, at the interface, where an
  implementer reads it.

## Consequences

**Buys.** A consumer backs the loop with whatever it already runs. The
conformance kit turns "read the doc comment carefully" into an acceptance gate,
which is the only thing that scales past the second implementation. And
`eval.Service` gets session provisioning for free — each case uses a fresh id
and relies on the documented auto-create.

**Costs.** Every consumer writes ~150 lines of store code, and the ancestors
show they will be near-identical (studio-apps eventually extracted a shared
Postgres store for exactly this reason). There is no `SessionStoreContract` yet
— only steps are covered — so the interface with the subtler contract is the one
without a harness.

## What would change our mind

- **A second consumer ships a near-identical Postgres store.** That is the cue
  for a reference implementation in a sibling package, opt-in like everything
  else.
- **A conformance failure is found in the field rather than by the kit.** Then
  the kit is under-specified; extend it and add the `SessionStore` half, which
  is the known gap.

## Cost of change

Adding to the interfaces is a breaking change for every implementer, so the bill
lands outside this repo — `Exists` was added exactly once and it was
coordinated across four stores. Adding a sibling package with a reference store
costs nobody anything.

## Revisions

- 2026-06-13 — The `Exists` channel and the "absence is not an error" contract
  originate in studio-apps ADR-0017, after the silent-persona bug.
- 2026-08-21 — `agentloopmem` and `agentlooptest` ported alongside the
  extraction.
- 2026-08-28 — Recorded here retroactively. Doing so surfaced that
  `SessionStore` has no conformance harness.
