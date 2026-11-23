package server

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/memory"
)

type storageGCRequest struct {
	Actions []string `json:"actions"`
}

func storageGCOptions(actions []string, apply bool) memory.StorageGCOptions {
	opts := memory.StorageGCOptions{Apply: apply}
	for _, action := range actions {
		switch strings.TrimSpace(action) {
		case "inline_images":
			opts.InlineImages = true
		case "historical_payload":
			opts.HistoricalPayload = true
		case "blank_conversations":
			opts.BlankConversations = true
		case "finished_queue":
			opts.FinishedQueue = true
		case "stale_summaries":
			opts.StaleSummaries = true
		case "vacuum":
			opts.Vacuum = true
		}
	}
	return opts
}

func allStorageGCActions() []string {
	return []string{
		"inline_images",
		"historical_payload",
		"blank_conversations",
		"finished_queue",
		"stale_summaries",
		"vacuum",
	}
}

// PreviewStorageGC reports how much storage each cleanup pass would reclaim.
func (h *Handler) PreviewStorageGC(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	report, err := h.store.StorageGC(storageGCOptions(allStorageGCActions(), false), nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, report)
}

// RunStorageGC applies the requested cleanup passes.
func (h *Handler) RunStorageGC(c *gin.Context) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return
	}
	var req storageGCRequest
	if err := c.ShouldBindJSON(&req); err != nil && err != io.EOF {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(req.Actions) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "at least one action is required"})
		return
	}
	opts := storageGCOptions(req.Actions, true)
	var sink memory.ImageStore
	if opts.InlineImages {
		sink = h.persistGCImage
	}
	report, err := h.store.StorageGC(opts, sink)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error(), "report": report})
		return
	}
	c.JSON(http.StatusOK, report)
}

// persistGCImage stores a reclaimed image without an owner sidecar.
// Any conversation whose message content references upl://<id> can still
// read it; identical bytes share one file via the caller's hash cache.
func (h *Handler) persistGCImage(convID, mime string, data []byte) (string, error) {
	ext := ".img"
	switch mime {
	case "image/png":
		ext = ".png"
	case "image/jpeg":
		ext = ".jpg"
	case "image/gif":
		ext = ".gif"
	case "image/webp":
		ext = ".webp"
	}
	if err := os.MkdirAll(UploadDir(), 0o755); err != nil {
		return "", err
	}
	id := newUploadID()
	path := filepath.Join(UploadDir(), id+"-gc"+ext)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", err
	}
	return id, nil
}
