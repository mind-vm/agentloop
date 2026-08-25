package ext

import (
	"context"
	"fmt"

	"github.com/dop251/goja"

	"github.com/mind-vm/agentloop/sandbox"
)

// secretModuleJS is the JS wrapper installed as the `secret` require()
// module. Both `secret(name)` (bare global) and
// `require('secret').get(name)` forward to the internal `_secret_get`
// global registered by SecretPack.
const secretModuleJS = `
exports.get = function(name) { return _secret_get(name); };
exports.secret = function(name) { return _secret_get(name); };
`

// SecretPack exposes a synchronous `secret(name)` primitive that reads a
// decrypted secret by name. It takes a getter callback rather than a
// concrete secrets service so this package stays free of that
// dependency — the application's Capability closes the getter over its
// own secrets backend and the per-session scope.
//
// Every read is policy-gated with toolName "secret";
// sandbox.DefaultPolicy denies it unless granted via
// DefaultPolicy.AllowTools. ctx is the per-session context the check
// shares with the run.
//
// Lookup failures (unknown name, decrypt error) surface as a thrown JS
// error so a skill can try/catch; they do not crash the run.
func SecretPack(ctx context.Context, get func(name string) (string, error)) sandbox.Pack {
	return sandbox.Pack{
		Name:        "secret",
		Description: "Read project secrets by name — secret(name)",
		Prompt: `// --- Secrets ---
/** Read a decrypted project secret by name. Throws if the secret is not set. */
declare function secret(name: string): string;`,
		HelpEntries: map[string]string{
			"secret": `secret(name) — Read a decrypted project secret by name. Returns string; throws if not set.
  Example: var key = secret("STRIPE_API_KEY");`,
		},
		Register: func(rt *goja.Runtime, sb *sandbox.Sandbox) {
			getFn := func(call goja.FunctionCall) goja.Value {
				if len(call.Arguments) == 0 || call.Argument(0).String() == "" {
					panic(rt.NewGoError(fmt.Errorf("secret: name is required")))
				}
				name := call.Argument(0).String()
				sb.CheckPolicyOrPanic(rt, ctx, "secret", map[string]any{"name": name})
				if get == nil {
					panic(rt.NewGoError(fmt.Errorf("secret(%q): no secrets backend configured", name)))
				}
				val, err := get(name)
				if err != nil {
					panic(rt.NewGoError(fmt.Errorf("secret(%q): %w", name, err)))
				}
				return rt.ToValue(val)
			}

			// Bare global — the primary surface advertised by the prompt.
			_ = rt.Set("secret", getFn)
			// require('secret') alias for module-style skills.
			_ = rt.Set("_secret_get", getFn)
			sb.AddSkillCode("secret", secretModuleJS)
		},
	}
}
