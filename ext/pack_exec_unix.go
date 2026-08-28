//go:build unix

package ext

import (
	"os/exec"
	"syscall"
)

// isolateProcess puts the child in its own process group.
//
// Without this, a timeout kills the command and nothing else: the
// compiler, test binary, or helper it spawned keeps running, still
// holding the pipes this package is reading. The run appears to hang
// after the thing that hung was already killed.
func isolateProcess(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// terminateProcess kills the child's whole process group. A negative
// pid addresses the group, which is why isolateProcess had to create
// one — signalling the group of a child that shares ours would take
// down the agent itself.
func terminateProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		// The group may already be gone, or never formed if the exec
		// failed. Falling back to the process keeps the timeout honest.
		return cmd.Process.Kill()
	}
	return nil
}
