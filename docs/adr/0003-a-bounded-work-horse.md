# ADR-0003: A bounded work-horse, not a general agent

- **Status:** Revisiting
- **Confidence:** Medium
- **Decided:** 2026-07-05 (stated in `studio-apps/core/agentloop/CONCEPT.md` §11)
- **Last reviewed:** 2026-08-28
- **Consumers:** the library's own roadmap — this is the record that says which
  feature requests are in scope and which are somebody else's problem.

## Context

A loop whose only action is "run this JavaScript" is tempting to point at
everything. The ancestor's design rationale is explicit that this is a mistake:
the intelligence here is **capitalised in the harness** — the `run(args)`
protocol, the shape digest, the carry-vs-see split, error recovery, capability
gating — authored once, rather than rented from the model per token on every
run. That property is the source of the cheap/reproducible/auditable payoff, and
it is also the boundary. The structure forecloses specific failure modes
(guessing a schema name, guessing an `args` field, blind querying); it does
nothing for reasoning it cannot scaffold.

The niche it is good at is **retrieve → join → compute → report**. The measured
case: a cross-source spend-cap question answered on a cheap model in **2 turns
at ~11k prompt / ~1.3k completion**, deterministically, where the same task
under a data-echo design cost ~27k tokens and produced a *wrong* answer.

Four places it is the wrong tool, named honestly in CONCEPT.md §11:
latency-critical single-shot work (plain function calling is simpler); real
async/streaming/parallel I/O ([ADR-0007](0007-goja-synchronous-runtime.md));
open-ended planning the structure cannot scaffold; and workloads where
capabilities are never composed, which is exactly where the code-as-glue
advantage evaporates and only sandbox overhead is left.

## Decision

We grow the library **horizontally, not vertically**: more capabilities, each
just a function inside the sandbox, rather than more cognition asked of the
model.

- **A fast, cheap model is the intended default**, not a compromise. The
  structure removes the weak-model failure modes, so paying for a strong model
  to avoid mistakes the harness already forecloses is waste. A strong model is
  for genuinely ambiguous analytical steps.
- **Features that only pay off for open-ended cognition are out of scope**
  unless the case is made in a record of its own.
- **The four boundaries above are documented, not hidden.** `README.md`'s *Known
  limitations* is the user-facing half of this.

## Consequences

**Buys.** A clear answer to "should agentloop do X?". It is why there is no
planner, no multi-agent orchestration and no async story, and why the
context-economy work ([ADR-0005](0005-carry-by-return-see-by-log.md),
[ADR-0006](0006-elide-stale-code-per-run.md),
[ADR-0013](0013-context-control-from-one-number.md)) got the effort instead.

**Costs.** It forecloses the largest and loudest use case in the field. Coding
agents are open-ended repository work — precisely the "planning the structure
cannot scaffold" the boundary excludes — and they are where the interesting
stress on an agent loop is.

## What would change our mind

- **This is no longer hypothetical — it has already happened here.**
  studio-apps ADR-0049
  (`docs/adr/0049-coding-agent-cli-as-agentloop-hardening-vehicle.md`) proposed
  a coding-agent CLI on top of this loop *as a hardening vehicle*, arguing that
  repository work is the most punishing consumer available and that the context
  economy should compound harder there, not less. This repository now ships
  `cmd/agentloop` with `ext.ExecPack` (run commands) and filesystem packs — an
  agent that reads and changes a workspace, which is precisely the open-ended
  work §11's third boundary excludes. The boundary was crossed before it was
  ever written down here. What is still unresolved is whether it *works*: no
  eval yet says whether the harness holds up on repository work or degrades the
  way §11 predicts. Until it does, this record is `Revisiting` and the boundary
  should be treated as an open question, not as a rule to enforce.
- **Composition turns out to be rare in practice.** If real consumers write
  single-primitive scripts, they are paying sandbox overhead for nothing and
  should be using function calling.
- **The fast-model default stops holding on our own evals.** `eval/` is the
  instrument; if cheap models fail cases the harness was supposed to make safe,
  the "intelligence in the harness" claim is weaker than stated.

## Cost of change

Free to widen, expensive to narrow again — the usual asymmetry, and unusually
sharp here. Nothing stops us adding scope; but every consumer that adopts
agentloop for open-ended work makes the boundary unenforceable afterwards, and
the mechanisms that make the bounded case cheap are the ones that would have to
bend first.

## Revisions

- 2026-07-05 — Stated as CONCEPT.md §11, after the prompt-instinct overhaul and
  a live verification run.
- 2026-08-26 — Challenged by studio-apps ADR-0049, which proposes the coding
  agent this record excludes.
- 2026-08-28 — Recorded here retroactively, as `Revisiting` because of the above.
- 2026-08-28 — Overtaken the same day: `cmd/agentloop`, `ext.ExecPack` and the
  filesystem packs make this repository the coding agent ADR-0049 proposed. The
  boundary in §11 now describes an intention the code has already left behind,
  which is the strongest possible reason to leave the record open rather than
  quietly delete the boundary.
