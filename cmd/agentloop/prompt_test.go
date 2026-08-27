package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mind-vm/agentloop/agentloopsql"
	"github.com/mind-vm/agentloop/sandbox"
)

func TestResolveApprovalMode(t *testing.T) {
	cases := []struct {
		name        string
		requested   string
		hasTerminal bool
		want        approvalMode
		wantErr     bool
	}{
		{"default at a terminal", "", true, approvalPrompt, false},
		// The load-bearing default: an unattended run must fail closed
		// rather than block forever on an answer nobody will give.
		{"default with no terminal", "", false, approvalDeny, false},
		{"explicit auto", "auto", false, approvalAuto, false},
		{"explicit deny", "deny", true, approvalDeny, false},
		{"explicit prompt at a terminal", "prompt", true, approvalPrompt, false},
		// Stating an expectation the environment cannot meet is an error,
		// not something to silently downgrade.
		{"explicit prompt with no terminal", "prompt", false, "", true},
		{"nonsense", "maybe", true, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveApprovalMode(tc.requested, tc.hasTerminal)
			if tc.wantErr {
				if err == nil {
					t.Fatal("want an error")
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveApprovalMode: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// prompterFor builds a prompter reading answers from input, writing its
// questions to the returned buffer.
func prompterFor(t *testing.T, mode approvalMode, input string, store *agentloopsql.Store) (*terminalPrompter, *bytes.Buffer) {
	t.Helper()
	out := &bytes.Buffer{}
	p := newTerminalPrompter(bufio.NewReader(strings.NewReader(input)), out, mode, store, func() string { return "s-1" })
	return p, out
}

func TestConfirmAutoAndDenyDoNotAsk(t *testing.T) {
	for _, tc := range []struct {
		mode approvalMode
		want bool
	}{{approvalAuto, true}, {approvalDeny, false}} {
		t.Run(string(tc.mode), func(t *testing.T) {
			p, out := prompterFor(t, tc.mode, "", nil)
			got, err := p.Request(sandbox.InputRequestData{InputType: "confirm", Message: "Allow example.com?"})
			if err != nil {
				t.Fatalf("Request: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
			if out.Len() != 0 {
				t.Errorf("%s must not ask anything, wrote %q", tc.mode, out.String())
			}
		})
	}
}

func TestConfirmReadsTheAnswer(t *testing.T) {
	cases := map[string]bool{
		"y\n": true, "Y\n": true, "yes\n": true, "YES\n": true,
		"n\n": false, "\n": false, "anything else\n": false,
	}
	for input, want := range cases {
		t.Run(strings.TrimSpace(input), func(t *testing.T) {
			p, out := prompterFor(t, approvalPrompt, input, nil)
			got, err := p.Request(sandbox.InputRequestData{InputType: "confirm", Message: "Allow example.com?"})
			if err != nil {
				t.Fatalf("Request: %v", err)
			}
			if got != want {
				t.Errorf("input %q: got %v, want %v", input, got, want)
			}
			if !strings.Contains(out.String(), "Allow example.com?") {
				t.Errorf("the question should be shown, got %q", out.String())
			}
		})
	}
}

// End of input means nobody is going to answer. That is a failure to
// report, not a "no" to pass off as a considered refusal.
func TestConfirmWithNoAnswerAvailable(t *testing.T) {
	p, _ := prompterFor(t, approvalPrompt, "", nil)
	_, err := p.Request(sandbox.InputRequestData{InputType: "confirm", Message: "Allow example.com?"})
	if !errors.Is(err, errNoTerminal) {
		t.Fatalf("got %v, want errNoTerminal", err)
	}
}

// The same question twice in one run should trouble the user once.
func TestConfirmRemembersWithinTheProcess(t *testing.T) {
	p, out := prompterFor(t, approvalPrompt, "y\n", nil)
	q := sandbox.InputRequestData{InputType: "confirm", Message: "Allow example.com?"}

	first, err := p.Request(q)
	if err != nil || first != true {
		t.Fatalf("first: %v %v", first, err)
	}
	// Only one answer was supplied; a second read would hit EOF and error.
	second, err := p.Request(q)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if second != true {
		t.Errorf("second answer = %v, want the remembered approval", second)
	}
	if n := strings.Count(out.String(), "[y/N]"); n != 1 {
		t.Errorf("asked %d times, want 1", n)
	}
}

// A refusal is deliberately NOT remembered: the user may well allow it
// next time, and silently blocking forever is the worse failure.
func TestRefusalIsNotRemembered(t *testing.T) {
	p, out := prompterFor(t, approvalPrompt, "n\ny\n", nil)
	q := sandbox.InputRequestData{InputType: "confirm", Message: "Allow example.com?"}

	if got, _ := p.Request(q); got != false {
		t.Fatalf("first answer = %v, want false", got)
	}
	got, err := p.Request(q)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if got != true {
		t.Errorf("second answer = %v — a refusal must not stick", got)
	}
	if n := strings.Count(out.String(), "[y/N]"); n != 2 {
		t.Errorf("asked %d times, want 2", n)
	}
}

func TestApprovalSurvivesIntoALaterRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sessions.db")
	store, err := agentloopsql.Open(path, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	q := sandbox.InputRequestData{InputType: "confirm", Message: "Allow example.com?"}

	first, _ := prompterFor(t, approvalPrompt, "y\n", store)
	if got, err := first.Request(q); err != nil || got != true {
		t.Fatalf("first run: %v %v", got, err)
	}

	// A fresh prompter with NO answers available stands in for the next
	// invocation of the same session.
	second, out := prompterFor(t, approvalPrompt, "", store)
	got, err := second.Request(q)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if got != true {
		t.Errorf("got %v, want the stored approval", got)
	}
	if strings.Contains(out.String(), "[y/N]") {
		t.Errorf("a stored approval must not ask again, got %q", out.String())
	}

	// And revoking it puts the question back.
	if err := store.Revoke(context.Background(), "s-1"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	third, _ := prompterFor(t, approvalPrompt, "", store)
	if _, err := third.Request(q); !errors.Is(err, errNoTerminal) {
		t.Errorf("after revoking, the question should be asked again; got %v", err)
	}
}

// Grants are per session: approving something in one must not pre-approve
// it in another.
func TestApprovalDoesNotLeakBetweenSessions(t *testing.T) {
	store, err := agentloopsql.Open(filepath.Join(t.TempDir(), "sessions.db"), nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer store.Close()

	q := sandbox.InputRequestData{InputType: "confirm", Message: "Allow example.com?"}
	id := "s-1"

	p := newTerminalPrompter(bufio.NewReader(strings.NewReader("y\n")), &bytes.Buffer{}, approvalPrompt, store, func() string { return id })
	if got, err := p.Request(q); err != nil || got != true {
		t.Fatalf("approve: %v %v", got, err)
	}

	other, _ := prompterFor(t, approvalPrompt, "", store)
	// prompterFor's session is also "s-1"; point this one elsewhere.
	other.sessionID = func() string { return "s-2" }
	if _, err := other.Request(q); !errors.Is(err, errNoTerminal) {
		t.Errorf("another session must be asked afresh; got %v", err)
	}
}

func TestChoicePrompt(t *testing.T) {
	q := sandbox.InputRequestData{InputType: "choice", Message: "Which?", Options: []string{"alpha", "beta", "gamma"}}

	t.Run("selects by number", func(t *testing.T) {
		p, out := prompterFor(t, approvalPrompt, "2\n", nil)
		got, err := p.Request(q)
		if err != nil {
			t.Fatalf("Request: %v", err)
		}
		if got != "beta" {
			t.Errorf("got %v, want beta", got)
		}
		for _, opt := range q.Options {
			if !strings.Contains(out.String(), opt) {
				t.Errorf("every option should be shown, missing %q", opt)
			}
		}
	})

	t.Run("empty takes the first", func(t *testing.T) {
		p, _ := prompterFor(t, approvalPrompt, "\n", nil)
		got, _ := p.Request(q)
		if got != "alpha" {
			t.Errorf("got %v, want alpha", got)
		}
	})

	t.Run("out of range is an error", func(t *testing.T) {
		p, _ := prompterFor(t, approvalPrompt, "9\n", nil)
		if _, err := p.Request(q); err == nil {
			t.Error("want an error for a number outside the options")
		}
	})

	// auto and deny answer "may I?"; a choice is not that question.
	t.Run("not answerable without a terminal", func(t *testing.T) {
		for _, mode := range []approvalMode{approvalAuto, approvalDeny} {
			p, _ := prompterFor(t, mode, "", nil)
			if _, err := p.Request(q); !errors.Is(err, errNoTerminal) {
				t.Errorf("%s: got %v, want errNoTerminal", mode, err)
			}
		}
	})
}

func TestTextPrompt(t *testing.T) {
	p, out := prompterFor(t, approvalPrompt, "the answer\n", nil)
	got, err := p.Request(sandbox.InputRequestData{InputType: "text", Message: "Name?", Placeholder: "e.g. ada"})
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if got != "the answer" {
		t.Errorf("got %q, want %q", got, "the answer")
	}
	if !strings.Contains(out.String(), "e.g. ada") {
		t.Errorf("the placeholder should be shown, got %q", out.String())
	}
}

func TestUnknownPromptType(t *testing.T) {
	p, _ := prompterFor(t, approvalPrompt, "\n", nil)
	if _, err := p.Request(sandbox.InputRequestData{InputType: "captcha"}); err == nil {
		t.Error("want an error for an unrecognised prompt type")
	}
}
