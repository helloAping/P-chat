package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/knowledge"
)

func TestSearchKnowledgeReportsStatsAndCapsOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dir := t.TempDir()
	baseName := "search_stats"
	store, err := knowledge.NewWikiStore(baseName, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		store.Close()
		knowledge.CloseWikiStore()
	})

	nodes := []knowledge.IndexNode{
		{ID: 1, ParentID: 0, Base: baseName, Level: 1, Title: baseName, Overview: "[Knowledge Base]"},
		{ID: 2, ParentID: 1, Base: baseName, Level: 2, Source: "guide.md", Kind: "text", Title: "guide.md", Overview: "alpha guide"},
		{ID: 3, ParentID: 2, Base: baseName, Level: 3, Source: "guide.md", Kind: "text", Title: "Alpha", Keywords: "alpha", Overview: "alpha overview"},
	}
	contents := []knowledge.ContentNode{
		{NodeID: 3, Content: strings.Repeat("正文", 1200), ContentType: "text", SortOrder: 0},
	}
	if err := store.ReplaceBaseNodes(context.Background(), baseName, nodes, contents); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{Knowledge: config.KnowledgeConfig{
		Enabled: true,
		Bases:   []config.KnowledgeBase{{Name: baseName, Path: dir, Enabled: true}},
	}}
	h := &Handler{}
	h.cfg.Store(cfg)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/knowledge/search", strings.NewReader(`{"query":"alpha","top_k":999}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.SearchKnowledge(c)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		Results []struct {
			Content          string `json:"content"`
			ContentTruncated bool   `json:"content_truncated"`
			ContentFullChars int    `json:"content_full_chars"`
		} `json:"results"`
		Stats struct {
			TopK             int  `json:"top_k"`
			RequestedTopK    int  `json:"requested_top_k"`
			TopKCapped       bool `json:"top_k_capped"`
			ContentTruncated int  `json:"content_truncated"`
			Returned         int  `json:"returned"`
		} `json:"stats"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Stats.TopK != 50 || resp.Stats.RequestedTopK != 999 || !resp.Stats.TopKCapped {
		t.Fatalf("unexpected stats: %+v", resp.Stats)
	}
	if resp.Stats.Returned != len(resp.Results) || len(resp.Results) == 0 {
		t.Fatalf("unexpected returned count: stats=%+v results=%d", resp.Stats, len(resp.Results))
	}
	foundTruncated := false
	for _, result := range resp.Results {
		if result.ContentTruncated {
			foundTruncated = true
			if result.ContentFullChars <= len([]rune(result.Content)) {
				t.Fatalf("expected full length metadata for truncated content: %+v", result)
			}
		}
	}
	if !foundTruncated {
		t.Fatalf("expected at least one truncated result: %+v", resp.Results)
	}
	if resp.Stats.ContentTruncated != 1 {
		t.Fatalf("content_truncated stats = %d, want 1", resp.Stats.ContentTruncated)
	}
}

func TestGrepKBRespectsBaseFilterAndExcludePatterns(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	writeKBSearchTestFile(t, dirA, "keep.md", "needle keep")
	writeKBSearchTestFile(t, dirA, "docs/skip.md", "needle skip")
	writeKBSearchTestFile(t, dirB, "other.md", "needle other")

	cfg := &config.Config{Knowledge: config.KnowledgeConfig{
		Enabled: true,
		Bases: []config.KnowledgeBase{
			{Name: "a", Path: dirA, Enabled: true, ExcludePatterns: []string{`docs\**`}},
			{Name: "b", Path: dirB, Enabled: true},
		},
	}}

	got := grepKB(cfg, "needle", 10, map[string]bool{"a": true})
	if len(got) != 1 {
		t.Fatalf("len(got) = %d, want 1: %+v", len(got), got)
	}
	if got[0].Path != "keep.md" || strings.Contains(got[0].Content, "skip") || strings.Contains(got[0].Content, "other") {
		t.Fatalf("unexpected grep result: %+v", got[0])
	}
}

func writeKBSearchTestFile(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
