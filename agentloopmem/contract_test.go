package agentloopmem_test

import (
	"testing"

	"github.com/mind-vm/agentloop"
	"github.com/mind-vm/agentloop/agentloopmem"
	"github.com/mind-vm/agentloop/agentlooptest"
)

// TestStepStore_Contract runs the shared StepStore conformance harness
// against the in-memory implementation. It's the worked example an
// application copies: implement agentloop.StepStore, then point the
// same contract at a fresh instance.
func TestStepStore_Contract(t *testing.T) {
	agentlooptest.StepStoreContract(t, agentlooptest.StepStoreHarness{
		NewStore: func() agentloop.StepStore { return agentloopmem.NewStepStore() },
		// In-memory steps have no session FK → no EnsureSession needed.
	})
}

// TestSessionStore_Contract runs the shared SessionStore conformance
// harness against the in-memory implementation — the reference the
// contract was written from, and so the check that it specifies the
// behaviour the loop relies on rather than one store's incidentals.
func TestSessionStore_Contract(t *testing.T) {
	agentlooptest.SessionStoreContract(t, agentlooptest.SessionStoreHarness{
		NewStore: func() agentloop.SessionStore { return agentloopmem.NewSessionStore(nil) },
	})
}
