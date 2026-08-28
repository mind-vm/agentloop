# ADR-0011: One LLM protocol — the OpenAI wire, with retries inside the client

- **Status:** Working
- **Confidence:** Medium
- **Decided:** 2026-08-21 (the wire), 2026-08-26 (the retries)
- **Last reviewed:** 2026-08-28
- **Consumers:** every caller — `llm.Client` is how the loop reaches a model at
  all.

## Context

The ancestor's client was EdenAI-specific. A published library cannot be, and
writing a provider abstraction with an adapter per vendor is a large, permanent
surface for something that is not this project's differentiator.

`/chat/completions` is the de-facto lingua franca: OpenAI, OpenRouter, EdenAI,
Groq, together.ai, vLLM and Ollama all speak it. Since the loop parses a code
fence out of response *text* rather than using provider function calling
([ADR-0004](0004-code-as-action.md)), nothing else about a provider's API
matters.

Separately, the client had **no retry at all**. A single 429 or 503 turned into
an error and killed a whole reasoning trail mid-run — expensive, because the
turns already spent are lost with it.

## Decision

`llm.Client` is a small interface; `llm.NewOpenAI` is one implementation
speaking the OpenAI wire, pointed anywhere by `OPENAI_BASE_URL` /
`OPENAI_CHAT_MODEL`. Retry is **inside** that client, not in the loop.

- **Retry policy**: exponential backoff from a 500ms base with up to 50% jitter,
  capped at 30s, on rate limits and upstream failures. A `Retry-After` header
  (delay-seconds or HTTP-date) **wins outright** over the computed backoff — the
  server knows when its own window reopens. Adapted from `owainlewis/neo`'s
  `internal/llm/retry` (MIT, attributed in the file header), trimmed to the
  single-provider case and kept **unexported**: it is an implementation detail,
  not a seam.
- **The loop lives in `doJSONPost`**, the one path both `postChat` and
  `streamChat` go through, so streaming is covered by construction. Retries
  happen before anything is read from the body — a failed attempt is drained and
  closed, a success returns a fresh unconsumed stream — so a retried `Stream`
  call cannot replay deltas the caller already saw.
- **`Stream`'s 4xx fallback to `Complete` skips retryable statuses.** That
  fallback exists for upstreams that reject `stream:true`; a 429 is not a
  streaming rejection, and falling through would spend the retry budget a second
  time against a window still closed.
- **Configurable** via `Config.MaxRetries` (zero = default 3, negative = off)
  and `Config.RetryBaseDelay`, plus `OPENAI_MAX_RETRIES`, where `"0"` maps onto
  the negative sentinel because the zero value already means "use the default".

## Consequences

**Buys.** One client covers every hosted gateway and every local runtime worth
targeting, with no adapter layer to maintain. A transient 429 costs seconds
instead of the whole run. And because retry is below the seam, a consumer that
implements `llm.Client` itself is not obliged to reimplement it.

**Costs.** Provider-specific features are unreachable: no Anthropic-native API,
no provider function calling, no prompt-cache hints or stop-reason detail beyond
what the wire exposes. Retries also make a slow failure slower — up to four
attempts with backoff before an error surfaces — and burying the policy means a
consumer cannot swap it without replacing the client.

## What would change our mind

- **A model we need is only reachable on a native API.** Then `llm.Client` grows
  a second implementation; the interface is already the right size for that, and
  it is the reason the interface exists at all.
- **Cache hints, retries and stop reasons need to be first-class in the
  contract.** studio-apps
  ADR-0048 (`docs/adr/0048-llm-contract-carries-cache-retry-and-stop-reason.md`)
  argues exactly this for its own `llm` module; if it lands there, this client's
  `Config`-only surface is the thing that looks thin.
- **A consumer wants a different retry policy.** One request is a config field;
  two different ones mean the policy should be a seam after all.

## Cost of change

Low for retry (unexported, one function). Higher for the wire: swapping the
default client is fine, but the `OPENAI_*` env-var names are documented public
surface and appear in every consumer's deployment config.

## Revisions

- 2026-08-21 — EdenAI-specific client generalised to the OpenAI wire during the
  extraction, smoke-tested end-to-end.
- 2026-08-26 — Retry policy added, adapted from `owainlewis/neo` (MIT).
- 2026-08-28 — Recorded retroactively.
