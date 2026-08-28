module github.com/mind-vm/agentloop/browser/chrome

go 1.25.0

// chromedp is held at v0.14.2, the last release whose own go directive
// is below 1.26, and cdproto at the revision it pairs with. A newer
// chromedp raises this module's floor to 1.26, and go.work must be at
// least the highest floor among its modules — which would put every
// `go` command run from the repo root, CI's included, on a toolchain
// the root module does not otherwise need.
//
// That bounds the cost; it does not remove it. cdproto imports
// go-json-experiment/json, which declares 1.26 itself, so compiling
// *this* module still wants a 1.26 toolchain. The difference is that
// the requirement now stops at this module's edge: `./...` from the
// root does not descend into a nested module, so the root build and CI
// stay on 1.25. Take a newer chromedp when the root module's own floor
// moves past 1.26.
//
// agentloop is a test-only dependency: chrome.Chrome is built from
// chromedp alone, and the import exists so chrome_test.go can assert
// that it still satisfies browser.Driver. Local builds resolve it
// through the repo's go.work; the version below has to name a release
// that actually contains the browser package before this module can be
// published on its own — and `go mod tidy`, which cannot resolve that
// import until then, has to run once it can.
require (
	github.com/chromedp/cdproto v0.0.0-20250724212937-08a3db8b4327
	github.com/chromedp/chromedp v0.14.2
	github.com/mind-vm/agentloop v0.3.0
)

require (
	github.com/chromedp/sysutil v1.1.0 // indirect
	github.com/go-json-experiment/json v0.0.0-20260623181947-01eb4420fa68 // indirect
	github.com/gobwas/httphead v0.1.0 // indirect
	github.com/gobwas/pool v0.2.1 // indirect
	github.com/gobwas/ws v1.4.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
)
