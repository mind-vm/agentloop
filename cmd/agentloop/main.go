// Command agentloop drives an agentloop.Loop from the terminal.
//
// It is the chat surface for the engine in the parent module: it
// discovers project instructions (AGENTS.md) and skills (SKILL.md) from
// a workspace directory, composes the default capability bundle, and
// runs one turn — or an interactive conversation — against an
// OpenAI-wire-compatible LLM.
//
// Usage:
//
//	export OPENAI_API_KEY=sk-...
//	agentloop "What's 12 * 7? Answer as a single number."
//	agentloop chat
//	agentloop doctor
//
// Two output modes. The default is for a human: the loop's activity
// streams to stderr and stdout receives only the final answer, so
// `answer=$(agentloop run "...")` captures what you'd expect. With
// --json, stdout becomes a newline-delimited JSON stream of every run
// event followed by a terminal result object — the mode another program
// (or another agent) should drive.
package main

import (
	"fmt"
	"os"
)

// Exit codes. A script driving the CLI can tell "the agent answered",
// "the agent failed", "the agent ran out of steps", and "you invoked me
// wrong" apart without parsing any output. The first three mirror
// agentloop.RunResult.Status, which already distinguishes them.
const (
	exitOK       = 0 // Status == "completed"
	exitError    = 1 // Status == "error", or Run itself returned an error
	exitMaxSteps = 2 // Status == "max_iterations"
	exitUsage    = 3 // bad flags, missing message, unusable environment
)

// subcommands are the words dispatch recognises in argv[0]. Anything
// else is treated as the start of a message, so `agentloop "hello"`
// keeps working without the explicit `run`.
var subcommands = map[string]func([]string) int{
	"run":      cmdRun,
	"chat":     cmdChat,
	"sessions": cmdSessions,
	"doctor":   cmdDoctor,
}

func main() {
	os.Exit(dispatch(os.Args[1:]))
}

// route resolves argv into a subcommand name and the arguments that
// belong to it. It is pure so the routing rules — chiefly that an
// unrecognised first word starts a message rather than being an error —
// can be tested without running anything.
func route(argv []string) (name string, rest []string) {
	if len(argv) == 0 {
		// No arguments at all is ambiguous rather than wrong: it could
		// be a piped message. Let run decide — it reads stdin when
		// stdin isn't a terminal, and explains itself when it is.
		return "run", nil
	}
	switch argv[0] {
	case "help", "-h", "-help", "--help":
		return "help", argv[1:]
	case "version", "--version":
		return "version", argv[1:]
	}
	if _, ok := subcommands[argv[0]]; ok {
		return argv[0], argv[1:]
	}
	// Not a subcommand, so the whole of argv is a `run` invocation,
	// flags included — `agentloop --json "hello"` works as well as
	// `agentloop run --json "hello"`.
	return "run", argv
}

// dispatch routes argv to a subcommand. It is separate from main so
// tests can exercise routing without exiting the process.
func dispatch(argv []string) int {
	name, rest := route(argv)
	switch name {
	case "help":
		usage(os.Stdout)
		return exitOK
	case "version":
		fmt.Println(versionString())
		return exitOK
	}
	return subcommands[name](rest)
}

func usage(w *os.File) {
	fmt.Fprint(w, `agentloop — run an LLM agent that thinks by writing JavaScript.

USAGE
  agentloop [flags] <message>     run one turn (the default subcommand)
  agentloop run [flags] <message> run one turn
  agentloop chat [flags]          interactive conversation
  agentloop sessions ls           list stored sessions
  agentloop sessions show <id>    print a session's trace
  agentloop sessions rm <id>...   delete stored sessions
  agentloop sessions revoke <id>  forget a session's approvals
  agentloop doctor [flags]        check the environment and workspace
  agentloop version               print version information
  agentloop help                  print this message

  With no message argument, `+"`run`"+` reads it from stdin — so
  `+"`echo 'summarise this' | agentloop run`"+` works.

FLAGS
  --model string          chat model (default: $OPENAI_CHAT_MODEL)
  --max-steps int         cap LLM round-trips per run (default 20)
  --timeout duration      wall-clock cap per run (default 5m)
  --history-window int    prior steps rehydrated into context (default 80)
  --context-window int    the model's context size in tokens; sizes the prompt
                          budget, compaction and log caps to it (default: assume
                          a large window). Set this for a local model.
  --session string        session id to run under (default: a new one)
  --continue              resume the most recently updated session
  --ephemeral             keep the session in memory; write nothing
  --db string             session database path
  --cwd string            project directory the agent reads and edits, and
                          where AGENTS.md / SKILL.md are discovered
  --json                  emit newline-delimited JSON events on stdout
  --json-stream           in --json mode, also emit response_chunk events
  --quiet                 suppress the live activity stream on stderr
  --approve mode          answer permission prompts: prompt, auto, or deny
                          (default: prompt at a terminal, deny otherwise)
  --no-network            remove fetch() and require('http')
  --no-exec               remove exec()
  --allow-exec names      run these commands without asking, e.g. go,git
  --dangerously-allow-shell
                          let exec() take a shell string as well as an argv

ENVIRONMENT
  OPENAI_API_KEY          required
  OPENAI_BASE_URL         any OpenAI-wire-compatible endpoint
  OPENAI_CHAT_MODEL       model name that endpoint expects
  OPENAI_MAX_RETRIES      retries after a failed request (default 3, 0 disables)
  AGENTLOOP_DB            session database path, unless --db says otherwise

EXIT CODES
  0 completed   1 error   2 max iterations reached   3 usage
`)
}
