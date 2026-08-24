---
title: Packages
description: What each of agentloop's five Go packages is responsible for.
sidebar:
  order: 2
---

agentloop is five packages. Each is usable on its own — a project can take
the sandbox and not the loop, or the loop with a hand-rolled store.

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
your own) per call — see [Policy](/agentloop/extending/#policy).

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

## Next

[Extending agentloop](/agentloop/extending/) covers the seams — capabilities,
`SandboxBuilder`, stores, and policy — in more depth.
