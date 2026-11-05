package server

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

type bookmarkResponse struct {
	MessageID int64  `json:"message_id"`
	Note      string `json:"note,omitempty"`
	Seq       int64  `json:"seq,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

func (h *Handler) ListBookmarks(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	id := c.Param("id")
	if _, err := h.store.GetConversation(id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}
	list, err := h.store.ListBookmarks(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	out := make([]bookmarkResponse, 0, len(list))
	for _, b := range list {
		out = append(out, bookmarkResponse{
			MessageID: b.MessageID,
			Note:      b.Note,
			Seq:       b.Seq,
			CreatedAt: b.CreatedAt.Unix(),
		})
	}
	c.JSON(http.StatusOK, gin.H{"bookmarks": out})
}

func (h *Handler) ToggleBookmark(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	id := c.Param("id")
	messageID, err := strconv.ParseInt(c.Param("message_id"), 10, 64)
	if err != nil || messageID <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "message_id is required"})
		return
	}
	on, err := h.store.ToggleBookmark(id, messageID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"bookmarked": on, "message_id": messageID, "toggled_at": time.Now().Unix()})
}
