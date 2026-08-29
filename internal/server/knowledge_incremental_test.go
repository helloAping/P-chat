package server

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/knowledge"
)

func TestIndexScanIncrementalSkipsUnchangedAndReindexesChanged(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "a.md", "# A\n\n## Intro\nhello alpha")
	writeTestFile(t, dir, "b.md", "# B\n\n## Start\nhello beta")

	store, err := knowledge.NewWikiStore("kb", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	h := &Handler{}
	base := &config.KnowledgeBase{Name: "kb", Path: dir, Enabled: true}
	stats, err := h.indexScan(context.Background(), store, base, dir, "kb", nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Changed != 2 || stats.Skipped != 0 || stats.Failed != 0 || stats.L2 != 2 {
		t.Fatalf("first scan stats = %+v", stats)
	}
	overview, err := store.GetL1Overview(context.Background(), "kb")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(overview, "a.md") || !strings.Contains(overview, "b.md") {
		t.Fatalf("first scan should refresh L1 overview, got: %s", overview)
	}

	stats, err = h.indexScan(context.Background(), store, base, dir, "kb", nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Changed != 0 || stats.Skipped != 2 || stats.Failed != 0 || stats.L2 != 2 {
		t.Fatalf("second scan should skip unchanged files: %+v", stats)
	}

	time.Sleep(2 * time.Millisecond)
	writeTestFile(t, dir, "b.md", "# B\n\n## Changed\nhello gamma")
	stats, err = h.indexScan(context.Background(), store, base, dir, "kb", nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Changed != 1 || stats.Skipped != 1 || stats.Failed != 0 || stats.L2 != 2 {
		t.Fatalf("third scan should reindex one file: %+v", stats)
	}

	if err := os.Remove(filepath.Join(dir, "a.md")); err != nil {
		t.Fatal(err)
	}
	stats, err = h.indexScan(context.Background(), store, base, dir, "kb", nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Deleted != 1 || stats.L2 != 1 {
		t.Fatalf("delete scan should remove stale file: %+v", stats)
	}
	overview, err = store.GetL1Overview(context.Background(), "kb")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(overview, "a.md") || !strings.Contains(overview, "b.md") {
		t.Fatalf("delete scan should refresh L1 overview, got: %s", overview)
	}
}

func TestIndexScanHonorsKnowledgeBaseFilters(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, dir, "keep.md", "# K\n\n## OK\nalpha")
	writeTestFile(t, dir, "skip.txt", "# TXT\n\n## No\ntext")
	writeTestFile(t, dir, "large.md", "# Large\n\n## No\n"+strings.Repeat("x", 80))
	writeTestFile(t, dir, "ignored.md", "# Ignored\n\n## No\nroot")
	writeTestFile(t, dir, "docs/nested.md", "# Nested\n\n## No\nnested")

	base := &config.KnowledgeBase{
		Name:            "kb",
		Path:            dir,
		Enabled:         true,
		FileTypes:       []string{".md"},
		ExcludePatterns: []string{"ignored.md", `docs\**`},
		MaxFileSize:     32,
	}
	if got := countIndexableFiles(dir, base); got != 1 {
		t.Fatalf("countIndexableFiles() = %d, want 1", got)
	}

	store, err := knowledge.NewWikiStore("kb", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	h := &Handler{}
	stats, err := h.indexScan(context.Background(), store, base, dir, "kb", nil)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Changed != 1 || stats.Skipped != 0 || stats.Failed != 0 || stats.L2 != 1 {
		t.Fatalf("scan should index only keep.md: %+v", stats)
	}
	res, err := store.LookupSearch(context.Background(), "", "kb", false, 0, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 1 || len(res.Items) != 1 || res.Items[0].Source != "keep.md" {
		t.Fatalf("unexpected indexed files: %+v", res.Items)
	}
}

func TestParseKWAndOverviewChineseLabels(t *testing.T) {
	keywords, overview := parseKWAndOverview("关键词：alpha, beta\n内容概览：这是概览")
	if keywords != "alpha, beta" {
		t.Fatalf("keywords = %q", keywords)
	}
	if overview != "这是概览" {
		t.Fatalf("overview = %q", overview)
	}
}

func writeTestFile(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
