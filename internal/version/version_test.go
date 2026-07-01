package version

import "testing"

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
