package agentloop

// RunEvent is one observability emission the loop streams to
// RunRequest.OnEvent in real time.
//
// The Type discriminator names what fields are populated:
//
//	user              user turn persisted; Content = message
//	execute_js        agent emitted a JS block; Content = the JS source
//	execute_js_result a JS block finished; Content = textual result
//	sandbox_event     a primitive emitted observability; Args carries
//	                  the underlying sandbox.Event fields
//	data_update       the agent's carried data changed; Args = new value
//	compacted         history was summarized before the run's turns;
//	                  Args = {"messages_before", "messages_after"}
//	response          final markdown answer; Content = the answer
//	response_chunk    streamed token from a final-text turn;
//	                  Content = the chunk (no Args)
//	warning           non-fatal degradation; Content = human-readable detail
//	error             a step errored; Content = human-readable error
//	done              terminal event; Summary = aggregate RunSummary
//
// Tokens is populated on `response` and `execute_js_result` events to
// attribute LLM cost back to the step that incurred it.
type RunEvent struct {
	Type    string         `json:"type"`
	Content string         `json:"content,omitempty"`
	Tool    string         `json:"tool,omitempty"`
	Args    map[string]any `json:"args,omitempty"`
	Summary *RunSummary    `json:"summary,omitempty"`
	Tokens  *CallTokens    `json:"tokens,omitempty"`
}

// CallTokens is the per-LLM-call token count carried on execute_js_result
// and response events, for fine-grained reporting.
type CallTokens struct {
	Prompt     int32 `json:"prompt"`
	Completion int32 `json:"completion"`
}

// RunSummary rides the terminal "done" event — the same numbers RunResult
// carries, for a caller that only subscribes to the event stream.
type RunSummary struct {
	SessionID string `json:"session_id"`
	Steps     int    `json:"steps"`
	Tokens    struct {
		Prompt     int32 `json:"prompt"`
		Completion int32 `json:"completion"`
	} `json:"tokens"`
	// DataBytesCarried is the run's context-economy measurement: bytes
	// of threaded working state withheld from prompts, summed per LLM
	// call, net of the shape digests sent.
	DataBytesCarried int64 `json:"data_bytes_carried,omitempty"`
}
