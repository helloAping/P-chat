package server

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/config"
)

type generationOperationOption struct {
	Operation         config.GenerationOperation    `json:"operation"`
	OutputKind        config.MediaKind              `json:"output_kind"`
	RequiredInput     config.MediaKind              `json:"required_input_kind,omitempty"`
	Available         bool                          `json:"available"`
	Enabled           bool                          `json:"enabled"`
	DefaultTarget     *config.GenerationModelTarget `json:"default_target,omitempty"`
	UnavailableReason string                        `json:"unavailable_reason,omitempty"`
}

// GenerationOptions GET /api/v1/generation/options returns the canonical
// capability matrix plus application-level model resolution. Session switches
// are informational here; enforcement remains inside each generation tool.
func (h *Handler) GenerationOptions(c *gin.Context) {
	cfg := h.getCfg()
	if cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	sessionID := strings.TrimSpace(c.Query("session_id"))
	enabled := map[config.GenerationOperation]bool{}
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
	}

	options := make([]generationOperationOption, 0, len(config.AllGenerationOperations()))
	for _, operation := range config.AllGenerationOperations() {
		item := generationOperationOption{
			Operation:     operation,
			OutputKind:    operation.OutputKind(),
			RequiredInput: operation.RequiredInputKind(),
			Enabled:       enabled[operation],
		}
		if target := cfg.Generation.Defaults[operation]; target.Valid() {
			copy := target
			item.DefaultTarget = &copy
		}
		_, _, _, err := cfg.ResolveGenerationTarget(operation, config.GenerationModelTarget{})
		if err == nil {
			item.Available = true
		} else {
			item.UnavailableReason = err.Error()
		}
		options = append(options, item)
	}
	c.JSON(http.StatusOK, gin.H{"operations": options})
}
