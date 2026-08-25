package pool_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dop251/goja"

	"github.com/mind-vm/agentloop"
	"github.com/mind-vm/agentloop/pool"
	"github.com/mind-vm/agentloop/sandbox"
)

// fakeBuilder is an agentloop.SandboxBuilder that counts Build calls and
// records which session IDs get cleaned up. Its sandbox exposes
// ctxErr(), a JS primitive that reports the *build-time* ctx's current
// Err() — this is what makes the context-swap test meaningful: without
// swapCtx, a reused sandbox would keep reporting the first Run's
// (possibly cancelled) ctx forever.
type fakeBuilder struct {
	mu       sync.Mutex
	builds   int
	cleanups []string
}

func (f *fakeBuilder) Build(ctx context.Context, sess agentloop.Session, _ agentloop.Scope, _ sandbox.OnEvent) (*sandbox.Sandbox, func(), error) {
	f.mu.Lock()
	f.builds++
	f.mu.Unlock()

	sb := sandbox.New(sandbox.Pack{
		Name: "test",
		Register: func(rt *goja.Runtime, _ *sandbox.Sandbox) {
			_ = rt.Set("ctxErr", func(goja.FunctionCall) goja.Value {
				if err := ctx.Err(); err != nil {
					return rt.ToValue(err.Error())
				}
				return rt.ToValue("")
			})
		},
	})

	id := sess.ID
	cleanup := func() {
		f.mu.Lock()
		f.cleanups = append(f.cleanups, id)
		f.mu.Unlock()
	}
	return sb, cleanup, nil
}

func TestSandboxPool_ReusesSandboxAcrossRuns(t *testing.T) {
	fb := &fakeBuilder{}
	p := pool.New(fb, pool.Options{})
	defer p.Close()

	sess := agentloop.Session{ID: "s1"}
	sb1, cleanup1, err := p.Build(context.Background(), sess, agentloop.Scope{}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// Release before the next Build for the same session — a real Run
	// would release via its own deferred cleanup() before any later Run
	// for that session starts.
	cleanup1()

	sb2, cleanup2, err := p.Build(context.Background(), sess, agentloop.Scope{}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer cleanup2()

	if sb1 != sb2 {
		t.Fatalf("expected the same sandbox on the second Build for the same session")
	}
	if fb.builds != 1 {
		t.Fatalf("expected 1 delegate build, got %d", fb.builds)
	}
}

func TestSandboxPool_DifferentSessionsGetDistinctSandboxes(t *testing.T) {
	fb := &fakeBuilder{}
	p := pool.New(fb, pool.Options{})
	defer p.Close()

	sb1, cleanup1, err := p.Build(context.Background(), agentloop.Session{ID: "a"}, agentloop.Scope{}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer cleanup1()
	sb2, cleanup2, err := p.Build(context.Background(), agentloop.Session{ID: "b"}, agentloop.Scope{}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer cleanup2()

	if sb1 == sb2 {
		t.Fatalf("expected distinct sandboxes for distinct sessions")
	}
	if fb.builds != 2 {
		t.Fatalf("expected 2 delegate builds, got %d", fb.builds)
	}
	if p.Len() != 2 {
		t.Fatalf("expected 2 cached entries, got %d", p.Len())
	}
}

// TestSandboxPool_SwapsContextOnReuse is the correctness test for the
// context-swap half of this package: a cached sandbox must observe each
// Run's own ctx, not whichever ctx happened to be live when the sandbox
// was first built.
func TestSandboxPool_SwapsContextOnReuse(t *testing.T) {
	fb := &fakeBuilder{}
	p := pool.New(fb, pool.Options{})
	defer p.Close()

	sess := agentloop.Session{ID: "s1"}

	ctx1, cancel1 := context.WithCancel(context.Background())
	sb1, cleanup1, err := p.Build(ctx1, sess, agentloop.Scope{}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if out, err := sb1.Execute("ctxErr()"); err != nil || out != "" {
		t.Fatalf("ctxErr() before cancellation: out=%q err=%v, want empty", out, err)
	}
	cleanup1()

	// Simulate the first Run's request scope tearing down after the Run
	// returned — exactly what would cancel a request-derived ctx in a
	// real caller.
	cancel1()

	// A second Run for the same session, with its own fresh ctx.
	sb2, cleanup2, err := p.Build(context.Background(), sess, agentloop.Scope{}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer cleanup2()

	if sb1 != sb2 {
		t.Fatalf("expected the cached sandbox to be reused")
	}
	out, err := sb2.Execute("ctxErr()")
	if err != nil {
		t.Fatalf("ctxErr(): %v", err)
	}
	if out != "" {
		t.Fatalf("stale context leaked into the reused sandbox: ctxErr() = %q, want empty (fresh ctx)", out)
	}
}

func TestSandboxPool_EvictIdle(t *testing.T) {
	fb := &fakeBuilder{}
	// A near-zero idle timeout means the entry is already "idle" by the
	// time EvictIdle runs, with no need to sleep for real time to pass.
	p := pool.New(fb, pool.Options{IdleTimeout: time.Nanosecond, ReapInterval: time.Hour})
	defer p.Close()

	sess := agentloop.Session{ID: "s1"}
	sb1, cleanup1, err := p.Build(context.Background(), sess, agentloop.Scope{}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// Must release before EvictIdle: eviction waits out any in-flight
	// Run rather than tearing a live sandbox down underneath it.
	cleanup1()

	// A back-to-back time.Now() pair can tie on some clocks, which with
	// a bare IdleTimeout: time.Nanosecond would make idleFor(now) == 0
	// and nothing would look idle yet. This sleep guarantees real
	// elapsed time clears the threshold.
	time.Sleep(time.Millisecond)
	p.EvictIdle()

	if p.Len() != 0 {
		t.Fatalf("expected the idle entry to be evicted, got %d cached", p.Len())
	}
	fb.mu.Lock()
	cleanups := append([]string(nil), fb.cleanups...)
	fb.mu.Unlock()
	if len(cleanups) != 1 || cleanups[0] != "s1" {
		t.Fatalf("expected cleanup for s1, got %v", cleanups)
	}

	sb2, cleanup2, err := p.Build(context.Background(), sess, agentloop.Scope{}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer cleanup2()
	if sb1 == sb2 {
		t.Fatalf("expected a fresh sandbox after eviction")
	}
	if fb.builds != 2 {
		t.Fatalf("expected 2 delegate builds, got %d", fb.builds)
	}
}

func TestSandboxPool_Evict(t *testing.T) {
	fb := &fakeBuilder{}
	p := pool.New(fb, pool.Options{})
	defer p.Close()

	sess := agentloop.Session{ID: "s1"}
	_, cleanup1, err := p.Build(context.Background(), sess, agentloop.Scope{}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	cleanup1()

	p.Evict("s1")

	if p.Len() != 0 {
		t.Fatalf("expected eviction to remove the entry")
	}
	fb.mu.Lock()
	n := len(fb.cleanups)
	fb.mu.Unlock()
	if n != 1 {
		t.Fatalf("expected 1 cleanup, got %d", n)
	}

	// Evicting an unknown session is a no-op, not an error.
	p.Evict("does-not-exist")
}

func TestSandboxPool_Close(t *testing.T) {
	fb := &fakeBuilder{}
	p := pool.New(fb, pool.Options{})

	_, cleanupA, err := p.Build(context.Background(), agentloop.Session{ID: "a"}, agentloop.Scope{}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	cleanupA()
	_, cleanupB, err := p.Build(context.Background(), agentloop.Session{ID: "b"}, agentloop.Scope{}, nil)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	cleanupB()

	p.Close()

	if p.Len() != 0 {
		t.Fatalf("expected Close to clear all entries")
	}
	fb.mu.Lock()
	n := len(fb.cleanups)
	fb.mu.Unlock()
	if n != 2 {
		t.Fatalf("expected 2 cleanups from Close, got %d", n)
	}
}

// TestSandboxPool_ConcurrentBuildSerializes drives many concurrent Build
// calls for the same session and confirms two things the data-race
// fix is about: every caller ends up with the same sandbox, and no two
// callers are ever checked out at the same time (each holds the
// sandbox only between Build and its own cleanup call, and a second
// Build for the same session blocks until the first is released).
func TestSandboxPool_ConcurrentBuildSerializes(t *testing.T) {
	fb := &fakeBuilder{}
	p := pool.New(fb, pool.Options{})
	defer p.Close()

	const n = 20
	sess := agentloop.Session{ID: "s1"}
	results := make([]*sandbox.Sandbox, n)
	var wg sync.WaitGroup
	var concurrent, maxConcurrent atomic.Int32
	wg.Add(n)
	for i := range n {
		go func(i int) {
			defer wg.Done()
			sb, cleanup, err := p.Build(context.Background(), sess, agentloop.Scope{}, nil)
			if err != nil {
				t.Errorf("Build: %v", err)
				return
			}
			defer cleanup()
			results[i] = sb

			cur := concurrent.Add(1)
			for {
				m := maxConcurrent.Load()
				if cur <= m || maxConcurrent.CompareAndSwap(m, cur) {
					break
				}
			}
			// Hold the checkout briefly so an overlapping Build — were
			// serialization broken — would show up as concurrent > 1.
			time.Sleep(time.Millisecond)
			concurrent.Add(-1)
		}(i)
	}
	wg.Wait()

	if got := maxConcurrent.Load(); got > 1 {
		t.Fatalf("expected Builds for one session to serialize, saw %d concurrent checkouts", got)
	}

	first := results[0]
	for i, sb := range results {
		if sb != first {
			t.Errorf("result %d: got a different sandbox than result 0", i)
		}
	}
	if p.Len() != 1 {
		t.Fatalf("expected 1 cached entry, got %d", p.Len())
	}

	fb.mu.Lock()
	defer fb.mu.Unlock()
	if fb.builds < 1 {
		t.Fatalf("expected at least one delegate build")
	}
	if len(fb.cleanups) != fb.builds-1 {
		t.Errorf("expected builds-1 cleanups for lost races, got builds=%d cleanups=%d", fb.builds, len(fb.cleanups))
	}
}
