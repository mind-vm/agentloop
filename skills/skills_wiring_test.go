package skills_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mind-vm/agentloop"
	"github.com/mind-vm/agentloop/sandbox"
	"github.com/mind-vm/agentloop/skills"
)

// buildSandbox wires discovered skills through the same builder an
// application would use, so the test exercises the real path from
// SKILL.md on disk to what a run() script can see.
func buildSandbox(t *testing.T, sk []skills.Skill, enabled *[]string) *sandbox.Sandbox {
	t.Helper()
	builder := &agentloop.DefaultSandboxBuilder{
		Capabilities:        skills.Capabilities(sk),
		EnabledCapabilities: enabled,
	}
	sb, cleanup, err := builder.Build(context.Background(), agentloop.Session{ID: "s"}, agentloop.Scope{}, nil)
	if err != nil {
		t.Fatalf("build sandbox: %v", err)
	}
	t.Cleanup(cleanup)
	return sb
}

// eval runs one script body and unmarshals what it returned.
func eval(t *testing.T, sb *sandbox.Sandbox, code string, into any) {
	t.Helper()
	_, ret, err := sb.ExecuteTurn("function run(args) {\n"+code+"\n}", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("execute %q: %v", code, err)
	}
	if err := json.Unmarshal(ret, into); err != nil {
		t.Fatalf("unmarshal %s: %v", ret, err)
	}
}

func writeSkill(t *testing.T, root, name, content string) {
	t.Helper()
	dir := filepath.Join(root, filepath.FromSlash(skills.DefaultDir), name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, skills.FileName), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func project(t *testing.T) string {
	t.Helper()
	real, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(real, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return real
}

// The end-to-end claim: a SKILL.md on disk becomes something the model
// can find with skillList() and read with skillGet(), without its body
// ever entering the system prompt.
func TestSkillsAreDiscoverableAndRetrievableInTheSandbox(t *testing.T) {
	root := project(t)
	writeSkill(t, root, "release", "---\ndescription: Cut a release\n---\nStep one. Step two. UNIQUE-BODY-TOKEN")

	sk, err := skills.Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(sk) != 1 {
		t.Fatalf("loaded %v", sk)
	}
	sb := buildSandbox(t, sk, nil)

	var listed []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Type        string `json:"type"`
	}
	eval(t, sb, `return skillList();`, &listed)

	var found bool
	for _, s := range listed {
		if s.Name != "release" {
			continue
		}
		found = true
		if s.Description != "Cut a release" {
			t.Errorf("description = %q", s.Description)
		}
		// Not "pack": it brings no primitives, only instructions.
		if s.Type != "prompt" {
			t.Errorf("type = %q, want %q", s.Type, "prompt")
		}
	}
	if !found {
		t.Fatalf("skillList() did not include the discovered skill: %+v", listed)
	}

	var body string
	eval(t, sb, `return skillGet("release");`, &body)
	if !strings.Contains(body, "UNIQUE-BODY-TOKEN") {
		t.Errorf("skillGet returned %q, want the SKILL.md body", body)
	}
}

func TestSkillBodyStaysOutOfTheSystemPrompt(t *testing.T) {
	root := project(t)
	writeSkill(t, root, "release", "---\ndescription: Cut a release\n---\nUNIQUE-BODY-TOKEN in the body.")

	sk, err := skills.Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	prompt := buildSandbox(t, sk, nil).SystemPrompt()
	if strings.Contains(prompt, "UNIQUE-BODY-TOKEN") {
		t.Error("the skill body leaked into the system prompt; it should cost context only when fetched")
	}
	if !strings.Contains(prompt, `skillGet("release")`) {
		t.Errorf("the prompt should advertise the skill, got:\n%s", prompt)
	}
}

// A per-session allowlist names capabilities, so naming one capability
// per skill is what makes a skill switchable per session.
func TestSkillsRespectTheCapabilityAllowlist(t *testing.T) {
	root := project(t)
	writeSkill(t, root, "allowed", "---\ndescription: yes\n---\nAllowed body.")
	writeSkill(t, root, "denied", "---\ndescription: no\n---\nDenied body.")

	sk, err := skills.Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	enabled := []string{"allowed"}
	prompt := buildSandbox(t, sk, &enabled).SystemPrompt()
	if !strings.Contains(prompt, `skillGet("allowed")`) {
		t.Error("the allowlisted skill should be advertised")
	}
	if strings.Contains(prompt, `skillGet("denied")`) {
		t.Error("a skill outside the allowlist should not be installed for this session")
	}
}
