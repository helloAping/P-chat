package cli

import (
	"os"
	"testing"
)

// withStdin redirects os.Stdin to read from the supplied
// string until it's consumed, then restores the original
// stdin. Used to drive the various /rollback, /history forget
// and /setup confirmation prompts without spinning up a
// subprocess.
//
// The trick: os.Stdin is a package-level *os.File pointing
// at fd 0. We replace it with a pipe whose write end is
// already filled with the answer. The pipe is closed after
// the write so the reader sees EOF once the answer is
// consumed — important for Scanln-style reads that
// otherwise block waiting for more input.
//
// Always paired with t.Cleanup; never call this without it.
//
// Caveat: this is a process-wide mutation, so tests that
// use it cannot run in parallel (t.Parallel()). The go test
// runner runs tests within a package serially by default, so
// that's fine.
func withStdin(t *testing.T, answers string) {
	t.Helper()
	orig := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	if _, err := w.WriteString(answers); err != nil {
		t.Fatalf("write to stdin pipe: %v", err)
	}
	_ = w.Close()
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = orig
		_ = r.Close()
	})
}
