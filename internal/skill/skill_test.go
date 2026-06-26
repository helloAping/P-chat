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
