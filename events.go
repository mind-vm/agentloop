package agentloop

// RunEvent is one observability emission the loop streams to
// RunRequest.OnEvent in real time.
//
// Type ∈ {user, execute_js, execute_js_result, response, response_chunk,
// sandbox_event, data_update, warning, error, done}.
type RunEvent struct {
	Type    string
	Content string
	Tool    string
	Args    map[string]any
	Summary *RunSummary
	Tokens  *CallTokens
}

// CallTokens is the per-LLM-call token count carried on execute_js_result
// and response events, for fine-grained reporting.
type CallTokens struct {
	Prompt     int32
	Completion int32
}

// RunSummary rides the terminal "done" event — the same numbers RunResult
// carries, for a caller that only subscribes to the event stream.
type RunSummary struct {
	SessionID string
	Steps     int
	Tokens    struct {
		Prompt     int32 `json:"prompt"`
		Completion int32 `json:"completion"`
	}
	DataBytesCarried int64
}
