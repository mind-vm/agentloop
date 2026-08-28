package browser

import (
	"context"
	"fmt"
	"time"

	"github.com/dop251/goja"

	"github.com/mind-vm/agentloop/sandbox"
)

// maxSleep caps browser.sleep(). A script that wants to wait longer
// than this for something wants waitVisible(), and an unbounded sleep
// is the easiest way for a turn to burn its whole execution budget
// doing nothing.
const maxSleep = 30 * time.Second

// Pack exposes a `browser` global that drives d, one page, one call at
// a time. Every primitive is synchronous: it returns when the browser
// has finished the step, so a script reads as a straight sequence of
// page actions.
//
// Every call is policy-gated under tool name "browser", with the
// primitive's name in args["action"] and, for goto, the target in
// args["url"] — sandbox.DefaultPolicy runs browser URLs through the
// same rules it applies to fetch, so an agent cannot reach a private
// address by opening it in a tab instead. Note that this gates
// navigation, not the page: a page the agent has been allowed to open
// can redirect itself, and browser.eval() can set location. Treat the
// allowlist as a statement about where the agent may steer, not as a
// network boundary.
//
// vision may be nil, in which case ask() and askMarks() are neither
// registered nor advertised; mark() works either way. ctx is the
// per-run context — it bounds every call and is what the driver aborts
// on when the turn ends.
func Pack(ctx context.Context, d Driver, vision Vision) sandbox.Pack {
	// marksValid records whether the ids from the last mark() still
	// resolve in the page. A navigation replaces the document and takes
	// the id→element map with it, so acting on a pre-goto id would hit
	// whatever now holds that number — an error is the only safe answer.
	marksValid := false
	hasVision := vision != nil

	return sandbox.Pack{
		Name:        "browser",
		Description: "Drive a real browser — browser.goto/click/type/text, plus numbered Set-of-Marks navigation",
		Prompt:      buildPrompt(hasVision),
		HelpEntries: buildHelp(hasVision),
		Register: func(rt *goja.Runtime, sb *sandbox.Sandbox) {
			fail := func(name string, err error) {
				sb.Emit(sandbox.Event{
					Kind:    sandbox.EventError,
					Summary: "browser." + name + " failed",
					Detail:  err.Error(),
				})
				panic(rt.NewGoError(fmt.Errorf("browser.%s: %w", name, err)))
			}

			// gate runs the driver-presence check and the policy check
			// every primitive shares. args carries the call's
			// user-visible inputs; "action" is added here so a policy
			// can discriminate between navigating and reading.
			gate := func(name string, args map[string]any) {
				if d == nil {
					panic(rt.NewGoError(fmt.Errorf("browser.%s: no browser driver configured", name)))
				}
				if args == nil {
					args = make(map[string]any, 1)
				}
				args["action"] = name
				sb.CheckPolicyOrPanic(rt, ctx, "browser", args)
			}

			str := func(call goja.FunctionCall, i int, arg, name string) string {
				v := call.Argument(i)
				if goja.IsUndefined(v) || goja.IsNull(v) || v.String() == "" {
					panic(rt.NewGoError(fmt.Errorf("browser.%s: %s is required", name, arg)))
				}
				return v.String()
			}

			num := func(call goja.FunctionCall, i int, arg, name string) int {
				v := call.Argument(i)
				if goja.IsUndefined(v) || goja.IsNull(v) {
					panic(rt.NewGoError(fmt.Errorf("browser.%s: %s is required", name, arg)))
				}
				return int(v.ToInteger())
			}

			optNum := func(call goja.FunctionCall, i int) int {
				v := call.Argument(i)
				if goja.IsUndefined(v) || goja.IsNull(v) {
					return 0
				}
				return int(v.ToInteger())
			}

			// requireMarks refuses to act on ids the page can no longer
			// resolve, with the fix in the message — the model recovers
			// by re-marking rather than by guessing.
			requireMarks := func(name string) {
				if !marksValid {
					panic(rt.NewGoError(fmt.Errorf(
						"browser.%s: no valid marks — call browser.mark() first (ids are renumbered by every mark() and dropped by goto())", name)))
				}
			}

			act := func(summary string) {
				sb.Emit(sandbox.Event{Kind: sandbox.EventBrowser, Summary: summary})
			}

			// evalVoid runs expr in the page and discards the result.
			evalVoid := func(name, expr string) {
				if err := d.Eval(ctx, expr, nil); err != nil {
					fail(name, err)
				}
			}

			obj := rt.NewObject()
			set := func(name string, fn func(goja.FunctionCall) goja.Value) {
				_ = obj.Set(name, fn)
			}

			set("goto", func(call goja.FunctionCall) goja.Value {
				url := str(call, 0, "url", "goto")
				gate("goto", map[string]any{"url": url})
				act("goto " + url)
				if err := d.Navigate(ctx, url); err != nil {
					fail("goto", err)
				}
				marksValid = false
				return goja.Undefined()
			})

			set("click", func(call goja.FunctionCall) goja.Value {
				sel := str(call, 0, "selector", "click")
				gate("click", map[string]any{"selector": sel})
				act("click " + sel)
				if err := d.Click(ctx, sel); err != nil {
					fail("click", err)
				}
				return goja.Undefined()
			})

			set("type", func(call goja.FunctionCall) goja.Value {
				sel := str(call, 0, "selector", "type")
				text := call.Argument(1).String()
				gate("type", map[string]any{"selector": sel})
				// The typed text stays out of the event: this is the
				// primitive that fills password and token fields.
				act(fmt.Sprintf("type %d chars into %s", len(text), sel))
				if err := d.SendKeys(ctx, sel, text); err != nil {
					fail("type", err)
				}
				return goja.Undefined()
			})

			set("waitVisible", func(call goja.FunctionCall) goja.Value {
				sel := str(call, 0, "selector", "waitVisible")
				gate("waitVisible", map[string]any{"selector": sel})
				if err := d.WaitVisible(ctx, sel); err != nil {
					fail("waitVisible", err)
				}
				return goja.Undefined()
			})

			set("waitReady", func(call goja.FunctionCall) goja.Value {
				sel := str(call, 0, "selector", "waitReady")
				gate("waitReady", map[string]any{"selector": sel})
				if err := d.WaitReady(ctx, sel); err != nil {
					fail("waitReady", err)
				}
				return goja.Undefined()
			})

			set("text", func(call goja.FunctionCall) goja.Value {
				sel := str(call, 0, "selector", "text")
				gate("text", map[string]any{"selector": sel})
				out, err := d.Text(ctx, sel)
				if err != nil {
					fail("text", err)
				}
				return rt.ToValue(out)
			})

			set("value", func(call goja.FunctionCall) goja.Value {
				sel := str(call, 0, "selector", "value")
				gate("value", map[string]any{"selector": sel})
				out, err := d.Value(ctx, sel)
				if err != nil {
					fail("value", err)
				}
				return rt.ToValue(out)
			})

			set("eval", func(call goja.FunctionCall) goja.Value {
				expr := str(call, 0, "expression", "eval")
				gate("eval", map[string]any{"expression": expr})
				var out any
				if err := d.Eval(ctx, expr, &out); err != nil {
					fail("eval", err)
				}
				return rt.ToValue(out)
			})

			set("scroll", func(call goja.FunctionCall) goja.Value {
				dx, dy := optNum(call, 0), optNum(call, 1)
				gate("scroll", map[string]any{"dx": dx, "dy": dy})
				evalVoid("scroll", fmt.Sprintf("window.scrollBy(%d,%d)", dx, dy))
				return goja.Undefined()
			})

			set("sleep", func(call goja.FunctionCall) goja.Value {
				ms := num(call, 0, "ms", "sleep")
				if ms <= 0 {
					return goja.Undefined()
				}
				wait := time.Duration(ms) * time.Millisecond
				if wait > maxSleep {
					wait = maxSleep
				}
				timer := time.NewTimer(wait)
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-ctx.Done():
					panic(rt.NewGoError(fmt.Errorf("browser.sleep: %w", ctx.Err())))
				}
				return goja.Undefined()
			})

			set("mark", func(call goja.FunctionCall) goja.Value {
				gate("mark", nil)
				var marks []Mark
				if err := d.Eval(ctx, somJS+";__som.install()", &marks); err != nil {
					fail("mark", err)
				}
				marksValid = true
				act(fmt.Sprintf("mark: %d interactive elements", len(marks)))
				return rt.ToValue(marksToJS(marks))
			})

			set("unmark", func(call goja.FunctionCall) goja.Value {
				gate("unmark", nil)
				evalVoid("unmark", somJS+";__som.uninstall()")
				return goja.Undefined()
			})

			set("clickMark", func(call goja.FunctionCall) goja.Value {
				id := num(call, 0, "id", "clickMark")
				gate("clickMark", map[string]any{"id": id})
				requireMarks("clickMark")
				act(fmt.Sprintf("clickMark %d", id))
				evalVoid("clickMark", fmt.Sprintf("%s;__som.click(%d)", somJS, id))
				return goja.Undefined()
			})

			set("typeMark", func(call goja.FunctionCall) goja.Value {
				id := num(call, 0, "id", "typeMark")
				text := call.Argument(1).String()
				gate("typeMark", map[string]any{"id": id})
				requireMarks("typeMark")
				act(fmt.Sprintf("typeMark %d, %d chars", id, len(text)))
				// Focus through the mark map, then type through the
				// driver: keystrokes have to reach the page as real key
				// events, not as a value assignment a framework ignores.
				evalVoid("typeMark", fmt.Sprintf("%s;__som.focus(%d)", somJS, id))
				if err := d.KeyEvent(ctx, text); err != nil {
					fail("typeMark", err)
				}
				return goja.Undefined()
			})

			if !hasVision {
				_ = rt.Set("browser", obj)
				return
			}

			ask := func(name, question string) goja.Value {
				shot, err := d.Screenshot(ctx)
				if err != nil {
					fail(name, err)
				}
				answer, err := vision(ctx, shot, question)
				if err != nil {
					fail(name, err)
				}
				sb.Emit(sandbox.Event{
					Kind:    sandbox.EventBrowser,
					Summary: "browser." + name + ": " + question,
					Result:  answer,
				})
				return rt.ToValue(answer)
			}

			set("ask", func(call goja.FunctionCall) goja.Value {
				question := str(call, 0, "question", "ask")
				gate("ask", map[string]any{"question": question})
				return ask("ask", question)
			})

			set("askMarks", func(call goja.FunctionCall) goja.Value {
				question := str(call, 0, "question", "askMarks")
				gate("askMarks", map[string]any{"question": question})
				// Mark, capture, then strip the boxes before asking: the
				// model sees the marked page, the user's next frame is
				// clean, and the ids in the answer stay resolvable
				// because uninstall() keeps the element map.
				evalVoid("askMarks", somJS+";__som.install()")
				marksValid = true
				shot, err := d.Screenshot(ctx)
				if err != nil {
					fail("askMarks", err)
				}
				evalVoid("askMarks", somJS+";__som.uninstall()")
				answer, err := vision(ctx, shot, question)
				if err != nil {
					fail("askMarks", err)
				}
				sb.Emit(sandbox.Event{
					Kind:    sandbox.EventBrowser,
					Summary: "browser.askMarks: " + question,
					Result:  answer,
				})
				return rt.ToValue(answer)
			})

			_ = rt.Set("browser", obj)
		},
	}
}

// marksToJS converts marks to plain maps so the sandbox runtime sees
// the lowercase key names the prompt documents, rather than the Go
// field names goja would otherwise expose.
func marksToJS(marks []Mark) []map[string]any {
	out := make([]map[string]any, 0, len(marks))
	for _, m := range marks {
		out = append(out, map[string]any{
			"id": m.ID, "tag": m.Tag, "role": m.Role, "name": m.Name,
			"x": m.X, "y": m.Y, "w": m.W, "h": m.H,
		})
	}
	return out
}
