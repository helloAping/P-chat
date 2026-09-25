package agent

import (
	"context"
	"strings"
	"testing"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/knowledge"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/memory"
	"github.com/p-chat/pchat/internal/style"
	"github.com/p-chat/pchat/internal/tool"
	"github.com/p-chat/pchat/internal/upgrade"
)

func TestBuildKBIndexTruncatesInjectedOverview(t *testing.T) {
	dir := t.TempDir()
	baseName := "kb_prompt_budget"
	store, err := knowledge.NewWikiStore(baseName, dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		store.Close()
		knowledge.CloseWikiStore()
	})
	nodes := []knowledge.IndexNode{
		{ID: 1, ParentID: 0, Base: baseName, Level: 1, Title: baseName, Overview: "[Knowledge Base]\n" + strings.Repeat("超长索引", 800)},
	}
	if err := store.ReplaceBaseNodes(context.Background(), baseName, nodes, nil); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Knowledge.Enabled = true
	cfg.Knowledge.Bases = []config.KnowledgeBase{{Name: baseName, Path: dir, Enabled: true}}
	llmClient, err := llm.NewClient(&cfg.LLM)
	if err != nil {
		t.Fatal(err)
	}
	tools := tool.NewRegistry()
	mem, err := memory.OpenAt(":memory:", 50)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mem.Close() })
	upgrade.SeedForTesting(mem.DB())
	styleMgr, err := style.NewManager(mem.DB())
	if err != nil {
		t.Fatal(err)
	}
	a := New(cfg, llmClient, styleMgr, mem, tools)

	got := a.buildKBIndex(baseName)
	if len([]rune(got)) > maxKBIndexPromptRunes {
		t.Fatalf("KB index prompt length = %d, want <= %d", len([]rune(got)), maxKBIndexPromptRunes)
	}
	if !strings.Contains(got, "Knowledge Base index truncated") {
		t.Fatalf("expected truncation notice in prompt:\n%s", got)
	}
	if !strings.Contains(got, "wiki_lookup") || !strings.Contains(got, "wiki_list") {
		t.Fatalf("expected tool usage footer after truncation:\n%s", got)
	}
}
