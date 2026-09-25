package server

import (
	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/knowledge"
	"github.com/p-chat/pchat/internal/tool"
)

// SyncConfigDrivenTools updates built-in tools whose visibility depends on
// runtime configuration. It is safe to call at startup and after config reloads.
func SyncConfigDrivenTools(reg *tool.Registry, cfg *config.Config) {
	if reg == nil || cfg == nil {
		return
	}

	reg.UnregisterBuiltin("web_search")
	tool.RegisterWebSearch(reg, cfg.Search)

	reg.UnregisterBuiltin("wiki_lookup")
	reg.UnregisterBuiltin("wiki_list")
	if cfg.Knowledge.Enabled {
		tool.RegisterWiki(reg, cfg)
		var bases []knowledge.BaseRef
		for _, b := range cfg.Knowledge.Bases {
			bases = append(bases, knowledge.BaseRef{Name: b.Name, Path: b.Path, Enabled: b.Enabled})
		}
		knowledge.EnsureMigrated(bases)
	}
}
