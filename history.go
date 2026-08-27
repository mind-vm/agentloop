package agentloop

import (
	"strings"

	"github.com/mind-vm/agentloop/llm"
)

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
//
// Compaction checkpoints: when the loaded steps contain a
// StepTypeSummary checkpoint, its summary is replayed in place of every
// step it covers, and history resumes at the step its marker names.
// That is what stops a compacted session from being summarized again on
// every Run. Only the most recent checkpoint is replayed — an older
// one's content was itself folded into the newer summary, so replaying
// both would double it.
//
// The second return value is the StepIndex each message came from,
// positionally aligned with the first. compactHistory needs it to
// translate a compactor's message-space split back into the step index
// a new checkpoint has to name.
func rehydrateHistory(steps []RunStep, window int) ([]llm.Message, []int32) {
	if window <= 0 {
		window = HistoryWindow
	}
	if len(steps) > window {
		steps = steps[len(steps)-window:]
	}

	summary, retainFrom, at, compacted := lastCheckpoint(steps)

	out := make([]llm.Message, 0, len(steps)+1)
	stepOf := make([]int32, 0, len(steps)+1)
	if compacted {
		out = append(out, llm.Message{Role: "user", Content: summary})
		stepOf = append(stepOf, at)
	}
	for _, step := range steps {
		// A checkpoint is not a conversational turn, and only the most
		// recent one is replayed — handled above.
		if step.StepType == StepTypeSummary {
			continue
		}
		// Everything the checkpoint covers is represented by its summary.
		if compacted && step.StepIndex < retainFrom {
			continue
		}
		role, text, ok := stepToTurn(step)
		if !ok {
			continue
		}
		out = append(out, llm.Message{Role: role, Content: text})
		stepOf = append(stepOf, step.StepIndex)
	}
	return out, stepOf
}

// lastCheckpoint returns the most recent usable compaction checkpoint
// among steps, along with the step index it retains from and its own
// index.
//
// A checkpoint with an empty summary or an unreadable marker is skipped
// rather than honoured: replaying nothing in place of the history it
// claims to cover would erase that stretch of the conversation
// outright, which is strictly worse than the redundant summarization
// that ignoring it costs.
func lastCheckpoint(steps []RunStep) (summary string, retainFrom, at int32, ok bool) {
	for i := len(steps) - 1; i >= 0; i-- {
		step := steps[i]
		if step.StepType != StepTypeSummary || strings.TrimSpace(step.Content) == "" {
			continue
		}
		cp, err := decodeCheckpoint(step.ToolArgs)
		if err != nil {
			continue
		}
		return step.Content, cp.RetainFromStep, step.StepIndex, true
	}
	return "", 0, 0, false
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
		return "user", executionResultPrefix + "\n" + step.Content, true
	default:
		return "", "", false
	}
}
