package feishu

import (
	"errors"
	"testing"

	"github.com/p-chat/pchat/internal/config"
)

func TestParseURLVerification(t *testing.T) {
	payload := []byte(`{"type":"url_verification","token":"verify-token","challenge":"challenge-123"}`)
	result, err := ParseCallback(payload, config.IMPlatformConfig{VerificationToken: "verify-token"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if result.Challenge != "challenge-123" {
		t.Fatalf("challenge = %q, want challenge-123", result.Challenge)
	}
	if result.Event != nil {
		t.Fatalf("event = %+v, want nil", result.Event)
	}
}

func TestParseReceiveV1TextEvent(t *testing.T) {
	payload := []byte(`{
	  "schema": "2.0",
	  "header": {
	    "event_id": "ev_123",
	    "event_type": "im.message.receive_v1",
	    "create_time": "1721880000123",
	    "token": "verify-token",
	    "app_id": "cli_xxx",
	    "tenant_key": "tenant_x"
	  },
	  "event": {
	    "sender": {
	      "sender_id": { "open_id": "ou_sender", "user_id": "u_sender" },
	      "sender_type": "user"
	    },
	    "message": {
	      "message_id": "om_msg",
	      "root_id": "om_root",
	      "parent_id": "om_parent",
	      "chat_id": "oc_group",
	      "chat_type": "group",
	      "message_type": "text",
	      "content": "{\"text\":\"hello @PChat\"}",
	      "mentions": [
	        {
	          "key": "@_user_1",
	          "id": { "open_id": "ou_bot" },
	          "name": "PChat"
	        }
	      ]
	    }
	  }
	}`)
	result, err := ParseCallback(payload, config.IMPlatformConfig{
		Variant:           "bot",
		VerificationToken: "verify-token",
		Extra: map[string]any{
			"bot_open_id": "ou_bot",
		},
	})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if result.Event == nil {
		t.Fatal("event = nil, want IMEvent")
	}
	ev := result.Event
	if ev.ID != "om_msg" {
		t.Fatalf("id = %q, want om_msg", ev.ID)
	}
	if ev.Platform != "feishu" || ev.Variant != "bot" {
		t.Fatalf("platform/variant = %q/%q, want feishu/bot", ev.Platform, ev.Variant)
	}
	if ev.Chat.ChatID != "oc_group" || ev.Chat.ChatType != "group" || ev.Chat.ThreadID != "om_root" {
		t.Fatalf("chat = %+v", ev.Chat)
	}
	if ev.ReplyTo == nil || *ev.ReplyTo != "om_parent" {
		t.Fatalf("reply_to = %v, want om_parent", ev.ReplyTo)
	}
	if ev.Sender.ID != "ou_sender" {
		t.Fatalf("sender = %+v", ev.Sender)
	}
	if ev.Text != "hello @PChat" {
		t.Fatalf("text = %q, want hello @PChat", ev.Text)
	}
	if len(ev.Mentions) != 1 || ev.Mentions[0].ID != "ou_bot" || !ev.Mentions[0].Bot {
		t.Fatalf("mentions = %+v, want bot mention ou_bot", ev.Mentions)
	}
}

func TestParseRejectsInvalidVerificationToken(t *testing.T) {
	payload := []byte(`{"type":"url_verification","token":"bad-token","challenge":"challenge-123"}`)
	_, err := ParseCallback(payload, config.IMPlatformConfig{VerificationToken: "verify-token"})
	if err == nil {
		t.Fatal("expected invalid token error")
	}
}

func TestParseUnsupportedEvent(t *testing.T) {
	payload := []byte(`{"schema":"2.0","header":{"event_type":"contact.user.created_v3","token":"verify-token"},"event":{}}`)
	_, err := ParseCallback(payload, config.IMPlatformConfig{VerificationToken: "verify-token"})
	if !errors.Is(err, ErrUnsupportedEvent) {
		t.Fatalf("err = %v, want ErrUnsupportedEvent", err)
	}
}

func TestParseUnsupportedMessageType(t *testing.T) {
	payload := []byte(`{
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
	}`)
	_, err := ParseCallback(payload, config.IMPlatformConfig{VerificationToken: "verify-token"})
	if !errors.Is(err, ErrUnsupportedEvent) {
		t.Fatalf("err = %v, want ErrUnsupportedEvent", err)
	}
}

func TestParseEncryptedCallbackUnsupported(t *testing.T) {
	payload := []byte(`{"encrypt":"ciphertext"}`)
	_, err := ParseCallback(payload, config.IMPlatformConfig{VerificationToken: "verify-token"})
	if !errors.Is(err, ErrEncryptedCallbackUnsupported) {
		t.Fatalf("err = %v, want ErrEncryptedCallbackUnsupported", err)
	}
}
