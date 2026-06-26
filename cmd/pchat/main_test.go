package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveServerBinary_ExplicitFlag(t *testing.T) {
	dir := t.TempDir()
	want := filepath.Join(dir, "pchat-server.exe")
	if err := os.WriteFile(want, []byte("dummy"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := resolveServerBinary(want)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveServerBinary_FlagMissing(t *testing.T) {
	_, err := resolveServerBinary(filepath.Join(t.TempDir(), "no-such.exe"))
	if err == nil {
		t.Error("expected error for missing --bin path")
	}
}

func TestResolveServerBinary_SiblingOfExecutable(t *testing.T) {
	// We can't reliably control the *executable*'s directory from
	// inside a test, so this test falls back to the PATH search
	// path (which is the final branch in resolveServerBinary). The
	// sibling branch is exercised by the real `pchat web` run; we
	// just want to confirm that when nothing is found, we get a
	// useful error and not a crash.
	dir := t.TempDir()
	orig, hadPath := os.LookupEnv("PATH")
	t.Setenv("PATH", dir)
	if hadPath {
		defer os.Setenv("PATH", orig)
	}
	_, err := resolveServerBinary("")
	if err == nil {
		t.Skip("a pchat-server binary is on PATH; nothing to assert here")
	}
	if err.Error() == "" {
		t.Error("error should have a message")
	}
}
