package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/im"
)

func TestRegisterIMAdaptersRegistersFeishuBot(t *testing.T) {
	cfg := config.DefaultIMConfig()
	cfg.Platforms = []config.IMPlatformConfig{
		{Type: "feishu", Variant: "bot", Enabled: true},
	}
	gateway := im.NewGateway(cfg)

	registerIMAdapters(gateway, cfg)

	result := gateway.TestConnection("feishu", "bot")
	if !result.OK || result.Status != "registered" {
		t.Fatalf("test result = %+v, want registered ok", result)
	}
}

func TestRegisterIMAdaptersRegistersFeishuBotEvenBeforeConfigured(t *testing.T) {
	cfg := config.DefaultIMConfig()
	gateway := im.NewGateway(cfg)

	registerIMAdapters(gateway, cfg)

	result := gateway.TestConnection("feishu", "bot")
	if result.Status != "not_configured" {
		t.Fatalf("test result = %+v, want not_configured until config adds platform", result)
	}

	cfg.Platforms = []config.IMPlatformConfig{{Type: "feishu", Variant: "bot", Enabled: true}}
	gateway.UpdateConfig(cfg)
	result = gateway.TestConnection("feishu", "bot")
	if !result.OK || result.Status != "registered" {
		t.Fatalf("test result after config = %+v, want registered ok", result)
	}
}

func TestRegisterIMAdaptersRegistersFeishuRendererFactory(t *testing.T) {
	var sent bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"t-token","expire":7200}`))
		case "/open-apis/im/v1/messages":
			sent = true
			if r.Method != http.MethodPost {
				t.Fatalf("send method = %s, want POST", r.Method)
			}
			var body struct {
				ReceiveID string `json:"receive_id"`
				MsgType   string `json:"msg_type"`
				Content   string `json:"content"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode send body: %v", err)
			}
			if body.ReceiveID != "oc_group" || body.MsgType != "text" {
				t.Fatalf("send body = %+v", body)
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"om_sent"}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	cfg := config.DefaultIMConfig()
	cfg.Enabled = true
	cfg.Platforms = []config.IMPlatformConfig{{
		Type:      "feishu",
		Variant:   "bot",
		Enabled:   true,
		AppID:     "cli_xxx",
		AppSecret: "secret_x",
		Out:       config.IMOutboundConfig{UseOpenAPI: true, APIBase: srv.URL},
	}}
	gateway := im.NewGateway(cfg)

	registerIMAdapters(gateway, cfg)

	err := gateway.DispatchOutbound(context.Background(), im.IMOutChunk{
		Platform: "feishu",
		Chat:     im.ChatRef{Platform: "feishu", Variant: "bot", ChatID: "oc_group"},
		Kind:     "text",
		Text:     "hello feishu",
	})
	if err != nil {
		t.Fatalf("dispatch outbound: %v", err)
	}
	if !sent {
		t.Fatal("feishu send endpoint was not called")
	}
}

func TestRegisterIMAdaptersHonorsFeishuUseOpenAPI(t *testing.T) {
	var called bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		t.Fatalf("unexpected request when use_openapi is false: %s", r.URL.Path)
	}))
	defer srv.Close()

	cfg := config.DefaultIMConfig()
	cfg.Enabled = true
	cfg.Platforms = []config.IMPlatformConfig{{
		Type:      "feishu",
		Variant:   "bot",
		Enabled:   true,
		AppID:     "cli_xxx",
		AppSecret: "secret_x",
		Out:       config.IMOutboundConfig{UseOpenAPI: false, APIBase: srv.URL},
	}}
	gateway := im.NewGateway(cfg)

	registerIMAdapters(gateway, cfg)

	err := gateway.DispatchOutbound(context.Background(), im.IMOutChunk{
		Platform: "feishu",
		Chat:     im.ChatRef{Platform: "feishu", Variant: "bot", ChatID: "oc_group"},
		Kind:     "text",
		Text:     "hello feishu",
	})
	var disabled im.ErrOutboundDisabled
	if !errors.As(err, &disabled) {
		t.Fatalf("error = %v, want ErrOutboundDisabled", err)
	}
	if called {
		t.Fatal("feishu OpenAPI server should not be called")
	}
}
