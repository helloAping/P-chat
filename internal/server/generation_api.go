package server

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/config"
)

type generationModelOption struct {
	Provider    string `json:"provider"`
	Model       string `json:"model"`
	DisplayName string `json:"display_name"`
	Vendor      string `json:"vendor,omitempty"`
}

type generationOperationOption struct {
	Operation         config.GenerationOperation    `json:"operation"`
	OutputKind        config.MediaKind              `json:"output_kind"`
	RequiredInput     config.MediaKind              `json:"required_input_kind,omitempty"`
	Available         bool                          `json:"available"`
	Enabled           bool                          `json:"enabled"`
	Models            []generationModelOption       `json:"models"`
	DefaultTarget     *config.GenerationModelTarget `json:"default_target,omitempty"`
	SessionOverride   *config.GenerationModelTarget `json:"session_override,omitempty"`
	EffectiveTarget   *config.GenerationModelTarget `json:"effective_target,omitempty"`
	UnavailableReason string                        `json:"unavailable_reason,omitempty"`
}

// GenerationOptions GET /api/v1/generation/options returns the canonical
// capability matrix plus app/session model resolution. Session switches are
// informational here; enforcement remains inside each generation tool.
func (h *Handler) GenerationOptions(c *gin.Context) {
	cfg := h.getCfg()
	if cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	sessionID := strings.TrimSpace(c.Query("session_id"))
	enabled := map[config.GenerationOperation]bool{}
	overrides := map[config.GenerationOperation]config.GenerationModelTarget{}
	promptAssist := true
	if sessionID != "" {
		if h.store == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
			return
		}
		if _, err := h.store.GetConversation(sessionID); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
			} else {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			}
			return
		}
		for _, operation := range h.sessionGenerationOperations(sessionID) {
			enabled[operation] = true
		}
		overrides = h.sessionGenerationModelOverrides(sessionID)
		promptAssist = h.sessionGenerationPromptAssist(sessionID)
	}

	options := make([]generationOperationOption, 0, len(config.AllGenerationOperations()))
	for _, operation := range config.AllGenerationOperations() {
		item := generationOperationOption{
			Operation:     operation,
			OutputKind:    operation.OutputKind(),
			RequiredInput: operation.RequiredInputKind(),
			Enabled:       enabled[operation],
			Models:        h.generationModelOptions(operation),
		}
		if target := cfg.Generation.Defaults[operation]; target.Valid() {
			copy := target
			item.DefaultTarget = &copy
		}
		if target := overrides[operation]; target.Valid() {
			copy := target
			item.SessionOverride = &copy
		}
		target, _, _, err := cfg.ResolveGenerationTarget(operation, overrides[operation])
		if err == nil {
			copy := target
			item.EffectiveTarget = &copy
			item.Available = true
		} else {
			item.UnavailableReason = err.Error()
		}
		options = append(options, item)
	}
	c.JSON(http.StatusOK, gin.H{
		"operations":    options,
		"prompt_assist": promptAssist,
	})
}

func (h *Handler) generationModelOptions(operation config.GenerationOperation) []generationModelOption {
	targets := h.getCfg().GenerationModelsFor(operation)
	out := make([]generationModelOption, 0, len(targets))
	for _, target := range targets {
		option := generationModelOption{Provider: target.Provider, Model: target.Model, DisplayName: target.Model}
		for _, provider := range h.getCfg().LLM.Providers {
			if provider.Name != target.Provider {
				continue
			}
			option.Vendor = provider.Vendor
			for _, model := range provider.Models {
				if model.Name == target.Model && strings.TrimSpace(model.DisplayName) != "" {
					option.DisplayName = model.DisplayName
					break
				}
			}
			break
		}
		out = append(out, option)
	}
	return out
}
