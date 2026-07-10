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
			Timeout: pickTimeout(cfg.RequestTimeout),
		}
	default:
		// Unknown provider name: surface a sentinel that the
		// tool handler can translate to a user-friendly error.
		return DisabledProvider{}
	}
}

// pickTimeout normalizes a possibly-zero/negative duration. The
// default 20s matches Tavily's typical p95 and is short enough
// that the agent loop doesn't block a full minute on a stuck
// network call.
func pickTimeout(d time.Duration) time.Duration {
	if d <= 0 {
		return tavilyDefaultTimeout
	}
	if d > 60*time.Second {
		return 60 * time.Second
	}
	return d
}
