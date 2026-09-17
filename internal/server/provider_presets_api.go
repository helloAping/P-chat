package server

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/provider"
)

// ProviderPresetsResponse 是 GET /api/v1/provider-presets 的响应体。
// ProviderPresetsResponse is returned by GET /api/v1/provider-presets.
type ProviderPresetsResponse struct {
	Presets []provider.ProviderPreset `json:"presets"`
}

// ProviderPresets 为设置页列出内置供应商策略预设。
// ProviderPresets lists built-in provider strategy presets for the settings UI.
func (h *Handler) ProviderPresets(c *gin.Context) {
	c.JSON(http.StatusOK, ProviderPresetsResponse{Presets: provider.Presets()})
}
