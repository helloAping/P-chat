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

func TestLoadAll_BothPresent_ProjectWins(t *testing.T) {
	// 2026-07: under the OR policy, when both global and
	// project-level files exist, ONLY the project-level
	// content is loaded (project root AGENTS.md takes
	// priority over global). This test replaces the
	// pre-OR TestLoadAll_BothPresent which asserted that
	// both sections appeared — that parallel-load
	// behaviour was the bug the 2026-07 spec removes.
	//
	// Note: LoadAll() goes through LoadAllWithRoot("")
	// which means the project-level slots are SKIPPED
	// (the no-project-pinned path → global only). So we
	// call LoadAllWithRoot(tmp) directly to test the
	// project-aware path.
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)
	writeFile(t, filepath.Join(tmp, ".p-chat", "AGENTS.md"), "Global rules")
	writeFile(t, filepath.Join(tmp, "AGENTS.md"), "Project rules")

	got := LoadAllWithRoot(tmp)
	if !contains(got, "Project rules") {
		t.Errorf("LoadAllWithRoot should contain 'Project rules' under OR policy, got %q", got)
	}
	if contains(got, "Global rules") {
		t.Errorf("LoadAllWithRoot should NOT contain 'Global rules' under OR policy, got %q", got)
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

// --- 2026-07 OR loader tests ---------------------------------
//
// The previous parallel loader returned BOTH global and
// project instructions as separate sections. The 2026-07
// spec switches to an OR policy (project-level priority;
// fallback to global) because parallel loading produces
// conflicting instructions when the two files disagree.

// TestLoadAllWithRoot_ProjectRootTakesPrecedence is the
// happy path: <root>/AGENTS.md exists, both other slots
// exist too — the root AGENTS.md wins, with header
// "### Project (root)".
func TestLoadAllWithRoot_ProjectRootTakesPrecedence(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "AGENTS.md"), "Root instructions")
	writeFile(t, filepath.Join(root, ".p-chat", "AGENTS.md"), "PChat instructions")
	writeFile(t, filepath.Join(tmp, ".p-chat", "AGENTS.md"), "Global instructions")

	got := LoadAllWithRoot(root)
	if !contains(got, "Root instructions") {
		t.Errorf("expected 'Root instructions' in output, got %q", got)
	}
	if contains(got, "PChat instructions") {
		t.Errorf("should NOT contain 'PChat instructions' (root AGENTS.md takes priority), got %q", got)
	}
	if contains(got, "Global instructions") {
		t.Errorf("should NOT contain 'Global instructions' (project-level wins), got %q", got)
	}
	if !contains(got, "### Project (root)") {
		t.Errorf("expected '### Project (root)' header, got %q", got)
	}
}

// TestLoadAllWithRoot_ProjectPChatFallback: <root>/AGENTS.md
// missing, <root>/.p-chat/AGENTS.md exists — it wins with
// header "### Project (.p-chat)".
func TestLoadAllWithRoot_ProjectPChatFallback(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".p-chat", "AGENTS.md"), "PChat instructions")
	writeFile(t, filepath.Join(tmp, ".p-chat", "AGENTS.md"), "Global instructions")

	got := LoadAllWithRoot(root)
	if !contains(got, "PChat instructions") {
		t.Errorf("expected 'PChat instructions' in output, got %q", got)
	}
	if contains(got, "Global instructions") {
		t.Errorf("should NOT contain 'Global instructions' (project-level wins), got %q", got)
	}
	if !contains(got, "### Project (.p-chat)") {
		t.Errorf("expected '### Project (.p-chat)' header, got %q", got)
	}
}

// TestLoadAllWithRoot_GlobalFallback: no project-level
// files — global wins with header "### Global".
func TestLoadAllWithRoot_GlobalFallback(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)
	root := t.TempDir()
	// No project-level files at all.
	writeFile(t, filepath.Join(tmp, ".p-chat", "AGENTS.md"), "Global instructions")

	got := LoadAllWithRoot(root)
	if !contains(got, "Global instructions") {
		t.Errorf("expected 'Global instructions' in output, got %q", got)
	}
	if !contains(got, "### Global") {
		t.Errorf("expected '### Global' header, got %q", got)
	}
}

// TestLoadAllWithRoot_NoFiles: nothing anywhere —
// output is the (none) placeholder, byte-stable.
func TestLoadAllWithRoot_NoFiles(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)
	root := t.TempDir()

	got := LoadAllWithRoot(root)
	if !contains(got, "(none)") {
		t.Errorf("expected '(none)' placeholder, got %q", got)
	}
	if !contains(got, "## Agent Instructions") {
		t.Errorf("expected section header, got %q", got)
	}
}

// TestLoadAllWithRoot_ProjectRootBeatsPChat locks the
// project-level intra-slot precedence: when BOTH
// <root>/AGENTS.md and <root>/.p-chat/AGENTS.md exist,
// the root one wins. This is the spec's explicit
// "项目根目录的 AGENTS.md 是默认路径" rule — the root
// location is the primary project slot, the .p-chat/
// location is the secondary fallback.
func TestLoadAllWithRoot_ProjectRootBeatsPChat(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "AGENTS.md"), "ROOT WINS")
	writeFile(t, filepath.Join(root, ".p-chat", "AGENTS.md"), "p-chat should be ignored")

	got := LoadAllWithRoot(root)
	if !contains(got, "ROOT WINS") {
		t.Errorf("expected 'ROOT WINS' (root AGENTS.md), got %q", got)
	}
	if contains(got, "p-chat should be ignored") {
		t.Errorf("root AGENTS.md should beat .p-chat/AGENTS.md, got %q", got)
	}
	if !contains(got, "### Project (root)") {
		t.Errorf("expected '### Project (root)' header, got %q", got)
	}
}

// TestLoadAllWithRoot_NoProjectGlobalOnly: no project root
// (root == "") and a global AGENTS.md present — the
// global is used. Mirrors the "no project pinned"
// workflow where the user is in global mode.
func TestLoadAllWithRoot_NoProjectGlobalOnly(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)
	writeFile(t, filepath.Join(tmp, ".p-chat", "AGENTS.md"), "Global only")

	got := LoadAllWithRoot("")
	if !contains(got, "Global only") {
		t.Errorf("expected 'Global only' for empty project root, got %q", got)
	}
	if !contains(got, "### Global") {
		t.Errorf("expected '### Global' header, got %q", got)
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
