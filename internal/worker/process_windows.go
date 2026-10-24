package worker

import (
	"errors"
	"os/exec"
)

// Windows distributes the client. Simulation processes run in Linux Pods.
func startCommand(cmd *exec.Cmd) error {
	return errors.New("worker processes require a Linux container")
}

func stopProcessGroup(cmd *exec.Cmd, force bool) {
	if cmd.Process != nil {
		cmd.Process.Kill()
	}
}
