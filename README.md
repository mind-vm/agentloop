# agentloop

A small Go engine for LLM agents that think by writing JavaScript instead of
calling named tools.

Each turn the model emits one fenced ` ```javascript ` block defining
`function run(args) { ... }`. agentloop executes it in a sandboxed
[goja](https://github.com/dop251/goja) runtime and threads its return value
into the next turn as `args` — full fidelity, server-side, never
re-serialised into the prompt. The model sees only its own `log()` output
plus a compact structural digest of what it returned. A run finishes when
the script calls `answer(result)`.

This return-threading design keeps the prompt small even for many-step
runs: stale code is elided from history (only the most recent turn's script
is kept verbatim) and large data never appears in the context window twice.

## Packages

- **`agentloop`** — the reasoning loop itself: `Loop.Run` drives the
  rehydrate → prompt → execute → thread cycle for one user message.
- **`sandbox`** — the goja-based JS executor and its built-in packs:
  `require`, `http`, `fetch` + `htmlToMarkdown`, `markdown` (structured
  parsing), `ai`/`aiJSON`, `help`, and skill discovery. A `PolicyChecker`
  seam gates side-effecting primitives.
- **`llm`** — a small `Client` interface plus an OpenAI-wire-compatible
  implementation that works with OpenAI itself, [OpenRouter](https://openrouter.ai),
  [EdenAI](https://edenai.co), Groq, together.ai, a local vLLM/Ollama
  server, or any other gateway that speaks the same `/chat/completions`
  protocol. Rate limits (429) and upstream failures (5xx) are retried
  with exponential backoff plus jitter, honouring a `Retry-After` header
  when the upstream sends one — see `Config.MaxRetries`.
- **`agentloopmem`** — in-memory `SessionStore` / `StepStore` for local
  dev, one-shot CLIs, and eval suites. Not for production traffic (no
  durability, no cross-process visibility).
- **`agentlooptest`** — `StepStoreContract`, a reusable conformance test
  harness. Point it at your own `StepStore` implementation (Postgres,
  SQLite, whatever) to hold it to the same behavioural guarantees the
  loop relies on — see `agentloopmem`'s own `contract_test.go` for the
  worked example.
- **`ext`** — optional `sandbox.Pack`s that are generically useful but
  don't belong in the core sandbox package: `EmailPack` (`sendEmail`),
  `SecretPack` (`secret`), `SearchPack` (`documentSearch`), `StoresPack`
  (`stores.list`/`stores.read`), and `OpenAPIPack`, which generates a
  `require()`-able skill — one JS function per operation — from an
  OpenAPI 3 document. Each takes a callback, small interface, or (for
  `OpenAPIPack`) a parsed spec, same decoupling the core packs use, so
  this package stays free of any mail/secrets/search-backend/HTTP-client
  dependency of its own.
- **`pool`** — `SandboxPool`, a `SandboxBuilder` that reuses one
  long-lived sandbox per session across every `Run` instead of paying
  `goja.New()` + pack-registration cost on every message. Wraps any
  other `SandboxBuilder`; see [Extending](#extending) for the
  correctness issue it has to solve to do that safely.
- **`projectctx`** — discovers project instruction files (`AGENTS.md`)
  from a checkout on disk and renders them into a prompt section. A
  layered capability: nothing in the core imports it, and an application
  opts in by passing `projectctx.Render(docs)` as `RunRequest.Context`.
- **`skills`** — discovers `SKILL.md` files in a project and turns each
  into a `Capability` the model can find with `skillList()` and read with
  `skillGet(name)`. Bodies stay out of the prompt until fetched, so a
  project can carry many detailed skills at the cost of one catalog line
  each.
- **`eval`** — an LLM-judged eval harness: a `Suite` of `Case`s (input +
  judge criteria), each dispatched through an `agentloop.Loop` and
  scored 0–10 by a judge `llm.Client`. `agentloop.Loop` and
  `agentloop.RunRequest`/`RunResult` already have exactly the shape the
  harness needs, so `Service` takes a `Loop` directly — no adapter
  interface required.
- **`evalmem`** — in-memory `eval.Store` for local dev and CI, same
  role `agentloopmem` plays for `SessionStore`/`StepStore`. Not for
  production traffic.
- **`redact`** — `Redactor`, a small ordered (value, placeholder) list
  that strips known secret values out of text or bytes; a nil
  `*Redactor` is a safe pass-through everywhere. `Config.Redactor` and
  `eval.NewService`'s redactor parameter both take one — see
  [Extending](#extending) for what it's wired into.

## Quickstart

```go
client, err := llm.NewOpenAI(llm.ConfigFromEnv())
if err != nil {
    log.Fatal(err)
}

caps := agentloop.DefaultCapabilities(client, "")
loop := agentloop.New(agentloop.Config{
    LLM:            client,
    Sessions:       mySessionStore, // implements agentloop.SessionStore
    Steps:          myStepStore,    // implements agentloop.StepStore
    SandboxBuilder: &agentloop.DefaultSandboxBuilder{Capabilities: caps},
})

result, err := loop.Run(ctx, agentloop.RunRequest{
    SessionID: "session-1",
    Message:   "What's 12 * 7? Reply with just the number.",
})
```

See [`examples/cli`](examples/cli) for a complete runnable program with
in-memory stores:

```sh
export OPENAI_API_KEY=sk-...
go run ./examples/cli "What's 12 * 7? Reply with just the number."
```

To point at a different OpenAI-wire-compatible gateway, set `OPENAI_BASE_URL`
and `OPENAI_CHAT_MODEL` to match it, e.g.:

```sh
# OpenRouter
export OPENAI_BASE_URL=https://openrouter.ai/api/v1
export OPENAI_CHAT_MODEL=openai/gpt-4o-mini

# EdenAI
export OPENAI_BASE_URL=https://api.edenai.run/v3
export OPENAI_CHAT_MODEL=google/gemini-2.5-flash
```

A failed request is retried up to three times by default — set
`OPENAI_MAX_RETRIES` (or `llm.Config.MaxRetries`) to tune that, or to
`0` to turn retrying off entirely.

## Extending

- **Custom capabilities** — a `Capability` is a name plus a `Build` func
  returning `[]sandbox.Pack`. Application-specific dependencies (a
  database handle, an accumulator, a broadcaster) should be closed over
  when the `Capability` is constructed rather than threaded through
  `BuildContext` — see `DefaultCapabilities` in `defaults.go` for the
  pattern.
- **Custom sandbox composition** — implement `agentloop.SandboxBuilder`
  yourself (its one method, `Build`) when your capability set varies per
  scope or session, e.g. pulled from a database per tenant.
- **Custom stores** — `SessionStore` and `StepStore` are small
  interfaces; back them with whatever persistence you already have.
  `agentloopmem` ships in-memory implementations to start from, and
  `agentlooptest.StepStoreContract` is a conformance harness to run
  against your own implementation as an acceptance gate.
- **History compaction** — by default a session's history is truncated
  to `Config.HistoryWindow` steps and the overflow is gone. Set
  `Config.Compactor` to fold that older stretch into a summary instead.
  `agentloop.SummarizingCompactor` is the batteries-included
  implementation: past `Trigger` messages it summarizes everything
  except the most recent `Keep` via an `llm.Client` (point it at a small,
  cheap model), and it never splits a `run()` block from the execution
  result it produced. A session long enough to need compaction can be
  long enough to overflow the context of the call meant to fix that, so
  the transcript is summarized in **chunks** that each fit one call and
  the partial summaries are folded together until one remains —
  `SummarizingCompactor.Budget` (a `ContextBudget`, sized to the
  *summarizer's* window, not the loop's) is what sizes a chunk, and
  `MergePrompt` overrides the fold instruction. Nothing is discarded to
  make the transcript fit: a stretch a single-call summarizer would have
  elided is summarized like any other, and the only surviving elision is
  for one individual message too large for a whole call on its own. A
  242 KB transcript takes one call against a 128k window and 21 against
  a 4k one, where before it was a single oversized call the small model
  would reject. Raising `HistoryWindow` alongside it is how a
  session gets long-term memory — the prompt stays bounded by the
  compactor rather than by the window. Each compaction is persisted as
  a `summary` checkpoint step, so the next `Run` rehydrates from it
  rather than summarizing the same turns again — a long session pays for
  a summarization only when it has grown past `Trigger` since the last
  one, not on every `Run`. The covered steps stay in the trace; only
  what is replayed to the model changes. Compaction is best-effort — a
  failure emits a `warning` event and the run proceeds on the
  uncompacted history.
- **Token budget** — `HistoryWindow` and the compactor both count
  *messages*, which says nothing about how many *tokens* those messages
  occupy: the same 60-turn history is trivial against a frontier model
  and a hard overflow against a locally hosted one. Set
  `Config.ContextBudget` to bound each turn's prompt in tokens instead.
  `MaxTokens` is the window the model is actually served with (for a
  local runtime, the server's configured context size — llama.cpp's
  `--ctx-size`, Ollama's `num_ctx` — not the figure on the model card),
  and `Reserve` is what's held back for the completion (default
  `MaxTokens/8`, clamped to `[512, 4096]`); the reserve is also sent as
  the request's output cap, so a long answer can't overrun the room made
  for it. The zero value is disabled, so adding a budget changes nothing
  until `MaxTokens` is set.

  The trim runs immediately before each request — after compaction,
  after stale-code elision, and after this run's own turns have grown
  the history, which is the only point where the real size is knowable.
  The system prompt and the current turn are never dropped; the oldest
  conversational turns go first, an execution result is never stranded
  from the `run()` block that produced it, and a marker tells the model
  how many turns are gone so it doesn't quietly assume what they
  contained. Every trim emits a `budget_trimmed` event and every turn
  span carries `agentloop.budget.estimated_tokens` against
  `agentloop.budget.allowance`, so headroom is observable before a
  session starts losing history. Sizing uses a crude bytes/4 estimate by
  default — supply `ContextBudget.Estimate` to plug in a real tokenizer
  and fill the window tighter.

  This is a backstop, not a replacement for compaction: the compactor
  preserves meaning, the budget only guarantees the request is sendable.
  Configure both.
- **Project instructions** — `projectctx.Load(cwd)` walks from the
  repository root down to `cwd` collecting `AGENTS.md` files (general
  first, most specific last), and `projectctx.Render(docs)` turns them
  into a `## Project instructions` section to pass as
  `RunRequest.Context` — see `examples/cli`. Repository files must
  resolve inside the root even after symlinks and are read through an
  `os.Root`, so a symlinked `AGENTS.md` can't pull in a file from
  outside the project. `Load` returns any docs that read cleanly
  *alongside* its error, so one unreadable file warns rather than
  denying the run its remaining context. Unlike the CLI convention this
  came from, no user-global file is read unless you ask for one
  (`Loader{GlobalDir: ...}`) — a library shouldn't reach into `$HOME` on
  its own.

  `Render` inlines every file in full, which is a *standing* cost —
  these ride in the prompt of every turn of every run, whether or not
  the turn touches anything they cover. `projectctx.RenderCatalog(docs)`
  plus `projectctx.Capabilities(docs)` is the retrieval alternative: the
  prompt carries each file's opening section (`Loader.InlineBytes`,
  4 KB by default, cut at a paragraph boundary and never left dangling
  on a heading), and the model pulls the rest with `projectGet(name)`
  when it needs it — `projectList()` reports each file's size and
  whether the prompt already has all of it. A typical page-or-two
  AGENTS.md rides along whole and nothing changes; a 36 KB one drops
  from ~9,300 to ~1,200 prompt tokens per turn, with nothing lost —
  unlike `MaxBytes` truncation, the tail stays reachable. The two are
  alternatives, not a pair: `RenderCatalog` writes pointers to
  `projectGet`, so wire the capabilities alongside it. See
  `examples/cli`.
- **Project skills** — `skills.Load(cwd)` reads
  `<root>/.agentloop/skills/<name>/SKILL.md` (directory configurable) and
  `skills.Capabilities(sk)` turns them into capabilities to append to
  the slice a `DefaultSandboxBuilder` gets — see `examples/cli`. Each
  skill contributes ONE line to the system prompt (name, description,
  and how to fetch it); the instructions themselves arrive only when the
  model calls `skillGet(name)`, which is what keeps a big skill library
  cheap. One capability per skill, so
  `DefaultSandboxBuilder.EnabledCapabilities` can switch a skill on per
  session. Names that would shadow a built-in (`fetch`, `http`, `require`,
  …) are rejected: pack help entries merge by name, so such a skill would
  otherwise replace that primitive's own documentation.
- **Policy** — implement `sandbox.PolicyChecker` to gate side-effecting
  primitives (`fetch`, `ai`, or your own) per call. `sandbox.DefaultPolicy`
  is a conservative default (deny side effects, block private-network
  fetches); `sandbox.AllowAll` is the explicit fail-open escape hatch.
- **Optional extension packs** — `ext.EmailPack`, `ext.SecretPack`,
  `ext.SearchPack`, and `ext.StoresPack` are common but not universal, so
  they live outside `sandbox` and aren't in `DefaultCapabilities`. Wrap
  one in a `Capability` that closes over your own backend and add it to
  the slice passed to `DefaultSandboxBuilder`. `sendEmail` and `secret`
  are already in `DefaultPolicy`'s side-effect list, so they're denied
  until granted via `DefaultPolicy.AllowTools`.
- **Generate a skill from an OpenAPI spec** — `ext.OpenAPIPack(spec, cfg)`
  turns an OpenAPI 3 document into a `require()`-able skill: one JS
  function per operation, named after its `operationId` (or derived from
  the method + path when it has none), taking a single `params` object
  (`params.<name>` per path/query parameter, `params.body` for the
  request body) and returning `{status, body, headers}` — the same shape
  `fetch()`/`require('http')` already return. Generated functions call
  the sandbox's own internal HTTP primitive, not a new client, so every
  call is still policy-gated exactly like `fetch()` already is, and
  `require(<skill name>)` is gated by that name like any other skill.
  `cfg.Headers` is the whole auth story (a static header map — bearer
  token, API key — applied to every call); OpenAPI `securitySchemes`
  aren't interpreted. The generated `Pack`'s `Prompt` is one short line
  (API name, operation count, a pointer to `skillGet`/`require`) rather
  than the full surface — a spec can have far more operations than are
  worth inlining into every turn. The full docs, returned by
  `skillGet(<skill name>)` on demand, are TypeScript ambient
  declarations — `declare function getPetById(params: { petId: string;
  verbose?: boolean }): { status: number; body: string; headers:
  Record<string, string> };` — in the same style the core packs' own
  `Prompt` fields already use, so a generated skill's API reads the
  same way a built-in one does:

  ```go
  spec, err := openapi3.NewLoader().LoadFromFile("petstore.yaml")
  pack, err := ext.OpenAPIPack(spec, ext.OpenAPIConfig{
      Headers: map[string]string{"Authorization": "Bearer " + apiKey},
  })
  caps = append(caps, agentloop.Capability{
      Name: pack.Name,
      Build: func(agentloop.BuildContext) ([]sandbox.Pack, error) { return []sandbox.Pack{pack}, nil },
  })
  ```

- **Session-scoped sandbox reuse** — `agentloop.New`'s default is a
  fresh sandbox per `Run` (`sandbox.New()` builds a whole `goja.Runtime`
  and re-registers every pack from scratch every time). Wrap your
  `SandboxBuilder` in `pool.New` to reuse one sandbox per session across
  every `Run` instead:

  ```go
  builder := pool.New(&agentloop.DefaultSandboxBuilder{Capabilities: caps}, pool.Options{
      IdleTimeout: 30 * time.Minute, // evict a session's sandbox after this much idle time
  })
  defer builder.Close()

  loop := agentloop.New(agentloop.Config{
      // ...
      SandboxBuilder: builder,
  })
  ```

  This isn't just a cache wrapper — a `Capability`'s `Build` closes over
  `BuildContext.Ctx` once (see `DefaultCapabilities` in `defaults.go`),
  so a naively cached sandbox would keep using the *first* Run's
  context — including its cancellation — forever. `pool.SandboxPool`
  gives the delegate builder a swappable context instead, and swaps in
  each Run's real context before handing the sandbox back. It also
  serializes concurrent Runs for one session: a `goja.Runtime` isn't
  safe for concurrent use, so a second Run for a session already in
  flight blocks until the first releases the sandbox, rather than
  racing on it. See the `pool` package doc comment for both mechanisms
  in detail.
- **Distributed tracing (OpenTelemetry)** — `Config.TracerProvider`
  takes a [`trace.TracerProvider`](https://pkg.go.dev/go.opentelemetry.io/otel/trace#TracerProvider).
  Left unset, `Run` produces no spans (a no-op tracer, a few allocations
  and nothing else); set it to an OTel SDK `TracerProvider` — e.g.
  configured with an OTLP exporter pointed at a Jaeger collector — and
  every `Run` produces a span tree: one root `agentloop.run` span, with
  `agentloop.sandbox_build` and one `agentloop.turn` per LLM round-trip
  as children, each turn's own `agentloop.llm_call` and (when the model
  emits JS) `agentloop.execute_js` nested under it. Only
  `go.opentelemetry.io/otel/trace` (the stable, SDK-free API package) is
  an agentloop dependency — the SDK, exporter, and Jaeger wiring are
  entirely the application's to choose:

  ```go
  loop := agentloop.New(agentloop.Config{
      // ...
      TracerProvider: myOTelSDKTracerProvider, // e.g. wired to an OTLP/Jaeger exporter
  })
  ```

A capability whose `Build` fails is logged and skipped (a `warning`
sandbox event, not an aborted session) — one flaky capability shouldn't
deny the user their turn.

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
// run.Summary.Passed / .Failed; run.Results[i].{Response,Score,Rationale,Passed}
```

`agentloop.Loop`'s `Run(ctx, RunRequest) (RunResult, error)` already has
exactly the shape an eval-harness runner needs (`RunResult.FinalText` is
the response to judge), so `Service` takes a `Loop` directly rather than
some separate runner interface. Each case gets its own fresh, never
reused session ID — `agentloop.SessionStore.Get` is documented to
auto-create a shell for an unknown ID, so `RunSuite` needs no separate
session-provisioning step.

A `Case` can carry either a single `Criteria` string + `PassThreshold`
(the legacy path), or a per-criterion `CriteriaItems` rubric — when
set, the judge scores each item separately and the case passes only
when *every* item clears its own `MinScore`, with the overall `Score`
reported as the average. A case that errors (agent failure or judge
failure) records the error inline on that case's result rather than
aborting the suite — `RunSuite` always finishes and returns a `Run`.

`evalmem.InMemoryStore` implements `eval.Store` for local dev and CI;
back a real deployment with whatever persistence you already have.

## Redacting secrets from observability

`redact.Redactor` holds a small ordered list of (secret value,
placeholder) pairs and strips every occurrence out of text or bytes —
build one with `redact.FromSecrets(map[string]string{"api_key": key, ...})`
over whatever values a session's capabilities can return. A nil
`*Redactor` is a safe pass-through everywhere it's used, so this is
entirely opt-in.

Two places take one:

- **`Config.Redactor`** — applied to every RunEvent (`Content` and
  `Args`, before `OnEvent` sees it), `RunResult.FinalText`, persisted
  `RunStep.Content`, and error messages recorded on a span. The
  defense-in-depth case: a script does `log(secret("API_KEY"))`, or a
  `fetch()` response happens to echo a credential back, and that value
  would otherwise land in whatever `OnEvent` forwards to, the
  persisted trace, or a trace backend. One trade-off: setting this
  suppresses `"response_chunk"` events (live token-by-token
  streaming), because a secret can split across two chunk boundaries
  with neither chunk containing the whole value to redact against —
  the complete, redacted text still arrives via the terminal
  `"response"` event.
- **`eval.NewService`'s 4th argument** — applied to the agent's
  response before it reaches the judge's prompt (a third-party LLM
  call) or gets persisted in `CaseResult.Response`. An eval case
  exercises the same capabilities production traffic does, so without
  this a case that happens to trigger a credential-bearing response
  would send it on to the judge and store it in the run.

## Known limitations

This is a synchronous, single-process design, not a durable workflow
engine:

- **No durable execution.** A `Run` call lives in one goroutine; a
  process restart mid-run kills it (steps already persisted are fine,
  but nothing resumes automatically).
- **Everything inside a turn is synchronous**, including I/O — no
  `Promise`/`async`/`.then()` in the sandbox (goja parses them but has
  no event loop, so continuations silently never run; the system prompt
  warns the model off this).
- **Process-level isolation only.** goja is memory-safe and
  interruptible, but sandboxes share the host process's heap/CPU — no
  per-run resource quota.
- **Unbounded args carry.** Nothing caps the size of the server-side
  state threaded between turns (`RunResult.DataBytesCarried` gives you
  the observability to notice, not a limit).
- **No built-in cost ceiling.** `MaxIterations` bounds turns and token
  usage is tracked, but there's no per-run or per-tenant token budget.

None of these are architectural dead ends — they're the natural next
layer (suspend/resume, batched I/O primitives, resource quotas, budget
enforcement) to add on top if/when you need them.

## License

MIT — see [LICENSE](LICENSE).
