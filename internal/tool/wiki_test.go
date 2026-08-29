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

func TestWikiLookup_EmptyBase(t *testing.T) {
	cfg, cleanup := setupKnowledgeTest(t, "test_empty_base")
	defer cleanup()
	handler := makeWikiLookupHandler(cfg)

	args, _ := json.Marshal(wikiLookupArgs{Query: "test", Page: 1, Size: 5})
	res, err := handler(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "知识库为空") {
		t.Errorf("empty base should return empty message, got: %s", res.Content)
	}
}

func TestWikiLookup_DefaultPageSize(t *testing.T) {
	cfg, cleanup := setupKnowledgeTest(t, "test_defaults")
	defer cleanup()
	handler := makeWikiLookupHandler(cfg)

	args, _ := json.Marshal(wikiLookupArgs{Query: "test"})
	res, err := handler(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(res.Content, "知识库为空") {
		t.Errorf("expected empty message, got: %s", res.Content)
	}
}

func TestWikiLookup_KnowledgeDisabled(t *testing.T) {
	cfg, cleanup := setupKnowledgeTest(t, "test_disabled")
	defer cleanup()
	cfg.Knowledge.Enabled = false
	handler := makeWikiLookupHandler(cfg)

	args, _ := json.Marshal(wikiLookupArgs{Query: "test"})
	res, _ := handler(context.Background(), args)
	if !res.IsError {
		t.Error("disabled knowledge should return error")
	}
}

func TestWikiList_MissingParentID(t *testing.T) {
	cfg, cleanup := setupKnowledgeTest(t, "test_list")
	defer cleanup()
	handler := makeWikiListHandler(cfg)

	args, _ := json.Marshal(wikiListArgs{})
	res, _ := handler(context.Background(), args)
	if !res.IsError {
		t.Error("missing parent_id should return error")
	}
}

func TestWikiList_DisabledKnowledge(t *testing.T) {
	cfg, cleanup := setupKnowledgeTest(t, "test_list_disabled")
	defer cleanup()
	cfg.Knowledge.Enabled = false
	handler := makeWikiListHandler(cfg)

	args, _ := json.Marshal(wikiListArgs{ParentID: 1})
	res, _ := handler(context.Background(), args)
	if !res.IsError {
		t.Error("disabled knowledge should return error for wiki_list")
	}
}

func TestWikiList_UsesBaseToAvoidNodeIDCollision(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	seedWikiListBase(t, "test_list_a", dirA, "alpha.md")
	seedWikiListBase(t, "test_list_b", dirB, "beta.md")
	defer knowledge.CloseWikiStore()

	cfg := &config.Config{Knowledge: config.KnowledgeConfig{
		Enabled: true,
		Bases: []config.KnowledgeBase{
			{Name: "test_list_a", Path: dirA, Enabled: true},
			{Name: "test_list_b", Path: dirB, Enabled: true},
		},
	}}
	handler := makeWikiListHandler(cfg)

	args, _ := json.Marshal(wikiListArgs{ParentID: 1, Base: "test_list_b"})
	res, err := handler(context.Background(), args)
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("wiki_list returned error: %s", res.Content)
	}
	if !strings.Contains(res.Content, "beta.md") || !strings.Contains(res.Content, "base=test_list_b") {
		t.Fatalf("expected test_list_b result, got:\n%s", res.Content)
	}
	if strings.Contains(res.Content, "alpha.md") {
		t.Fatalf("wiki_list should not merge another base when base is set:\n%s", res.Content)
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

func seedWikiListBase(t *testing.T, baseName, dir, fileTitle string) {
	t.Helper()
	store, err := knowledge.NewWikiStore(baseName, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	nodes := []knowledge.IndexNode{
		{ID: 1, ParentID: 0, Base: baseName, Level: 1, Title: baseName, Overview: "[Knowledge Base]"},
		{ID: 2, ParentID: 1, Base: baseName, Level: 2, Source: fileTitle, Kind: "text", SortOrder: 0, Title: fileTitle, Overview: fileTitle + " (1 chapters)"},
	}
	if err := store.ReplaceBaseNodes(context.Background(), baseName, nodes, nil); err != nil {
		t.Fatal(err)
	}
}
