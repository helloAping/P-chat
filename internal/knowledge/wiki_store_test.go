package knowledge

import (
	"context"
	"testing"
)

func newTestWikiStore(t *testing.T) *WikiStore {
	t.Helper()
	dir := t.TempDir()
	ws, err := NewWikiStore("test", dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.Close() })
	return ws
}

func TestNewWikiStore_CreatesTables(t *testing.T) {
	ws := newTestWikiStore(t)
	var cnt int
	err := ws.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='wiki_sections'`).Scan(&cnt)
	if err != nil {
		t.Fatal(err)
	}
	if cnt != 1 {
		t.Errorf("wiki_sections table missing")
	}
	err = ws.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='media_cache'`).Scan(&cnt)
	if err != nil {
		t.Fatal(err)
	}
	if cnt != 1 {
		t.Errorf("media_cache table missing")
	}
	err = ws.db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='file_mtimes'`).Scan(&cnt)
	if err != nil {
		t.Fatal(err)
	}
	if cnt != 1 {
		t.Errorf("file_mtimes table missing")
	}
}

func TestReplaceBase_InsertAndList(t *testing.T) {
	ws := newTestWikiStore(t)
	ctx := context.Background()

	sections := []WikiSection{
		{Title: "Section A", Content: "Content A", Source: "doc.md", Base: "test"},
		{Title: "Section B", Content: "Content B", Source: "doc.md", Base: "test"},
		{Title: "Section C", Content: "Content C", Source: "other.md", Base: "test"},
	}
	if err := ws.ReplaceBase(ctx, "test", sections); err != nil {
		t.Fatal(err)
	}

	all, err := ws.ListBase(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("want 3 sections, got %d", len(all))
	}
}

func TestReplaceBase_OverwritesPreviousData(t *testing.T) {
	ws := newTestWikiStore(t)
	ctx := context.Background()

	sections1 := []WikiSection{
		{Title: "Old", Content: "Old content", Source: "old.md", Base: "test"},
	}
	ws.ReplaceBase(ctx, "test", sections1)

	sections2 := []WikiSection{
		{Title: "New", Content: "New content", Source: "new.md", Base: "test"},
	}
	if err := ws.ReplaceBase(ctx, "test", sections2); err != nil {
		t.Fatal(err)
	}

	all, err := ws.ListBase(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("want 1 section after replace, got %d", len(all))
	}
	if all[0].Title != "New" {
		t.Errorf("title = %q", all[0].Title)
	}
}

func TestReplaceSource_IncrementalUpdate(t *testing.T) {
	ws := newTestWikiStore(t)
	ctx := context.Background()

	ws.ReplaceBase(ctx, "test", []WikiSection{
		{Title: "A", Content: "Content A", Source: "doc.md", Base: "test"},
		{Title: "B", Content: "Content B", Source: "other.md", Base: "test"},
	})

	if err := ws.ReplaceSource(ctx, "test", "doc.md", []WikiSection{
		{Title: "A-v2", Content: "Updated A", Source: "doc.md", Base: "test"},
	}); err != nil {
		t.Fatal(err)
	}

	all, err := ws.ListBase(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("want 2 sections after source replace, got %d", len(all))
	}

	for _, s := range all {
		if s.Source == "doc.md" && s.Title != "A-v2" {
			t.Errorf("source doc.md not updated: title=%q", s.Title)
		}
	}
}

func TestSearchFTS_Basic(t *testing.T) {
	ws := newTestWikiStore(t)
	ctx := context.Background()

	ws.ReplaceBase(ctx, "test", []WikiSection{
		{Title: "User Authentication", Content: "How to set up JWT auth", Source: "auth.md", Base: "test"},
		{Title: "SSE Streaming", Content: "Server-sent events guide", Source: "sse.md", Base: "test"},
		{Title: "Deployment", Content: "Docker and k8s config", Source: "deploy.md", Base: "test"},
	})

	// Search by title
	results, err := ws.SearchFTS(ctx, "Authentication", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for 'Authentication'")
	}

	// Search by content
	results, err = ws.SearchFTS(ctx, "Docker", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for 'Docker'")
	}
}

func TestSearchFTS_NoResults(t *testing.T) {
	ws := newTestWikiStore(t)
	ctx := context.Background()

	ws.ReplaceBase(ctx, "test", []WikiSection{
		{Title: "Hello", Content: "World", Source: "x.md", Base: "test"},
	})

	results, err := ws.SearchFTS(ctx, "zzzzzzznonexistent", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("want 0 results, got %d", len(results))
	}
}

func TestFileMtime(t *testing.T) {
	ws := newTestWikiStore(t)
	ctx := context.Background()

	mtime, err := ws.GetFileMtime(ctx, "test", "doc.md")
	if err != nil {
		t.Fatal(err)
	}
	if mtime != 0 {
		t.Errorf("initial mtime should be 0, got %d", mtime)
	}

	if err := ws.SetFileMtime(ctx, "test", "doc.md", 12345); err != nil {
		t.Fatal(err)
	}

	mtime, err = ws.GetFileMtime(ctx, "test", "doc.md")
	if err != nil {
		t.Fatal(err)
	}
	if mtime != 12345 {
		t.Errorf("want 12345, got %d", mtime)
	}
}

func TestRemoveStaleSources(t *testing.T) {
	ws := newTestWikiStore(t)
	ctx := context.Background()

	ws.ReplaceBase(ctx, "test", []WikiSection{
		{Title: "A", Content: "a", Source: "keep.md", Base: "test"},
		{Title: "B", Content: "b", Source: "stale.md", Base: "test"},
	})

	ws.SetFileMtime(ctx, "test", "keep.md", 100)
	ws.SetFileMtime(ctx, "test", "stale.md", 200)

	// Only keep.md exists now
	currentSources := map[string]bool{"keep.md": true}
	if err := ws.RemoveStaleSources(ctx, "test", currentSources); err != nil {
		t.Fatal(err)
	}

	all, err := ws.ListBase(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 {
		t.Fatalf("want 1 section after stale removal, got %d", len(all))
	}
	if all[0].Title != "A" {
		t.Errorf("expected 'A', got %q", all[0].Title)
	}

	// Mtime for stale source should also be removed.
	mtime, _ := ws.GetFileMtime(ctx, "test", "stale.md")
	if mtime != 0 {
		t.Errorf("stale mtime not cleaned, got %d", mtime)
	}
}

func TestMediaCache(t *testing.T) {
	ws := newTestWikiStore(t)
	ctx := context.Background()

	desc, err := ws.GetCachedMediaDescription(ctx, "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if desc != "" {
		t.Errorf("expected empty for uncached key")
	}

	if err := ws.CacheMediaDescription(ctx, "abc123", "A cat sitting on a chair"); err != nil {
		t.Fatal(err)
	}

	desc, err = ws.GetCachedMediaDescription(ctx, "abc123")
	if err != nil {
		t.Fatal(err)
	}
	if desc != "A cat sitting on a chair" {
		t.Errorf("want 'A cat sitting on a chair', got %q", desc)
	}
}

func TestIndex(t *testing.T) {
	ws := newTestWikiStore(t)
	ctx := context.Background()

	ws.ReplaceBase(ctx, "test", []WikiSection{
		{Title: "Getting Started", Content: "...", Source: "guide.md", Base: "test"},
		{Title: "API Reference", Content: "...", Source: "api.md", Base: "test"},
		{Title: "Configuration", Content: "...", Source: "api.md", Base: "test"},
	})

	idx, err := ws.Index(ctx, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(idx, "Getting Started") {
		t.Errorf("index missing 'Getting Started': %s", idx)
	}
	if !contains(idx, "api.md") {
		t.Errorf("index missing 'api.md': %s", idx)
	}
}

func TestAppendSections(t *testing.T) {
	ws := newTestWikiStore(t)
	ctx := context.Background()

	ws.ReplaceBase(ctx, "test", []WikiSection{
		{Title: "Existing", Content: "old", Source: "a.md", Base: "test"},
	})

	if err := ws.AppendSections(ctx, []WikiSection{
		{Title: "New", Content: "new", Source: "b.md", Base: "test"},
	}); err != nil {
		t.Fatal(err)
	}

	all, err := ws.ListBase(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 2 {
		t.Fatalf("want 2 sections after append, got %d", len(all))
	}
}

func TestGetOrOpenWikiStore_Cache(t *testing.T) {
	dir := t.TempDir()
	ws1, err := GetOrOpenWikiStore("testcache", dir)
	if err != nil {
		t.Fatal(err)
	}
	defer ws1.Close()
	defer CloseWikiStore()

	ws2, err := GetOrOpenWikiStore("testcache", dir)
	if err != nil {
		t.Fatal(err)
	}
	if ws1 != ws2 {
		t.Error("expected same store from cache")
	}
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && len(s) >= len(substr) &&
		(s == substr || len(s) > len(substr) && hasSubstr(s, substr))
}

func hasSubstr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
