//go:build !unix

package ext

import "os/exec"

// isolateProcess is a no-op where process groups are not available.
func isolateProcess(*exec.Cmd) {}

// terminateProcess kills just the child. Descendants it spawned may
// outlive the timeout on this platform.
func terminateProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
