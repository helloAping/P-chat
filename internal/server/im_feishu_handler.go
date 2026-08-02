package server

import (
	"errors"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/im/feishu"
)

// FeishuWebhook 处理飞书 Bot v3 回调事件。
// FeishuWebhook handles Feishu Bot v3 callback events.
func (h *Handler) FeishuWebhook(c *gin.Context) {
	if h.imGateway == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "IM gateway not available"})
		return
	}
	platform, ok := h.imPlatformConfig("feishu", "bot")
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "feishu platform not configured"})
		return
	}
	data, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "read body: " + err.Error()})
		return
	}
	result, err := feishu.ParseCallback(data, platform)
	if err != nil {
		if errors.Is(err, feishu.ErrInvalidToken) {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid verification token"})
			return
		}
		if errors.Is(err, feishu.ErrUnsupportedEvent) {
			c.JSON(http.StatusAccepted, gin.H{"ok": true, "ignored": true})
			return
		}
		if errors.Is(err, feishu.ErrEncryptedCallbackUnsupported) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "encrypted feishu callbacks are not supported yet"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if result.Challenge != "" {
		c.JSON(http.StatusOK, gin.H{"challenge": result.Challenge})
		return
	}
	if result.Event == nil {
		c.JSON(http.StatusAccepted, gin.H{"ok": true})
		return
	}
	if err := h.imGateway.Submit(c.Request.Context(), *result.Event); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"ok": true})
}

func (h *Handler) imPlatformConfig(platformType, fallbackVariant string) (config.IMPlatformConfig, bool) {
	cfg := h.getCfg()
	if cfg == nil {
		return config.IMPlatformConfig{}, false
	}
	if !cfg.IM.Enabled {
		return config.IMPlatformConfig{}, false
	}
	for _, platform := range cfg.IM.Platforms {
		if platform.Type != platformType {
			continue
		}
		if !platform.Enabled {
			continue
		}
		if platform.Variant == fallbackVariant || platform.Variant == "" {
			if platform.Variant == "" {
				platform.Variant = fallbackVariant
			}
			return platform, true
		}
	}
	return config.IMPlatformConfig{}, false
}
