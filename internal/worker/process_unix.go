//go:build !windows

package worker

import (
	"os/exec"
	"syscall"
)

func startCommand(cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return cmd.Start()
}

func stopProcessGroup(cmd *exec.Cmd, force bool) {
	signal := syscall.SIGTERM
	if force {
		signal = syscall.SIGKILL
	}
	syscall.Kill(-cmd.Process.Pid, signal)
}
