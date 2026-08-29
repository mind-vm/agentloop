module github.com/mind-vm/agentloop/browser/chrome

go 1.26

require (
	github.com/chromedp/cdproto v0.0.0-20260804232424-e85f50dbfd32
	github.com/chromedp/chromedp v0.16.0
	github.com/mind-vm/agentloop v0.4.0
)

require (
	github.com/JohannesKaufmann/dom v0.3.1 // indirect
	github.com/JohannesKaufmann/html-to-markdown/v2 v2.5.2 // indirect
	github.com/chromedp/sysutil v1.1.0 // indirect
	github.com/dlclark/regexp2/v2 v2.5.2 // indirect
	github.com/dop251/goja v0.0.0-20260820211235-95a30dcd3fa5 // indirect
	github.com/go-json-experiment/json v0.0.0-20260623181947-01eb4420fa68 // indirect
	github.com/go-sourcemap/sourcemap v2.1.3+incompatible // indirect
	github.com/gobwas/httphead v0.1.0 // indirect
	github.com/gobwas/pool v0.2.1 // indirect
	github.com/gobwas/ws v1.4.0 // indirect
	github.com/google/pprof v0.0.0-20260802141513-ef3492d7dac3 // indirect
	github.com/yuin/goldmark v1.8.5 // indirect
	golang.org/x/net v0.55.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
	golang.org/x/text v0.37.0 // indirect
)

// A replace, not a workspace. go.work would pull this module's
// requirements into the build list of every `go` command run from the
// repo root — CI included, where GOTOOLCHAIN=local means a dependency
// that declares a newer Go than the root module is a hard failure
// rather than a toolchain download. A replace keeps that entirely
// inside this directory. Consumers ignore it and resolve the require
// above, which has to name a release that actually contains the
// browser package before this module can be published.
replace github.com/mind-vm/agentloop => ../..
