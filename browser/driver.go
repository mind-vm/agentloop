package browser

import "context"

// Driver is the seam between the browser pack and a real browser. It
// is deliberately small — ten methods, all synchronous, all taking
// the per-run context — so an implementation on top of chromedp,
// Playwright, WebDriver, or a test fake is a short file.
//
// Every method blocks until the browser has finished the step,
// including any navigation it triggered. Callers hold no session
// handle: a Driver is bound to exactly one page, and the pack drives
// that page in sequence.
//
// Implementations must honour ctx cancellation by aborting the
// in-flight command, and must not tear the browser down when it fires
// — ctx is the agent's turn, which is shorter than the browser's life.
type Driver interface {
	// Navigate loads url and waits for the page to settle.
	Navigate(ctx context.Context, url string) error

	// Click dispatches a real mouse click at the centre of the first
	// element matching the CSS selector, waiting for it first.
	Click(ctx context.Context, selector string) error

	// SendKeys focuses the first element matching the CSS selector and
	// types text into it as key events.
	SendKeys(ctx context.Context, selector, text string) error

	// WaitVisible blocks until the CSS selector matches an element
	// that is present and visible.
	WaitVisible(ctx context.Context, selector string) error

	// WaitReady blocks until the CSS selector matches an element that
	// is present in the DOM, visible or not.
	WaitReady(ctx context.Context, selector string) error

	// Text returns the visible text of the first element matching the
	// CSS selector.
	Text(ctx context.Context, selector string) (string, error)

	// Value returns the `value` property of the first element matching
	// the CSS selector.
	Value(ctx context.Context, selector string) (string, error)

	// Eval evaluates expr in the page and decodes its completion value
	// into out, which is a pointer with encoding/json semantics. A nil
	// out evaluates expr and discards the result.
	Eval(ctx context.Context, expr string, out any) error

	// KeyEvent types text into whatever currently has focus, as key
	// events the page sees as real input.
	KeyEvent(ctx context.Context, text string) error

	// Screenshot captures the current viewport as PNG bytes.
	Screenshot(ctx context.Context) ([]byte, error)
}

// Vision answers a natural-language question about a screenshot. The
// pack takes one as a callback rather than importing a model client, so
// this package stays free of any vision-provider dependency — an
// application closes it over whichever model it already has configured
// (agentloop's own llm.Client, Gemini, whatever).
//
// A nil Vision is valid and common: the pack then registers no ask() /
// askMarks() and advertises neither in the prompt, so the model is
// never shown a primitive that would fail. mark() still works — its
// structured element listing is what most navigation actually needs.
type Vision func(ctx context.Context, screenshot []byte, question string) (string, error)

// Mark is one interactive element found by mark(). X and Y are the
// element's centre in viewport coordinates, W and H its size, all in
// CSS pixels rounded to integers. Name is the element's best available
// accessible label — aria-label, alt, title, placeholder, value, or
// trimmed inner text, in that order — truncated to 80 characters.
type Mark struct {
	ID   int    `json:"id"`
	Tag  string `json:"tag"`
	Role string `json:"role"`
	Name string `json:"name"`
	X    int    `json:"x"`
	Y    int    `json:"y"`
	W    int    `json:"w"`
	H    int    `json:"h"`
}
