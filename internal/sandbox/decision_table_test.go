package sandbox

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/p-chat/pchat/internal/config"
)

// TestCheckWrite_PathClass_Table is the 2026-07 spec table for
// the path-class decision matrix. The table covers:
//
//	project  → Confirm  ("项目内写 = confirm")
//	allowed  → Allow    (whitelist is a deliberate "open this up")
//	global   → Confirm  (project set, path outside)
//	external → Confirm  (no project, "global mode")
//	protected → Block   (hard, no override)
//
// Precedence (highest first) is tested in classifyPath tests;
// this test focuses on the read-vs-write decision table
// behaviour.
func TestCheckWrite_PathClass_Table(t *testing.T) {
	tmp := t.TempDir()
	projectRoot := filepath.Join(tmp, "proj")
	allowed := filepath.Join(tmp, "notes")
	protected := filepath.Join(tmp, ".ssh")
	other := filepath.Join(tmp, "other", "x.go")
	for _, d := range []string{projectRoot, allowed, protected, filepath.Dir(other)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %q: %v", d, err)
		}
	}

	cfg := config.SandboxConfig{
		Enabled:             true,
		RequireConfirm:      "dangerous",
		WriteProtectedPaths: []string{protected},
		ExtraAllowedPaths:   []string{allowed},
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	cases := []struct {
		desc        string
		path        string
		projectRoot string
		want        Decision
	}{
		// project bucket
		{"write inside project → confirm", filepath.Join(projectRoot, "x.go"), projectRoot, Confirm},
		// allowed bucket (whitelist)
		{"write in whitelist → allow", filepath.Join(allowed, "x.md"), projectRoot, Allow},
		// global bucket (project set, path outside)
		{"write outside project → confirm", other, projectRoot, Confirm},
		// external bucket (no project)
		{"write in global mode → confirm", other, "", Confirm},
		// protected bucket
		{"write to protected → block", filepath.Join(protected, "id_rsa"), projectRoot, Block},
	}

	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			got := s.CheckWrite(c.path, c.projectRoot)
			if got != c.want {
				t.Errorf("CheckWrite(%q, projectRoot=%q) = %s, want %s",
					c.path, c.projectRoot, got, c.want)
			}
		})
	}
}

// TestCheckRead_PathClass_Table mirrors TestCheckWrite_*
// with the read decision table. The 2026-07 spec's exception:
// project-internal reads are Allow (not Confirm). Everything
// else mirrors writes — global / external reads still confirm
// so the LLM can't exfiltrate /etc/passwd.
func TestCheckRead_PathClass_Table(t *testing.T) {
	tmp := t.TempDir()
	projectRoot := filepath.Join(tmp, "proj")
	allowed := filepath.Join(tmp, "notes")
	protected := filepath.Join(tmp, ".ssh")
	other := filepath.Join(tmp, "other", "x.go")
	for _, d := range []string{projectRoot, allowed, protected, filepath.Dir(other)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir %q: %v", d, err)
		}
	}

	cfg := config.SandboxConfig{
		Enabled:             true,
		WriteProtectedPaths: []string{protected},
		ExtraAllowedPaths:   []string{allowed},
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	cases := []struct {
		desc        string
		path        string
		projectRoot string
		want        Decision
	}{
		// project bucket — 2026-07 spec exception: Allow
		{"read inside project → allow", filepath.Join(projectRoot, "x.go"), projectRoot, Allow},
		// allowed bucket (whitelist)
		{"read in whitelist → allow", filepath.Join(allowed, "x.md"), projectRoot, Allow},
		// global bucket
		{"read outside project → confirm", other, projectRoot, Confirm},
		// external bucket (no project)
		{"read in global mode → confirm", other, "", Confirm},
		// protected bucket
		{"read from protected → block", filepath.Join(protected, "id_rsa"), projectRoot, Block},
	}

	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			got := s.CheckRead(c.path, c.projectRoot)
			if got != c.want {
				t.Errorf("CheckRead(%q, projectRoot=%q) = %s, want %s",
					c.path, c.projectRoot, got, c.want)
			}
		})
	}
}

// TestCheckRead_TraversalEscape is the P0 regression test the
// 2026-07 spec called out: an LLM that writes
// `read_file("../../../etc/passwd")` must not slip through the
// project-root check by textual accident. The classifier's
// cleanForClassify step resolves `..` segments before the
// containment check, so the read lands in the global bucket
// (Confirm) — never the project bucket (Allow).
func TestCheckRead_TraversalEscape(t *testing.T) {
	tmp := t.TempDir()
	projectRoot := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(projectRoot, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Path that traverses FROM the project INTO the temp
	// root (which is outside the project).
	traversal := filepath.Join(projectRoot, "..", "outside.txt")

	cfg := config.SandboxConfig{Enabled: true}
	s, _ := New(cfg)
	got := s.CheckRead(traversal, projectRoot)
	if got == Allow {
		t.Errorf("traversal %q leaked into project bucket (Allow); got %s", got, got)
	}
	if got != Confirm {
		t.Errorf("expected Confirm for traversal, got %s", got)
	}
}
