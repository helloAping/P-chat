package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	_ "modernc.org/sqlite"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/knowledge"
)

func setupKnowledgeTest(t *testing.T, baseName string) (*config.Config, func()) {
	t.Helper()
	dir := t.TempDir()

	store, err := knowledge.NewWikiStore(baseName, dir)
	if err != nil {
		t.Fatal(err)
	}

	sections := []knowledge.WikiSection{
		{Title: "Getting Started", Content: "This is a getting started guide.", Source: "guide.md", Base: baseName},
		{Title: "API Reference", Content: "Detailed API reference for all endpoints.", Source: "api.md", Base: baseName},
		{Title: "Configuration", Content: "How to configure the system.", Source: "config.md", Base: baseName},
		{Title: "部署指南", Content: "如何在生产环境部署。", Source: "deploy.md", Base: baseName},
	}
	if err := store.ReplaceBase(context.Background(), baseName, sections); err != nil {
		t.Fatal(err)
	}
	store.Close()
	knowledge.CloseWikiStore()

	cfg := &config.Config{Knowledge: config.KnowledgeConfig{
		Enabled: true,
		Bases: []config.KnowledgeBase{
			{Name: baseName, Path: dir, Enabled: true},
		},
	}}
	return cfg, func() { knowledge.CloseWikiStore() }
}

func TestWikiLookup_Basic(t *testing.T) {
	cfg, cleanup := setupKnowledgeTest(t, "test_basic")
	defer cleanup()
	handler := makeWikiLookupHandler(cfg)

	args, _ := json.Marshal(wikiLookupArgs{Title: "Getting Started", TopK: 5})
	res, err := handler(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("unexpected error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "Getting Started") {
		t.Errorf("result missing title: %s", res.Content)
	}
}

func TestWikiLookup_EmptyTitle(t *testing.T) {
	cfg, cleanup := setupKnowledgeTest(t, "test_empty")
	defer cleanup()
	handler := makeWikiLookupHandler(cfg)

	args, _ := json.Marshal(wikiLookupArgs{Title: ""})
	res, _ := handler(context.Background(), args)
	if !res.IsError {
		t.Error("empty title should return error")
	}
}

func TestWikiLookup_NoResults(t *testing.T) {
	cfg, cleanup := setupKnowledgeTest(t, "test_none")
	defer cleanup()
	handler := makeWikiLookupHandler(cfg)

	args, _ := json.Marshal(wikiLookupArgs{Title: "zzzzzzNonexistentTitle"})
	res, err := handler(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Errorf("no results should not be an error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "未找到") {
		t.Errorf("expected '未找到' for no results, got: %s", res.Content)
	}
}

func TestWikiLookup_CJKTitle(t *testing.T) {
	cfg, cleanup := setupKnowledgeTest(t, "test_cjk")
	defer cleanup()
	handler := makeWikiLookupHandler(cfg)

	args, _ := json.Marshal(wikiLookupArgs{Title: "部署", TopK: 5})
	res, err := handler(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "部署指南") {
		t.Errorf("CJK search failed: %s", res.Content)
	}
}

func TestWikiLookup_KnowledgeDisabled(t *testing.T) {
	cfg, cleanup := setupKnowledgeTest(t, "test_disabled")
	defer cleanup()
	cfg.Knowledge.Enabled = false
	handler := makeWikiLookupHandler(cfg)

	args, _ := json.Marshal(wikiLookupArgs{Title: "test"})
	res, _ := handler(context.Background(), args)
	if !res.IsError {
		t.Error("disabled knowledge should return error")
	}
}

func TestWikiIndex_Basic(t *testing.T) {
	cfg, cleanup := setupKnowledgeTest(t, "test_idx")
	defer cleanup()
	handler := makeWikiIndexHandler(cfg)

	args, _ := json.Marshal(wikiIndexArgs{})
	res, err := handler(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "Getting Started") {
		t.Errorf("index missing title: %s", res.Content)
	}
	if !strings.Contains(res.Content, "API Reference") {
		t.Errorf("index missing title: %s", res.Content)
	}
}

func TestWikiIndex_FilterBySource(t *testing.T) {
	cfg, cleanup := setupKnowledgeTest(t, "test_idx_src")
	defer cleanup()
	handler := makeWikiIndexHandler(cfg)

	args, _ := json.Marshal(wikiIndexArgs{Source: "guide.md"})
	res, err := handler(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "Getting Started") {
		t.Errorf("filtered index missing title: %s", res.Content)
	}
	if strings.Contains(res.Content, "API Reference") {
		t.Errorf("filtered index should not contain API Reference: %s", res.Content)
	}
}

func TestResolveBases_All(t *testing.T) {
	kc := config.KnowledgeConfig{
		Bases: []config.KnowledgeBase{
			{Name: "a", Enabled: true},
			{Name: "b", Enabled: false},
			{Name: "c", Enabled: true},
		},
	}

	all := resolveBases(kc, "")
	if len(all) != 2 {
		t.Errorf("want 2 enabled bases, got %d", len(all))
	}

	all = resolveBases(kc, "__all__")
	if len(all) != 2 {
		t.Errorf("__all__: want 2, got %d", len(all))
	}
}

func TestResolveBases_Specific(t *testing.T) {
	kc := config.KnowledgeConfig{
		Bases: []config.KnowledgeBase{
			{Name: "a", Enabled: true},
			{Name: "b", Enabled: true},
		},
	}

	result := resolveBases(kc, "b")
	if len(result) != 1 || result[0].Name != "b" {
		t.Errorf("want base 'b', got %v", result)
	}

	result = resolveBases(kc, "nonexistent")
	if len(result) != 0 {
		t.Errorf("nonexistent should return 0 bases, got %d", len(result))
	}

	result = resolveBases(kc, "a")
	if len(result) != 1 || !result[0].Enabled {
		t.Errorf("want enabled 'a', got %v", result)
	}
}
