//go:build !windows

package main

import "os/exec"

func hideChildConsole(cmd *exec.Cmd) {}
