package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/config"
)

// ProviderFull is the rich provider view returned by
// GET /api/v1/providers/:name. It carries the full Models list
// (each with its per-model settings: display name, max tokens
// context/output, capabilities) plus the legacy single-model
// fallback field, so a UI can render every model in one place.
//
// For the v0.9 "list all providers" view, see Handler.Providers —
// it returns a slimmer shape (name/model/protocol only) suitable
// for the model picker in the chat input.
type ProviderFull struct {
	Name     string               `json:"name"`
	Protocol string               `json:"protocol"`
	BaseURL  string               `json:"base_url"`
	APIKey   string               `json:"api_key"`
	IsDefault bool                `json:"is_default"`
	Models   []config.ModelConfig `json:"models"`
	// Legacy single-model form (kept for backward compat; equals
	// the first entry of Models when populated).
	Model string `json:"model,omitempty"`
}

// GetProvider GET /api/v1/providers/:name — rich view of a single
// provider, including every model and its per-model configuration
// (max_tokens_context, max_tokens_output, display_name, capabilities).
func (h *Handler) GetProvider(c *gin.Context) {
	if h.getCfg() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	name := c.Param("name")
	for _, p := range h.getCfg().LLM.Providers {
		if p.Name != name {
			continue
		}
		models := p.AllModels()
		c.JSON(http.StatusOK, ProviderFull{
			Name:      p.Name,
			Protocol:  p.GetProtocol(),
			BaseURL:   p.BaseURL,
			APIKey:    p.APIKey,
			IsDefault: p.Name == h.getCfg().LLM.Default,
			Models:    models,
			Model:     p.EffectiveModel(),
		})
		return
	}
	c.JSON(http.StatusNotFound, gin.H{"error": "provider not found: " + name})
}

// UpdateModelRequest is the body of PUT /api/v1/providers/:name/models/:model.
//
// Per-model fields:
//   - display_name, description, max_tokens_context, max_tokens_output
//
// A zero value for a numeric field means "leave it as is" (the
// API treats 0 as "not provided"). Pass an explicit negative
// value (e.g. -1) to clear the field. DisplayName and
// Description accept empty string to clear.
type UpdateModelRequest struct {
	DisplayName      string `json:"display_name"`
	Description      string `json:"description"`
	MaxTokensContext int    `json:"max_tokens_context"`
	MaxTokensOutput  int    `json:"max_tokens_output"`
	ClearAll         bool   `json:"clear_all,omitempty"`
}

// UpdateModel PUT /api/v1/providers/:name/models/:model
//
// Replaces the editable fields of a model. The model Name is the
// URL path segment and cannot be changed (callers should
// delete-and-recreate to rename). Writes to
// ~/.p-chat/config.json and reloads the in-memory LLM client.
func (h *Handler) UpdateModel(c *gin.Context) {
	if h.getCfg() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	providerName := c.Param("name")
	modelName := c.Param("model")
	var req UpdateModelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	patch := config.ModelConfig{
		DisplayName:      req.DisplayName,
		Description:      req.Description,
		MaxTokensContext: req.MaxTokensContext,
		MaxTokensOutput:  req.MaxTokensOutput,
	}
	updated, err := config.UpdateModel(providerName, modelName, patch, req.ClearAll)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "update failed: " + err.Error()})
		return
	}
	_ = updated
	h.reloadAfterConfigChange()
	c.JSON(http.StatusOK, gin.H{
		"ok":       true,
		"provider": providerName,
		"model":    modelName,
	})
}

// SetCapabilitiesRequest is the body of PATCH
// /api/v1/providers/:name/models/:model/capabilities.
//
// All fields are optional — pass `{}` to clear. The server
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
	if h.getCfg() == nil {
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

// upstreamModel mirrors one entry from the OpenAI GET /v1/models response.
type upstreamModel struct {
	ID      string `json:"id"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// upstreamModelsResponse is the OpenAI /v1/models response shape.
type upstreamModelsResponse struct {
	Data []upstreamModel `json:"data"`
}

// UpstreamModelsItem is the slim item returned to the frontend.
type UpstreamModelsItem struct {
	ID      string `json:"id"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
	Added   bool   `json:"added"` // already exists in this provider
}

// FetchUpstreamModels GET /api/v1/providers/:name/upstream-models
// Calls the upstream provider's GET /v1/models with the stored API key
// and returns the model list so the user can pick which to add.
func (h *Handler) FetchUpstreamModels(c *gin.Context) {
	name := c.Param("name")
	if h.getCfg() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}

	var provider *config.ProviderConfig
	for i := range h.getCfg().LLM.Providers {
		if h.getCfg().LLM.Providers[i].Name == name {
			provider = &h.getCfg().LLM.Providers[i]
			break
		}
	}
	if provider == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "provider not found: " + name})
		return
	}
	if provider.APIKey == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "provider has no API key configured"})
		return
	}

	baseURL := strings.TrimRight(provider.BaseURL, "/")
	url := baseURL + "/models"

	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("build request: %v", err)})
		return
	}
	req.Header.Set("Authorization", "Bearer "+provider.APIKey)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("upstream request failed: %v", err)})
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		c.JSON(http.StatusBadGateway, gin.H{"error": fmt.Sprintf("upstream returned %d", resp.StatusCode)})
		return
	}

	var parsed upstreamModelsResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("parse upstream response: %v", err)})
		return
	}

	// Build index of already-added models.
	existing := map[string]bool{}
	for _, m := range provider.AllModels() {
		existing[m.Name] = true
	}

	out := make([]UpstreamModelsItem, 0, len(parsed.Data))
	for _, m := range parsed.Data {
		out = append(out, UpstreamModelsItem{
			ID:      m.ID,
			Created: m.Created,
			OwnedBy: m.OwnedBy,
			Added:   existing[m.ID],
		})
	}

	c.JSON(http.StatusOK, gin.H{"models": out})
}
