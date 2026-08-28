package chrome_test

import (
	"testing"
	"time"

	"github.com/mind-vm/agentloop/browser"
	"github.com/mind-vm/agentloop/browser/chrome"
)

// Chrome must satisfy the Driver seam. This assertion is the reason the
// module depends on agentloop at all, and it is the whole point: the
// interface and its only real implementation live in different modules,
// so nothing else would catch them drifting apart.
var _ browser.Driver = (*chrome.Chrome)(nil)

// TestNew_Defaults confirms a zero-value Options yields a usable
// configuration, and — more importantly — that constructing a Chrome
// launches nothing. A per-session driver is only cheap if the sessions
// that never browse never pay for a process.
func TestNew_Defaults(t *testing.T) {
	c := chrome.New(chrome.Options{})
	if c == nil {
		t.Fatal("New returned nil")
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.Close()
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Close on an unstarted Chrome blocked — it must not have started one")
	}
}

// TestClose_Idempotent confirms repeated teardown is safe: an
// application that closes a per-session browser in a cleanup func and
// again on shutdown should not panic.
func TestClose_Idempotent(t *testing.T) {
	c := chrome.New(chrome.Options{})
	c.Close()
	c.Close()
}

// TestCallAfterClose confirms a closed browser reports itself instead
// of silently starting a second process.
func TestCallAfterClose(t *testing.T) {
	c := chrome.New(chrome.Options{})
	c.Close()
	if err := c.Navigate(t.Context(), "https://example.com"); err == nil {
		t.Fatal("expected an error after Close")
	}
}
