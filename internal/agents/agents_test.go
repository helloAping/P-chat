package agents

import (
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadGlobal_Missing(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	if got := LoadGlobal(); got != "" {
		t.Errorf("missing file should give empty string, got %q", got)
	}
}

func TestLoadProject_Missing(t *testing.T) {
	// ProjectAgents uses cwd, not env. Save and restore.
	tmp := t.TempDir()
	oldCwd, _ := os.Getwd()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })

	if got := LoadProject(); got != "" {
		t.Errorf("missing file should give empty string, got %q", got)
	}
}

func TestLoadGlobal_Present(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	writeFile(t, filepath.Join(tmp, ".p-chat", "AGENTS.md"), "Global rules\n")

	if got := LoadGlobal(); got != "Global rules" {
		t.Errorf("got %q, want %q", got, "Global rules")
	}
}

func TestLoadProject_Present(t *testing.T) {
	tmp := t.TempDir()
	oldCwd, _ := os.Getwd()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })
	// ProjectAgents is ./AGENTS.md (project root), not under .p-chat/
	writeFile(t, filepath.Join(tmp, "AGENTS.md"), "Project rules\n")

	if got := LoadProject(); got != "Project rules" {
		t.Errorf("got %q, want %q", got, "Project rules")
	}
}

func TestLoadAll_OnlyGlobal(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)
	writeFile(t, filepath.Join(tmp, ".p-chat", "AGENTS.md"), "Global")

	got := LoadAll()
	if got == "" {
		t.Fatal("expected non-empty LoadAll")
	}
	if !contains(got, "Global") {
		t.Errorf("LoadAll() should contain 'Global', got %q", got)
	}
	// Section header should always be present.
	if !contains(got, "## Agent Instructions") {
		t.Errorf("LoadAll() should contain '## Agent Instructions' header, got %q", got)
	}
}

func TestLoadAll_BothPresent(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)
	oldCwd, _ := os.Getwd()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })

	writeFile(t, filepath.Join(tmp, ".p-chat", "AGENTS.md"), "Global rules")
	writeFile(t, filepath.Join(tmp, "AGENTS.md"), "Project rules")

	got := LoadAll()
	if !contains(got, "Global rules") {
		t.Errorf("LoadAll() should contain 'Global rules', got %q", got)
	}
	if !contains(got, "Project rules") {
		t.Errorf("LoadAll() should contain 'Project rules', got %q", got)
	}
}

func TestLoadAll_NeitherPresent(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)
	oldCwd, _ := os.Getwd()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })

	got := LoadAll()
	if got == "" {
		t.Fatal("LoadAll should return at least the section header even with no content")
	}
	if !contains(got, "(none)") {
		t.Errorf("expected '(none)' marker in empty config, got %q", got)
	}
}

func TestLoadAll_StableAcrossCalls(t *testing.T) {
	// Two consecutive calls with the same content should return
	// byte-identical strings (the prefix-cache invariant).
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)
	writeFile(t, filepath.Join(tmp, ".p-chat", "AGENTS.md"), "Rules")

	if LoadAll() != LoadAll() {
		t.Error("two consecutive LoadAll() calls should return the same string")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
