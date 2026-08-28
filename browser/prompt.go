package browser

// The prompt and help text are split from the pack so the wording — the
// part that actually decides whether a model drives the browser well —
// can be read and edited without scrolling past the plumbing.

const promptHead = `// --- Browser ---
/** A real browser, one page. Every call is synchronous: it returns once the
 *  browser has finished the step, and throws on failure. */
declare const browser: {
  /** Load a URL and wait for the page to settle. Drops any existing marks. */
  goto(url: string): void;
  /** Click the first element matching a CSS selector, waiting for it first. */
  click(selector: string): void;
  /** Type into the first element matching a CSS selector. */
  type(selector: string, text: string): void;
  /** Block until the selector matches a visible element. */
  waitVisible(selector: string): void;
  /** Block until the selector matches an element in the DOM, visible or not. */
  waitReady(selector: string): void;
  /** Visible text of the first matching element. */
  text(selector: string): string;
  /** The 'value' property of the first matching element. */
  value(selector: string): string;
  /** Evaluate an expression in the page and return its value. */
  eval(expression: string): any;
  /** Scroll the page by a pixel offset. Re-mark afterwards to see what came into view. */
  scroll(dx: number, dy: number): void;
  /** Wait, capped at 30s. Prefer waitVisible when waiting for something specific. */
  sleep(ms: number): void;
  /** Number every visible interactive element and return the listing. Ids are
   *  assigned in document order and renumbered by every mark(), so mark, act,
   *  and re-mark rather than reusing an old listing. Viewport only: scroll and
   *  mark again to reach what is below the fold. */
  mark(): { id: number; tag: string; role: string; name: string; x: number; y: number; w: number; h: number }[];
  /** Remove the numbered boxes from the page. The ids stay usable. */
  unmark(): void;
  /** Click a marked element by id. */
  clickMark(id: number): void;
  /** Focus a marked element by id and type into it. */
  typeMark(id: number, text: string): void;
`

const promptVision = `  /** Screenshot the page and ask a vision model about it. */
  ask(question: string): string;
  /** Mark, screenshot, ask, then strip the boxes. Ids in the answer stay usable
   *  by clickMark / typeMark. Use this when the page's structure is unclear
   *  from mark()'s listing alone. */
  askMarks(question: string): string;
`

const promptTail = `};

// Prefer mark() + clickMark() over CSS selectors on pages you have not seen
// before: the listing tells you what is actually clickable, and an id cannot
// be a selector that silently matches nothing.`

func buildPrompt(hasVision bool) string {
	if hasVision {
		return promptHead + promptVision + promptTail
	}
	return promptHead + promptTail
}

const helpBrowser = `browser — Drive a real browser, one page, synchronously.

  Navigation and reading:
    browser.goto(url), browser.click(sel), browser.type(sel, text)
    browser.waitVisible(sel), browser.waitReady(sel)
    browser.text(sel), browser.value(sel), browser.eval(expr)
    browser.scroll(dx, dy), browser.sleep(ms)

  Set-of-Marks — navigate without selectors:
    var marks = browser.mark();          // [{id, tag, role, name, x, y, w, h}, ...]
    var search = marks.find(m => m.name.toLowerCase().includes("search"));
    browser.typeMark(search.id, "socks");
    browser.clickMark(marks.find(m => m.tag === "button").id);

  Ids come from the most recent mark() and are dropped by goto(); acting on a
  stale id throws rather than clicking the wrong thing. mark() covers the
  viewport only — scroll() and mark() again for what is below the fold.`

const helpMark = `browser.mark() — Number every visible interactive element and return the listing.

  Returns [{id, tag, role, name, x, y, w, h}], ids in document order starting at 1.
  'name' is the element's accessible label: aria-label, alt, title, placeholder,
  value, or inner text, whichever it has first, trimmed to 80 chars.

  Every call renumbers from scratch, and goto() drops the ids entirely. The
  reliable pattern is mark → act → mark again, not mark once and reuse.
  Also draws numbered boxes on the page; browser.unmark() removes them without
  invalidating the ids.`

const helpAskMarks = `browser.askMarks(question) — Ask a vision model about the marked page.

  Installs the numbered boxes, screenshots, removes the boxes, and asks the
  question against that image. The answer's ids stay usable with clickMark() /
  typeMark(). Use it when mark()'s listing is ambiguous — an icon-only button,
  a custom widget with no accessible name — and plain mark() when it is not,
  since this one costs a model call.
  Example: var id = parseInt(browser.askMarks("which mark is the cart icon?"), 10);`

func buildHelp(hasVision bool) map[string]string {
	help := map[string]string{
		"browser":      helpBrowser,
		"browser.mark": helpMark,
	}
	if hasVision {
		help["browser.askMarks"] = helpAskMarks
	}
	return help
}
