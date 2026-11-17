package memory

import (
	"testing"

	"github.com/p-chat/pchat/internal/llm"
)

func TestToggleBookmark(t *testing.T) {
	s := testStore(t)
	id := s.CurrentConversationID()
	s.AddChatMessageTo(id, llm.ChatMessage{Role: llm.RoleUser, Content: "bookmark me"})
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	var messageID int64
	if err := s.db.QueryRow(`SELECT id FROM messages WHERE conversation_id = ? ORDER BY id DESC LIMIT 1`, id).Scan(&messageID); err != nil {
		t.Fatal(err)
	}
	on, err := s.ToggleBookmark(id, messageID)
	if err != nil || !on {
		t.Fatalf("pin: on=%v err=%v", on, err)
	}
	list, err := s.ListBookmarks(id)
	if err != nil || len(list) != 1 || list[0].MessageID != messageID {
		t.Fatalf("list: %#v %v", list, err)
	}
	on, err = s.ToggleBookmark(id, messageID)
	if err != nil || on {
		t.Fatalf("unpin: on=%v err=%v", on, err)
	}
	list, err = s.ListBookmarks(id)
	if err != nil || len(list) != 0 {
		t.Fatalf("list after unpin: %#v %v", list, err)
	}
}

func TestDeleteConversationRemovesBookmarks(t *testing.T) {
	s := testStore(t)
	id := s.CurrentConversationID()
	s.AddChatMessageTo(id, llm.ChatMessage{Role: llm.RoleUser, Content: "keep this line"})
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	var messageID int64
	if err := s.db.QueryRow(`SELECT id FROM messages WHERE conversation_id = ? ORDER BY id DESC LIMIT 1`, id).Scan(&messageID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ToggleBookmark(id, messageID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteConversation(id); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM message_bookmarks WHERE conversation_id = ?`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("bookmarks left after delete: %d", n)
	}
}

func TestUserTurnsIgnorePagingAndHiddenPrompts(t *testing.T) {
	s := testStore(t)
	id := s.CurrentConversationID()
	s.AddChatMessageTo(id, llm.ChatMessage{Role: llm.RoleUser, Content: "first question"})
	s.AddChatMessageTo(id, llm.ChatMessage{Role: llm.RoleAssistant, Content: "answer"})
	s.AddChatMessageTo(id, llm.ChatMessage{Role: llm.RoleUser, Content: "⚠ 系统检测：你刚才的回复没有调用任何工具，请继续"})
	s.AddChatMessageTo(id, llm.ChatMessage{Role: llm.RoleUser, Content: ""})
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	turns, err := s.ListUserTurns(id)
	if err != nil {
		t.Fatal(err)
	}
	if len(turns) != 2 {
		t.Fatalf("turns: %#v", turns)
	}
	if turns[0].Snippet != "first question" || turns[1].Snippet != "附件" {
		t.Fatalf("snippets: %#v", turns)
	}
}
