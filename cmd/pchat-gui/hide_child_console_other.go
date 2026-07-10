//go:build !windows

// hide_child_console_other.go: non-Windows stub for
// hideChildConsole.
//
// The Windows version (hide_child_console_windows.go) sets
// CREATE_NO_WINDOW so the WINDOWS_GUI-subsystem pchat-gui
// doesn't pop up a console for pchat-server.exe. That flag
// is a Windows concept — Linux/macOS GUI processes don't
// have a stray console window to suppress, so this stub is
// a no-op. The function is callable from the shared
// `spawnServer` call site without a runtime.GOOS branch.
package main

import "os/exec"

func hideChildConsole(cmd *exec.Cmd) {
	_ = cmd
}
