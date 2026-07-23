package server

import (
	"context"
	"os"
	"path/filepath"
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
}

func writeTestFile(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
