// Package chrome drives a real Chrome through the Chrome DevTools
// Protocol and implements browser.Driver.
//
// It is a module of its own — github.com/mind-vm/agentloop/browser/chrome
// — because chromedp and cdproto together are larger than the whole of
// agentloop, and a deployment that never opens a browser should not
// carry them. The repo's go.work makes the split invisible while
// developing; consumers opt in with one `go get`.
//
//	chrome := chrome.New(chrome.Options{})
//	defer chrome.Close()
//	pack := browser.Pack(ctx, chrome, nil)
//
// # Why CDP directly
//
// CDP is the protocol Chrome DevTools itself speaks, and every
// automation library — Playwright, Puppeteer, Selenium's CDP mode —
// ends up speaking it. chromedp is a Go client for it, so this is one
// Go dependency rather than a Node helper process driven over a second
// hop.
//
// # One page, one process
//
// A Chrome is one browser process, one page, and one temporary profile,
// which is what keeps sessions from seeing each other's cookies. That
// isolation is the expensive kind: an idle headless Chrome is 4-6 OS
// processes and ~100 MB, a content page 150-300 MB, a heavy SPA 400-700
// MB, so concurrency should be planned against measured memory rather
// than session count, and the process-count pressure (400-600 processes
// for 100 sessions) tends to hit file-descriptor and pid limits before
// RAM.
//
// Where sessions share a trust boundary — the same org, or an
// application's own internal automations — several tabs in one browser
// cost ~30-50 MB each instead, at the price of a shared crash blast
// radius. That variant is not implemented here: it belongs to whatever
// pools browsers, not to a single-page driver.
package chrome
