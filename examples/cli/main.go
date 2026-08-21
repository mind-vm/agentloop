// Command cli is a minimal, runnable demo of agentloop: in-memory
// session/step stores, the default capability bundle (require, http,
// fetch, markdown, ai), and one Run call against an OpenAI-wire-compatible
// LLM.
//
// Usage:
//
//	export OPENAI_API_KEY=sk-...
//	# OpenAI is the default; point OPENAI_BASE_URL elsewhere for
//	# OpenRouter / EdenAI / a local gateway — see llm.ConfigFromEnv.
//	go run ./examples/cli "What's 12 * 7? Answer as a single number."
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/jryannel/agentloop"
	"github.com/jryannel/agentloop/llm"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: cli <message>")
		os.Exit(1)
	}
	message := strings.Join(os.Args[1:], " ")

	client, err := llm.NewOpenAI(llm.ConfigFromEnv())
	if err != nil {
		fmt.Fprintln(os.Stderr, "agentloop: "+err.Error())
		os.Exit(1)
	}

	caps := agentloop.DefaultCapabilities(client, "")
	l := agentloop.New(agentloop.Config{
		LLM:            client,
		Sessions:       newMemSessionStore(),
		Steps:          newMemStepStore(),
		SandboxBuilder: &agentloop.DefaultSandboxBuilder{Capabilities: caps},
	})

	result, err := l.Run(context.Background(), agentloop.RunRequest{
		SessionID: "cli-session",
		Scope:     agentloop.Scope{WorkspaceID: "local"},
		Message:   message,
		OnEvent:   printEvent,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "\nagentloop: "+err.Error())
		os.Exit(1)
	}

	fmt.Printf("\n--- result (%s, %d steps, %d+%d tokens) ---\n%s\n",
		result.Status, result.Steps, result.Tokens.Prompt, result.Tokens.Completion, result.FinalText)
}

// printEvent renders each RunEvent to stderr as it streams in, so you
// can watch the loop think: the JS it emits, execution results, and the
// final response as it's generated.
func printEvent(evt agentloop.RunEvent) {
	switch evt.Type {
	case "execute_js":
		fmt.Fprintf(os.Stderr, "\n--- run() ---\n%s\n", evt.Content)
	case "execute_js_result":
		fmt.Fprintf(os.Stderr, "--- result ---\n%s\n", evt.Content)
	case "response_chunk":
		fmt.Fprint(os.Stderr, evt.Content)
	case "error", "warning":
		fmt.Fprintf(os.Stderr, "\n[%s] %s\n", evt.Type, evt.Content)
	}
}
