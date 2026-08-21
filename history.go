package agentloop

import "github.com/jryannel/agentloop/llm"

// HistoryWindow is the default cap on how many prior steps the loop
// replays into the LLM's context window (override via
// Config.HistoryWindow). Roughly the last several user turns of full
// reasoning trails before the oldest start to drop — beyond this the
// prompt gets expensive and the model loses the user's actual question
// in the noise.
const HistoryWindow = 80

// rehydrateHistory converts the loop's persisted RunSteps into the
// []llm.Message slice the LLM sees as the conversation, so
// multi-turn chat works across separate Run calls. Only step types that
// map cleanly to LLM turns are replayed; "error" rows stay in the trace
// but don't feed back to the model.
//
// Window: the most recent `window` steps are kept in chronological
// order (non-positive falls back to the HistoryWindow default). Sessions
// shorter than the window replay in full.
func rehydrateHistory(steps []RunStep, window int) []llm.Message {
	if window <= 0 {
		window = HistoryWindow
	}
	if len(steps) > window {
		steps = steps[len(steps)-window:]
	}
	out := make([]llm.Message, 0, len(steps))
	for _, step := range steps {
		role, text, ok := stepToTurn(step)
		if !ok {
			continue
		}
		out = append(out, llm.Message{Role: role, Content: text})
	}
	return out
}

// stepToTurn maps a RunStep onto an llm.Message turn. Returns
// (_, _, false) when the step type doesn't belong in LLM history.
//
// execute_js turns get re-fenced so the model recognises its own
// previous emission shape. execute_js_result turns get the "Execution
// result:" prefix the loop's run-time variant uses too.
func stepToTurn(step RunStep) (role, text string, ok bool) {
	switch step.StepType {
	case "user":
		return "user", step.Content, true
	case "response":
		return "assistant", step.Content, true
	case "execute_js":
		return "assistant", "```javascript\n" + step.Content + "\n```", true
	case "execute_js_result":
		return "user", "Execution result:\n" + step.Content, true
	default:
		return "", "", false
	}
}
