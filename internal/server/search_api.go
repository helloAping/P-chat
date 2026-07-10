package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/search"
)

// SearchSettingsResponse is the body of GET
// /api/v1/settings/web_search. It is the read-only view the
// settings panel renders.
//
// Note: APIKey is intentionally omitted — we don't want
// the full key flowing to the browser on every page load.
// The UI shows "••••••" when HasKey is true and an empty
// field when HasKey is false; the user can paste a new key
// to replace the existing one.
type SearchSettingsResponse struct {
	Enabled        bool   `json:"enabled"`
	Provider       string `json:"provider"`
	HasKey         bool   `json:"has_key"`
	BaseURL        string `json:"base_url,omitempty"`
	Path           string `json:"path,omitempty"`
	Topic          string `json:"topic,omitempty"`
	DailyQuota     int    `json:"daily_quota"`
	RequestTimeout string `json:"request_timeout"`

	// Live status (read from the in-process tracker).
	UsedToday int       `json:"used_today"`
	ResetsAt  time.Time `json:"resets_at"`
}

// GetWebSearchSettings GET /api/v1/settings/web_search
func (h *Handler) GetWebSearchSettings(c *gin.Context) {
	if h.getCfg() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	cfg := h.getCfg().Search
	used, _, _ := search.Quota().Peek()
	c.JSON(http.StatusOK, SearchSettingsResponse{
		Enabled:        cfg.Enabled,
		Provider:       effectiveProviderName(cfg),
		HasKey:         cfg.APIKey != "",
		BaseURL:        cfg.BaseURL,
		Path:           cfg.Path,
		Topic:          cfg.Topic,
		DailyQuota:     cfg.DailyQuota,
		RequestTimeout: cfg.RequestTimeout.String(),
		UsedToday:      used,
		ResetsAt:       search.Quota().ResetsAt(),
	})
}

// SearchSettingsRequest is the body of PUT
// /api/v1/settings/web_search. Every field is optional;
// "omit" means "leave alone" for everything except Provider
// (which defaults to "tavily" if blank).
//
// The user only ever sends APIKey when they want to change
// it — leaving APIKey blank is treated as "keep the
// existing key", not "delete the key". To delete, the UI
// must explicitly send the sentinel "__DELETE__" (a real
// key with that exact name is improbable). The frontend
// tracks this in a separate `clearKey` flag.
type SearchSettingsRequest struct {
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

// UpdateWebSearchSettings PUT /api/v1/settings/web_search
//
// Persists the search config and re-registers the
// web_search tool so changes take effect on the very next
// tool call (no server restart).
func (h *Handler) UpdateWebSearchSettings(c *gin.Context) {
	if h.getCfg() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	var req SearchSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	patch := config.SearchConfigPatch{
		Enabled:        req.Enabled,
		Provider:       req.Provider,
		APIKey:         req.APIKey,
		ClearAPIKey:    req.ClearAPIKey,
		BaseURL:        req.BaseURL,
		Path:           req.Path,
		Topic:          req.Topic,
		DailyQuota:     req.DailyQuota,
		RequestTimeout: req.RequestTimeout,
	}
	updated, err := config.UpdateSearchConfig(patch)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.reloadAfterConfigChange()
	// Re-pick the quota cap so the live tracker matches
	// the freshly-persisted config.
	search.SetQuotaLimit(updated.DailyQuota)
	// Return the full response so the UI can re-render
	// without a follow-up GET.
	used, _, _ := search.Quota().Peek()
	c.JSON(http.StatusOK, SearchSettingsResponse{
		Enabled:        updated.Enabled,
		Provider:       effectiveProviderName(*updated),
		HasKey:         updated.APIKey != "",
		BaseURL:        updated.BaseURL,
		Path:           updated.Path,
		Topic:          updated.Topic,
		DailyQuota:     updated.DailyQuota,
		RequestTimeout: updated.RequestTimeout.String(),
		UsedToday:      used,
		ResetsAt:       search.Quota().ResetsAt(),
	})
}

// TestWebSearchConnection POST /api/v1/settings/web_search/test
//
// Sends a tiny "hello" query to the provider so the UI can
// show a green/red "connection ok" indicator next to the
// "Test" button. The query does NOT consume the daily
// quota — testing is meant to be free.
//
// Implementation: we build a fresh Provider from the
// *currently saved* config, then call Search with a
// trivial query. We use a 10s timeout so a stuck
// connection can't hang the UI.
func (h *Handler) TestWebSearchConnection(c *gin.Context) {
	if h.getCfg() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	provider := search.BuildProvider(h.getCfg().Search)
	if provider == nil || provider.Name() == "disabled" {
		c.JSON(http.StatusBadRequest, gin.H{
			"ok":    false,
			"error": "web_search is not configured. Set enabled=true and provide a valid API key/base_url.",
		})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	// 1 result is enough to prove the connection works.
	// We pick "test" as the query because it's cheap,
	// short, and unlikely to trigger any content policy.
	results, err := provider.Search(ctx, search.Query{Query: "test", MaxResults: 1})
	if err != nil {
		c.JSON(http.StatusOK, gin.H{
			"ok":    false,
			"error": err.Error(),
		})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":           true,
		"provider":     provider.Name(),
		"result_count": len(results),
	})
}

// effectiveProviderName returns the canonical lowercase
// provider name; empty string is normalised to "tavily"
// (the default) so the UI can match against a fixed enum.
func effectiveProviderName(cfg config.SearchConfig) string {
	p := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if p == "" {
		return "tavily"
	}
	return p
}
