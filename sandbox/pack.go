package sandbox

import "github.com/dop251/goja"

// Pack is a self-contained unit of sandbox functionality.
//
// Each pack declares its name (must be unique per Sandbox), one-line
// description, a documentation block inlined into the agent's system
// prompt (this is how the LLM learns what functions are available
// and how to use them), and a Register function that installs JS
// functions into the Goja runtime.
//
// HelpEntries map JS function names to their detailed help text — the
// in-sandbox help(name) primitive reads from here, so the LLM can
// inspect a function it doesn't recognise without a round trip.
type Pack struct {
	// Name identifies the pack (e.g. "search", "fetch", "markdown").
	Name string

	// Description is a one-line summary shown in the skill-listing
	// system primitive.
	Description string

	// SkillType is what this pack reports as in skillList(), one of
	// SkillInfo's documented kinds. Empty means "pack" — the right
	// answer for anything that installs JS functions. A pack that
	// exists only to carry documentation the model retrieves with
	// skillGet(), such as one built from a SKILL.md file, should say
	// "prompt" so the listing does not claim it brings primitives.
	SkillType string

	// Prompt is the detailed documentation inlined into the agent's
	// system prompt — TypeScript-style `declare` lines work well
	// because the model recognises the shape.
	Prompt string

	// HelpEntries maps JS function names to their help text. Surfaced
	// via help(name).
	HelpEntries map[string]string

	// Register installs the pack's functions into the runtime. The
	// Sandbox pointer is provided for state access (event emission,
	// help-entry merging, skill-code registration).
	Register func(rt *goja.Runtime, s *Sandbox)
}
