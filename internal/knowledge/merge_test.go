package knowledge

import "testing"

func TestMergeAndRerank_NormalisesPerBase(t *testing.T) {
	// Base A has inflated raw ranks; base B has small ranks but a
	// relatively better top hit. After per-base max-normalisation the
	// best of each base should both land near the top.
	items := []IndexSearchItem{
		{Base: "A", Source: "a/foo.md", Title: "A-weak", Rank: 50},
		{Base: "A", Source: "a/bar.md", Title: "A-strong", Rank: 100},
		{Base: "B", Source: "b/baz.md", Title: "B-strong", Rank: 2},
		{Base: "B", Source: "b/qux.md", Title: "B-weak", Rank: 1},
	}
	got := MergeAndRerank(items, MergeOptions{TopK: 10})
	if len(got) != 4 {
		t.Fatalf("len=%d want 4", len(got))
	}
	// A-strong and B-strong both normalised to 1.0 — stable sort keeps
	// original relative order among equals (A before B).
	if got[0].Title != "A-strong" {
		t.Errorf("first = %q, want A-strong", got[0].Title)
	}
	if got[1].Title != "B-strong" {
		t.Errorf("second = %q, want B-strong", got[1].Title)
	}
	// Normalised ranks: strong ≈ 1, weak ≈ 0.5
	if got[0].Rank < 0.99 {
		t.Errorf("A-strong rank=%v want ~1", got[0].Rank)
	}
	if got[2].Rank < 0.4 || got[2].Rank > 0.6 {
		t.Errorf("weak ranks should be ~0.5, got %v / %v", got[2].Rank, got[3].Rank)
	}
}

func TestMergeAndRerank_DedupesSameSourceTitle(t *testing.T) {
	items := []IndexSearchItem{
		{Base: "A", Source: `C:\docs\Guide.md`, Title: "Intro", Rank: 0.9},
		{Base: "B", Source: `c:/docs/Guide.md`, Title: "Intro", Rank: 0.3}, // same file, lower score
		{Base: "B", Source: "other.md", Title: "Other", Rank: 0.5},
	}
	got := MergeAndRerank(items, MergeOptions{TopK: 10})
	if len(got) != 2 {
		t.Fatalf("len=%d want 2 after dedupe (got %#v)", len(got), titles(got))
	}
	// Higher-score base A wins the dedupe.
	if got[0].Base != "A" {
		t.Errorf("dedupe winner base=%q want A", got[0].Base)
	}
}

func TestMergeAndRerank_TopK(t *testing.T) {
	var items []IndexSearchItem
	for i := 0; i < 20; i++ {
		items = append(items, IndexSearchItem{
			Base: "X", Source: itoa(i) + ".md", Title: itoa(i), Rank: float64(i),
		})
	}
	got := MergeAndRerank(items, MergeOptions{TopK: 5})
	if len(got) != 5 {
		t.Fatalf("len=%d want 5", len(got))
	}
	// Highest rank first.
	if got[0].Title != "19" {
		t.Errorf("first title=%q want 19", got[0].Title)
	}
}

func TestMergeAndRerank_BaseWeights(t *testing.T) {
	items := []IndexSearchItem{
		{Base: "primary", Source: "p.md", Title: "P", Rank: 1},
		{Base: "secondary", Source: "s.md", Title: "S", Rank: 1},
	}
	got := MergeAndRerank(items, MergeOptions{
		TopK: 10,
		BaseWeights: map[string]float64{
			"primary":   1.5,
			"secondary": 0.5,
		},
	})
	if got[0].Base != "primary" {
		t.Errorf("weighted first base=%q want primary", got[0].Base)
	}
	if got[0].Rank <= got[1].Rank {
		t.Errorf("primary score %v should exceed secondary %v", got[0].Rank, got[1].Rank)
	}
}

func TestTagBase(t *testing.T) {
	items := []IndexSearchItem{{Title: "x"}, {Title: "y", Base: "keep"}}
	got := TagBase(items, "docs")
	if got[0].Base != "docs" {
		t.Errorf("got[0].Base=%q", got[0].Base)
	}
	if got[1].Base != "keep" {
		t.Errorf("existing base overwritten: %q", got[1].Base)
	}
	// Input not mutated.
	if items[0].Base != "" {
		t.Error("TagBase mutated input")
	}
}

func TestMergeAndRerank_Empty(t *testing.T) {
	if got := MergeAndRerank(nil, MergeOptions{}); got != nil {
		t.Errorf("nil in → %v", got)
	}
}

func titles(items []IndexSearchItem) []string {
	out := make([]string, len(items))
	for i, it := range items {
		out[i] = it.Base + ":" + it.Title
	}
	return out
}
