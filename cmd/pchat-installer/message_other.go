//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
)

func showInstallerError(title string, message string) {
	showInstallerDialog(title, message, true)
}

func showInstallerNotice(title string, message string) {
	showInstallerDialog(title, message, false)
}

func showInstallerDialog(title string, message string, isError bool) {
	switch runtime.GOOS {
	case "darwin":
		icon := "note"
		if isError {
			icon = "caution"
		}
		script := fmt.Sprintf("display dialog %q with title %q buttons {\"OK\"} default button 1 with icon %s", message, title, icon)
		if exec.Command("osascript", "-e", script).Run() == nil {
			return
		}
	case "linux":
		kind := "--info"
		if isError {
			kind = "--error"
		}
		if _, err := exec.LookPath("zenity"); err == nil && exec.Command("zenity", kind, "--title", title, "--text", message).Run() == nil {
			return
		}
	}
	stream := os.Stdout
	if isError {
		stream = os.Stderr
	}
	fmt.Fprintf(stream, "%s\n%s\n", title, message)
}
