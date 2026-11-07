package view

import (
	"encoding/json"

	"github.com/mind-vm/thinking-core/sandbox"
)

// AskQuestion is sandbox's question as it crosses the wire; its json tags
// are the contract agentrun forwards.
type AskQuestion = sandbox.Question

// AskRequest is a pending ask() as agentrun's input_request event carries it.
type AskRequest struct {
	RequestID   string        `json:"requestId"`
	InputType   string        `json:"inputType"`
	Message     string        `json:"-"`
	Options     []string      `json:"options"`
	Placeholder string        `json:"placeholder"`
	Questions   []AskQuestion `json:"questions"`
}

// AskFromEvent decodes an input_request event's args. It goes through JSON
// rather than type assertions because the args hold a []sandbox.Question in
// process and a []any after a hop over the wire, and this decodes both.
func AskFromEvent(content string, args map[string]any) (AskRequest, error) {
	var r AskRequest
	b, err := json.Marshal(args)
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return r, err
	}
	r.Message = content
	return r, nil
}
