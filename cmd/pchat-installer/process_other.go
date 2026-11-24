//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func configureInstallCommand(cmd *exec.Cmd) {}

func runPlatformInstall(tmp string, args []string) error {
	script := filepath.Join(tmp, "install.sh")
	cmd := exec.Command(script, args...)
	cmd.Dir = tmp
	output, err := cmd.CombinedOutput()
	if err != nil {
		if len(output) > 0 {
			return fmt.Errorf("%w\n\n%s", err, string(output))
		}
		return err
	}
	if len(output) > 0 {
		fmt.Fprintln(os.Stdout, string(output))
	}
	return nil
}
