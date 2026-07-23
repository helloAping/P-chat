package knowledge

import (
	"context"
	"testing"
)

func TestFuseRRF_PrefersSharedTopHits(t *testing.T) {
	// Doc 1 tops both lists → highest RRF.
	// Doc 2 only in list A, doc 3 only in list B.
	listA := []searchHit{
		{item: IndexSearchItem{ID: 1, Title: "shared", MatchType: MatchTitle}, weight: 1},
		{item: IndexSearchItem{ID: 2, Title: "only-a", MatchType: MatchTitle}, weight: 1},
	}
	listB := []searchHit{
		{item: IndexSearchItem{ID: 1, Title: "shared", MatchType: MatchKeywords}, weight: 1},
		{item: IndexSearchItem{ID: 3, Title: "only-b", MatchType: MatchKeywords}, weight: 1},
	}
	got := fuseRRF([][]searchHit{listA, listB})
	if len(got) != 3 {
		t.Fatalf("len=%d want 3", len(got))
	}
	if got[0].item.ID != 1 {
		t.Errorf("first id=%d want 1 (shared top)", got[0].item.ID)
	}
	// Title outranks keywords for the shared hit.
	if got[0].item.MatchType != MatchTitle {
		t.Errorf("match_type=%q want title", got[0].item.MatchType)
	}
}

func TestFuseRRF_PathBeatsContent(t *testing.T) {
	pathList := []searchHit{
		{item: IndexSearchItem{ID: 10, Title: "config.go", MatchType: MatchFilename}, weight: 1.3},
	}
	contentList := []searchHit{
		{item: IndexSearchItem{ID: 11, Title: "unrelated", MatchType: MatchContent}, weight: 0.2},
		{item: IndexSearchItem{ID: 10, Title: "config.go", MatchType: MatchContent}, weight: 0.2},
	}
	got := fuseRRF([][]searchHit{pathList, contentList})
	if got[0].item.ID != 10 {
		t.Fatalf("first=%d want 10", got[0].item.ID)
	}
	if got[0].item.MatchType != MatchFilename {
		t.Errorf("match_type=%q want filename", got[0].item.MatchType)
	}
}

func TestLookupSearch_PathExact(t *testing.T) {
	ws := newTestWikiStore(t)
	defer ws.Close()
	// Seed a code-like path that FTS may not prioritise.
	ctx := context.Background()
	nodes := []IndexNode{
		{ID: 1, ParentID: 0, Base: "hybrid-path", Level: 1, Title: "hybrid-path"},
		{ID: 2, ParentID: 1, Base: "hybrid-path", Level: 2, Source: "internal/config/config.go",
			Kind: "text", Title: "config.go", Keywords: "yaml", Overview: "config loader"},
		{ID: 3, ParentID: 2, Base: "hybrid-path", Level: 3, Source: "internal/config/config.go",
			Kind: "text", Title: "LoadConfig", Keywords: "yaml, load", Overview: "loads yaml config"},
	}
	contents := []ContentNode{
		{NodeID: 3, Content: "func LoadConfig() error { ... }", ContentType: "text"},
	}
	if err := ws.ReplaceBaseNodes(ctx, "hybrid-path", nodes, contents); err != nil {
		t.Fatal(err)
	}

	res, err := ws.LookupSearch(ctx, "config.go", "hybrid-path", false, 0, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total < 1 {
		t.Fatal("expected path hit for config.go")
	}
	// Top hit should be path/filename related.
	top := res.Items[0]
	if top.MatchType != MatchFilename && top.MatchType != MatchPath && top.MatchType != MatchTitle {
		t.Errorf("top match_type=%q want path/filename/title, item=%+v", top.MatchType, top)
	}
	if !contains(top.Source, "config.go") && !contains(top.Title, "config") {
		t.Errorf("top hit should reference config.go: %+v", top)
	}
}

func TestLookupSearch_MatchTypePresent(t *testing.T) {
	ws := newTestWikiStore(t)
	defer ws.Close()
	createTestIndexData(t, ws, "hybrid-mt")

	res, err := ws.LookupSearch(context.Background(), "oauth", "hybrid-mt", false, 0, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if res.Total < 1 {
		t.Fatal("expected oauth hit")
	}
	// At least one result should carry a non-empty match_type.
	has := false
	for _, it := range res.Items {
		if it.MatchType != "" {
			has = true
			break
		}
	}
	if !has {
		t.Errorf("expected MatchType on hybrid results: %+v", res.Items)
	}
}
