package server

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/p-chat/pchat/internal/im"
)

const feishuWebhookConfigJSON = `{
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
    "platforms": [
      {
        "type": "feishu",
        "variant": "bot",
        "enabled": true,
        "verification_token": "verify-token",
        "extra": { "bot_open_id": "ou_bot" }
      }
    ]
  }
}`

func TestFeishuWebhookURLVerification(t *testing.T) {
	s, cfg := newTestServerWithConfig(t, feishuWebhookConfigJSON)
	s.SetIMGateway(im.NewGateway(cfg.IM))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/im/feishu/webhook", strings.NewReader(`{"type":"url_verification","token":"verify-token","challenge":"challenge-123"}`))
	req.Header.Set("Content-Type", "application/json")
	s.engine.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "challenge-123") {
		t.Fatalf("body = %s, want challenge", w.Body.String())
	}
}

func TestFeishuWebhookPublishesInboundEvent(t *testing.T) {
	s, cfg := newTestServerWithConfig(t, feishuWebhookConfigJSON)
	gateway := im.NewGateway(cfg.IM)
	s.SetIMGateway(gateway)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := gateway.Subscribe(ctx)
	if err := gateway.Start(ctx); err != nil {
		t.Fatalf("start gateway: %v", err)
	}

	payload := `{
	  "schema": "2.0",
	  "header": { "event_type": "im.message.receive_v1", "token": "verify-token" },
	  "event": {
	    "sender": { "sender_id": { "open_id": "ou_sender" } },
	    "message": {
	      "message_id": "om_msg",
	      "chat_id": "oc_group",
	      "chat_type": "group",
	      "message_type": "text",
	      "content": "{\"text\":\"hello\"}"
	    }
	  }
	}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/im/feishu/webhook", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	s.engine.ServeHTTP(w, req)

	if w.Code != 202 {
		t.Fatalf("status = %d, want 202; body=%s", w.Code, w.Body.String())
	}
	deadline := time.After(time.Second)
	for {
		select {
		case ev := <-events:
			if ev.Type == "inbound_received" && ev.Platform == "feishu" && ev.Message == "om_msg" {
				return
			}
		case <-deadline:
			t.Fatal("timed out waiting for inbound lifecycle event")
		}
	}
}

func TestFeishuWebhookRejectsInvalidToken(t *testing.T) {
	s, cfg := newTestServerWithConfig(t, feishuWebhookConfigJSON)
	s.SetIMGateway(im.NewGateway(cfg.IM))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/im/feishu/webhook", strings.NewReader(`{"type":"url_verification","token":"bad-token","challenge":"challenge-123"}`))
	req.Header.Set("Content-Type", "application/json")
	s.engine.ServeHTTP(w, req)

	if w.Code != 401 {
		t.Fatalf("status = %d, want 401; body=%s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "verify-token") {
		t.Fatalf("response leaked verification token: %s", w.Body.String())
	}
}

func TestFeishuWebhookIgnoresUnsupportedEvent(t *testing.T) {
	s, cfg := newTestServerWithConfig(t, feishuWebhookConfigJSON)
	s.SetIMGateway(im.NewGateway(cfg.IM))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/im/feishu/webhook", strings.NewReader(`{"schema":"2.0","header":{"event_type":"contact.user.created_v3","token":"verify-token"},"event":{}}`))
	req.Header.Set("Content-Type", "application/json")
	s.engine.ServeHTTP(w, req)

	if w.Code != 202 {
		t.Fatalf("status = %d, want 202; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "ignored") {
		t.Fatalf("body = %s, want ignored marker", w.Body.String())
	}
}

func TestFeishuWebhookIgnoresUnsupportedMessageType(t *testing.T) {
	s, cfg := newTestServerWithConfig(t, feishuWebhookConfigJSON)
	s.SetIMGateway(im.NewGateway(cfg.IM))

	payload := `{
	  "schema": "2.0",
	  "header": { "event_type": "im.message.receive_v1", "token": "verify-token" },
	  "event": {
	    "sender": { "sender_id": { "open_id": "ou_sender" } },
	    "message": {
	      "message_id": "om_msg",
	      "chat_id": "oc_group",
	      "chat_type": "group",
	      "message_type": "image",
	      "content": "{\"image_key\":\"img_x\"}"
	    }
	  }
	}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/im/feishu/webhook", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	s.engine.ServeHTTP(w, req)

	if w.Code != 202 {
		t.Fatalf("status = %d, want 202; body=%s", w.Code, w.Body.String())
	}
}

func TestFeishuWebhookRejectsEncryptedCallbackAsUnsupported(t *testing.T) {
	s, cfg := newTestServerWithConfig(t, feishuWebhookConfigJSON)
	s.SetIMGateway(im.NewGateway(cfg.IM))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/im/feishu/webhook", strings.NewReader(`{"encrypt":"ciphertext"}`))
	req.Header.Set("Content-Type", "application/json")
	s.engine.ServeHTTP(w, req)

	if w.Code != 400 {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "encrypted feishu callbacks are not supported yet") {
		t.Fatalf("body = %s, want explicit encrypted unsupported error", w.Body.String())
	}
}
