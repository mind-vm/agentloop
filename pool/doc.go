// Package pool provides SandboxPool, an agentloop.SandboxBuilder that
// reuses one long-lived *sandbox.Sandbox per session across every Run,
// instead of paying goja.New() + pack-Register cost (which for some
// packs includes compiling JS module wrappers — e.g. require('http'),
// require('secret')) on every single message.
//
// # The stale-context problem this solves
//
// A Capability's Build closes over BuildContext.Ctx once (see
// agentloop.Capability's doc comment) — DefaultCapabilities' ai
// capability and every ext pack do this. That ctx is the *per-Run*
// context Loop.Run received, typically derived from a request (an HTTP
// handler's context, say) and cancelled once that Run returns. A
// naively cached Sandbox — built once, its packs' closures holding the
// first Run's ctx forever — would use an already-cancelled context for
// every fetch()/ai()/secret()/etc. call on every subsequent Run.
//
// SandboxPool solves this by giving the delegate builder a swappable
// context.Context (swapCtx, in context.go) in place of the real one: a
// value that implements context.Context but forwards every call to
// whichever context was most recently swapped in. Every Build call —
// cache hit or miss — swaps in the current Run's ctx before returning,
// so a pack that captured the swappable value at session-creation time
// still observes the *current* Run's cancellation and deadline.
//
// This only fixes Ctx. BuildContext has no other field that legitimately
// varies per Run for one session: Session.MessageID is documented as
// set once at session creation, and Scope is the tenant boundary, not
// expected to change mid-session. A capability that somehow captures
// per-Run state some other way won't be covered by this fix.
//
// # What doesn't need fixing
//
// sb.SetOnEvent and sb.SetPolicy are plain setters on the returned
// Sandbox, called fresh on every Run by SandboxPool.Build (SetOnEvent)
// and by Loop.Run itself (SetPolicy) — both already rebind correctly on
// a reused sandbox with no extra work.
package pool
