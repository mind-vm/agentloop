package chrome

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// Default sizing and timeout for a zero-value Options.
const (
	defaultWidth   = 1280
	defaultHeight  = 800
	defaultTimeout = 30 * time.Second
)

// Options configures a Chrome. The zero value is the one most callers
// want: a headless 1280x800 Chrome from PATH, with a 30-second cap on
// any single step.
type Options struct {
	// ExecPath is the Chrome/Chromium binary. Empty lets chromedp find
	// one the usual way (PATH, then the platform's standard install
	// locations).
	ExecPath string

	// Headed runs Chrome with a visible window. Off by default —
	// headless is what a server wants, and a visible window on a
	// developer's machine steals focus on every navigation.
	Headed bool

	// Width and Height size the viewport, in CSS pixels. Zero uses
	// 1280x800. The viewport matters more than usual here: mark() only
	// enumerates what is currently visible, so a short viewport means
	// more scrolling and more re-marking.
	Width, Height int

	// UserDataDir is Chrome's profile directory. Empty gives each
	// Chrome a fresh temporary profile that is deleted on Close —
	// which is what isolates one session's cookies from another's.
	// Point two Chromes at the same directory and they will fight.
	UserDataDir string

	// Timeout caps a single driver call. Zero uses 30s. It is a
	// backstop, not the mechanism for ending a turn: the per-run
	// context each call carries does that, and fires first.
	Timeout time.Duration

	// Flags are extra Chrome command-line flags, applied after the
	// defaults so they win. A false bool omits the flag entirely.
	Flags map[string]any

	// OnFrame, when set, subscribes to Chrome's screencast: it is
	// called with a base64-encoded JPEG each time the viewport changes,
	// which is what a live view of a headless browser is made of.
	// Chrome pushes a frame only when something actually changed and
	// waits for each acknowledgement before sending the next, so an
	// idle page costs nothing and a slow consumer drops frames rather
	// than building a backlog.
	//
	// It is called from Chrome's event goroutine: return quickly, and
	// do not call back into the driver from it.
	OnFrame func(jpegBase64 string)
}

// Chrome drives one page in one Chrome process through the Chrome
// DevTools Protocol, and implements browser.Driver.
//
// The process starts lazily on the first call and lives until Close, so
// constructing one per session is cheap for the sessions that never
// browse. Calls are serialised: a Chrome is one page, and two
// overlapping navigations on one page are a race whatever the caller
// intended.
//
// Callers must Close it. A per-session Chrome belongs to whatever owns
// the session (an agentloop SandboxBuilder returns a cleanup func for
// exactly this); a shared one belongs to the process.
type Chrome struct {
	opts Options

	// mu guards the lazy start and the cancel funcs.
	mu      sync.Mutex
	bctx    context.Context
	cancels []context.CancelFunc
	closed  bool

	// runMu serialises CDP round trips against the single page.
	runMu sync.Mutex
}

// New returns a Chrome that will start on its first use.
func New(opts Options) *Chrome {
	if opts.Width <= 0 {
		opts.Width = defaultWidth
	}
	if opts.Height <= 0 {
		opts.Height = defaultHeight
	}
	if opts.Timeout <= 0 {
		opts.Timeout = defaultTimeout
	}
	return &Chrome{opts: opts}
}

// Close shuts the browser down and releases its temporary profile.
// Safe to call more than once, and on a Chrome that never started.
func (c *Chrome) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	c.bctx = nil
	// Innermost context first: the page, then the browser, then the
	// allocator that owns the process.
	for i := len(c.cancels) - 1; i >= 0; i-- {
		c.cancels[i]()
	}
	c.cancels = nil
}

// ensure starts the browser once, on first use.
func (c *Chrome) ensure() (context.Context, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, errors.New("browser is closed")
	}
	if c.bctx != nil {
		return c.bctx, nil
	}

	// DefaultExecAllocatorOptions is an array; [:] has no spare
	// capacity, so this append copies rather than writing into it.
	opts := append(chromedp.DefaultExecAllocatorOptions[:],
		chromedp.Flag("headless", !c.opts.Headed),
		chromedp.WindowSize(c.opts.Width, c.opts.Height),
	)
	if c.opts.ExecPath != "" {
		opts = append(opts, chromedp.ExecPath(c.opts.ExecPath))
	}
	if c.opts.UserDataDir != "" {
		opts = append(opts, chromedp.UserDataDir(c.opts.UserDataDir))
	}
	for name, value := range c.opts.Flags {
		opts = append(opts, chromedp.Flag(name, value))
	}

	// The browser is rooted at Background, not at any caller's context:
	// it outlives every turn that uses it, and only Close ends it.
	allocCtx, allocCancel := chromedp.NewExecAllocator(context.Background(), opts...)
	bctx, browserCancel := chromedp.NewContext(allocCtx)
	c.cancels = []context.CancelFunc{allocCancel, browserCancel}

	if c.opts.OnFrame != nil {
		c.listenScreencast(bctx)
	}

	// Starting the process is deferred until the first command, so this
	// navigation is what actually launches Chrome — and what surfaces a
	// missing binary as an error the caller can report.
	if err := chromedp.Run(bctx, chromedp.Navigate("about:blank")); err != nil {
		browserCancel()
		allocCancel()
		c.cancels = nil
		return nil, fmt.Errorf("starting chrome: %w", err)
	}

	if c.opts.OnFrame != nil {
		if err := chromedp.Run(bctx, page.StartScreencast().
			WithFormat(page.ScreencastFormatJpeg).
			WithQuality(70).
			WithEveryNthFrame(1)); err != nil {
			return nil, fmt.Errorf("starting screencast: %w", err)
		}
	}

	c.bctx = bctx
	return bctx, nil
}

// listenScreencast forwards each screencast frame to OnFrame and
// acknowledges it. The ack is what releases the next frame, so it goes
// on its own goroutine: this callback runs on the event loop that would
// have to deliver the ack's own response.
func (c *Chrome) listenScreencast(bctx context.Context) {
	onFrame := c.opts.OnFrame
	chromedp.ListenTarget(bctx, func(ev any) {
		frame, ok := ev.(*page.EventScreencastFrame)
		if !ok {
			return
		}
		onFrame(frame.Data)
		go func(session int64) {
			_ = chromedp.Run(bctx, page.ScreencastFrameAck(session))
		}(frame.SessionID)
	})
}

// run executes actions against the page, bounded by both the caller's
// context and the configured timeout.
//
// Neither bound touches the browser itself. The action context is
// derived from the browser context so that cancelling it aborts the
// in-flight commands and leaves the page open — cancelling the browser
// context is what closes it, and that is Close's job alone.
func (c *Chrome) run(ctx context.Context, actions ...chromedp.Action) error {
	bctx, err := c.ensure()
	if err != nil {
		return err
	}

	c.runMu.Lock()
	defer c.runMu.Unlock()

	actx, cancel := context.WithTimeout(bctx, c.opts.Timeout)
	defer cancel()
	stop := context.AfterFunc(ctx, cancel)
	defer stop()

	if err := chromedp.Run(actx, actions...); err != nil {
		// A cancelled turn and an exceeded step timeout both surface
		// from chromedp as a bare "context canceled". Say which.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		if errors.Is(actx.Err(), context.DeadlineExceeded) {
			return fmt.Errorf("timed out after %s: %w", c.opts.Timeout, err)
		}
		return err
	}
	return nil
}

// Navigate implements browser.Driver.
func (c *Chrome) Navigate(ctx context.Context, url string) error {
	return c.run(ctx, chromedp.Navigate(url))
}

// Click implements browser.Driver.
func (c *Chrome) Click(ctx context.Context, selector string) error {
	return c.run(ctx, chromedp.Click(selector, chromedp.ByQuery))
}

// SendKeys implements browser.Driver.
func (c *Chrome) SendKeys(ctx context.Context, selector, text string) error {
	return c.run(ctx, chromedp.SendKeys(selector, text, chromedp.ByQuery))
}

// WaitVisible implements browser.Driver.
func (c *Chrome) WaitVisible(ctx context.Context, selector string) error {
	return c.run(ctx, chromedp.WaitVisible(selector, chromedp.ByQuery))
}

// WaitReady implements browser.Driver.
func (c *Chrome) WaitReady(ctx context.Context, selector string) error {
	return c.run(ctx, chromedp.WaitReady(selector, chromedp.ByQuery))
}

// Text implements browser.Driver.
func (c *Chrome) Text(ctx context.Context, selector string) (string, error) {
	var out string
	err := c.run(ctx, chromedp.Text(selector, &out, chromedp.ByQuery))
	return out, err
}

// Value implements browser.Driver.
func (c *Chrome) Value(ctx context.Context, selector string) (string, error) {
	var out string
	err := c.run(ctx, chromedp.Value(selector, &out, chromedp.ByQuery))
	return out, err
}

// Eval implements browser.Driver. A nil out discards the result, which
// is not just an optimisation: chromedp rejects an undefined completion
// value when a destination is given, and the void half of the
// Set-of-Marks API returns exactly that.
func (c *Chrome) Eval(ctx context.Context, expr string, out any) error {
	return c.run(ctx, chromedp.Evaluate(expr, out))
}

// KeyEvent implements browser.Driver.
func (c *Chrome) KeyEvent(ctx context.Context, text string) error {
	return c.run(ctx, chromedp.KeyEvent(text))
}

// Screenshot implements browser.Driver. The result is PNG: screencast
// frames are JPEG because they are for watching, but a still that a
// vision model has to read text off is worth the extra bytes.
func (c *Chrome) Screenshot(ctx context.Context) ([]byte, error) {
	var out []byte
	err := c.run(ctx, chromedp.CaptureScreenshot(&out))
	return out, err
}
