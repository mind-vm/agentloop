package agentloopmem_test

import (
	"testing"

	"github.com/jryannel/agentloop"
	"github.com/jryannel/agentloop/agentloopmem"
	"github.com/jryannel/agentloop/agentlooptest"
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
