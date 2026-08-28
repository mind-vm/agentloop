# ADR-0014: Retrieval over inlining for project instructions and skills

- **Status:** Working
- **Confidence:** Medium
- **Decided:** 2026-08-27 (skills), 2026-08-28 (project instructions)
- **Last reviewed:** 2026-08-28
- **Consumers:** `projectctx` and `skills`, the two layered packages that read a
  checkout from disk.

## Context

Both packages face the same problem from opposite ends. A project's `AGENTS.md`
files and its `SKILL.md` library are content the agent sometimes needs — and
anything placed in the system prompt is a **standing** cost, riding every turn
of every run whether or not the turn touches what it covers. A page-or-two
`AGENTS.md` is fine. A 36 KB one is ~9,300 prompt tokens *per turn*, and a
project with twenty skills cannot inline them at all.

Truncating with a byte cap is the obvious fix and the wrong one: the tail
becomes unreachable, and the model has no way to know it is missing.

## Decision

Put a **catalog** in the prompt and let the model fetch the body.

- **Skills are catalog-only.** `skills.Load(cwd)` reads
  `<root>/.agentloop/skills/<name>/SKILL.md`, and each skill contributes exactly
  **one line** to the prompt — name, description, and how to fetch it. The
  instructions arrive only when the model calls `skillGet(name)`. That is what
  makes a large skill library cheap: many detailed skills at one catalog line
  each.
- **Project instructions offer both.** `Render(docs)` inlines everything, which
  is right for a small file. `RenderCatalog(docs)` + `Capabilities(docs)` is the
  retrieval alternative: the prompt carries each file's opening section
  (`Loader.InlineBytes`, 4 KB by default, **cut at a paragraph boundary and
  never left dangling on a heading**), and the model pulls the rest with
  `projectGet(name)`. `projectList()` reports each file's size and whether the
  prompt already has all of it, so the model knows what it is missing. On a 36 KB
  file that is ~9,300 → ~1,200 tokens per turn with nothing lost.
- **They are alternatives, not a pair.** `RenderCatalog` writes pointers to
  `projectGet`, so the capabilities must be wired alongside it.
- **One capability per skill**, so `EnabledCapabilities` can switch a skill on
  per session ([ADR-0008](0008-sandbox-is-the-trust-boundary.md)).
- **A skill may not shadow a built-in** (`fetch`, `http`, `require`, …). Pack
  help entries merge by name, so such a skill would silently replace that
  primitive's own documentation. Rejected at load.
- **Reading is confined to the repository.** Files must resolve inside the root
  even after symlinks and are read through an `os.Root`, so a symlinked
  `AGENTS.md` cannot pull in a file from outside the project. `Load` returns
  docs that read cleanly *alongside* its error, so one unreadable file warns
  rather than denying the run its remaining context.
- **No user-global file is read unless asked for** (`Loader{GlobalDir: ...}`).
  This departs from the CLI convention the format came from, deliberately: a
  library should not reach into `$HOME` on its own.

## Consequences

**Buys.** Prompt cost tracks what the run actually uses instead of what the
project contains, and it stops scaling with the size of the instruction corpus.
Unlike a byte cap, nothing becomes unreachable.

**Costs.** Retrieval is a round-trip: content the model would have had for free
now costs a turn when it needs it, and the model has to *decide* to fetch —
which it will sometimes not do, answering from a catalog line where the body
would have corrected it. That failure is silent. The two `projectctx` rendering
modes are also a genuine fork in the API, and picking wrong is invisible until
a token bill arrives.

## What would change our mind

- **Models skip the fetch and answer from the catalog line.** That is the
  failure this design risks; the fix is a stronger catalog line, or inlining
  more of the head, not abandoning retrieval.
- **`InlineBytes` needs tuning per project rather than per window.** Today it is
  derived from the model's window ([ADR-0013](0013-context-control-from-one-number.md));
  if content shape matters more than window size, it belongs elsewhere.
- **A third retrieval surface appears.** Two (`projectGet`, `skillGet`) is a
  pattern; three is a mechanism, and it should be one.

## Cost of change

Low. Both packages are opt-in siblings
([ADR-0009](0009-thin-core-optional-packages.md)) and nothing in the core
imports them; a consumer switches rendering modes by changing which function it
passes as `RunRequest.Context`.

## Revisions

- 2026-08-27 — `skills` added, catalog-only from the start.
- 2026-08-28 — `projectctx` gained `RenderCatalog` / `projectGet` /
  `projectList` alongside the existing `Render`.
- 2026-08-28 — Recorded.
