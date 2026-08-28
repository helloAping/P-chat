package server

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	pchatupdate "github.com/p-chat/pchat/internal/update"
	"github.com/p-chat/pchat/internal/version"
)

type downloadUpdateRequest struct {
	CurrentVersion string `json:"current_version,omitempty"`
}

// CheckUpdate GET /api/v1/updates/check
func (h *Handler) CheckUpdate(c *gin.Context) {
	current := strings.TrimSpace(c.Query("current_version"))
	if current == "" {
		current = version.ReleaseString()
	}
	res, err := pchatupdate.NewService(pchatupdate.Options{}).Check(c.Request.Context(), current)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, res)
}

// DownloadUpdate POST /api/v1/updates/download
func (h *Handler) DownloadUpdate(c *gin.Context) {
	current := strings.TrimSpace(c.Query("current_version"))
	if current == "" {
		var req downloadUpdateRequest
		if c.Request.Body != nil && c.Request.ContentLength != 0 {
			if err := c.ShouldBindJSON(&req); err != nil && !errors.Is(err, io.EOF) {
				c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
				return
			}
		}
		current = strings.TrimSpace(req.CurrentVersion)
	}
	if current == "" {
		current = version.ReleaseString()
	}
	res, err := pchatupdate.NewService(pchatupdate.Options{}).Download(c.Request.Context(), current)
	if err != nil {
		status := http.StatusBadGateway
		msg := err.Error()
		if strings.Contains(msg, "no update available") {
			status = http.StatusConflict
		} else if strings.Contains(msg, "not an update zip") || strings.Contains(msg, "not a zip file") {
			status = http.StatusConflict
		} else if strings.Contains(msg, "sha256") || strings.Contains(msg, "size mismatch") {
			status = http.StatusBadGateway
		}
		c.JSON(status, gin.H{"error": msg})
		return
	}
	c.JSON(http.StatusOK, res)
}
