# ADR-0007: goja in-process — ES5.1, synchronous, no event loop

- **Status:** Working
- **Confidence:** Medium
- **Decided:** 2026-03 (in `thinking-core`), carried over 2026-08-21
- **Last reviewed:** 2026-08-28
- **Consumers:** every caller — the execution model is visible to the model in
  the system prompt and to the operator as the durability limits.

## Context

Running model-written code needs a runtime. The options are a separate process
or container per run (strong isolation, heavyweight, an IPC boundary for every
primitive), a WASM runtime, or an embedded interpreter.

agentloop embeds [goja](https://github.com/dop251/goja): pure Go, memory-safe,
interruptible, and — decisively — a runtime where registering a capability is
handing it a Go closure rather than designing a wire protocol for it. goja
parses `async`/`await`/Promises but has **no event loop**, so continuations
silently never run.

## Decision

Scripts execute in an in-process goja runtime, synchronously, with no ambient
authority and no event loop.

- **Synchronous is the contract, not an accident.** Every primitive — `fetch`,
  `ai`, `require` — blocks and returns a value directly. The system prompt tells
  the model this explicitly and warns it off `Promise`/`async`/`.then()`, since
  goja would accept that syntax and then never run the continuation, which is
  the worst possible failure shape.
- **ES5.1 idiom is what the prompt asks for** (`var`, function expressions,
  string concatenation), because that is what the runtime reliably supports.
- **Containment is timeouts and output caps**, not quotas. Execution is bounded
  by a wall clock and log output is truncated at `MaxLogBytes`.
- **Determinism is a feature.** A synchronous single-threaded runtime plus a
  fakeable `llm.Client` means a run is replayable and the composed prompt can be
  pinned in a unit test.

## Consequences

**Buys.** A capability is a Go function. Testing is straightforward. Traces are
replayable. And there is no container lifecycle, no IPC, and no second runtime
to deploy — which is most of why the library is small enough to embed.

**Costs.** Three, all documented in `README.md`'s *Known limitations*:

- **No parallel I/O inside a turn.** Fetching 50 URLs is 50 sequential blocking
  calls holding a goroutine and a VM for the duration. The fix that fits the
  model is batched primitives (`fetchAll(urls)`) that fan out in Go and return
  synchronously — not Promises.
- **Isolation is process-level, not tenant-level.** goja is memory-safe and
  interruptible, but every sandbox shares the host's heap and CPU. There is no
  per-run memory or CPU quota, so a pathological script is a noisy-neighbour
  risk.
- **No durable execution.** A `Run` lives in one goroutine and the carried
  `args` are in memory between turns; a process restart mid-run kills it. Steps
  already persisted survive, but nothing resumes. This is the **largest gap in
  the library**, and return-threading is what would make the fix cheap: the
  state to persist at a turn boundary is already one value.

## What would change our mind

- **A consumer needs runs to survive a restart, or approvals that outlive a
  run.** That is the durability seam — persist `args` plus history at turn
  boundaries — and it is the first thing to build, not a rewrite of this record.
- **Multi-tenant load makes noisy neighbours real.** Then execution moves behind
  a quota'd runner or an isolate-per-tenant boundary, and the in-process
  assumption goes with it.
- **Parallel I/O becomes the bottleneck** in a real workload. Batched primitives
  first; an event loop only if batching demonstrably is not enough, because
  async would invalidate the prompt contract and the determinism story at once.

## Cost of change

High and asymmetric. Adding batched primitives or a durability seam is additive
and cheap. Moving execution out of process is not: every capability becomes a
serialisable call, `BuildContext` closures stop working
([ADR-0008](0008-sandbox-is-the-trust-boundary.md)), and `pool` becomes
meaningless. Introducing an event loop would break the one contract the system
prompt states most emphatically.

## Revisions

- 2026-03 — goja chosen in `valiro-ai/thinking-core`.
- 2026-07-05 — The three costs assessed in `core/agentloop/ASSESSMENT.md`, which
  names durability the #1 gap.
- 2026-08-28 — Recorded here retroactively.
