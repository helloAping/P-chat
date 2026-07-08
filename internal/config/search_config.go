package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// SearchConfigPatch is a partial update for SearchConfig.
//
// Every field is optional; nil means "leave alone" (no
// effect on disk). The two exceptions are ClearAPIKey
// (boolean, not pointer — see below) and Provider (empty
// string is normalised to "tavily" by UpdateSearchConfig).
//
// The APIKey pointer uses the *string idiom so the UI can
// distinguish "I want to set the key to X" from "I don't
// care about the key". ClearAPIKey is a separate bool
// field so the same UI can also say "delete the key
// entirely" (a destructive operation we want to make
// explicit).
type SearchConfigPatch struct {
	Enabled        *bool   `json:"enabled,omitempty"`
	Provider       *string `json:"provider,omitempty"`
	APIKey         *string `json:"api_key,omitempty"`
	ClearAPIKey    bool    `json:"clear_api_key,omitempty"`
	BaseURL        *string `json:"base_url,omitempty"`
	Path           *string `json:"path,omitempty"`
	Topic          *string `json:"topic,omitempty"`
	DailyQuota     *int    `json:"daily_quota,omitempty"`
	RequestTimeout *string `json:"request_timeout,omitempty"`
}

// UpdateSearchConfig merges a SearchConfigPatch into the
// persisted global config and returns the merged struct.
//
// Validation rules (same as those enforced by
// RegisterWebSearch and search.BuildProvider, so the
// caller can rely on a round-trip):
//
//   - provider, if non-empty, must be "tavily" or
//     "openai_compat" (case-insensitive).
//   - base_url (if non-empty) must parse as a URL with
//     http or https scheme; non-loopback http is rejected.
//   - daily_quota, if non-zero, must be 1..100000.
//   - request_timeout, if non-empty, must parse as a
//     positive duration; capped at 60s by BuildProvider.
//
// Errors include a stable prefix the UI can match on
// (E_BADCFG) so the settings panel can surface a localised
// message.
func UpdateSearchConfig(patch SearchConfigPatch) (*SearchConfig, error) {
	cfg, err := Load("")
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}

	if patch.Provider != nil {
		normalized := strings.ToLower(strings.TrimSpace(*patch.Provider))
		switch normalized {
		case "", "tavily", "openai_compat":
			// OK
		default:
			return nil, fmt.Errorf("%w: unknown provider %q (expected 'tavily' or 'openai_compat')",
				ErrBadSearch, *patch.Provider)
		}
		cfg.Search.Provider = normalized
	}

	if patch.Enabled != nil {
		cfg.Search.Enabled = *patch.Enabled
	}

	if patch.ClearAPIKey {
		cfg.Search.APIKey = ""
	} else if patch.APIKey != nil {
		// Treat any non-empty key the user pastes as the new
		// value. We don't validate format here — the search
		// backend's own 401/403 check on the next real call
		// is the ground truth.
		cfg.Search.APIKey = *patch.APIKey
	}

	if patch.BaseURL != nil {
		base := strings.TrimSpace(*patch.BaseURL)
		if base != "" {
			if err := validateSearchBaseURL(base); err != nil {
				return nil, err
			}
		}
		cfg.Search.BaseURL = base
	}

	if patch.Path != nil {
		cfg.Search.Path = strings.TrimSpace(*patch.Path)
	}

	if patch.Topic != nil {
		topic := strings.ToLower(strings.TrimSpace(*patch.Topic))
		switch topic {
		case "", "general", "news", "finance":
			// OK
		default:
			return nil, fmt.Errorf("%w: topic must be one of general/news/finance (got %q)",
				ErrBadSearch, *patch.Topic)
		}
		cfg.Search.Topic = topic
	}

	if patch.DailyQuota != nil {
		q := *patch.DailyQuota
		if q < 0 || q > 100000 {
			return nil, fmt.Errorf("%w: daily_quota must be 0..100000 (got %d)", ErrBadSearch, q)
		}
		cfg.Search.DailyQuota = q
	}

	if patch.RequestTimeout != nil {
		raw := strings.TrimSpace(*patch.RequestTimeout)
		if raw == "" {
			cfg.Search.RequestTimeout = 0
		} else {
			d, err := time.ParseDuration(raw)
			if err != nil || d <= 0 {
				return nil, fmt.Errorf("%w: request_timeout must be a positive Go duration (e.g. '20s')", ErrBadSearch)
			}
			if d > 60*time.Second {
				return nil, fmt.Errorf("%w: request_timeout capped at 60s (got %v)", ErrBadSearch, d)
			}
			cfg.Search.RequestTimeout = d
		}
	}

	// Cross-field sanity: if the user just enabled web_search
	// without supplying the per-provider credentials, we
	// surface a single clear error instead of letting the
	// tool fail on every call.
	if cfg.Search.Enabled {
		prov := cfg.Search.Provider
		if prov == "" {
			prov = "tavily"
		}
		switch prov {
		case "tavily":
			if cfg.Search.APIKey == "" {
				return nil, fmt.Errorf("%w: enabled=true requires api_key for provider 'tavily'", ErrBadSearch)
			}
		case "openai_compat":
			if cfg.Search.BaseURL == "" {
				return nil, fmt.Errorf("%w: enabled=true requires base_url for provider 'openai_compat'", ErrBadSearch)
			}
		}
	}

	mgr := NewManager()
	if err := mgr.SaveGlobal(cfg); err != nil {
		return nil, fmt.Errorf("save search config: %w", err)
	}
	return &cfg.Search, nil
}

// ErrBadSearch is the sentinel error wrapped by every
// validation failure in UpdateSearchConfig. Callers can
// use errors.Is to render a localised message.
var ErrBadSearch = errors.New("E_BADCFG: invalid search config")

// validateSearchBaseURL mirrors the check in
// search.OpenAICompatProvider.validateBaseURL but lives in
// the config package so we can fail at save time rather
// than at search time. Keeping two copies is fine — the
// config check is the user-friendly version that runs in
// the same process, and the provider's check is the
// last-line-of-defence that runs after a config reload.
func validateSearchBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("%w: base_url parse: %v", ErrBadSearch, err)
	}
	if u.Host == "" {
		return fmt.Errorf("%w: base_url missing host", ErrBadSearch)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" && scheme != "http" {
		return fmt.Errorf("%w: base_url must be http or https (got %q)", ErrBadSearch, scheme)
	}
	if scheme == "http" {
		host := strings.ToLower(u.Hostname())
		if host != "127.0.0.1" && host != "::1" && host != "localhost" {
			return fmt.Errorf("%w: base_url http is only allowed for loopback (got %q)", ErrBadSearch, host)
		}
	}
	return nil
}
