---
title: Running against a local model
description: Point the client at Ollama, llama.cpp, vLLM, or LM Studio; size every context setting to the served window; and the four things that bite on local hardware.
sidebar:
  order: 2
---

The wire protocol is the easy part — `llm.NewOpenAI` already talks to any
server that speaks OpenAI's `/chat/completions`, which every common local
runtime does. What needs attention is everything downstream of the context
window, plus a couple of timeouts sized for a hosted API on someone else's
GPUs.

## 1. Point the client at the server

| Runtime | Base URL | `OPENAI_CHAT_MODEL` |
|---|---|---|
| Ollama | `http://localhost:11434/v1` | the tag you pulled |
| llama.cpp (`llama-server`) | `http://localhost:8080/v1` | usually ignored; any value works |
| LM Studio | `http://localhost:1234/v1` | the loaded model's id |
| vLLM | `http://localhost:8000/v1` | the `--model` you launched with |

```sh
export OPENAI_BASE_URL=http://localhost:11434/v1
export OPENAI_CHAT_MODEL=your-model-tag
export OPENAI_API_KEY=unused
```

:::caution[The API key is not optional, even locally]
`llm.NewOpenAI` returns an error when `APIKey` is empty — the caller decides
what a missing key means, so the client refuses to build one that can't
authenticate. A local server ignores the value, but it still has to be
**non-empty**. Set it to any placeholder.
:::

Ports vary with how you launched the server; take the ones above as the
usual defaults, not as guarantees.

Check it before writing any code — [`agentloop
doctor`](/cli/#pointing-at-another-provider) makes one real request through
the same path a run uses, including the fallback for gateways that refuse to
stream:

```sh
agentloop doctor
```

## 2. Size everything to the served window

This is the step that matters. agentloop's defaults assume a frontier model,
and against a local window they are wrong by an order of magnitude — see
[Fitting a session in the context window](/concepts/context/) for what
they are and why.

`agentloop.ProfileFor(window)` derives all of them from one number:

```go
p := agentloop.ProfileFor(8192)
p.Apply(&cfg) // ContextBudget, HistoryWindow, and the compactor's knobs
```

Use the window the server is **actually serving**, not the figure on the
model card. A model that supports 128k loaded with a 4k KV cache serves 4k,
and a profile built on the wrong number fits nothing:

| Runtime | Where the window comes from |
|---|---|
| Ollama | the `num_ctx` parameter (Modelfile or per-request options) |
| llama.cpp | `--ctx-size` / `-c` on `llama-server` |
| vLLM | `--max-model-len` |
| LM Studio | the context length set for the loaded model |

Most of these log the resolved context size at startup — read it there
rather than assuming.

## 3. Give it time

Two timeouts are sized for a hosted API and will both bite on local
hardware:

- **`Config.RunTimeout`** (default 5 minutes) covers a whole `Run` —
  *every* turn of it, not one request. A ten-turn run on a CPU-bound model
  will pass it.
- **The HTTP client's own timeout** (default 5 minutes) covers one request.

Raise both:

```go
cfg.RunTimeout = 30 * time.Minute

client, err := llm.NewOpenAI(llm.Config{
    APIKey:     "unused",
    BaseURL:    "http://localhost:11434/v1",
    ChatModel:  "your-model-tag",
    HTTPClient: &http.Client{Timeout: 10 * time.Minute},
})
```

Worth knowing too: a local server that runs out of memory answers `500`,
which the client treats as retryable and retries three times with backoff —
several minutes spent failing the same way. `OPENAI_MAX_RETRIES=1` (or
`llm.Config.MaxRetries`) makes that fail faster while you're finding the
right model size.

## 4. Trim the tool surface

`DefaultCapabilities` costs about 1,310 prompt tokens of `declare` lines on
every turn. Against 128k that's noise; against 4k it is a third of the
allowance before the conversation starts.

`EnabledCapabilities` is the per-session allowlist:

```go
enabled := []string{"fetch", "markdown"}
builder := &agentloop.DefaultSandboxBuilder{
    Capabilities:        caps,
    EnabledCapabilities: &enabled,
    MaxLogBytes:         p.MaxLogBytes,
}
```

Capabilities marked `AlwaysOn` — `require` — are installed regardless.

## Putting it together

```go
package main

import (
    "context"
    "fmt"
    "log"
    "net/http"
    "time"

    "github.com/mind-vm/agentloop"
    "github.com/mind-vm/agentloop/agentloopmem"
    "github.com/mind-vm/agentloop/llm"
)

func main() {
    // The window the server is serving, not the model card's.
    const window = 8192

    client, err := llm.NewOpenAI(llm.Config{
        APIKey:     "unused", // non-empty, but ignored by a local server
        BaseURL:    "http://localhost:11434/v1",
        ChatModel:  "your-model-tag",
        HTTPClient: &http.Client{Timeout: 10 * time.Minute},
        MaxRetries: 1,
    })
    if err != nil {
        log.Fatal(err)
    }

    p := agentloop.ProfileFor(window)

    enabled := []string{"fetch", "markdown"}
    cfg := agentloop.Config{
        LLM:      client,
        Sessions: agentloopmem.NewSessionStore(nil),
        Steps:    agentloopmem.NewStepStore(),
        SandboxBuilder: &agentloop.DefaultSandboxBuilder{
            Capabilities:        agentloop.DefaultCapabilities(client, ""),
            EnabledCapabilities: &enabled,
            MaxLogBytes:         p.MaxLogBytes,
        },
        // The summarizer runs on the same server here, so it gets the
        // same window — that is what makes compaction chunk to fit.
        Compactor:  &agentloop.SummarizingCompactor{LLM: client},
        RunTimeout: 30 * time.Minute,
    }
    p.Apply(&cfg)

    result, err := agentloop.New(cfg).Run(context.Background(), agentloop.RunRequest{
        SessionID: "local-1",
        Message:   "What's 12 * 7? Reply with just the number.",
        OnEvent: func(e agentloop.RunEvent) {
            // The two events worth watching on a small window.
            if e.Type == "budget_trimmed" || e.Type == "warning" {
                log.Printf("%s: %v %s", e.Type, e.Args, e.Content)
            }
        },
    })
    if err != nil {
        log.Fatal(err)
    }
    fmt.Println(result.FinalText)
}
```

## What to watch

- **`budget_trimmed` events** mean the prompt hit the ceiling and the oldest
  turns were dropped. Occasional is fine; every turn from the first means
  the window is too small for the capability set, not for the conversation.
- **Turn spans** carry `agentloop.budget.estimated_tokens` against
  `agentloop.budget.allowance`, so headroom is visible before history starts
  disappearing. See [Distributed
  tracing](/extending/#distributed-tracing-opentelemetry).
- **Token counts may read zero.** `RunResult.Tokens` comes from the server's
  `usage` field; a server that omits it reports nothing. The context budget
  doesn't depend on this — it estimates the prompt itself — so budgeting
  still works, only the accounting is blank.

## Using the CLI against a local server

[`agentloop`](/cli/) takes the same `OPENAI_BASE_URL` /
`OPENAI_CHAT_MODEL` / `OPENAI_API_KEY` environment, and `--context-window`
is step 2 as a flag — the same one number, expanded the same way:

```sh
agentloop --context-window 8192 --timeout 30m "..."
```

That single flag sets all of it: the context budget, the history window,
the compactor's message counts, `log()` output, and how much of each
`AGENTS.md` goes in the prompt. It also installs a
`SummarizingCompactor` on the same client, with the same window as its own
budget — so compaction chunks to fit rather than sending one call the
server rejects. Pass the window the server is **serving**, per the table
above.

Two things follow from setting it, both of which the Go API leaves to you:

- **Project instructions are excerpted, not inlined whole.** Each
  `AGENTS.md` goes in down to the profile's inline size, and `projectGet(name)`
  is offered for the rest. This matters more than it looks: the budget
  trimmer never drops the system prompt, so an unbounded instruction file on
  a small window is not a prompt that gets trimmed — it is one that cannot
  be sent.
- **`--history-window` still wins where it overlaps.** The profile is
  applied first and an explicit flag overrides it, so
  `--context-window 8192 --history-window 40` gets the profile's budget,
  compaction and log caps with a history window of exactly 40.

What the CLI does not expose is step 4: there is no flag for
`EnabledCapabilities`, so the full ~1,310-token `declare` surface is sent
every turn. On a 4k window that is the next thing worth reaching for, and it
needs the Go API.

## Choosing a model

agentloop asks for something more specific than chat: one fenced
` ```javascript ` block per turn, defining `function run(args)`, calling
`answer()` to finish. That needs solid instruction-following and competent
code generation. A capable 7–8B instruction-tuned or coding model is roughly
the practical floor; below it, expect the model to describe what it would do
instead of emitting a block.

**That failure is quiet, so know its shape.** When a reply contains no
fenced block and no `DONE` marker, the loop treats the whole reply as the
final answer rather than looping pointlessly. A model that never emits the
protocol therefore *looks* like it works — it answers, the run succeeds —
while the sandbox is never used and no primitive is ever called. If your
local runs come back plausible but never touch a tool, check for an
`execute_js` event before suspecting anything subtler.

## Next

- [Fitting a session in the context window](/concepts/context/) — what
  `ProfileFor` is setting, and what to override.
- [Extending agentloop](/extending/) — capabilities, stores, and policy.
