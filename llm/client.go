// Package llm is the LLM client interface agentloop's ai()/aiJSON()
// sandbox capability calls against, and what drives the agent loop's
// own turn-taking.
package llm

import "context"

// Message is one turn of a chat completion request.
type Message struct {
	Role    string // "system", "user", or "assistant"
	Content string
}

// CompletionRequest is one call to Client.Complete or Client.Stream.
type CompletionRequest struct {
	Messages    []Message
	Model       string // empty uses the client's configured default
	Temperature *float64
	MaxTokens   *int
}

// CompletionResponse is what a completion call returns.
type CompletionResponse struct {
	Content      string
	Model        string
	InputTokens  int
	OutputTokens int
}

// Client is what the ai()/aiJSON() sandbox capability, and the agent
// loop's own turn-taking, call against.
type Client interface {
	Name() string
	Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error)
	// Stream delivers content deltas through onChunk as they arrive and
	// still returns the full accumulated response — "best-effort
	// incremental, full response always", so a caller that doesn't care
	// about incremental output can pass a nil onChunk and just use the
	// return value.
	Stream(ctx context.Context, req CompletionRequest, onChunk func(chunk string)) (CompletionResponse, error)
}
