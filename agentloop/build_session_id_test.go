package agentloop_test

import (
	"context"
	"testing"

	"github.com/mind-vm/thinking-core/agentloop"
	"github.com/mind-vm/thinking-core/llm"
	"github.com/mind-vm/thinking-core/sandbox"
)

// A store may answer an unknown id with a zero Session — agentloopmem
// does, and the SessionStore contract allows it. The run is still that
// session, and a capability that keys on it (a per-session
// InputRequester) must see the id on the very first turn, not "".
func TestBuildContextCarriesTheSessionIDOfAFreshSession(t *testing.T) {
	var seen []string
	capture := agentloop.Capability{
		Name: "capture",
		Build: func(bc agentloop.BuildContext) ([]sandbox.Pack, error) {
			seen = append(seen, bc.SessionID)
			return nil, nil
		},
	}
	loop := agentloop.New(agentloop.Config{
		LLM:            newFakeLLM("fake", llm.CompletionResponse{Content: "DONE\nok", Model: "m"}),
		Sessions:       newMemorySessions(), // nothing put: a fresh session
		Steps:          newMemorySteps(),
		SandboxBuilder: &agentloop.DefaultSandboxBuilder{Capabilities: []agentloop.Capability{capture}},
	})
	if _, err := loop.Run(context.Background(), agentloop.RunRequest{SessionID: "fresh-1", Message: "hi"}); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(seen) == 0 || seen[0] != "fresh-1" {
		t.Fatalf("BuildContext.SessionID = %q, want %q", seen, "fresh-1")
	}
}
