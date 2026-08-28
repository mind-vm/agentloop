package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/google/uuid"

	"github.com/mind-vm/agentloop"
	"github.com/mind-vm/agentloop/agentloopmem"
	"github.com/mind-vm/agentloop/agentloopsql"
	"github.com/mind-vm/agentloop/llm"
	"github.com/mind-vm/agentloop/projectctx"
	"github.com/mind-vm/agentloop/sandbox"
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

	// contextWindow is the model's context size in tokens. It is not a
	// setting of its own so much as the input to several: given one,
	// agentloop.ProfileFor derives the token budget, the compaction
	// thresholds, the per-turn log cap, and how much of an AGENTS.md
	// rides in the prompt. Zero leaves every one of them at its engine
	// default, which assumes a frontier-sized window.
	contextWindow int
	session       string
	cwd           string
	db            string
	resume        bool
	ephemeral     bool
	asJSON        bool
	jsonStream    bool
	quiet         bool

	// approve is the raw --approve value; approval is what resolve
	// settles it to, including the default that depends on whether
	// anyone is at the keyboard.
	approve  string
	approval approvalMode

	// noNetwork removes fetch and http, for working on a tree whose
	// contents should not be able to leave the machine.
	noNetwork bool

	// noExec removes exec() entirely; allowExec pre-approves command
	// basenames; allowShell permits the string form of exec().
	noExec     bool
	allowExec  string
	allowShell bool
	execNames  []string
}

// bind registers the shared flags on fs. Every subcommand binds the
// same set so `--model` means the same thing everywhere.
func (o *options) bind(fs *flag.FlagSet) {
	fs.StringVar(&o.model, "model", "", "chat model (default: the client's own, from $OPENAI_CHAT_MODEL)")
	fs.IntVar(&o.maxSteps, "max-steps", 0, "cap LLM round-trips per run (0 = engine default)")
	fs.DurationVar(&o.timeout, "timeout", 0, "wall-clock cap per run (0 = engine default)")
	fs.IntVar(&o.historyWindow, "history-window", 0, "prior steps rehydrated into context (0 = engine default; overrides --context-window's derived value)")
	fs.IntVar(&o.contextWindow, "context-window", 0, "the model's SERVED context size in tokens — sizes the prompt budget, compaction, and log limits to it (0 = assume a large window)")
	fs.StringVar(&o.session, "session", "", "session id to run under (default: generated)")
	fs.StringVar(&o.cwd, "cwd", "", "workspace root for AGENTS.md / SKILL.md discovery (default: current directory)")
	fs.StringVar(&o.db, "db", "", "session database path (default: $AGENTLOOP_DB, else a per-user location)")
	fs.BoolVar(&o.resume, "continue", false, "resume the most recently updated session")
	fs.BoolVar(&o.ephemeral, "ephemeral", false, "keep this session in memory only — nothing is written to the database")
	fs.BoolVar(&o.asJSON, "json", false, "emit newline-delimited JSON events on stdout")
	fs.BoolVar(&o.jsonStream, "json-stream", false, "in --json mode, also emit response_chunk events")
	fs.BoolVar(&o.quiet, "quiet", false, "suppress the live activity stream on stderr")
	fs.StringVar(&o.approve, "approve", "", "how to answer permission prompts: prompt, auto, or deny (default: prompt at a terminal, deny otherwise)")
	fs.BoolVar(&o.noNetwork, "no-network", false, "remove fetch() and require('http') — the agent can read the workspace but cannot send it anywhere")
	fs.BoolVar(&o.noExec, "no-exec", false, "remove exec() — the agent cannot run commands at all")
	fs.StringVar(&o.allowExec, "allow-exec", "", "comma-separated command names to run without asking, e.g. go,git")
	fs.BoolVar(&o.allowShell, "dangerously-allow-shell", false, "let exec() take a shell string as well as an argv array")
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
	if o.maxSteps < 0 || o.historyWindow < 0 || o.timeout < 0 || o.contextWindow < 0 {
		return errors.New("--max-steps, --history-window, --timeout and --context-window cannot be negative")
	}

	approval, err := resolveApprovalMode(o.approve, isTerminal(os.Stdin))
	if err != nil {
		return err
	}
	o.approval = approval

	o.execNames = splitList(o.allowExec)
	if o.noExec && (len(o.execNames) > 0 || o.allowShell) {
		return errors.New("--no-exec removes exec() entirely, so there is nothing for --allow-exec or --dangerously-allow-shell to permit")
	}
	return nil
}

// splitList parses a comma-separated flag value, ignoring empty entries
// so "go,,git" and a trailing comma both mean what they look like.
func splitList(v string) []string {
	var out []string
	for _, part := range strings.Split(v, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// policy is the PolicyChecker for this invocation.
//
// The loop installs a zero-value DefaultPolicy when none is configured,
// which is the right shape but cannot carry the command grants
// --allow-exec expresses, so one is built explicitly here.
func (o *options) policy() sandbox.PolicyChecker {
	return sandbox.DefaultPolicy{AllowCommands: o.execNames}
}

// resolveApprovalMode settles how permission prompts get answered.
//
// The default depends on whether anyone is there to answer: prompt at a
// terminal, deny otherwise. Denying is the right unattended default — a
// run driven by a script must fail closed rather than block forever on
// an answer nobody will give, and a capability refused is recoverable in
// a way an approval nobody saw is not.
//
// Asking for prompting with no terminal is a usage error rather than a
// silent downgrade: the caller stated an expectation the environment
// cannot meet, and quietly doing something else would hide that.
func resolveApprovalMode(requested string, hasTerminal bool) (approvalMode, error) {
	mode, err := parseApprovalMode(requested)
	if err != nil {
		return "", err
	}
	if mode == "" {
		if hasTerminal {
			return approvalPrompt, nil
		}
		return approvalDeny, nil
	}
	if mode == approvalPrompt && !hasTerminal {
		return "", errors.New("--approve prompt needs a terminal to ask at; use auto or deny when stdin is redirected")
	}
	return mode, nil
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

	// root is the workspace handle every file primitive resolves against.
	root *os.Root
}

// close releases the session database and the workspace handle.
func (s *session) close() {
	if s.store != nil {
		s.store.Close()
	}
	if s.root != nil {
		s.root.Close()
	}
}

// newSession builds the loop for these options. warn receives non-fatal
// setup problems — a partially readable AGENTS.md tree still yields the
// docs that did load, and that is worth a warning rather than an exit.
//
// in is the reader permission prompts are answered on. It is passed in
// rather than opened here so it can be the SAME reader the chat REPL
// reads messages from: two independent buffered readers over one stdin
// would race for buffered bytes, and an approval prompt would swallow
// the user's next message.
func newSession(o *options, warn func(string), in *bufio.Reader) (*session, error) {
	client, err := llm.NewOpenAI(llm.ConfigFromEnv())
	if err != nil {
		return nil, err
	}

	// Project instructions and skills are layered capabilities: the loop
	// knows nothing about either. The chat surface discovers them and
	// passes one as prompt text and the other as capabilities.
	//
	// InlineBytes only governs RenderCatalog, so setting it here is inert
	// until `sized` switches the render mode below.
	docs, err := projectctx.Loader{InlineBytes: o.profile().ProjectInlineBytes}.Load(o.cwd)
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

	// The workspace root is opened once and held for the session. os.Root
	// resolves every path against this handle, so ".." and symlinks
	// cannot reach outside it — confinement enforced by the runtime
	// rather than by path arithmetic.
	root, err := os.OpenRoot(o.cwd)
	if err != nil {
		if store != nil {
			store.Close()
		}
		return nil, fmt.Errorf("cannot open the workspace %s: %w", o.cwd, err)
	}

	// Sized runs excerpt the instruction files and let the agent pull the
	// rest with projectGet().
	//
	// Not merely to save tokens: RunRequest.Context is appended to the
	// SYSTEM prompt, and the budget never drops that — it is what
	// carries the run()/answer() protocol and the whole tool surface. So
	// an oversized AGENTS.md is not a prompt that gets trimmed, it is a
	// prompt that cannot be sent, and no budget setting rescues it. The
	// excerpt is the only thing that does.
	projectCx := projectctx.Render(docs)
	if o.sized() {
		projectCx = projectctx.RenderCatalog(docs)
	}

	sess := &session{projectCx: projectCx, id: id, store: store, root: root}

	// The prompter reads the session id through a closure because chat's
	// /new changes it mid-process, and an approval must not carry across
	// that boundary into a session the user meant to start clean.
	prompter := newTerminalPrompter(in, os.Stderr, o.approval, store, func() string { return sess.id })
	caps := buildCapabilities(o, client, prompter, root, warn)
	caps = append(caps, skills.Capabilities(sk)...)
	if o.sized() {
		// RenderCatalog writes pointers to projectGet(); without these
		// capabilities those pointers name a function that does not exist.
		caps = append(caps, projectctx.Capabilities(docs)...)
	}

	sess.loop = agentloop.New(o.loopConfig(client, caps, sessions, steps))
	return sess, nil
}

// profile is the context settings --context-window implies. Pure in o,
// so every caller derives the same numbers without them having to be
// threaded around.
//
// With the flag unset this is the zero Profile: its Apply does nothing
// and its byte caps are zero, which every package reads as "use your
// default" — so an unsized run is configured exactly as it was before
// the flag existed.
func (o *options) profile() agentloop.Profile { return agentloop.ProfileFor(o.contextWindow) }

// sized reports whether the user told us the model's context window.
// It gates the settings a profile cannot express as a number — whether
// to compact at all, and whether instruction files are excerpted or
// inlined whole.
func (o *options) sized() bool { return o.contextWindow > 0 }

// loopConfig assembles the engine configuration for one invocation.
//
// Split out of newSession because it is the part worth testing on its
// own: newSession needs a provider, a database and a workspace, while
// what the flags settle is exactly this struct.
func (o *options) loopConfig(client llm.Client, caps []agentloop.Capability, sessions agentloop.SessionStore, steps agentloop.StepStore) agentloop.Config {
	cfg := agentloop.Config{
		LLM:      client,
		Sessions: sessions,
		Steps:    steps,
		SandboxBuilder: &agentloop.DefaultSandboxBuilder{
			Capabilities: caps,
			MaxLogBytes:  o.profile().MaxLogBytes,
		},
		Policy:        o.policy(),
		Model:         o.model,
		MaxIterations: o.maxSteps,
		RunTimeout:    o.timeout,
		HistoryWindow: o.historyWindow,
	}
	if o.sized() {
		// Apply configures a *SummarizingCompactor's thresholds but will
		// not install one, so a sized run has to. Without it the budget
		// would drop old turns that compaction could have kept in
		// summary — the budget only guarantees the request is sendable,
		// it is the compactor that preserves meaning. Summarizing on the
		// same client is right for the local-server case this flag
		// exists for; the library API is where a different summarizer
		// goes.
		//
		// Installed BEFORE Apply, not after: Apply configures whatever
		// compactor it finds, so one installed afterwards would keep the
		// frontier-sized defaults this invocation just said it does not
		// have. No "don't clobber an existing one" guard is needed —
		// loopConfig builds the Config it returns, so there is never one
		// to clobber.
		cfg.Compactor = &agentloop.SummarizingCompactor{LLM: client}
	}
	o.profile().Apply(&cfg)
	// Apply overwrites HistoryWindow with the profile's derived value, so
	// an explicit --history-window is restored afterwards: a number the
	// user typed outranks one the profile inferred.
	if o.historyWindow > 0 {
		cfg.HistoryWindow = o.historyWindow
	}
	return cfg
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

// isTerminal reports whether f is a terminal someone could be typing at.
//
// This asks the terminal driver, rather than testing for a character
// device. /dev/null IS a character device, so the cheaper check calls
// `agentloop run < /dev/null` interactive — precisely the unattended
// invocation that must fail closed instead of stopping to ask a question
// nobody will answer.
func isTerminal(f *os.File) bool {
	return term.IsTerminal(int(f.Fd()))
}
