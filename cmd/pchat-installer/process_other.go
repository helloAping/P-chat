//go:build !windows

package main

import "os/exec"

func configureInstallCommand(cmd *exec.Cmd) {}
