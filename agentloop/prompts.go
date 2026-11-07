package agentloop

import "strings"

// TextEmissionSystemPrompt is the workflow contract the loop layers on
// top of the sandbox's primitive documentation: each reply is one fenced
// ```javascript block that runs; what the script leaves in `state` is
// threaded into the next turn (full fidelity, server-side — the model
// sees only a shape digest of it plus its own log() output).
//
// It states only what the model cannot know — that a reply runs, what
// persists, what the runtime lacks — and leaves the rest to how it would
// write a script anyway. Every line is a fact about this sandbox, not a
// convention to learn; that is what keeps it to a few lines. Primitive
// listings come from the packs via sandbox.Sandbox.SystemPrompt() and
// are appended under "## Sandbox API".
//
// "Exactly one block and nothing else, then stop" is the one guard the
// short prompt keeps: without it gemini-3.5-flash-lite returned an empty
// reply or wrote the sandbox's output itself (down to an invented
// answer()) in 12 of 24 first turns; with it, 24 of 24 were clean.
//
// The state line says what the runtime does in JavaScript's own terms —
// a fresh scope per script, state round-tripped through JSON.stringify —
// so a model that keeps rows in a local variable, or puts a Map in state,
// knows from the prompt why the next script sees nothing.
//
// The finish sentence stays this short on purpose. A small model calling
// answer() as a progress report is handled by the loop (see
// unreadAnswerNotice), not by wording: every rephrasing measured against
// gemini-3.5-flash-lite was fragile, and with the loop's guard the short
// sentence did as well as the longest one, in fewer scripts.
//
// The runtime line is empirically grounded: goja parses Promise/async
// code and then its continuations silently never run, so the prompt
// rules them out rather than leaving the model to find out.
func TextEmissionSystemPrompt() string {
	return textEmissionPrompt(false)
}

// textEmissionPrompt names the finish the loop wired: print() and exit()
// under Config.UserOutput, answer() otherwise.
func textEmissionPrompt(userOutput bool) string {
	finish := "you call answer(result)"
	if userOutput {
		finish = "you print() the result for the user and call exit()"
	}
	return strings.Replace(textEmissionTemplate, "{{finish}}", finish, 1)
}

const textEmissionTemplate = "You solve tasks by writing JavaScript. Reply with exactly one ```javascript block and nothing else, then stop: it runs in a sandbox, and what it logs comes back to you as the next message. Continue like that until {{finish}}.\n\n" +
	"- Each script runs fresh; only `state` carries over, saved with JSON.stringify. You see its shape, not its values.\n" +
	"- log() what you need to read yourself: counts, names, small samples, not whole datasets.\n" +
	"- The runtime is synchronous: no await or Promises, no Node or browser APIs. The functions declared below are all there is."

// ComposeSystemPrompt builds the full system prompt the loop sends to
// the LLM for a session: the text-emission contract, then the optional
// persona, then the sandbox's primitive documentation. Persona AFTER
// protocol, sandbox API LAST so declarations sit close to the user
// message.
func ComposeSystemPrompt(persona, sandboxAPI string) string {
	return composeSystemPrompt("", false, persona, sandboxAPI)
}

// composeSystemPrompt is ComposeSystemPrompt with Config.Protocol in place
// of the default contract when one is set, and the default contract's
// finish matching Config.UserOutput when not.
func composeSystemPrompt(protocol string, userOutput bool, persona, sandboxAPI string) string {
	out := protocol
	if out == "" {
		out = textEmissionPrompt(userOutput)
	}
	if persona != "" {
		out += "\n\n## Persona\n\n" + persona
	}
	if sandboxAPI != "" {
		out += "\n\n## Sandbox API\n\n" + sandboxAPI
	}
	return out
}
