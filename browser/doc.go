// Package browser gives an agentloop sandbox a `browser` global that
// drives a real browser: navigate, click, type, read text, and — via
// Set-of-Marks — enumerate the page's interactive elements as numbered
// ids the model can act on without ever producing a CSS selector or a
// pixel coordinate.
//
// The package holds no browser dependency of its own. Everything here
// talks to a [Driver], a ten-method seam an implementation satisfies;
// the chromedp-backed one lives in the sibling module
// github.com/mind-vm/agentloop/browser/chrome, which is a separate Go
// module precisely so agentloop's own dependency set stays small for
// the deployments that never open a browser.
//
// Wiring follows the same shape as ext's packs — an application closes
// an agentloop.Capability over its Driver:
//
//	chrome := chrome.New(chrome.Options{})
//	defer chrome.Close()
//
//	cap := agentloop.Capability{
//	    Name:        "browser",
//	    Description: "Drive a real browser",
//	    Build: func(bc agentloop.BuildContext) ([]sandbox.Pack, error) {
//	        return []sandbox.Pack{browser.Pack(bc.Ctx, chrome, nil)}, nil
//	    },
//	}
//
// The Driver is the application's to own and close: Capability.Build
// has no teardown hook, so a per-session browser belongs to a
// SandboxBuilder (whose Build returns a cleanup func), and a shared
// one belongs to the process.
//
// # Set-of-Marks
//
// A vision model can describe a screenshot but cannot name a CSS
// selector, and coordinates it invents are unreliable. Set-of-Marks
// fixes the vocabulary problem: mark() enumerates every visible
// interactive element, draws a numbered box over each, and returns
// {id, tag, role, name, x, y, w, h} for all of them. The model picks an
// id; clickMark(id) / typeMark(id, text) resolve it back to the DOM
// node. That listing is useful on its own — an agent with no vision
// backend at all can navigate a page from the returned metadata, which
// is why mark() does not require one.
//
// Ids are assigned in document order at mark() time and are stable only
// until the next mark(): any DOM change renumbers them. A navigation
// drops them entirely, and the pack enforces that — clickMark after
// goto errors rather than clicking whatever now holds that number.
package browser
