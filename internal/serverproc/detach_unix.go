//go:build !windows

package serverproc

import (
	"os/exec"
	"syscall"
)

// SetSysProcAttrNewPG puts cmd in a new process group so signals to
// the parent (Ctrl+C, SIGTERM) don't propagate. Used for detached
// helpers like opening the default browser - we don't want killing
// pchat to close the user's browser.
func SetSysProcAttrNewPG(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.Setpgid = true
}

// SetSysProcAttrHiddenWindow is a no-op on non-Windows. Windows
// GUI apps need CREATE_NO_WINDOW to suppress the popup console
// for child processes; Unix processes don't have a console window
// to suppress.
func SetSysProcAttrHiddenWindow(_ *exec.Cmd) {}
