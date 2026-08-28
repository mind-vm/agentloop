# ADR-0002: A standalone library, not an application

- **Status:** Working
- **Confidence:** High
- **Decided:** 2026-08-21
- **Last reviewed:** 2026-08-28
- **Consumers:** `cmd/agentloop`, the CLI in this repository — the first real
  one, and in-tree rather than external. The ancestors
  (`studio-apps/core/agentloop`, mybrain) still run their own copies, so the
  duplication this record set out to end has not ended yet.

## Context

This loop has been written three times. `valiro-ai/thinking-core` (through
2026-03) was an embeddable Go library where the agent called a `think` tool and
kept working state on a persistent `data` global. `studio-apps/core/agentloop`
is the platform module version, wired into fx, tenancy, an event bus and an
`agents.Runner` adapter. mybrain carried a third, with capabilities bound to its
own schema. A fourth — valiro-go's `internal/agent` — existed independently and
is what surfaced the elision bug in
[ADR-0006](0006-elide-stale-code-per-run.md).

Every copy re-derived the same core and then fused it to its host: fx modules,
`tenancy.Scope`, `sqlb.DB` on `BuildContext`, docs/board/messages capabilities.
The reasoning loop is the differentiated part; none of that plumbing is.

## Decision

agentloop is a **standalone Go module** — `github.com/mind-vm/agentloop` — that
depends on no application framework and no application schema.

- **Nothing host-specific in the core.** The extraction dropped mybrain's
  docs/board/messages/schedule/blocks capabilities and the `sqlb.DB` / `Blocks`
  fields from `BuildContext`. What an application needs, it closes over when it
  constructs a `Capability`.
- **Usable with no application at all.** `DefaultCapabilities` and
  `DefaultSandboxBuilder` exist so the library composes itself; `agentloopmem`
  supplies stores and `examples/cli` is a runnable program. A port that requires
  the caller to write the glue before it runs at all is not a library.
- **No dependency injection framework, no bus, no tenancy.** Composition is
  struct literals and interfaces. An fx module belongs in the consumer.
- **The name is `agentloop`.** `thinkingcore` named the ancestor and is retired;
  the loop, not the "core", is what this is.

## Consequences

**Buys.** One place to fix a loop bug instead of three or four. A published
module can be depended on by repositories that will never share a monorepo with
studio-apps. And the constraint is load-bearing in itself — being unable to
reach for fx or a schema is what kept the seams
([ADR-0008](0008-sandbox-is-the-trust-boundary.md),
[ADR-0010](0010-store-interfaces-and-conformance.md)) small enough to be
implemented by an outsider.

**Costs.** The forks did not disappear when this was published. Until they adopt
it, a fix here reaches nothing and the count of copies has gone *up*, not down.
That is the central risk of this record and the thing to check first at review.
`cmd/agentloop` blunts it without removing it: an in-tree CLI proves the seams
compose from outside the root package, but it cannot prove they compose for
someone who does not control the library.

## What would change our mind

- **No ancestor adopts it within a couple of release cycles.** Then this is a
  fourth copy with better docs, and the answer is either to do the adoption work
  or to stop maintaining this as the canonical one.
- **Adoption requires the core to grow host concepts.** If a consumer cannot
  wire this up without `tenancy.Scope` or an fx module leaking into an exported
  signature, the seam is wrong — fix the seam, do not admit the concept.

## Cost of change

Cheap to keep, expensive to undo asymmetrically. Folding this back into a host
means the exported API disappears and every external caller loses it; whereas
keeping it standalone only ever costs the adoption work above. Reversing the
*name* is the expensive part — the module path is in every consumer's `go.mod`,
and it has already been renamed once (`jryannel` → `mind-vm`, 2026-08-25).

## Revisions

- 2026-08-21 — Extracted from mybrain as a standalone module.
- 2026-08-25 — Module path moved from `github.com/jryannel/agentloop` to
  `github.com/mind-vm/agentloop`.
- 2026-08-28 — Recorded retroactively.
- 2026-08-28 — `cmd/agentloop` shipped with a release pipeline, giving the
  library its first consumer and the *Consumers* line something other than
  "none".
