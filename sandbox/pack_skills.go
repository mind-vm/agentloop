package sandbox

import (
	"encoding/json"
	"fmt"

	"github.com/dop251/goja"
)

// SkillInfo describes one entry returned by the skillList() primitive.
// Type is one of "pack" | "module" | "prompt" | "webhook" depending
// on what backs the skill — packs are agentloop's built-ins,
// module/prompt/webhook come from application-supplied skill rows.
type SkillInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type"`
}

// SkillDiscoveryPack returns the pack that exposes skillList() and
// skillGet(name). The skills slice is the union of every other pack
// in the sandbox — assemblers (e.g. DefaultSandboxBuilder) build the
// list AFTER the other packs are constructed, then pass it here.
//
// skillList() returns the full slice as a JS array. skillGet(name)
// returns the detailed help text registered for `name`, falling back
// to the SkillInfo.Description when no detailed help was registered.
func SkillDiscoveryPack(skills []SkillInfo) Pack {
	// Coerce nil to an empty slice so json.Marshal returns "[]"
	// rather than "null" — agents call skillList().forEach(...) and
	// a null result would silently crash the JS run.
	if skills == nil {
		skills = []SkillInfo{}
	}

	helpByName := make(map[string]string, len(skills))
	for _, sk := range skills {
		helpByName[sk.Name] = sk.Description
	}

	return Pack{
		Name:        "skills",
		Description: "Skill discovery",
		Prompt: `// --- Discovery ---

interface SkillInfo { name: string; description: string; type: "pack" | "module" | "prompt" | "webhook" }
/** List all available skills. */
declare function skillList(): SkillInfo[];
/** Get detailed docs for a skill. Call before using unfamiliar skills. */
declare function skillGet(name: string): string;`,
		HelpEntries: map[string]string{
			"skillList": "skillList() — List all available skills. Returns [{name, description, type}].",
			"skillGet":  "skillGet(name) — Get detailed help for a skill. Returns description string.",
		},
		Register: func(rt *goja.Runtime, s *Sandbox) {
			_ = rt.Set("skillList", func(call goja.FunctionCall) goja.Value {
				b, _ := json.Marshal(skills)
				var jsResult any
				_ = json.Unmarshal(b, &jsResult)
				return rt.ToValue(jsResult)
			})

			_ = rt.Set("skillGet", func(call goja.FunctionCall) goja.Value {
				name := call.Argument(0).String()
				if name == "" {
					panic(rt.NewGoError(fmt.Errorf("skillGet: name is required")))
				}
				if desc, ok := s.HelpEntry(name); ok {
					return rt.ToValue(desc)
				}
				if desc, ok := helpByName[name]; ok {
					return rt.ToValue(desc)
				}
				return rt.ToValue(fmt.Sprintf("Skill %q not found. Call skillList() to see available skills.", name))
			})
		},
	}
}
