package cli

import (
	"github.com/fatih/color"
	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/llm"
)

// reloadLLMClient rebuilds r.llm from the latest on-disk config.
// Used after `/config *` mutates the yaml. On success the REPL picks
// up the new providers / models / API keys / defaults immediately.
// On failure (e.g. the new config is broken) the old client stays in
// place and the error is shown.
func (r *REPL) reloadLLMClient() error {
	cfg, err := config.Load("")
	if err != nil {
		color.Red("  ✗ 重新加载配置失败: %v", err)
		return err
	}
	newClient, err := llm.NewClient(&cfg.LLM)
	if err != nil {
		color.Red("  ✗ 重建 LLM 客户端失败: %v", err)
		return err
	}

	// Keep the current provider if it still exists; otherwise fall
	// back to the config's default. This keeps the user's context
	// stable across a config edit.
	if r.provider == "" || !r.hasProvider(newClient, r.provider) {
		r.provider = cfg.LLM.Default
	}

	// Try to preserve the active model on the new client.
	if r.provider != "" {
		_ = newClient.SetModel(r.provider, r.llm.GetModel(r.provider))
	}

	r.SetLLMClient(newClient)
	return nil
}

// hasProvider reports whether client has a provider named name. The
// llm.Client doesn't expose a direct lookup, so we walk the list.
func (r *REPL) hasProvider(c *llm.Client, name string) bool {
	for _, p := range c.Providers() {
		if p.Name == name {
			return true
		}
	}
	return false
}
