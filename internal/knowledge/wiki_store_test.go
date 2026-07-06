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

// ── Three-level index store tests ──

func createTestIndexData(t *testing.T, ws *WikiStore, base string) {
	t.Helper()
	ctx := context.Background()

	l1 := IndexNode{ID: 1, ParentID: 0, Base: base, Level: 1, Title: base,
		Keywords: "test, kb", Overview: "[Knowledge Base] (2 files)\n· guide.md — 2 章节: Getting Started, Auth\n· api.md — 1 章节: Config\n"}
	l2a := IndexNode{ID: 2, ParentID: 1, Base: base, Level: 2, Source: "guide.md", Kind: "text",
		SortOrder: 0, Title: "guide.md", Keywords: "guide, started",
		Overview: "guide.md — 2 章节: Getting Started, Authentication"}
	l2b := IndexNode{ID: 3, ParentID: 1, Base: base, Level: 2, Source: "api.md", Kind: "text",
		SortOrder: 1, Title: "api.md", Keywords: "api, config",
		Overview: "api.md — 1 章节: Configuration"}
	l3a := IndexNode{ID: 4, ParentID: 2, Base: base, Level: 3, Source: "guide.md", Kind: "text",
		SortOrder: 0, Title: "Getting Started", Keywords: "guide, quickstart, intro",
		Overview: "Quick start guide for new users."}
	l3b := IndexNode{ID: 5, ParentID: 2, Base: base, Level: 3, Source: "guide.md", Kind: "text",
		SortOrder: 1, Title: "Authentication", Keywords: "auth, login, JWT, OAuth",
		Overview: "Authentication flow using JWT and OAuth2."}
	l3c := IndexNode{ID: 6, ParentID: 3, Base: base, Level: 3, Source: "api.md", Kind: "text",
		SortOrder: 0, Title: "Configuration", Keywords: "config, yaml, env",
		Overview: "System configuration via YAML and environment variables."}

	nodes := []IndexNode{l1, l2a, l2b, l3a, l3b, l3c}
	contents := []ContentNode{
		{NodeID: 4, Content: "This is the getting started guide.\nFollow these steps...", ContentType: "text", SortOrder: 0},
		{NodeID: 5, Content: "JWT authentication: generate token with...", ContentType: "text", SortOrder: 0},
		{NodeID: 6, Content: "Configuration: set YAML file or env vars...", ContentType: "text", SortOrder: 0},
	}
	if err := ws.ReplaceBaseNodes(ctx, base, nodes, contents); err != nil {
		t.Fatal(err)
	}
}

func TestIndexStore_LookupSearch_EmptyQuery_ListsL2(t *testing.T) {
	ws := newTestWikiStore(t)
	defer ws.Close()
	createTestIndexData(t, ws, "testempty")

	res, err := ws.LookupSearch(context.Background(), "", "testempty", false, 0, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 2 {
		t.Fatalf("want 2 L2 items, got %d", res.Total)
	}
	if res.Items[0].Title != "guide.md" || res.Items[1].Title != "api.md" {
		t.Errorf("unexpected L2 order: %v, %v", res.Items[0].Title, res.Items[1].Title)
	}
}

func TestIndexStore_LookupSearch_KeywordMatch(t *testing.T) {
	ws := newTestWikiStore(t)
	defer ws.Close()
	createTestIndexData(t, ws, "testkw")

	// Search by keyword "oauth" → should match L3 Authentication
	res, err := ws.LookupSearch(context.Background(), "oauth", "testkw", false, 0, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total < 1 {
		t.Fatal("expected at least 1 result for keyword 'oauth'")
	}
	found := false
	for _, it := range res.Items {
		if contains(it.Title, "Authentication") || contains(it.Keywords, "OAuth") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("keyword search for 'oauth' should find Authentication: %+v", res.Items)
	}
}

func TestIndexStore_LookupSearch_TitleMatch(t *testing.T) {
	ws := newTestWikiStore(t)
	defer ws.Close()
	createTestIndexData(t, ws, "testtitle")

	// Search by title "Getting" → should match L3
	res, err := ws.LookupSearch(context.Background(), "Getting", "testtitle", false, 0, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total < 1 {
		t.Fatal("expected at least 1 result for title 'Getting'")
	}
	found := false
	for _, it := range res.Items {
		if contains(it.Title, "Getting Started") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("title search for 'Getting' should find Getting Started: %+v", res.Items)
	}
}

func TestIndexStore_LookupSearch_Expand(t *testing.T) {
	ws := newTestWikiStore(t)
	defer ws.Close()
	createTestIndexData(t, ws, "testexpand")

	res, err := ws.LookupSearch(context.Background(), "quickstart", "testexpand", true, 0, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total < 1 {
		t.Fatal("expected at least 1 result")
	}
	foundContent := false
	for _, it := range res.Items {
		if len(it.Children) > 0 {
			foundContent = true
			if !contains(it.Children[0].Content, "getting started") {
				t.Errorf("expanded content missing expected text: %s", it.Children[0].Content)
			}
			break
		}
	}
	if !foundContent {
		t.Error("expand=true with L3 match should include children")
	}
}

func TestIndexStore_LookupSearch_Pagination(t *testing.T) {
	ws := newTestWikiStore(t)
	defer ws.Close()
	createTestIndexData(t, ws, "testpage")

	// page 1, size 1
	res, err := ws.LookupSearch(context.Background(), "", "testpage", false, 0, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Items) != 1 {
		t.Fatalf("size=1 should return 1 item, got %d", len(res.Items))
	}
	if !res.HasMore {
		t.Error("hasMore should be true for page=1 size=1 with 2 total")
	}

	// page 2, size 1
	res2, err := ws.LookupSearch(context.Background(), "", "testpage", false, 0, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Items) != 1 {
		t.Fatalf("page=2 should return 1 item, got %d", len(res2.Items))
	}
	if res2.HasMore {
		t.Error("hasMore should be false for last page")
	}
	if res.Items[0].Title == res2.Items[0].Title {
		t.Error("page 1 and page 2 should return different items")
	}
}

func TestIndexStore_ListChildren(t *testing.T) {
	ws := newTestWikiStore(t)
	defer ws.Close()
	createTestIndexData(t, ws, "testchild")

	// List L1's children (L2 nodes: id=1 → children are 2 and 3).
	res, err := ws.ListChildren(context.Background(), 1, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 2 {
		t.Fatalf("L1 should have 2 children (L2s), got %d", res.Total)
	}

	// List L2's children (L3 nodes under guide.md: id=2 → children are 4 and 5).
	res, err = ws.ListChildren(context.Background(), 2, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 2 {
		t.Fatalf("L2 should have 2 children (L3s), got %d", res.Total)
	}
}

func TestIndexStore_GetL1Overview(t *testing.T) {
	ws := newTestWikiStore(t)
	defer ws.Close()
	createTestIndexData(t, ws, "testl1")

	overview, err := ws.GetL1Overview(context.Background(), "testl1")
	if err != nil {
		t.Fatal(err)
	}
	if !contains(overview, "guide.md") || !contains(overview, "api.md") {
		t.Errorf("L1 overview missing file references: %s", overview)
	}
}

func TestIndexStore_MigrateFromWikiSections(t *testing.T) {
	ws := newTestWikiStore(t)
	defer ws.Close()

	ctx := context.Background()
	base := "testmigrate"

	// Insert legacy wiki_sections data.
	legacy := []WikiSection{
		{Title: "Intro", Content: "Introduction to the system.", Source: "intro.md", Base: base},
		{Title: "Setup", Content: "Setup instructions.", Source: "intro.md", Base: base},
		{Title: "API", Content: "API reference page.", Source: "ref.md", Base: base},
	}
	if err := ws.ReplaceBase(ctx, base, legacy); err != nil {
		t.Fatal(err)
	}

	// Migrate.
	count, err := ws.MigrateBaseToIndex(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Fatal("migration should produce nodes")
	}

	// Verify: L1 overview exists.
	overview, err := ws.GetL1Overview(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(overview, "intro.md") || !contains(overview, "ref.md") {
		t.Errorf("L1 overview missing sources after migration: %s", overview)
	}

	// Verify: search works on migrated data.
	res, err := ws.LookupSearch(ctx, "Intro", base, false, 0, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total < 1 {
		t.Fatal("search should find migrated data")
	}

	// Verify: migration is idempotent.
	count2, _ := ws.MigrateBaseToIndex(ctx, base)
	if count2 <= 0 {
		t.Error("second migration should return existing count")
	}
}

func TestIndexStore_EmptySearch(t *testing.T) {
	ws := newTestWikiStore(t)
	defer ws.Close()

	res, err := ws.LookupSearch(context.Background(), "nothing", "nonexistent", false, 0, 1, 20)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 0 {
		t.Errorf("empty base should have 0 results, got %d", res.Total)
	}
}
