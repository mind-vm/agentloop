package sandbox

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/dop251/goja"
)

// PolicyChecker gates tool calls. Implementations typically close
// over the (workspace / session) tuple so the sandbox only has to
// pass tool-level inputs.
//
// A nil PolicyChecker on the bare Sandbox fails open — useful for
// direct sandbox tests. agentloop's Loop installs DefaultPolicy when
// Config.Policy is nil.
type PolicyChecker interface {
	// Check is called by every pack with side effects before the
	// effect runs. toolName is the primitive's name (e.g. "fetch",
	// "sendEmail"); args carries the call's user-visible inputs.
	//
	// Implementations return PolicyCheckResult{Allowed: false, Reason: ...}
	// to deny, or PolicyCheckResult{Allowed: true, ConfigJSON: ...}
	// to allow optionally with tool-specific config (e.g. fetch's
	// allowlist comes back here).
	Check(ctx context.Context, toolName string, args map[string]any) (PolicyCheckResult, error)
}

// PolicyCheckResult is the sandbox-side view of a policy decision.
// When Allowed is false, Reason carries the human-readable
// explanation that's shown to the agent. When Allowed is true,
// ConfigJSON may carry tool-specific configuration the pack should
// consult (e.g. fetch's URL allowlist).
type PolicyCheckResult struct {
	Allowed    bool
	ConfigJSON json.RawMessage
	Reason     string
}

// SetPolicy installs a PolicyChecker on the sandbox. Nil clears it
// (fails open).
func (s *Sandbox) SetPolicy(p PolicyChecker) {
	s.policy = p
}

// CheckPolicy runs the installed checker and returns its result.
// A nil checker fails open with Allowed=true and no config.
func (s *Sandbox) CheckPolicy(ctx context.Context, toolName string, args map[string]any) (PolicyCheckResult, error) {
	if s.policy == nil {
		return PolicyCheckResult{Allowed: true}, nil
	}
	return s.policy.Check(ctx, toolName, args)
}

// CheckPolicyOrPanic runs CheckPolicy and panics with a Goja runtime
// error when the call is denied or when the check itself errors —
// matching the panic(rt.NewGoError(...)) pattern packs already use
// for their other invariant violations. Returns the
// PolicyCheckResult on allow so callers can consume ConfigJSON for
// tool-specific sub-checks (fetch's URL allowlist is the canonical
// example).
func (s *Sandbox) CheckPolicyOrPanic(rt *goja.Runtime, ctx context.Context, toolName string, args map[string]any) PolicyCheckResult {
	res, err := s.CheckPolicy(ctx, toolName, args)
	if err != nil {
		panic(rt.NewGoError(fmt.Errorf("%s: policy check failed: %w", toolName, err)))
	}
	if !res.Allowed {
		reason := res.Reason
		if reason == "" {
			reason = "denied by policy"
		}
		s.Emit(Event{Kind: EventError, Summary: fmt.Sprintf("%s blocked by policy", toolName), Detail: reason})
		panic(rt.NewGoError(fmt.Errorf("%s: %s", toolName, reason)))
	}
	return res
}

// URLAllowlistPolicy is a stateless, in-memory checker that allows
// fetch() / require('http') / browser navigation to reach exactly the
// URL prefixes in AllowedPrefixes. Other tool names always allow (the
// canonical use is to compose it under MultiPolicy alongside a stricter
// product-side checker).
//
// The decision is encoded so the fetch pack can read the allowlist
// from ConfigJSON instead of re-querying somewhere. Field name
// matches what the existing fetch pack expects: `whitelist`.
type URLAllowlistPolicy struct {
	AllowedPrefixes []string
}

// Check implements PolicyChecker. fetch, the http module, and browser
// navigation honour the allowlist; everything else passes through.
func (p URLAllowlistPolicy) Check(_ context.Context, toolName string, args map[string]any) (PolicyCheckResult, error) {
	// For non-URL tools, this checker is transparent.
	if toolName != "fetch" && toolName != "http" && toolName != "_http" && toolName != "browser" {
		return PolicyCheckResult{Allowed: true}, nil
	}

	rawURL, _ := args["url"].(string)
	if rawURL == "" {
		// Surface the allowlist for the pack to consult per-request.
		cfg, _ := json.Marshal(map[string]any{"whitelist": p.AllowedPrefixes})
		return PolicyCheckResult{Allowed: true, ConfigJSON: cfg}, nil
	}

	for _, prefix := range p.AllowedPrefixes {
		if strings.HasPrefix(rawURL, prefix) {
			return PolicyCheckResult{Allowed: true}, nil
		}
	}
	return PolicyCheckResult{
		Allowed: false,
		Reason:  fmt.Sprintf("URL %q is not in the allowlist", rawURL),
	}, nil
}

// MultiPolicy chains multiple checkers; the first denial wins. Each
// checker's ConfigJSON is preserved through the chain when every
// checker allows — later allow-with-config calls overwrite earlier
// ones, so put the canonical config provider (e.g. URLAllowlistPolicy)
// last in the chain.
type MultiPolicy struct {
	Checkers []PolicyChecker
}

// Check implements PolicyChecker. Returns the first denial or the
// most-recent allow-with-config.
func (m MultiPolicy) Check(ctx context.Context, toolName string, args map[string]any) (PolicyCheckResult, error) {
	final := PolicyCheckResult{Allowed: true}
	for _, c := range m.Checkers {
		res, err := c.Check(ctx, toolName, args)
		if err != nil {
			return PolicyCheckResult{}, err
		}
		if !res.Allowed {
			return res, nil
		}
		if len(res.ConfigJSON) > 0 {
			final = res
		}
	}
	return final, nil
}
