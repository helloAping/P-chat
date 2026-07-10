package agent

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReloadWithRootIfChanged_NoOpOnSameRoot is the
// cache-stability test: if the session sends a follow-up
// message in the SAME project (root didn't change), the
// reload should be a no-op so the static-prompt cache
// keeps hitting. The pre-2026-07 design had no root-
// tracking at all and would rebuild the cache on every
// turn (or worse, never rebuild when the root actually
// changed).
func TestReloadWithRootIfChanged_NoOpOnSameRoot(t *testing.T) {
	a := &Agent{lastProjectRoot: "D:\\projects\\myapp"}
	a.staticPrompt = "cached prompt"
	a.staticPromptID = "stable-sig"

	a.ReloadWithRootIfChanged("D:\\projects\\myapp")

	if a.staticPrompt != "cached prompt" {
		t.Errorf("same root should NOT invalidate cache; got %q", a.staticPrompt)
	}
	if a.staticPromptID != "stable-sig" {
		t.Errorf("same root should NOT invalidate sig; got %q", a.staticPromptID)
	}
}

// TestReloadWithRootIfChanged_InvalidatesOnRootChange
// confirms the cache is dropped when the root actually
// changes — so the next buildStaticSystemPrompt rebuilds
// the prompt with the new project's AGENTS.md / skills /
// rules.
func TestReloadWithRootIfChanged_InvalidatesOnRootChange(t *testing.T) {
	a := &Agent{lastProjectRoot: "D:\\projects\\A"}
	a.staticPrompt = "old prompt"
	a.staticPromptID = "old-sig"

	a.ReloadWithRootIfChanged("D:\\projects\\B")

	if a.staticPrompt != "" {
		t.Errorf("root change should clear staticPrompt; got %q", a.staticPrompt)
	}
	if a.staticPromptID != "" {
		t.Errorf("root change should clear staticPromptID; got %q", a.staticPromptID)
	}
	if a.lastProjectRoot != "D:\\projects\\B" {
		t.Errorf("lastProjectRoot not updated; got %q", a.lastProjectRoot)
	}
}

// TestReloadWithRootIfChanged_FirstCallWithRoot is the
// "first message of a new session" case: lastProjectRoot
// starts empty, the first call with a real root must
// invalidate the cache (so the project-specific
// instructions get loaded).
func TestReloadWithRootIfChanged_FirstCallWithRoot(t *testing.T) {
	a := &Agent{} // lastProjectRoot is ""
	a.staticPrompt = "stale"
	a.staticPromptID = "stale-sig"

	a.ReloadWithRootIfChanged("D:\\projects\\myapp")

	if a.staticPrompt != "" {
		t.Errorf("first call with root should clear cache; got %q", a.staticPrompt)
	}
}

// TestReloadWithRoot_LoadsProjectSkills is the integration
// regression test: when ReloadWithRootIfChanged fires for
// a real project root, the agent's skills slice is
// populated from that root's .p-chat/skills, NOT from the
// server CWD. The test sets up two distinct directories
// and checks that only the root-anchored one is consulted.
func TestReloadWithRoot_LoadsProjectSkills(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)

	root := t.TempDir()

	// Set CWD to a directory with NO project skills. If
	// the loader fell back to os.Getwd()-based
	// ProjectSkillsDir, the test would find zero skills
	// and fail.
	oldCwd, _ := os.Getwd()
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })

	// Put a project skill under the explicit root.
	projectSkills := filepath.Join(root, ".p-chat", "skills", "my-skill")
	if err := os.MkdirAll(projectSkills, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(projectSkills, "SKILL.md"),
		[]byte("# my-skill\n\nProject skill content.\n"),
		0o644); err != nil {
		t.Fatal(err)
	}

	a := &Agent{}
	a.ReloadWithRootIfChanged(root)

	if len(a.skills) != 1 {
		t.Fatalf("expected 1 skill loaded from project root, got %d: %+v", len(a.skills), a.skills)
	}
	if a.skills[0].Name != "my-skill" {
		t.Errorf("expected skill name 'my-skill', got %q", a.skills[0].Name)
	}
}
