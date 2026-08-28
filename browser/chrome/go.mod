module github.com/mind-vm/agentloop/browser/chrome

go 1.26

// agentloop is a test-only dependency: chrome.Chrome is built from
// chromedp alone, and the import exists so chrome_test.go can assert
// that it still satisfies browser.Driver. Local builds resolve it
// through the repo's go.work; the version below has to name a release
// that actually contains the browser package before this module can be
// published on its own.
require (
	github.com/chromedp/cdproto v0.0.0-20260714215040-dc233986426f
	github.com/chromedp/chromedp v0.16.0
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
