package main

import (
	"os"
	"path/filepath"
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
