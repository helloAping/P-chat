package server

import (
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

type bookmarkResponse struct {
	MessageID int64  `json:"message_id"`
	Note      string `json:"note,omitempty"`
	Seq       int64  `json:"seq,omitempty"`
	CreatedAt int64  `json:"created_at"`
}

type bookmarkIndexResponse struct {
	MessageID      int64  `json:"message_id"`
	ConversationID string `json:"conversation_id"`
	Title          string `json:"title"`
	ProjectPath    string `json:"project_path"`
	Snippet        string `json:"snippet"`
	Note           string `json:"note,omitempty"`
	Seq            int64  `json:"seq,omitempty"`
	CreatedAt      int64  `json:"created_at"`
}

type userTurnResponse struct {
	MessageID int64  `json:"message_id"`
	Seq       int64  `json:"seq,omitempty"`
	Content   string `json:"content"`
}

// ListAllBookmarks returns every pinned message across conversations.
func (h *Handler) ListAllBookmarks(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	list, err := h.store.ListAllBookmarks()
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	out := make([]bookmarkIndexResponse, 0, len(list))
	for _, hit := range list {
		out = append(out, bookmarkIndexResponse{
			MessageID:      hit.MessageID,
			ConversationID: hit.ConversationID,
			Title:          hit.Title,
			ProjectPath:    hit.ProjectPath,
			Snippet:        hit.Snippet,
			Note:           hit.Note,
			Seq:            hit.Seq,
			CreatedAt:      hit.CreatedAt.Unix(),
		})
	}
	c.JSON(http.StatusOK, gin.H{"bookmarks": out})
}

// ListUserTurns returns every user message in the session for the guide rail.
func (h *Handler) ListUserTurns(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	id := c.Param("id")
	if _, err := h.store.GetConversation(id); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return
	}
	list, err := h.store.ListUserTurns(id)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	out := make([]userTurnResponse, 0, len(list))
	for _, turn := range list {
		out = append(out, userTurnResponse{
			MessageID: turn.MessageID,
			Seq:       turn.Seq,
			Content:   turn.Snippet,
		})
	}
	c.JSON(http.StatusOK, gin.H{"turns": out})
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

// UpdateBookmarkNote stores a short note on an existing pin.
func (h *Handler) UpdateBookmarkNote(c *gin.Context) {
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
	var body struct {
		Note string `json:"note"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid json"})
		return
	}
	note := strings.TrimSpace(body.Note)
	if utf8.RuneCountInString(note) > 200 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "备注不能超过 200 字"})
		return
	}
	if err := h.store.UpdateBookmarkNote(id, messageID, note); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "bookmark not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "message_id": messageID, "note": note})
}
