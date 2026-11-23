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

func TestStampTurnTodosKeepsTheVisibleUserMessage(t *testing.T) {
	s := newTestStore(t)
	convID, err := s.NewConversation()
	if err != nil {
		t.Fatalf("NewConversation: %v", err)
	}
	visibleID, err := s.AddChatMessageWithMetaToRegenNow(convID, llm.ChatMessage{
		Role: llm.RoleUser, Type: llm.TypeText, Content: "分析一下哪些能清理",
		MsgType: llm.MsgTypeText, SubmitToLLM: 1,
	}, nil, "", false)
	if err != nil {
		t.Fatalf("visible: %v", err)
	}
	hiddenID, err := s.AddChatMessageWithMetaToRegenNow(convID, llm.ChatMessage{
		Role: llm.RoleUser, Type: llm.TypeText, Content: "hidden",
		MsgType: llm.MsgTypeText, SubmitToLLM: 0,
	}, map[string]string{"ui_hidden": "true"}, "", false)
	if err != nil {
		t.Fatalf("visible: %v", err)
	}
	if err := s.StampTurnTodos(convID, []TodoItem{{ID: "a", Content: "核对数据库", Status: "in_progress"}}); err != nil {
		t.Fatalf("stamp: %v", err)
	}
	var hiddenMeta, visibleMeta string
	if err := s.db.QueryRow(`SELECT COALESCE(metadata, '') FROM messages WHERE id = ?`, hiddenID).Scan(&hiddenMeta); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRow(`SELECT COALESCE(metadata, '') FROM messages WHERE id = ?`, visibleID).Scan(&visibleMeta); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(hiddenMeta, "turn_todos") {
		t.Fatalf("hidden message received the plan: %s", hiddenMeta)
	}
	if !strings.Contains(visibleMeta, "核对数据库") {
		t.Fatalf("visible metadata = %s", visibleMeta)
	}
}
