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
  protocol.

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
  `examples/cli` ships trivial in-memory implementations to start from.
- **Policy** — implement `sandbox.PolicyChecker` to gate side-effecting
  primitives (`fetch`, `ai`, or your own) per call. `sandbox.DefaultPolicy`
  is a conservative default (deny side effects, block private-network
  fetches); `sandbox.AllowAll` is the explicit fail-open escape hatch.

## License

MIT — see [LICENSE](LICENSE).
