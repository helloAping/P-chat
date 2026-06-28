package agent

import (
	"testing"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/memory"
	"github.com/p-chat/pchat/internal/style"
	"github.com/p-chat/pchat/internal/tool"
)

func TestBuildStaticSystemPrompt_CacheHit(t *testing.T) {
	cfg, _ := config.Load("")
	llmClient, _ := llm.NewClient(&cfg.LLM)
	styleMgr, _ := style.NewManager(config.PromptDir())
	store, _ := memory.OpenAt(":memory:", 50)
	defer store.Close()
	tools := tool.NewRegistry()

	agt := New(cfg, llmClient, styleMgr, store, tools)

	// First call: cache miss, builds the prompt.
	p1, sig1, err := agt.buildStaticSystemPrompt(style.Tech, nil, "")
	if err != nil {
		t.Fatalf("first build: %v", err)
	}
	if p1 == "" {
		t.Fatal("expected non-empty prompt")
	}

	// Second call with the same args: cache hit (same sig).
	p2, sig2, err := agt.buildStaticSystemPrompt(style.Tech, nil, "")
	if err != nil {
		t.Fatalf("second build: %v", err)
	}
	if sig1 != sig2 {
		t.Errorf("expected same sig, got %q vs %q", sig1, sig2)
	}
	if p1 != p2 {
		t.Error("expected identical cached prompt")
	}
}

func TestBuildStaticSystemPrompt_DifferentStyle(t *testing.T) {
	cfg, _ := config.Load("")
	llmClient, _ := llm.NewClient(&cfg.LLM)
	styleMgr, _ := style.NewManager(config.PromptDir())
	store, _ := memory.OpenAt(":memory:", 50)
	defer store.Close()
	tools := tool.NewRegistry()

	agt := New(cfg, llmClient, styleMgr, store, tools)

	p1, _, _ := agt.buildStaticSystemPrompt(style.Tech, nil, "")
	p2, _, _ := agt.buildStaticSystemPrompt(style.Cute, nil, "")

	if p1 == p2 {
		t.Error("different styles should produce different prompts")
	}
}

func TestBuildStaticSystemPrompt_DifferentTools(t *testing.T) {
	cfg, _ := config.Load("")
	llmClient, _ := llm.NewClient(&cfg.LLM)
	styleMgr, _ := style.NewManager(config.PromptDir())
	store, _ := memory.OpenAt(":memory:", 50)
	defer store.Close()
	tools := tool.NewRegistry()

	agt := New(cfg, llmClient, styleMgr, store, tools)

	// Pass different tool sets; sig should differ.
	openAITools := llm.ToolsFromRegistryDef([]tool.Tool{
		{Name: "x", Description: "x"},
	})

	_, sig1, _ := agt.buildStaticSystemPrompt(style.Tech, nil, "")
	_, sig2, _ := agt.buildStaticSystemPrompt(style.Tech, openAITools, "")
	if sig1 == sig2 {
		t.Error("different tool sets should produce different sigs")
	}
}

func TestBuildStaticSystemPrompt_LanguageHint(t *testing.T) {
	cfgZh := &config.Config{
		LLM: config.LLMConfig{Output: config.OutputConfig{Language: "zh"}},
	}
	cfgEn := &config.Config{
		LLM: config.LLMConfig{Output: config.OutputConfig{Language: "en"}},
	}

	llmClient, _ := llm.NewClient(&config.LLMConfig{Default: "ollama", Providers: []config.ProviderConfig{
		{Name: "ollama", Protocol: "openai", BaseURL: "http://localhost", Model: "x"},
	}})
	styleMgr, _ := style.NewManager(config.PromptDir())
	tools := tool.NewRegistry()

	store1, _ := memory.OpenAt(":memory:", 50)
	defer store1.Close()
	a1 := New(cfgZh, llmClient, styleMgr, store1, tools)
	pZh, _, _ := a1.buildStaticSystemPrompt(style.Tech, nil, "")
	if !contains(pZh, "简体中文") {
		t.Error("Chinese language hint missing")
	}

	store2, _ := memory.OpenAt(":memory:", 50)
	defer store2.Close()
	a2 := New(cfgEn, llmClient, styleMgr, store2, tools)
	pEn, _, _ := a2.buildStaticSystemPrompt(style.Tech, nil, "")
	if !contains(pEn, "English") {
		t.Error("English language hint missing")
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
