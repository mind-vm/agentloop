// Package redact strips known secret values from text before it
// leaves the runtime. Used as defense-in-depth over agentloop's own
// event/span payloads and eval judge prompts — the primitive contract
// is that a capability substituting a secret into a request (say,
// ext.SecretPack's secret(name) into an Authorization header) should
// never itself emit that value in a RunEvent, a span, or an eval
// transcript. This package is the second line of defense for the
// times that contract leaks — a script that does log(secret("KEY")),
// or a fetch() response that happens to echo a credential back.
//
// A Redactor holds a small ordered list of (value, placeholder) pairs.
// Apply (strings) and ApplyBytes ([]byte) replace every occurrence of
// each value with its placeholder, longest value first so a value
// that's a substring of another registered value can't carve into the
// longer one's placeholder after it's already been substituted.
//
// This does not parse JSON or know anything about event structure — it
// operates on raw bytes/text. A caller redacting a structured payload
// (e.g. RunEvent.Args) marshals it to JSON, calls ApplyBytes, and
// unmarshals the result — which is exactly how agentloop's own Config
// wires this in (see redactEvent in the root package's tracing.go).
//
// Pure library — no external dependencies, safe to construct once per
// session and reuse across many calls.
package redact

import (
	"bytes"
	"sort"
	"strings"
)

// Redactor replaces pre-loaded secret values with placeholder strings.
// A nil *Redactor is a safe no-op pass-through on every method, so
// "no redaction configured" needs no special-casing at call sites.
type Redactor struct {
	entries []entry
}

type entry struct {
	value       []byte
	placeholder []byte
}

// New returns a Redactor that replaces each values[i] with
// placeholders[i] in any text or bytes processed. The two slices must
// be the same length — a mismatch degrades to an empty (no-op)
// Redactor rather than panicking, since this is typically wired up at
// runtime and a construction bug shouldn't crash an agent run. Empty
// values are skipped, so a value that happens to be "" can't turn into
// "replace every byte."
//
// Entries are sorted longest-first: an overlap between two registered
// values (say "app" and "apple") would otherwise let the shorter one
// match first and carve into text that the longer, more specific value
// should have claimed.
func New(values, placeholders []string) *Redactor {
	if len(values) != len(placeholders) {
		return &Redactor{}
	}
	r := &Redactor{}
	for i, v := range values {
		if v == "" {
			continue
		}
		r.entries = append(r.entries, entry{value: []byte(v), placeholder: []byte(placeholders[i])})
	}
	sort.SliceStable(r.entries, func(i, j int) bool {
		return len(r.entries[i].value) > len(r.entries[j].value)
	})
	return r
}

// FromSecrets is the canonical constructor for redacting a named set
// of secret values — e.g. every value an ext.SecretPack-backed
// require("secret") getter could return this session. Each entry
// produces a redaction of the form:
//
//	value -> [redacted:secret.<key>]
//
// Returns nil when secrets is empty, so the caller's nil-Redactor path
// (already a no-op) needs no per-event allocation for the common case
// of a session with nothing to redact.
func FromSecrets(secrets map[string]string) *Redactor {
	if len(secrets) == 0 {
		return nil
	}
	values := make([]string, 0, len(secrets))
	placeholders := make([]string, 0, len(secrets))
	for k, v := range secrets {
		values = append(values, v)
		placeholders = append(placeholders, "[redacted:secret."+k+"]")
	}
	return New(values, placeholders)
}

// ApplyBytes returns b with every loaded value replaced by its
// placeholder. A nil receiver (or one with nothing registered) returns
// b unchanged.
//
// The trade-off of operating on raw bytes rather than a decoded
// structure: over-redaction if a registered value happens to appear
// inside unrelated text. Registered values are credential material, so
// that collision is rare enough in practice to accept in exchange for
// catching a secret at any nesting depth without walking the payload
// field by field.
func (r *Redactor) ApplyBytes(b []byte) []byte {
	if r == nil || len(r.entries) == 0 {
		return b
	}
	for _, e := range r.entries {
		b = bytes.ReplaceAll(b, e.value, e.placeholder)
	}
	return b
}

// Apply is the string equivalent of ApplyBytes.
func (r *Redactor) Apply(s string) string {
	if r == nil || len(r.entries) == 0 {
		return s
	}
	for _, e := range r.entries {
		s = strings.ReplaceAll(s, string(e.value), string(e.placeholder))
	}
	return s
}
