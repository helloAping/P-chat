package version

import (
	"os"
	"path/filepath"
	"testing"
)

func TestString_NotEmpty(t *testing.T) {
	v := String()
	if v == "" {
		t.Error("version string should not be empty")
	}
	t.Logf("version: %s", v)
}

func TestFullString_NotEmpty(t *testing.T) {
	v := FullString()
	if v == "" {
		t.Error("full version string should not be empty")
	}
	t.Logf("full: %s", v)
}

func TestReleaseString_PrefersInjectedReleaseVersion(t *testing.T) {
	oldVersion := Version
	oldGitCommit := GitCommit
	Version = "1.2.3"
	GitCommit = "abc1234"
	t.Cleanup(func() {
		Version = oldVersion
		GitCommit = oldGitCommit
	})

	if got := ReleaseString(); got != "1.2.3" {
		t.Fatalf("ReleaseString() = %q, want 1.2.3", got)
	}
}

func TestReleaseString_UsesVersionFileForDevBuild(t *testing.T) {
	oldVersion := Version
	oldGitCommit := GitCommit
	Version = ""
	GitCommit = "a23e80a"
	t.Cleanup(func() {
		Version = oldVersion
		GitCommit = oldGitCommit
	})

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "VERSION"), []byte("1.0.11\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	if got := String(); got != "dev-a23e80a" {
		t.Fatalf("String() = %q, want dev-a23e80a", got)
	}
	if got := ReleaseString(); got != "1.0.11" {
		t.Fatalf("ReleaseString() = %q, want 1.0.11", got)
	}
}

func TestGitCommit_ReturnsHash(t *testing.T) {
	h := resolveGitHash(".")
	if h == "" {
		t.Skip("not in git repo — skipping hash test")
	}
	if len(h) < 6 {
		t.Errorf("hash too short: %q", h)
	}
	t.Logf("git hash: %s", h)
}
