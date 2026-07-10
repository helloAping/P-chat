package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/p-chat/pchat/internal/config"
)

func setupGrepTest(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()

	os.WriteFile(filepath.Join(dir, "readme.md"), []byte("## Project Overview\n\nThis is a test project.\n\n## Installation\n\nRun `go install`"), 0o644)
	os.WriteFile(filepath.Join(dir, "api.go"), []byte("package main\n\nfunc HandleRequest() {\n\t// process\n}\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "sub", "nested"), 0o755)
	os.WriteFile(filepath.Join(dir, "sub", "nested", "deep.md"), []byte("# Deep\n\ndeep content here"), 0o644)

	// Large file to test size skip
	large := make([]byte, 6*1024*1024)
	os.WriteFile(filepath.Join(dir, "large.txt"), large, 0o644)

	cfg := &config.Config{Knowledge: config.KnowledgeConfig{
		Enabled: true,
		Bases: []config.KnowledgeBase{
			{Name: "testgrep", Path: dir, Enabled: true},
		},
	}}
	return cfg
}

func TestGrepKnowledgeBases_Basic(t *testing.T) {
	cfg := setupGrepTest(t)
	results := grepKnowledgeBases(context.Background(), cfg, "", "Installation", 10)
	if len(results) == 0 {
		t.Fatal("expected results for 'Installation'")
	}
	if results[0].Source == "" {
		t.Error("source should not be empty")
	}
}

func TestGrepKnowledgeBases_CaseInsensitive(t *testing.T) {
	cfg := setupGrepTest(t)
	results := grepKnowledgeBases(context.Background(), cfg, "", "installation", 10)
	if len(results) == 0 {
		t.Fatal("case-insensitive search failed")
	}
}

func TestGrepKnowledgeBases_NoResults(t *testing.T) {
	cfg := setupGrepTest(t)
	results := grepKnowledgeBases(context.Background(), cfg, "", "zzzzzzznonexistent", 10)
	if len(results) != 0 {
		t.Errorf("want 0 results, got %d", len(results))
	}
}

func TestGrepKnowledgeBases_TopKLimit(t *testing.T) {
	cfg := setupGrepTest(t)
	results := grepKnowledgeBases(context.Background(), cfg, "", "project", 1)
	if len(results) > 1 {
		t.Errorf("topK=1 should return at most 1 result, got %d", len(results))
	}
}

func TestGrepKnowledgeBases_LargeFileSkipped(t *testing.T) {
	cfg := setupGrepTest(t)
	results := grepKnowledgeBases(context.Background(), cfg, "", "a", 100)
	// The large.txt file (6MB) should be skipped; no results from it.
	for _, r := range results {
		if strings.Contains(r.Source, "large.txt") {
			t.Errorf("large file should be skipped: %s", r.Source)
		}
	}
}

func TestGrepKnowledgeBases_ExcludePattern(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "keep.md"), []byte("hello world"), 0o644)
	os.WriteFile(filepath.Join(dir, "skip.log"), []byte("hello world"), 0o644)

	cfg := &config.Config{Knowledge: config.KnowledgeConfig{
		Enabled: true,
		Bases: []config.KnowledgeBase{
			{Name: "testex", Path: dir, Enabled: true, ExcludePatterns: []string{"*.log"}},
		},
	}}

	results := grepKnowledgeBases(context.Background(), cfg, "", "hello", 10)
	for _, r := range results {
		if strings.Contains(r.Source, "skip.log") {
			t.Errorf("excluded file should not appear: %s", r.Source)
		}
	}
	if len(results) == 0 {
		t.Fatal("expected at least keep.md match")
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
	if len(results) == 0 {
		t.Fatal("expected results from base1")
	}
	for _, r := range results {
		if strings.Contains(r.Source, "unique-xyz") {
			t.Errorf("base1 should not include base2 results: %s", r.Source)
		}
	}
}
