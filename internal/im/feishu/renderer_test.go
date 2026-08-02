package feishu

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/im"
)

func TestRendererSendTextMessage(t *testing.T) {
	var tokenRequests int
	var sendRequests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			tokenRequests++
			if r.Method != http.MethodPost {
				t.Fatalf("token method = %s, want POST", r.Method)
			}
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode token body: %v", err)
			}
			if body["app_id"] != "cli_xxx" || body["app_secret"] != "secret_x" {
				t.Fatalf("token body = %+v", body)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"t-token","expire":7200}`))
		case "/open-apis/im/v1/messages":
			sendRequests++
			if r.Method != http.MethodPost {
				t.Fatalf("send method = %s, want POST", r.Method)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer t-token" {
				t.Fatalf("authorization = %q, want bearer token", got)
			}
			if got := r.URL.Query().Get("receive_id_type"); got != "chat_id" {
				t.Fatalf("receive_id_type = %q, want chat_id", got)
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
			var content map[string]string
			if err := json.Unmarshal([]byte(body.Content), &content); err != nil {
				t.Fatalf("decode content: %v", err)
			}
			if content["text"] != "hello feishu" {
				t.Fatalf("content text = %q", content["text"])
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"om_sent"}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	renderer := NewRenderer(config.IMPlatformConfig{
		AppID:     "cli_xxx",
		AppSecret: "secret_x",
		Out:       config.IMOutboundConfig{APIBase: srv.URL},
	}, srv.Client())

	messageID, err := renderer.SendMessage(context.Background(), im.IMOutChunk{
		Platform: "feishu",
		Chat:     im.ChatRef{Platform: "feishu", ChatID: "oc_group"},
		Kind:     "text",
		Text:     "hello feishu",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if messageID != "om_sent" {
		t.Fatalf("message id = %q, want om_sent", messageID)
	}
	if tokenRequests != 1 || sendRequests != 1 {
		t.Fatalf("token/send requests = %d/%d, want 1/1", tokenRequests, sendRequests)
	}
}

func TestRendererReusesTenantToken(t *testing.T) {
	var tokenRequests int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			tokenRequests++
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"t-token","expire":7200}`))
		case "/open-apis/im/v1/messages":
			_, _ = w.Write([]byte(`{"code":0,"data":{"message_id":"om_sent"}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	renderer := NewRenderer(config.IMPlatformConfig{
		AppID:     "cli_xxx",
		AppSecret: "secret_x",
		Out:       config.IMOutboundConfig{APIBase: srv.URL},
	}, srv.Client())

	chunk := im.IMOutChunk{Platform: "feishu", Chat: im.ChatRef{Platform: "feishu", ChatID: "oc_group"}, Kind: "text", Text: "hello"}
	if err := renderer.Send(context.Background(), chunk); err != nil {
		t.Fatalf("first send: %v", err)
	}
	if err := renderer.Send(context.Background(), chunk); err != nil {
		t.Fatalf("second send: %v", err)
	}
	if tokenRequests != 1 {
		t.Fatalf("token requests = %d, want 1", tokenRequests)
	}
}

func TestRendererSendRequiresChatID(t *testing.T) {
	renderer := NewRenderer(config.IMPlatformConfig{}, nil)
	err := renderer.Send(context.Background(), im.IMOutChunk{Text: "hello"})
	if err == nil {
		t.Fatal("expected error for missing chat id")
	}
}

func TestRendererEditTextMessage(t *testing.T) {
	var edited bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			_, _ = w.Write([]byte(`{"code":0,"tenant_access_token":"t-token","expire":7200}`))
		case "/open-apis/im/v1/messages/om_msg":
			edited = true
			if r.Method != http.MethodPatch {
				t.Fatalf("edit method = %s, want PATCH", r.Method)
			}
			var body struct {
				MsgType string `json:"msg_type"`
				Content string `json:"content"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode edit body: %v", err)
			}
			if body.MsgType != "text" {
				t.Fatalf("msg_type = %q, want text", body.MsgType)
			}
			var content map[string]string
			if err := json.Unmarshal([]byte(body.Content), &content); err != nil {
				t.Fatalf("decode content: %v", err)
			}
			if content["text"] != "updated" {
				t.Fatalf("content text = %q, want updated", content["text"])
			}
			_, _ = w.Write([]byte(`{"code":0}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	renderer := NewRenderer(config.IMPlatformConfig{
		AppID:     "cli_xxx",
		AppSecret: "secret_x",
		Out:       config.IMOutboundConfig{APIBase: srv.URL},
	}, srv.Client())
	if err := renderer.Edit(context.Background(), im.ChatRef{Platform: "feishu", ChatID: "oc_group"}, "om_msg", im.IMOutChunk{Text: "updated"}); err != nil {
		t.Fatalf("edit: %v", err)
	}
	if !edited {
		t.Fatal("edit endpoint was not called")
	}
}

func TestRendererRejectsTooLongText(t *testing.T) {
	renderer := NewRenderer(config.IMPlatformConfig{}, nil)
	long := strings.Repeat("x", renderer.MaxTextLen()+1)
	err := renderer.Send(context.Background(), im.IMOutChunk{
		Chat: im.ChatRef{Platform: "feishu", ChatID: "oc_group"},
		Text: long,
	})
	if err == nil {
		t.Fatal("expected too-long send error")
	}
	err = renderer.Edit(context.Background(), im.ChatRef{Platform: "feishu", ChatID: "oc_group"}, "om_msg", im.IMOutChunk{Text: long})
	if err == nil {
		t.Fatal("expected too-long edit error")
	}
}
