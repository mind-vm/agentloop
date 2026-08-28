package sandbox

import (
	"encoding/json"
	"strings"
	"testing"
)

func runTurn(t *testing.T, sb *Sandbox, code string) string {
	t.Helper()
	logs, _, err := sb.ExecuteTurn("function run(args) {\n"+code+"\n}", json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	return logs
}

func TestTurnLogsAreCappedByDefault(t *testing.T) {
	sb := New()

	// A script that logs a whole dataset instead of returning it — the
	// exact thing the system prompt asks the model not to do.
	logs := runTurn(t, sb, `for (var i = 0; i < 4000; i++) { log("row " + i + " of the dataset"); }`)

	if len(logs) > DefaultMaxLogBytes+512 {
		t.Fatalf("logs are %d bytes against a %d cap", len(logs), DefaultMaxLogBytes)
	}
	if !strings.Contains(logs, "omitted") {
		t.Error("truncation should be marked, so the model knows its output was cut")
	}
}

// Head AND tail: the head has how the script proceeded, the tail
// typically has what it concluded. Head-only truncation reliably drops
// the one line the agent meant to read.
func TestTurnLogsKeepHeadAndTail(t *testing.T) {
	sb := New()
	sb.SetMaxLogBytes(2048)

	logs := runTurn(t, sb, `
		log("FIRST-LINE");
		for (var i = 0; i < 500; i++) { log("filler line " + i); }
		log("TOTAL: 42");
	`)

	if !strings.Contains(logs, "FIRST-LINE") {
		t.Error("head of the log output was dropped")
	}
	if !strings.Contains(logs, "TOTAL: 42") {
		t.Error("tail of the log output was dropped — that is usually the line that mattered")
	}
	if len(logs) > 2048+512 {
		t.Errorf("logs are %d bytes against a 2048 cap", len(logs))
	}
}

func TestTurnLogsUnderTheCapAreUntouched(t *testing.T) {
	sb := New()

	logs := runTurn(t, sb, `log("just a little output"); log("and one more line");`)

	if logs != "just a little output\nand one more line" {
		t.Fatalf("logs = %q, want them verbatim", logs)
	}
}

func TestSetMaxLogBytesZeroRestoresTheDefaultAndNegativeUncaps(t *testing.T) {
	big := `for (var i = 0; i < 4000; i++) { log("row " + i + " of the dataset"); }`

	uncapped := New()
	uncapped.SetMaxLogBytes(-1)
	full := runTurn(t, uncapped, big)
	if len(full) <= DefaultMaxLogBytes {
		t.Fatalf("uncapped run produced only %d bytes; the fixture is too small to prove anything", len(full))
	}
	if strings.Contains(full, "omitted") {
		t.Error("negative cap should not truncate")
	}

	restored := New()
	restored.SetMaxLogBytes(1024)
	restored.SetMaxLogBytes(0)
	if got := runTurn(t, restored, big); len(got) > DefaultMaxLogBytes+512 {
		t.Errorf("zero did not restore the default: %d bytes", len(got))
	}
}

// The cap applies to the failure path too — a script that logs a lot
// and then throws still has its output replayed into every later
// prompt.
func TestTurnLogsAreCappedOnAScriptError(t *testing.T) {
	sb := New()
	sb.SetMaxLogBytes(1024)

	logs, _, err := sb.ExecuteTurn(
		`function run(args) { for (var i = 0; i < 2000; i++) { log("row " + i); } throw new Error("boom"); }`,
		json.RawMessage(`{}`))

	if err == nil {
		t.Fatal("expected the script to error")
	}
	if len(logs) > 1024+512 {
		t.Fatalf("error-path logs are %d bytes against a 1024 cap", len(logs))
	}
}
