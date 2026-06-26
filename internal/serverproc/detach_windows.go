//go:build windows

package serverproc

import "os/exec"

// setSysProcAttrNewPG is a no-op on Windows. Go's exec.Cmd already
// detaches non-Wait'd children: the child runs in its own process
// and is not signalled when the parent exits, which is what we want
// for the browser-launcher helper.
func setSysProcAttrNewPG(_ *exec.Cmd) {}
