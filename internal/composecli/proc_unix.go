//go:build unix

package composecli

import (
	"os/exec"
	"syscall"
)

// isolate puts the child in its own process group so signals reach every
// process compose spawns, not just the docker CLI.
func isolate(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// signalGroup signals the child's whole process group. ESRCH (already gone)
// is expected and ignored.
func signalGroup(cmd *exec.Cmd, sig syscall.Signal) {
	if cmd.Process == nil {
		return
	}
	_ = syscall.Kill(-cmd.Process.Pid, sig)
}
