package agentloop

import (
	"context"

	"github.com/mind-vm/agentloop/sandbox"
)

// Capability is the seam between the loop and application-supplied
// packs — a named, optionally-gated unit of sandbox functionality a
// SandboxBuilder composes into a session's sandbox.
type Capability struct {
	// Name is the stable identifier a per-session allowlist can
	// reference (see BuildContext.EnabledCapabilities).
	Name string

	// Description is shown in a capability catalog / skill listing.
	Description string

	// AlwaysOn skips the enabled-capabilities allowlist filter — for
	// capabilities nothing should be able to disable without making the
	// runtime unusable (e.g. require()).
	AlwaysOn bool

	// Build runs at session-start with the per-run BuildContext. Empty
	// returns are fine: a capability with a missing optional dependency
	// (no LLM key configured, say) should return (nil, nil) so the
	// session can proceed without it.
	Build func(BuildContext) ([]sandbox.Pack, error)
}

// BuildContext is the per-run bag of dependencies each capability's
// Build receives. Application-specific dependencies (a database handle,
// an accumulator slice, …) that a capability needs should be closed over
// when the Capability is constructed, not threaded through here — see
// DefaultCapabilities for the pattern.
type BuildContext struct {
	// Ctx is the per-run context. Capabilities should honour
	// cancellation — a long-running primitive (fetch, ai()) must abort
	// when the run's deadline fires.
	Ctx context.Context

	// Scope is the tenant boundary. Capabilities that touch
	// application data must filter on it.
	Scope Scope

	// SessionID identifies the session this run extends.
	SessionID string

	// MessageID is the inbound message that started this session, if
	// any (mirrors Session.MessageID — carried here too so a capability
	// doesn't need the Session value itself).
	MessageID string

	// UserID is the invoking user, empty for system-initiated runs.
	UserID string

	// EnabledCapabilities is the session's capability allowlist. nil
	// means "default-all"; a non-nil slice (possibly empty) means "only
	// load capabilities whose Name appears here." AlwaysOn capabilities
	// load regardless.
	EnabledCapabilities *[]string
}
