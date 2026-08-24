---
title: Extending agentloop
description: Custom capabilities, sandbox composition, stores, and policy — the seams agentloop is designed to be extended through.
sidebar:
  order: 1
---

agentloop ships a minimal default (`DefaultCapabilities`,
`DefaultSandboxBuilder`, `agentloopmem`'s in-memory stores,
`sandbox.DefaultPolicy`) that's enough to run the quickstart, and four seams
meant to be replaced for a real application.

## Custom capabilities

A `Capability` is a name plus a `Build` func returning `[]sandbox.Pack`:

```go
type Capability struct {
    Name        string
    Description string
    AlwaysOn    bool
    Build       func(BuildContext) ([]sandbox.Pack, error)
}
```

Application-specific dependencies — a database handle, an accumulator, a
broadcaster — should be **closed over when the `Capability` is
constructed**, rather than threaded through `BuildContext`. `BuildContext`
carries only what's generic to every run: context, tenant scope, session and
message IDs, the invoking user, and the capability allowlist. See
`DefaultCapabilities` in `defaults.go` for the pattern — it closes over an
`llm.Client` and a model name to build the `ai` capability.

A capability whose `Build` fails is logged and skipped — a `warning` sandbox
event, not an aborted session. One flaky capability (a missing API key, a
downed dependency) shouldn't deny the user their turn.

## Custom sandbox composition

`DefaultSandboxBuilder` composes a fixed `Capabilities` list into a fresh
sandbox for every run, filtered by an optional allowlist. That's enough when
every session gets the same capability set.

Implement `agentloop.SandboxBuilder` yourself — its one method, `Build` —
when the capability set **varies per scope or session**, e.g. a per-tenant
allowlist pulled from a database rather than passed in as a static slice.

```go
type SandboxBuilder interface {
    Build(ctx context.Context, sess Session, scope Scope, onEvent sandbox.OnEvent) (*sandbox.Sandbox, func(), error)
}
```

`DefaultSandboxBuilder.Build` in `defaults.go` is a good starting point to
copy: it shows the enabled-capability filtering, the failure-doesn't-abort
handling, and wiring in the skill-discovery and help packs last (they
introspect every other pack that was built).

## Custom stores

`SessionStore` and `StepStore` are small interfaces — back them with
whatever persistence your application already has. `agentloopmem` ships
in-memory implementations to start from.

`agentlooptest.StepStoreContract` is a conformance harness: run it against
your own `StepStore` implementation as an acceptance gate, the same way
`agentloopmem`'s own `contract_test.go` does. It exists so a Postgres-backed
or SQLite-backed store gets held to the exact behavioural guarantees the
loop relies on, without having to read the loop's source to infer what those
are.

## Policy

Implement `sandbox.PolicyChecker` to gate side-effecting primitives —
`fetch`, `ai`, or your own — per call.

- **`sandbox.DefaultPolicy`** is a conservative default: deny side effects,
  block private-network fetches.
- **`sandbox.AllowAll`** is the explicit fail-open escape hatch, for
  contexts (a trusted internal tool, a sandboxed eval harness) where that
  conservatism isn't wanted.

## Next

[Reference](/agentloop/reference/) has the full list of known limitations —
worth reading before extending agentloop into a production path, since
several of them (no durable execution, no built-in cost ceiling) are things
an application built on top has to supply itself.
