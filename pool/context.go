package pool

import (
	"context"
	"sync"
	"time"
)

// swapCtx is a context.Context whose underlying context can be swapped
// out after construction. SandboxPool gives one to the delegate
// SandboxBuilder in place of the real per-Run ctx, then swaps in each
// Run's actual ctx before returning the (possibly cached) sandbox — see
// the package doc comment for why.
//
// Swaps are only safe between calls, not mid-flight: agentloop's
// execution model is fully synchronous within one Run (no
// async/goroutines survive inside the sandbox — see the root package's
// "Known limitations"), so every pack call that reads ctx does so
// synchronously, within the Run that swapped it in. Nothing observes a
// swap while a call is in progress.
type swapCtx struct {
	mu  sync.RWMutex
	ctx context.Context
}

func newSwapCtx(ctx context.Context) *swapCtx {
	return &swapCtx{ctx: ctx}
}

func (s *swapCtx) swap(ctx context.Context) {
	s.mu.Lock()
	s.ctx = ctx
	s.mu.Unlock()
}

func (s *swapCtx) current() context.Context {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ctx
}

func (s *swapCtx) Deadline() (time.Time, bool) { return s.current().Deadline() }
func (s *swapCtx) Done() <-chan struct{}       { return s.current().Done() }
func (s *swapCtx) Err() error                  { return s.current().Err() }
func (s *swapCtx) Value(key any) any           { return s.current().Value(key) }

var _ context.Context = (*swapCtx)(nil)
