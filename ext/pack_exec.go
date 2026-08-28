package ext

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dop251/goja"

	"github.com/mind-vm/agentloop/sandbox"
)

// ExecOptions tunes ExecPack. The zero value is usable.
type ExecOptions struct {
	// Timeout is the default wall-clock cap on one command. Zero uses
	// 30s. A script may ask for less, and for more up to MaxTimeout.
	Timeout time.Duration

	// MaxTimeout is the ceiling a script cannot raise its own timeout
	// past. Zero uses 2 minutes.
	//
	// This bound is load-bearing in a way the sandbox's own execution
	// timeout is not: that one works by interrupting the JS runtime,
	// and a runtime blocked inside a Go call is not running JS to
	// interrupt. While a command runs, this is the only thing bounding
	// the turn.
	MaxTimeout time.Duration

	// MaxOutputBytes caps how much of each of stdout and stderr is kept.
	// Zero uses 64 KiB. Output past the cap is still READ and discarded
	// — a command whose pipe fills up blocks forever, so the choice is
	// between draining it and hanging.
	MaxOutputBytes int

	// PassEnv names environment variables the child inherits. Nil uses
	// baseEnvPassthrough.
	//
	// An allowlist, never a denylist: the agent's own credentials are in
	// this process's environment, and a list of things to strip is a
	// list someone has to remember to update. Anything not named here
	// simply is not there.
	PassEnv []string

	// Env is extra variables to set, applied after PassEnv.
	Env map[string]string

	// AllowShell permits the string form of exec(), which runs through
	// `sh -c`. Off by default: a shell turns a model-authored string
	// into an injection surface, and argv covers most of what an agent
	// actually needs.
	AllowShell bool

	// Approver is asked to allow a command the policy denied. Nil means
	// no human is available and a denied command simply fails.
	Approver sandbox.InputRequester
}

// baseEnvPassthrough is what a command inherits by default: enough for
// a compiler or version-control tool to behave normally, and nothing
// that carries a credential.
var baseEnvPassthrough = []string{
	"PATH", "HOME", "TMPDIR", "TMP", "TEMP",
	"LANG", "LC_ALL", "LC_CTYPE", "TZ",
	"USER", "LOGNAME", "SHELL", "TERM",
}

func (o ExecOptions) timeout() time.Duration {
	if o.Timeout <= 0 {
		return 30 * time.Second
	}
	return o.Timeout
}

func (o ExecOptions) maxTimeout() time.Duration {
	if o.MaxTimeout <= 0 {
		return 2 * time.Minute
	}
	return o.MaxTimeout
}

func (o ExecOptions) maxOutput() int {
	if o.MaxOutputBytes <= 0 {
		return 64 << 10
	}
	return o.MaxOutputBytes
}

func (o ExecOptions) passEnv() []string {
	if o.PassEnv == nil {
		return baseEnvPassthrough
	}
	return o.PassEnv
}

// ExecPack exposes exec(): run a command and get back its output and
// exit status.
//
// root gives cwd a default and a meaning — a relative cwd is resolved
// inside it. That is an ergonomic boundary, NOT a security one, and the
// distinction matters: a command runs with the full privileges of the
// user running the agent, and nothing here stops it reading or writing
// anywhere that user can. The confinement WorkspacePack gets from
// os.Root has no equivalent once another program is executing. What
// bounds this primitive is the policy check on every call, the approval
// prompt behind it, and the caller's judgement about which commands to
// allow.
func ExecPack(ctx context.Context, root *os.Root, opts ExecOptions) sandbox.Pack {
	x := &execer{ctx: ctx, root: root, opts: opts}

	shellNote := ""
	if opts.AllowShell {
		shellNote = "\n/** Passing a string runs it through the shell. */\ndeclare function exec(command: string, opts?: object): { stdout: string; stderr: string; code: number; timedOut: boolean };"
	}

	return sandbox.Pack{
		Name:        "exec",
		Description: "Run a command and read its output — exec(argv)",
		Prompt: `// --- Commands ---
/** Run a command. Pass argv as an ARRAY — there is no shell, so no quoting, pipes or globs.
 *  A non-zero exit is returned, not thrown: check .code. Needs permission per command. */
declare function exec(argv: string[], opts?: {
    cwd?: string;        // relative to the project root
    timeoutMs?: number;  // default 30000
    input?: string;      // written to the command's stdin
    env?: Record<string, string>;
}): { stdout: string; stderr: string; code: number; timedOut: boolean };` + shellNote,
		HelpEntries: map[string]string{
			"exec": `exec(argv, opts?) — Run a command and return {stdout, stderr, code, timedOut}.
  argv is an ARRAY: exec(["go", "test", "./..."]). There is no shell, so
  pipes, globs, redirection and quoting do not apply — build the arguments
  yourself. A non-zero exit is a RESULT, not an error: read .code rather
  than wrapping the call in try/catch. Output is capped and the tail is
  dropped, with a marker saying so. The command needs permission the first
  time this session runs it.
  Example: const r = exec(["go", "test", "./..."]); if (r.code !== 0) log(r.stderr);`,
		},
		Register: func(rt *goja.Runtime, sb *sandbox.Sandbox) {
			x.rt, x.sb = rt, sb
			_ = rt.Set("exec", x.exec)
		},
	}
}

type execer struct {
	ctx  context.Context
	root *os.Root
	opts ExecOptions
	rt   *goja.Runtime
	sb   *sandbox.Sandbox
}

func (x *execer) throw(format string, args ...any) {
	panic(x.rt.NewGoError(fmt.Errorf(format, args...)))
}

// exec is the JS entry point. command is an argv array, or a string
// when the shell is allowed.
func (x *execer) exec(command goja.Value, opts map[string]any) map[string]any {
	argv, display, err := x.resolveCommand(command)
	if err != nil {
		x.throw("exec: %v", err)
	}

	dir, err := x.resolveDir(optString(opts, "cwd"))
	if err != nil {
		x.throw("exec: %v", err)
	}

	timeout := x.opts.timeout()
	if ms := optInt(opts, "timeoutMs"); ms > 0 {
		timeout = time.Duration(ms) * time.Millisecond
	}
	if max := x.opts.maxTimeout(); timeout > max {
		timeout = max
	}

	x.authorize(argv[0], display)

	x.sb.Emit(sandbox.Event{Kind: sandbox.EventExec, Summary: "exec: " + display})
	result := x.run(argv, dir, timeout, optString(opts, "input"), stringMap(opts["env"]))

	summary := fmt.Sprintf("exec: %s → exit %v", display, result["code"])
	if result["timedOut"] == true {
		summary = fmt.Sprintf("exec: %s → timed out after %s", display, timeout)
	}
	x.sb.Emit(sandbox.Event{Kind: sandbox.EventExecResult, Summary: summary})
	return result
}

// resolveCommand turns the JS argument into an argv, plus a
// human-readable rendering for prompts and the trace.
func (x *execer) resolveCommand(command goja.Value) (argv []string, display string, err error) {
	if command == nil || goja.IsUndefined(command) || goja.IsNull(command) {
		return nil, "", errors.New("a command is required")
	}

	switch v := command.Export().(type) {
	case []any:
		for _, item := range v {
			s, ok := item.(string)
			if !ok {
				return nil, "", fmt.Errorf("every argument must be a string, got %T", item)
			}
			argv = append(argv, s)
		}
	case []string:
		argv = v
	case string:
		if !x.opts.AllowShell {
			return nil, "", fmt.Errorf("pass argv as an array — exec([%q]) — rather than one string; there is no shell", v)
		}
		return []string{"sh", "-c", v}, v, nil
	default:
		return nil, "", fmt.Errorf("the command must be an array of strings, got %T", v)
	}

	if len(argv) == 0 || strings.TrimSpace(argv[0]) == "" {
		return nil, "", errors.New("the command is empty")
	}
	return argv, strings.Join(argv, " "), nil
}

// resolveDir settles the working directory. A relative cwd is resolved
// inside the root and must exist there — which catches a mistyped path,
// and is not pretending to confine the command itself.
func (x *execer) resolveDir(cwd string) (string, error) {
	if x.root == nil {
		if cwd != "" {
			return "", errors.New("no project directory is configured, so cwd cannot be resolved")
		}
		return "", nil
	}
	if strings.TrimSpace(cwd) == "" {
		return x.root.Name(), nil
	}
	if strings.HasPrefix(cwd, "/") {
		return "", fmt.Errorf("cwd %q is absolute; it is resolved inside the project root", cwd)
	}
	clean := filepath.Clean(cwd)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("cwd %q leaves the project root", cwd)
	}
	info, err := x.root.Stat(clean)
	if err != nil {
		return "", fmt.Errorf("cwd: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("cwd %q is not a directory", cwd)
	}
	return filepath.Join(x.root.Name(), clean), nil
}

// authorize gates one command. The policy sees the basename, so a
// deployment can allow "go" without allowing everything; the user is
// asked about the FULL command, because the arguments are most of what
// there is to judge.
func (x *execer) authorize(command, display string) {
	res, err := x.sb.CheckPolicy(x.ctx, "exec", map[string]any{
		"command": filepath.Base(command),
		"argv":    display,
	})
	if err != nil {
		x.throw("exec: policy check failed: %v", err)
	}
	if res.Allowed {
		return
	}

	reason := res.Reason
	if reason == "" {
		reason = "denied by policy"
	}
	if x.opts.Approver == nil {
		x.sb.Emit(sandbox.Event{Kind: sandbox.EventError, Summary: "exec blocked by policy", Detail: reason})
		x.throw("exec: %s", reason)
	}

	x.sb.Emit(sandbox.Event{Kind: sandbox.EventAsk, Summary: "requesting permission to run: " + display})
	value, err := x.opts.Approver.Request(sandbox.InputRequestData{
		InputType: "confirm",
		Message:   fmt.Sprintf("Allow the agent to run: %s", display),
	})
	if err != nil {
		x.sb.Emit(sandbox.Event{Kind: sandbox.EventError, Summary: "permission request failed", Detail: err.Error()})
		x.throw("exec: %s (could not ask: %v)", reason, err)
	}
	if approved, ok := value.(bool); !ok || !approved {
		x.sb.Emit(sandbox.Event{Kind: sandbox.EventAskResult, Summary: "denied: " + display})
		x.throw("exec: the user declined to run %s", display)
	}
	x.sb.Emit(sandbox.Event{Kind: sandbox.EventAskResult, Summary: "approved: " + display})
}

// buildEnv assembles the child's environment from the allowlist plus
// any explicit additions.
func (x *execer) buildEnv(extra map[string]string) []string {
	env := make([]string, 0, len(x.opts.passEnv())+len(x.opts.Env)+len(extra))
	for _, name := range x.opts.passEnv() {
		if v, ok := os.LookupEnv(name); ok {
			env = append(env, name+"="+v)
		}
	}
	for k, v := range x.opts.Env {
		env = append(env, k+"="+v)
	}
	for k, v := range extra {
		env = append(env, k+"="+v)
	}
	return env
}

func stringMap(v any) map[string]string {
	raw, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	out := make(map[string]string, len(raw))
	for k, val := range raw {
		if s, ok := val.(string); ok {
			out[k] = s
		}
	}
	return out
}
