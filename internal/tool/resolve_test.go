package tool

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// withProjectRoot is a tiny helper for the resolveToProjectRoot
// tests — wraps context.Background() with the project root.
func withProjectRoot(root string) context.Context {
	return WithProjectRoot(context.Background(), root)
}

// TestResolveToProjectRoot_RelativePath is the happy path: a
// relative path inside the project gets joined to the
// project root, and `..` segments are resolved.
func TestResolveToProjectRoot_RelativePath(t *testing.T) {
	root := mustMkdirTemp(t, "proj")
	ctx := withProjectRoot(root)
	got := resolveToProjectRoot(ctx, "src/foo.go")
	want := filepath.Clean(filepath.Join(root, "src/foo.go"))
	if got != want {
		t.Errorf("resolveToProjectRoot(rel) = %q, want %q", got, want)
	}
}

// TestResolveToProjectRoot_Traversal is the P0 2026-07
// regression: a path with `..` that traverses OUT of the
// project must be resolved to its canonical form (escaping
// the project) so the downstream sandbox check sees the
// escaped path. Before the fix, the function returned
// "C:\projects\myapp\..\..\..\etc\passwd" and the sandbox
// would (correctly) flag it as outside the project — but
// the OS read would still follow the `..` and access
// /etc/passwd because we hadn't cleaned the path.
func TestResolveToProjectRoot_Traversal(t *testing.T) {
	root := mustMkdirTemp(t, "proj")
	ctx := withProjectRoot(root)
	rel := "../../../etc/passwd"
	got := resolveToProjectRoot(ctx, rel)
	// After Clean, the path no longer contains ".." segments.
	if strings.Contains(got, "..") {
		t.Errorf("resolveToProjectRoot didn't clean ..\\ segments: %q", got)
	}
	// And it's now under the system root, not the project.
	if filepath.HasPrefix(got, root) {
		t.Errorf("traversal should escape project root: got %q (root %q)", got, root)
	}
}

// TestResolveToProjectRoot_AbsolutePath checks that absolute
// paths get cleaned but otherwise pass through unchanged.
func TestResolveToProjectRoot_AbsolutePath(t *testing.T) {
	abs := filepath.Clean(mustMkdirTemp(t, "anywhere") + "/foo/../bar.go")
	ctx := withProjectRoot(mustMkdirTemp(t, "proj"))
	got := resolveToProjectRoot(ctx, abs)
	if got != abs {
		t.Errorf("absolute path: got %q, want %q", got, abs)
	}
}

// TestResolveToProjectRoot_NoProject covers the "no project
// pinned" branch: relative paths return the cleaned form
// (still relative — caller decides what to do with it).
func TestResolveToProjectRoot_NoProject(t *testing.T) {
	ctx := context.Background() // no projectRoot
	got := resolveToProjectRoot(ctx, "src/foo.go")
	if got != filepath.Clean("src/foo.go") {
		t.Errorf("no project: got %q, want %q", got, filepath.Clean("src/foo.go"))
	}
}

// TestResolveToProjectRoot_Empty covers the empty-input
// short-circuit added alongside the Clean fix.
func TestResolveToProjectRoot_Empty(t *testing.T) {
	if got := resolveToProjectRoot(context.Background(), ""); got != "" {
		t.Errorf("empty input: got %q, want empty", got)
	}
}

func mustMkdirTemp(t *testing.T, prefix string) string {
	t.Helper()
	d, err := filepath.Abs(t.TempDir())
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	return filepath.Join(d, prefix)
}
