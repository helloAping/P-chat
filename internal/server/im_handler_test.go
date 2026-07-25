package server

import (
	"bytes"
	"encoding/json"
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
