package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/im"
)

type imTestRequest struct {
	Type    string `json:"type"`
	Variant string `json:"variant,omitempty"`
}

func emptyIMHealth(cfg *config.Config) im.GatewayHealth {
	enabled := false
	if cfg != nil {
		enabled = cfg.IM.Enabled
	}
	return im.GatewayHealth{Enabled: enabled, Running: false, Platforms: []im.HealthStatus{}}
}

// IMHealth 返回 IM Gateway 健康状态。
// IMHealth returns the IM Gateway health status.
func (h *Handler) IMHealth(c *gin.Context) {
	if h.imGateway == nil {
		c.JSON(http.StatusOK, emptyIMHealth(h.getCfg()))
		return
	}
	c.JSON(http.StatusOK, h.imGateway.Health())
}

// GetIMConfig 返回当前 IM 配置块。
// GetIMConfig returns the current IM config block.
func (h *Handler) GetIMConfig(c *gin.Context) {
	cfg := h.getCfg()
	if cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	next := cfg.IM
	next.Normalize()
	c.JSON(http.StatusOK, next)
}

// UpdateIMConfig 替换并持久化 IM 配置块。
// UpdateIMConfig replaces and persists the IM config block.
func (h *Handler) UpdateIMConfig(c *gin.Context) {
	if h.getCfg() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	var patch config.IMConfigPatch
	if err := c.ShouldBindJSON(&patch); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid body: " + err.Error()})
		return
	}
	updated, err := config.UpdateIMConfigPatch(patch)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	h.reloadAfterConfigChange()
	if h.imGateway != nil {
		if err := h.imGateway.Reconfigure(c.Request.Context(), updated.IM); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	c.JSON(http.StatusOK, updated.IM)
}

// TestIMConnection 对一个 IM 平台执行连接自检。
// TestIMConnection runs a connection self-test for one IM platform.
func (h *Handler) TestIMConnection(c *gin.Context) {
	if h.imGateway == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "IM gateway not available"})
		return
	}
	req := imTestRequest{Type: c.Param("type")}
	if c.Request.Body != nil {
		_ = c.ShouldBindJSON(&req)
	}
	if req.Type == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "type is required"})
		return
	}
	c.JSON(http.StatusOK, h.imGateway.TestConnection(req.Type, req.Variant))
}

// IMEvents 推送 IM Gateway 生命周期事件。
// IMEvents streams IM Gateway lifecycle events.
func (h *Handler) IMEvents(c *gin.Context) {
	if h.imGateway == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "IM gateway not available"})
		return
	}
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")

	events := h.imGateway.Subscribe(c.Request.Context())
	c.Stream(func(w io.Writer) bool {
		select {
		case <-c.Request.Context().Done():
			return false
		case ev, ok := <-events:
			if !ok {
				return false
			}
			data, err := json.Marshal(ev)
			if err != nil {
				return true
			}
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
			return true
		}
	})
}
