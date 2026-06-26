//go:build windows

package serverproc

import (
	"os/exec"
	"syscall"
)

// CREATE_NO_WINDOW (Win32 process creation flag). When set, the
// child is created without a console window. Without this flag,
// Go's os/exec on Windows allocates a fresh console for the child
// even when the parent is a GUI-subsystem (WAILS_GUI) binary —
// which causes a stray black window to pop up on every launch of
// pchat-server.exe.
const createNoWindow = 0x08000000

// SetSysProcAttrNewPG is a no-op on Windows. Go's exec.Cmd already
// detaches non-Wait'd children: the child runs in its own process
// and is not signalled when the parent exits, which is what we want
// for the browser-launcher helper.
func SetSysProcAttrNewPG(_ *exec.Cmd) {}

// SetSysProcAttrHiddenWindow sets CREATE_NO_WINDOW on the child so
// no console window pops up when pchat-server.exe is spawned from a
// GUI-subsystem parent. Stdout/Stderr still go to whatever the
// caller wired up; this only suppresses the visible window.
//
// We use the raw CREATE_NO_WINDOW constant instead of
// syscall.SysProcAttr{HideWindow: true} because the latter is
// specific to the windows-specific SysProcAttr struct and the
// portable syscall.SysProcAttr doesn't expose it. Going through
// the bit flag keeps the code identical across Go versions and
// works on all supported Windows versions (Win7+).
func SetSysProcAttrHiddenWindow(cmd *exec.Cmd) {
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
