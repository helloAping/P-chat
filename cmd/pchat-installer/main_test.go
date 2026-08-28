package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestCopyFileAddsUTF8BOMToPowerShellScripts(t *testing.T) {
	dst := filepath.Join(t.TempDir(), "install.ps1")
	if err := copyFile("assets/install.ps1", dst); err != nil {
		t.Fatalf("copy install script: %v", err)
	}

	data, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("read copied install script: %v", err)
	}
	if !hasUTF8BOM(data) {
		t.Fatalf("copied install script missing UTF-8 BOM: first bytes=% X", data[:min(len(data), 4)])
	}
}

func TestDefaultInstallPowerShellArgsOpenGuiWithSTA(t *testing.T) {
	args := installPowerShellArgs(`C:\Temp\install.ps1`, nil)
	if !slices.Contains(args, "-STA") {
		t.Fatalf("PowerShell args missing -STA: %v", args)
	}
	if !slices.Contains(args, "-Gui") {
		t.Fatalf("default PowerShell args should open the GUI installer: %v", args)
	}
}

func TestExplicitInstallPowerShellArgsPassThrough(t *testing.T) {
	args := installPowerShellArgs(`C:\Temp\install.ps1`, []string{"-InstallDir", `D:\P-Chat`})
	if slices.Contains(args, "-Gui") {
		t.Fatalf("explicit PowerShell args should not force GUI mode: %v", args)
	}
	if !slices.Contains(args, "-InstallDir") {
		t.Fatalf("explicit PowerShell args were not passed through: %v", args)
	}
}
