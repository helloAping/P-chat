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

// FetchSettingsResponse is the body of GET /api/v1/settings/web_fetch.
// The API key itself is never returned.
type FetchSettingsResponse struct {
	Provider       string    `json:"provider"`
	HasKey         bool      `json:"has_key"`
	BaseURL        string    `json:"base_url,omitempty"`
	Path           string    `json:"path,omitempty"`
	Model          string    `json:"model,omitempty"`
	DailyQuota     int       `json:"daily_quota"`
	RequestTimeout string    `json:"request_timeout"`
	UsedToday      int       `json:"used_today"`
	ResetsAt       time.Time `json:"resets_at"`
}

// GetWebFetchSettings GET /api/v1/settings/web_fetch
func (h *Handler) GetWebFetchSettings(c *gin.Context) {
	if h.getCfg() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	cfg := h.getCfg().Fetch
	used, _, _ := search.FetchQuota().Peek()
	c.JSON(http.StatusOK, FetchSettingsResponse{
		Provider:       effectiveFetchProvider(cfg),
		HasKey:         cfg.APIKey != "",
		BaseURL:        cfg.BaseURL,
		Path:           cfg.Path,
		Model:          cfg.Model,
		DailyQuota:     cfg.DailyQuota,
		RequestTimeout: timeoutString(cfg.RequestTimeout),
		UsedToday:      used,
		ResetsAt:       search.FetchQuota().ResetsAt(),
	})
}

// FetchSettingsRequest is the body of PUT /api/v1/settings/web_fetch.
type FetchSettingsRequest struct {
	Provider       *string `json:"provider,omitempty"`
	APIKey         *string `json:"api_key,omitempty"`
	ClearAPIKey    bool    `json:"clear_api_key,omitempty"`
	BaseURL        *string `json:"base_url,omitempty"`
	Path           *string `json:"path,omitempty"`
	Model          *string `json:"model,omitempty"`
	DailyQuota     *int    `json:"daily_quota,omitempty"`
	RequestTimeout *string `json:"request_timeout,omitempty"`
}

// UpdateWebFetchSettings PUT /api/v1/settings/web_fetch
func (h *Handler) UpdateWebFetchSettings(c *gin.Context) {
	if h.getCfg() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	var req FetchSettingsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	updated, err := config.UpdateFetchConfigAt(h.configPath, config.FetchConfigPatch{
		Provider:       req.Provider,
		APIKey:         req.APIKey,
		ClearAPIKey:    req.ClearAPIKey,
		BaseURL:        req.BaseURL,
		Path:           req.Path,
		Model:          req.Model,
		DailyQuota:     req.DailyQuota,
		RequestTimeout: req.RequestTimeout,
	})
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.reloadAfterConfigChange()
	search.SetFetchQuotaLimit(updated.DailyQuota)
	used, _, _ := search.FetchQuota().Peek()
	c.JSON(http.StatusOK, FetchSettingsResponse{
		Provider:       effectiveFetchProvider(*updated),
		HasKey:         updated.APIKey != "",
		BaseURL:        updated.BaseURL,
		Path:           updated.Path,
		Model:          updated.Model,
		DailyQuota:     updated.DailyQuota,
		RequestTimeout: timeoutString(updated.RequestTimeout),
		UsedToday:      used,
		ResetsAt:       search.FetchQuota().ResetsAt(),
	})
}

// TestWebFetchConnection POST /api/v1/settings/web_fetch/test
//
// Local fetch does not call the network. Vendor fetch requests
// https://example.com and does not consume the daily quota.
func (h *Handler) TestWebFetchConnection(c *gin.Context) {
	if h.getCfg() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	fetcher := search.BuildFetcher(h.getCfg().Fetch)
	if fetcher == nil || fetcher.Name() == "local" {
		c.JSON(http.StatusOK, gin.H{"ok": true, "provider": "local"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 20*time.Second)
	defer cancel()
	page, err := fetcher.Fetch(ctx, "https://example.com")
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "provider": fetcher.Name(), "error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"ok":       true,
		"provider": fetcher.Name(),
		"title":    page.Title,
	})
}

func effectiveFetchProvider(cfg config.FetchConfig) string {
	p := strings.ToLower(strings.TrimSpace(cfg.Provider))
	if p == "" {
		return "local"
	}
	return p
}

func timeoutString(d time.Duration) string {
	if d <= 0 {
		return "20s"
	}
	return d.String()
}
