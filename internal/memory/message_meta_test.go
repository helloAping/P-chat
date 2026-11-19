package memory

import (
	"strings"
	"testing"

	"github.com/p-chat/pchat/internal/llm"
)

func TestMergeMessageMetaStampsElapsed(t *testing.T) {
	s := newTestStore(t)
	convID, err := s.NewConversation()
	if err != nil {
		t.Fatalf("NewConversation: %v", err)
	}
	id, err := s.AddChatMessageWithMetaToRegenNow(convID, llm.ChatMessage{
		Role: llm.RoleAssistant, Type: llm.TypeText, Content: "hi",
		MsgType: llm.MsgTypeText, SubmitToLLM: 1,
	}, map[string]string{"tokens_out": "3"}, "", false)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := s.MergeMessageMeta(id, map[string]string{"elapsed": "3.6s"}); err != nil {
		t.Fatalf("MergeMessageMeta: %v", err)
	}
	var raw string
	if err := s.db.QueryRow(`SELECT metadata FROM messages WHERE id = ?`, id).Scan(&raw); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(raw, `"elapsed":"3.6s"`) || !strings.Contains(raw, `"tokens_out":"3"`) {
		t.Fatalf("metadata = %s, want elapsed and the original tokens_out", raw)
	}
}
