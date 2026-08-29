package server

import (
	"testing"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/tool"
)

func TestSyncConfigDrivenToolsTogglesWiki(t *testing.T) {
	reg := tool.NewRegistry()
	cfg := &config.Config{Knowledge: config.KnowledgeConfig{Enabled: true}}

	SyncConfigDrivenTools(reg, cfg)
	if _, _, ok := reg.Lookup("wiki_lookup"); !ok {
		t.Fatal("wiki_lookup should be registered when knowledge is enabled")
	}
	if _, _, ok := reg.Lookup("wiki_list"); !ok {
		t.Fatal("wiki_list should be registered when knowledge is enabled")
	}

	cfg.Knowledge.Enabled = false
	SyncConfigDrivenTools(reg, cfg)
	if _, _, ok := reg.Lookup("wiki_lookup"); ok {
		t.Fatal("wiki_lookup should be removed when knowledge is disabled")
	}
	if _, _, ok := reg.Lookup("wiki_list"); ok {
		t.Fatal("wiki_list should be removed when knowledge is disabled")
	}
}

func TestSyncConfigDrivenToolsTogglesWebSearch(t *testing.T) {
	reg := tool.NewRegistry()
	cfg := &config.Config{Search: config.SearchConfig{Enabled: true, Provider: "tavily", APIKey: "k"}}

	SyncConfigDrivenTools(reg, cfg)
	if _, _, ok := reg.Lookup("web_search"); !ok {
		t.Fatal("web_search should be registered when provider config is usable")
	}

	cfg.Search.Enabled = false
	SyncConfigDrivenTools(reg, cfg)
	if _, _, ok := reg.Lookup("web_search"); ok {
		t.Fatal("web_search should be removed when search is disabled")
	}
}
