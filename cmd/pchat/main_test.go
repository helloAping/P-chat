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

// TestWebCmd_Registered makes sure the `pchat web` subcommand is
// wired into rootCmd. We don't try to actually start a server in
// the test — signal handling and child process lifecycle are
// covered by the manual smoke test (`pchat web` + Ctrl+C).
func TestWebCmd_Registered(t *testing.T) {
	found := false
	for _, c := range rootCmd.Commands() {
		if c.Name() == "web" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("`web` subcommand is not registered on rootCmd")
	}
}

// TestWebCmd_DefaultPort confirms the --port flag defaults to 0
// (auto-pick) and --no-open defaults to false. This is the contract
// the help text relies on.
func TestWebCmd_DefaultPort(t *testing.T) {
	f := webCmd.Flags().Lookup("port")
	if f == nil {
		t.Fatal("`--port` flag not defined on webCmd")
	}
	if f.DefValue != "0" {
		t.Errorf("--port default = %q, want \"0\" (auto-pick)", f.DefValue)
	}
	noOpen := webCmd.Flags().Lookup("no-open")
	if noOpen == nil {
		t.Fatal("`--no-open` flag not defined on webCmd")
	}
	if noOpen.DefValue != "false" {
		t.Errorf("--no-open default = %q, want \"false\"", noOpen.DefValue)
	}
}

// TestResolveWebDir_PicksExisting confirms the helper picks a real
// `web/` folder when one exists. We can't assert the exact path
// because it depends on the test process's executable location,
// but we can confirm the result (if non-empty) is a real dir.
//
// In a real `go test ./...` run the test binary lives in
// $GOCACHE, so none of the candidates (sibling-of-exe, ../web
// relative to exe, CWD/web) will match unless the test is run
// from the repo root. We don't want a flaky test, so we skip
// when no candidate resolves.
func TestResolveWebDir_PicksExisting(t *testing.T) {
	got := resolveWebDir()
	if got == "" {
		t.Skip("no web/ folder reachable from the test binary; " +
			"the happy path is verified by the manual `pchat web` smoke test")
	}
	if fi, err := os.Stat(got); err != nil || !fi.IsDir() {
		t.Errorf("resolveWebDir() = %q, but it's not a real directory (err=%v)", got, err)
	}
}
