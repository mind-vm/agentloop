---
title: Extending agentloop
description: Custom capabilities, sandbox composition, stores, policy, optional extension packs, and session-scoped sandbox reuse — the seams agentloop is designed to be extended through.
sidebar:
  order: 1
---

agentloop ships a minimal default (`DefaultCapabilities`,
`DefaultSandboxBuilder`, `agentloopmem`'s in-memory stores,
`sandbox.DefaultPolicy`) that's enough to run the quickstart, and six seams
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

## Optional extension packs

The core `sandbox` package's built-in packs — `require`, `http`, `fetch`,
`markdown`, `ai`, `help`, skills — are the minimal set every deployment
needs. The `ext` package holds packs that are generically useful but not
universal, so they stay out of `DefaultCapabilities` and out of `sandbox`
itself:

| Pack | Primitive | Backend seam |
|---|---|---|
| `ext.EmailPack` | `sendEmail({to, subject, body})` | `func(to, subject, body string) error` |
| `ext.SecretPack` | `secret(name)` | `func(name string) (string, error)` |
| `ext.SearchPack` | `documentSearch(query, topK?)` | `func(query string, topK int) ([]ext.SearchHit, error)` |
| `ext.StoresPack` | `stores.list()` / `stores.read(id)` | `ext.StoresBackend` interface |

Each takes a callback or small interface rather than a concrete dependency —
the same decoupling `DefaultCapabilities`' `ai` capability uses for
`llm.Client` — so `ext` stays free of any mail/secrets/search-backend
import. Wire one in the same way as any custom capability:

```go
caps := agentloop.DefaultCapabilities(client, "")
caps = append(caps, agentloop.Capability{
    Name:        "sendEmail",
    Description: "Send a plain-text email",
    Build: func(bc agentloop.BuildContext) ([]sandbox.Pack, error) {
        return []sandbox.Pack{ext.EmailPack(bc.Ctx, mySender.Send)}, nil
    },
})
```

`sendEmail` and `secret` are already in `sandbox.DefaultPolicy`'s
side-effect list, so a bare `DefaultPolicy` denies both until granted:

```go
sandbox.DefaultPolicy{AllowTools: []string{"sendEmail", "secret"}}
```

`documentSearch` and `stores` are read-only against an application-scoped
backend and aren't policy-gated by default — gate them yourself in a
custom `PolicyChecker` if that scoping isn't enough.

## Session-scoped sandbox reuse

`sandbox.New()` builds a whole `goja.Runtime` and re-registers every pack
from scratch — for some packs (the `http`/`secret` `require()` module
wrappers, say) that includes compiling JS source. `DefaultSandboxBuilder`
pays that cost on **every** `Run`, i.e. every message. `pool.SandboxPool`
wraps any other `SandboxBuilder` and instead reuses one sandbox per session
across every `Run`:

```go
builder := pool.New(&agentloop.DefaultSandboxBuilder{Capabilities: caps}, pool.Options{
    IdleTimeout: 30 * time.Minute,
})
defer builder.Close()

loop := agentloop.New(agentloop.Config{
    // ...
    SandboxBuilder: builder,
})
```

This isn't just a cache wrapper, because of two things a fresh-per-Run
builder never had to worry about:

- **Stale context.** A `Capability`'s `Build` closes over `BuildContext.Ctx`
  *once* — `DefaultCapabilities`' `ai` capability and every `ext` pack do
  this. A naively cached sandbox would keep using the *first* Run's
  context — including its cancellation — on every later Run. `SandboxPool`
  gives the delegate builder a swappable context in place of the real one,
  and swaps in each Run's actual context before handing the sandbox back.
- **Concurrent use.** `goja.Runtime` isn't safe for concurrent access from
  multiple goroutines. Two fresh sandboxes never collided on this; one
  shared sandbox can. `SandboxPool` serializes: a second `Run` for a
  session already in flight blocks until the first releases the sandbox
  (via the `cleanup` func `Loop.Run` already defers), rather than racing on
  it. For a typical chat session — no second message before the first one's
  response — this is invisible; an application that intentionally runs
  concurrent `Run`s for one session will see them serialize instead of
  parallelize.

`pool.SandboxPool` also exposes `Evict(sessionID)` for an application-level
session end (logout), and `EvictIdle()` to drive eviction on your own
schedule instead of (or in addition to) the built-in background reaper.

## Next

[Reference](/agentloop/reference/) has the full list of known limitations —
worth reading before extending agentloop into a production path, since
several of them (no durable execution, no built-in cost ceiling) are things
an application built on top has to supply itself.
