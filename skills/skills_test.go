package skills

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mind-vm/agentloop"
)

// project builds a throwaway checkout with a .git marker and returns
// its symlink-resolved root.
func project(t *testing.T) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	if err := os.Mkdir(filepath.Join(real, ".git"), 0o755); err != nil {
		t.Fatalf("create .git: %v", err)
	}
	return real
}

// skill writes <root>/<dir>/<name>/SKILL.md.
func skill(t *testing.T, base, name, content string) {
	t.Helper()
	dir := filepath.Join(base, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte(content), 0o644); err != nil {
		t.Fatalf("write skill %s: %v", name, err)
	}
}

func skillDir(root string) string { return filepath.Join(root, filepath.FromSlash(DefaultDir)) }

func byName(sk []Skill) map[string]Skill {
	out := make(map[string]Skill, len(sk))
	for _, s := range sk {
		out[s.Name] = s
	}
	return out
}

// ---- discovery ----

func TestLoadReadsSkillsSortedByName(t *testing.T) {
	root := project(t)
	skill(t, skillDir(root), "review", "---\ndescription: Review a diff\n---\nDo the review.")
	skill(t, skillDir(root), "abridge", "---\ndescription: Shorten prose\n---\nCut it down.")

	sk, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(sk) != 2 {
		t.Fatalf("loaded %d skills, want 2", len(sk))
	}
	if sk[0].Name != "abridge" || sk[1].Name != "review" {
		t.Fatalf("names = %q, %q; want sorted", sk[0].Name, sk[1].Name)
	}
	if sk[1].Description != "Review a diff" || sk[1].Body != "Do the review." {
		t.Errorf("review = %+v", sk[1])
	}
	if !filepath.IsAbs(sk[0].Path) {
		t.Errorf("Path = %q, want absolute", sk[0].Path)
	}
}

func TestLoadPrefersFrontmatterNameOverDirectory(t *testing.T) {
	root := project(t)
	skill(t, skillDir(root), "some-folder", "---\nname: real-name\n---\nBody.")

	sk, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(sk) != 1 || sk[0].Name != "real-name" {
		t.Fatalf("names = %v, want [real-name]", sk)
	}
}

func TestLoadDerivesDescriptionFromBody(t *testing.T) {
	root := project(t)
	skill(t, skillDir(root), "no-meta", "# My Skill\n\nUse this when releasing.\n\nMore detail here.")

	sk, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// The heading is skipped — "# My Skill" would tell the model nothing.
	if sk[0].Description != "Use this when releasing." {
		t.Errorf("description = %q, want the first prose line", sk[0].Description)
	}
}

func TestLoadRejectsBadSkillsButKeepsTheRest(t *testing.T) {
	root := project(t)
	dir := skillDir(root)
	skill(t, dir, "good", "---\ndescription: fine\n---\nBody.")
	skill(t, dir, "fetch", "Body.")                     // reserved: would hijack fetch's help entry
	skill(t, dir, "Not Valid", "Body.")                 // not an identifier
	skill(t, dir, "empty", "---\nname: empty\n---\n\n") // nothing to retrieve

	sk, err := Load(root)
	if err == nil {
		t.Fatal("Load: want an error naming the rejected skills")
	}
	if len(sk) != 1 || sk[0].Name != "good" {
		t.Fatalf("loaded %v, want only [good]", sk)
	}
	for _, want := range []string{"reserved", "empty"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got: %v", want, err)
		}
	}
}

func TestLoadReadsNoGlobalDirByDefault(t *testing.T) {
	root := project(t)
	global := t.TempDir()
	skill(t, global, "shared", "---\ndescription: global version\n---\nGlobal body.")
	skill(t, skillDir(root), "shared", "---\ndescription: project version\n---\nProject body.")
	skill(t, global, "global-only", "---\ndescription: only global\n---\nBody.")

	sk, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(sk) != 1 || sk[0].Description != "project version" {
		t.Fatalf("loaded %v, want the project skill only", sk)
	}

	sk, err = Loader{GlobalDir: global}.Load(root)
	if err != nil {
		t.Fatalf("Load with GlobalDir: %v", err)
	}
	got := byName(sk)
	if len(got) != 2 {
		t.Fatalf("loaded %v, want shared + global-only", sk)
	}
	if got["shared"].Description != "project version" {
		t.Errorf("shared = %q, want the project skill to override the global one", got["shared"].Description)
	}
	if _, ok := got["global-only"]; !ok {
		t.Error("a global skill with no project counterpart should survive")
	}
}

func TestLoadHonorsCustomDir(t *testing.T) {
	root := project(t)
	skill(t, filepath.Join(root, ".claude", "skills"), "custom", "---\ndescription: d\n---\nBody.")

	sk, err := Loader{Dir: ".claude/skills"}.Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(sk) != 1 || sk[0].Name != "custom" {
		t.Fatalf("loaded %v, want [custom]", sk)
	}
}

func TestLoadMissingDirectoryIsNotAnError(t *testing.T) {
	sk, err := Load(project(t))
	if err != nil {
		t.Fatalf("Load: %v — most projects have no skills directory", err)
	}
	if len(sk) != 0 {
		t.Fatalf("loaded %v, want none", sk)
	}
}

func TestLoadTruncatesOversizedBodies(t *testing.T) {
	root := project(t)
	skill(t, skillDir(root), "big", "---\ndescription: d\n---\n"+strings.Repeat("b", 5000))

	sk, err := Loader{MaxBytes: 100}.Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(sk[0].Body) > 200 || !strings.Contains(sk[0].Body, "truncated") {
		t.Errorf("body is %d bytes and %q-marked; want truncated near 100", len(sk[0].Body), "truncated")
	}

	sk, err = Loader{MaxBytes: -1}.Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(sk[0].Body) != 5000 {
		t.Errorf("body is %d bytes, want the full 5000 with truncation disabled", len(sk[0].Body))
	}
}

// ---- frontmatter ----

func TestSplitFrontmatter(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		wantMeta map[string]string
		wantBody string
	}{
		{
			name:     "fenced",
			in:       "---\nname: a\ndescription: b\n---\nBody here.",
			wantMeta: map[string]string{"name": "a", "description": "b"},
			wantBody: "Body here.",
		},
		{
			name:     "no frontmatter",
			in:       "Just a body.",
			wantBody: "Just a body.",
		},
		{
			// An unterminated fence is far more likely to be prose that
			// happens to start with --- than broken metadata, so it is
			// kept as body rather than swallowed.
			name:     "unterminated fence stays body",
			in:       "---\nname: a\nBody with no closing fence.",
			wantBody: "---\nname: a\nBody with no closing fence.",
		},
		{
			name:     "quotes stripped",
			in:       "---\ndescription: \"quoted value\"\n---\nBody.",
			wantMeta: map[string]string{"description": "quoted value"},
			wantBody: "Body.",
		},
		{
			// Keys belonging to some richer frontmatter aimed at another
			// tool are ignored, not rejected.
			name:     "nested keys ignored",
			in:       "---\nname: a\nmetadata:\n  type: project\n# a comment: x\n---\nBody.",
			wantMeta: map[string]string{"name": "a", "metadata": ""},
			wantBody: "Body.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta, body := splitFrontmatter([]byte(tt.in))
			if strings.TrimSpace(string(body)) != tt.wantBody {
				t.Errorf("body = %q, want %q", strings.TrimSpace(string(body)), tt.wantBody)
			}
			for k, want := range tt.wantMeta {
				if want == "" {
					if _, ok := meta[k]; ok {
						t.Errorf("meta[%q] should have been ignored, got %q", k, meta[k])
					}
					continue
				}
				if meta[k] != want {
					t.Errorf("meta[%q] = %q, want %q", k, meta[k], want)
				}
			}
		})
	}
}

// ---- packs ----

func TestPacksKeepBodiesOutOfThePrompt(t *testing.T) {
	packs := Packs([]Skill{{
		Name:        "release",
		Description: "Cut a release",
		Body:        "SECRET-BODY-MARKER: step one, step two.",
	}})
	if len(packs) != 1 {
		t.Fatalf("got %d packs, want one per skill", len(packs))
	}
	p := packs[0]

	if strings.Contains(p.Prompt, "SECRET-BODY-MARKER") {
		t.Error("the body must not ride along in the system prompt — that is the whole design")
	}
	if !strings.Contains(p.Prompt, `skillGet("release")`) {
		t.Errorf("the catalog line should tell the model how to fetch it, got: %s", p.Prompt)
	}
	if !strings.Contains(p.Prompt, "Cut a release.") {
		t.Errorf("the catalog line should carry the description as a sentence, got: %s", p.Prompt)
	}
	if p.HelpEntries["release"] != "SECRET-BODY-MARKER: step one, step two." {
		t.Errorf("the body should be the help entry skillGet() resolves, got %q", p.HelpEntries["release"])
	}
	if p.SkillType != "prompt" {
		t.Errorf("SkillType = %q, want %q — it installs no primitives", p.SkillType, "prompt")
	}
	if p.Register != nil {
		t.Error("a documentation-only skill should install nothing into the runtime")
	}
}

func TestCapabilitiesAreNamedPerSkill(t *testing.T) {
	caps := Capabilities([]Skill{
		{Name: "a", Description: "first", Body: "A body"},
		{Name: "b", Description: "second", Body: "B body"},
	})
	if len(caps) != 2 {
		t.Fatalf("got %d capabilities, want one per skill so an allowlist can name them", len(caps))
	}
	for i, want := range []string{"a", "b"} {
		if caps[i].Name != want {
			t.Errorf("caps[%d].Name = %q, want %q", i, caps[i].Name, want)
		}
		built, err := caps[i].Build(agentloop.BuildContext{})
		if err != nil {
			t.Fatalf("caps[%d].Build: %v", i, err)
		}
		// Each capability must close over ITS OWN pack — a loop
		// variable captured by reference would give every capability
		// the last skill.
		if len(built) != 1 || built[0].Name != want {
			t.Fatalf("caps[%d] built %v, want a pack named %q", i, built, want)
		}
	}
}
