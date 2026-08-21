package agentloop

import (
	"regexp"
	"strings"
)

// jsFenceRe matches the first ```javascript or ```js fenced block.
// (?s) enables dotall so the body can contain newlines; (?:javascript|js)
// accepts both aliases the model uses.
var jsFenceRe = regexp.MustCompile("(?s)```(?:javascript|js)\\s*\n(.*?)\n```")

// ExtractJSBlock returns the contents of the first ```javascript or
// ```js fenced block, or "" if none is present. Whitespace inside the
// block is trimmed — some models add a blank line right after the fence
// and the intent is unaffected.
func ExtractJSBlock(s string) string {
	m := jsFenceRe.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(m[1])
}

// doneMarkerRe matches "DONE" on its own line (optionally indented)
// followed by the final answer on subsequent lines.
var doneMarkerRe = regexp.MustCompile(`(?m)^\s*DONE\s*\n([\s\S]*)`)

// ExtractDoneMarker reports whether s contains a terminating DONE
// marker, and if so returns the answer that follows it. The marker must
// be on its own line — an inline "DONE" inside prose doesn't prematurely
// terminate the run. answer() is the documented way to finish; this is
// a defensive fallback for a model that emits the legacy marker instead.
func ExtractDoneMarker(s string) (done bool, final string) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "DONE" {
		return true, ""
	}
	m := doneMarkerRe.FindStringSubmatch(s)
	if m == nil {
		return false, ""
	}
	return true, strings.TrimSpace(m[1])
}
