package ext

import (
	"context"
	"fmt"
	"strings"

	"github.com/dop251/goja"

	"github.com/mind-vm/agentloop/sandbox"
)

// EmailPack exposes a synchronous `sendEmail({to, subject, body})`
// primitive. It takes a send callback rather than a concrete mailer so
// this package stays transport-agnostic — the application's Capability
// closes the callback over its configured sender + from address.
//
// Every call is policy-gated with toolName "sendEmail";
// sandbox.DefaultPolicy denies it unless granted via
// DefaultPolicy.AllowTools. ctx is the per-session context the check
// shares with the run.
//
// The argument is an object so the call site is self-documenting and
// resilient to added optional fields. Returns true on success; a send
// error surfaces as a catchable JS throw.
func EmailPack(ctx context.Context, send func(to, subject, body string) error) sandbox.Pack {
	return sandbox.Pack{
		Name:        "sendEmail",
		Description: "Send a plain-text email — sendEmail({to, subject, body})",
		Prompt: `// --- Email ---
/** Send a plain-text email. Returns true on success; throws on failure. */
declare function sendEmail(msg: { to: string; subject: string; body: string }): boolean;`,
		HelpEntries: map[string]string{
			"sendEmail": `sendEmail({to, subject, body}) — Send a plain-text email. Returns true; throws on failure.
  Example: sendEmail({ to: "ops@example.com", subject: "Run done", body: "Finished." });`,
		},
		Register: func(rt *goja.Runtime, sb *sandbox.Sandbox) {
			fn := func(call goja.FunctionCall) goja.Value {
				if len(call.Arguments) == 0 || goja.IsUndefined(call.Argument(0)) || goja.IsNull(call.Argument(0)) {
					panic(rt.NewGoError(fmt.Errorf("sendEmail: an argument object {to, subject, body} is required")))
				}
				obj, ok := call.Argument(0).Export().(map[string]any)
				if !ok {
					panic(rt.NewGoError(fmt.Errorf("sendEmail: argument must be an object {to, subject, body}")))
				}
				to := asString(obj["to"])
				subject := asString(obj["subject"])
				body := asString(obj["body"])
				if strings.TrimSpace(to) == "" {
					panic(rt.NewGoError(fmt.Errorf("sendEmail: 'to' is required")))
				}
				sb.CheckPolicyOrPanic(rt, ctx, "sendEmail", map[string]any{"to": to, "subject": subject})
				if send == nil {
					panic(rt.NewGoError(fmt.Errorf("sendEmail: no email backend configured")))
				}
				if err := send(to, subject, body); err != nil {
					panic(rt.NewGoError(fmt.Errorf("sendEmail: %w", err)))
				}
				return rt.ToValue(true)
			}
			_ = rt.Set("sendEmail", fn)
		},
	}
}

func asString(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprintf("%v", v)
}
