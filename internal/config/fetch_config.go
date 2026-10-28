package config

import (
	"fmt"
	"strings"
	"time"
)

// FetchConfigPatch is a partial update for FetchConfig.
// Nil fields are left unchanged. ClearAPIKey deletes the key.
type FetchConfigPatch struct {
	Provider       *string `json:"provider,omitempty"`
	APIKey         *string `json:"api_key,omitempty"`
	ClearAPIKey    bool    `json:"clear_api_key,omitempty"`
	BaseURL        *string `json:"base_url,omitempty"`
	Path           *string `json:"path,omitempty"`
	Model          *string `json:"model,omitempty"`
	DailyQuota     *int    `json:"daily_quota,omitempty"`
	RequestTimeout *string `json:"request_timeout,omitempty"`
}

// UpdateFetchConfig merges a fetch-channel patch into the active config.
func UpdateFetchConfig(patch FetchConfigPatch) (*FetchConfig, error) {
	return UpdateFetchConfigAt("", patch)
}

// UpdateFetchConfigAt updates web-fetch configuration at the active path.
func UpdateFetchConfigAt(configPath string, patch FetchConfigPatch) (*FetchConfig, error) {
	cfg, err := Load(configPath)
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	if patch.Provider != nil {
		normalized := strings.ToLower(strings.TrimSpace(*patch.Provider))
		switch normalized {
		case "", "local", "deepseek", "qwen":
		default:
			return nil, fmt.Errorf("%w: unknown fetch provider %q (expected local, deepseek, or qwen)",
				ErrBadSearch, *patch.Provider)
		}
		if normalized == "" {
			normalized = "local"
		}
		cfg.Fetch.Provider = normalized
	}

	if patch.ClearAPIKey {
		cfg.Fetch.APIKey = ""
	} else if patch.APIKey != nil {
		cfg.Fetch.APIKey = *patch.APIKey
	}

	if patch.BaseURL != nil {
		base := strings.TrimSpace(*patch.BaseURL)
		if base != "" {
			if err := validateSearchBaseURL(base); err != nil {
				return nil, err
			}
		}
		cfg.Fetch.BaseURL = base
	}

	if patch.Path != nil {
		cfg.Fetch.Path = strings.TrimSpace(*patch.Path)
	}

	if patch.Model != nil {
		model := strings.TrimSpace(*patch.Model)
		if len([]rune(model)) > 128 {
			return nil, fmt.Errorf("%w: model must be at most 128 characters", ErrBadSearch)
		}
		cfg.Fetch.Model = model
	}

	if patch.DailyQuota != nil {
		q := *patch.DailyQuota
		if q < 0 || q > 100000 {
			return nil, fmt.Errorf("%w: daily_quota must be 0..100000 (got %d)", ErrBadSearch, q)
		}
		cfg.Fetch.DailyQuota = q
	}

	if patch.RequestTimeout != nil {
		raw := strings.TrimSpace(*patch.RequestTimeout)
		if raw == "" {
			cfg.Fetch.RequestTimeout = 0
		} else {
			d, err := time.ParseDuration(raw)
			if err != nil || d <= 0 {
				return nil, fmt.Errorf("%w: request_timeout must be a positive Go duration (e.g. '20s')", ErrBadSearch)
			}
			if d > 60*time.Second {
				return nil, fmt.Errorf("%w: request_timeout capped at 60s (got %v)", ErrBadSearch, d)
			}
			cfg.Fetch.RequestTimeout = d
		}
	}

	prov := cfg.Fetch.Provider
	if prov == "" {
		prov = "local"
	}
	switch prov {
	case "deepseek":
		if cfg.Fetch.APIKey == "" {
			return nil, fmt.Errorf("%w: fetch provider 'deepseek' requires api_key", ErrBadSearch)
		}
	case "qwen":
		if cfg.Fetch.APIKey == "" || cfg.Fetch.BaseURL == "" {
			return nil, fmt.Errorf("%w: fetch provider 'qwen' requires api_key and base_url", ErrBadSearch)
		}
	}

	fields := make([]configOverlayField, 0, 8)
	if patch.Provider != nil {
		fields = append(fields, overlayField(cfg.Fetch.Provider, "fetch", "provider"))
	}
	if patch.ClearAPIKey || patch.APIKey != nil {
		fields = append(fields, overlayField(cfg.Fetch.APIKey, "fetch", "api_key"))
	}
	if patch.BaseURL != nil {
		fields = append(fields, overlayField(cfg.Fetch.BaseURL, "fetch", "base_url"))
	}
	if patch.Path != nil {
		fields = append(fields, overlayField(cfg.Fetch.Path, "fetch", "path"))
	}
	if patch.Model != nil {
		fields = append(fields, overlayField(cfg.Fetch.Model, "fetch", "model"))
	}
	if patch.DailyQuota != nil {
		fields = append(fields, overlayField(cfg.Fetch.DailyQuota, "fetch", "daily_quota"))
	}
	if patch.RequestTimeout != nil {
		fields = append(fields, overlayField(cfg.Fetch.RequestTimeout, "fetch", "request_timeout"))
	}
	if err := saveConfigFieldsAt(configPath, cfg, fields...); err != nil {
		return nil, fmt.Errorf("save fetch config: %w", err)
	}
	return &cfg.Fetch, nil
}
