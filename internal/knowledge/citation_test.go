package knowledge

import "testing"

func TestBuildCitationExplainsParentQueryAndMatchType(t *testing.T) {
	it := IndexSearchItem{
		Base:      "docs",
		Source:    "guide.md",
		Title:     "Auth",
		Level:     3,
		Kind:      "text",
		Rank:      0.75,
		Query:     "OAuth",
		MatchType: MatchKeywords,
		Parent:    &NodeRef{Title: "guide.md"},
	}
	got := BuildCitation(it)
	if got.Base != "docs" || got.Source != "guide.md" || got.ParentTitle != "guide.md" {
		t.Fatalf("bad citation metadata: %#v", got)
	}
	if got.Explanation == "" {
		t.Fatal("expected explanation")
	}
	if !contains(got.Explanation, "OAuth") || !contains(got.Explanation, "关键词") {
		t.Fatalf("explanation missing query/match type: %q", got.Explanation)
	}
}

func TestBuildCitationFallback(t *testing.T) {
	got := BuildCitation(IndexSearchItem{Source: "x.md"})
	if got.Explanation == "" {
		t.Fatal("expected fallback explanation")
	}
}
