package server

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/tool"
)

// GetFileChanges returns the latest undoable file-change batch for a session.
// GET /api/v1/sessions/:id/file-changes
func (h *Handler) GetFileChanges(c *gin.Context) {
	id := c.Param("id")
	if h.fileJournal == nil {
		c.JSON(http.StatusOK, tool.FileChangeSummary{})
		return
	}
	c.JSON(http.StatusOK, h.fileJournal.PeekLastTurn(id))
}

// UndoFileChanges restores files from the latest non-empty journal turn.
// POST /api/v1/sessions/:id/file-changes/undo
func (h *Handler) UndoFileChanges(c *gin.Context) {
	id := c.Param("id")
	if h.fileJournal == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "file journal unavailable"})
		return
	}
	summary, err := h.fileJournal.UndoLastTurn(id)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error(), "summary": summary})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "summary": summary})
}
