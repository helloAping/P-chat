package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/browser"
	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/version"
	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

// BrowserExtZip holds the embedded browser extension zip bytes.
// Set by cmd/pchat-server/main.go via go:embed.
var BrowserExtZip []byte

func versionString() string {
	if v := version.Version; v != "" {
		return v
	}
	return "dev"
}

func (h *Handler) BrowserList(c *gin.Context) {
	if h.browserMgr == nil {
		c.JSON(http.StatusOK, gin.H{"browsers": []browser.BrowserInfo{}, "count": 0})
		return
	}
	list := h.browserMgr.Hub().List()
	c.JSON(http.StatusOK, gin.H{"browsers": list, "count": len(list)})
}

func (h *Handler) BrowserStatus(c *gin.Context) {
	if h.browserMgr == nil {
		c.JSON(http.StatusOK, gin.H{"enabled": false, "count": 0})
		return
	}
	lastErr, lastErrAt := h.browserMgr.Hub().LastError()
	out := gin.H{
		"enabled":                   h.browserMgr.IsEnabled(),
		"count":                     h.browserMgr.Hub().Count(),
		"expected_protocol_version": browser.ProtocolVersion,
	}
	if h.listenAddr != "" {
		out["http_url"] = "http://" + h.listenAddr
		out["ws_url"] = "ws://" + h.listenAddr + "/api/v1/browser/ws"
	}
	if lastErr != "" {
		out["last_error"] = lastErr
		out["last_error_at"] = lastErrAt
	}
	c.JSON(http.StatusOK, out)
}

// BrowserExtensionDownload serves the browser extension zip file.
// GET /api/v1/browser/extension
func (h *Handler) BrowserExtensionDownload(c *gin.Context) {
	if len(BrowserExtZip) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "browser extension zip not available"})
		return
	}
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="pchat-browser-ext-%s.zip"`, versionString()))
	c.Data(http.StatusOK, "application/zip", BrowserExtZip)
}

func (h *Handler) UpdateBrowserConfig(c *gin.Context) {
	if h.browserMgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "browser manager not available"})
		return
	}

	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	h.browserMgr.SetEnabled(req.Enabled)

	cfg := h.getCfg()
	cfg.Browser.Enabled = req.Enabled
	if mgr := config.NewManager(); mgr != nil {
		if err := mgr.SaveGlobal(cfg); err != nil {
			log.Printf("[browser] persist config: %v", err)
		}
	}

	c.JSON(http.StatusOK, gin.H{"ok": true, "enabled": req.Enabled})
}

func (h *Handler) BrowserWebSocket(c *gin.Context) {
	if h.browserMgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "browser manager not available"})
		return
	}
	// Auto-enable browser control when a browser extension attempts
	// to connect. This avoids the "user enables in settings, then
	// reloads extension" step. The feature is implicitly enabled
	// by the act of connecting.
	if !h.browserMgr.IsEnabled() {
		h.browserMgr.SetEnabled(true)
		cfg := h.getCfg()
		cfg.Browser.Enabled = true
		if mgr := config.NewManager(); mgr != nil {
			if err := mgr.SaveGlobal(cfg); err != nil {
				log.Printf("[browser] auto-enable persist config: %v", err)
			}
		}
		log.Println("[browser] auto-enabled browser control (browser extension connected)")
	}

	conn, err := websocket.Accept(c.Writer, c.Request, &websocket.AcceptOptions{
		InsecureSkipVerify: true,
	})
	if err != nil {
		log.Printf("[browser] WebSocket accept failed: %v", err)
		return
	}
	// The browser extension sends screenshots as base64 data URLs
	// (~150–400 KB). The nhooyr.io/websocket library defaults to a
	// 32 KB read limit which kills the connection on the very first
	// screenshot. Raise to 10 MB.
	conn.SetReadLimit(10 << 20)

	ctx := c.Request.Context()
	hi := readHello(ctx, conn)
	if hi == nil {
		h.browserMgr.Hub().RecordError("hello timeout or parse failure")
		_ = conn.Close(websocket.StatusProtocolError, "hello timeout")
		return
	}

	clientID := hi.ID
	if clientID == "" {
		clientID = browser.NewBrowserID()
	}

	helloResp := browser.HelloResponse{
		Type:                    "hello-ok",
		BrowserID:               clientID,
		ProtocolVersion:         hi.ProtocolVersion,
		ExpectedProtocolVersion: browser.ProtocolVersion,
		UpdateRequired:          !browser.ProtocolCompatible(hi.ProtocolVersion),
		UpdateMessage:           browser.UpdateMessage(hi.ProtocolVersion),
	}
	wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
	defer wcancel()
	if err := wsjson.Write(wctx, conn, helloResp); err != nil {
		log.Printf("[browser] failed to send hello-ok: %v", err)
		_ = conn.Close(websocket.StatusInternalError, "hello failed")
		return
	}

	log.Printf("[browser] client %s connected (name=%q, tabs=%d, ext=%q, proto=%q)", clientID, hi.BrowserName, hi.TabsCount, hi.ExtensionVersion, hi.ProtocolVersion)

	client := browser.NewBrowserClient(clientID, hi.BrowserName, conn, *hi)
	defer h.browserMgr.Hub().Unregister(clientID)

	pumpCtx, pumpCancel := context.WithCancel(ctx)
	defer pumpCancel()

	h.browserMgr.Hub().Register(client)
	client.StartReadPump(pumpCtx)
	if snap := client.Snapshot(); snap.LastError != "" {
		h.browserMgr.Hub().RecordError(fmt.Sprintf("%s: %s", clientID, snap.LastError))
	}
}

func readHello(ctx context.Context, conn *websocket.Conn) *browser.HelloParams {
	rctx, rcancel := context.WithTimeout(ctx, 10*time.Second)
	defer rcancel()

	var raw json.RawMessage
	if err := wsjson.Read(rctx, conn, &raw); err != nil {
		log.Printf("[browser] hello read error: %v", err)
		return nil
	}

	var direct browser.HelloParams
	if err := json.Unmarshal(raw, &direct); err == nil && direct.BrowserName != "" {
		return &direct
	}

	var wrapped struct {
		Params browser.HelloParams `json:"params"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil && wrapped.Params.BrowserName != "" {
		return &wrapped.Params
	}

	log.Printf("[browser] hello parse error, raw: %s", string(raw))
	return &browser.HelloParams{BrowserName: "unknown"}
}

// BrowserTabs lists open tabs for a connected browser.
// GET /api/v1/browser/:id/tabs
func (h *Handler) BrowserTabs(c *gin.Context) {
	if h.browserMgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "browser manager not available"})
		return
	}
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "browser id required"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	result, err := h.browserMgr.Hub().ListTabs(ctx, id)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, result)
}

// BrowserSetActiveTab sets the preferred control target tab.
// POST /api/v1/browser/:id/active-tab
// Body: { "tab_id": 123 } or { "index": 0 }
func (h *Handler) BrowserSetActiveTab(c *gin.Context) {
	if h.browserMgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "browser manager not available"})
		return
	}
	id := c.Param("id")
	if id == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "browser id required"})
		return
	}
	var req struct {
		TabID *int `json:"tab_id"`
		Index *int `json:"index"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	tabID := 0
	if req.TabID != nil {
		tabID = *req.TabID
	}
	if tabID == 0 && req.Index == nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "tab_id or index is required"})
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	result, err := h.browserMgr.Hub().SetActiveTab(ctx, id, tabID, req.Index)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, result)
}
