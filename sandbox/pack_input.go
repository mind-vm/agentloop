package sandbox

// InputRequester is the sandbox-side seam for human-in-the-loop. The
// fetch pack's allowUrls auto-prompt calls Request — implementations
// close over the run's HITL transport and block until the user
// replies.
//
// A nil InputRequester is the documented "no HITL for this run" case:
// FetchPack still requires the type to exist, but an unapproved-domain
// fetch simply fails with a policy-denial error instead of prompting.
type InputRequester interface {
	// Request dispatches the prompt and blocks until the user replies
	// (or the underlying transport errors out). The returned value's
	// concrete type depends on InputType:
	//   - "confirm" → bool
	//   - "choice"  → string (the selected option)
	//   - "text"    → string
	Request(req InputRequestData) (any, error)
}

// InputRequestData is the payload InputRequester.Request receives.
type InputRequestData struct {
	// RequestID is a caller-allocated identifier. Optional.
	RequestID string
	// InputType is one of: "confirm", "choice", "text".
	InputType string
	// Message is the question shown to the user.
	Message string
	// Options is required for InputType == "choice".
	Options []string
	// Placeholder is hint text for InputType == "text" (optional).
	Placeholder string
}
