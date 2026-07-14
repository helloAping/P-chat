package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFindServerBinary_FindsSibling(t *testing.T) {
	dir := t.TempDir()
	// Create a fake pchat-server.exe beside a fake "self" path.
	// findServerBinary() uses os.Executable() so we can only test the
	// "PATH" branch deterministically; the sibling branch is exercised
	// by the smoke test that actually launches the real binary.
	fake := filepath.Join(dir, "pchat-server.exe")
	if err := os.WriteFile(fake, []byte("MZ"), 0644); err != nil {
		t.Fatal(err)
	}
	orig := os.Getenv("PATH")
	t.Setenv("PATH", dir+string(os.PathListSeparator)+orig)

	got, err := findServerBinary()
	if err != nil {
		t.Fatalf("findServerBinary: %v", err)
	}
	if !strings.EqualFold(filepath.Base(got), "pchat-server.exe") {
		t.Fatalf("expected pchat-server.exe, got %q", got)
	}
}

func TestFindServerBinary_NotFound(t *testing.T) {
	// findServerBinary walks CWD up to 5 levels looking for
	// bin/pchat-server.exe (dev-mode fallback). If the test binary
	// runs from inside the project tree, the walk will find it and
	// defeat the "not found" probe. Chdir to a temp dir so the walk
	// has no chance of finding a matching file.
	cwd, _ := os.Getwd()
	os.Chdir(t.TempDir())
	defer os.Chdir(cwd)

	t.Setenv("PATH", t.TempDir())
	if _, err := findServerBinary(); err == nil {
		t.Fatal("expected error when pchat-server.exe is not in PATH or beside the binary")
	}
}

func TestPickFreePort_Unique(t *testing.T) {
	p1, err := pickFreePort()
	if err != nil {
		t.Fatal(err)
	}
	p2, err := pickFreePort()
	if err != nil {
		t.Fatal(err)
	}
	if p1 == 0 || p2 == 0 {
		t.Fatalf("ports should be nonzero, got %d and %d", p1, p2)
	}
	// The OS may reuse the same port if the previous one was closed,
	// so we only assert both are valid.
}

func TestPickConfigPath_PrefersJSON(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home)
	t.Setenv("HOME", home)

	// Neither file: should return "" (fresh install — pchat-server
	// uses built-in defaults; see pickConfigPath godoc).
	if p := pickConfigPath(); p != "" {
		t.Fatalf("expected empty string for fresh install, got %q", p)
	}

	// Both files: prefer json.
	if err := os.MkdirAll(filepath.Join(home, ".p-chat"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".p-chat", "config.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".p-chat", "config.yaml"), []byte("a: b"), 0644); err != nil {
		t.Fatal(err)
	}
	if p := pickConfigPath(); !strings.HasSuffix(p, "config.json") {
		t.Fatalf("expected config.json, got %q", p)
	}

	// Only yaml: should fall back to yaml.
	os.Remove(filepath.Join(home, ".p-chat", "config.json"))
	if p := pickConfigPath(); !strings.HasSuffix(p, "config.yaml") {
		t.Fatalf("expected config.yaml fallback, got %q", p)
	}
}
