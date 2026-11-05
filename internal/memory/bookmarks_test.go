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
