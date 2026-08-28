---
title: Extending agentloop
description: Custom capabilities, sandbox composition, stores, policy, optional extension packs, OpenAPI-generated skills, session-scoped sandbox reuse, distributed tracing, context management, evaluating agent quality, and redacting secrets.
sidebar:
  order: 1
---

agentloop ships a minimal default (`DefaultCapabilities`,
`DefaultSandboxBuilder`, `agentloopmem`'s in-memory stores,
`sandbox.DefaultPolicy`) that's enough to run the quickstart, and nine seams
meant to be replaced for a real application — plus an eval harness for
checking whether replacing them made things better or worse, and a
redaction seam for keeping secrets out of both.

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

## Generating a skill from an OpenAPI spec

`ext.OpenAPIPack(spec, cfg)` turns an OpenAPI 3 document into a
`require()`-able skill — one JS function per operation:

```go
spec, err := openapi3.NewLoader().LoadFromFile("petstore.yaml")
pack, err := ext.OpenAPIPack(spec, ext.OpenAPIConfig{
    Headers: map[string]string{"Authorization": "Bearer " + apiKey},
})
caps = append(caps, agentloop.Capability{
    Name: pack.Name,
    Build: func(agentloop.BuildContext) ([]sandbox.Pack, error) {
        return []sandbox.Pack{pack}, nil
    },
})
```

Each generated function takes a single `params` object —
`params.<name>` for every path/query parameter, `params.body` for the
request body — rather than positional arguments, since operations vary
too much in parameter count for a positional signature to stay
readable. It returns `{status, body, headers}`: the same shape
`fetch()`/`require('http')` already return, body included as a raw
string (`JSON.parse` it yourself if the response is JSON).

**No new HTTP client.** Generated functions call the sandbox's own
internal `_http` primitive — the same one `require('http')` wraps —
not a client `OpenAPIPack` brings itself. That means every call a
generated function makes is policy-gated exactly like `fetch()` already
is (URL allowlist, private-network denial, all of it), and
`require(<skill name>)` is gated by that name like any other skill.
`_http` is used directly instead of going through `require('http')`
because it uniformly supports every HTTP method via one `method`
option, including `PATCH` — which the `http` module's fixed
`get`/`post`/`put`/`del` set has no wrapper for.

**`cfg.Headers` is the whole auth story** — a static header map (bearer
token, API key) applied to every request this skill's functions make.
OpenAPI `securitySchemes` are not interpreted; there's no OAuth flow, no
per-call credential resolution. This covers the common case (one API
key or token for the whole skill) and nothing beyond it.

**A short pointer in the prompt, TypeScript declarations behind
`skillGet()`.** A spec can have far more operations than are worth
inlining into every turn, so the generated `Pack`'s `Prompt` — always
visible, like every pack's — is one line: the API's name, its
operation count, and a reminder to call `skillGet`/`require`:

```
// --- Pet Store (skill: pet-store) ---
/** Pet Store — 2 operation(s). skillGet("pet-store") for the full TypeScript API; require("pet-store") to call it. */
```

`skillGet("pet-store")` returns the real API surface: an intro (title,
version, description, base URL) followed by one TypeScript ambient
declaration per operation — the same `declare function` style the core
packs' own `Prompt` fields already use, so a generated skill's API
reads the same way a built-in one does:

```
Pet Store (v1.0.0)

Base URL: https://api.petstore.example — 2 operation(s) below.
Every function takes one params object and returns { status: number; body: string; headers: Record<string, string> } — body is a raw string, JSON.parse it if the response is JSON.

/** Create a pet */
declare function createPet(params: { body: any; // JSON-serialized request body }): { status: number; body: string; headers: Record<string, string> };
/** Get a pet by ID */
declare function getPetById(params: { petId: string; verbose?: boolean; }): { status: number; body: string; headers: Record<string, string> };
```

A parameter is optional in the declaration (`verbose?:`) exactly when
OpenAPI marks it non-required; `params` itself is only optional
(`params?:`) when nothing inside it — no path parameter (always
required), no required query parameter, no required body — forces the
caller's hand.

A few things worth knowing before pointing this at a real spec:

- **Base URL** comes from `cfg.BaseURL`, or the spec's first `servers`
  entry if unset. `OpenAPIPack` errors if neither is present — a skill
  with nowhere to send requests is a config mistake, not something to
  silently install.
- **Required-ness is documented, not enforced.** A required path or
  query parameter left out of `params` isn't caught client-side; the
  request goes out and the server rejects it (or doesn't).
- **Function names come from `operationId`** (sanitized into a valid JS
  identifier) when the operation has one, otherwise derived from the
  method and path — `GET /pets/{petId}/photos` becomes
  `getPetsByPetIdPhotos`. Two operations that collide on the same
  derived name get a numeric suffix (`getPets`, `getPets2`, ...) rather
  than one silently overwriting the other.

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

## Distributed tracing (OpenTelemetry)

`Config.TracerProvider` takes a [`trace.TracerProvider`](https://pkg.go.dev/go.opentelemetry.io/otel/trace#TracerProvider).
Left unset, `Run` produces no spans — `resolveTracer` installs a no-op
tracer, so tracing costs a few allocations and nothing else until you wire
one in:

```go
loop := agentloop.New(agentloop.Config{
    // ...
    TracerProvider: myOTelSDKTracerProvider, // e.g. wired to an OTLP/Jaeger exporter
})
```

Only `go.opentelemetry.io/otel/trace` — the stable, SDK-free API package —
is an agentloop dependency. The SDK, exporter, and Jaeger (or Zipkin, or
whatever backend) wiring are entirely the application's to choose; agentloop
never imports `go.opentelemetry.io/otel/sdk` itself.

Every `Run` produces this span tree:

```
agentloop.run                    (one per Run call)
├── agentloop.sandbox_build      (SandboxBuilder.Build)
├── agentloop.turn                (one per LLM round-trip)
│   ├── agentloop.llm_call        (the Stream call)
│   └── agentloop.execute_js      (only when the model emitted JS this turn)
├── agentloop.turn
│   └── agentloop.llm_call
└── ...
```

`agentloop.run` carries `agentloop.session_id`, and on completion
`agentloop.status`, `agentloop.steps`, and aggregate token-count attributes.
`agentloop.turn` carries the iteration index; `agentloop.llm_call` carries
the model name and, on success, per-call token counts. Following OTel
convention, a span's status is only ever set to `Error` (with the error
recorded) on failure — a successful span is left `Unset`, not explicitly
marked `Ok`.

A JS execution error (the model's script threw) is recorded on
`agentloop.execute_js` alone, not on the enclosing `agentloop.turn` — the
turn itself didn't fail, it fed the error back to the model and kept going,
so only the span for the operation that actually errored is marked.

## Context management

`Config.ContextBudget`, `Config.Compactor`, and `Config.HistoryWindow` are
seams too, but they're covered on their own page: what fills the prompt, the
four bounds that keep it inside a model's window, and
`agentloop.ProfileFor(window)`, which derives all of them from one number.
See [Fitting a session in the context
window](/concepts/context/) — it matters most if you're pointing agentloop
at a locally hosted model, where the defaults are wrong by an order of
magnitude.

## Evaluating agent quality

`eval.Service` runs a `Suite` of `Case`s — each an input plus judge
criteria — through an `agentloop.Loop`, has a judge `llm.Client` score
every response 0–10, and persists the run:

```go
store := evalmem.New() // or your own eval.Store
svc := eval.NewService(store, loop, judgeClient, nil) // last arg: optional *redact.Redactor

suite, _ := svc.CreateSuite(ctx, "arithmetic", "" /* judge model, empty = client default */)
svc.AddCase(ctx, suite.ID, "sums", "What's 2+2?", "must say 4", 7, nil)

run, err := svc.RunSuite(ctx, suite.ID)
// run.Summary.Passed / .Failed; run.Results[i].{Response, Score, Rationale, Passed}
```

`agentloop.Loop`'s `Run(ctx, RunRequest) (RunResult, error)` already has
exactly the shape an eval-harness runner needs — `RunResult.FinalText` is
the response to judge — so `Service` takes a `Loop` directly rather than a
separate runner interface. Each case gets its own fresh, never-reused
session ID: `agentloop.SessionStore.Get` is documented to auto-create a
shell for an unknown ID, so `RunSuite` needs no separate
session-provisioning step of its own.

A `Case` carries either a single `Criteria` string + `PassThreshold` (the
legacy path), or a per-criterion `CriteriaItems` rubric — when set, the
judge scores each item separately and the case passes only when **every**
item clears its own `MinScore`, with the overall `Score` reported as the
average across items. A case that errors (agent failure or judge failure)
records the error inline on that case's result rather than aborting the
suite — `RunSuite` always finishes and returns a `Run`.

`evalmem.InMemoryStore` implements `eval.Store` for local dev and CI; back
a real deployment with whatever persistence you already have.

## Redacting secrets from observability

`redact.Redactor` holds a small ordered list of (secret value, placeholder)
pairs and strips every occurrence out of text or bytes:

```go
redactor := redact.FromSecrets(map[string]string{
    "api_key": theActualKeyValue,
})
```

A nil `*Redactor` is a safe pass-through on every method, so this is
entirely opt-in — nothing changes until you build one and hand it to one
of the two places that take it:

- **`Config.Redactor`** — applied to every `RunEvent` (`Content` and
  `Args`, before `OnEvent` sees it), `RunResult.FinalText`, persisted
  `RunStep.Content`, and error messages recorded on a span. The
  defense-in-depth case: a script does `log(secret("API_KEY"))`, or a
  `fetch()` response happens to echo a credential back, and that value
  would otherwise reach whatever `OnEvent` forwards to, the persisted
  trace, or a trace backend.
- **`eval.NewService`'s 4th argument** — applied to the agent's response
  before it reaches the judge's prompt (a third-party LLM call, see
  [Evaluating agent quality](#evaluating-agent-quality) above) or gets
  persisted in `CaseResult.Response`. An eval case exercises the same
  capabilities production traffic does, so without this a case that
  happens to trigger a credential-bearing response would send it on to
  the judge and store it in the run.

**One trade-off in `Config.Redactor`:** setting it suppresses
`"response_chunk"` events — live token-by-token streaming. A secret value
can split across two chunk boundaries with neither chunk containing the
whole substring to redact against, so per-chunk redaction can't be made
safe. The complete, redacted text still arrives via the terminal
`"response"` event; only the incremental "typing" effect is lost.

## Next

[Reference](/reference/) has the full list of known limitations —
worth reading before extending agentloop into a production path, since
several of them (no durable execution, no built-in cost ceiling) are things
an application built on top has to supply itself.
