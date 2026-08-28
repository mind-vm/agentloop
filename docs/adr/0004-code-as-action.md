# ADR-0004: Code as action — one tool, one JavaScript block per turn

- **Status:** Working
- **Confidence:** High
- **Decided:** 2026-03 (in `thinking-core`), reconfirmed 2026-08-21
- **Last reviewed:** 2026-08-28
- **Consumers:** the whole library — every other record here assumes it.

## Context

An agent needs a way to act. The default is tool calling: N tools as JSON
schemas, the model chooses and sequences them, each result is serialised back
into the prompt, and a multi-step task costs one round-trip per step.

The original reason this project went the other way was narrow and is now
obsolete: **Gemini Flash's constrained decoder corrupted JSON-encoded call
arguments on long JavaScript strings**, so text emission was the only channel
that worked. Current models emit long tool arguments fine.

Two properties kept the choice after the reason expired. **Composition is
free** — joining a document extraction against a query result, filtering,
looping, shaping output is code the model already knows how to write, where a
tool-calling design needs an orchestration protocol and a round-trip per step.
And **the surface the model must learn never grows**: adding a capability is
adding a function, not teaching a new tool schema. Measured on the spend-cap
eval, a cross-source join lands in 2 turns at ~11k prompt tokens.

The approach is also no longer unusual: the CodeAct paper (ICML 2024),
HuggingFace's smolagents `CodeAgent`, Cloudflare's Code Mode and Anthropic's
code-execution-with-MCP all arrive at the same place independently.

## Decision

The model gets **exactly one tool — a JavaScript sandbox** — and every
capability is a function inside it. Each turn it emits one fenced
` ```javascript ` block defining `function run(args) { ... }`; the loop parses
it out of the response text, executes it, and feeds the outcome back. A run ends
when the script calls `answer(result)`.

- **One block per turn, parsed from text** — not a provider tool call. This is
  what keeps the loop portable across any chat-completions endpoint
  ([ADR-0011](0011-openai-wire-and-retries.md)) instead of tied to one
  provider's function-calling dialect.
- **A function, not a loose script over a magic global.** "Parameters in, value
  out" is the most over-represented pattern in training data, and it makes data
  flow explicit: what survives to the next turn is exactly what you `return`.
  The `thinking-core` ancestor used a persistent `data` global and a `think`
  tool; that is a bespoke protocol a weak model forgets.
- **Primitive docs are TypeScript `declare` lines** in the system prompt — a
  format models read well, and one that costs the same whether the model calls
  one primitive or composes six.
- **A reply with no code fence is treated as the final answer**, so the loop can
  never spin waiting for a block that is not coming.

## Consequences

**Buys.** Multi-step work collapses into one LLM call, which is latency and
margin. Intermediate values stay server-side, which is what makes
[ADR-0005](0005-carry-by-return-see-by-log.md) possible at all. And the
model's reasoning *is* the code it ran — a far stronger audit artifact than a
list of tool calls ([ADR-0008](0008-sandbox-is-the-trust-boundary.md)).

**Costs.** The model can write code that does not run: syntax errors,
hallucinated APIs, type errors on undefined. That is routine, not exceptional,
and [ADR-0012](0012-a-thrown-turn-is-routine.md) is the whole record about
absorbing it. Providers are also optimising hard for tool calling, so we are off
the path their evals cover, and a real sandbox plus a policy layer is a
prerequisite rather than an option.

## What would change our mind

- **A model we must support cannot reliably emit a parseable block.**
  `parser_adversarial_test.go` exists for exactly this; if a target model fails
  it, text is the wrong channel for that model.
- **Structured output beats it on our own evals.** `eval/` is the instrument.
  This has never been measured head-to-head here, and the *original* reason for
  the choice is dead — so the belief currently rests on mechanism plus one
  spend-cap measurement, not on a comparison.
- **Capabilities stop being composed.** Covered by
  [ADR-0003](0003-a-bounded-work-horse.md): single-primitive scripts pay sandbox
  overhead for nothing.

## Cost of change

Very high. The system prompt, the parser, the sandbox, the shape digest and the
elision logic all exist to serve this protocol; replacing it with tool calling
is a different library, not a refactor. Adding tool calling *alongside* it is
cheaper but forks every context-economy mechanism, which is the worse outcome.

## Revisions

- 2026-03 — Chosen in `valiro-ai/thinking-core`, for the Gemini decoder reason,
  and exposed there as a `think` tool over a persistent `data` global.
- 2026-08-21 — Carried into the standalone extraction as `run(args)`.
- 2026-08-28 — Recorded retroactively. Writing it surfaced that the motivating
  reason is obsolete and the surviving justification has one measurement behind
  it, not a comparison.
