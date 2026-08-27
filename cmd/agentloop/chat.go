package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"

	"github.com/mind-vm/agentloop"
)

// cmdChat runs turns interactively against one session. Because every
// turn reuses the same loop and the same session id, history carries
// across turns for as long as the process lives — the in-memory stores
// go no further than that, which is what a durable store will change.
//
// Reads one message per line from stdin, so it works at a terminal and
// under a pipe alike. --json makes each turn emit the same NDJSON
// stream `run` does, which is how a long conversation can be driven by
// another program.
func cmdChat(argv []string) int {
	var o options
	fs := flag.NewFlagSet("agentloop chat", flag.ContinueOnError)
	o.bind(fs)
	if err := fs.Parse(argv); err != nil {
		return exitUsage
	}
	if err := o.resolve(); err != nil {
		fmt.Fprintf(os.Stderr, "agentloop: %s\n", err)
		return exitUsage
	}
	if args := fs.Args(); len(args) > 0 {
		fmt.Fprintf(os.Stderr, "agentloop: chat takes no message argument (got %q) — use `agentloop run` for a single turn.\n", strings.Join(args, " "))
		return exitUsage
	}

	configureLogging(&o)

	r := newRenderer(&o, os.Stdout, os.Stderr)
	sess, err := newSession(&o, warnTo(r))
	if err != nil {
		r.fatal(err)
		return exitUsage
	}
	defer sess.close()

	interactive := !o.asJSON && isTerminal(os.Stdin)
	if interactive {
		fmt.Fprintf(os.Stderr, "agentloop chat · session %s\nType a message, or /help for commands. Ctrl-D to leave.\n", sess.id)
	}

	last := exitOK
	in := bufio.NewScanner(os.Stdin)
	// A pasted message or a piped document can be far longer than the
	// scanner's 64KB default line budget.
	in.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	for {
		if interactive {
			fmt.Fprint(os.Stderr, "\n› ")
		}
		if !in.Scan() {
			break
		}
		message := strings.TrimSpace(in.Text())
		if message == "" {
			continue
		}
		if done, code := chatCommand(message, sess, interactive); done {
			if code >= 0 {
				return code
			}
			continue
		}
		last = chatTurn(sess, r, message)
	}
	if err := in.Err(); err != nil {
		r.fatal(fmt.Errorf("cannot read stdin: %w", err))
		return exitError
	}
	if interactive {
		fmt.Fprintln(os.Stderr)
	}
	return last
}

// chatTurn runs one message and returns the exit code that message
// would have produced on its own. A failed turn does not end the
// conversation — the user can rephrase and try again.
func chatTurn(sess *session, r renderer, message string) int {
	// Scoped per turn so Ctrl-C cancels the turn in progress and leaves
	// the conversation running; at the prompt, with no handler
	// installed, Ctrl-C still exits the process.
	ctx, stop := signalContext()
	defer stop()

	res, err := sess.loop.Run(ctx, agentloop.RunRequest{
		SessionID: sess.id,
		Scope:     agentloop.Scope{WorkspaceID: "local"},
		Message:   message,
		Context:   sess.projectCx,
		OnEvent:   r.event,
	})
	if err != nil {
		r.fatal(err)
	}
	if res.Status != "" {
		r.result(res)
	}
	return exitCodeFor(res, err)
}

// chatCommand handles the slash commands. It reports whether the input
// was a command, and an exit code >= 0 when that command ends the
// session.
func chatCommand(message string, sess *session, interactive bool) (handled bool, exit int) {
	if !strings.HasPrefix(message, "/") {
		return false, -1
	}
	switch strings.Fields(message)[0] {
	case "/exit", "/quit":
		return true, exitOK
	case "/new":
		sess.id = uuid.NewString()
		if interactive {
			fmt.Fprintf(os.Stderr, "· started session %s\n", sess.id)
		}
		return true, -1
	case "/session":
		fmt.Fprintf(os.Stderr, "· session %s\n", sess.id)
		return true, -1
	case "/help":
		fmt.Fprint(os.Stderr, "· /new      start a fresh session\n"+
			"· /session  print the current session id\n"+
			"· /exit     leave\n")
		return true, -1
	default:
		fmt.Fprintf(os.Stderr, "· unknown command %q — try /help\n", message)
		return true, -1
	}
}
