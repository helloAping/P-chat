package search

import (
	"strings"
	"time"

	"github.com/p-chat/pchat/internal/config"
)

// BuildProvider constructs the active Provider from a
// config.SearchConfig block. The returned value is never nil;
// an unconfigured / invalid config yields DisabledProvider so
// the LLM gets a clear "not configured" error.
//
// Callers (the server bootstrap path) are expected to call
// BuildProvider once at startup and again whenever the user
// saves new web_search settings.
func BuildProvider(cfg config.SearchConfig) Provider {
	if !cfg.Enabled {
		return DisabledProvider{}
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Provider)) {
	case "", "tavily":
		if cfg.APIKey == "" {
			return DisabledProvider{}
		}
		return &TavilyProvider{
			APIKey:  cfg.APIKey,
			Timeout: pickTimeout(cfg.RequestTimeout),
			BaseURL: cfg.BaseURL, // empty → uses default
		}
	case "openai_compat":
		if cfg.BaseURL == "" {
			return DisabledProvider{}
		}
		return &OpenAICompatProvider{
			APIKey:  cfg.APIKey,
			BaseURL: cfg.BaseURL,
			Path:    cfg.Path,
			Timeout: pickTimeout(cfg.RequestTimeout),
		}
	case "deepseek":
		if strings.TrimSpace(cfg.APIKey) == "" {
			return DisabledProvider{}
		}
		return &DeepSeekProvider{
			APIKey:  cfg.APIKey,
			BaseURL: cfg.BaseURL,
			Path:    cfg.Path,
			Timeout: pickTimeout(cfg.RequestTimeout),
		}
	case "mimo":
		if strings.TrimSpace(cfg.APIKey) == "" {
			return DisabledProvider{}
		}
		return &MiMoProvider{
			APIKey:  cfg.APIKey,
			BaseURL: cfg.BaseURL,
			Model:   cfg.Model,
			Timeout: pickTimeout(cfg.RequestTimeout),
		}
	case "qwen":
		if strings.TrimSpace(cfg.APIKey) == "" || strings.TrimSpace(cfg.BaseURL) == "" {
			return DisabledProvider{}
		}
		return &QwenProvider{
			APIKey:  cfg.APIKey,
			BaseURL: cfg.BaseURL,
			Model:   cfg.Model,
			Timeout: pickTimeout(cfg.RequestTimeout),
		}
	default:
		// Unknown provider name: surface a sentinel that the
		// tool handler can translate to a user-friendly error.
		return DisabledProvider{}
	}
}

// ProviderReady reports whether cfg can serve web_search.
func ProviderReady(cfg config.SearchConfig) bool {
	if !cfg.Enabled {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Provider)) {
	case "", "tavily", "deepseek", "mimo":
		return strings.TrimSpace(cfg.APIKey) != ""
	case "openai_compat":
		return strings.TrimSpace(cfg.BaseURL) != ""
	case "qwen":
		return strings.TrimSpace(cfg.APIKey) != "" && strings.TrimSpace(cfg.BaseURL) != ""
	default:
		return false
	}
}

// pickTimeout normalizes a possibly-zero/negative duration. The
// default 20s matches Tavily's typical p95. The 60s cap is the
// scheduler ceiling for web_search (tool.WebSearchTimeout) so a
// stuck HTTP call cannot outlive the advertised per-tool budget.
func pickTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		return tavilyDefaultTimeout
	}
	if d > 60*time.Second {
		return 60 * time.Second
	}
	return d
}
