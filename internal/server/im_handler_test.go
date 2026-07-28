package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/p-chat/pchat/internal/im"
)

const imTestConfigJSON = `{
  "server": { "host": "127.0.0.1", "port": 8960 },
  "llm": {
    "default": "cs",
    "providers": [
      {
        "name": "cs",
        "protocol": "openai",
        "base_url": "http://api-convert.08ms.cn/v1",
        "api_key": "sk-cs",
        "models": [
          { "name": "doubao-seed-2.0-lite", "default": true }
        ]
      }
    ]
  },
  "im": {
    "enabled": true,
    "session": { "scope": "per_thread", "record_sender": true },
    "command": { "prefix": "/", "forward_unknown_to_agent": true, "require_mention_in_group": true },
    "platforms": [
      { "type": "telegram", "variant": "polling", "enabled": true, "token": "secret-token" }
    ]
  }
}`

func TestIMHealthWithoutGatewayUsesConfig(t *testing.T) {
	s, _ := newTestServerWithConfig(t, imTestConfigJSON)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/im/health", nil)
	s.engine.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body struct {
		Enabled bool `json:"enabled"`
		Running bool `json:"running"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.Enabled {
		t.Fatal("enabled = false, want true from config")
	}
	if body.Running {
		t.Fatal("running = true, want false without gateway")
	}
}

func TestIMTestConnectionDoesNotExposePlainConfigSecret(t *testing.T) {
	s, cfg := newTestServerWithConfig(t, imTestConfigJSON)
	s.SetIMGateway(im.NewGateway(cfg.IM))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/im/test", bytes.NewBufferString(`{"type":"telegram","variant":"polling"}`))
	req.Header.Set("Content-Type", "application/json")
	s.engine.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "secret-token") {
		t.Fatalf("test response leaked platform token: %s", w.Body.String())
	}
	var body struct {
		OK     bool   `json:"ok"`
		Status string `json:"status"`
		Error  string `json:"error"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.OK {
		t.Fatal("unregistered skeleton adapter should not report ok")
	}
	if body.Status != "not_implemented" {
		t.Fatalf("status = %q, want not_implemented", body.Status)
	}
}

func TestUpdateIMConfigPersistsAndUpdatesGateway(t *testing.T) {
	s, cfg := newTestServerWithConfig(t, richTestConfigJSON)
	gateway := im.NewGateway(cfg.IM)
	s.SetIMGateway(gateway)

	body := `{
	  "enabled": true,
	  "session": { "scope": "per_chat", "record_sender": true },
	  "command": { "prefix": "!", "forward_unknown_to_agent": true },
	  "platforms": [
	    { "type": "telegram", "variant": "polling", "enabled": true, "token": "plain-token-in-config" }
	  ]
	}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PATCH", "/api/v1/im/config", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	s.engine.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	health := gateway.Health()
	if !health.Enabled {
		t.Fatal("gateway config was not updated to enabled")
	}
	if !health.Running {
		t.Fatal("gateway should start after enabling IM config")
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/v1/im/config", nil)
	s.engine.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("get status = %d, want 200", w.Code)
	}
	if !strings.Contains(w.Body.String(), "plain-token-in-config") {
		t.Fatal("IM config should persist plain platform token")
	}
}

func TestPatchIMConfigDoesNotClearDefaults(t *testing.T) {
	s, cfg := newTestServerWithConfig(t, richTestConfigJSON)
	gateway := im.NewGateway(cfg.IM)
	s.SetIMGateway(gateway)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("PATCH", "/api/v1/im/config", bytes.NewBufferString(`{"enabled":true}`))
	req.Header.Set("Content-Type", "application/json")
	s.engine.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Enabled               bool     `json:"enabled"`
		AuditLog              bool     `json:"audit_log"`
		AuditLocalOnly        bool     `json:"audit_local_only"`
		ToolsAllowlistDefault []string `json:"tools_allowlist_default"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if !body.Enabled || !body.AuditLog || !body.AuditLocalOnly {
		t.Fatalf("defaults lost after patch: %+v", body)
	}
	if len(body.ToolsAllowlistDefault) == 0 {
		t.Fatal("tools allowlist default was cleared")
	}
}

func TestWeChatQRFlowPersistsConfirmedCredential(t *testing.T) {
	var gotStart bool
	var gotPoll bool
	ilink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ilink/bot/get_bot_qrcode":
			gotStart = true
			if r.Method != http.MethodGet {
				t.Fatalf("qr method = %s, want GET", r.Method)
			}
			if r.URL.Query().Get("bot_type") != "3" {
				t.Fatalf("bot_type = %q, want 3", r.URL.Query().Get("bot_type"))
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"qrcode":"qr-123","qrcode_img_content":"data:image/png;base64,abc"}}`))
		case "/ilink/bot/get_qrcode_status":
			gotPoll = true
			if r.URL.Query().Get("qrcode") != "qr-123" {
				t.Fatalf("qrcode = %q, want qr-123", r.URL.Query().Get("qrcode"))
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"status":"confirmed","bot_token":"wx-token","ilink_bot_id":"bot-1","ilink_user_id":"user-1","baseurl":"https://wx.example","nickname":"P-Chat"}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer ilink.Close()

	cfgJSON := strings.Replace(imTestConfigJSON, `"platforms": [
      { "type": "telegram", "variant": "polling", "enabled": true, "token": "secret-token" }
    ]`, `"platforms": [
      { "type": "wechat", "variant": "wechatbot", "enabled": true, "mode": "websocket", "extra": { "qr_base_url": "`+ilink.URL+`" } }
    ]`, 1)
	s, cfg := newTestServerWithConfig(t, cfgJSON)
	s.SetIMGateway(im.NewGateway(cfg.IM))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/im/wechat/qr", nil)
	s.engine.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("start status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var start struct {
		ID     string `json:"id"`
		QRURL  string `json:"qr_url"`
		Status string `json:"status"`
	}
	if err := json.NewDecoder(w.Body).Decode(&start); err != nil {
		t.Fatal(err)
	}
	if start.ID != "qr-123" || start.QRURL == "" || start.Status != "waiting" {
		t.Fatalf("start response = %+v, want waiting qr-123 with image", start)
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/v1/im/wechat/qr/"+start.ID, nil)
	s.engine.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("poll status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var poll struct {
		Status  string            `json:"status"`
		Account map[string]string `json:"account"`
	}
	if err := json.NewDecoder(w.Body).Decode(&poll); err != nil {
		t.Fatal(err)
	}
	if poll.Status != "confirmed" || poll.Account["bot_id"] != "bot-1" {
		t.Fatalf("poll response = %+v, want confirmed bot-1", poll)
	}
	if !gotStart || !gotPoll {
		t.Fatal("mock iLink server was not exercised")
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/v1/im/config", nil)
	s.engine.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("config status = %d, want 200", w.Code)
	}
	var body struct {
		Platforms []struct {
			Type    string         `json:"type"`
			Token   string         `json:"token"`
			Extra   map[string]any `json:"extra"`
			Enabled bool           `json:"enabled"`
		} `json:"platforms"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Platforms) != 1 || body.Platforms[0].Type != "wechat" || body.Platforms[0].Token != "wx-token" || !body.Platforms[0].Enabled {
		t.Fatalf("persisted platforms = %+v, want enabled wechat token", body.Platforms)
	}
	if body.Platforms[0].Extra["ilink_bot_id"] != "bot-1" {
		t.Fatalf("extra = %+v, want ilink_bot_id", body.Platforms[0].Extra)
	}
}

func TestWeChatQRUnavailableIsUserReadable(t *testing.T) {
	cfgJSON := strings.Replace(imTestConfigJSON, `"platforms": [
      { "type": "telegram", "variant": "polling", "enabled": true, "token": "secret-token" }
    ]`, `"platforms": [
      { "type": "wechat", "variant": "wechatbot", "enabled": true, "mode": "websocket", "extra": { "qr_base_url": "http://127.0.0.1:1" } }
    ]`, 1)
	s, cfg := newTestServerWithConfig(t, cfgJSON)
	s.SetIMGateway(im.NewGateway(cfg.IM))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/im/wechat/qr", nil)
	s.engine.ServeHTTP(w, req)
	if w.Code != http.StatusFailedDependency {
		t.Fatalf("status = %d, want 424; body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "HTTP 502") || strings.Contains(w.Body.String(), "dial tcp") {
		t.Fatalf("response should not expose raw transport error: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "微信扫码服务暂时无法访问") {
		t.Fatalf("body = %s, want user-readable unavailable message", w.Body.String())
	}
}
