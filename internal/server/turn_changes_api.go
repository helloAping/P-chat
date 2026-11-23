package server

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

type turnChangeResponse struct {
	MessageID int64  `json:"message_id"`
	Tool      string `json:"tool"`
	Path      string `json:"path,omitempty"`
	Preview   string `json:"preview,omitempty"`
}

// ListTurnChanges returns persisted file and command summaries for a session.
// Byte-level undo stays on the in-memory journal and only covers the latest turn.
func (h *Handler) ListTurnChanges(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	id := c.Param("id")
	if _, err := h.store.GetConversation(id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}
	list, err := h.store.ListTurnChanges(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	out := make([]turnChangeResponse, 0, len(list))
	for _, row := range list {
		out = append(out, turnChangeResponse{
			MessageID: row.MessageID,
			Tool:      row.Tool,
			Path:      row.Path,
			Preview:   row.Preview,
		})
	}
	c.JSON(http.StatusOK, gin.H{
		"changes": out,
		"note":    "字节撤销只覆盖最近一轮仍留在内存里的文件快照。",
	})
}
