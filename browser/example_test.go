package browser_test

import (
	"context"

	"github.com/mind-vm/agentloop"
	"github.com/mind-vm/agentloop/browser"
	"github.com/mind-vm/agentloop/sandbox"
)

// ExamplePack shows the wiring an application supplies: a Driver it
// owns and closes, closed over by a Capability. Nothing here needs a
// real browser — swap the fake for chrome.New(chrome.Options{}) from
// the browser/chrome module and the same Capability drives Chrome.
func ExamplePack() {
	driver := &fakeDriver{} // in production: chrome.New(chrome.Options{})

	cap := agentloop.Capability{
		Name:        "browser",
		Description: "Drive a real browser",
		Build: func(bc agentloop.BuildContext) ([]sandbox.Pack, error) {
			// bc.Ctx is the per-run context: it bounds every browser
			// call without outliving, or ending, the browser itself.
			return []sandbox.Pack{browser.Pack(bc.Ctx, driver, nil)}, nil
		},
	}

	builder := &agentloop.DefaultSandboxBuilder{
		Capabilities: append(agentloop.DefaultCapabilities(nil, ""), cap),
	}
	_ = builder

	// What the model then writes, turn by turn:
	s := sandbox.New(browser.Pack(context.Background(), driver, nil))
	s.SetPolicy(sandbox.DefaultPolicy{URLAllowPrefixes: []string{"https://example.com/"}})
	_, _ = s.Execute(`
		browser.goto("https://example.com/search");
		var field = browser.mark().find(m => m.role === "textbox" || m.tag === "input");
		browser.typeMark(field.id, "socks");
	`)
}
