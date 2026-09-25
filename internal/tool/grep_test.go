package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/p-chat/pchat/internal/config"
)

// buildGrepWorkspace creates a temp project root with a few text
// files (matching the extensions the grep tool searches) plus a
// large file, a binary file, and a node_modules dir that must be
// skipped. Returns the root.
func buildGrepWorkspace(t *testing.T) string {
	t.Helper()
	root := t.TempDir()

	os.WriteFile(filepath.Join(root, "readme.md"), []byte("## Project Overview\n\nThis is a test project.\n\n## Installation\n\nRun `go install`"), 0o644)
	os.WriteFile(filepath.Join(root, "api.go"), []byte("package main\n\nfunc HandleRequest() {\n\t// process\n}\n"), 0o644)
	os.WriteFile(filepath.Join(root, "server.ts"), []byte("export const server = 'hello';\n"), 0o644)
	os.MkdirAll(filepath.Join(root, "sub", "nested"), 0o755)
	os.WriteFile(filepath.Join(root, "sub", "nested", "deep.md"), []byte("# Deep\n\ndeep content here"), 0o644)

	// node_modules must be skipped.
	os.MkdirAll(filepath.Join(root, "node_modules", "pkg"), 0o755)
	os.WriteFile(filepath.Join(root, "node_modules", "pkg", "index.js"), []byte("Installation here"), 0o644)

	// Large file to test size skip.
	large := make([]byte, 6*1024*1024)
	os.WriteFile(filepath.Join(root, "large.txt"), large, 0o644)

	// Binary file (has a NUL byte) must be skipped.
	os.WriteFile(filepath.Join(root, "blob.bin"), []byte{0x00, 0x01, 0x02, 'h', 'e', 'l', 'l', 'o'}, 0o644)
	return root
}

func grepCtx(root string) context.Context {
	return WithProjectRoot(context.Background(), root)
}

func TestGrepWorkingDir_Basic(t *testing.T) {
	root := buildGrepWorkspace(t)
	results := grepWorkingDir(grepCtx(root), root, root, "Installation", false, 10)
	if len(results) == 0 {
		t.Fatal("expected results for 'Installation' in readme.md")
	}
	if !strings.Contains(results[0], "readme.md") {
		t.Errorf("expected readme.md in result, got %q", results[0])
	}
}

func TestGrepWorkingDir_CaseInsensitive(t *testing.T) {
	root := buildGrepWorkspace(t)
	results := grepWorkingDir(grepCtx(root), root, root, "installation", false, 10)
	if len(results) == 0 {
		t.Fatal("case-insensitive search failed for 'installation'")
	}
}

func TestGrepWorkingDir_CaseSensitive(t *testing.T) {
	root := buildGrepWorkspace(t)
	results := grepWorkingDir(grepCtx(root), root, root, "Installation", true, 10)
	if len(results) == 0 {
		t.Fatal("case-sensitive exact 'Installation' should match")
	}
	results = grepWorkingDir(grepCtx(root), root, root, "installation", true, 10)
	if len(results) != 0 {
		t.Errorf("case-sensitive 'installation' (lowercase) should not match, got %d", len(results))
	}
}

func TestGrepWorkingDir_NoResults(t *testing.T) {
	root := buildGrepWorkspace(t)
	results := grepWorkingDir(grepCtx(root), root, root, "zzzzzzznonexistent", false, 10)
	if len(results) != 0 {
		t.Errorf("want 0 results, got %d", len(results))
	}
}

func TestGrepWorkingDir_TopKLimit(t *testing.T) {
	root := buildGrepWorkspace(t)
	// "project" appears only once, so topK=1 still works; use a
	// term that appears many times to verify the cap.
	os.WriteFile(filepath.Join(root, "many.txt"), []byte(strings.Repeat("needle line\n", 50)), 0o644)
	results := grepWorkingDir(grepCtx(root), root, root, "needle", false, 5)
	if len(results) > 5 {
		t.Errorf("topK=5 should return at most 5 results, got %d", len(results))
	}
}

func TestGrepWorkingDir_SkipsNodeModulesAndBinary(t *testing.T) {
	root := buildGrepWorkspace(t)
	results := grepWorkingDir(grepCtx(root), root, root, "Installation", false, 10)
	for _, r := range results {
		if strings.Contains(r, "node_modules") {
			t.Errorf("node_modules should be skipped: %q", r)
		}
		if strings.Contains(r, "large.txt") {
			t.Errorf("large file should be skipped: %q", r)
		}
		if strings.Contains(r, "blob.bin") {
			t.Errorf("binary file should be skipped: %q", r)
		}
	}
}

func TestGrepWorkingDir_PathScoped(t *testing.T) {
	root := buildGrepWorkspace(t)
	// Search only the sub/ directory. "content" appears once in
	// sub/nested/deep.md.
	results := grepWorkingDir(grepCtx(root), filepath.Join(root, "sub"), root, "content", false, 10)
	if len(results) != 1 {
		t.Fatalf("expected 1 result in sub/, got %d", len(results))
	}
	if !strings.Contains(results[0], "deep.md") {
		t.Errorf("expected deep.md in result, got %q", results[0])
	}
}

func TestGrepWorkingDir_RelativePath(t *testing.T) {
	root := buildGrepWorkspace(t)
	results := grepWorkingDir(grepCtx(root), filepath.Join(root, "sub", "nested"), root, "content", false, 10)
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
}

func TestGrepHandler_RejectsPathOutsideRoot(t *testing.T) {
	root := buildGrepWorkspace(t)
	outside := t.TempDir()
	os.WriteFile(filepath.Join(outside, "secret.md"), []byte("secret-token"), 0o644)

	r := NewRegistry()
	RegisterGrep(r, nil)
	_, h, ok := r.Lookup("grep")
	if !ok {
		t.Fatal("grep not registered")
	}
	args, err := json.Marshal(map[string]any{
		"pattern": "secret-token",
		"path":    outside,
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err := h(grepCtx(root), args)
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("want error for outside path, got %#v", res)
	}
	if strings.Contains(res.Content, "secret-token") {
		t.Fatal("outside file content leaked")
	}

	relArgs, err := json.Marshal(map[string]any{
		"pattern": "secret-token",
		"path":    "..",
	})
	if err != nil {
		t.Fatal(err)
	}
	res, err = h(grepCtx(root), relArgs)
	if err != nil {
		t.Fatal(err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("want error for relative escape, got %#v", res)
	}
}

// ---- knowledge-base fallback (kept from the original grep) ----

func setupGrepKBTest(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()

	os.WriteFile(filepath.Join(dir, "readme.md"), []byte("## Project Overview\n\nThis is a test project.\n\n## Installation\n\nRun `go install`"), 0o644)
	os.WriteFile(filepath.Join(dir, "api.go"), []byte("package main\n\nfunc HandleRequest() {\n\t// process\n}\n"), 0o644)

	cfg := &config.Config{Knowledge: config.KnowledgeConfig{
		Enabled: true,
		Bases: []config.KnowledgeBase{
			{Name: "testgrep", Path: dir, Enabled: true},
		},
	}}
	return cfg
}

func TestGrepKnowledgeBases_Basic(t *testing.T) {
	cfg := setupGrepKBTest(t)
	results := grepKnowledgeBases(context.Background(), cfg, "", "Installation", 10)
	if len(results) == 0 {
		t.Fatal("expected results for 'Installation'")
	}
}

func TestGrepKnowledgeBases_CaseInsensitive(t *testing.T) {
	cfg := setupGrepKBTest(t)
	results := grepKnowledgeBases(context.Background(), cfg, "", "installation", 10)
	if len(results) == 0 {
		t.Fatal("case-insensitive search failed")
	}
}

func TestGrepKnowledgeBases_NoResults(t *testing.T) {
	cfg := setupGrepKBTest(t)
	results := grepKnowledgeBases(context.Background(), cfg, "", "zzzzzzznonexistent", 10)
	if len(results) != 0 {
		t.Errorf("want 0 results, got %d", len(results))
	}
}

func TestGrepKnowledgeBases_SpecificBase(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	os.WriteFile(filepath.Join(dir1, "a.md"), []byte("unique-abc"), 0o644)
	os.WriteFile(filepath.Join(dir2, "b.md"), []byte("unique-xyz"), 0o644)

	cfg := &config.Config{Knowledge: config.KnowledgeConfig{
		Enabled: true,
		Bases: []config.KnowledgeBase{
			{Name: "base1", Path: dir1, Enabled: true},
			{Name: "base2", Path: dir2, Enabled: true},
		},
	}}

	results := grepKnowledgeBases(context.Background(), cfg, "base1", "unique", 10)
	for _, r := range results {
		if strings.Contains(r.Content, "unique-xyz") {
			t.Errorf("base1 should not include base2 results: %s", r.Source)
		}
	}
	if len(results) == 0 {
		t.Fatal("expected results from base1")
	}
}

func TestGrepKnowledgeBases_RespectsExcludePatterns(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "keep.md"), []byte("shared-token keep"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "docs", "skip.md"), []byte("shared-token skip"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{Knowledge: config.KnowledgeConfig{
		Enabled: true,
		Bases: []config.KnowledgeBase{
			{Name: "testgrep", Path: dir, Enabled: true, ExcludePatterns: []string{`docs\**`}},
		},
	}}

	results := grepKnowledgeBases(context.Background(), cfg, "", "shared-token", 10)
	if len(results) != 1 {
		t.Fatalf("len(results) = %d, want 1", len(results))
	}
	if strings.Contains(results[0].Content, "skip") {
		t.Fatalf("excluded file was searched: %+v", results[0])
	}
}

func TestGrepKnowledgeBases_Disabled(t *testing.T) {
	cfg := &config.Config{Knowledge: config.KnowledgeConfig{Enabled: false}}
	results := grepKnowledgeBases(context.Background(), cfg, "", "anything", 10)
	if len(results) != 0 {
		t.Errorf("disabled KB should return no results, got %d", len(results))
	}
}
