package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/p-chat/pchat/internal/config"
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

func TestIMProviderModelUsesGlobalDefaultInsteadOfSessionMeta(t *testing.T) {
	s, _ := newTestServerWithConfig(t, richTestConfigJSON)
	sessionID := "im:wechat:u:user-1"
	s.handler.metaMu.Lock()
	s.handler.meta[sessionID] = sessionMeta{Provider: "openai", Model: "gpt-4o"}
	s.handler.metaMu.Unlock()

	provider, model := s.handler.imProviderModel(config.IMPersona{})
	if provider != "cs" || model != "doubao-seed-2.0-lite" {
		t.Fatalf("provider/model = %q/%q, want global default cs/doubao-seed-2.0-lite", provider, model)
	}

	provider, model = s.handler.imProviderModel(config.IMPersona{Model: "doubao-pro"})
	if provider != "cs" || model != "doubao-pro" {
		t.Fatalf("persona provider/model = %q/%q, want cs/doubao-pro", provider, model)
	}
}

func TestIMConversationTitlePrefixesWeChatDisplayName(t *testing.T) {
	got := imConversationTitle(im.IMEvent{
		Platform: "wechat",
		Variant:  "wechatbot",
		Chat:     im.ChatRef{Platform: "wechat", Variant: "wechatbot", ChatID: "wx-user-1", ChatType: "private"},
		Sender:   im.SenderRef{ID: "wx-user-1", DisplayName: "张三"},
	})
	if got != "微信 · 张三" {
		t.Fatalf("title = %q, want 微信 · 张三", got)
	}
}

func TestIMConversationTitleShortensWeChatRawID(t *testing.T) {
	rawID := "o9cq80y1DXkot94pBWNtdN9YcvnQ@im.wechat"
	got := imConversationTitle(im.IMEvent{
		Platform: "wechat",
		Variant:  "wechatbot",
		Chat:     im.ChatRef{Platform: "wechat", Variant: "wechatbot", ChatID: rawID, ChatType: "private"},
		Sender:   im.SenderRef{ID: rawID},
	})
	if !strings.HasPrefix(got, "微信 · o9cq80y1DXkot94p") {
		t.Fatalf("title = %q, want wechat-prefixed shortened raw id", got)
	}
	if strings.Contains(got, "@im.wechat") {
		t.Fatalf("title = %q, should not expose @im.wechat suffix", got)
	}
	if len([]rune(strings.TrimPrefix(got, "微信 · "))) > 17 {
		t.Fatalf("title = %q, raw id should be shortened", got)
	}
}

func TestEnsureIMConversationTitleRenamesRawWeChatID(t *testing.T) {
	s, _ := newTestServerWithConfig(t, richTestConfigJSON)
	rawID := "o9cq80y1DXkot94pBWNtdN9YcvnQ@im.wechat"
	sessionID := "im:wechat:u:" + rawID
	if err := s.store.EnsureConversation(sessionID, rawID); err != nil {
		t.Fatal(err)
	}
	ev := im.IMEvent{
		Platform: "wechat",
		Variant:  "wechatbot",
		Chat:     im.ChatRef{Platform: "wechat", Variant: "wechatbot", ChatID: rawID, ChatType: "private"},
		Sender:   im.SenderRef{ID: rawID},
	}
	want := imConversationTitle(ev)

	s.handler.ensureIMConversationTitle(sessionID, ev, want)

	got, err := s.store.GetConversation(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != want {
		t.Fatalf("title = %q, want %q", got.Title, want)
	}
}

func TestEnsureIMConversationTitlePrefixesLegacyDisplayName(t *testing.T) {
	s, _ := newTestServerWithConfig(t, richTestConfigJSON)
	sessionID := "im:wechat:u:user-1"
	if err := s.store.EnsureConversation(sessionID, "张三"); err != nil {
		t.Fatal(err)
	}
	ev := im.IMEvent{
		Platform: "wechat",
		Variant:  "wechatbot",
		Chat:     im.ChatRef{Platform: "wechat", Variant: "wechatbot", ChatID: "user-1", ChatType: "private"},
		Sender:   im.SenderRef{ID: "user-1", DisplayName: "张三"},
	}

	s.handler.ensureIMConversationTitle(sessionID, ev, imConversationTitle(ev))

	got, err := s.store.GetConversation(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "微信 · 张三" {
		t.Fatalf("title = %q, want 微信 · 张三", got.Title)
	}
}

func TestEnsureIMConversationTitleKeepsUserRename(t *testing.T) {
	s, _ := newTestServerWithConfig(t, richTestConfigJSON)
	sessionID := "im:wechat:u:user-1"
	if err := s.store.EnsureConversation(sessionID, "客户 A"); err != nil {
		t.Fatal(err)
	}
	ev := im.IMEvent{
		Platform: "wechat",
		Variant:  "wechatbot",
		Chat:     im.ChatRef{Platform: "wechat", Variant: "wechatbot", ChatID: "user-1", ChatType: "private"},
		Sender:   im.SenderRef{ID: "user-1", DisplayName: "张三"},
	}

	s.handler.ensureIMConversationTitle(sessionID, ev, imConversationTitle(ev))

	got, err := s.store.GetConversation(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Title != "客户 A" {
		t.Fatalf("title = %q, want user rename to stay", got.Title)
	}
}

func TestIMConversationResponseTitleLabelsStoredRawTitle(t *testing.T) {
	rawID := "o9cq80y1DXkot94pBWNtdN9YcvnQ@im.wechat"
	got := imConversationResponseTitle("im:wechat:u:"+rawID, rawID)
	if !strings.HasPrefix(got, "微信 · o9cq80y1DXkot94p") {
		t.Fatalf("title = %q, want wechat-prefixed shortened response title", got)
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
			if r.Method != http.MethodPost {
				t.Fatalf("qr method = %s, want POST", r.Method)
			}
			if r.URL.Query().Get("bot_type") != "3" {
				t.Fatalf("bot_type = %q, want 3", r.URL.Query().Get("bot_type"))
			}
			var body struct {
				LocalTokenList []string `json:"local_token_list"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode qr request body: %v", err)
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
			Mode    string         `json:"mode"`
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
	if body.Platforms[0].Mode != "polling" {
		t.Fatalf("mode = %q, want polling", body.Platforms[0].Mode)
	}
	if body.Platforms[0].Extra["ilink_bot_id"] != "bot-1" {
		t.Fatalf("extra = %+v, want ilink_bot_id", body.Platforms[0].Extra)
	}
}

func TestWeChatQRConfirmedWithoutTokenDoesNotPersistCredential(t *testing.T) {
	ilink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ilink/bot/get_bot_qrcode":
			_, _ = w.Write([]byte(`{"code":0,"data":{"qrcode":"qr-no-token","qrcode_img_content":"data:image/png;base64,abc"}}`))
		case "/ilink/bot/get_qrcode_status":
			_, _ = w.Write([]byte(`{"code":0,"data":{"status":"confirmed","ilink_bot_id":"bot-1"}}`))
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
		ID string `json:"id"`
	}
	if err := json.NewDecoder(w.Body).Decode(&start); err != nil {
		t.Fatal(err)
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/v1/im/wechat/qr/"+start.ID, nil)
	s.engine.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("poll status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var poll struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	if err := json.NewDecoder(w.Body).Decode(&poll); err != nil {
		t.Fatal(err)
	}
	if poll.Status != "confirmed_without_token" {
		t.Fatalf("status = %q, want confirmed_without_token; body=%s", poll.Status, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/v1/im/config", nil)
	s.engine.ServeHTTP(w, req)
	if strings.Contains(w.Body.String(), `"token"`) && strings.Contains(w.Body.String(), "bot-1") {
		t.Fatalf("config should not persist incomplete credential: %s", w.Body.String())
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
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "HTTP 502") || strings.Contains(w.Body.String(), "dial tcp") {
		t.Fatalf("response should not expose raw transport error: %s", w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "微信扫码服务暂时无法访问") {
		t.Fatalf("body = %s, want user-readable unavailable message", w.Body.String())
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Status != "unavailable" {
		t.Fatalf("status body = %q, want unavailable", body.Status)
	}
}
