---
title: Reference
description: Known limitations, license, and where to find the full API reference.
sidebar:
  order: 1
---

## Known limitations

This is a synchronous, single-process design, not a durable workflow engine:

| Limitation | Detail |
|---|---|
| No durable execution | A `Run` call lives in one goroutine; a process restart mid-run kills it (steps already persisted are fine, but nothing resumes automatically) |
| Everything inside a turn is synchronous | Including I/O — no `Promise`/`async`/`.then()` in the sandbox (goja parses them but has no event loop, so continuations silently never run; the system prompt warns the model off this) |
| Process-level isolation only | goja is memory-safe and interruptible, but sandboxes share the host process's heap/CPU — no per-run resource quota |
| Unbounded args carry | Nothing caps the size of the server-side state threaded between turns (`RunResult.DataBytesCarried` gives you the observability to notice, not a limit) |
| No built-in **spend** ceiling | `MaxIterations` bounds turns and token usage is tracked, and `Config.ContextBudget` bounds each turn's *prompt* — but nothing caps what a run or a tenant is allowed to spend in total. A chunked compaction of a very long session makes many provider calls, all under the same non-limit |
| Token counts are estimated, not tokenized | `ContextBudget` sizes the prompt with a bytes/4 heuristic and a deliberately pessimistic reserve. Supply `ContextBudget.Estimate` for a real tokenizer; without one, the budget is a safe approximation rather than an exact fit |

None of these are architectural dead ends — they're the natural next layer
(suspend/resume, batched I/O primitives, resource quotas, spend
enforcement) to add on top if and when you need them.

For what *is* bounded — prompt size, history, compaction, project
instructions, log output — see [Fitting a session in the context
window](/concepts/context/).

## API reference

The full API reference is the Go doc comments, on
[pkg.go.dev](https://pkg.go.dev/github.com/mind-vm/agentloop).

## Source

[github.com/mind-vm/agentloop](https://github.com/mind-vm/agentloop)

## License

MIT — see
[LICENSE](https://github.com/mind-vm/agentloop/blob/main/LICENSE).
