package ext_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jryannel/agentloop/ext"
	"github.com/jryannel/agentloop/sandbox"
)

// TestSecretPack_ReturnsValue confirms the bare global secret(name)
// forwards to the getter and returns its plaintext.
func TestSecretPack_ReturnsValue(t *testing.T) {
	get := func(name string) (string, error) {
		if name == "API_KEY" {
			return "sk-123", nil
		}
		return "", errors.New("not found")
	}
	s := sandbox.New(ext.SecretPack(context.Background(), get))

	out, err := s.Execute(`secret("API_KEY")`)
	if err != nil {
		t.Fatalf("secret(): %v", err)
	}
	if out != "sk-123" {
		t.Fatalf("got %q, want %q", out, "sk-123")
	}
}

// TestSecretPack_RequireModule confirms the require('secret') module
// wrapper resolves to the same getter.
func TestSecretPack_RequireModule(t *testing.T) {
	get := func(string) (string, error) { return "val", nil }
	// require() is provided by RequirePack (AlwaysOn in real runs).
	s := sandbox.New(sandbox.RequirePack(context.Background()), ext.SecretPack(context.Background(), get))

	out, err := s.Execute(`require('secret').get("anything")`)
	if err != nil {
		t.Fatalf("require('secret').get: %v", err)
	}
	if out != "val" {
		t.Fatalf("got %q, want %q", out, "val")
	}
}

// TestSecretPack_LookupErrorThrows confirms a getter error surfaces as
// a catchable JS throw rather than crashing the run.
func TestSecretPack_LookupErrorThrows(t *testing.T) {
	get := func(string) (string, error) { return "", errors.New("secrets: not found") }
	s := sandbox.New(ext.SecretPack(context.Background(), get))

	_, err := s.Execute(`secret("MISSING")`)
	if err == nil {
		t.Fatalf("expected error for missing secret")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected lookup error to propagate, got: %v", err)
	}

	// It must be catchable from JS (fresh sandbox to avoid reusing the
	// errored runtime above).
	s2 := sandbox.New(ext.SecretPack(context.Background(), get))
	out, err := s2.Execute("var r;\ntry { secret(\"MISSING\"); r = \"no-throw\"; } catch (e) { r = \"caught\"; }\nr")
	if err != nil {
		t.Fatalf("try/catch around secret(): %v", err)
	}
	if out != "caught" {
		t.Fatalf("secret() error should be catchable, got %q", out)
	}
}

// TestSecretPack_NameRequired confirms an empty/missing name fails fast
// without calling the getter.
func TestSecretPack_NameRequired(t *testing.T) {
	called := false
	get := func(string) (string, error) { called = true; return "", nil }
	s := sandbox.New(ext.SecretPack(context.Background(), get))

	if _, err := s.Execute(`secret("")`); err == nil {
		t.Fatalf("expected error for empty name")
	}
	if called {
		t.Errorf("getter should not be called for empty name")
	}
}

// TestSecretPack_NilGetter confirms a nil backend yields a clear thrown
// error rather than a panic that escapes the sandbox.
func TestSecretPack_NilGetter(t *testing.T) {
	s := sandbox.New(ext.SecretPack(context.Background(), nil))
	_, err := s.Execute(`secret("X")`)
	if err == nil || !strings.Contains(err.Error(), "no secrets backend") {
		t.Fatalf("expected nil-backend error, got: %v", err)
	}
}

func TestSecretPack_DeniedByDefaultPolicy(t *testing.T) {
	s := sandbox.New(ext.SecretPack(context.Background(), func(string) (string, error) { return "x", nil }))
	s.SetPolicy(sandbox.DefaultPolicy{})
	_, err := s.Execute(`secret("X")`)
	if err == nil || !strings.Contains(err.Error(), "denied by the default sandbox policy") {
		t.Fatalf("expected policy denial, got: %v", err)
	}
}
