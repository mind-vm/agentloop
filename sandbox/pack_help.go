package sandbox

import (
	"fmt"
	"strings"

	"github.com/dop251/goja"
)

// HelpPack provides the in-sandbox help() primitive. Should be
// registered LAST in the pack list so it sees every other pack's
// HelpEntries — the order is the caller's responsibility (e.g.
// DefaultSandboxBuilder), not this pack's.
func HelpPack() Pack {
	return Pack{
		Name:        "help",
		Description: "Primitive discovery",
		// help itself doesn't need system-prompt docs — it's a
		// utility, not a primitive the LLM should reach for first.
		Prompt: "",
		HelpEntries: map[string]string{
			"help": "help(name?) — Show available primitives. Call with a name for details.",
		},
		Register: func(rt *goja.Runtime, s *Sandbox) {
			_ = rt.Set("help", func(call goja.FunctionCall) goja.Value {
				if len(call.Arguments) > 0 && !goja.IsUndefined(call.Argument(0)) {
					name := call.Argument(0).String()
					if desc, ok := s.HelpEntry(name); ok {
						return rt.ToValue(desc)
					}
					return rt.ToValue(fmt.Sprintf("Unknown primitive: %s. Call help() for a full list.", name))
				}

				var lines []string
				lines = append(lines, "Available primitives:", "")
				for _, e := range s.HelpEntries() {
					firstLine := e.Description
					if idx := strings.Index(e.Description, "\n"); idx > 0 {
						firstLine = e.Description[:idx]
					}
					lines = append(lines, fmt.Sprintf("  %s — %s", e.Name, firstLine))
				}
				lines = append(lines, "", "Call help(name) for detailed help on a specific primitive.")
				return rt.ToValue(strings.Join(lines, "\n"))
			})
		},
	}
}
