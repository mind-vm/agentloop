package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/mind-vm/agentloop"
)

// cmdRun executes exactly one turn and exits with a code describing how
// it ended.
func cmdRun(argv []string) int {
	var o options
	fs := flag.NewFlagSet("agentloop run", flag.ContinueOnError)
	o.bind(fs)
	if err := fs.Parse(argv); err != nil {
		return exitUsage
	}
	if err := o.resolve(); err != nil {
		fmt.Fprintf(os.Stderr, "agentloop: %s\n", err)
		return exitUsage
	}

	configureLogging(&o)

	message, err := resolveMessage(fs.Args(), os.Stdin)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agentloop: %s\n\nRun `agentloop help` for usage.\n", err)
		return exitUsage
	}

	r := newRenderer(&o, os.Stdout, os.Stderr)
	sess, err := newSession(&o, warnTo(r))
	if err != nil {
		r.fatal(err)
		// A missing key or an unusable endpoint is an environment
		// problem, not a failed agent run.
		return exitUsage
	}

	ctx, stop := signalContext()
	defer stop()

	res, runErr := sess.loop.Run(ctx, agentloop.RunRequest{
		SessionID: sess.id,
		Scope:     agentloop.Scope{WorkspaceID: "local"},
		Message:   message,
		Context:   sess.projectCx,
		OnEvent:   r.event,
	})
	if runErr != nil {
		r.fatal(runErr)
	}
	// A run that failed early enough carries no status at all; there is
	// nothing truthful to report about it beyond the error itself.
	if res.Status != "" {
		r.result(res)
	}
	return exitCodeFor(res, runErr)
}

// resolveMessage takes the message from the positional arguments, or
// from stdin when none were given and stdin is not a terminal. That is
// what makes `echo "..." | agentloop run` work while an argument-less
// invocation at a prompt still explains itself instead of hanging on a
// read that will never end.
func resolveMessage(args []string, stdin *os.File) (string, error) {
	if joined := strings.TrimSpace(strings.Join(args, " ")); joined != "" {
		return joined, nil
	}
	if stdin != nil && !isTerminal(stdin) {
		piped, err := io.ReadAll(stdin)
		if err != nil {
			return "", fmt.Errorf("cannot read the message from stdin: %w", err)
		}
		if msg := strings.TrimSpace(string(piped)); msg != "" {
			return msg, nil
		}
	}
	return "", errors.New("no message given")
}

// warnTo adapts a renderer into the setup-warning callback newSession
// expects, so a partial AGENTS.md or SKILL.md load is reported through
// the same channel as everything else the run emits.
func warnTo(r renderer) func(string) {
	return func(msg string) {
		r.event(agentloop.RunEvent{Type: "warning", Content: msg})
	}
}

// signalContext cancels the run on the first SIGINT or SIGTERM. The
// loop threads the context into the LLM call and the sandbox, so Ctrl-C
// ends the turn rather than orphaning it.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
