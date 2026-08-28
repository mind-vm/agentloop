package ext_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mind-vm/agentloop/ext"
	"github.com/mind-vm/agentloop/sandbox"
)

// workspaceAt builds a sandbox with the workspace pack rooted at a
// temp directory seeded with files. It runs against the real goja
// runtime, because how Go values cross that boundary is exactly what
// these tests are for.
func workspaceAt(t *testing.T, files map[string]string, opts ext.WorkspaceOptions, policy sandbox.PolicyChecker) (*sandbox.Sandbox, string) {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		full := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatalf("OpenRoot: %v", err)
	}
	t.Cleanup(func() { root.Close() })

	sb := sandbox.New(ext.WorkspacePack(context.Background(), root, opts))
	if policy != nil {
		sb.SetPolicy(policy)
	}
	return sb, dir
}

// run executes one turn and returns what run() returned, as JSON —
// which is exactly what the loop threads forward, so asserting on it
// asserts on what the next turn would actually see.
func run(t *testing.T, sb *sandbox.Sandbox, script string) string {
	t.Helper()
	_, ret, err := sb.ExecuteTurn("function run(args) {\n"+script+"\n}", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("execute: %v\nscript:\n%s", err, script)
	}
	return string(ret)
}

// runErr executes a turn expected to throw, returning the message.
func runErr(t *testing.T, sb *sandbox.Sandbox, script string) string {
	t.Helper()
	_, _, err := sb.ExecuteTurn("function run(args) {\n"+script+"\n}", json.RawMessage(`{}`))
	if err == nil {
		t.Fatalf("want an error from:\n%s", script)
	}
	return err.Error()
}

var sampleTree = map[string]string{
	"README.md":         "# Project\n\nHello.\n",
	"src/main.go":       "package main\n\nfunc main() {\n\tprintln(\"hi\")\n}\n",
	"src/util.go":       "package main\n\nfunc helper() int { return 7 }\n",
	"src/deep/thing.go": "package deep\n\nfunc Thing() {}\n",
	"docs/guide.md":     "guide\n",
	".git/config":       "[core]\n\tbare = false\n",
}

// --- reading --------------------------------------------------------

func TestReadFile(t *testing.T) {
	sb, _ := workspaceAt(t, sampleTree, ext.WorkspaceOptions{}, sandbox.AllowAll)
	got := run(t, sb, `return readFile("README.md");`)
	if !strings.Contains(got, "# Project") {
		t.Errorf("got %s", got)
	}
}

// The options object is the binding most likely to be wrong: goja maps
// JS keys onto Go field names, not json tags, so these are read from a
// plain map. This asserts the documented lowercase interface works.
func TestReadFileLineWindow(t *testing.T) {
	sb, _ := workspaceAt(t, map[string]string{
		"n.txt": "one\ntwo\nthree\nfour\nfive\n",
	}, ext.WorkspaceOptions{}, sandbox.AllowAll)

	got := run(t, sb, `return readFile("n.txt", { offset: 2, limit: 2 });`)
	if got != `"two\nthree"` {
		t.Errorf("got %s, want lines 2 and 3", got)
	}

	// An offset past the end is a fact about the file, not an error.
	if got := run(t, sb, `return readFile("n.txt", { offset: 900 });`); got != `""` {
		t.Errorf("offset past the end: got %s, want empty", got)
	}
}

func TestReadFileTruncatesLargeFiles(t *testing.T) {
	big := strings.Repeat("0123456789abcdef\n", 400) // ~6.8 KB
	sb, _ := workspaceAt(t, map[string]string{"big.txt": big},
		ext.WorkspaceOptions{MaxFileBytes: 1000}, sandbox.AllowAll)

	got := run(t, sb, `return readFile("big.txt");`)
	if !strings.Contains(got, "truncated") {
		t.Errorf("a truncated read must say so, got %.200s", got)
	}
	// The last line handed back must be whole — half a line of code
	// reads as real and is not.
	var text string
	if err := json.Unmarshal([]byte(got), &text); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	body := text[:strings.Index(text, "[truncated")]
	for _, line := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		if line != "0123456789abcdef" {
			t.Fatalf("truncation cut mid-line: %q", line)
		}
	}
}

func TestReadFileRejectsDirectory(t *testing.T) {
	sb, _ := workspaceAt(t, sampleTree, ext.WorkspaceOptions{}, sandbox.AllowAll)
	if msg := runErr(t, sb, `return readFile("src");`); !strings.Contains(msg, "listDir") {
		t.Errorf("the error should point at listDir, got %q", msg)
	}
}

func TestListDir(t *testing.T) {
	sb, _ := workspaceAt(t, sampleTree, ext.WorkspaceOptions{}, sandbox.AllowAll)

	// The documented keys are lowercase; this fails loudly if the
	// Go→JS conversion ever starts using field names.
	got := run(t, sb, `return listDir("src").map(e => e.name + (e.dir ? "/" : "")).sort();`)
	if got != `["deep/","main.go","util.go"]` {
		t.Errorf("got %s", got)
	}

	if got := run(t, sb, `return listDir("src").find(e => e.name === "main.go").path;`); got != `"src/main.go"` {
		t.Errorf("path should be relative to the root, got %s", got)
	}
	if got := run(t, sb, `return listDir("src").find(e => e.name === "main.go").size > 0;`); got != "true" {
		t.Errorf("size should be populated, got %s", got)
	}
}

// .git is not source, is frequently enormous, and its config can hold
// credentials.
func TestListDirSkipsGit(t *testing.T) {
	sb, _ := workspaceAt(t, sampleTree, ext.WorkspaceOptions{}, sandbox.AllowAll)
	if got := run(t, sb, `return listDir().map(e => e.name).includes(".git");`); got != "false" {
		t.Errorf(".git should not be listed, got %s", got)
	}
}

// --- confinement ----------------------------------------------------

func TestPathsCannotLeaveTheRoot(t *testing.T) {
	sb, dir := workspaceAt(t, sampleTree, ext.WorkspaceOptions{}, sandbox.AllowAll)

	// Something real and readable outside the root, to be sure a
	// "not found" is not doing the work a refusal should.
	outside := filepath.Join(filepath.Dir(dir), "outside.txt")
	if err := os.WriteFile(outside, []byte("SECRET"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Cleanup(func() { os.Remove(outside) })

	for _, attempt := range []string{
		`readFile("../outside.txt")`,
		`readFile("src/../../outside.txt")`,
		`readFile("/etc/hosts")`,
		`readFile("../../../../etc/hosts")`,
		`listDir("..")`,
		`writeFile("../escaped.txt", "x")`,
	} {
		t.Run(attempt, func(t *testing.T) {
			msg := runErr(t, sb, "return "+attempt+";")
			if strings.Contains(msg, "SECRET") {
				t.Fatalf("content from outside the root leaked: %q", msg)
			}
		})
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escaped.txt")); err == nil {
		t.Fatal("a write escaped the root")
	}
}

// A symlink pointing out of the workspace is the classic escape, and
// the reason confinement is delegated to os.Root rather than to path
// arithmetic here.
func TestSymlinkCannotEscape(t *testing.T) {
	sb, dir := workspaceAt(t, sampleTree, ext.WorkspaceOptions{}, sandbox.AllowAll)

	secret := filepath.Join(filepath.Dir(dir), "secret.txt")
	if err := os.WriteFile(secret, []byte("SECRET"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	t.Cleanup(func() { os.Remove(secret) })
	if err := os.Symlink(secret, filepath.Join(dir, "link.txt")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	msg := runErr(t, sb, `return readFile("link.txt");`)
	if strings.Contains(msg, "SECRET") {
		t.Fatalf("a symlink read outside the root: %q", msg)
	}
}

// --- glob and grep --------------------------------------------------

func TestGlob(t *testing.T) {
	sb, _ := workspaceAt(t, sampleTree, ext.WorkspaceOptions{}, sandbox.AllowAll)

	cases := map[string]string{
		`glob("*.md")`:          `["README.md"]`,
		`glob("src/*.go")`:      `["src/main.go","src/util.go"]`,
		`glob("**/*.go")`:       `["src/deep/thing.go","src/main.go","src/util.go"]`,
		`glob("**/*.md")`:       `["README.md","docs/guide.md"]`,
		`glob("src/**/*.go")`:   `["src/deep/thing.go","src/main.go","src/util.go"]`,
		`glob("**/thing.go")`:   `["src/deep/thing.go"]`,
		`glob("nothing/*.zzz")`: `[]`,
	}
	for expr, want := range cases {
		t.Run(expr, func(t *testing.T) {
			if got := run(t, sb, "return "+expr+".sort();"); got != want {
				t.Errorf("got %s, want %s", got, want)
			}
		})
	}
}

func TestGlobSkipsGit(t *testing.T) {
	sb, _ := workspaceAt(t, sampleTree, ext.WorkspaceOptions{}, sandbox.AllowAll)
	if got := run(t, sb, `return glob("**").filter(p => p.indexOf(".git") === 0);`); got != "[]" {
		t.Errorf(".git should not be walked, got %s", got)
	}
}

func TestGrep(t *testing.T) {
	sb, _ := workspaceAt(t, sampleTree, ext.WorkspaceOptions{}, sandbox.AllowAll)

	got := run(t, sb, `return grep("func \\w+").map(h => h.path + ":" + h.line).sort();`)
	want := `["src/deep/thing.go:3","src/main.go:3","src/util.go:3"]`
	if got != want {
		t.Errorf("got %s, want %s", got, want)
	}

	if got := run(t, sb, `return grep("helper", { glob: "src/*.go" }).map(h => h.path);`); got != `["src/util.go"]` {
		t.Errorf("glob filter: got %s", got)
	}
	if got := run(t, sb, `return grep("HELPER", { ignoreCase: true }).length;`); got != "1" {
		t.Errorf("ignoreCase: got %s, want 1", got)
	}
	if got := run(t, sb, `return grep("func", { limit: 2 }).length;`); got != "2" {
		t.Errorf("limit: got %s, want 2", got)
	}
	if got := run(t, sb, `return grep("println").map(h => h.text.trim());`); got != `["println(\"hi\")"]` {
		t.Errorf("text: got %s", got)
	}
}

func TestGrepSkipsBinaryFiles(t *testing.T) {
	sb, _ := workspaceAt(t, map[string]string{
		"text.txt": "findme here\n",
		"blob.bin": "findme\x00\x01\x02 here\n",
	}, ext.WorkspaceOptions{}, sandbox.AllowAll)

	if got := run(t, sb, `return grep("findme").map(h => h.path);`); got != `["text.txt"]` {
		t.Errorf("binary files should be skipped, got %s", got)
	}
}

func TestGrepRejectsABadPattern(t *testing.T) {
	sb, _ := workspaceAt(t, sampleTree, ext.WorkspaceOptions{}, sandbox.AllowAll)
	if msg := runErr(t, sb, `return grep("func (");`); !strings.Contains(msg, "grep") {
		t.Errorf("got %q", msg)
	}
}

// --- mutation -------------------------------------------------------

func TestWriteFile(t *testing.T) {
	sb, dir := workspaceAt(t, sampleTree, ext.WorkspaceOptions{}, sandbox.AllowAll)

	if got := run(t, sb, `return writeFile("notes.md", "hello\n");`); got != "true" {
		t.Fatalf("got %s", got)
	}
	assertFile(t, dir, "notes.md", "hello\n")

	// Parent directories are made, or the agent would need a mkdir
	// primitive that does not exist.
	run(t, sb, `return writeFile("a/b/c/deep.txt", "nested\n");`)
	assertFile(t, dir, "a/b/c/deep.txt", "nested\n")

	// And it replaces.
	run(t, sb, `return writeFile("notes.md", "replaced\n");`)
	assertFile(t, dir, "notes.md", "replaced\n")
}

func TestEditFile(t *testing.T) {
	sb, dir := workspaceAt(t, map[string]string{
		"f.txt": "alpha\nbeta\ngamma\n",
	}, ext.WorkspaceOptions{}, sandbox.AllowAll)

	if got := run(t, sb, `return editFile("f.txt", "beta", "BETA");`); got != "1" {
		t.Fatalf("got %s, want 1 replacement", got)
	}
	assertFile(t, dir, "f.txt", "alpha\nBETA\ngamma\n")
}

// An edit that matches nothing, or matches in more places than the
// caller realised, is a mistaken edit. Refusing costs one turn; making
// it costs a wrong file the agent believes is right.
func TestEditFileRefusesAmbiguousAndMissingMatches(t *testing.T) {
	sb, dir := workspaceAt(t, map[string]string{
		"f.txt": "x\nsame\nsame\ny\n",
	}, ext.WorkspaceOptions{}, sandbox.AllowAll)

	msg := runErr(t, sb, `return editFile("f.txt", "nope", "z");`)
	if !strings.Contains(msg, "does not contain") {
		t.Errorf("missing match: got %q", msg)
	}

	msg = runErr(t, sb, `return editFile("f.txt", "same", "z");`)
	if !strings.Contains(msg, "appears 2 times") {
		t.Errorf("ambiguous match: got %q", msg)
	}
	// Neither attempt may have written anything.
	assertFile(t, dir, "f.txt", "x\nsame\nsame\ny\n")

	if got := run(t, sb, `return editFile("f.txt", "same", "z", { all: true });`); got != "2" {
		t.Errorf("all: got %s, want 2", got)
	}
	assertFile(t, dir, "f.txt", "x\nz\nz\ny\n")
}

// An edit should not quietly make a script non-executable.
func TestEditFilePreservesMode(t *testing.T) {
	sb, dir := workspaceAt(t, map[string]string{"s.sh": "echo old\n"}, ext.WorkspaceOptions{}, sandbox.AllowAll)
	script := filepath.Join(dir, "s.sh")
	if err := os.Chmod(script, 0o755); err != nil {
		t.Fatalf("chmod: %v", err)
	}

	run(t, sb, `return editFile("s.sh", "old", "new");`)

	info, err := os.Stat(script)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Errorf("mode = %o, want 755", info.Mode().Perm())
	}
}

func assertFile(t *testing.T, dir, name, want string) {
	t.Helper()
	got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	if string(got) != want {
		t.Errorf("%s = %q, want %q", name, got, want)
	}
}

// --- gating ---------------------------------------------------------

// recordingApprover answers a fixed way and remembers what it was asked.
type recordingApprover struct {
	answer bool
	err    error
	asked  []string
}

func (r *recordingApprover) Request(req sandbox.InputRequestData) (any, error) {
	r.asked = append(r.asked, req.Message)
	if r.err != nil {
		return nil, r.err
	}
	return r.answer, nil
}

// Reads are ungated by design: confined to the root they cannot reach
// anything the user did not point the agent at, and a prompt per file
// trains people to approve without reading.
func TestReadsAreNotGated(t *testing.T) {
	approver := &recordingApprover{answer: false}
	sb, _ := workspaceAt(t, sampleTree, ext.WorkspaceOptions{Approver: approver}, sandbox.DefaultPolicy{})

	for _, expr := range []string{`readFile("README.md")`, `listDir(".")`, `glob("**/*.go")`, `grep("func")`} {
		if _, _, err := sb.ExecuteTurn("function run(args) { return "+expr+"; }", []byte(`{}`)); err != nil {
			t.Errorf("%s should not need permission: %v", expr, err)
		}
	}
	if len(approver.asked) != 0 {
		t.Errorf("reads asked for permission: %v", approver.asked)
	}
}

func TestWritesAreDeniedByTheDefaultPolicy(t *testing.T) {
	// No approver: the documented "no human available" case.
	sb, dir := workspaceAt(t, sampleTree, ext.WorkspaceOptions{}, sandbox.DefaultPolicy{})

	if msg := runErr(t, sb, `return writeFile("new.txt", "x");`); !strings.Contains(msg, "writeFile") {
		t.Errorf("got %q", msg)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); err == nil {
		t.Fatal("a denied write still created the file")
	}
	if msg := runErr(t, sb, `return editFile("README.md", "Hello", "Goodbye");`); !strings.Contains(msg, "editFile") {
		t.Errorf("got %q", msg)
	}
	assertFile(t, dir, "README.md", sampleTree["README.md"])
}

func TestWritesProceedWhenThePolicyGrantsThem(t *testing.T) {
	approver := &recordingApprover{answer: false}
	sb, dir := workspaceAt(t, sampleTree,
		ext.WorkspaceOptions{Approver: approver},
		sandbox.DefaultPolicy{AllowTools: []string{"writeFile", "editFile"}})

	run(t, sb, `return writeFile("new.txt", "x");`)
	assertFile(t, dir, "new.txt", "x")
	// A policy that already allows it must not then ask.
	if len(approver.asked) != 0 {
		t.Errorf("a granted write should not prompt: %v", approver.asked)
	}
}

func TestDeniedWriteIsOfferedToTheUser(t *testing.T) {
	approver := &recordingApprover{answer: true}
	sb, dir := workspaceAt(t, sampleTree, ext.WorkspaceOptions{Approver: approver}, sandbox.DefaultPolicy{})

	run(t, sb, `return writeFile("new.txt", "x");`)
	assertFile(t, dir, "new.txt", "x")
	if len(approver.asked) != 1 {
		t.Fatalf("asked %d times, want 1: %v", len(approver.asked), approver.asked)
	}
	if !strings.Contains(approver.asked[0], "new.txt") {
		t.Errorf("the question should name the file: %q", approver.asked[0])
	}
}

func TestRefusedWriteDoesNotHappen(t *testing.T) {
	approver := &recordingApprover{answer: false}
	sb, dir := workspaceAt(t, sampleTree, ext.WorkspaceOptions{Approver: approver}, sandbox.DefaultPolicy{})

	msg := runErr(t, sb, `return writeFile("new.txt", "x");`)
	if !strings.Contains(msg, "declined") {
		t.Errorf("got %q", msg)
	}
	if _, err := os.Stat(filepath.Join(dir, "new.txt")); err == nil {
		t.Fatal("a refused write still created the file")
	}
}

// Both mutating primitives ask the same question about a path, so
// whatever remembers approvals covers an edit and a write alike.
func TestWriteAndEditAskTheSameQuestion(t *testing.T) {
	approver := &recordingApprover{answer: true}
	sb, _ := workspaceAt(t, sampleTree, ext.WorkspaceOptions{Approver: approver}, sandbox.DefaultPolicy{})

	run(t, sb, `return writeFile("src/main.go", "package main\n");`)
	run(t, sb, `return editFile("src/main.go", "main", "other");`)

	if len(approver.asked) != 2 {
		t.Fatalf("want two questions, got %v", approver.asked)
	}
	if approver.asked[0] != approver.asked[1] {
		t.Errorf("the same path should raise the same question, so an approval covers both:\n  %q\n  %q",
			approver.asked[0], approver.asked[1])
	}
}

// An edit that will be refused must not be counted as having happened,
// and an ambiguity must be reported before anyone is asked to approve
// something that was never going to work.
func TestAmbiguousEditIsRefusedBeforeAsking(t *testing.T) {
	approver := &recordingApprover{answer: true}
	sb, _ := workspaceAt(t, map[string]string{"f.txt": "dup\ndup\n"},
		ext.WorkspaceOptions{Approver: approver}, sandbox.DefaultPolicy{})

	runErr(t, sb, `return editFile("f.txt", "dup", "x");`)
	if len(approver.asked) != 0 {
		t.Errorf("no one should be asked to approve an edit that cannot be made: %v", approver.asked)
	}
}
