package redact_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jryannel/agentloop/redact"
)

func TestRedactor_NilSafe(t *testing.T) {
	// A nil receiver must pass through unchanged, so a caller with no
	// redactor configured needs no wrapper/nil-check of its own.
	var r *redact.Redactor
	if got := r.Apply("hello"); got != "hello" {
		t.Fatalf("Apply on nil redactor changed value: %q", got)
	}
	if got := string(r.ApplyBytes([]byte("hello"))); got != "hello" {
		t.Fatalf("ApplyBytes on nil redactor changed value: %q", got)
	}
}

func TestRedactor_FromSecrets_Empty(t *testing.T) {
	if r := redact.FromSecrets(nil); r != nil {
		t.Fatalf("nil map should produce nil redactor")
	}
	if r := redact.FromSecrets(map[string]string{}); r != nil {
		t.Fatalf("empty map should produce nil redactor")
	}
}

func TestRedactor_ReplacesEachValue(t *testing.T) {
	r := redact.FromSecrets(map[string]string{
		"api_key":   "sk-eyJhbGc.payload.sig",
		"tenant_id": "tnt_42",
	})

	in := `Authorization: Bearer sk-eyJhbGc.payload.sig | tenant=tnt_42`
	got := r.Apply(in)

	if strings.Contains(got, "sk-eyJhbGc.payload.sig") {
		t.Fatalf("api_key value leaked: %q", got)
	}
	if strings.Contains(got, "tnt_42") {
		t.Fatalf("tenant_id value leaked: %q", got)
	}
	if !strings.Contains(got, "[redacted:secret.api_key]") {
		t.Fatalf("api_key placeholder missing: %q", got)
	}
	if !strings.Contains(got, "[redacted:secret.tenant_id]") {
		t.Fatalf("tenant_id placeholder missing: %q", got)
	}
}

func TestRedactor_ReplacesMultipleOccurrences(t *testing.T) {
	r := redact.FromSecrets(map[string]string{"key": "TOK"})
	got := r.Apply("first=TOK retry=TOK confirm=TOK")
	if strings.Contains(got, "TOK") {
		t.Fatalf("residual occurrence: %q", got)
	}
}

func TestRedactor_LongestFirst(t *testing.T) {
	// If "app" and "apple" are both registered and replaced in
	// arbitrary order, "apple" -> "[redacted:short]le" because "app"
	// matched first. Longest-first prevents that.
	r := redact.New(
		[]string{"app", "apple"},
		[]string{"[redacted:short]", "[redacted:long]"},
	)
	got := r.Apply("I ate an apple today.")
	if !strings.Contains(got, "[redacted:long]") {
		t.Fatalf("longer match should win: %q", got)
	}
	if strings.Contains(got, "apple") {
		t.Fatalf("residual apple: %q", got)
	}
}

func TestRedactor_SkipsEmptyValues(t *testing.T) {
	// A secret explicitly set to "" must not become a redaction
	// pattern — otherwise every byte would be replaced.
	r := redact.New(
		[]string{"", "real"},
		[]string{"[redacted:empty]", "[redacted:real]"},
	)
	got := r.Apply("the real deal")
	if !strings.Contains(got, "[redacted:real]") {
		t.Fatalf("real should be redacted: %q", got)
	}
	if strings.Contains(got, "[redacted:empty]") {
		t.Fatalf("empty pattern leaked into output: %q", got)
	}
}

func TestRedactor_ApplyBytes_JSONPayload(t *testing.T) {
	// A JSON-marshalled payload whose value is nested inside Content,
	// inside a nested Args map, and inside a deep array must have every
	// occurrence caught — this is the whole point of operating on raw
	// bytes rather than walking the struct.
	const key = "sk-SECRET_PAYLOAD"
	r := redact.FromSecrets(map[string]string{"api_key": key})

	payload := map[string]any{
		"type":    "execute_js_result",
		"content": "Authorization: Bearer " + key,
		"args": map[string]any{
			"headers": map[string]string{
				"authorization": "Bearer " + key,
			},
		},
		"result": map[string]any{
			"deep": map[string]any{
				"deeper": []string{"opaque", key, "more"},
			},
		},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := r.ApplyBytes(raw)

	if strings.Contains(string(got), key) {
		t.Fatalf("plaintext key leaked through bytes-level redactor: %s", got)
	}
	if want := "[redacted:secret.api_key]"; !strings.Contains(string(got), want) {
		t.Fatalf("placeholder missing: %s", got)
	}
}

func TestRedactor_NoSubstring_NoChange(t *testing.T) {
	r := redact.FromSecrets(map[string]string{"key": "never-appears"})
	in := `{"type":"text","delta":"hello world"}`
	if got := r.Apply(in); got != in {
		t.Fatalf("redactor altered payload that contained no values: %q", got)
	}
}

func TestRedactor_New_MismatchedLengths_Degrades(t *testing.T) {
	// A wiring bug at construction time shouldn't crash an agent run —
	// the constructor degrades to an empty (no-op) redactor.
	r := redact.New([]string{"a", "b"}, []string{"only-one"})
	if got := r.Apply("a and b"); got != "a and b" {
		t.Fatalf("expected pass-through on length mismatch, got %q", got)
	}
}
