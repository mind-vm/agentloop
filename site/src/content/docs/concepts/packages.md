---
title: Packages
description: What each of agentloop's Go packages is responsible for.
sidebar:
  order: 2
---

Each package is usable on its own — a project can take the sandbox and not
the loop, or the loop with a hand-rolled store.

## `agentloop`

The reasoning loop itself. `Loop.Run` drives the rehydrate → prompt →
execute → thread cycle for one user message: load session history, build the
prompt, get the model's script, execute it in a fresh sandbox, thread the
return value into the next turn, repeat until `answer()` or
`MaxIterations`.

The root package also defines the seams the rest of this section covers:
`Capability`, `SandboxBuilder`, `SessionStore`, `StepStore`.

## `sandbox`

The goja-based JS executor and its built-in packs:

| Pack | Provides |
|---|---|
| `require` | CommonJS-style `require()` resolver — always on |
| `http` | `require('http')` verbs |
| `fetch` | `fetch()` + `htmlToMarkdown()` |
| `markdown` | `require('markdown')` — structured markdown parsing |
| `ai` | `ai()` / `aiJSON()` sub-calls against the configured LLM |
| `help` | `help()` — reads every pack's registered help entries |
| skill discovery | `skillList()` / `skillGet()` — introspects the union of loaded packs |

A `PolicyChecker` seam gates side-effecting primitives (`fetch`, `ai`, or
your own) per call — see [Policy](/extending/#policy).

## `llm`

A small `Client` interface plus an OpenAI-wire-compatible implementation
(`llm.NewOpenAI`) that works with OpenAI itself,
[OpenRouter](https://openrouter.ai), [EdenAI](https://edenai.co), Groq,
together.ai, a local vLLM/Ollama server, or any other gateway that speaks
the same `/chat/completions` protocol.

## `agentloopmem`

In-memory `SessionStore` / `StepStore` for local dev, one-shot CLIs, and eval
suites. **Not for production traffic** — no durability, no cross-process
visibility. Its own `contract_test.go` is the worked example for testing a
real implementation.

## `agentlooptest`

`StepStoreContract`, a reusable conformance test harness. Point it at your
own `StepStore` implementation (Postgres, SQLite, whatever) to hold it to
the same behavioural guarantees the loop relies on.

## `ext`

Optional `sandbox.Pack`s that are generically useful but don't belong in the
core sandbox package: `EmailPack` (`sendEmail`), `SecretPack` (`secret`),
`SearchPack` (`documentSearch`), `StoresPack` (`stores.list`/`stores.read`),
and `OpenAPIPack`, which generates a whole `require()`-able skill — one JS
function per operation — from an OpenAPI 3 document. Not part of
`DefaultCapabilities` — wire in the ones you need. See [Extending → Optional
extension packs](/extending/#optional-extension-packs) and
[Extending → Generating a skill from an OpenAPI
spec](/extending/#generating-a-skill-from-an-openapi-spec).

## `browser`

A `browser` global that drives a real browser: `goto` / `click` / `type` /
`text` against CSS selectors, plus Set-of-Marks — `mark()` numbers every
visible interactive element and returns `{id, tag, role, name, x, y, w, h}`
for each, so the model acts by id (`clickMark(3)`) instead of inventing a
selector or a pixel coordinate. Optional vision (`ask` / `askMarks`) is a
callback, so the package pulls in no model client.

Everything here talks to a ten-method `Driver`. The chromedp
implementation is a **separate module**,
`github.com/mind-vm/agentloop/browser/chrome`, so that chromedp and cdproto
stay off the dependency list of every deployment that never opens a
browser. Navigation is policy-gated under tool name `browser` — the same
URL rules `DefaultPolicy` applies to `fetch`.

```bash
go get github.com/mind-vm/agentloop/browser/chrome
```

## `pool`

`SandboxPool`, a `SandboxBuilder` decorator that reuses one sandbox per
session across every `Run` instead of building a fresh one — a whole
`goja.Runtime` plus every pack's registration — on every message. Wraps any
other `SandboxBuilder`. See [Extending → Session-scoped sandbox
reuse](/extending/#session-scoped-sandbox-reuse) for the
correctness problem it has to solve to do that safely.

## `eval`

An LLM-judged eval harness: a `Suite` of `Case`s (input + judge criteria),
each dispatched through an `agentloop.Loop` and scored 0–10 by a judge
`llm.Client`. `Loop`'s own `Run` method already has exactly the shape a
runner needs, so `Service` takes one directly — no adapter interface. See
[Evaluating agent quality](/extending/#evaluating-agent-quality).

## `evalmem`

In-memory `eval.Store` for local dev and CI — the same role `agentloopmem`
plays for `SessionStore`/`StepStore`. Not for production traffic.

## `redact`

`Redactor` holds a small ordered (secret value, placeholder) list and
strips every occurrence out of text or bytes. A nil `*Redactor` is a safe
pass-through everywhere it's used, so it's entirely opt-in — build one
with `redact.FromSecrets(...)` and hand it to `Config.Redactor` or
`eval.NewService`. See [Redacting secrets from
observability](/extending/#redacting-secrets-from-observability).

## Next

[Extending agentloop](/extending/) covers the seams — capabilities,
`SandboxBuilder`, stores, and policy — in more depth.
