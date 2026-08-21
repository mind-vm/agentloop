package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"slices"
	"strings"
)

// AllowAll is the explicit fail-open PolicyChecker. Every check passes
// with no config. Use it in tests and in the rare deployment that
// consciously wants ungated side effects — passing it is greppable and
// reviewable, unlike a nil-means-open default.
var AllowAll PolicyChecker = allowAllPolicy{}

type allowAllPolicy struct{}

func (allowAllPolicy) Check(context.Context, string, map[string]any) (PolicyCheckResult, error) {
	return PolicyCheckResult{Allowed: true}, nil
}

// sideEffectTools are the primitives DefaultPolicy denies unless
// explicitly granted: they move data out of the run (sendEmail) or pull
// sensitive material into it (secret). fetch/http are handled by the
// URL rules instead.
var sideEffectTools = map[string]bool{
	"sendEmail": true,
	"secret":    true,
}

// DefaultPolicy is the conservative PolicyChecker agentloop's Loop
// installs when Config.Policy is nil: side-effecting primitives are
// denied unless explicitly granted, and fetch/http may not reach private
// address ranges. The zero value is the safe default; a caller states
// its grants:
//
//	sandbox.DefaultPolicy{
//	    AllowTools:       []string{"sendEmail"},
//	    URLAllowPrefixes: []string{"https://api.example.com/"},
//	}
//
// A caller that truly wants fail-open passes AllowAll.
type DefaultPolicy struct {
	// AllowTools grants side-effecting tool names ("sendEmail",
	// "secret"). Empty means all of them are denied.
	AllowTools []string

	// URLAllowPrefixes is the outbound allowlist for fetch/http,
	// matched by prefix (same semantics as URLAllowlistPolicy). Empty
	// means public URLs are allowed (the fetch pack's per-domain user
	// approval still applies); non-empty means only matching URLs are.
	// An explicit prefix also overrides the private-network denial —
	// listing an internal URL is a conscious decision.
	URLAllowPrefixes []string

	// AllowPrivateNetworks disables the deny-by-default for loopback,
	// RFC-1918/ULA, link-local, and *.localhost/*.local/*.internal
	// targets. Leave false outside local development.
	AllowPrivateNetworks bool
}

// Check implements PolicyChecker.
func (p DefaultPolicy) Check(_ context.Context, toolName string, args map[string]any) (PolicyCheckResult, error) {
	switch toolName {
	case "fetch", "http", "_http":
		return p.checkURL(args)
	}
	if sideEffectTools[toolName] && !slices.Contains(p.AllowTools, toolName) {
		return PolicyCheckResult{
			Allowed: false,
			Reason:  fmt.Sprintf("%s is denied by the default sandbox policy — grant it via DefaultPolicy.AllowTools or install a custom PolicyChecker", toolName),
		}, nil
	}
	return PolicyCheckResult{Allowed: true}, nil
}

func (p DefaultPolicy) checkURL(args map[string]any) (PolicyCheckResult, error) {
	rawURL, _ := args["url"].(string)
	if rawURL == "" {
		// Config probe (fetchWhitelist(), pack warm-up): surface the
		// allowlist for the pack to consult per-request.
		cfg, _ := json.Marshal(map[string]any{"whitelist": p.URLAllowPrefixes})
		return PolicyCheckResult{Allowed: true, ConfigJSON: cfg}, nil
	}
	for _, prefix := range p.URLAllowPrefixes {
		if strings.HasPrefix(rawURL, prefix) {
			return PolicyCheckResult{Allowed: true}, nil
		}
	}
	if !p.AllowPrivateNetworks && isPrivateHostURL(rawURL) {
		return PolicyCheckResult{
			Allowed: false,
			Reason:  fmt.Sprintf("URL %q targets a private or internal address, denied by the default sandbox policy", rawURL),
		}, nil
	}
	if len(p.URLAllowPrefixes) > 0 {
		return PolicyCheckResult{
			Allowed: false,
			Reason:  fmt.Sprintf("URL %q is not in the allowlist", rawURL),
		}, nil
	}
	return PolicyCheckResult{Allowed: true}, nil
}

// isPrivateHostURL reports whether rawURL's host is a non-public
// target: loopback, unspecified, RFC-1918 / IPv6 ULA, link-local, or a
// conventional internal hostname. The check is syntactic (IP literals
// and hostname suffixes) — it does not resolve DNS, so a public name
// pointing at a private address (DNS rebinding) is out of scope here
// and belongs to the HTTP client layer. An unparseable URL counts as
// private: conservative default.
func isPrivateHostURL(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Hostname() == "" {
		return true
	}
	host := strings.ToLower(u.Hostname())
	if host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback() || ip.IsUnspecified() || ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast()
}
