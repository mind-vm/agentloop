package projectctx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// repo builds a throwaway project: a directory with a .git marker,
// returning its symlink-resolved root so path comparisons hold on
// systems where the temp dir is itself a symlink (macOS /var).
func repo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	if err := os.Mkdir(filepath.Join(real, ".git"), 0o755); err != nil {
		t.Fatalf("create .git: %v", err)
	}
	return real
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", path, err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func names(docs []Doc) []string {
	out := make([]string, len(docs))
	for i, d := range docs {
		out[i] = d.Name
	}
	return out
}

// ---- locating the project ----

func TestAncestorsStopsAtRepoRoot(t *testing.T) {
	root := repo(t)
	deep := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}

	got := Ancestors(deep)
	want := []string{deep, filepath.Join(root, "a"), root}
	if len(got) != len(want) {
		t.Fatalf("Ancestors = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("Ancestors = %v, want %v (cwd-first, stopping at the root)", got, want)
		}
	}
}

func TestRootFindsGitDirAndGitFile(t *testing.T) {
	root := repo(t)
	sub := filepath.Join(root, "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if got := Root(sub); got != root {
		t.Errorf("Root = %q, want %q", got, root)
	}

	// A worktree or submodule checkout has .git as a FILE, and is just
	// as much a root as a directory .git is.
	wt := t.TempDir()
	real, err := filepath.EvalSymlinks(wt)
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(real, ".git"), "gitdir: /elsewhere/.git/worktrees/x")
	if got := Root(real); got != real {
		t.Errorf("Root with a .git file = %q, want %q", got, real)
	}
}

func TestRootWithoutRepoIsTheDirectoryItself(t *testing.T) {
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	// No .git anywhere under the temp tree, so there is no root to find
	// and the directory stands in for one.
	if got := Root(real); got != real {
		t.Errorf("Root = %q, want the directory itself %q", got, real)
	}
}

func TestResolveWithin(t *testing.T) {
	root := repo(t)
	write(t, filepath.Join(root, "inside.md"), "x")

	if _, err := ResolveWithin(root, filepath.Join(root, "inside.md")); err != nil {
		t.Errorf("in-root path rejected: %v", err)
	}
	// A path that does not exist yet still resolves, against where it
	// would land.
	if _, err := ResolveWithin(root, filepath.Join(root, "sub", "later.md")); err != nil {
		t.Errorf("not-yet-existing in-root path rejected: %v", err)
	}
	if _, err := ResolveWithin(root, filepath.Join(root, "..", "escape.md")); err == nil {
		t.Error("path above the root should be rejected")
	}
}

func TestResolveWithinRejectsSymlinkEscape(t *testing.T) {
	root := repo(t)
	outside := t.TempDir()
	target := filepath.Join(outside, "secret.md")
	write(t, target, "not yours")

	link := filepath.Join(root, "AGENTS.md")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := ResolveWithin(root, link); err == nil {
		t.Fatal("a symlink pointing outside the root should be rejected")
	}
}

// ---- Load ----

func TestLoadOrdersFromGeneralToSpecific(t *testing.T) {
	root := repo(t)
	deep := filepath.Join(root, "svc", "api")
	write(t, filepath.Join(root, "AGENTS.md"), "root rules")
	write(t, filepath.Join(root, "svc", "AGENTS.md"), "svc rules")
	write(t, filepath.Join(deep, "AGENTS.md"), "api rules")

	docs, err := Load(deep)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []string{"AGENTS.md", filepath.Join("svc", "AGENTS.md"), filepath.Join("svc", "api", "AGENTS.md")}
	got := names(docs)
	if len(got) != 3 {
		t.Fatalf("loaded %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != filepath.ToSlash(want[i]) {
			t.Fatalf("order = %v, want outermost first %v", got, want)
		}
	}
	if docs[2].Content != "api rules" {
		t.Errorf("last doc = %q, want the most specific file", docs[2].Content)
	}
	if !filepath.IsAbs(docs[0].Path) {
		t.Errorf("Path = %q, want an absolute path", docs[0].Path)
	}
}

func TestLoadSkipsMissingAndEmptyFiles(t *testing.T) {
	root := repo(t)
	sub := filepath.Join(root, "sub")
	write(t, filepath.Join(root, "AGENTS.md"), "real guidance")
	write(t, filepath.Join(sub, "AGENTS.md"), "   \n\t\n  ") // whitespace only

	docs, err := Load(sub)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("loaded %v, want only the non-empty file", names(docs))
	}
	if docs[0].Content != "real guidance" {
		t.Errorf("content = %q", docs[0].Content)
	}
}

func TestLoadReadsNoGlobalFileByDefault(t *testing.T) {
	root := repo(t)
	global := t.TempDir()
	write(t, filepath.Join(global, "AGENTS.md"), "global guidance")
	write(t, filepath.Join(root, "AGENTS.md"), "repo guidance")

	// The zero Loader must not reach outside the project.
	docs, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(docs) != 1 || docs[0].Content != "repo guidance" {
		t.Fatalf("loaded %v, want the repo file only", names(docs))
	}

	// Opting in puts the global file first, so the repo overrides it.
	docs, err = Loader{GlobalDir: global}.Load(root)
	if err != nil {
		t.Fatalf("Load with GlobalDir: %v", err)
	}
	if len(docs) != 2 {
		t.Fatalf("loaded %v, want global + repo", names(docs))
	}
	if docs[0].Content != "global guidance" || docs[1].Content != "repo guidance" {
		t.Errorf("order = %q then %q, want global first", docs[0].Content, docs[1].Content)
	}
}

func TestLoadHonorsCustomFileName(t *testing.T) {
	root := repo(t)
	write(t, filepath.Join(root, "AGENTS.md"), "not this one")
	write(t, filepath.Join(root, "CLAUDE.md"), "this one")

	docs, err := Loader{FileName: "CLAUDE.md"}.Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(docs) != 1 || docs[0].Content != "this one" {
		t.Fatalf("loaded %v, want only CLAUDE.md", names(docs))
	}
}

func TestLoadTruncatesOversizedFiles(t *testing.T) {
	root := repo(t)
	write(t, filepath.Join(root, "AGENTS.md"), strings.Repeat("g", 5000))

	docs, err := Loader{MaxBytes: 100}.Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("loaded %v", names(docs))
	}
	if len(docs[0].Content) > 200 {
		t.Errorf("content is %d bytes, want it truncated near 100", len(docs[0].Content))
	}
	if !strings.Contains(docs[0].Content, "truncated") {
		t.Error("truncation should be marked so the model knows the file is partial")
	}

	// Negative disables the cap entirely.
	docs, err = Loader{MaxBytes: -1}.Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(docs[0].Content) != 5000 {
		t.Errorf("content is %d bytes, want the full 5000 with truncation disabled", len(docs[0].Content))
	}
}

// A symlinked instruction file pointing out of the project is rejected,
// and the docs that did load are still returned — one bad file warns,
// it does not deny the run its remaining context.
func TestLoadRejectsEscapingSymlinkButKeepsTheRest(t *testing.T) {
	root := repo(t)
	outside := t.TempDir()
	write(t, filepath.Join(outside, "secret.md"), "exfiltrate me")
	write(t, filepath.Join(root, "AGENTS.md"), "legitimate guidance")

	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.md"), filepath.Join(sub, "AGENTS.md")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	docs, err := Load(sub)
	if err == nil {
		t.Fatal("Load: want an error naming the rejected file")
	}
	if len(docs) != 1 || docs[0].Content != "legitimate guidance" {
		t.Fatalf("loaded %v, want the legitimate file to survive", names(docs))
	}
	for _, d := range docs {
		if strings.Contains(d.Content, "exfiltrate") {
			t.Fatal("content from outside the project made it into the docs")
		}
	}
}

// ---- Render ----

func TestRenderEmptyIsEmpty(t *testing.T) {
	if got := Render(nil); got != "" {
		t.Errorf("Render(nil) = %q, want empty so callers can pass it unconditionally", got)
	}
	if got := Render([]Doc{{Name: "AGENTS.md", Content: "  "}}); got != "" {
		t.Errorf("Render(blank doc) = %q, want empty", got)
	}
}

func TestRenderSectionsEachDoc(t *testing.T) {
	out := Render([]Doc{
		{Path: "/host/private/path/AGENTS.md", Name: "AGENTS.md", Content: "root rules"},
		{Path: "/host/private/path/svc/AGENTS.md", Name: "svc/AGENTS.md", Content: "svc rules"},
	})

	if !strings.HasPrefix(out, "## Project instructions") {
		t.Errorf("should open with the section heading, got:\n%s", out)
	}
	if !strings.Contains(out, "### AGENTS.md") || !strings.Contains(out, "### svc/AGENTS.md") {
		t.Errorf("each doc needs its own heading, got:\n%s", out)
	}
	if strings.Contains(out, "/host/private/path") {
		t.Errorf("absolute host paths must not reach the prompt, got:\n%s", out)
	}
	if i, j := strings.Index(out, "root rules"), strings.Index(out, "svc rules"); i > j {
		t.Error("docs should render in the order given, general before specific")
	}
}
