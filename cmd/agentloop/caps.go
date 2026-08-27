package main

import (
	"github.com/mind-vm/agentloop"
	"github.com/mind-vm/agentloop/llm"
	"github.com/mind-vm/agentloop/sandbox"
)

// fetchCapability is the name DefaultCapabilities gives the capability
// that owns the human-in-the-loop seam.
const fetchCapability = "fetch"

// capabilitiesWithApproval is the default bundle with fetch's approval
// seam filled in.
//
// agentloop.DefaultCapabilities builds FetchPack with a nil
// InputRequester, which is the documented "no human available" case: a
// fetch to an unapproved domain fails instead of asking. A CLI does have
// a human available, so it swaps that one capability's Build for a
// version holding the requester.
//
// It patches the bundle in place rather than reconstructing it, so a
// capability added to the library later still reaches the CLI without a
// change here. The reported bool says whether the fetch capability was
// actually found — if the library ever renames it, the caller should say
// so out loud rather than silently losing every approval prompt.
func capabilitiesWithApproval(client llm.Client, model string, requester sandbox.InputRequester) ([]agentloop.Capability, bool) {
	caps := agentloop.DefaultCapabilities(client, model)
	patched := false
	for i := range caps {
		if caps[i].Name != fetchCapability {
			continue
		}
		caps[i].Build = func(bc agentloop.BuildContext) ([]sandbox.Pack, error) {
			return []sandbox.Pack{sandbox.FetchPack(bc.Ctx, requester)}, nil
		}
		patched = true
	}
	return caps, patched
}
