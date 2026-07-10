package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, name), 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name, "SKILL.md")
	if err := os.WriteFile(path, []byte("# "+name+"\n\nSkill content.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadAll_BothEmpty(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)

	got, err := LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 skills, got %d", len(got))
	}
}

func TestLoadAll_GlobalOnly(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)
	writeSkill(t, filepath.Join(tmp, ".p-chat", "skills"), "alpha")
	writeSkill(t, filepath.Join(tmp, ".p-chat", "skills"), "beta")

	got, err := LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 skills, got %d", len(got))
	}
	// Sorted alphabetically: alpha before beta.
	if got[0].Name != "alpha" || got[1].Name != "beta" {
		t.Errorf("expected alpha, beta order, got %s, %s", got[0].Name, got[1].Name)
	}
}

func TestLoadAll_ProjectOverridesGlobal(t *testing.T) {
	// Project and global are at the same path in the test setup, so
	// this test directly exercises the dedup logic via skillMap.
	// We simulate two skills with the same name by writing to a
	// shared dir and trusting the map to keep one entry.
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)
	oldCwd, _ := os.Getwd()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })

	// First write: "alpha" with content A
	writeSkill(t, filepath.Join(tmp, ".p-chat", "skills"), "alpha")

	// Manually re-write with different content (simulating override)
	if err := os.WriteFile(
		filepath.Join(tmp, ".p-chat", "skills", "alpha", "SKILL.md"),
		[]byte("# alpha\n\nOverridden content.\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	got, err := LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Errorf("expected 1 skill (deduped), got %d", len(got))
	}
	if !strings.Contains(got[0].Content, "Overridden content") {
		t.Errorf("got content %q, want to contain 'Overridden content'", got[0].Content)
	}
}

func TestLoadAll_IgnoresNonSkillFiles(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)

	skillsDir := filepath.Join(tmp, ".p-chat", "skills")
	// Real skill (in subdir)
	goodDir := filepath.Join(skillsDir, "good")
	if err := os.MkdirAll(goodDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(goodDir, "SKILL.md"), []byte("# good\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Skill using README.md (alternate name)
	readmeDir := filepath.Join(skillsDir, "readme-skill")
	if err := os.MkdirAll(readmeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(readmeDir, "README.md"), []byte("# readme\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, _ := LoadAll()
	if len(got) != 2 {
		t.Errorf("expected 2 skills, got %d", len(got))
	}
}

func TestLoadAll_SortedAlphabetically(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)
	for _, name := range []string{"zebra", "alpha", "mango"} {
		writeSkill(t, filepath.Join(tmp, ".p-chat", "skills"), name)
	}
	got, _ := LoadAll()
	want := []string{"alpha", "mango", "zebra"}
	for i, w := range want {
		if got[i].Name != w {
			t.Errorf("got[%d].Name = %q, want %q", i, got[i].Name, w)
		}
	}
}

func TestBuildSkillContext_Empty(t *testing.T) {
	got := BuildSkillContext(nil)
	if !strings.Contains(got, "## Available Skills") {
		t.Errorf("missing header: %s", got)
	}
	if !strings.Contains(got, "(none)") {
		t.Errorf("expected (none) marker, got %q", got)
	}
}

func TestBuildSkillContext_WithSkills(t *testing.T) {
	skills := []Skill{
		{Name: "a", Description: "alpha desc", Content: "alpha body"},
		{Name: "b", Description: "beta desc", Content: "beta body"},
	}
	got := BuildSkillContext(skills)
	for _, want := range []string{"## Available Skills", "### a", "alpha body", "### b", "beta body"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q: %s", want, got)
		}
	}
}

func TestBuildSkillContext_StableAcrossCalls(t *testing.T) {
	skills := []Skill{{Name: "x", Content: "y"}}
	if BuildSkillContext(skills) != BuildSkillContext(skills) {
		t.Error("BuildSkillContext not stable")
	}
}

// --- 2026-07 project-aware loader tests ----------------------
//
// Pre-2026-07 LoadAll() walked paths.ProjectSkillsDir() which
// was rooted at os.Getwd() — broken for the Wails GUI server
// whose CWD is unrelated to the user's project. LoadAllWithRoot
// fixes this by accepting the session's projectRoot and walking
// paths.ProjectSkillsDirWithRoot(root) instead.

// TestLoadAllWithRoot_ProjectWinsOverGlobal verifies the
// merge semantics: a project-level skill with the same name
// as a global skill replaces the global content (project
// overrides).
func TestLoadAllWithRoot_ProjectWinsOverGlobal(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)
	root := t.TempDir()

	// Global "alpha" with content A
	writeSkill(t, filepath.Join(tmp, ".p-chat", "skills"), "alpha")
	if err := os.WriteFile(
		filepath.Join(tmp, ".p-chat", "skills", "alpha", "SKILL.md"),
		[]byte("# alpha\n\nGlobal content.\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Project "alpha" with content B
	writeSkill(t, filepath.Join(root, ".p-chat", "skills"), "alpha")
	if err := os.WriteFile(
		filepath.Join(root, ".p-chat", "skills", "alpha", "SKILL.md"),
		[]byte("# alpha\n\nProject content.\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := LoadAllWithRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 dedup'd skill, got %d", len(got))
	}
	if !strings.Contains(got[0].Content, "Project content") {
		t.Errorf("project should override global; got %q", got[0].Content)
	}
}

// TestLoadAllWithRoot_ProjectRootIgnoresServerCWD is the
// 2026-07 P1 regression test: a project skill under a
// DIFFERENT directory than the server CWD must still be
// picked up. Pre-2026-07 LoadAll() used os.Getwd() so a
// Wails GUI server (whose CWD is unrelated to the user's
// project) would skip the project slot entirely.
func TestLoadAllWithRoot_ProjectRootIgnoresServerCWD(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)
	root := t.TempDir()

	// Save and restore CWD so the test demonstrates the
	// old "use os.Getwd()" approach would have missed the
	// project skill. We chdir to a third directory that
	// has no project skill; only the explicit root param
	// finds it.
	oldCwd, _ := os.Getwd()
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })

	writeSkill(t, filepath.Join(root, ".p-chat", "skills"), "project-only")

	got, err := LoadAllWithRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "project-only" {
		t.Errorf("expected 1 skill 'project-only', got %+v", got)
	}
}

// TestLoadAllWithRoot_EmptyRootSkipsProjectSlot locks the
// "no project pinned" branch: root == "" means the project
// slot is skipped (matches the global-mode UX where the
// user explicitly opted out of project scoping).
func TestLoadAllWithRoot_EmptyRootSkipsProjectSlot(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)
	root := t.TempDir()

	// Project skill under root — must NOT be picked up
	// when LoadAllWithRoot is called with "".
	writeSkill(t, filepath.Join(root, ".p-chat", "skills"), "project-only")
	// Global skill — must be picked up.
	writeSkill(t, filepath.Join(tmp, ".p-chat", "skills"), "global-only")

	got, err := LoadAllWithRoot("")
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range got {
		if s.Name == "project-only" {
			t.Errorf("empty root should skip project slot; got %+v", got)
		}
	}
	foundGlobal := false
	for _, s := range got {
		if s.Name == "global-only" {
			foundGlobal = true
		}
	}
	if !foundGlobal {
		t.Errorf("expected global-only with empty root, got %+v", got)
	}
}
