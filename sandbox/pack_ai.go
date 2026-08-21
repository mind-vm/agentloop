package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/dop251/goja"

	"github.com/jryannel/agentloop/llm"
)

// codeFenceRe strips an optional wrapping code fence from an LLM
// response, regardless of language tag (```json, ```javascript,
// ```markdown, …). Models like Gemini often wrap output when the
// prompt implies a format; agents shouldn't have to hand-write
// indexOf/substring boilerplate to read the payload.
var codeFenceRe = regexp.MustCompile("(?s)^\\s*```[a-zA-Z0-9_+-]*\\s*\n?(.*?)\\s*```\\s*$")

func stripCodeFences(s string) string {
	s = strings.TrimSpace(s)
	if m := codeFenceRe.FindStringSubmatch(s); m != nil {
		return strings.TrimSpace(m[1])
	}
	return s
}

// aiModuleJS is the JS wrapper installed as the `ai` require() module.
// The agent calls `require('ai').ai(...)` etc.; the wrapper forwards
// to internal globals (_ai_text, _ai_json) registered by AIPack.
const aiModuleJS = `
exports.ai = function(prompt, system) { return _ai_text(prompt, system); };
exports.aiJSON = function(prompt, schema) { return _ai_json(prompt, schema); };
`

// AIPack exposes synchronous LLM sub-calls inside the sandbox:
//
//   - ai(prompt, system?) — free-text completion. Fences stripped.
//   - aiJSON(prompt, schema?) — JSON-shaped completion. The pack
//     augments the prompt with a "respond with JSON" instruction and
//     parses the response. Schema is best-effort: the pack inlines it
//     in the prompt rather than passing it to a native JSON-mode API
//     (llm.Client doesn't yet expose JSON mode).
//
// Both functions emit EventAI for the trace and gate through the
// installed PolicyChecker (under tool name "ai") so quota / tier
// limits can block them.
//
// The pack also wires require('ai') as a module — a script can stick
// to either style.
func AIPack(ctx context.Context, client llm.Client, model string) Pack {
	return Pack{
		Name:        "ai",
		Description: "LLM sub-calls — text synthesis and JSON-shaped output",
		Prompt: `// --- AI ---

/** Free-text LLM call — summarize, classify, translate, any text synthesis. Synchronous; returns the text. */
declare function ai(prompt: string, system?: string): string;
/**
 * LLM call that returns parsed JSON. Pass the shape you want as schema — an example
 * value works best: aiJSON("Extract every row from:\n" + text, [{ name: "string", total: "number" }]).
 * The schema guides the model (inlined into the prompt); the result is parsed but NOT
 * validated, so check the fields you rely on.
 */
declare function aiJSON(prompt: string, schema?: unknown): any;`,
		HelpEntries: map[string]string{
			"ai": `ai(prompt, system?) — Free-text LLM call. Returns string.
  Example: var s = ai("Summarize: " + text);`,
			"aiJSON": `aiJSON(prompt, schema?) — JSON-shaped LLM call. Returns parsed value.
  schema is inlined into the prompt as a guideline; the pack strips fences and JSON.parses.
  Example: var cities = aiJSON("List 10 German cities", { type: "array", items: { type: "string" } });`,
		},
		Register: func(rt *goja.Runtime, sb *Sandbox) {
			aiText := func(call goja.FunctionCall) goja.Value {
				if len(call.Arguments) == 0 || call.Argument(0).String() == "" {
					panic(rt.NewGoError(fmt.Errorf("ai: prompt is required")))
				}
				prompt := call.Argument(0).String()
				var system string
				if len(call.Arguments) > 1 && !goja.IsUndefined(call.Argument(1)) {
					system = call.Argument(1).String()
				}

				sb.CheckPolicyOrPanic(rt, ctx, "ai", map[string]any{"prompt": prompt})
				text, err := callAI(ctx, sb, client, model, prompt, system)
				if err != nil {
					panic(rt.NewGoError(err))
				}
				return rt.ToValue(stripCodeFences(text))
			}

			aiJSON := func(call goja.FunctionCall) goja.Value {
				if len(call.Arguments) == 0 || call.Argument(0).String() == "" {
					panic(rt.NewGoError(fmt.Errorf("aiJSON: prompt is required")))
				}
				prompt := call.Argument(0).String()
				var schemaPrompt string
				if len(call.Arguments) > 1 && !goja.IsUndefined(call.Argument(1)) && !goja.IsNull(call.Argument(1)) {
					schemaBytes, err := json.Marshal(call.Argument(1).Export())
					if err != nil {
						panic(rt.NewGoError(fmt.Errorf("aiJSON: schema: %w", err)))
					}
					schemaPrompt = "\n\nRespond with JSON matching this schema:\n" + string(schemaBytes)
				} else {
					schemaPrompt = "\n\nRespond with a JSON value."
				}

				sb.CheckPolicyOrPanic(rt, ctx, "ai", map[string]any{"prompt": prompt})
				text, err := callAI(ctx, sb, client, model, prompt+schemaPrompt, "Respond with raw JSON only; no prose, no code fences.")
				if err != nil {
					panic(rt.NewGoError(err))
				}
				stripped := stripCodeFences(text)
				var parsed any
				if err := json.Unmarshal([]byte(stripped), &parsed); err != nil {
					sb.Emit(Event{Kind: EventError, Summary: "aiJSON: parse failure", Detail: truncate(stripped, 200)})
					panic(rt.NewGoError(fmt.Errorf("aiJSON: response was not valid JSON: %w (raw: %q)", err, truncate(stripped, 200))))
				}
				return rt.ToValue(parsed)
			}

			// Bare globals — the primary surface advertised by the
			// system prompt.
			_ = rt.Set("ai", aiText)
			_ = rt.Set("aiJSON", aiJSON)

			// require('ai') alias — convenience for skills that want
			// an explicit module-style import.
			_ = rt.Set("_ai_text", aiText)
			_ = rt.Set("_ai_json", aiJSON)
			sb.AddSkillCode("ai", aiModuleJS)
		},
	}
}

// callAI centralizes the llm.Client call shared by _ai_text and
// _ai_json. Emits EventAI before and after so the trace shows the LLM
// hop. Errors surface as EventError before the caller raises into
// Goja.
func callAI(ctx context.Context, sb *Sandbox, client llm.Client, model, prompt, system string) (string, error) {
	sb.Emit(Event{Kind: EventAI, Summary: fmt.Sprintf("ai: %s", truncate(prompt, 80)), Detail: prompt})

	msgs := make([]llm.Message, 0, 2)
	if system != "" {
		msgs = append(msgs, llm.Message{Role: "system", Content: system})
	}
	msgs = append(msgs, llm.Message{Role: "user", Content: prompt})

	resp, err := client.Complete(ctx, llm.CompletionRequest{Messages: msgs, Model: model})
	if err != nil {
		sb.Emit(Event{Kind: EventError, Summary: "ai: generation error", Detail: err.Error()})
		return "", fmt.Errorf("ai: %w", err)
	}
	sb.Emit(Event{Kind: EventAI, Summary: fmt.Sprintf("ai: done (%d chars)", len(resp.Content)), Result: truncate(resp.Content, 200)})
	return resp.Content, nil
}

// truncate clamps s to at most n bytes, ending with "..." when cut.
// Defensive against negative n for safety; n <= 3 returns "...".
func truncate(s string, n int) string {
	if n <= 3 {
		return "..."
	}
	if len(s) <= n {
		return s
	}
	return s[:n-3] + "..."
}
