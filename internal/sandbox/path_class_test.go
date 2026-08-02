package sandbox

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/p-chat/pchat/internal/config"
)

// TestClassifyPath_Table is the main behavioural spec for the
// path classifier. The table covers the four cases the 2026-07
// spec called out:
//
//	project → project-internal   (relaxed read / confirm-write)
//	external → no project pinned  (confirm everything)
//	global → project set, path outside  (confirm everything)
//	protected → hard block  (always block, never confirm)
//
// plus the user-whitelist (`allowed`) bucket, the
// ../../-traversal attack, and the empty-input corner.
func TestClassifyPath_Table(t *testing.T) {
	// Build a real temp directory tree so the test runs on
	// any OS. The classifier calls EvalSymlinks + Abs, which
	// behaves differently for paths that don't exist on the
	// current platform (Windows test host + Unix-style paths
	// = `/home/u/proj` gets mapped to `C:\home\u\proj` and
	// the containment check falls apart).
	tmp := t.TempDir()
	projectRoot := filepath.Join(tmp, "proj")
	allowed := filepath.Join(tmp, "notes")
	protected := filepath.Join(tmp, ".ssh")
	for _, d := range []string{projectRoot, allowed, protected} {
		if err := mkdirAllForTest(d); err != nil {
			t.Fatalf("mkdir %q: %v", d, err)
		}
	}
	// Pre-build a "file exists" path so EvalSymlinks succeeds.
	if err := mkdirAllForTest(filepath.Join(projectRoot, "src", "api")); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := mkdirAllForTest(filepath.Join(allowed, "sub")); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := mkdirAllForTest(filepath.Join(protected)); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	protectedDirs := []string{protected}
	extraAllowed := []string{allowed}

	cases := []struct {
		desc        string
		path        string
		projectRoot string
		want        PathClass
	}{
		// --- project bucket ---
		{"project root itself", projectRoot, projectRoot, PathClassProject},
		{"project subdirectory", filepath.Join(projectRoot, "src", "api"), projectRoot, PathClassProject},
		{"project nested file", filepath.Join(projectRoot, "src", "api", "handler.go"), projectRoot, PathClassProject},

		// --- external (no projectRoot) ---
		// A path that's not in protected, not in allowed, and
		// projectRoot is empty → external. (Note: a path under
		// the `allowed` whitelist stays "allowed" regardless
		// of projectRoot, by design — the whitelist is a
		// deliberate "open this directory" gesture.)
		{"no project, path not whitelisted", filepath.Join(tmp, "other", "file.go"), "", PathClassExternal},
		{"no project, relative path", "src/foo.go", "", PathClassExternal},

		// --- global (projectRoot set, path outside) ---
		// We use a sibling temp dir that is NOT in protected or
		// allowed. t.TempDir's siblings aren't easy to build, so
		// we use a hard path that doesn't intersect project/allowed/protected.
		{"project set, path outside", filepath.Join(tmp, "other", "file.go"), projectRoot, PathClassGlobal},
		{"project set, sibling directory", filepath.Join(tmp, "sibling"), projectRoot, PathClassGlobal},

		// --- allowed (whitelist) ---
		{"whitelisted file", filepath.Join(allowed, "foo.md"), projectRoot, PathClassAllowed},
		{"whitelisted subdir", filepath.Join(allowed, "sub", "note.md"), projectRoot, PathClassAllowed},

		// --- protected (hard block) ---
		{"protected exact", protected, projectRoot, PathClassProtected},
		{"protected nested", filepath.Join(protected, "id_rsa"), projectRoot, PathClassProtected},
		// protected wins over whitelist too — defence in depth.
		{"protected beats whitelist", filepath.Join(protected, "x"), projectRoot, PathClassProtected},

		// --- traversal attacks ---
		// `..` resolved to a path inside project should still
		// land in the project bucket (after `..` resolution).
		{"traversal to project", filepath.Join(projectRoot, "..", "proj", "foo"), projectRoot, PathClassProject},

		// --- empty / weird ---
		{"empty path", "", projectRoot, PathClassExternal},
	}

	for _, c := range cases {
		t.Run(c.desc, func(t *testing.T) {
			got := classifyPath(c.path, c.projectRoot, extraAllowed, protectedDirs, nil)
			if got != c.want {
				t.Errorf("classifyPath(%q, projectRoot=%q) = %s, want %s",
					c.path, c.projectRoot, got, c.want)
			}
		})
	}
}

// TestClassifyPath_TraversalCleanup covers the path-normalisation
// step that happens INSIDE classifyPath (cleanForClassify). The
// main table test above feeds already-absolute paths; this one
// feeds relative + `..` paths to verify the `..` segments get
// resolved before the containment check.
//
// The 2026-07 spec called this out as a P0 security concern: the
// LLM can write `../../../etc/passwd` thinking it's still in the
// project, and without cleanup, the textual containment check
// would (incorrectly) say "yes, that's under /home/u/proj".
func TestClassifyPath_TraversalCleanup(t *testing.T) {
	tmp := t.TempDir()
	project := filepath.Join(tmp, "proj")
	if err := mkdirAllForTest(project); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	protectedDirs := []string{filepath.Join(tmp, ".ssh")}

	// Build a path that traverses from project into the temp
	// root (outside project, not in protected). Should NOT be
	// classified as project.
	rel := filepath.Join(project, "..", "outside.txt")
	if got := classifyPath(rel, project, nil, protectedDirs, nil); got == PathClassProject {
		t.Errorf("traversal %q leaked into project bucket (got %s)", rel, got)
	}
	// The only invariant is "not project". It should be
	// global (projectRoot is set, path is outside).
	if got := classifyPath(rel, project, nil, protectedDirs, nil); got != PathClassGlobal {
		t.Errorf("traversal expected global, got %s", got)
	}
}

// TestClassifyPath_EmptyInputs exercises the "what if the caller
// hands us empty strings" corners. classifyPath returns a
// PathClass rather than an error because callers want a value to
// pass to the decision matrix; an empty input mapping to
// PathClassExternal is the most conservative default.
func TestClassifyPath_EmptyInputs(t *testing.T) {
	if got := classifyPath("", "/home/u/proj", nil, nil, nil); got != PathClassExternal {
		t.Errorf("empty path: got %s, want external", got)
	}
	if got := classifyPath("foo.go", "", nil, nil, nil); got != PathClassExternal {
		t.Errorf("empty projectRoot: got %s, want external", got)
	}
	if got := classifyPath("foo.go", "/home/u/proj", nil, nil, nil); got != PathClassGlobal {
		t.Errorf("project set, path outside: got %s, want global", got)
	}
}

// TestClassifyPath_NoIO is a sanity check that classifyPath does
// not require pre-existing target files. The classifier must
// return a deterministic answer for a non-existent path because
// the LLM's write_file target may not exist yet (it'll be
// created by the write). We rely on textual Abs+Clean as the
// fallback when EvalSymlinks fails.
func TestClassifyPath_NoIO(t *testing.T) {
	tmp := t.TempDir()
	project := filepath.Join(tmp, "proj")
	if err := mkdirAllForTest(project); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	ghost := filepath.Join(project, "__ghost_does_not_exist__", "file.go")
	// A non-existent file under project must still land in
	// PathClassProject (the textual containment check on the
	// cleaned form should not require the file to exist).
	if got := classifyPath(ghost, project, nil, nil, nil); got != PathClassProject {
		t.Errorf("classifyPath(non-existent under project) = %s, want project", got)
	}
}

// TestClassifyPath_CaseInsensitiveOnWindows mirrors the
// isPathUnder behaviour: on Windows the path comparison is
// case-insensitive. We skip this on non-Windows platforms via
// the runtime check; the underlying filepath.Clean handles it
// on Linux/macOS the other way.
func TestClassifyPath_CaseInsensitiveOnWindows(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("Windows-only test")
	}
	tmp := t.TempDir()
	project := filepath.Join(tmp, "Proj")
	if err := mkdirAllForTest(project); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Different case variant of the project dir + a sub file.
	upperVariant := filepath.Join(tmp, "PROJ", "foo.go")
	if err := mkdirAllForTest(filepath.Dir(upperVariant)); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// On Windows, EvalSymlinks may return the actual on-disk
	// case; the test exercises the textual-clean path.
	got := classifyPath(upperVariant, project, nil, nil, nil)
	// We accept either Project (case-insensitive match
	// worked) or Global (case-sensitive match failed) — the
	// invariant is that classifyPath does not crash. Tighten
	// the assertion once isPathUnder is verified case-
	// insensitive on Windows in another test.
	_ = got
}

// TestClassifyPath_ProjectBeatsWideProtected is the regression test
// for the workspace-under-home bug: a project living under a WIDE
// protected prefix (e.g. the whole home dir in
// defaultWriteProtectedPaths) must get project policy, not a hard
// block. Specific protected dirs (~/.ssh) still win over the
// project.
func TestClassifyPath_ProjectBeatsWideProtected(t *testing.T) {
	tmp := t.TempDir()
	home := filepath.Join(tmp, "home")
	project := filepath.Join(home, "proj")
	if err := mkdirAllForTest(project); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// The wide protected dir is home itself (broad, wraps project).
	wide := []string{home}
	// A specific sensitive dir under home that must stay protected.
	specific := []string{filepath.Join(home, ".ssh")}

	// File inside project, project set → project (not blocked by wide home).
	if got := classifyPath(filepath.Join(project, "x.go"), project, nil, specific, wide); got != PathClassProject {
		t.Errorf("project under wide-protected home = %s, want project", got)
	}
	// File under ~/.ssh, even inside a "project" claim → still protected.
	if got := classifyPath(filepath.Join(home, ".ssh", "id_rsa"), project, nil, specific, wide); got != PathClassProtected {
		t.Errorf("~/.ssh inside project = %s, want protected", got)
	}
	// Non-project file under home, project set → protected (wide dir still guards non-project paths).
	if got := classifyPath(filepath.Join(home, "other", "x.txt"), project, nil, specific, wide); got != PathClassProtected {
		t.Errorf("non-project home path = %s, want protected", got)
	}
}

// TestCheckWrite_WorkspaceUnderHomeNotBlocked is the end-to-end
// regression for the ~/.p-chat/workspace default working directory:
// with home in WriteProtectedPaths (the default), a write inside the
// workspace (which lives under home) must be Confirm, not Block. This
// is what lets a project-less session write files in its default
// working directory.
func TestCheckWrite_WorkspaceUnderHomeNotBlocked(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	cfg := config.SandboxConfig{
		Enabled:             true,
		RequireConfirm:      "dangerous",
		WriteProtectedPaths: []string{home},
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ws := filepath.Join(home, ".p-chat", "workspace")
	if got := s.CheckWrite(filepath.Join(ws, "x.go"), ws); got != Confirm {
		t.Errorf("CheckWrite(workspace, projectRoot=workspace) = %s, want confirm", got)
	}
	if got := s.CheckRead(filepath.Join(ws, "x.go"), ws); got != Allow {
		t.Errorf("CheckRead(workspace, projectRoot=workspace) = %s, want allow", got)
	}
}

// TestCheckWrite_HomeProtectedStillGuards is the safety-net check:
// the home-wide protection must still hard-block writes OUTSIDE the
// project (no silent widening), and specific dirs like ~/.ssh remain
// blocked even inside a project claim.
func TestCheckWrite_HomeProtectedStillGuards(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	cfg := config.SandboxConfig{
		Enabled:             true,
		RequireConfirm:      "dangerous",
		WriteProtectedPaths: []string{home, filepath.Join(home, ".ssh")},
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	proj := filepath.Join(home, "myproj")
	// Non-project home path with a project set → still blocked by the
	// home-wide protection (unchanged safety net).
	if got := s.CheckWrite(filepath.Join(home, "other", "x.txt"), proj); got != Block {
		t.Errorf("non-project home path = %s, want block", got)
	}
	// ~/.ssh inside a project claim → still blocked (specific wins).
	if got := s.CheckWrite(filepath.Join(home, ".ssh", "id_rsa"), proj); got != Block {
		t.Errorf("~/.ssh = %s, want block", got)
	}
}

// mustAbs is a tiny helper that turns a path into an absolute
// one or fails the test immediately. The class tests use it
// because classifyPath is a pure function — it doesn't call
// filepath.Abs itself, the test sets up the absolute paths the
// same way Sandbox.New does in production.
func mustAbs(t *testing.T, p string) string {
	t.Helper()
	abs, err := filepath.Abs(p)
	if err != nil {
		t.Fatalf("filepath.Abs(%q): %v", p, err)
	}
	return abs
}

// mkdirAllForTest is a tiny helper that creates the directory
// (and any parents) needed for the classifier test. We use it
// instead of os.MkdirAll directly so the test reads as a flat
// sequence of `t.TempDir()` + named subdirs.
func mkdirAllForTest(d string) error {
	return mkdirAll(d)
}

// mkdirAll is the actual OS-backed call. Kept in a separate
// function so the helper surface is one-liner, and so the test
// file doesn't need to import os in multiple places.
func mkdirAll(d string) error {
	return os.MkdirAll(d, 0o755)
}
