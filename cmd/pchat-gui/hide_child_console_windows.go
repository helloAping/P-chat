//go:build windows

// hide_child_console_windows.go: Windows-specific implementation
// of hideChildConsole.
//
// Why a separate file: Go's syscall.SysProcAttr is platform-
// specific. On Windows it has a `CreationFlags` field; on
// Linux/macOS the field doesn't exist at all (it's a Windows
// concept — POSIX processes don't have "creation flags"). The
// non-Windows file (hide_child_console_other.go) is a no-op
// stub; the caller doesn't need to branch on runtime.GOOS
// because the function is always callable.
//
// The whole `os/exec` round-trip for the child server runs on
// all platforms, but only Windows needs the CREATE_NO_WINDOW
// trick — Linux/macOS GUI binaries (and Wails itself) don't
// have a stray console window to suppress.
package main

import (
	"os/exec"
	"syscall"
)

// CREATE_NO_WINDOW (0x08000000) is documented at
// https://learn.microsoft.com/en-us/windows/win32/procthread/process-creation-flags
// We set it as a CreationFlag on the child process so the
// WINDOWS_GUI-subsystem pchat-gui parent doesn't pop up a
// console for pchat-server.exe.
const createNoWindow = 0x08000000

func hideChildConsole(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
