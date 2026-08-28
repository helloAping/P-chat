package im

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

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
		"message_type": 2,
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

func TestParseWeChatEventDoesNotTreatQRCodeUserAsSelf(t *testing.T) {
	raw := json.RawMessage(`{
		"message_id": 123,
		"from_user_id": "user-1@im.wechat",
		"to_user_id": "bot-1@im.bot",
		"message_type": 1,
		"message_state": 2,
		"context_token": "ctx-1",
		"item_list": [
			{"type": 1, "text_item": {"text": "你好"}}
		]
	}`)
	ev, ok, err := parseWeChatEvent(raw, config.IMPlatformConfig{
		Type:    "wechat",
		Variant: "wechatbot",
		Extra: map[string]any{
			"ilink_bot_id":  "bot-1@im.bot",
			"ilink_user_id": "user-1@im.wechat",
		},
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !ok {
		t.Fatal("qr user message should not be ignored")
	}
	if ev.Chat.ChatID != "user-1@im.wechat" || ev.Sender.ID != "user-1@im.wechat" {
		t.Fatalf("event = %+v, want sender/chat user-1@im.wechat", ev)
	}
	if ev.Text != "你好" || ev.ContextToken != "ctx-1" {
		t.Fatalf("event text/context = %q/%q, want inbound text and context", ev.Text, ev.ContextToken)
	}
}

func TestWeChatAdapterSendUsesPersistedContextToken(t *testing.T) {
	var gotAuth string
	var gotAppID string
	var gotClientVersion string
	var gotPath string
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotAppID = r.Header.Get("iLink-App-Id")
		gotClientVersion = r.Header.Get("iLink-App-ClientVersion")
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
		Extra: map[string]any{
			"ilink_app_id":         "wx-app-1",
			"ilink_client_version": "9",
		},
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
	if gotAppID != "wx-app-1" || gotClientVersion != "9" {
		t.Fatalf("iLink headers = (%q, %q), want configured app id/version", gotAppID, gotClientVersion)
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

func TestWeChatUpdatesNormalizeReadsOpenClawListAliases(t *testing.T) {
	var resp wechatUpdatesResponse
	if err := json.Unmarshal([]byte(`{
		"ret": 0,
		"data": {
			"cursor": "cursor-2",
			"msg_list": [
				{
					"msg": {
						"msg_id": "m-alias-1",
						"from_user_id": "user-1",
						"context_token": "ctx-1",
						"text": "hello alias"
					}
				}
			]
		}
	}`), &resp); err != nil {
		t.Fatal(err)
	}
	resp.normalize()
	if resp.Cursor != "cursor-2" {
		t.Fatalf("cursor = %q, want cursor-2", resp.Cursor)
	}
	if len(resp.Messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(resp.Messages))
	}
	ev, ok, err := parseWeChatEvent(resp.Messages[0], config.IMPlatformConfig{Type: "wechat", Variant: "wechatbot"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !ok || ev.ID != "m-alias-1" || ev.Text != "hello alias" {
		t.Fatalf("event = %+v ok=%v, want alias message", ev, ok)
	}
}

func TestWeChatUpdatesNormalizeReadsTopLevelListAliases(t *testing.T) {
	var resp wechatUpdatesResponse
	if err := json.Unmarshal([]byte(`{
		"ret": 0,
		"next_cursor": "cursor-top",
		"msg_list": [
			{
				"message": {
					"msg": {
						"msg_id": "m-top-1",
						"from_user_id": "user-1",
						"context_token": "ctx-top",
						"text": "hello top alias"
					}
				}
			}
		]
	}`), &resp); err != nil {
		t.Fatal(err)
	}
	resp.normalize()
	if resp.Cursor != "cursor-top" {
		t.Fatalf("cursor = %q, want cursor-top", resp.Cursor)
	}
	if len(resp.Messages) != 1 {
		t.Fatalf("messages len = %d, want 1", len(resp.Messages))
	}
	ev, ok, err := parseWeChatEvent(resp.Messages[0], config.IMPlatformConfig{Type: "wechat", Variant: "wechatbot"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !ok || ev.ID != "m-top-1" || ev.Text != "hello top alias" || ev.ContextToken != "ctx-top" {
		t.Fatalf("event = %+v ok=%v, want top-level alias message", ev, ok)
	}
}

func TestRegisterWeChatAdapterSkipsUnsupportedMode(t *testing.T) {
	cfg := config.DefaultIMConfig()
	cfg.Enabled = true
	cfg.Platforms = []config.IMPlatformConfig{{
		Type:    "wechat",
		Variant: "wechatbot",
		Enabled: true,
		Mode:    "websocket",
		Token:   "token-1",
	}}
	g := NewGateway(cfg)
	RegisterConfiguredWeChatAdapters(g, cfg)

	result := g.TestConnection("wechat", "wechatbot")
	if result.OK || result.Status != "not_implemented" {
		t.Fatalf("test result = %+v, want unsupported mode to leave adapter unregistered", result)
	}
}

func TestWeChatAdapterPollLoopDoesNotBlockOnNotifyStart(t *testing.T) {
	updatesCalled := make(chan struct{}, 1)
	releaseNotifyStart := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case wechatNotifyStartPath:
			select {
			case <-releaseNotifyStart:
				_, _ = w.Write([]byte(`{"ret":0}`))
			case <-r.Context().Done():
			}
		case wechatNotifyStopPath:
			_, _ = w.Write([]byte(`{"ret":0}`))
		case wechatLongPollPath:
			select {
			case updatesCalled <- struct{}{}:
			default:
			}
			_, _ = w.Write([]byte(`{"ret":0,"data":{"get_updates_buf":"cursor-1","msgs":[]}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	defer close(releaseNotifyStart)

	adapter := NewWeChatAdapter(config.IMPlatformConfig{
		Type:     "wechat",
		Variant:  "wechatbot",
		Enabled:  true,
		Token:    "token-1",
		Endpoint: srv.URL,
	})
	adapter.statePath = filepath.Join(t.TempDir(), "wechat-state.json")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := adapter.Start(ctx, make(chan IMEvent, 1)); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer adapter.Stop(context.Background())

	select {
	case <-updatesCalled:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("getupdates was blocked by notifystart")
	}
	health := adapter.Health()
	if health.LastPollAt.IsZero() {
		t.Fatal("last_poll_at should be recorded even when notifystart is still pending")
	}
}

func TestWeChatAdapterPollLoopReadsDataWrappedMessages(t *testing.T) {
	var updatesCalled bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case wechatNotifyStartPath, wechatNotifyStopPath:
			_, _ = w.Write([]byte(`{"ret":0}`))
		case wechatLongPollPath:
			updatesCalled = true
			_, _ = w.Write([]byte(`{
				"ret": 0,
				"data": {
					"get_updates_buf": "cursor-1",
					"msgs": [
						{
							"msg": {
								"msg_id": "m-data-1",
								"from_user_id": "user-1",
								"context_token": "ctx-1",
								"item_list": [
									{"type": 1, "text_item": {"text": "hello from data wrapper"}}
								]
							}
						}
					]
				}
			}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	adapter := NewWeChatAdapter(config.IMPlatformConfig{
		Type:     "wechat",
		Variant:  "wechatbot",
		Enabled:  true,
		Token:    "token-1",
		Endpoint: srv.URL,
	})
	adapter.statePath = filepath.Join(t.TempDir(), "wechat-state.json")
	in := make(chan IMEvent, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := adapter.Start(ctx, in); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer adapter.Stop(context.Background())

	select {
	case ev := <-in:
		if ev.ID != "m-data-1" || ev.Text != "hello from data wrapper" || ev.ContextToken != "ctx-1" {
			t.Fatalf("event = %+v, want data-wrapped message", ev)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for data-wrapped wechat event")
	}
	if !updatesCalled {
		t.Fatal("getupdates was not called")
	}
	health := adapter.Health()
	if health.LastPollAt.IsZero() {
		t.Fatal("last_poll_at should be recorded after polling")
	}
	if health.LastInboundAt.IsZero() {
		t.Fatal("last_inbound_at should be recorded after receiving a message")
	}
	if _, err := os.Stat(adapter.statePath); err != nil {
		t.Fatalf("state file should be saved: %v", err)
	}
}

func TestWeChatAdapterPollLoopPublishesQRCodeUserMessages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case wechatNotifyStartPath, wechatNotifyStopPath:
			_, _ = w.Write([]byte(`{"ret":0}`))
		case wechatLongPollPath:
			_, _ = w.Write([]byte(`{
				"ret": 0,
				"msgs": [
					{
						"msg_id": "m-user-1",
						"from_user_id": "user-1@im.wechat",
						"to_user_id": "bot-1@im.bot",
						"message_type": 1,
						"context_token": "ctx-user",
						"item_list": [
							{"type": 1, "text_item": {"text": "你好"}}
						]
					}
				]
			}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	adapter := NewWeChatAdapter(config.IMPlatformConfig{
		Type:     "wechat",
		Variant:  "wechatbot",
		Enabled:  true,
		Token:    "token-1",
		Endpoint: srv.URL,
		Extra: map[string]any{
			"ilink_bot_id":  "bot-1@im.bot",
			"ilink_user_id": "user-1@im.wechat",
		},
	})
	adapter.statePath = filepath.Join(t.TempDir(), "wechat-state.json")
	in := make(chan IMEvent, 1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := adapter.Start(ctx, in); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer adapter.Stop(context.Background())

	select {
	case ev := <-in:
		if ev.ID != "m-user-1" || ev.Text != "你好" || ev.ContextToken != "ctx-user" {
			t.Fatalf("event = %+v, want qr user inbound message", ev)
		}
		if ev.Sender.ID != "user-1@im.wechat" || ev.Chat.ChatID != "user-1@im.wechat" {
			t.Fatalf("event sender/chat = %+v/%+v, want qr user id", ev.Sender, ev.Chat)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for qr user wechat event")
	}
}

func TestWeChatAdapterHealthSeparatesCurrentAndLastError(t *testing.T) {
	adapter := NewWeChatAdapter(config.IMPlatformConfig{
		Type:    "wechat",
		Variant: "wechatbot",
		Enabled: true,
		Token:   "token-1",
	})
	adapter.started = true
	adapter.status = "polling"

	adapter.setError(errors.New("temporary network error"))
	health := adapter.Health()
	if health.Error != "temporary network error" || health.LastError != "temporary network error" {
		t.Fatalf("health after error = %+v, want current and last error", health)
	}

	adapter.markPollOK()
	health = adapter.Health()
	if health.Error != "" {
		t.Fatalf("current error = %q, want cleared after successful poll", health.Error)
	}
	if health.LastError != "temporary network error" {
		t.Fatalf("last error = %q, want preserved diagnostic", health.LastError)
	}
	if health.Status != "polling" {
		t.Fatalf("status = %q, want polling", health.Status)
	}
}
