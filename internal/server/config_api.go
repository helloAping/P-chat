package server

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/config"
)

// AddProviderRequest is the body of POST /api/v1/providers.
type AddProviderRequest struct {
	Name     string `json:"name" binding:"required"`
	Protocol string `json:"protocol" binding:"required"` // "openai" | "anthropic"
	BaseURL  string `json:"base_url"`
	APIKey   string `json:"api_key"`
	Model    string `json:"model"`
}

// AddProvider POST /api/v1/providers
//
// Writes the new provider to ~/.p-chat/config.json and reloads
// the in-memory LLM client so the change takes effect immediately.
func (h *Handler) AddProvider(c *gin.Context) {
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	var req AddProviderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := config.AddProvider(config.ProviderConfig{
		Name:     req.Name,
		Protocol: req.Protocol,
		BaseURL:  req.BaseURL,
		APIKey:   req.APIKey,
		Model:    req.Model,
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.reloadAfterConfigChange()
	c.JSON(http.StatusCreated, gin.H{"ok": true, "name": req.Name})
}

// DeleteProvider DELETE /api/v1/providers/:name
func (h *Handler) DeleteProvider(c *gin.Context) {
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	name := c.Param("name")
	if name == h.cfg.LLM.Default {
		c.JSON(http.StatusBadRequest, gin.H{"error": "不能删除当前默认 provider"})
		return
	}
	if err := config.RemoveProvider(name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.reloadAfterConfigChange()
	c.JSON(http.StatusOK, gin.H{"ok": true, "name": name})
}

// SetProviderAPIKeyRequest is the body of PATCH /api/v1/providers/:name.
type SetProviderAPIKeyRequest struct {
	APIKey string `json:"api_key"`
}

// SetProviderAPIKey PATCH /api/v1/providers/:name
//
// Updates the API key (and only the API key) for an existing
// provider. The provider must already exist.
func (h *Handler) SetProviderAPIKey(c *gin.Context) {
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	name := c.Param("name")
	var req SetProviderAPIKeyRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := config.SetProviderAPIKey(name, req.APIKey); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.reloadAfterConfigChange()
	c.JSON(http.StatusOK, gin.H{"ok": true, "name": name})
}

// SetDefaultProviderRequest is the body of POST /api/v1/providers/:name/default.
type SetDefaultProviderRequest struct{}

// SetDefaultProvider POST /api/v1/providers/:name/default
func (h *Handler) SetDefaultProvider(c *gin.Context) {
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	name := c.Param("name")
	if err := config.SetDefaultProvider(name); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.reloadAfterConfigChange()
	c.JSON(http.StatusOK, gin.H{"ok": true, "default": name})
}

// AddModelRequest is the body of POST /api/v1/providers/:name/models.
type AddModelRequest struct {
	Name        string `json:"name" binding:"required"`
	DisplayName string `json:"display_name"`
	Description string `json:"description"`
}

// AddModel POST /api/v1/providers/:name/models
func (h *Handler) AddModel(c *gin.Context) {
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	name := c.Param("name")
	var req AddModelRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if _, err := config.AddModel(name, config.ModelConfig{
		Name:         req.Name,
		DisplayName:  req.DisplayName,
		Description:  req.Description,
	}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.reloadAfterConfigChange()
	c.JSON(http.StatusCreated, gin.H{"ok": true, "name": req.Name})
}

// DeleteModel DELETE /api/v1/providers/:name/models/:model
func (h *Handler) DeleteModel(c *gin.Context) {
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	name := c.Param("name")
	model := c.Param("model")
	if err := config.RemoveModel(name, model); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.reloadAfterConfigChange()
	c.JSON(http.StatusOK, gin.H{"ok": true, "name": name, "model": model})
}

// SetDefaultModel POST /api/v1/providers/:name/models/:model/default
func (h *Handler) SetDefaultModel(c *gin.Context) {
	if h.cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	name := c.Param("name")
	model := c.Param("model")
	if err := config.SetDefaultModel(name, model); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.reloadAfterConfigChange()
	c.JSON(http.StatusOK, gin.H{"ok": true, "name": name, "model": model})
}
