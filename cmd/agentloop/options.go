package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/mind-vm/agentloop"
	"github.com/mind-vm/agentloop/agentloopmem"
	"github.com/mind-vm/agentloop/llm"
	"github.com/mind-vm/agentloop/projectctx"
	"github.com/mind-vm/agentloop/skills"
)

// options is one invocation's resolved configuration. Every field maps
// onto either agentloop.Config or the shape of the output, so the flag
// set stays a thin surface over the engine rather than a second
// configuration model.
type options struct {
	model         string
	maxSteps      int
	timeout       time.Duration
	historyWindow int
	session       string
	cwd           string
	asJSON        bool
	jsonStream    bool
	quiet         bool
}

// bind registers the shared flags on fs. Every subcommand binds the
// same set so `--model` means the same thing everywhere.
func (o *options) bind(fs *flag.FlagSet) {
	fs.StringVar(&o.model, "model", "", "chat model (default: the client's own, from $OPENAI_CHAT_MODEL)")
	fs.IntVar(&o.maxSteps, "max-steps", 0, "cap LLM round-trips per run (0 = engine default)")
	fs.DurationVar(&o.timeout, "timeout", 0, "wall-clock cap per run (0 = engine default)")
	fs.IntVar(&o.historyWindow, "history-window", 0, "prior steps rehydrated into context (0 = engine default)")
	fs.StringVar(&o.session, "session", "", "session id to run under (default: generated)")
	fs.StringVar(&o.cwd, "cwd", "", "workspace root for AGENTS.md / SKILL.md discovery (default: current directory)")
	fs.BoolVar(&o.asJSON, "json", false, "emit newline-delimited JSON events on stdout")
	fs.BoolVar(&o.jsonStream, "json-stream", false, "in --json mode, also emit response_chunk events")
	fs.BoolVar(&o.quiet, "quiet", false, "suppress the live activity stream on stderr")
}

// resolve fills in the defaults that need the process to compute them,
// and rejects combinations that can't mean anything.
func (o *options) resolve() error {
	if o.cwd == "" {
		wd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("cannot determine the working directory: %w", err)
		}
		o.cwd = wd
	}
	if o.session == "" {
		// Sessions are not yet persisted across invocations (the stores
		// are in-memory), so this id only spans one process today. It
		// is generated rather than fixed so the value is already
		// meaningful once a durable store lands behind it.
		o.session = uuid.NewString()
	}
	if o.jsonStream && !o.asJSON {
		return errors.New("--json-stream has no meaning without --json")
	}
	if o.maxSteps < 0 || o.historyWindow < 0 || o.timeout < 0 {
		return errors.New("--max-steps, --history-window and --timeout cannot be negative")
	}
	return nil
}

// configureLogging points the engine's slog output at stderr and quiets
// it down to what a CLI user should see. Without this the default
// handler prints the loop's INFO lines — "installing conservative
// sandbox.DefaultPolicy" and friends — into every invocation.
func configureLogging(o *options) {
	level := slog.LevelWarn
	if o.quiet {
		level = slog.LevelError
	}
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level})))
}

// session bundles everything a subcommand needs to run turns: the loop
// itself, the project context to fold into each run, and the id the
// turns share.
type session struct {
	loop      agentloop.Loop
	projectCx string
	id        string
}

// newSession builds the loop for these options. warn receives non-fatal
// setup problems — a partially readable AGENTS.md tree still yields the
// docs that did load, and that is worth a warning rather than an exit.
func newSession(o *options, warn func(string)) (*session, error) {
	client, err := llm.NewOpenAI(llm.ConfigFromEnv())
	if err != nil {
		return nil, err
	}

	// Project instructions and skills are layered capabilities: the loop
	// knows nothing about either. The chat surface discovers them and
	// passes one as prompt text and the other as capabilities.
	docs, err := projectctx.Load(o.cwd)
	if err != nil && warn != nil {
		warn(err.Error())
	}
	sk, err := skills.Load(o.cwd)
	if err != nil && warn != nil {
		warn(err.Error())
	}

	caps := append(agentloop.DefaultCapabilities(client, o.model), skills.Capabilities(sk)...)
	loop := agentloop.New(agentloop.Config{
		LLM:            client,
		Sessions:       agentloopmem.NewSessionStore(nil),
		Steps:          agentloopmem.NewStepStore(),
		SandboxBuilder: &agentloop.DefaultSandboxBuilder{Capabilities: caps},
		Model:          o.model,
		MaxIterations:  o.maxSteps,
		RunTimeout:     o.timeout,
		HistoryWindow:  o.historyWindow,
	})

	return &session{loop: loop, projectCx: projectctx.Render(docs), id: o.session}, nil
}

// newRenderer picks the output shape for these options.
func newRenderer(o *options, stdout, stderr io.Writer) renderer {
	if o.asJSON {
		return &jsonRenderer{out: stdout, errOut: stderr, chunks: o.jsonStream}
	}
	return &humanRenderer{
		out:    stdout,
		errOut: stderr,
		quiet:  o.quiet,
		// Streaming the answer to stderr while also writing it to stdout
		// prints it twice when both are the same terminal. Stream only
		// when stdout is redirected somewhere else — then stderr is the
		// human's live view and stdout is the capture.
		streamAnswer: !o.quiet && !isTerminal(os.Stdout),
	}
}

// exitCodeFor maps a finished run onto the process exit code.
//
// Running out of iterations is reported by the loop as a populated
// result AND an error describing the same fact, so it is checked first:
// treating that error as a generic failure would collapse "the agent
// needed more steps" into "something broke", which is the one
// distinction a caller most wants to act on. Any other error outranks
// the status it came with.
func exitCodeFor(res agentloop.RunResult, err error) int {
	if res.Status == "max_iterations" {
		return exitMaxSteps
	}
	if err != nil {
		return exitError
	}
	if res.Status == "completed" {
		return exitOK
	}
	return exitError
}

// isTerminal reports whether f is attached to a character device — the
// standard way to tell an interactive terminal from a pipe or file
// without taking a dependency for it.
func isTerminal(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}
