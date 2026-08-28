package memory

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/p-chat/pchat/internal/llm"
)

func TestAddChatMessagePersistsExtensionMeta(t *testing.T) {
	store, err := OpenAt(filepath.Join(t.TempDir(), "test.db"), 10)
	if err != nil {
		t.Fatalf("OpenAt: %v", err)
	}
	defer store.Close()
	if err := store.EnsureConversation("session-1", ""); err != nil {
		t.Fatalf("EnsureConversation: %v", err)
	}

	store.AddChatMessageTo("session-1", llm.ChatMessage{
		Role:        llm.RoleUser,
		Type:        llm.TypeText,
		Content:     "continue",
		MsgType:     llm.MsgTypeText,
		SubmitToLLM: 1,
		Meta: map[string]any{
			"origin":    "auto_resume",
			"ui_hidden": true,
		},
	})
	if err := store.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	_, metas, _ := store.GetChatMessagesWithMetaFor("session-1", 10)
	if len(metas) != 1 {
		t.Fatalf("metas len = %d, want 1", len(metas))
	}
	var meta map[string]string
	if err := json.Unmarshal([]byte(metas[0]), &meta); err != nil {
		t.Fatalf("decode meta: %v", err)
	}
	if meta["origin"] != "auto_resume" || meta["ui_hidden"] != "true" {
		t.Fatalf("meta = %+v, want origin=auto_resume ui_hidden=true", meta)
	}
}
