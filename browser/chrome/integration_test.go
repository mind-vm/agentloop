package chrome_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"

	"github.com/mind-vm/agentloop/browser"
	"github.com/mind-vm/agentloop/browser/chrome"
	"github.com/mind-vm/agentloop/sandbox"
)

// findChrome returns a Chrome binary to test against, or "" when the
// machine has none. The integration test skips in that case rather than
// failing: a browser is not something CI should be assumed to have.
func findChrome() string {
	if p := os.Getenv("CHROME_PATH"); p != "" {
		return p
	}
	if runtime.GOOS == "darwin" {
		const mac = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"
		if _, err := os.Stat(mac); err == nil {
			return mac
		}
	}
	for _, name := range []string{"google-chrome", "chromium", "chromium-browser", "chrome"} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	return ""
}

const testPage = `<!doctype html>
<html><body>
  <h1 id="title">Shop</h1>
  <input id="q" placeholder="Search products">
  <button id="go" onclick="document.getElementById('out').textContent =
      'searched: ' + document.getElementById('q').value">Go</button>
  <a href="/other">Other page</a>
  <div id="out"></div>
</body></html>`

// TestIntegration_MarkTypeClick drives a real Chrome through the whole
// Set-of-Marks path: enumerate the page's controls, pick two of them out
// of the returned listing by name and tag, type into one and click the
// other, then read the result the page produced. Every layer is real
// here — the injected JS, CDP, the policy gate — which is what the
// fake-driver tests cannot cover.
func TestIntegration_MarkTypeClick(t *testing.T) {
	path := findChrome()
	if path == "" {
		t.Skip("no Chrome binary found; set CHROME_PATH to run this test")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/html")
		_, _ = w.Write([]byte(testPage))
	}))
	defer srv.Close()

	c := chrome.New(chrome.Options{ExecPath: path})
	defer c.Close()

	s := sandbox.New(browser.Pack(context.Background(), c, nil))
	// httptest binds to loopback, so this is the one case where
	// reaching a private address is the point of the test.
	s.SetPolicy(sandbox.DefaultPolicy{AllowPrivateNetworks: true})

	out, err := s.Execute(`
		browser.goto("` + srv.URL + `");
		var marks = browser.mark();
		log("marks: " + JSON.stringify(marks));
		var field = marks.find(function (m) { return m.tag === "input"; });
		var button = marks.find(function (m) { return m.name === "Go"; });
		browser.typeMark(field.id, "socks");
		browser.clickMark(button.id);
		browser.waitVisible("#out");
		field.name + "|" + browser.text("#out") + "|" + browser.value("#q");
	`)
	if err != nil {
		t.Fatalf("script: %v", err)
	}

	want := "Search products|searched: socks|socks"
	if out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
}

// TestIntegration_MarkListsWhatIsVisible confirms the marks describe the
// real page: every interactive control is enumerated, ids run in
// document order, and the accessible names are the ones a model would
// have to reason about.
func TestIntegration_MarkListsWhatIsVisible(t *testing.T) {
	path := findChrome()
	if path == "" {
		t.Skip("no Chrome binary found; set CHROME_PATH to run this test")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("content-type", "text/html")
		_, _ = w.Write([]byte(testPage))
	}))
	defer srv.Close()

	c := chrome.New(chrome.Options{ExecPath: path})
	defer c.Close()

	s := sandbox.New(browser.Pack(context.Background(), c, nil))
	s.SetPolicy(sandbox.DefaultPolicy{AllowPrivateNetworks: true})

	out, err := s.Execute(`
		browser.goto("` + srv.URL + `");
		browser.mark().map(function (m) { return m.id + ":" + m.tag + ":" + m.name; }).join(",");
	`)
	if err != nil {
		t.Fatalf("script: %v", err)
	}
	for _, want := range []string{"1:input:Search products", "2:button:Go", "3:a:Other page"} {
		if !strings.Contains(out, want) {
			t.Errorf("marks %q missing %q", out, want)
		}
	}
}
