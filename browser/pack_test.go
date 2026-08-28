package browser_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mind-vm/agentloop/browser"
	"github.com/mind-vm/agentloop/sandbox"
)

// fakeDriver records every call as a "method:arg" line so a test can
// assert on the sequence the pack produced, and serves canned JSON for
// evaluations. It stands in for a real browser everywhere below —
// nothing in this package's logic needs one, which is the point of the
// Driver seam.
type fakeDriver struct {
	calls []string

	// marksJSON is what an __som.install() evaluation decodes into.
	marksJSON string
	// evalJSON is what a plain browser.eval() decodes into.
	evalJSON string
	// shot is what Screenshot returns.
	shot []byte

	// failOn names a method that should return failErr instead of
	// succeeding, e.g. "Click".
	failOn  string
	failErr error
}

func (f *fakeDriver) record(name, arg string) error {
	f.calls = append(f.calls, name+":"+arg)
	if f.failOn == name {
		if f.failErr != nil {
			return f.failErr
		}
		return errors.New("driver failed")
	}
	return nil
}

func (f *fakeDriver) Navigate(_ context.Context, url string) error {
	return f.record("Navigate", url)
}
func (f *fakeDriver) Click(_ context.Context, sel string) error { return f.record("Click", sel) }
func (f *fakeDriver) SendKeys(_ context.Context, sel, text string) error {
	return f.record("SendKeys", sel+"="+text)
}
func (f *fakeDriver) WaitVisible(_ context.Context, sel string) error {
	return f.record("WaitVisible", sel)
}
func (f *fakeDriver) WaitReady(_ context.Context, sel string) error {
	return f.record("WaitReady", sel)
}
func (f *fakeDriver) Text(_ context.Context, sel string) (string, error) {
	return "text of " + sel, f.record("Text", sel)
}
func (f *fakeDriver) Value(_ context.Context, sel string) (string, error) {
	return "value of " + sel, f.record("Value", sel)
}
func (f *fakeDriver) KeyEvent(_ context.Context, text string) error {
	return f.record("KeyEvent", text)
}
func (f *fakeDriver) Screenshot(_ context.Context) ([]byte, error) {
	return f.shot, f.record("Screenshot", "")
}

// Eval records the *interesting* part of the expression: the injected
// Set-of-Marks source is several KB and identical every time, so tests
// assert on the call that follows it.
func (f *fakeDriver) Eval(_ context.Context, expr string, out any) error {
	trimmed := expr
	if i := strings.LastIndex(expr, ";__som."); i >= 0 {
		trimmed = expr[i+1:]
	}
	if err := f.record("Eval", trimmed); err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	raw := f.evalJSON
	if strings.Contains(expr, "__som.install()") {
		raw = f.marksJSON
	}
	if raw == "" {
		raw = "null"
	}
	return json.Unmarshal([]byte(raw), out)
}

const twoMarks = `[
  {"id":1,"tag":"input","role":"","name":"Search","x":100,"y":40,"w":200,"h":30},
  {"id":2,"tag":"button","role":"","name":"Go","x":320,"y":40,"w":40,"h":30}
]`

// newSandbox wires the pack into a sandbox with an explicit fail-open
// policy, so a test that cares about policy installs its own.
func newSandbox(t *testing.T, d browser.Driver, v browser.Vision) *sandbox.Sandbox {
	t.Helper()
	s := sandbox.New(browser.Pack(context.Background(), d, v))
	s.SetPolicy(sandbox.AllowAll)
	return s
}

func wantCalls(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("driver calls:\n got %v\nwant %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("driver call %d:\n got %q\nwant %q", i, got[i], want[i])
		}
	}
}

// TestPack_NavigateAndRead confirms the plain selector primitives reach
// the driver and hand their results back to the script.
func TestPack_NavigateAndRead(t *testing.T) {
	d := &fakeDriver{}
	s := newSandbox(t, d, nil)

	out, err := s.Execute(`
		browser.goto("https://example.com");
		browser.waitVisible("h1");
		browser.type("#q", "socks");
		browser.click("button[type=submit]");
		browser.text("h1");
	`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if out != "text of h1" {
		t.Fatalf("text(): got %q, want %q", out, "text of h1")
	}
	wantCalls(t, d.calls, []string{
		"Navigate:https://example.com",
		"WaitVisible:h1",
		"SendKeys:#q=socks",
		"Click:button[type=submit]",
		"Text:h1",
	})
}

// TestPack_EvalReturnsPageValue confirms eval decodes the page's value
// into JS rather than returning it as a string.
func TestPack_EvalReturnsPageValue(t *testing.T) {
	d := &fakeDriver{evalJSON: `{"items":3}`}
	s := newSandbox(t, d, nil)

	out, err := s.Execute(`String(browser.eval("({items: document.links.length})").items)`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if out != "3" {
		t.Fatalf("eval(): got %q, want %q", out, "3")
	}
}

// TestPack_ScrollUsesScrollBy confirms scroll is expressed as a page
// evaluation with the offsets the script asked for.
func TestPack_ScrollUsesScrollBy(t *testing.T) {
	d := &fakeDriver{}
	s := newSandbox(t, d, nil)

	if _, err := s.Execute(`browser.scroll(0, 600)`); err != nil {
		t.Fatalf("execute: %v", err)
	}
	wantCalls(t, d.calls, []string{"Eval:window.scrollBy(0,600)"})
}

// TestPack_MarkReturnsListing confirms mark() surfaces the element
// metadata under the lowercase keys the prompt documents — the Go field
// names would be a silent breaking difference from the docs.
func TestPack_MarkReturnsListing(t *testing.T) {
	d := &fakeDriver{marksJSON: twoMarks}
	s := newSandbox(t, d, nil)

	out, err := s.Execute(`
		var marks = browser.mark();
		marks.length + ":" + marks[0].name + ":" + marks[1].tag + ":" + marks[0].w;
	`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if out != "2:Search:button:200" {
		t.Fatalf("mark(): got %q, want %q", out, "2:Search:button:200")
	}
}

// TestPack_ClickMarkNeedsMarks confirms an id used before any mark()
// errors, and that the message names the fix.
func TestPack_ClickMarkNeedsMarks(t *testing.T) {
	d := &fakeDriver{marksJSON: twoMarks}
	s := newSandbox(t, d, nil)

	_, err := s.Execute(`browser.clickMark(1)`)
	if err == nil {
		t.Fatal("expected clickMark without marks to fail")
	}
	if !strings.Contains(err.Error(), "browser.mark()") {
		t.Errorf("error should point at the fix, got: %v", err)
	}
	for _, c := range d.calls {
		if strings.Contains(c, "__som.click") {
			t.Fatalf("clickMark reached the page without marks: %v", d.calls)
		}
	}
}

// TestPack_GotoDropsMarks is the guard that matters most: a navigation
// replaces the document, so a pre-goto id would resolve against a
// different page. Acting on one has to fail, not click something else.
func TestPack_GotoDropsMarks(t *testing.T) {
	d := &fakeDriver{marksJSON: twoMarks}
	s := newSandbox(t, d, nil)

	_, err := s.Execute(`
		browser.mark();
		browser.goto("https://example.com/next");
		browser.clickMark(1);
	`)
	if err == nil {
		t.Fatal("expected clickMark after goto to fail")
	}
	if !strings.Contains(err.Error(), "no valid marks") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestPack_UnmarkKeepsIds confirms removing the overlay does not
// invalidate the ids — askMarks depends on that, and so does any script
// that cleans up the boxes before acting.
func TestPack_UnmarkKeepsIds(t *testing.T) {
	d := &fakeDriver{marksJSON: twoMarks}
	s := newSandbox(t, d, nil)

	if _, err := s.Execute(`
		browser.mark();
		browser.unmark();
		browser.clickMark(2);
	`); err != nil {
		t.Fatalf("execute: %v", err)
	}
	wantCalls(t, d.calls, []string{
		"Eval:__som.install()",
		"Eval:__som.uninstall()",
		"Eval:__som.click(2)",
	})
}

// TestPack_TypeMarkFocusesThenTypes confirms typing into a mark goes
// through a real key-event dispatch after focusing, not through a value
// assignment the page's framework would never see.
func TestPack_TypeMarkFocusesThenTypes(t *testing.T) {
	d := &fakeDriver{marksJSON: twoMarks}
	s := newSandbox(t, d, nil)

	if _, err := s.Execute(`
		browser.mark();
		browser.typeMark(1, "socks");
	`); err != nil {
		t.Fatalf("execute: %v", err)
	}
	wantCalls(t, d.calls, []string{
		"Eval:__som.install()",
		"Eval:__som.focus(1)",
		"KeyEvent:socks",
	})
}

// TestPack_NoVisionHidesAsk confirms a pack built without a vision
// backend neither registers ask/askMarks nor advertises them — the
// model is never shown a primitive that would fail.
func TestPack_NoVisionHidesAsk(t *testing.T) {
	s := newSandbox(t, &fakeDriver{}, nil)

	out, err := s.Execute(`typeof browser.ask + "," + typeof browser.askMarks + "," + typeof browser.mark`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if out != "undefined,undefined,function" {
		t.Fatalf("got %q, want %q", out, "undefined,undefined,function")
	}

	p := browser.Pack(context.Background(), &fakeDriver{}, nil)
	if strings.Contains(p.Prompt, "askMarks") {
		t.Error("prompt advertises askMarks with no vision backend")
	}
	if _, ok := p.HelpEntries["browser.askMarks"]; ok {
		t.Error("help offers browser.askMarks with no vision backend")
	}
}

// TestPack_AskMarksRoundTrip confirms the mark → screenshot → unmark →
// ask sequence, that the model's answer comes back, and that the ids it
// referred to are still usable afterwards.
func TestPack_AskMarksRoundTrip(t *testing.T) {
	d := &fakeDriver{marksJSON: twoMarks, shot: []byte("PNG")}
	var gotShot []byte
	var gotQuestion string
	vision := func(_ context.Context, shot []byte, question string) (string, error) {
		gotShot, gotQuestion = shot, question
		return "2", nil
	}
	s := newSandbox(t, d, vision)

	out, err := s.Execute(`
		var id = parseInt(browser.askMarks("which mark is the search button?"), 10);
		browser.clickMark(id);
		"clicked " + id;
	`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if out != "clicked 2" {
		t.Fatalf("got %q, want %q", out, "clicked 2")
	}
	if string(gotShot) != "PNG" {
		t.Errorf("vision got screenshot %q", gotShot)
	}
	if gotQuestion != "which mark is the search button?" {
		t.Errorf("vision got question %q", gotQuestion)
	}
	wantCalls(t, d.calls, []string{
		"Eval:__som.install()",
		"Screenshot:",
		"Eval:__som.uninstall()",
		"Eval:__som.click(2)",
	})
}

// TestPack_VisionErrorIsCatchable confirms a failing vision backend
// surfaces as a JS throw the script can recover from.
func TestPack_VisionErrorIsCatchable(t *testing.T) {
	vision := func(context.Context, []byte, string) (string, error) {
		return "", errors.New("model unavailable")
	}
	s := newSandbox(t, &fakeDriver{}, vision)

	out, err := s.Execute(`
		var result = "no throw";
		try { browser.ask("what is this?"); } catch (e) { result = "caught: " + (e.message || e); }
		result;
	`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "model unavailable") {
		t.Fatalf("got %q, want the backend error", out)
	}
}

// TestPack_DriverErrorIsCatchable confirms the same for the browser
// itself: a failed step is a JS exception, not a dead turn.
func TestPack_DriverErrorIsCatchable(t *testing.T) {
	d := &fakeDriver{failOn: "Click", failErr: errors.New("node not found")}
	s := newSandbox(t, d, nil)

	out, err := s.Execute(`
		var result = "no throw";
		try { browser.click("#missing"); } catch (e) { result = "caught: " + (e.message || e); }
		result;
	`)
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "node not found") {
		t.Fatalf("got %q, want the driver error", out)
	}
}

// TestPack_NilDriver confirms a pack built without a driver says so
// instead of panicking on a nil interface.
func TestPack_NilDriver(t *testing.T) {
	s := newSandbox(t, nil, nil)

	_, err := s.Execute(`browser.goto("https://example.com")`)
	if err == nil {
		t.Fatal("expected an error with no driver")
	}
	if !strings.Contains(err.Error(), "no browser driver configured") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestPack_MissingArguments confirms a call with no argument reports
// the missing parameter rather than navigating to "undefined".
func TestPack_MissingArguments(t *testing.T) {
	d := &fakeDriver{}
	s := newSandbox(t, d, nil)

	_, err := s.Execute(`browser.goto()`)
	if err == nil || !strings.Contains(err.Error(), "url is required") {
		t.Fatalf("goto() with no url: %v", err)
	}
	if len(d.calls) != 0 {
		t.Fatalf("driver was called anyway: %v", d.calls)
	}
}

// TestPack_SleepHonoursCancellation confirms a waiting script gives up
// when the turn's context ends, instead of holding the runtime open.
func TestPack_SleepHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := sandbox.New(browser.Pack(ctx, &fakeDriver{}, nil))
	s.SetPolicy(sandbox.AllowAll)

	_, err := s.Execute(`browser.sleep(10000)`)
	if err == nil {
		t.Fatal("expected sleep on a cancelled context to fail")
	}
	if !strings.Contains(err.Error(), "context canceled") {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestPack_EmitsBrowserEvents confirms page-changing steps show up in
// the event stream a trace or UI drawer reads, and that typed text does
// not — browser.type is how a password reaches a login form.
func TestPack_EmitsBrowserEvents(t *testing.T) {
	d := &fakeDriver{}
	var events []sandbox.Event
	s := sandbox.New(browser.Pack(context.Background(), d, nil))
	s.SetPolicy(sandbox.AllowAll)
	s.SetOnEvent(func(e sandbox.Event) { events = append(events, e) })

	if _, err := s.Execute(`
		browser.goto("https://example.com");
		browser.type("#password", "hunter2");
	`); err != nil {
		t.Fatalf("execute: %v", err)
	}

	var summaries []string
	for _, e := range events {
		if e.Kind != sandbox.EventBrowser {
			continue
		}
		summaries = append(summaries, e.Summary)
		if strings.Contains(e.Summary+e.Detail+e.Result, "hunter2") {
			t.Fatalf("typed text leaked into an event: %+v", e)
		}
	}
	wantCalls(t, summaries, []string{
		"goto https://example.com",
		"type 7 chars into #password",
	})
}

// TestPack_DefaultPolicyBlocksPrivateNetwork confirms navigation runs
// through the same URL rules as fetch: an agent must not reach an
// internal address just by opening it in a tab.
func TestPack_DefaultPolicyBlocksPrivateNetwork(t *testing.T) {
	d := &fakeDriver{}
	s := sandbox.New(browser.Pack(context.Background(), d, nil))
	s.SetPolicy(sandbox.DefaultPolicy{})

	_, err := s.Execute(`browser.goto("http://192.168.1.1/admin")`)
	if err == nil {
		t.Fatal("expected a private address to be denied")
	}
	if !strings.Contains(err.Error(), "private or internal") {
		t.Errorf("unexpected denial reason: %v", err)
	}
	if len(d.calls) != 0 {
		t.Fatalf("driver navigated despite the denial: %v", d.calls)
	}
}

// TestPack_DefaultPolicyHonoursAllowlist confirms a configured prefix
// list gates browser navigation the way it gates fetch.
func TestPack_DefaultPolicyHonoursAllowlist(t *testing.T) {
	d := &fakeDriver{}
	s := sandbox.New(browser.Pack(context.Background(), d, nil))
	s.SetPolicy(sandbox.DefaultPolicy{URLAllowPrefixes: []string{"https://shop.example.com/"}})

	if _, err := s.Execute(`browser.goto("https://shop.example.com/cart")`); err != nil {
		t.Fatalf("allowlisted URL was denied: %v", err)
	}
	_, err := s.Execute(`browser.goto("https://elsewhere.example.net/")`)
	if err == nil || !strings.Contains(err.Error(), "not in the allowlist") {
		t.Fatalf("off-allowlist URL: %v", err)
	}
	wantCalls(t, d.calls, []string{"Navigate:https://shop.example.com/cart"})
}

// TestPack_PolicyCanDenyTheWholeTool confirms a checker that refuses
// toolName "browser" stops every primitive, not just navigation.
func TestPack_PolicyCanDenyTheWholeTool(t *testing.T) {
	d := &fakeDriver{}
	s := sandbox.New(browser.Pack(context.Background(), d, nil))
	s.SetPolicy(denyBrowser{})

	for _, script := range []string{
		`browser.goto("https://example.com")`,
		`browser.click("#buy")`,
		`browser.mark()`,
	} {
		if _, err := s.Execute(script); err == nil {
			t.Errorf("%s was allowed", script)
		}
	}
	if len(d.calls) != 0 {
		t.Fatalf("driver ran despite denial: %v", d.calls)
	}
}

type denyBrowser struct{}

func (denyBrowser) Check(_ context.Context, tool string, _ map[string]any) (sandbox.PolicyCheckResult, error) {
	if tool == "browser" {
		return sandbox.PolicyCheckResult{Allowed: false, Reason: "browsing is off for this session"}, nil
	}
	return sandbox.PolicyCheckResult{Allowed: true}, nil
}
