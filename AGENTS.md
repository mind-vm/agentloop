# Working in agentloop

A Go engine for LLM agents that think by writing JavaScript. The README's
package list is the map; this file is the part the code does not tell you.

## Comment the why

Every comment here explains a reason, a trade-off, or a consequence — never
what the next line does. Match that. A comment that survives is one that
would stop someone from "simplifying" the code back into a bug.

## The core cannot touch the host

`sandbox` has no filesystem or process access, and that absence is a feature
people rely on. Anything reaching the host — files, commands, mail, secrets —
belongs in `ext`, where a library user opts into it deliberately. Keep
`sandbox` dependency-light too.

`ext` packs take a callback or a small interface, never a concrete backend,
so the package itself stays free of any mail, search, or database
dependency. `Capability` closes over what it needs when it is constructed
rather than threading it through `BuildContext`.

## Seams

`PolicyChecker`, `InputRequester`, `SessionStore`, `StepStore`,
`SandboxBuilder`, `Compactor`, `Redactor` — small interfaces an application
replaces. When you add behaviour that an application might reasonably want
to own, add a seam rather than a config flag.

A new store implementation earns its keep by passing
`agentlooptest.StepStoreContract` and `SessionStoreContract`. Write the
contract assertion before the implementation.

## Deny, then ask

Side-effecting primitives check the policy first. When the policy denies and
an `InputRequester` is available, the pack asks the user; the answer is
remembered per session. Both halves live in the pack — see `ext.WorkspacePack`
for the shape.

Errors reaching the model say what to do next: `editFile` reports the match
count and suggests more context, `exec` names the whole command it wants to
run. A refusal costs the agent one turn; a wrong file costs a wrong answer it
believes in.

## What will bite you

- **`RunStep.StepIndex` repeats.** The loop derives it from the size of the
  history window it just read, so a long session reuses indices. Order steps
  by insertion, never by index — `agentloopsql` explains it at length.
- **`LastN` must return oldest-first.** The loop replays it straight into the
  context window.
- **goja maps values by Go field name, not `json` tag.** A pack returning a
  struct surfaces `{Name}`, not `{name}`. Use `map[string]any` at the
  boundary and keep the documented interface true.
- **A nil slice reaches JS as `null`.** Return initialised empty slices, or
  `.length` throws.
- **A pack's `Prompt` is the model's entire interface** to that pack.
  TypeScript `declare` lines, because the model recognises the shape. If the
  prompt and the code disagree, the model believes the prompt.

## Verify by running it

Every real bug found in this repo lately was found by executing something,
not by reading it: SQLite refusing a concurrent open, `/dev/null` passing a
character-device test for a terminal, goja ignoring `json` tags. Reading
confirmed the wrong answer each time.

Drive the CLI end to end against a scripted OpenAI-wire endpoint — a small
HTTP server returning canned SSE frames is enough to exercise the whole loop
with no API key. Use `expect` when a terminal is needed.

## Before you finish

`gofmt -l .`, `go vet ./...`, and `go test -race ./...` all clean — that is
what CI runs. Cross-compile if you touched anything platform-specific; the
build tags in `ext/pack_exec_*.go` are invisible to a host-only build.
