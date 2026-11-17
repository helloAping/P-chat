package memory

import (
	"testing"

	"github.com/p-chat/pchat/internal/llm"
)

func TestReplaceTurnChangesSurvivesReload(t *testing.T) {
	s := testStore(t)
	id := s.CurrentConversationID()
	s.AddChatMessageTo(id, llm.ChatMessage{Role: llm.RoleAssistant, Content: "done"})
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	msgID := s.LatestAssistantMessageID(id)
	if msgID == 0 {
		t.Fatal("missing assistant row")
	}
	err := s.ReplaceTurnChanges(id, msgID, []TurnChange{
		{Tool: "write_file", Path: "a.go", Preview: "a.go"},
		{Tool: "exec_command", Preview: "go test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	list, err := s.ListTurnChanges(id)
	if err != nil || len(list) != 2 || list[1].Preview != "go test" {
		t.Fatalf("list = %#v %v", list, err)
	}
	if err := s.DeleteConversation(id); err != nil {
		t.Fatal(err)
	}
	left, err := s.ListTurnChanges(id)
	if err != nil || len(left) != 0 {
		t.Fatalf("after delete = %#v %v", left, err)
	}
}
