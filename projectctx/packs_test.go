package projectctx

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mind-vm/agentloop/sandbox"
)

// bigDoc is a body large enough that the default inline bound excerpts
// it, shaped as real markdown so the paragraph-boundary cut is
// exercised rather than a run of identical bytes.
func bigDoc(sections int) string {
	var b strings.Builder
	for i := 0; i < sections; i++ {
		b.WriteString("## Section\n\n")
		b.WriteString(strings.Repeat("guidance text. ", 40))
		b.WriteString("\n\n")
	}
	return strings.TrimSpace(b.String())
}

// ---- the inline bound ----

func TestLoadInlinesSmallDocsWhole(t *testing.T) {
	root := repo(t)
	write(t, filepath.Join(root, "AGENTS.md"), "use tabs\n\nrun the tests")

	docs, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	d := docs[0]
	if !d.Complete() {
		t.Fatal("a short file must ride along whole — retrieval is for the outliers")
	}
	if d.InlineText() != d.Content {
		t.Fatalf("Inline = %q, want the whole body %q", d.InlineText(), d.Content)
	}
}

func TestLoadExcerptsOversizedDocs(t *testing.T) {
	root := repo(t)
	body := bigDoc(20)
	write(t, filepath.Join(root, "AGENTS.md"), body)

	docs, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	d := docs[0]
	if d.Complete() {
		t.Fatalf("a %d-byte file was inlined whole against a %d-byte bound", len(body), DefaultInlineBytes)
	}
	if len(d.Inline) > DefaultInlineBytes {
		t.Errorf("Inline is %d bytes, want at most %d", len(d.Inline), DefaultInlineBytes)
	}
	// The full body stays reachable — this is retrieval, not the
	// lossy truncation MaxBytes performs.
	if d.Content != body {
		t.Errorf("Content is %d bytes, want the full %d", len(d.Content), len(body))
	}
	if !strings.HasPrefix(d.Content, d.Inline) {
		t.Error("Inline is not a prefix of Content")
	}
}

func TestInlineCutsAtALineBoundary(t *testing.T) {
	root := repo(t)
	write(t, filepath.Join(root, "AGENTS.md"), bigDoc(20))

	docs, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Severing mid-sentence invites the model to guess at the other
	// half, so the excerpt ends where a line does.
	if strings.HasSuffix(docs[0].Inline, "guidance tex") {
		t.Fatal("excerpt ends mid-word")
	}
	if !strings.HasSuffix(docs[0].Inline, ".") {
		t.Fatalf("excerpt ends at %q, want a line boundary", tail(docs[0].Inline, 30))
	}
}

// An excerpt that stops on a heading reads as an EMPTY section rather
// than as a cut — "## Deployment" with nothing under it tells an agent
// the project has no deployment rules.
func TestInlineDropsADanglingHeading(t *testing.T) {
	body := "# Rules\n\n" + strings.Repeat("keep this line. ", 20) + "\n\n## Deployment\n\nthe part that gets cut"

	got := head(body, len(body)-len("\nthe part that gets cut"))

	if strings.Contains(got, "## Deployment") {
		t.Fatalf("excerpt kept a heading with no body under it:\n%s", got)
	}
	if !strings.Contains(got, "keep this line.") {
		t.Fatalf("excerpt lost the section that did have a body:\n%s", got)
	}
}

// A file of nothing but headings has no better excerpt to offer than
// its headings, so stripping must not empty it out.
func TestInlineKeepsAHeadingOnlyExcerpt(t *testing.T) {
	body := "# One\n## Two\n### Three\n#### Four"

	if got := head(body, 12); strings.TrimSpace(got) == "" {
		t.Fatal("heading-only file excerpted to nothing")
	}
}

func TestNegativeInlineBytesInlinesEverything(t *testing.T) {
	root := repo(t)
	write(t, filepath.Join(root, "AGENTS.md"), bigDoc(20))

	docs, err := Loader{InlineBytes: -1}.Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !docs[0].Complete() {
		t.Fatal("InlineBytes -1 must inline the whole body")
	}
}

// A Doc a caller assembled itself has no Inline; it must still render
// rather than coming out blank.
func TestHandBuiltDocFallsBackToContent(t *testing.T) {
	d := Doc{Name: "AGENTS.md", Content: "hand-built guidance"}
	if d.InlineText() != "hand-built guidance" {
		t.Fatalf("InlineText = %q, want the content", d.InlineText())
	}
	if !d.Complete() {
		t.Fatal("Complete = false; a doc with no separate excerpt is complete by definition")
	}
}

// ---- RenderCatalog ----

func TestRenderCatalogEmptyIsEmpty(t *testing.T) {
	if got := RenderCatalog(nil); got != "" {
		t.Fatalf("RenderCatalog(nil) = %q, want empty", got)
	}
	if got := RenderCatalog([]Doc{{Name: "AGENTS.md", Content: "  "}}); got != "" {
		t.Fatalf("RenderCatalog(blank) = %q, want empty", got)
	}
}

func TestRenderCatalogPointsAtProjectGetForExcerpts(t *testing.T) {
	docs := []Doc{
		{Name: "AGENTS.md", Content: "short and complete"},
		{Name: "svc/AGENTS.md", Content: "opening section\nand a lot more", Inline: "opening section"},
	}

	got := RenderCatalog(docs)

	if !strings.Contains(got, "opening section") {
		t.Error("excerpted doc's head is missing from the prompt")
	}
	if strings.Contains(got, "and a lot more") {
		t.Error("excerpted doc's tail leaked into the prompt — the whole point is that it does not")
	}
	if !strings.Contains(got, `projectGet("svc/AGENTS.md")`) {
		t.Errorf("no retrieval pointer naming the excerpted file:\n%s", got)
	}
	if strings.Contains(got, `projectGet("AGENTS.md")`) {
		t.Error("a complete file must not be advertised as retrievable — a wasted round-trip returns nothing new")
	}
	if !strings.Contains(got, "short and complete") {
		t.Error("complete doc's body is missing")
	}
}

func TestRenderCatalogOmitsTheExcerptNoticeWhenNothingIsExcerpted(t *testing.T) {
	got := RenderCatalog([]Doc{{Name: "AGENTS.md", Content: "everything fits"}})

	if strings.Contains(got, "projectGet") {
		t.Errorf("catalog mentions projectGet with nothing to retrieve:\n%s", got)
	}
	if !strings.Contains(got, "## Project instructions") {
		t.Error("section heading missing")
	}
}

// Render is the older, inline-everything mode; adding retrieval must
// not have changed it.
func TestRenderStillInlinesEverything(t *testing.T) {
	docs := []Doc{{Name: "AGENTS.md", Content: "opening\nand the rest", Inline: "opening"}}

	got := Render(docs)

	if !strings.Contains(got, "and the rest") {
		t.Fatalf("Render dropped content it has always inlined:\n%s", got)
	}
}

// ---- the primitives ----

func newProjectSandbox(t *testing.T, docs []Doc) *sandbox.Sandbox {
	t.Helper()
	packs := Packs(docs)
	if len(packs) != 1 {
		t.Fatalf("Packs returned %d packs, want 1", len(packs))
	}
	return sandbox.New(packs...)
}

func TestPacksAreNilWithoutDocs(t *testing.T) {
	if got := Packs(nil); got != nil {
		t.Fatalf("Packs(nil) = %v, want nil so a project with no AGENTS.md pays nothing", got)
	}
	if got := Packs([]Doc{{Name: "AGENTS.md", Content: " "}}); got != nil {
		t.Fatalf("Packs(blank) = %v, want nil", got)
	}
}

func TestProjectGetReturnsTheFullBody(t *testing.T) {
	full := "opening section\nand the tail the prompt never carried"
	sb := newProjectSandbox(t, []Doc{{Name: "AGENTS.md", Content: full, Inline: "opening section"}})

	got := execString(t, sb, `projectGet("AGENTS.md")`)

	if got != full {
		t.Fatalf("projectGet returned %q, want the full body %q", got, full)
	}
}

func TestProjectGetFallsBackToTheBareFileName(t *testing.T) {
	sb := newProjectSandbox(t, []Doc{{Name: "svc/api/AGENTS.md", Content: "nested guidance"}})

	got := execString(t, sb, `projectGet("AGENTS.md")`)

	if got != "nested guidance" {
		t.Fatalf("projectGet(bare name) = %q, want the single nested match", got)
	}
}

// With several files sharing a base name, which one was meant is
// exactly what cannot be guessed — answering confidently with the wrong
// file is worse than a miss the model can correct.
func TestProjectGetRefusesAnAmbiguousBareName(t *testing.T) {
	sb := newProjectSandbox(t, []Doc{
		{Name: "AGENTS.md", Content: "root guidance"},
		{Name: "svc/AGENTS.md", Content: "service guidance"},
	})

	got := execString(t, sb, `projectGet("AGENTS.md")`)

	// The exact name still resolves — it is a real key, not an
	// ambiguous base name.
	if got != "root guidance" {
		t.Fatalf("exact name = %q, want the root file", got)
	}

	got = execString(t, sb, `projectGet("nope.md")`)
	if !strings.Contains(got, "projectList") {
		t.Fatalf("miss = %q, want it to point at projectList()", got)
	}
}

func TestProjectListReportsSizeAndCompleteness(t *testing.T) {
	sb := newProjectSandbox(t, []Doc{
		{Name: "AGENTS.md", Content: "short"},
		{Name: "svc/AGENTS.md", Content: "opening and a much longer tail", Inline: "opening"},
	})

	raw := execString(t, sb, `JSON.stringify(projectList())`)
	var list []struct {
		Name     string `json:"name"`
		Bytes    int    `json:"bytes"`
		Complete bool   `json:"complete"`
	}
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		t.Fatalf("projectList did not return JSON-shaped rows: %v (%s)", err, raw)
	}
	if len(list) != 2 {
		t.Fatalf("projectList returned %d rows, want 2: %s", len(list), raw)
	}
	byName := map[string]bool{}
	for _, d := range list {
		byName[d.Name] = d.Complete
		if d.Bytes == 0 {
			t.Errorf("%s reports 0 bytes", d.Name)
		}
	}
	if !byName["AGENTS.md"] {
		t.Error("the short file should report complete=true")
	}
	if byName["svc/AGENTS.md"] {
		t.Error("the excerpted file should report complete=false")
	}
}

func TestPackDeclaresItsPrimitives(t *testing.T) {
	prompt := newProjectSandbox(t, []Doc{{Name: "AGENTS.md", Content: "guidance"}}).SystemPrompt()

	for _, want := range []string{"projectGet", "projectList", "ProjectDoc"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("system prompt does not declare %s", want)
		}
	}
	// The bodies are held server-side; the pack must not smuggle them
	// back into the prompt it was introduced to keep them out of.
	if strings.Count(prompt, "guidance") != 0 {
		t.Error("doc body leaked into the sandbox system prompt")
	}
}

// execString runs an expression in the sandbox and returns its string
// result, via the same run()/return protocol the loop uses.
func execString(t *testing.T, sb *sandbox.Sandbox, expr string) string {
	t.Helper()
	_, ret, err := sb.ExecuteTurn("function run(args) { return "+expr+"; }", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("execute %s: %v", expr, err)
	}
	var out string
	if err := json.Unmarshal(ret, &out); err != nil {
		t.Fatalf("unmarshal %s: %v", ret, err)
	}
	return out
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
