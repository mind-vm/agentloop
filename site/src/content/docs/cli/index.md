---
title: The CLI
description: Run agentloop from the terminal — durable sessions, a machine-readable output mode, workspace file access, and commands.
sidebar:
  order: 1
---

`cmd/agentloop` is the engine as a command. It discovers `AGENTS.md` and
`SKILL.md` from a project directory, composes the default capability bundle,
and runs turns against any OpenAI-wire-compatible endpoint.

```sh
go install github.com/mind-vm/agentloop/cmd/agentloop@latest

export OPENAI_API_KEY=sk-...
agentloop "What's 12 * 7? Reply with just the number."
```

`agentloop doctor` checks the whole setup in one command: the resolved
endpoint and model, one real request to confirm it answers, the workspace it
would work in, and what it is permitted to do.

## Two output modes

By default the loop's activity streams to stderr and **stdout carries only
the agent's answer** — no banner, no framing — so capturing it works:

```sh
answer=$(agentloop run "summarise the README")
```

With `--json`, stdout becomes newline-delimited JSON: one object per run
event, then a terminal `result` object. This is the mode another program
should drive.

```sh
agentloop run --json "audit the error handling" \
  | jq -c 'select(.type == "result")'
```

The exit code says how the run ended, so nothing has to be parsed to find
out: `0` completed, `1` error, `2` max iterations reached, `3` usage.

## Sessions

Every run is recorded in a SQLite database, so a conversation survives the
process:

```sh
agentloop run --session review "read the diff and tell me what changed"
agentloop run --continue "now check the tests cover it"

agentloop sessions ls
agentloop sessions show review
agentloop sessions rm review
```

The database is `$AGENTLOOP_DB`, else `$XDG_DATA_HOME/agentloop/`, else the
platform's per-user application directory; `--db` overrides all three.
`--ephemeral` writes nothing at all.

Several `agentloop` processes can share one database — it runs in WAL mode
with a busy timeout, so a second terminal waits for the lock rather than
failing the run.

## The workspace

`--cwd` (default: the current directory) is the project the agent reads and
edits, through `readFile`, `writeFile`, `editFile`, `listDir`, `glob`, and
`grep`. Every path resolves through an `os.Root` handle, so `..`, an
absolute path, and a symlink pointing outside are all refused by the runtime
rather than by path arithmetic that has to be got right. `.git` is never
walked.

`exec(argv)` runs a command and returns `{stdout, stderr, code, timedOut}`.
A non-zero exit is a *result*, not an error — a failing build is the
information the agent asked for:

```js
const r = exec(["go", "test", "./..."]);
if (r.code !== 0) log(r.stderr);
```

There is no shell: argv is an array, so a model-authored string never becomes
a command line. The child gets an allowlisted environment, so the agent's own
API key is absent by construction. A command that outlives its timeout is
killed along with everything it spawned.

:::caution
`exec` is **not** confined to `--cwd`. Once another program is running it has
the full privileges of the user running the agent, and `os.Root` has no say
in it. What bounds it is the permission prompt and your judgement about which
commands to allow.
:::

## Permission

Anything with a consequence asks first — reaching a domain the policy has not
allowed, changing a file, running a command:

```
Allow the agent to run: go test ./...  [y/N]
```

`--approve` decides how those are answered: `prompt`, `auto`, or `deny`. The
default depends on whether anyone is there to answer — `prompt` at a
terminal, `deny` otherwise — so an unattended run fails closed instead of
blocking forever. Asking for `--approve prompt` with stdin redirected is an
error rather than a silent downgrade.

An approval is remembered for the session, across invocations. A refusal
holds for that invocation but is never stored, so a later run is free to
allow what an earlier one declined. `agentloop sessions show <id>` lists what
a session has approved; `agentloop sessions revoke <id>` forgets it.

Reads of the workspace are **not** gated. Confined to the root they cannot
reach anything you did not point the agent at, and a prompt per file would
train you to approve without reading — the mutations are where the
consequence is.

### Narrowing what a run can do

| Flag | Effect |
| --- | --- |
| `--allow-exec go,git` | run these commands without asking |
| `--no-exec` | remove `exec()` entirely |
| `--no-network` | remove `fetch()` and `require('http')` |
| `--approve deny` | refuse every permission request |
| `--ephemeral` | record nothing |

`--no-network` pairs with the ungated reads: it is how a sensitive tree gets
worked on with no way for its contents to leave the machine.

## Pointing at another provider

Any OpenAI-wire-compatible endpoint works — set the base URL and a model name
it recognises:

```sh
# OpenRouter
export OPENAI_BASE_URL=https://openrouter.ai/api/v1
export OPENAI_CHAT_MODEL=openai/gpt-4o-mini

# EdenAI
export OPENAI_BASE_URL=https://api.edenai.run/v3
export OPENAI_CHAT_MODEL=google/gemini-2.5-flash
```

`OPENAI_API_KEY` holds whichever provider's key, whatever the variable is
called. Run `agentloop doctor` afterwards: it makes one real request through
the same path a run uses, including the fallback for gateways that refuse to
stream, so a configuration that will not work says so immediately rather than
on the first turn.

A locally hosted model works the same way, but needs more than the URL — a
placeholder key, a longer `--timeout`, and context settings sized to the
window the server actually serves. See [Running against a local
model](/start/local-models/).
