//go:build !windows

package tool

import (
	"os/exec"
	"syscall"
	"time"
)

func setProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func stopProcessTree(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err != nil {
		return cmd.Process.Kill()
	}
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	time.Sleep(800 * time.Millisecond)
	return syscall.Kill(-pgid, syscall.SIGKILL)
}
