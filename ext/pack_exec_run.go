package ext

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
)

// run executes the command and returns the JS-facing result.
//
// A command that completes is a RESULT, whatever its exit status: a
// failing build is the information the agent asked for, and turning it
// into a thrown error would push scripts into try/catch around the one
// call whose failure they most need to read. Only a command that could
// not be started at all throws.
func (x *execer) run(argv []string, dir string, timeout time.Duration, input string, extraEnv map[string]string) map[string]any {
	// Derived from the run's own context, so cancelling the run — a
	// Ctrl-C, or the loop's overall timeout — takes the child with it
	// rather than leaving it holding the pipes.
	ctx, cancel := context.WithTimeout(x.ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = dir
	cmd.Env = x.buildEnv(extraEnv)

	// Kill the whole process group, not just the child. A build tool
	// that spawned a compiler would otherwise survive the timeout and
	// keep the pipes open, so the run appears to hang after the thing
	// that hung was already killed.
	isolateProcess(cmd)
	cmd.Cancel = func() error { return terminateProcess(cmd) }
	// And bound the wait for I/O to drain afterwards, so a grandchild
	// still holding stdout cannot stall the turn indefinitely.
	cmd.WaitDelay = 2 * time.Second

	stdout := &cappedBuffer{limit: x.opts.maxOutput()}
	stderr := &cappedBuffer{limit: x.opts.maxOutput()}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}

	err := cmd.Run()
	timedOut := errors.Is(ctx.Err(), context.DeadlineExceeded)

	// ProcessState is nil when the command never started, so the exit
	// code has to be read defensively rather than off the happy path.
	code := -1
	if cmd.ProcessState != nil {
		code = cmd.ProcessState.ExitCode()
	}
	if err != nil {
		// Nothing ran: a missing binary, an unexecutable file, a cwd
		// that vanished. That is a mistake to fix, not an outcome to
		// report, so it throws. A non-zero exit and a timeout both DID
		// run, and are returned.
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) && !timedOut {
			x.throw("exec: %v", err)
		}
	}

	return map[string]any{
		"stdout":   stdout.String(),
		"stderr":   stderr.String(),
		"code":     code,
		"timedOut": timedOut,
	}
}

// cappedBuffer keeps the first limit bytes written to it and counts the
// rest.
//
// It never returns a short write or an error. A command whose output
// pipe stops being drained blocks forever on its next write, so the
// choice is not between reading and not reading — it is between reading
// and hanging. Everything past the cap is read and thrown away.
type cappedBuffer struct {
	buf     bytes.Buffer
	limit   int
	dropped int
}

func (c *cappedBuffer) Write(p []byte) (int, error) {
	room := c.limit - c.buf.Len()
	if room > 0 {
		if room > len(p) {
			room = len(p)
		}
		c.buf.Write(p[:room])
	}
	if extra := len(p) - room; extra > 0 {
		c.dropped += extra
	}
	return len(p), nil
}

// String returns what was kept, with a marker when anything was not.
// The marker matters more than the bytes: an agent reading truncated
// output as complete will draw a confident wrong conclusion from it.
func (c *cappedBuffer) String() string {
	if c.dropped == 0 {
		return c.buf.String()
	}
	return fmt.Sprintf("%s\n[truncated: %d further bytes not shown]\n", c.buf.String(), c.dropped)
}

var _ io.Writer = (*cappedBuffer)(nil)
