package ext_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mind-vm/agentloop/ext"
	"github.com/mind-vm/agentloop/sandbox"
)

// execAt builds a sandbox with the exec pack rooted at a temp directory.
func execAt(t *testing.T, opts ext.ExecOptions, policy sandbox.PolicyChecker) (*sandbox.Sandbox, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("these exercise POSIX shell behaviour")
	}
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh is not available")
	}

	dir := t.TempDir()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	t.Cleanup(func() { root.Close() })

	sb := sandbox.New(ext.ExecPack(context.Background(), root, opts))
	if policy != nil {
		sb.SetPolicy(policy)
	}
	return sb, dir
}

func TestExecReturnsOutputAndExitCode(t *testing.T) {
	sb, _ := execAt(t, ext.ExecOptions{}, sandbox.AllowAll)

	got := run(t, sb, `return exec(["echo", "hello"]).stdout.trim();`)
	if got != `"hello"` {
		t.Errorf("stdout = %s", got)
	}
	if got := run(t, sb, `return exec(["true"]).code;`); got != "0" {
		t.Errorf("code = %s, want 0", got)
	}
}

// A failing build is the information the agent asked for. Turning it
// into a thrown error would push scripts into try/catch around the one
// call whose failure they most need to read.
func TestNonZeroExitIsAResultNotAnError(t *testing.T) {
	sb, _ := execAt(t, ext.ExecOptions{}, sandbox.AllowAll)

	got := run(t, sb, `const r = exec(["sh", "-c", "echo out; echo err >&2; exit 3"]);
	return { code: r.code, out: r.stdout.trim(), err: r.stderr.trim(), timedOut: r.timedOut };`)
	if got != `{"code":3,"err":"err","out":"out","timedOut":false}` {
		t.Errorf("got %s", got)
	}
}

// A command that could not start at all is a mistake to fix, not an
// outcome to report.
func TestMissingBinaryThrows(t *testing.T) {
	sb, _ := execAt(t, ext.ExecOptions{}, sandbox.AllowAll)
	if msg := runErr(t, sb, `return exec(["definitely-not-a-real-binary-xyz"]);`); !strings.Contains(msg, "exec") {
		t.Errorf("got %q", msg)
	}
}

func TestExecStdin(t *testing.T) {
	sb, _ := execAt(t, ext.ExecOptions{}, sandbox.AllowAll)
	got := run(t, sb, `return exec(["cat"], { input: "piped in" }).stdout;`)
	if got != `"piped in"` {
		t.Errorf("got %s", got)
	}
}

// --- the environment ------------------------------------------------

// The agent's own credentials are in this process's environment. The
// child gets an allowlist, so a variable nobody thought to strip is
// absent by construction rather than by remembering.
func TestChildEnvironmentIsScrubbed(t *testing.T) {
	t.Setenv("AGENTLOOP_TEST_SECRET", "s3cr3t-value")
	t.Setenv("OPENAI_API_KEY", "sk-test-must-not-leak")

	sb, _ := execAt(t, ext.ExecOptions{}, sandbox.AllowAll)

	got := run(t, sb, `return exec(["sh", "-c", "echo ${AGENTLOOP_TEST_SECRET:-ABSENT}; echo ${OPENAI_API_KEY:-ABSENT}"]).stdout;`)
	if strings.Contains(got, "s3cr3t-value") || strings.Contains(got, "sk-test-must-not-leak") {
		t.Fatalf("a secret reached the child: %s", got)
	}
	if strings.Count(got, "ABSENT") != 2 {
		t.Errorf("want both variables absent, got %s", got)
	}

	// The whole environment, checked wholesale.
	all := run(t, sb, `return exec(["env"]).stdout;`)
	if strings.Contains(all, "s3cr3t-value") || strings.Contains(all, "sk-test-must-not-leak") {
		t.Fatalf("a secret appeared in the child's environment")
	}
}

// PATH has to survive, or nothing is findable.
func TestUsefulEnvironmentSurvives(t *testing.T) {
	sb, _ := execAt(t, ext.ExecOptions{}, sandbox.AllowAll)
	if got := run(t, sb, `return exec(["sh", "-c", "test -n \"$PATH\" && echo yes"]).stdout.trim();`); got != `"yes"` {
		t.Errorf("PATH should reach the child, got %s", got)
	}
}

func TestExplicitEnvIsPassed(t *testing.T) {
	sb, _ := execAt(t, ext.ExecOptions{}, sandbox.AllowAll)
	got := run(t, sb, `return exec(["sh", "-c", "echo $GREETING"], { env: { GREETING: "hi there" } }).stdout.trim();`)
	if got != `"hi there"` {
		t.Errorf("got %s", got)
	}
}

// --- output bounds --------------------------------------------------

// A command whose pipe stops being drained blocks forever, so the cap
// must discard rather than stop reading.
func TestLargeOutputIsCappedWithoutHanging(t *testing.T) {
	sb, _ := execAt(t, ext.ExecOptions{MaxOutputBytes: 2048}, sandbox.AllowAll)

	done := make(chan string, 1)
	go func() {
		done <- run(t, sb, `return exec(["sh", "-c", "for i in $(seq 1 20000); do echo aaaaaaaaaaaaaaaaaaaaaaaaaaaaaa; done"]).stdout;`)
	}()

	select {
	case got := <-done:
		var text string
		if err := json.Unmarshal([]byte(got), &text); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if !strings.Contains(text, "truncated") {
			t.Errorf("truncated output must say so, got %d bytes ending %q", len(text), tail(text, 80))
		}
		if len(text) > 4096 {
			t.Errorf("kept %d bytes, want roughly the 2048 cap", len(text))
		}
	case <-time.After(30 * time.Second):
		t.Fatal("a command producing more output than the cap hung the run")
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}

// --- timeouts and orphans -------------------------------------------

func TestTimeoutIsReportedNotThrown(t *testing.T) {
	sb, _ := execAt(t, ext.ExecOptions{}, sandbox.AllowAll)

	start := time.Now()
	got := run(t, sb, `const r = exec(["sleep", "30"], { timeoutMs: 500 });
	return { timedOut: r.timedOut, code: r.code };`)
	elapsed := time.Since(start)

	if !strings.Contains(got, `"timedOut":true`) {
		t.Errorf("got %s", got)
	}
	if elapsed > 20*time.Second {
		t.Errorf("the timeout took %s to take effect", elapsed)
	}
}

// A script cannot raise its own ceiling past what the caller allowed.
func TestTimeoutIsClamped(t *testing.T) {
	sb, _ := execAt(t, ext.ExecOptions{MaxTimeout: 500 * time.Millisecond}, sandbox.AllowAll)

	start := time.Now()
	run(t, sb, `return exec(["sleep", "30"], { timeoutMs: 600000 }).timedOut;`)
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Errorf("a script raised its own timeout past the ceiling: took %s", elapsed)
	}
}

// The one the plan flagged: a timeout must reclaim what the command
// spawned. Otherwise the compiler or test binary it started keeps
// running, still holding the pipes, and the run appears to hang after
// the thing that hung was already killed.
func TestTimeoutKillsTheWholeProcessGroup(t *testing.T) {
	sb, dir := execAt(t, ext.ExecOptions{}, sandbox.AllowAll)
	marker := filepath.Join(dir, "orphan-survived")

	// A grandchild that outlives its parent and leaves evidence if it
	// is never killed.
	script := `sh -c 'sleep 3; touch ` + marker + `' & sleep 30`
	run(t, sb, `return exec(["sh", "-c", `+strconvQuote(script)+`], { timeoutMs: 500 }).timedOut;`)

	// Well past when the orphan would have fired.
	time.Sleep(5 * time.Second)

	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a process the command spawned survived the timeout")
	}
}

func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// --- the shell ------------------------------------------------------

func TestStringFormNeedsTheShellFlag(t *testing.T) {
	sb, _ := execAt(t, ext.ExecOptions{}, sandbox.AllowAll)
	msg := runErr(t, sb, `return exec("echo hi | wc -l");`)
	if !strings.Contains(msg, "array") {
		t.Errorf("the error should say to pass an array, got %q", msg)
	}
}

func TestStringFormWorksWhenAllowed(t *testing.T) {
	sb, _ := execAt(t, ext.ExecOptions{AllowShell: true}, sandbox.AllowAll)
	got := run(t, sb, `return exec("echo one two three | wc -w").stdout.trim();`)
	if got != `"3"` {
		t.Errorf("got %s", got)
	}
}

// --- cwd ------------------------------------------------------------

func TestCwdResolvesInsideTheRoot(t *testing.T) {
	sb, dir := execAt(t, ext.ExecOptions{}, sandbox.AllowAll)
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	got := run(t, sb, `return exec(["pwd"], { cwd: "sub" }).stdout.trim();`)
	if !strings.HasSuffix(strings.Trim(got, `"`), "/sub") {
		t.Errorf("got %s, want a path ending in /sub", got)
	}
}

func TestCwdOutsideTheRootIsRefused(t *testing.T) {
	sb, _ := execAt(t, ext.ExecOptions{}, sandbox.AllowAll)
	for _, expr := range []string{
		`exec(["pwd"], { cwd: ".." })`,
		`exec(["pwd"], { cwd: "/etc" })`,
		`exec(["pwd"], { cwd: "nope" })`,
	} {
		if msg := runErr(t, sb, "return "+expr+";"); !strings.Contains(msg, "exec") {
			t.Errorf("%s: got %q", expr, msg)
		}
	}
}

// --- gating ---------------------------------------------------------

func TestExecIsDeniedByTheDefaultPolicy(t *testing.T) {
	sb, _ := execAt(t, ext.ExecOptions{}, sandbox.DefaultPolicy{})
	if msg := runErr(t, sb, `return exec(["echo", "hi"]);`); !strings.Contains(msg, "exec") {
		t.Errorf("got %q", msg)
	}
}

// AllowCommands is the difference between "may run the build" and "may
// run anything".
func TestAllowCommandsGrantsByBasename(t *testing.T) {
	approver := &recordingApprover{answer: false}
	sb, _ := execAt(t, ext.ExecOptions{Approver: approver},
		sandbox.DefaultPolicy{AllowCommands: []string{"echo"}})

	if got := run(t, sb, `return exec(["echo", "allowed"]).stdout.trim();`); got != `"allowed"` {
		t.Errorf("got %s", got)
	}
	if len(approver.asked) != 0 {
		t.Errorf("an allowed command should not prompt: %v", approver.asked)
	}

	// A command not on the list still has to be approved.
	runErr(t, sb, `return exec(["true"]);`)
	if len(approver.asked) != 1 {
		t.Errorf("want one prompt for the unlisted command, got %v", approver.asked)
	}
}

func TestDeniedExecIsOfferedToTheUser(t *testing.T) {
	approver := &recordingApprover{answer: true}
	sb, _ := execAt(t, ext.ExecOptions{Approver: approver}, sandbox.DefaultPolicy{})

	if got := run(t, sb, `return exec(["echo", "hi"]).stdout.trim();`); got != `"hi"` {
		t.Errorf("got %s", got)
	}
	if len(approver.asked) != 1 {
		t.Fatalf("asked %v", approver.asked)
	}
	// The arguments are most of what there is to judge, so the prompt
	// shows the whole command, not just the program.
	if !strings.Contains(approver.asked[0], "echo hi") {
		t.Errorf("the prompt should show the full command, got %q", approver.asked[0])
	}
}

func TestRefusedExecDoesNotRun(t *testing.T) {
	approver := &recordingApprover{answer: false}
	sb, dir := execAt(t, ext.ExecOptions{Approver: approver}, sandbox.DefaultPolicy{})

	marker := filepath.Join(dir, "should-not-exist")
	msg := runErr(t, sb, `return exec(["touch", `+strconvQuote(marker)+`]);`)
	if !strings.Contains(msg, "declined") {
		t.Errorf("got %q", msg)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a refused command ran anyway")
	}
}
