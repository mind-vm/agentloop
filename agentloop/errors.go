package agentloop

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// sanitizeErrorForModel strips runtime plumbing from a sandbox error so
// the model sees only the semantic message. It removes goja's appended
// stack frames (which leak internal Go symbols like
// "FetchPack.func3 (native)" and mean nothing to the agent) and the
// "sandbox:"/"GoError:" wrapper prefixes, while preserving JS error
// kinds (ReferenceError/TypeError) that retryHintForError and the model
// rely on.
//
//	raw:  "sandbox: GoError: fetch: dial tcp: i/o timeout at agentloop/sandbox.doFetchRequest.func1 (native)"
//	out:  "fetch: dial tcp: i/o timeout"
//
// stackFrame is the first inline goja frame: " at <sym> (native)",
// " at <fn> (<eval>:…)" or " at <eval>:l:c(pc)".
var stackFrame = regexp.MustCompile(` at (\S+ \((native|<eval>[^)]*)\)|<eval>:\d+)`)

// frameLine is a line that is nothing but a goja frame, as
// Exception.String() writes them ("\tat f (<eval>:2:17(2))"). It must match
// the whole line: a message line such as "  at least one row is required"
// is content, and dropping it is the same bug as cutting at the newline.
var frameLine = regexp.MustCompile(`^\s*at (native|<eval>:\d+:\d+(\(\d+\))?|\S+ \((native|<eval>.*)\))\s*$`)

func sanitizeErrorForModel(raw string) string {
	// A message may span lines — a pack listing every problem it found, one
	// per line. goja's Error() keeps those lines and appends only the first
	// frame inline on the last one; Exception.String() adds one frame per
	// line after them. Cutting at the first newline (as this once did) kept
	// the heading "Fix and call ui() again:" and threw away the list it
	// introduced, so drop frame lines and inline frames, never message lines.
	var kept []string
	for _, line := range strings.Split(raw, "\n") {
		if frameLine.MatchString(line) {
			continue
		}
		// Cut where the first inline frame begins — not at the first " at ",
		// which a message may hold itself ("does not stand at the top of …"):
		// cutting there hid the half of a refusal that says what is allowed
		// instead.
		if loc := stackFrame.FindStringIndex(line); loc != nil {
			line = line[:loc[0]]
		}
		kept = append(kept, strings.TrimRight(line, " \t"))
	}
	msg := strings.Join(kept, "\n")

	msg = strings.TrimSpace(msg)
	msg = strings.TrimPrefix(msg, "sandbox: ")
	// GoError: is goja's wrapper for a thrown Go error — noise. (ReferenceError/
	// TypeError are real JS kinds and are deliberately left intact.)
	msg = strings.ReplaceAll(msg, "GoError: ", "")
	return strings.TrimSpace(msg)
}

// retryHintForError pattern-matches common script failure modes and
// returns a concrete instruction string appended to the error fed back
// to the model. Empty string when there's no specific hint — the model
// then reasons from the raw error alone. Intentionally generic (no
// application-schema knowledge): an application with its own capability
// packs and error shapes (foreign-key violations, enum constraints,
// domain-specific field names) should layer its own hints in front of
// this — sort by specificity and return the first non-empty result.
func retryHintForError(errMsg string) string {
	lower := strings.ToLower(errMsg)

	// Transient infrastructure errors — timeouts and cancellations. These are
	// NOT the model's fault, and a write may or may not have landed, so
	// blindly repeating it risks a duplicate. Tell it to verify first.
	if strings.Contains(lower, "context deadline exceeded") ||
		strings.Contains(lower, "context canceled") ||
		strings.Contains(lower, "timeout") || strings.Contains(lower, "timed out") {
		return "This was a transient timeout, not a problem with your code. The operation may or may not " +
			"have completed. Before retrying a write, read the current state back first to check whether it " +
			"already succeeded — retry only if it did not. Do not repeat it blindly."
	}

	// ReferenceError / hallucinated APIs.
	if strings.Contains(lower, "referenceerror") {
		return "You referenced a function or variable that doesn't exist. The declarations under Sandbox API " +
			"in the system prompt are the complete list: call each exactly as it is declared there, with the " +
			"prefix it is declared under or none."
	}

	// TypeError on result properties — usually a shape mismatch.
	if strings.Contains(lower, "typeerror") && strings.Contains(lower, "of undefined") {
		return "You tried to read a property of an undefined value. Most likely the call's return shape " +
			"isn't what you assumed — check its declaration under Sandbox API in the system prompt."
	}

	return ""
}

// scriptLineOffset is how many lines the sandbox's wrapper puts above the
// model's script, so an engine line number minus this is the script's.
const scriptLineOffset = 1

var (
	syntaxLocRe = regexp.MustCompile(`Line (\d+):(\d+)`)
	evalLocRe   = regexp.MustCompile(`<eval>:(\d+):(\d+)`)
)

// scriptLocation finds where an engine error points in the model's own
// script: its 1-based line and column, and whether it lies past the end.
func scriptLocation(raw, code string, preludeLines int) (line, col int, pastEnd, ok bool) {
	m := syntaxLocRe.FindStringSubmatch(raw)
	if m == nil {
		m = evalLocRe.FindStringSubmatch(raw)
	}
	if m == nil {
		return 0, 0, false, false
	}
	engineLine, _ := strconv.Atoi(m[1])
	col, _ = strconv.Atoi(m[2])
	line = engineLine - scriptLineOffset - preludeLines
	lines := strings.Split(code, "\n")
	if line < 1 {
		return 0, 0, false, false
	}
	return line, col, line > len(lines), true
}

// describeScriptError is the error a model sees for a failed script: the
// message with the engine's line number replaced by the script's, the
// offending line with a caret under the column, and a hint for the two
// syntax failures the traces show most — a backtick that closes a
// template literal early, and a script that ends inside an open literal.
func describeScriptError(raw, code string, preludeLines int) (message, location, hint string) {
	message = sanitizeErrorForModel(raw)
	line, col, pastEnd, ok := scriptLocation(raw, code, preludeLines)
	if !ok {
		if preludeLines > 0 && (syntaxLocRe.MatchString(raw) || evalLocRe.MatchString(raw)) {
			return message, "while writing the fenced file blocks", ""
		}
		return message, "", ""
	}
	message = syntaxLocRe.ReplaceAllString(message, fmt.Sprintf("Line %d:%d", line, col))
	isSyntax := strings.Contains(message, "SyntaxError")

	lines := strings.Split(code, "\n")
	if pastEnd {
		location = "at the end of your script"
		if isSyntax {
			hint = "The script ended while a template literal, string, bracket or brace was still open. " +
				"A backtick inside a template literal closes it early: escape it as \\`, or put the text " +
				"(Go code, markdown) in its own fenced block after the script and read it as blocks[0]."
		}
		return message, location, hint
	}
	src := lines[line-1]
	caret := ""
	if col > 0 && col <= len(src)+1 {
		caret = "\n  " + strings.Repeat(" ", col-1) + "^"
	}
	location = fmt.Sprintf("at line %d of your script:\n  %s%s", line, src, caret)
	if isSyntax && strings.Count(src, "`") > 0 {
		hint = "A backtick inside a template literal closes it early. Escape it as \\`, or put the text " +
			"(Go code, markdown) in its own fenced block after the script and read it as blocks[0]."
	}
	return message, location, hint
}
