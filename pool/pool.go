package pool

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mind-vm/agentloop"
	"github.com/mind-vm/agentloop/sandbox"
)

// Options configures a SandboxPool. The zero value is a usable default.
type Options struct {
	// IdleTimeout is how long a session's cached sandbox may sit unused
	// before the background reaper evicts it (running its real cleanup
	// func). Zero uses a 30-minute default.
	IdleTimeout time.Duration

	// ReapInterval is how often the background reaper sweeps for idle
	// entries. Zero uses a 1-minute default.
	ReapInterval time.Duration
}

// SandboxPool is an agentloop.SandboxBuilder that reuses one long-lived
// *sandbox.Sandbox per session, built once via a delegate SandboxBuilder
// and returned again — with its context swapped to the current Run's,
// see the package doc comment — on every later Build call for the same
// session. A background goroutine evicts sandboxes idle longer than
// Options.IdleTimeout.
//
// # Concurrent Runs for one session serialize
//
// A goja.Runtime is not safe for concurrent use from multiple
// goroutines — this is a goja-level rule, not one SandboxPool invented.
// A fresh-per-Run SandboxBuilder never had to think about it, because
// two concurrent Runs for the same session got two separate Runtimes.
// SandboxPool's whole point is to hand out the *same* Runtime across
// Runs, which means it also has to guarantee only one Run is ever
// actually using it at a time.
//
// It does this with a per-session mutex, not a comment: Build blocks
// until any Run currently using that session's sandbox has returned its
// cleanup func, and the cleanup func Build returns is that mutex's
// Unlock. Loop.Run already calls `defer cleanup()` right after Build
// succeeds (see run.go), so this requires no change on the caller's
// side — but it does mean a second Run for a session already in flight
// now *waits* for the first to finish, rather than running in parallel
// the way two fresh sandboxes would have. For a typical single-threaded
// chat session (no second message before the first one's response) this
// is invisible; an application that intentionally runs concurrent Runs
// for one session will see them serialize.
type SandboxPool struct {
	delegate     agentloop.SandboxBuilder
	idleTimeout  time.Duration
	reapInterval time.Duration

	mu       sync.Mutex
	entries  map[string]*entry
	stop     chan struct{}
	stopOnce sync.Once
}

type entry struct {
	// inUse serializes Runs against this entry's sandbox — see
	// SandboxPool's doc comment. Held from Build's return until the
	// caller's deferred cleanup() releases it; also acquired before
	// running the entry's real cleanup, so eviction can never tear down
	// a sandbox a Run is still using.
	inUse sync.Mutex

	sb        *sandbox.Sandbox
	ctxHolder *swapCtx
	cleanup   func()
	lastUsed  atomic.Int64 // unix nano
}

func (e *entry) touch() { e.lastUsed.Store(time.Now().UnixNano()) }

func (e *entry) idleFor(now time.Time) time.Duration {
	return now.Sub(time.Unix(0, e.lastUsed.Load()))
}

// checkout blocks until the entry is free, marks it in-use for ctx and
// onEvent, and returns it plus the func that releases it — the func
// Build hands back to the caller as the cleanup to defer.
func (e *entry) checkout(ctx context.Context, onEvent sandbox.OnEvent) (*sandbox.Sandbox, func()) {
	e.inUse.Lock()
	e.touch()
	e.ctxHolder.swap(ctx)
	e.sb.SetOnEvent(onEvent)
	return e.sb, e.inUse.Unlock
}

// evict blocks until the entry is free (so it never runs out from under
// an in-flight Run — see the SandboxPool doc comment), then runs its
// real cleanup.
func (e *entry) evict() {
	e.inUse.Lock()
	defer e.inUse.Unlock()
	e.cleanup()
}

// New wraps delegate in a SandboxPool and starts its background reaper.
// Call Close to stop the reaper and clean up every cached sandbox.
func New(delegate agentloop.SandboxBuilder, opts Options) *SandboxPool {
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = 30 * time.Minute
	}
	if opts.ReapInterval <= 0 {
		opts.ReapInterval = 1 * time.Minute
	}
	p := &SandboxPool{
		delegate:     delegate,
		idleTimeout:  opts.IdleTimeout,
		reapInterval: opts.ReapInterval,
		entries:      make(map[string]*entry),
		stop:         make(chan struct{}),
	}
	go p.reap()
	return p
}

// Build implements agentloop.SandboxBuilder. A cache hit checks out the
// existing sandbox for sess.ID — swapping in ctx and rebinding onEvent,
// blocking first if another Run for this session is still in flight —
// and returns it with a cleanup that checks it back in. A cache miss
// builds via the delegate, with the delegate seeing a swappable context
// in place of ctx, and caches the result under sess.ID.
func (p *SandboxPool) Build(ctx context.Context, sess agentloop.Session, scope agentloop.Scope, onEvent sandbox.OnEvent) (*sandbox.Sandbox, func(), error) {
	if e := p.lookup(sess.ID); e != nil {
		sb, release := e.checkout(ctx, onEvent)
		return sb, release, nil
	}

	holder := newSwapCtx(ctx)
	sb, cleanup, err := p.delegate.Build(holder, sess, scope, onEvent)
	if err != nil {
		return nil, nil, err
	}
	ne := &entry{sb: sb, ctxHolder: holder, cleanup: cleanup}
	ne.inUse.Lock() // ours until we return its release below
	ne.touch()

	if existing := p.store(sess.ID, ne); existing != nil {
		// Lost a race with a concurrent Build for the same session:
		// another goroutine's entry landed first. Discard the sandbox we
		// just built rather than orphaning two live sandboxes under one
		// session ID, and check out the winner instead.
		cleanup()
		sb, release := existing.checkout(ctx, onEvent)
		return sb, release, nil
	}
	return ne.sb, ne.inUse.Unlock, nil
}

func (p *SandboxPool) lookup(sessionID string) *entry {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.entries[sessionID]
}

// store inserts ne under sessionID if nothing is there yet, and returns
// the entry that ended up in the map when it wasn't ne (i.e. a
// concurrent Build won the race).
func (p *SandboxPool) store(sessionID string, ne *entry) *entry {
	p.mu.Lock()
	defer p.mu.Unlock()
	if existing, ok := p.entries[sessionID]; ok {
		return existing
	}
	p.entries[sessionID] = ne
	return nil
}

// Evict removes and cleans up one session's cached sandbox, if any —
// for an application-level session end (logout, explicit close) rather
// than idle eviction. Blocks until any Run currently using it finishes.
func (p *SandboxPool) Evict(sessionID string) {
	p.mu.Lock()
	e, ok := p.entries[sessionID]
	if ok {
		delete(p.entries, sessionID)
	}
	p.mu.Unlock()
	if ok {
		e.evict()
	}
}

// EvictIdle runs one eviction sweep immediately, removing and cleaning
// up every session idle longer than Options.IdleTimeout. The background
// reaper calls this on ReapInterval; exported so callers can drive
// eviction on their own schedule (or deterministically in tests)
// instead of waiting on the timer.
//
// A session whose Run is taking longer than IdleTimeout looks idle by
// this sweep's clock (lastUsed is stamped at checkout, not continuously)
// and gets queued for eviction — but evict() blocks on the entry's lock,
// so the sweep waits out that Run rather than tearing down its sandbox.
// The session simply gets a fresh sandbox on its next Build.
func (p *SandboxPool) EvictIdle() {
	now := time.Now()
	var toEvict []*entry

	p.mu.Lock()
	for id, e := range p.entries {
		if e.idleFor(now) > p.idleTimeout {
			toEvict = append(toEvict, e)
			delete(p.entries, id)
		}
	}
	p.mu.Unlock()

	for _, e := range toEvict {
		e.evict()
	}
}

// Len returns the number of sessions currently cached.
func (p *SandboxPool) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.entries)
}

// Close stops the background reaper and cleans up every cached
// sandbox, waiting out any Run still in flight for each. The pool
// remains usable afterward — Build simply starts caching fresh entries
// again, same as a newly-constructed pool — but a stopped reaper never
// restarts, so idle entries added after Close only get cleaned up via
// Evict or a later Close.
func (p *SandboxPool) Close() {
	p.stopOnce.Do(func() { close(p.stop) })

	p.mu.Lock()
	entries := p.entries
	p.entries = make(map[string]*entry)
	p.mu.Unlock()

	for _, e := range entries {
		e.evict()
	}
}

func (p *SandboxPool) reap() {
	ticker := time.NewTicker(p.reapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-p.stop:
			return
		case <-ticker.C:
			p.EvictIdle()
		}
	}
}

var _ agentloop.SandboxBuilder = (*SandboxPool)(nil)
