package agentloop

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/jryannel/agentloop/llm"
)

// staleCodeMarker replaces the body of an already-executed run() in the
// history sent to the model. Its return value is in `args` and its
// output in the result message that follows, so the verbatim source is
// redundant.
const staleCodeMarker = "[earlier run() omitted — its result is in the message below; its data is in args]"

// elideStaleCode returns a copy of history with every assistant message
// except the most recent replaced by a one-line marker. A past run()'s
// effect is fully captured by the threaded `args` and the logs, so the
// only block worth keeping verbatim is the latest one (which the model
// may need to repair on an error-retry). This removes the triangular
// re-send of stale code that otherwise dominates prompt growth.
// Non-assistant messages pass through untouched.
func elideStaleCode(history []llm.Message) []llm.Message {
	lastAssistant := -1
	for i, m := range history {
		if m.Role == "assistant" {
			lastAssistant = i
		}
	}
	if lastAssistant < 0 {
		return history
	}
	out := make([]llm.Message, len(history))
	copy(out, history)
	for i, m := range out {
		if m.Role == "assistant" && i != lastAssistant {
			out[i] = llm.Message{Role: "assistant", Content: staleCodeMarker}
		}
	}
	return out
}

// dataShape renders a compact structural digest of a turn's return
// value: keys and types, array lengths, nested field names — but NOT
// the values. It is what the loop shows the model in place of the full
// data blob, so the model knows which fields it can reach on next
// turn's `args` without the values inflating the prompt.
//
// Rendered as a TypeScript type literal — e.g.
// { rows: { title: string }[] /* 3 items */ } — so the model reads it
// with the same instincts it applies to the ## Sandbox API declarations.
func dataShape(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "{}" {
		return ""
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return ""
	}
	return shapeOf(v, 0)
}

const (
	shapeMaxDepth = 4
	shapeMaxKeys  = 24
)

func shapeOf(v any, depth int) string {
	switch t := v.(type) {
	case map[string]any:
		if depth >= shapeMaxDepth {
			return "object"
		}
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		truncated := 0
		if len(keys) > shapeMaxKeys {
			truncated = len(keys) - shapeMaxKeys
			keys = keys[:shapeMaxKeys]
		}
		parts := make([]string, 0, len(keys)+1)
		for _, k := range keys {
			parts = append(parts, k+": "+shapeOf(t[k], depth+1))
		}
		if truncated > 0 {
			parts = append(parts, fmt.Sprintf("/* +%d more keys */", truncated))
		}
		return "{ " + strings.Join(parts, "; ") + " }"
	case []any:
		if len(t) == 0 {
			return "unknown[] /* empty */"
		}
		if depth >= shapeMaxDepth {
			return fmt.Sprintf("unknown[] /* %d items */", len(t))
		}
		return fmt.Sprintf("%s[] /* %d items */", elemShape(t[0], depth+1), len(t))
	case string:
		return "string"
	case float64, int, int64:
		return "number"
	case bool:
		return "boolean"
	case nil:
		return "null"
	default:
		return "any"
	}
}

// elemShape renders an array's element type for the `T[]` suffix form.
// Object literals are already brace-delimited and primitives are single
// words; any other composite shape is parenthesised so the [] suffix
// binds unambiguously.
func elemShape(v any, depth int) string {
	s := shapeOf(v, depth)
	if strings.HasPrefix(s, "{") || !strings.Contains(s, " ") {
		return s
	}
	return "(" + s + ")"
}
