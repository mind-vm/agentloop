# ADR-0009: Keep the core thin; optional surfaces are separate packages

- **Status:** Working
- **Confidence:** High
- **Decided:** 2026-08-21, extended with each package since
- **Last reviewed:** 2026-08-28
- **Consumers:** anyone importing `agentloop` who does not want kin-openapi,
  goldmark, an OTel SDK or an HTTP client in their build.

## Context

A reasoning loop attracts adjacent features: project instructions, skills,
OpenAPI clients, email, secrets, search, sandbox pooling, evals, redaction. Each
is genuinely useful and each brings a dependency, an opinion, or both. Put them
in the root package and every consumer pays for all of them; leave them out and
every consumer writes the same 150 lines.

The ancestors show the failure mode. `core/agentloop` imports `agents`, `llm`,
`sandbox`, `events`, `tenancy` and fx, and it is not extractable as a result —
which is why this repository exists ([ADR-0002](0002-standalone-library-not-an-application.md)).

## Decision

The root package holds the loop and its seams. Everything else is a sibling
package the caller opts into by importing it.

- **`ext`** — packs that are common but not universal (`EmailPack`,
  `SecretPack`, `SearchPack`, `StoresPack`, `OpenAPIPack`). Each takes a
  callback or small interface, so the package carries no mail, secrets, search
  or HTTP-client dependency of its own.
- **`projectctx`, `skills`** — layered capabilities that read a checkout on
  disk. **Nothing in the core imports them**; an application opts in by passing
  `projectctx.Render(docs)` as `RunRequest.Context` and appending
  `skills.Capabilities(sk)` to its slice.
- **`pool`** — a `SandboxBuilder` decorator, so reuse is a wrapper rather than a
  mode in the core.
- **`eval` / `evalmem`, `agentloopmem`, `agentlooptest`, `redact`** — testing,
  storage and hygiene, each usable alone.
- **The extension points are uniform**: `Capability`, `SandboxBuilder`,
  `PolicyChecker`, `Compactor`, `Redactor`, `TracerProvider`. All are interfaces
  or funcs the caller supplies, and every one is a no-op when unset.
- **`eval.Service` takes an `agentloop.Loop` directly** rather than defining its
  own runner interface. `Run(ctx, RunRequest) (RunResult, error)` is already the
  shape a harness needs, and inventing an adapter for it would be ceremony.

## Consequences

**Buys.** A minimal consumer's dependency graph is goja plus what it asked for.
The layering is also a design forcing function: a feature that cannot be built
against the public seams from outside the root package is a feature whose seam
is wrong.

**Costs.** Wiring is on the caller. `examples/cli` has to demonstrate five
packages composed together because no single import does it, and the README's
*Extending* section is long for the same reason. Discoverability suffers: a
consumer can easily not know `pool` or `projectctx` exists.

## What would change our mind

- **Every consumer writes the same wiring.** That is the argument for a
  batteries-included constructor over the same packages — a convenience layer,
  not a merge.
- **A sibling package needs something unexported from the root.** That means the
  seam is missing; export the seam rather than move the package in.

## Cost of change

Cheap to add a package, expensive to merge one back. Merging drags its
dependencies into every consumer's build permanently, which is exactly the
direction that cannot be undone without a breaking release.

## Revisions

- 2026-08-21 — Established by the extraction: mybrain's host-specific
  capabilities and `BuildContext` fields were dropped rather than ported.
- 2026-08-24 — `ext`, `pool`, `eval`/`evalmem`, `redact` added as siblings.
- 2026-08-26/27 — `projectctx` and `skills` added, both explicitly unimported by
  the core.
- 2026-08-28 — Recorded retroactively.
