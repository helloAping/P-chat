//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"syscall"
)

const createNoWindow = 0x08000000

func configureInstallCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
}

func runPlatformInstall(tmp string, args []string) error {
	ps1 := filepath.Join(tmp, "install.ps1")
	cmd := exec.Command("powershell", installPowerShellArgs(ps1, args)...)
	cmd.Dir = tmp
	configureInstallCommand(cmd)
	output, err := cmd.CombinedOutput()
	if err != nil {
		if len(output) > 0 {
			return fmt.Errorf("%w\n\n%s", err, string(output))
		}
		return err
	}
	return nil
}

func showInstallerNotice(string, string) {}
