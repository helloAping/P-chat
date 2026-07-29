package im

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/p-chat/pchat/internal/config"
)

func TestParseWeChatEventExtractsItemListTextAndContext(t *testing.T) {
	raw := json.RawMessage(`{
		"msg": {
			"msg_id": "m-1",
			"from_user_id": "user-1",
			"to_user_id": "bot-1",
			"context_token": "ctx-1",
			"item_list": [
				{"type": 1, "text_item": {"text": "hello"}},
				{"type": 1, "text_item": {"text": "P-Chat"}}
			],
			"create_time": 1720000000
		}
	}`)
	ev, ok, err := parseWeChatEvent(raw, config.IMPlatformConfig{
		Type:    "wechat",
		Variant: "wechatbot",
		Extra:   map[string]any{"ilink_bot_id": "bot-1"},
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !ok {
		t.Fatal("event was ignored")
	}
	if ev.Chat.ChatID != "user-1" {
		t.Fatalf("chat id = %q, want user-1", ev.Chat.ChatID)
	}
	if ev.Sender.ID != "user-1" {
		t.Fatalf("sender id = %q, want user-1", ev.Sender.ID)
	}
	if ev.Text != "hello\nP-Chat" {
		t.Fatalf("text = %q, want joined item_list text", ev.Text)
	}
	if ev.ContextToken != "ctx-1" {
		t.Fatalf("context token = %q, want ctx-1", ev.ContextToken)
	}
}

func TestParseWeChatEventIgnoresSelfMessages(t *testing.T) {
	raw := json.RawMessage(`{
		"from_user_id": "bot-1",
		"to_user_id": "user-1",
		"text": "echo"
	}`)
	_, ok, err := parseWeChatEvent(raw, config.IMPlatformConfig{
		Type:  "wechat",
		Extra: map[string]any{"ilink_bot_id": "bot-1"},
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ok {
		t.Fatal("self message should be ignored")
	}
}

func TestWeChatAdapterSendUsesPersistedContextToken(t *testing.T) {
	var gotAuth string
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ret":0}`))
	}))
	defer srv.Close()

	adapter := NewWeChatAdapter(config.IMPlatformConfig{
		Type:     "wechat",
		Variant:  "wechatbot",
		Enabled:  true,
		Token:    "token-1",
		Endpoint: srv.URL,
	})
	adapter.state.ContextTokens["user-1"] = "ctx-1"

	err := adapter.Send(context.Background(), IMOutChunk{
		Platform: "wechat",
		Chat:     ChatRef{Platform: "wechat", Variant: "wechatbot", ChatID: "user-1"},
		Kind:     "text",
		Text:     "reply text",
	})
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if gotPath != wechatSendMessagePath {
		t.Fatalf("path = %q, want %q", gotPath, wechatSendMessagePath)
	}
	if gotAuth != "Bearer token-1" {
		t.Fatalf("authorization = %q, want bearer token", gotAuth)
	}
	msg, ok := gotBody["msg"].(map[string]any)
	if !ok {
		t.Fatalf("msg body = %#v, want object", gotBody["msg"])
	}
	if msg["to_user_id"] != "user-1" {
		t.Fatalf("to_user_id = %#v, want user-1", msg["to_user_id"])
	}
	if msg["context_token"] != "ctx-1" {
		t.Fatalf("context_token = %#v, want ctx-1", msg["context_token"])
	}
	items, ok := msg["item_list"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("item_list = %#v, want one item", msg["item_list"])
	}
	item, _ := items[0].(map[string]any)
	textItem, _ := item["text_item"].(map[string]any)
	if textItem["text"] != "reply text" {
		t.Fatalf("text item = %#v, want reply text", textItem)
	}
}
