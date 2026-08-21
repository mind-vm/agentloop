package sandbox

import (
	"fmt"
	"regexp"
	"sort"

	"github.com/dop251/goja"
)

var validIdentifier = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// reservedNames are JS identifiers user-authored skill modules can't
// take — either because the sandbox already exposes them as core
// primitives (log, fetch, htmlToMarkdown, …) or because they're JS
// keywords. Validation catches a clash at write time before the
// module ever runs.
var reservedNames = map[string]bool{
	// Sandbox primitives & companion modules.
	"log": true, "print": true, "console": true, "text": true, "parseUrl": true,
	"help": true, "_http": true, "fetch": true, "htmlToMarkdown": true,
	"listStores": true, "getDocument": true, "ask": true,
	"_md_links": true, "_md_headings": true, "_md_items": true,
	"_md_sections": true, "_md_plain": true,
	// JS reserved words.
	"undefined": true, "null": true, "true": true, "false": true,
	"var": true, "function": true, "return": true, "if": true, "else": true,
	"for": true, "while": true, "do": true, "switch": true, "case": true,
	"break": true, "continue": true, "new": true, "this": true, "typeof": true,
	"delete": true, "void": true, "in": true, "instanceof": true, "try": true,
	"catch": true, "finally": true, "throw": true, "with": true,
}

// ReservedModuleNames returns a sorted snapshot of the reserved-name
// list. Surfaces externally (skill authoring guide, validation error
// detail) read from this rather than poking at private state.
func ReservedModuleNames() []string {
	out := make([]string, 0, len(reservedNames))
	for n := range reservedNames {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// ValidateModuleName reports whether name is a valid JS identifier
// and not in the reserved set. Used by product UIs to gate skill
// creation before persisting.
func ValidateModuleName(name string) error {
	if !validIdentifier.MatchString(name) {
		return fmt.Errorf("skill name %q is not a valid JavaScript identifier", name)
	}
	if reservedNames[name] {
		return fmt.Errorf("skill name %q is reserved", name)
	}
	return nil
}

// ValidateModuleCode checks JS code for syntax errors without
// executing it. The function-wrap mirrors the runtime exec wrap so
// "compiles" here means "will compile at registration time too."
func ValidateModuleCode(name, code string) error {
	wrapped := fmt.Sprintf("(function %s(args) {\n%s\n})", name, code)
	_, err := goja.Compile(name, wrapped, false)
	if err != nil {
		return fmt.Errorf("syntax error: %w", err)
	}
	return nil
}

// RegisterModule registers a JS module skill as a callable function
// in the sandbox. Used when a skill needs to be added after sandbox
// construction (e.g. mid-session); steady-state skill loading goes
// through SkillPack at construction time.
func (s *Sandbox) RegisterModule(name, code, description string) error {
	if err := ValidateModuleName(name); err != nil {
		return err
	}
	wrapped := fmt.Sprintf("function %s(args) {\n%s\n}", name, code)
	if _, err := goja.Compile(name, wrapped, false); err != nil {
		return fmt.Errorf("module %s: %w", name, err)
	}
	if _, err := s.runtime.RunString(wrapped); err != nil {
		return fmt.Errorf("module %s: registration failed: %w", name, err)
	}

	helpText := description
	if helpText == "" {
		helpText = "Custom module skill."
	}
	s.AddHelpEntry(name, fmt.Sprintf("%s(args) — %s", name, helpText))
	return nil
}

// SkillPack returns a Pack that wraps a user-authored JS skill
// module. It contributes the skill's docs to the system prompt and
// registers the JS function in the sandbox.
//
// A skill name "foo" gets:
//
//  1. AddSkillCode("foo", code) so require("foo") can load it.
//  2. A function _skill_foo(args) defined in the runtime (the
//     user's code wrapped).
//  3. A Go-backed wrapper bound to globalThis.foo that emits an
//     EventSkillCall event before delegating to _skill_foo.
//
// The wrapper indirection is what gives observability — every skill
// invocation shows up in the trace without the user having to add
// any instrumentation.
func SkillPack(name, code, description string) Pack {
	desc := description
	if desc == "" {
		desc = "Custom skill"
	}
	promptLine := fmt.Sprintf("/** %s */\ndeclare function %s(args?: any): any;", desc, name)

	return Pack{
		Name:        name,
		Description: description,
		Prompt:      promptLine,
		HelpEntries: map[string]string{
			name: fmt.Sprintf("%s(args) — %s", name, description),
		},
		Register: func(rt *goja.Runtime, s *Sandbox) {
			// Loadable via require(name).
			s.AddSkillCode(name, code)

			// Define the wrapped user function once.
			innerName := "_skill_" + name
			wrapped := fmt.Sprintf("function %s(args) {\n%s\n}", innerName, code)
			if _, err := goja.Compile(name, wrapped, false); err != nil {
				s.logs = append(s.logs, LogEntry{Message: fmt.Sprintf("skill %s: %s", name, err)})
				return
			}
			if _, err := rt.RunString(wrapped); err != nil {
				s.logs = append(s.logs, LogEntry{Message: fmt.Sprintf("skill %s: registration failed: %s", name, err)})
				return
			}

			// Public binding — emits the skill_call event around the
			// underlying user function.
			skillName := name
			_ = rt.Set(name, func(call goja.FunctionCall) goja.Value {
				s.Emit(Event{Kind: EventSkillCall, Summary: fmt.Sprintf("skill: %s", skillName)})
				fn, ok := goja.AssertFunction(rt.Get(innerName))
				if !ok {
					panic(rt.NewGoError(fmt.Errorf("skill %s: not a function", skillName)))
				}
				var arg = goja.Undefined()
				if len(call.Arguments) > 0 {
					arg = call.Arguments[0]
				}
				result, err := fn(goja.Undefined(), arg)
				if err != nil {
					s.Emit(Event{Kind: EventError, Summary: fmt.Sprintf("skill %s error", skillName), Detail: err.Error()})
					panic(rt.NewGoError(err))
				}
				return result
			})
		},
	}
}
