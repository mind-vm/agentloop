---
title: Why JavaScript instead of tool calls
description: How agentloop's turn loop works — one script per turn, a sandboxed executor, and a return value that threads server-side into the next turn.
sidebar:
  order: 1
---

Most agent frameworks give the model a fixed menu of named tools and ask it
to emit one structured call per turn. agentloop instead gives the model a
JavaScript sandbox and asks it to write a small program.

## One turn, one script

Each turn the model emits a single fenced ` ```javascript ` block defining:

```js
function run(args) {
  // ...
}
```

agentloop executes that script in a sandboxed
[goja](https://github.com/dop251/goja) runtime — a pure-Go JS engine, no
subprocess, no V8. `run`'s return value becomes `args` for the *next* turn.

A run ends when the script calls `answer(result)` — an explicit signal that
the agent is done, rather than agentloop guessing from the shape of the
output.

## Why a real return value beats re-serialised text

Named-tool loops thread state through the prompt: a tool's result gets
serialised to text, appended to the conversation, and the model has to
re-read and re-parse it on the next turn. That costs tokens on every step and
loses fidelity — numbers become strings, structure becomes prose the model
has to reconstruct.

agentloop's `run` return value is a real JavaScript value threaded
server-side into the next turn's `args`. The model never sees it directly —
it sees only what its own `log()` calls printed, plus a compact structural
digest (shape, not full contents) of what the previous turn returned. Large
data can pass through many turns without ever entering the context window
twice.

## Keeping the prompt small over many turns

A long-running agent that kept every turn's script verbatim in history would
grow its prompt linearly with turn count. agentloop elides stale code: only
the most recent turn's script is kept verbatim in the conversation sent to
the model; earlier ones are replaced by a one-line marker, since their effect
already lives in `args` and their observations in the logs. Combined with
structural-digest threading, this is what keeps many-step runs affordable.

Neither mechanism bounds the conversation itself, though. For that there are
four explicit limits — a token budget, chunked compaction, on-demand project
instructions, and a per-turn log cap — plus one profile that derives all of
them from a model's context window. See [Fitting a session in the context
window](/concepts/context/), which matters most if you're pointing agentloop
at a locally hosted model.

## Where this stops

agentloop is a synchronous, single-process design. There's no
`Promise`/`async`/`.then()` support in the sandbox — goja parses the syntax
but has no event loop, so a continuation would silently never run, and the
system prompt warns the model off writing one. See [known
limitations](/reference/#known-limitations) for the full list.

## Next

- [Packages](/concepts/packages/) — what each package is responsible for.
- [Fitting a session in the context window](/concepts/context/) — the
  bounds that keep a long session inside a model's window.
- [Extending agentloop](/extending/) — capabilities, sandbox
  composition, stores, and policy.
