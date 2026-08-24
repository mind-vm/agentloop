---
title: Quickstart
description: Install agentloop, wire up a loop with in-memory stores, and run the example CLI.
sidebar:
  order: 1
---

## Install

```sh
go get github.com/jryannel/agentloop
```

## Wire up a loop

A `Loop` needs an LLM client, a `SessionStore`, a `StepStore`, and a
`SandboxBuilder`. For local development, `agentloopmem` supplies in-memory
stores and `DefaultCapabilities` + `DefaultSandboxBuilder` supply a
reasonable sandbox:

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

## Run the example

[`examples/cli`](https://github.com/jryannel/agentloop/tree/main/examples/cli)
is a complete runnable program with in-memory stores:

```sh
export OPENAI_API_KEY=sk-...
go run ./examples/cli "What's 12 * 7? Reply with just the number."
```

## Point at a different gateway

`llm.NewOpenAI` talks to anything that speaks the same
`/chat/completions` wire protocol as OpenAI. Set `OPENAI_BASE_URL` and
`OPENAI_CHAT_MODEL` to match the gateway:

```sh
# OpenRouter
export OPENAI_BASE_URL=https://openrouter.ai/api/v1
export OPENAI_CHAT_MODEL=openai/gpt-4o-mini

# EdenAI
export OPENAI_BASE_URL=https://api.edenai.run/v3
export OPENAI_CHAT_MODEL=google/gemini-2.5-flash
```

Groq, together.ai, and a local vLLM/Ollama server all work the same way.

## Next

- [Why JavaScript instead of tool calls](/agentloop/concepts/) — the design
  the rest of the docs assume.
- [Extending agentloop](/agentloop/extending/) — plugging in your own
  capabilities, stores, and policy for production use.
