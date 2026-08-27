package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/mind-vm/agentloop"
	"github.com/mind-vm/agentloop/agentloopmem"
	"github.com/mind-vm/agentloop/agentloopsql"
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
	db            string
	resume        bool
	ephemeral     bool
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
	fs.StringVar(&o.db, "db", "", "session database path (default: $AGENTLOOP_DB, else a per-user location)")
	fs.BoolVar(&o.resume, "continue", false, "resume the most recently updated session")
	fs.BoolVar(&o.ephemeral, "ephemeral", false, "keep this session in memory only — nothing is written to the database")
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
	if o.jsonStream && !o.asJSON {
		return errors.New("--json-stream has no meaning without --json")
	}
	if o.session != "" && o.resume {
		return errors.New("--session names a session and --continue picks one; use one or the other")
	}
	if o.resume && o.ephemeral {
		return errors.New("--continue resumes a stored session, which --ephemeral has none of")
	}
	if o.db != "" && o.ephemeral {
		return errors.New("--db names a database and --ephemeral writes to none")
	}
	if !o.ephemeral && o.db == "" {
		path, err := defaultDBPath()
		if err != nil {
			return err
		}
		o.db = path
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

// defaultDBPath is where sessions live when the invocation does not say.
// AGENTLOOP_DB comes first so a project can pin its own database, then
// the XDG data directory, then whatever the platform calls a per-user
// application directory.
func defaultDBPath() (string, error) {
	if env := os.Getenv("AGENTLOOP_DB"); env != "" {
		return env, nil
	}
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return filepath.Join(xdg, "agentloop", "sessions.db"), nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine where to keep sessions — set --db or AGENTLOOP_DB: %w", err)
	}
	return filepath.Join(dir, "agentloop", "sessions.db"), nil
}

// openStore opens the session database named by o, creating its
// directory if needed.
func openStore(o *options) (*agentloopsql.Store, error) {
	if dir := filepath.Dir(o.db); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("cannot create %s: %w", dir, err)
		}
	}
	return agentloopsql.Open(o.db, nil)
}

// session bundles everything a subcommand needs to run turns: the loop
// itself, the project context to fold into each run, and the id the
// turns share.
type session struct {
	loop      agentloop.Loop
	projectCx string
	id        string

	// store is the durable backing, or nil for an ephemeral session.
	store *agentloopsql.Store
}

// close releases the session database, if there is one.
func (s *session) close() {
	if s.store != nil {
		s.store.Close()
	}
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

	sessions, steps, store, id, err := resolveStores(o)
	if err != nil {
		return nil, err
	}

	caps := append(agentloop.DefaultCapabilities(client, o.model), skills.Capabilities(sk)...)
	loop := agentloop.New(agentloop.Config{
		LLM:            client,
		Sessions:       sessions,
		Steps:          steps,
		SandboxBuilder: &agentloop.DefaultSandboxBuilder{Capabilities: caps},
		Model:          o.model,
		MaxIterations:  o.maxSteps,
		RunTimeout:     o.timeout,
		HistoryWindow:  o.historyWindow,
	})

	return &session{loop: loop, projectCx: projectctx.Render(docs), id: id, store: store}, nil
}

// resolveStores opens the backing stores and settles which session this
// invocation extends: the one named by --session, the most recent one
// under --continue, or a new one.
func resolveStores(o *options) (agentloop.SessionStore, agentloop.StepStore, *agentloopsql.Store, string, error) {
	if o.ephemeral {
		id := o.session
		if id == "" {
			id = uuid.NewString()
		}
		return agentloopmem.NewSessionStore(nil), agentloopmem.NewStepStore(), nil, id, nil
	}

	store, err := openStore(o)
	if err != nil {
		return nil, nil, nil, "", err
	}

	id := o.session
	switch {
	case id != "":
	case o.resume:
		id, err = store.MostRecent(context.Background())
		if err != nil {
			store.Close()
			if errors.Is(err, agentloopsql.ErrNoSession) {
				return nil, nil, nil, "", fmt.Errorf("nothing to continue — no sessions in %s", o.db)
			}
			return nil, nil, nil, "", err
		}
	default:
		id = uuid.NewString()
	}
	return store, store, store, id, nil
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
