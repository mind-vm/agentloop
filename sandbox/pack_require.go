package sandbox

import (
	"context"
	"fmt"

	"github.com/dop251/goja"
)

// RequirePack registers the require(name) primitive for CommonJS-
// style module loading inside the sandbox. Modules become loadable
// once a Pack calls Sandbox.AddSkillCode(name, body).
//
// Each require(name) runs a PolicyChecker.Check with toolName=name
// so an application can gate individual modules without touching sandbox
// internals. Cached results are returned without re-checking: the
// policy decision on the initial load is the load decision for the
// rest of the session.
//
// The ctx parameter is the per-session context the require() call
// shares with PolicyChecker — the loop's context, threaded through so
// a policy check can honour the run's cancellation.
func RequirePack(ctx context.Context) Pack {
	return Pack{
		Name:        "require",
		Description: "CommonJS module loading",
		Prompt: `/** CommonJS-style module loader. Loadable: every name declared as a module in this API (e.g. 'http', 'markdown') plus any skill documented in the context above — e.g. const api = require('my-api'). Results are cached per session. */
declare function require(name: string): any;`,
		HelpEntries: map[string]string{
			"require": `require(name) — Load a JavaScript module by name. Returns the module's exports.
  The module code runs as CommonJS: use module.exports or exports.x to export values.
  Results are cached per session.
  Example:
    var utils = require("string-utils")
    var result = utils.capitalize("hello")`,
		},
		Register: func(rt *goja.Runtime, s *Sandbox) {
			_ = rt.Set("require", func(call goja.FunctionCall) goja.Value {
				if len(call.Arguments) == 0 || call.Argument(0).String() == "" {
					panic(rt.NewGoError(fmt.Errorf("require: module name is required")))
				}
				name := call.Argument(0).String()

				if cached, ok := s.RequireCacheGet(name); ok {
					return cached
				}

				// Gate by the module name itself so an application can lock
				// down individual skills via policy.
				s.CheckPolicyOrPanic(rt, ctx, name, nil)

				code, ok := s.SkillCode(name)
				if !ok {
					panic(rt.NewGoError(fmt.Errorf("require: module %q not found", name)))
				}

				// Wrap as CommonJS. The IIFE captures module.exports
				// so the user's body can use either `module.exports`
				// or the shorthand `exports.x` and either lands in
				// the returned value.
				wrapped := fmt.Sprintf(
					"(function() { var module = {exports: {}}; var exports = module.exports;\n%s\n; return module.exports; })()",
					code,
				)
				val, err := rt.RunString(wrapped)
				if err != nil {
					panic(rt.NewGoError(fmt.Errorf("require(%s): %w", name, err)))
				}
				s.RequireCacheSet(name, val)
				return val
			})
		},
	}
}
