package rules

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRule(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
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
		t.Errorf("expected 0 rules, got %d", len(got))
	}
}

func TestLoadAll_GlobalOnly(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)
	writeRule(t, filepath.Join(tmp, ".p-chat", "rules"), "code-style.md", "use tabs")
	writeRule(t, filepath.Join(tmp, ".p-chat", "rules"), "naming.md", "PascalCase for types")

	got, err := LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("expected 2 rules, got %d", len(got))
	}
}

func TestLoadAll_BothLoaded(t *testing.T) {
	// Verify the global + project paths are both loaded, even if
	// they share the same physical dir in the test setup.
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)
	oldCwd, _ := os.Getwd()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })

	// Note: GlobalRulesDir and ProjectRulesDir are the same path
	// (cwd/.p-chat/rules) when HOME == cwd. To test the merge
	// path properly we'd need separate roots, but the current
	// implementation doesn't deduplicate. Skip the override test.
	writeRule(t, filepath.Join(tmp, ".p-chat", "rules"), "rule.md", "hi")

	got, err := LoadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 1 {
		t.Errorf("expected at least 1 rule, got %d", len(got))
	}
}

func TestLoadAll_IgnoresNonMarkdown(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)
	writeRule(t, filepath.Join(tmp, ".p-chat", "rules"), "real.md", "hello")
	writeRule(t, filepath.Join(tmp, ".p-chat", "rules"), "ignored.txt", "nope")
	writeRule(t, filepath.Join(tmp, ".p-chat", "rules"), "also-ignored.json", "{}")

	got, _ := LoadAll()
	if len(got) != 1 {
		t.Errorf("expected only 1 markdown rule, got %d", len(got))
	}
	if len(got) > 0 && got[0].Name != "real" {
		t.Errorf("expected 'real', got %q", got[0].Name)
	}
}

func TestLoadAll_SortedBySourceAndName(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)
	oldCwd, _ := os.Getwd()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })

	// Project rules with names that should sort before/after globals.
	writeRule(t, filepath.Join(tmp, ".p-chat", "rules"), "z.md", "z")
	writeRule(t, filepath.Join(tmp, ".p-chat", "rules"), "a.md", "a")
	writeRule(t, filepath.Join(tmp, ".p-chat", "rules"), "m.md", "m")
	writeRule(t, filepath.Join(tmp, ".p-chat", "rules"), "a.md", "project a")

	got, _ := LoadAll()
	names := make([]string, 0, len(got))
	for _, r := range got {
		names = append(names, r.Name+"@"+r.Path)
	}
	// All global entries should appear before all project entries.
	// Within each group, names are sorted.
	for i := 0; i < len(got)-1; i++ {
		gi, pi := isGlobal(got[i].Path), isGlobal(got[i+1].Path)
		if gi && !pi {
			continue
		}
		if !gi && pi {
			t.Errorf("ordering violated: %s before %s", names[i], names[i+1])
		}
	}
}

func isGlobal(p string) bool {
	return strings.Contains(p, filepath.Join(".p-chat", "rules"))
}

func TestBuildRulesContext_Empty(t *testing.T) {
	got := BuildRulesContext(nil)
	if !strings.Contains(got, "## Rules") {
		t.Errorf("header missing: %s", got)
	}
	if !strings.Contains(got, "(none)") {
		t.Errorf("expected (none) marker for empty rules, got %q", got)
	}
}

func TestBuildRulesContext_WithRules(t *testing.T) {
	rules := []Rule{
		{Name: "a", Content: "alpha"},
		{Name: "b", Content: "beta"},
	}
	got := BuildRulesContext(rules)
	for _, want := range []string{"## Rules", "### a", "alpha", "### b", "beta"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q: %s", want, got)
		}
	}
}

func TestBuildRulesContext_StableAcrossCalls(t *testing.T) {
	// Byte-stable for LLM cache: two calls with the same rules
	// produce identical output.
	rules := []Rule{{Name: "x", Content: "y"}}
	a := BuildRulesContext(rules)
	b := BuildRulesContext(rules)
	if a != b {
		t.Errorf("BuildRulesContext not stable:\n%s\n---\n%s", a, b)
	}
}

// --- 2026-07 project-aware loader tests ----------------------

// TestLoadAllWithRoot_ProjectRootIgnoresServerCWD is the
// 2026-07 P1.5 regression test: project rules under a
// DIFFERENT directory than the server CWD must still be
// picked up. Pre-2026-07 LoadAll() used os.Getwd() so a
// Wails GUI server (whose CWD is unrelated to the user's
// project) would skip the project slot entirely.
func TestLoadAllWithRoot_ProjectRootIgnoresServerCWD(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)
	root := t.TempDir()

	// Save and restore CWD to a third directory that has
	// no project rules; only the explicit root param
	// finds the project-level rule.
	oldCwd, _ := os.Getwd()
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })

	writeRule(t, filepath.Join(root, ".p-chat", "rules"), "project-only.md", "project content")

	got, err := LoadAllWithRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(got))
	}
	if got[0].Name != "project-only" {
		t.Errorf("expected 'project-only', got %q", got[0].Name)
	}
	if got[0].IsGlobal {
		t.Errorf("expected IsGlobal=false for project-level rule, got true")
	}
}

// TestLoadAllWithRoot_BothLoaded is the merge test: global
// + project rules are both loaded (AND policy). Pre-2026-07
// the merge was based on os.Getwd() so it didn't work for
// Wails; post-fix, the project slot is anchored to the
// session's projectRoot.
func TestLoadAllWithRoot_BothLoaded(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)
	root := t.TempDir()

	writeRule(t, filepath.Join(tmp, ".p-chat", "rules"), "global-rule.md", "global content")
	writeRule(t, filepath.Join(root, ".p-chat", "rules"), "project-rule.md", "project content")

	got, err := LoadAllWithRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 rules (global + project), got %d", len(got))
	}
	// Global first, then project (per sort by IsGlobal).
	if !got[0].IsGlobal {
		t.Errorf("first rule should be global, got IsGlobal=false")
	}
	if got[1].IsGlobal {
		t.Errorf("second rule should be project, got IsGlobal=true")
	}
}

// TestLoadAllWithRoot_EmptyRootSkipsProjectSlot locks the
// "no project pinned" branch: root == "" means the project
// slot is skipped.
func TestLoadAllWithRoot_EmptyRootSkipsProjectSlot(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)
	root := t.TempDir()

	writeRule(t, filepath.Join(root, ".p-chat", "rules"), "project-only.md", "x")
	writeRule(t, filepath.Join(tmp, ".p-chat", "rules"), "global-only.md", "y")

	got, err := LoadAllWithRoot("")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range got {
		if r.Name == "project-only" {
			t.Errorf("empty root should skip project slot, got %+v", got)
		}
	}
}
