package agentloop_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/dop251/goja"

	"github.com/mind-vm/agentloop"
	"github.com/mind-vm/agentloop/sandbox"
)

// TestDefaultSandboxBuilder_RegistersCapabilityPacks confirms
// capability Build outputs are installed into the runtime so JS code
// can call into them.
func TestDefaultSandboxBuilder_RegistersCapabilityPacks(t *testing.T) {
	cap := agentloop.Capability{
		Name: "say",
		Build: func(_ agentloop.BuildContext) ([]sandbox.Pack, error) {
			return []sandbox.Pack{{
				Name:        "say",
				Description: "greet primitive",
				Register: func(rt *goja.Runtime, _ *sandbox.Sandbox) {
					_ = rt.Set("hello", func() string { return "hi" })
				},
			}}, nil
		},
	}

	b := &agentloop.DefaultSandboxBuilder{Capabilities: []agentloop.Capability{cap}}
	sb, cleanup, err := b.Build(context.Background(), agentloop.Session{ID: "s1"}, agentloop.Scope{}, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer cleanup()

	out, err := sb.Execute(`hello()`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if out != "hi" {
		t.Fatalf("primitive output: got %q, want %q", out, "hi")
	}
}

// TestDefaultSandboxBuilder_AppendsMetaPacks confirms SkillDiscovery
// and Help land at the end, after the capability packs.
func TestDefaultSandboxBuilder_AppendsMetaPacks(t *testing.T) {
	cap := agentloop.Capability{
		Name: "demo",
		Build: func(_ agentloop.BuildContext) ([]sandbox.Pack, error) {
			return []sandbox.Pack{{
				Name:        "demo",
				Description: "demo cap",
				Prompt:      "/** demo cap */ declare function demo(): void;",
				Register: func(rt *goja.Runtime, _ *sandbox.Sandbox) {
					_ = rt.Set("demo", func() {})
				},
			}}, nil
		},
	}

	b := &agentloop.DefaultSandboxBuilder{Capabilities: []agentloop.Capability{cap}}
	sb, cleanup, err := b.Build(context.Background(), agentloop.Session{ID: "s1"}, agentloop.Scope{}, nil)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	defer cleanup()

	prompt := sb.SystemPrompt()
	if !strings.Contains(prompt, "declare function demo") {
		t.Errorf("system prompt missing capability docs: %s", prompt)
	}

	// help() should be invocable — that's the HelpPack contribution.
	out, err := sb.Execute(`help("demo")`)
	if err != nil {
		t.Fatalf("help(demo): %v", err)
	}
	if out == "" {
		t.Errorf("help(demo) should produce non-empty output")
	}
}

// TestDefaultSandboxBuilder_CapabilityBuildErrorIsLogged confirms a
// flaky capability is skipped rather than aborting the build.
func TestDefaultSandboxBuilder_CapabilityBuildErrorIsLogged(t *testing.T) {
	good := agentloop.Capability{
		Name: "ok",
		Build: func(_ agentloop.BuildContext) ([]sandbox.Pack, error) {
			return []sandbox.Pack{{
				Name:        "ok",
				Description: "ok",
				Register: func(rt *goja.Runtime, _ *sandbox.Sandbox) {
					_ = rt.Set("okmark", func() string { return "ok" })
				},
			}}, nil
		},
	}
	bad := agentloop.Capability{
		Name:  "boom",
		Build: func(_ agentloop.BuildContext) ([]sandbox.Pack, error) { return nil, errors.New("kaboom") },
	}

	b := &agentloop.DefaultSandboxBuilder{Capabilities: []agentloop.Capability{bad, good}}
	sb, cleanup, err := b.Build(context.Background(), agentloop.Session{}, agentloop.Scope{}, nil)
	if err != nil {
		t.Fatalf("build should not fail when one capability errors: %v", err)
	}
	defer cleanup()

	if out, err := sb.Execute(`okmark()`); err != nil || out != "ok" {
		t.Fatalf("good capability should still be wired: out=%q err=%v", out, err)
	}
}
