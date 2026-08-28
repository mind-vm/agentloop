---
title: Fitting a session in the context window
description: What actually fills the prompt, the four bounds that keep it inside a model's window, and one profile that derives all of them from the window size.
sidebar:
  order: 3
---

agentloop's defaults assume a frontier model. Against a locally hosted one —
8k, 16k, 32k served context — they are not slightly wrong, they are wrong by
an order of magnitude. This page is what fills the window, what bounds it,
and how to set all of that from the one number you already know.

## What's in the prompt

Every turn sends a fixed floor before any conversation:

| Piece | Bytes | ~tokens |
|---|---|---|
| Protocol prompt (`run(args)`/`answer()` contract) | 3,652 | ~910 |
| Sandbox API declarations, `DefaultCapabilities` | 5,245 | ~1,310 |
| **Total system prompt** | **8,915** | **~2,230** |

That floor grows with every pack you register — the `ext` packs, and
especially an OpenAPI-generated one, add more `declare` lines. On top of it
sits the conversation: up to `HistoryWindow` steps of prior turns, each
run's `log()` output, and any project instructions.

## What already keeps it small

Three mechanisms predate any explicit bound, and they do most of the work:

- **Return values never enter the prompt.** `run`'s return threads
  server-side into the next turn's `args`; the model sees only a structural
  digest — keys, types, array lengths, not values. A large result set costs
  one line of TypeScript-ish type text. See
  [Why a real return value beats re-serialised text](/concepts/#why-a-real-return-value-beats-re-serialised-text).
- **Stale code is elided.** Only the most recent turn's script stays
  verbatim; earlier ones become a one-line marker, since their effect
  already lives in `args` and their observations in the logs. Without this,
  prompt growth is triangular in turn count.
- **Skills are retrieved, not injected.** A `SKILL.md` contributes one
  catalog line to the prompt; the body arrives only when the model calls
  `skillGet(name)`.

What none of them bound is the conversation itself. That's what follows.

## The four bounds

### Token budget

`HistoryWindow` counts *steps*, and the compactor's `Trigger`/`Keep` count
*messages*. Neither says anything about how many **tokens** those occupy — a
message can be forty tokens or four thousand — so the same history is
trivial against a frontier model and a hard overflow against a local one.

`Config.ContextBudget` bounds each turn's prompt in tokens:

```go
cfg := agentloop.Config{
    // ...
    ContextBudget: agentloop.ContextBudget{MaxTokens: 8192},
}
```

`MaxTokens` is the window the model is actually **served** with — for a local
runtime, the server's configured context size (llama.cpp's `--ctx-size`,
Ollama's `num_ctx`), not the figure on the model card. `Reserve` is what's
held back for the completion (default `MaxTokens/8`, clamped to
`[512, 4096]`), and it doubles as the request's output cap, so a long answer
can't overrun the room the trim just made for it.

The zero value is disabled, so adding a budget changes nothing until
`MaxTokens` is set.

The trim runs immediately **before each request** — after compaction, after
stale-code elision, and after this run's own turns have grown the history.
That placement is the point: history grows *during* a `Run`, since every
`run()` block and execution result appends to it, so a check at rehydrate
time would pass and then overflow three turns later.

The system prompt and the current turn are never dropped — a request that
fits by discarding the protocol and the whole tool surface is worse than one
that overflows. The oldest turns go first, an execution result is never
stranded from the `run()` block that produced it, and a marker tells the
model how many turns are gone so it doesn't quietly assume what they held.

Nothing is silent. Every trim emits a `budget_trimmed` event, and every turn
span carries `agentloop.budget.estimated_tokens` against
`agentloop.budget.allowance` — so you can watch headroom shrink *before* a
session starts losing history. Sizing uses a crude bytes/4 estimate; supply
`ContextBudget.Estimate` to plug in a real tokenizer and fill the window
tighter.

:::note
The budget is a **backstop**, not a replacement for compaction. The
compactor preserves meaning; the budget only guarantees the request is
sendable. Configure both.
:::

### Compaction, in chunks

`Config.Compactor` folds the older stretch of a session into a summary
instead of letting it fall off the end of `HistoryWindow`.
`SummarizingCompactor` is the batteries-included implementation: past
`Trigger` messages it summarizes everything except the most recent `Keep`
via an `llm.Client` — point it at a small, cheap model.

A session long enough to need compaction can be long enough to overflow the
context of the call meant to fix that. So the transcript is summarized in
**chunks that each fit one call**, and the partial summaries are folded
together until one remains:

```go
Compactor: &agentloop.SummarizingCompactor{
    LLM:    smallClient,
    Budget: agentloop.ContextBudget{MaxTokens: 8192}, // the SUMMARIZER's window
},
```

`Budget` sizes a chunk against the window of the model doing the
summarizing — which is not the loop's model, and is usually deliberately
smaller. Left unset it defaults to roughly a 50,000-token transcript per
call, which suits a hosted summarizer.

Splits fall on message boundaries, so the summarizing model never reads half
a `run()` block, and each chunk says which part it is — without that, the
model reads the end of a middle chunk as the end of the session and reports
finished work as abandoned. Nothing is discarded to make the transcript fit:
the only surviving elision is for one individual message too large for a
whole call on its own.

The same 242 KB transcript takes:

| summarizer window | provider calls |
|---|---|
| 128k | 1 |
| default (unset) | 3 |
| 8k | 10 |
| 4k | 21 |

Each compaction is persisted as a `summary` checkpoint step, so the next
`Run` rehydrates from it rather than summarizing the same turns again — a
long session pays for a summarization only when it has grown past `Trigger`
since the last one, not on every `Run`. The covered steps stay in the trace;
only what is replayed to the model changes.

Compaction is best-effort: a failure emits a `warning` event and the run
proceeds on the uncompacted history. A failed *chunk* fails the whole
compaction, deliberately — a summary with a silent hole where a stretch of
the session used to be is worse than no summary at all.

### Project instructions on demand

`projectctx.Render(docs)` inlines every `AGENTS.md` in full, which is a
*standing* cost: these ride in the prompt of every turn of every run,
whether or not the turn touches anything they cover.

`RenderCatalog` + `Capabilities` is the retrieval alternative — the prompt
carries each file's opening section, and the model pulls the rest with
`projectGet(name)`:

```go
docs, _ := projectctx.Load(cwd)
caps = append(caps, projectctx.Capabilities(docs)...) // projectGet / projectList

result, err := loop.Run(ctx, agentloop.RunRequest{
    SessionID: id,
    Message:   msg,
    Context:   projectctx.RenderCatalog(docs),
})
```

`Loader.InlineBytes` (4 KB by default) is how much rides along, cut at a
paragraph boundary and never left dangling on a heading — a `## Deployment`
with nothing under it reads as an *empty* section rather than as a cut.
`projectList()` reports each file's size and whether the prompt already has
all of it.

A typical page-or-two `AGENTS.md` rides along whole and nothing changes. A
36 KB one drops from ~9,300 to ~1,200 prompt tokens per turn — and unlike
`MaxBytes` truncation, nothing is lost: the tail stays reachable.

The two renderers are alternatives, not a pair. `RenderCatalog` writes
pointers to `projectGet`, so wire the capabilities alongside it.

### Per-turn log output

A turn's `log()` output is the model's only visible channel out of the
sandbox — and it is replayed in **every later prompt of the session**. One
script that logs a whole dataset instead of returning it doesn't cost one
turn; it costs every turn after it.

`sandbox.DefaultMaxLogBytes` (16 KB) caps it, overridable per builder:

```go
SandboxBuilder: &agentloop.DefaultSandboxBuilder{
    Capabilities: caps,
    MaxLogBytes:  4096,
},
```

Truncation keeps the **head and the tail**, 3:1. The head has how the script
proceeded; the tail usually has what it concluded — a total, a count, the
line the agent actually meant to read. Head-only truncation reliably drops
the one line that mattered.

## One number, all four

Those four knobs live in three different units: tokens, steps, messages,
bytes. Setting them by hand means four unit conversions from a number you
already know. `agentloop.ProfileFor(window)` is that number expanded:

```go
p := agentloop.ProfileFor(8192)

docs, _ := projectctx.Loader{InlineBytes: p.ProjectInlineBytes}.Load(cwd)

cfg := agentloop.Config{
    LLM: client, Sessions: sessions, Steps: steps,
    SandboxBuilder: &agentloop.DefaultSandboxBuilder{
        Capabilities: caps,
        MaxLogBytes:  p.MaxLogBytes,
    },
    Compactor: &agentloop.SummarizingCompactor{LLM: client},
}
p.Apply(&cfg) // ContextBudget, HistoryWindow, and the compactor's knobs
```

| window | history | trigger/keep | log bytes | `AGENTS.md` inline |
|---|---|---|---|---|
| 4k | 42 | 14/4 | 1,792 | 1,792 |
| 8k | 84 | 28/9 | 3,584 | 3,584 |
| 16k | 171 | 57/19 | 7,168 | 4,096 |
| 32k | 180 | 60/20 | 14,336 | 4,096 |
| 128k+ | 180 | 60/20 | 16,384 | 4,096 |

At a large window it lands on the package defaults, so adopting a profile
changes nothing for a hosted model. `ProfileFor(0)` returns a zero `Profile`
whose `Apply` is a no-op — "I don't know the window" degrades to the
defaults rather than to a guess.

`Apply` is deliberately narrow. It writes what `Config` owns plus the
batteries-included compactor's knobs; it leaves a **custom** `Compactor`
alone, because its knobs are its own and silently doing nothing beats
silently doing the wrong thing. It never reaches into the sandbox builder or
the project-instruction loader, which are separate objects you construct —
hence `p.MaxLogBytes` and `p.ProjectInlineBytes` passed by hand above.

A profile is a starting point, not a constraint. Read the fields and
override what your workload justifies.

The [CLI](/cli/) does the whole of the above from one flag —
`agentloop --context-window 8192` — including the two `Apply` will not
write itself. An explicit `--history-window` still wins over the value the
profile derives.

## The lever a profile can't pull

**How many packs you register.** `DefaultCapabilities` alone costs ~1,310
prompt tokens of `declare` lines on every turn, and the `ext` packs add
more — an OpenAPI-generated skill scales with the spec. On a small window,
dropping capabilities a given agent never uses is often the largest single
saving available.

`DefaultSandboxBuilder.EnabledCapabilities` is the per-session allowlist for
that, and it's why `skills.Capabilities` returns one capability *per skill*
rather than one for all of them.

## Next

- [Running against a local model](/start/local-models/) — this page's
  settings applied end to end, plus the timeouts and the quiet failure mode
  local hardware brings with it.
- [Extending agentloop](/extending/) — the seams: capabilities,
  `SandboxBuilder`, stores, and policy.
- [Reference](/reference/#known-limitations) — what these bounds still
  don't cover, including the absence of a spend ceiling.
