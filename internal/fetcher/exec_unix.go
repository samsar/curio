//go:build unix

package fetcher

import (
	"os/exec"
	"syscall"
)

// isolateProcessGroup starts cmd in a process group of its own and makes
// cancellation SIGKILL that whole group, so helpers the tool spawned die
// with it.
func isolateProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
}
