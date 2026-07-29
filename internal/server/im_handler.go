package server

import (
	"context"
	"encoding/json"
	"errors"
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
		im.RegisterConfiguredWeChatAdapters(h.imGateway, updated.IM)
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

// StartWeChatQR starts a WeChat Bot QR login flow.
func (h *Handler) StartWeChatQR(c *gin.Context) {
	cfg := h.getCfg()
	if cfg == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	if h.wechatQR == nil {
		h.wechatQR = im.NewWeChatQRManager(im.WeChatQRClient{})
	}
	platform := findIMPlatform(cfg.IM, "wechat", "wechatbot")
	ctx := c.Request.Context()
	session, err := h.wechatQR.Start(ctx, platform)
	if err != nil {
		var unavailable im.WeChatQRServiceError
		if errors.As(err, &unavailable) {
			c.JSON(http.StatusOK, im.WeChatQRSession{
				Status:      "unavailable",
				Message:     unavailable.Error(),
				PollAfterMS: 0,
			})
			return
		}
		writeWeChatQRError(c, err)
		return
	}
	c.JSON(http.StatusOK, session)
}

// PollWeChatQR polls a WeChat Bot QR login flow and persists credentials once confirmed.
func (h *Handler) PollWeChatQR(c *gin.Context) {
	if h.getCfg() == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "config not available"})
		return
	}
	if h.wechatQR == nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "wechat qr session not found"})
		return
	}
	session, cred, err := h.wechatQR.Poll(c.Request.Context(), c.Param("id"))
	if err != nil {
		writeWeChatQRError(c, err)
		return
	}
	if session.Status == "confirmed" && cred.Token != "" {
		if err := h.persistWeChatCredential(cred); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	c.JSON(http.StatusOK, session)
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

func (h *Handler) persistWeChatCredential(cred im.WeChatCredential) error {
	cfg := h.getCfg()
	if cfg == nil {
		return fmt.Errorf("config not available")
	}
	next := cfg.IM
	next.Normalize()
	next.Enabled = true
	idx := -1
	for i := range next.Platforms {
		if next.Platforms[i].Type == "wechat" {
			idx = i
			break
		}
	}
	if idx < 0 {
		next.Platforms = append(next.Platforms, config.IMPlatformConfig{
			Type:    "wechat",
			Variant: "wechatbot",
			Mode:    "polling",
		})
		idx = len(next.Platforms) - 1
	}
	platform := next.Platforms[idx]
	platform.Type = "wechat"
	if platform.Variant == "" {
		platform.Variant = "wechatbot"
	}
	if platform.Mode == "" {
		platform.Mode = "polling"
	}
	platform.Enabled = true
	platform.Token = cred.Token
	if platform.Extra == nil {
		platform.Extra = map[string]any{}
	}
	if cred.BaseURL != "" {
		platform.Endpoint = cred.BaseURL
		platform.Extra["base_url"] = cred.BaseURL
	}
	if cred.BotID != "" {
		platform.Extra["ilink_bot_id"] = cred.BotID
	}
	if cred.UserID != "" {
		platform.Extra["ilink_user_id"] = cred.UserID
	}
	if cred.Nickname != "" {
		platform.Extra["nickname"] = cred.Nickname
	}
	next.Platforms[idx] = platform
	updated, err := config.UpdateIMConfig(next)
	if err != nil {
		return err
	}
	h.reloadAfterConfigChange()
	if h.imGateway != nil {
		im.RegisterConfiguredWeChatAdapters(h.imGateway, updated.IM)
		if err := h.imGateway.Reconfigure(context.Background(), updated.IM); err != nil {
			return err
		}
	}
	return nil
}

func findIMPlatform(cfg config.IMConfig, platformType, fallbackVariant string) config.IMPlatformConfig {
	cfg.Normalize()
	for _, platform := range cfg.Platforms {
		if platform.Type == platformType {
			if platform.Variant == "" {
				platform.Variant = fallbackVariant
			}
			return platform
		}
	}
	return config.IMPlatformConfig{
		Type:           platformType,
		Variant:        fallbackVariant,
		Enabled:        true,
		Mode:           "polling",
		AllowedSenders: []string{"*"},
	}
}

func writeWeChatQRError(c *gin.Context, err error) {
	var unavailable im.WeChatQRServiceError
	if errors.As(err, &unavailable) {
		c.JSON(http.StatusFailedDependency, gin.H{
			"error": unavailable.Error(),
			"code":  "wechat_qr_unavailable",
		})
		return
	}
	c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
}
