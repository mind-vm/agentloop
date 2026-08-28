package sandbox

import (
	"context"
	"encoding/json"
	"testing"
)

func check(t *testing.T, p PolicyChecker, tool string, args map[string]any) PolicyCheckResult {
	t.Helper()
	res, err := p.Check(context.Background(), tool, args)
	if err != nil {
		t.Fatalf("Check(%s): %v", tool, err)
	}
	return res
}

// --- side-effecting tools -------------------------------------------

// The zero value is the safe default: everything with a lasting
// consequence is denied until someone says otherwise.
func TestZeroPolicyDeniesEverySideEffect(t *testing.T) {
	var p DefaultPolicy
	for _, tool := range []string{"sendEmail", "secret", "writeFile", "editFile", "exec"} {
		t.Run(tool, func(t *testing.T) {
			res := check(t, p, tool, nil)
			if res.Allowed {
				t.Errorf("%s is allowed by the zero-value policy", tool)
			}
			// The reason reaches the model, and a bare "denied" leaves it
			// with nothing to act on.
			if res.Reason == "" {
				t.Errorf("%s was denied without a reason", tool)
			}
		})
	}
}

// Reads are deliberately absent from the gate: confined to a workspace
// root they cannot reach anything the user did not offer, and gating
// them would mean a prompt per file.
func TestReadsAreNotGated(t *testing.T) {
	var p DefaultPolicy
	for _, tool := range []string{"readFile", "listDir", "glob", "grep", "log", "anythingElse"} {
		if res := check(t, p, tool, nil); !res.Allowed {
			t.Errorf("%s should not be gated, got %q", tool, res.Reason)
		}
	}
}

func TestAllowToolsGrants(t *testing.T) {
	p := DefaultPolicy{AllowTools: []string{"writeFile", "sendEmail"}}

	for _, tool := range []string{"writeFile", "sendEmail"} {
		if res := check(t, p, tool, nil); !res.Allowed {
			t.Errorf("%s should be granted, got %q", tool, res.Reason)
		}
	}
	// Granting one does not grant the rest.
	if res := check(t, p, "secret", nil); res.Allowed {
		t.Error("granting writeFile and sendEmail should not grant secret")
	}
}

// --- exec -----------------------------------------------------------

// AllowCommands is the difference between "may run the build" and "may
// run anything", so it has to grant by name without granting exec.
func TestAllowCommandsGrantsOnlyThoseCommands(t *testing.T) {
	p := DefaultPolicy{AllowCommands: []string{"go", "git"}}

	for _, cmd := range []string{"go", "git"} {
		if res := check(t, p, "exec", map[string]any{"command": cmd}); !res.Allowed {
			t.Errorf("%s should be allowed, got %q", cmd, res.Reason)
		}
	}
	for _, cmd := range []string{"rm", "curl", "sh", ""} {
		if res := check(t, p, "exec", map[string]any{"command": cmd}); res.Allowed {
			t.Errorf("%q should not be allowed by AllowCommands{go,git}", cmd)
		}
	}
	// And it grants exec for those names only — not the tool outright.
	if res := check(t, p, "sendEmail", nil); res.Allowed {
		t.Error("AllowCommands should not grant unrelated tools")
	}
}

// Matching is on the exact basename the caller passes. A policy that
// matched a prefix or a substring would let "go" grant "gofmt", or
// worse, "git" grant anything containing it.
func TestAllowCommandsDoesNotMatchLoosely(t *testing.T) {
	p := DefaultPolicy{AllowCommands: []string{"go"}}
	for _, cmd := range []string{"gofmt", "gopls", "ago", "go2", "GO"} {
		if res := check(t, p, "exec", map[string]any{"command": cmd}); res.Allowed {
			t.Errorf("AllowCommands{go} should not allow %q", cmd)
		}
	}
}

func TestExecStillHonoursAllowTools(t *testing.T) {
	p := DefaultPolicy{AllowTools: []string{"exec"}}
	if res := check(t, p, "exec", map[string]any{"command": "anything"}); !res.Allowed {
		t.Errorf("granting exec outright should allow any command, got %q", res.Reason)
	}
}

// --- URLs -----------------------------------------------------------

func TestPrivateAddressesAreDeniedByDefault(t *testing.T) {
	var p DefaultPolicy
	private := []string{
		"http://localhost/x", "http://LOCALHOST/x", "http://api.localhost/x",
		"http://thing.local/x", "http://svc.internal/x",
		"http://127.0.0.1/x", "http://127.0.0.1:8080/x",
		"http://10.0.0.1/x", "http://192.168.1.1/x", "http://172.16.0.1/x",
		"http://169.254.169.254/latest/meta-data/", // the cloud metadata endpoint
		"http://[::1]/x", "http://[fd00::1]/x",
		"http://0.0.0.0/x",
	}
	for _, u := range private {
		if res := check(t, p, "fetch", map[string]any{"url": u}); res.Allowed {
			t.Errorf("%s should be denied as a private address", u)
		}
	}
}

func TestPublicAddressesAreAllowedWithNoAllowlist(t *testing.T) {
	var p DefaultPolicy
	for _, u := range []string{"https://example.com/x", "https://8.8.8.8/x"} {
		if res := check(t, p, "fetch", map[string]any{"url": u}); !res.Allowed {
			t.Errorf("%s should be allowed, got %q", u, res.Reason)
		}
	}
}

// An unparseable URL counts as private. Anything else would make a
// malformed string the way past the check.
func TestUnparseableURLsAreDenied(t *testing.T) {
	var p DefaultPolicy
	for _, u := range []string{"not a url", "://missing-scheme", "http://"} {
		if res := check(t, p, "fetch", map[string]any{"url": u}); res.Allowed {
			t.Errorf("%q should be denied", u)
		}
	}
}

func TestAllowlistNarrowsToItsPrefixes(t *testing.T) {
	p := DefaultPolicy{URLAllowPrefixes: []string{"https://api.example.com/"}}

	if res := check(t, p, "fetch", map[string]any{"url": "https://api.example.com/v1/things"}); !res.Allowed {
		t.Errorf("a listed prefix should be allowed, got %q", res.Reason)
	}
	// With an allowlist set, a public URL outside it is no longer allowed.
	if res := check(t, p, "fetch", map[string]any{"url": "https://other.example.com/"}); res.Allowed {
		t.Error("a URL outside the allowlist should be denied")
	}
}

// Listing an internal URL is a conscious decision, so it overrides the
// private-network denial rather than being quietly dropped.
func TestAnExplicitPrefixOverridesThePrivateDenial(t *testing.T) {
	p := DefaultPolicy{URLAllowPrefixes: []string{"http://127.0.0.1:9200/"}}
	if res := check(t, p, "fetch", map[string]any{"url": "http://127.0.0.1:9200/_search"}); !res.Allowed {
		t.Errorf("an explicitly listed internal URL should be allowed, got %q", res.Reason)
	}
}

func TestAllowPrivateNetworks(t *testing.T) {
	p := DefaultPolicy{AllowPrivateNetworks: true}
	if res := check(t, p, "fetch", map[string]any{"url": "http://localhost:8080/x"}); !res.Allowed {
		t.Errorf("private addresses should be allowed once opted in, got %q", res.Reason)
	}
}

// A call with no URL is the pack asking for its configuration, and the
// answer carries the allowlist for it to enforce per request.
func TestURLlessCheckReturnsTheAllowlist(t *testing.T) {
	p := DefaultPolicy{URLAllowPrefixes: []string{"https://a.example/", "https://b.example/"}}
	res := check(t, p, "fetch", map[string]any{})

	if !res.Allowed {
		t.Fatalf("a config probe should be allowed, got %q", res.Reason)
	}
	var cfg struct {
		Whitelist []string `json:"whitelist"`
	}
	if err := json.Unmarshal(res.ConfigJSON, &cfg); err != nil {
		t.Fatalf("ConfigJSON: %v (%s)", err, res.ConfigJSON)
	}
	if len(cfg.Whitelist) != 2 || cfg.Whitelist[0] != "https://a.example/" {
		t.Errorf("whitelist = %v", cfg.Whitelist)
	}
}

// http and _http go through the same URL rules as fetch; a second door
// into the same room is how these get bypassed.
func TestHTTPVariantsShareTheURLRules(t *testing.T) {
	var p DefaultPolicy
	for _, tool := range []string{"fetch", "http", "_http"} {
		if res := check(t, p, tool, map[string]any{"url": "http://169.254.169.254/"}); res.Allowed {
			t.Errorf("%s allowed a link-local address", tool)
		}
	}
}

// --- AllowAll -------------------------------------------------------

// The explicit fail-open. It exists so that choosing it is greppable,
// unlike a nil checker meaning the same thing.
func TestAllowAll(t *testing.T) {
	for _, tool := range []string{"sendEmail", "secret", "exec", "writeFile"} {
		if res := check(t, AllowAll, tool, map[string]any{"url": "http://127.0.0.1/"}); !res.Allowed {
			t.Errorf("AllowAll denied %s", tool)
		}
	}
}

// A sandbox with no policy installed fails open, which is what makes
// direct pack tests possible without ceremony.
func TestNilPolicyFailsOpen(t *testing.T) {
	s := New()
	res, err := s.CheckPolicy(context.Background(), "sendEmail", nil)
	if err != nil {
		t.Fatalf("CheckPolicy: %v", err)
	}
	if !res.Allowed {
		t.Error("a sandbox with no policy should fail open")
	}
}
