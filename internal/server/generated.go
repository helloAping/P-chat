package server

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// GetGeneratedAsset serves one locally materialized generated media file.
func (h *Handler) GetGeneratedAsset(c *gin.Context) {
	if h.generatedStore == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "generated asset store not available"})
		return
	}
	path, meta, err := h.generatedStore.Resolve(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "generated asset not found"})
		return
	}
	if meta.MIMEType != "" {
		c.Header("Content-Type", meta.MIMEType)
	}
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Cache-Control", "private, max-age=31536000, immutable")
	c.File(path)
}
