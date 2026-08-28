# ADR-0008: Capabilities build packs; the sandbox is the trust boundary

- **Status:** Working
- **Confidence:** High
- **Decided:** 2026-06 (in `core/agentloop`), reshaped for the library 2026-08-21
- **Last reviewed:** 2026-08-28
- **Consumers:** every application that adds a primitive — `Capability`,
  `SandboxBuilder` and `PolicyChecker` are the three seams the README's
  *Extending* section is about.

## Context

"You execute LLM-generated code?" is the first question anyone asks. The answer
has to be structural, not reassurance: the runtime must have **no ambient
authority** — no filesystem, no network, no process access except through
functions someone deliberately registered.

That also has to be true without the library knowing what those functions are. A
capability closes over an application's own dependencies (a database handle, a
mail sender, a broadcaster) that agentloop must never import
([ADR-0009](0009-thin-core-optional-packages.md)).

## Decision

Three nested seams, each with one method:

- **`sandbox.Pack`** — a named bundle of JS primitives plus the `declare` lines
  that document them to the model.
- **`Capability`** — a name plus `Build(BuildContext) ([]sandbox.Pack, error)`.
  Application-specific dependencies are **closed over when the `Capability` is
  constructed**, not threaded through `BuildContext`. That is what keeps
  `BuildContext` small enough to stay stable, and it is why the extraction could
  drop mybrain's `sqlb.DB` field outright.
- **`SandboxBuilder`** — composes the packs for one Run.
  `DefaultSandboxBuilder` covers the common case;
  `EnabledCapabilities` is a per-session allowlist over it, so authority is
  per-run rather than global.

Over all of them, **`sandbox.PolicyChecker` gates each side-effecting call**.
`sandbox.DefaultPolicy` is conservative (deny side effects, block
private-network fetches); `sandbox.AllowAll` is the explicit, named fail-open
escape hatch — fail-open is available but never accidental.

**A capability whose `Build` fails is logged and skipped**, emitting a `warning`
rather than aborting the run. One flaky capability should not deny the user
their turn.

## Consequences

**Buys.** The model reaches exactly what was handed to it and nothing else, and
the grant is auditable at the call site. Because the model's reasoning *is* the
script, the persisted trace is a stronger audit artifact than a list of tool
calls — the whole worked solution, replayable. And a third-party primitive
(`ext/`, an OpenAPI-generated skill) is policy-gated by construction, since
generated functions call the sandbox's own HTTP primitive rather than a client
of their own.

**Costs.** Two subtle traps come with closures. A `Capability` closes over
`BuildContext.Ctx`, so caching a built sandbox naively leaks the first Run's
cancellation into every later Run — the whole reason `pool.SandboxPool` swaps
contexts rather than just memoising. And skip-on-failure means a
misconfigured capability degrades **silently to a warning**: the run proceeds
with less authority than intended, which is the safe direction but not an
obvious one.

## What would change our mind

- **Skipping a failed capability produces a wrong answer rather than a worse
  one.** If a consumer needs "this capability or no run", that is a per-capability
  `Required` flag, not a change of default.
- **`BuildContext` grows a third field for one consumer.** That is the signal
  the closure discipline has broken and the seam needs rethinking, not widening.
- **Policy needs to be per-argument, not per-call.** Domain-level fetch approval
  is already close to that line.

## Cost of change

Low inside, high outside. The seams are three exported interfaces with one
method each; changing any of their signatures breaks every consumer's
capabilities at compile time. Adding to `DefaultPolicy`'s deny list is a
behaviour change for anyone relying on the default and should be treated as one.

## Revisions

- 2026-06 — Built in `core/agentloop` as fx value-group packs.
- 2026-08-21 — Reshaped for a standalone library: the fx value group became a
  plain `[]Capability`, and `BuildContext` lost its host-specific fields.
- 2026-08-24 — `pool.SandboxPool` added, exposing the closed-over-context trap.
- 2026-08-28 — Recorded here retroactively.
