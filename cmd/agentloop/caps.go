package main

import (
	"os"
	"slices"

	"github.com/mind-vm/agentloop"
	"github.com/mind-vm/agentloop/ext"
	"github.com/mind-vm/agentloop/llm"
	"github.com/mind-vm/agentloop/sandbox"
)

// networkCapabilities are the capabilities --no-network removes.
var networkCapabilities = []string{"fetch", "http"}

// buildCapabilities assembles what the agent can do this run.
//
// It starts from agentloop.DefaultCapabilities and edits that slice in
// place rather than reconstructing it, so a capability added to the
// library later still reaches the CLI without a change here.
//
// Two edits. DefaultCapabilities builds FetchPack with a nil
// InputRequester — the documented "no human available" case, where an
// unapproved domain fails instead of asking — and a CLI does have a
// human, so that capability's Build is swapped for one holding the
// requester. And the workspace pack is appended, rooted at the
// directory the invocation names.
func buildCapabilities(o *options, client llm.Client, requester sandbox.InputRequester, root *os.Root, warn func(string)) []agentloop.Capability {
	caps := agentloop.DefaultCapabilities(client, o.model)

	if o.noNetwork {
		caps = slices.DeleteFunc(caps, func(c agentloop.Capability) bool {
			return slices.Contains(networkCapabilities, c.Name)
		})
	} else {
		patched := false
		for i := range caps {
			if caps[i].Name != "fetch" {
				continue
			}
			caps[i].Build = func(bc agentloop.BuildContext) ([]sandbox.Pack, error) {
				return []sandbox.Pack{sandbox.FetchPack(bc.Ctx, requester)}, nil
			}
			patched = true
		}
		if !patched && warn != nil {
			warn("no fetch capability to attach approval prompts to — unapproved domains will fail rather than ask")
		}
	}

	if root != nil {
		caps = append(caps, agentloop.Capability{
			Name:        "workspace",
			Description: "read and change files in the project directory",
			Build: func(bc agentloop.BuildContext) ([]sandbox.Pack, error) {
				return []sandbox.Pack{
					ext.WorkspacePack(bc.Ctx, root, ext.WorkspaceOptions{Approver: requester}),
				}, nil
			},
		})
	}

	if !o.noExec {
		caps = append(caps, agentloop.Capability{
			Name:        "exec",
			Description: "run commands and read their output",
			Build: func(bc agentloop.BuildContext) ([]sandbox.Pack, error) {
				return []sandbox.Pack{
					ext.ExecPack(bc.Ctx, root, ext.ExecOptions{
						Approver:   requester,
						AllowShell: o.allowShell,
					}),
				}, nil
			},
		})
	}
	return caps
}
