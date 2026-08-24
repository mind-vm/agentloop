package ext_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jryannel/agentloop/ext"
	"github.com/jryannel/agentloop/sandbox"
)

func TestEmailPack_Sends(t *testing.T) {
	var to, subj, body string
	send := func(t2, s2, b2 string) error { to, subj, body = t2, s2, b2; return nil }
	s := sandbox.New(ext.EmailPack(context.Background(), send))

	out, err := s.Execute(`sendEmail({to: "ops@x.io", subject: "Hi", body: "Done"})`)
	if err != nil {
		t.Fatalf("sendEmail: %v", err)
	}
	if out != "true" {
		t.Fatalf("got %q, want true", out)
	}
	if to != "ops@x.io" || subj != "Hi" || body != "Done" {
		t.Errorf("send args: to=%q subj=%q body=%q", to, subj, body)
	}
}

func TestEmailPack_ToRequired(t *testing.T) {
	called := false
	s := sandbox.New(ext.EmailPack(context.Background(), func(string, string, string) error { called = true; return nil }))
	if _, err := s.Execute(`sendEmail({subject: "x", body: "y"})`); err == nil {
		t.Fatalf("expected error for missing 'to'")
	}
	if called {
		t.Errorf("backend should not be called when 'to' is missing")
	}
}

func TestEmailPack_SendErrorThrows(t *testing.T) {
	s := sandbox.New(ext.EmailPack(context.Background(), func(string, string, string) error { return errors.New("smtp down") }))
	_, err := s.Execute(`sendEmail({to: "a@b.c", subject: "s", body: "b"})`)
	if err == nil || !strings.Contains(err.Error(), "smtp down") {
		t.Fatalf("expected send error to propagate, got %v", err)
	}
}

func TestEmailPack_NilBackend(t *testing.T) {
	s := sandbox.New(ext.EmailPack(context.Background(), nil))
	_, err := s.Execute(`sendEmail({to: "a@b.c", subject: "s", body: "b"})`)
	if err == nil || !strings.Contains(err.Error(), "no email backend") {
		t.Fatalf("expected nil-backend error, got: %v", err)
	}
}

func TestEmailPack_DeniedByDefaultPolicy(t *testing.T) {
	s := sandbox.New(ext.EmailPack(context.Background(), func(string, string, string) error { return nil }))
	s.SetPolicy(sandbox.DefaultPolicy{})
	_, err := s.Execute(`sendEmail({to: "a@b.c", subject: "s", body: "b"})`)
	if err == nil || !strings.Contains(err.Error(), "denied by the default sandbox policy") {
		t.Fatalf("expected policy denial, got: %v", err)
	}
}
