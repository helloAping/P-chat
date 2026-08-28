//go:build windows

package main

import (
	"os/exec"
	"testing"
)

func TestConfigureInstallCommandDoesNotHideGuiWindows(t *testing.T) {
	cmd := exec.Command("powershell", "-NoProfile")
	configureInstallCommand(cmd)

	if cmd.SysProcAttr == nil {
		t.Fatal("SysProcAttr was not configured")
	}
	if cmd.SysProcAttr.HideWindow {
		t.Fatal("HideWindow hides the WinForms installer dialog")
	}
	if cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Fatalf("CreationFlags missing CREATE_NO_WINDOW: %#x", cmd.SysProcAttr.CreationFlags)
	}
}
