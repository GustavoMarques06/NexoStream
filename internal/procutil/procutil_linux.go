//go:build linux

package procutil

import (
	"os/exec"
	"syscall"
)

// SetAttr makes the kernel kill ffmpeg if the agent dies: the agent is
// in the data path (it feeds stdin), so an orphan pipeline is useless.
func SetAttr(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGKILL, Setpgid: true}
}

func AfterStart(*exec.Cmd) {}
