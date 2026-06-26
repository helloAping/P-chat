package server

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/config"
)

// ProviderFull is the rich provider view returned by
// GET /api/v1/providers/:name. It carries the full Models list
// (each with its Capabilities block) plus the legacy single-model
// fallback field, so a UI can render every model in one place.
//
// For the v0.9 "list all providers" view, see Handler.Providers -
// it returns a slimmer shape (name/model/protocol only) suitable
// for the model picker in the chat input.
type ProviderFull struct {
	Name     string             `json:"name"`
	Protocol string             `json:"protocol"`
	BaseURL  string             `json:"base_url"`
	APIKey   string             `json:"api_key"`
	IsDefault bool              `json:"is_default"`
	Models   []config.ModelConfig `json:"models"`
	// Legacy single-model form (kept for backward compat; equals
	// the first entry of Models when populated).
	Model string `json:"model,omitempty"`
}

// GetProvider GET /api/v1/providers/:name �?rich view of a single
// provider, including every model and its Capabilities.
func (h *Handler) GetProvider(c *gin.Context) {
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	name := c.Param("name")
	for _, p := range h.cfg.LLM.Providers {
		if p.Name != name {
			continue
		}
		models := p.AllModels()
		c.JSON(http.StatusOK, ProviderFull{
			Name:      p.Name,
			Protocol:  p.GetProtocol(),
			BaseURL:   p.BaseURL,
			APIKey:    p.APIKey,
			IsDefault: p.Name == h.cfg.LLM.Default,
			Models:    models,
			Model:     p.EffectiveModel(),
		})
		return
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "provider not found: " + name})
}

// SetCapabilitiesRequest is the body of PATCH
// /api/v1/providers/:name/models/:model/capabilities.
//
// All fields are optional �?pass `{}` to clear. The server
// validates ThinkingEffort before writing.
type SetCapabilitiesRequest struct {
	ThinkingEffort string `json:"thinking_effort,omitempty"`
	ContextWindow  int    `json:"context_window,omitempty"`
	SupportsVision bool   `json:"supports_vision,omitempty"`
	SupportsAudio  bool   `json:"supports_audio,omitempty"`
}

// SetCapabilities PATCH /api/v1/providers/:name/models/:model/capabilities
//
// Replaces the model entry's Capabilities block. Writes to
// ~/.p-chat/config.json and reloads the in-memory LLM client.
func (h *Handler) SetCapabilities(c *gin.Context) {
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	name := c.Param("name")
	model := c.Param("model")
	var req SetCapabilitiesRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := config.SetModelCapabilities(name, model, config.Capabilities{
		ThinkingEffort: config.ThinkingEffort(req.ThinkingEffort),
		ContextWindow:  req.ContextWindow,
		SupportsVision: req.SupportsVision,
		SupportsAudio:  req.SupportsAudio,
	}); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.reloadAfterConfigChange()
	c.JSON(http.StatusOK, gin.H{"ok": true, "provider": name, "model": model})
}
