package server

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/p-chat/pchat/internal/agent"
	"github.com/p-chat/pchat/internal/llm"
)

// TestRollbackMessages_QuestionToolResultIsFiltered
// verifies the bug fix: when a message carries a question
// tool's tool_result (the JSON `{questions:[...], answers:{...}}`),
// the rollback response must NOT include the standalone
// tool_result row as a text bubble. The row's payload is
// the raw question payload — the frontend already renders
// the same data via the question part in the parent
// assistant message, so re-emitting it as a free-floating
// text message shows the user "the question parameters as
// text" alongside the question card.
//
// Root cause: tool_result rows in the DB were being
// persisted with msg_type=0 (default) because agent.go
// didn't set MsgType when building the ChatMessage. The
// filter in buildMessageResponse (`if m.MsgType ==
// llm.MsgTypeTool || m.MsgType == llm.MsgTypeCommand
// { return nil }`) only drops rows whose msg_type is
// explicitly 4 (tool) or 5 (command), so the unfiltered
// tool_result row leaked into the rollback response — and
// therefore into the chat after the user's undo spliced
// the response back in.
func TestRollbackMessages_QuestionToolResultIsFiltered(t *testing.T) {
	s, _ := newTestServer(t)
	store := s.store

	if _, err := store.NewConversation(); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrent(store.CurrentConversationID()); err != nil {
		t.Fatal(err)
	}

	// Setup: user → assistant (with question card) →
	// tool_result (the question's raw JSON payload) →
	// user. The tool_result is what the agent.go
	// tool loop persists right after the question tool
	// returns. The bug was: with the fix, this row gets
	// msg_type=4 and is filtered; without the fix it
	// gets msg_type=0 and leaks through.
	store.AddMessage(llm.Message{Role: "user", Content: "first question"})

	partsBlob := []agent.MessagePart{
		{Kind: "question", Text: `{"questions":[{"header":"心情","question":"今天心情如何？","options":[{"label":"还行"}]}]}`, Name: `{"心情":"还行"}`, QuestionStatus: "ok"},
	}
	partsJSON, _ := json.Marshal(partsBlob)
	store.AddChatMessageWithMeta(llm.ChatMessage{
		Role:    llm.RoleAssistant,
		MsgType: llm.MsgTypeText, // the assistant message
	}, map[string]string{
		"parts": string(partsJSON),
	})

	// Standalone tool_result row — this is what the
	// question tool's CallResult.Content looks like in
	// the agent's tool loop, before the fix.
	questionResultJSON := `{"questions":[{"header":"心情","question":"今天心情如何？","options":[{"label":"还行"}]}],"answers":{"心情":"还行"}}`
	store.AddChatMessageTo(store.CurrentConversationID(), llm.ChatMessage{
		Role:     llm.RoleTool,
		Type:     llm.TypeToolResult,
		Content:  questionResultJSON,
		ToolID:   "call_1",
		ToolName: "question",
		MsgType:  llm.MsgTypeTool, // ← THE FIX
	})

	store.AddMessage(llm.Message{Role: "user", Content: "next message"})
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}

	// Rollback captures all 4 rows.
	_, _, _, rowIDs, _, _, _ := store.GetChatMessagesWithMetaPage(store.CurrentConversationID(), 0, 0)
	if len(rowIDs) < 4 {
		t.Fatalf("setup: want 4 rows, got %d", len(rowIDs))
	}
	rollbackID := rowIDs[0]

	body, _ := json.Marshal(map[string]any{"before_id": rollbackID})
	req := httptest.NewRequest("POST",
		"/api/v1/sessions/"+store.CurrentConversationID()+"/rollback",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.engine.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("rollback: status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	var resp struct {
		DeletedCount int               `json:"deleted_count"`
		DeletedMsgs  []MessageResponse `json:"deleted_messages"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}

	// The tool_result row must be filtered out by
	// buildMessageResponse. The rollback should
	// return 3 messages: 2 user + 1 assistant.
	if resp.DeletedCount != 3 {
		t.Errorf("deleted_count = %d, want 3 (1 user + 1 asst with question + 1 user; tool_result filtered)", resp.DeletedCount)
	}

	// No message in the response should have the question
	// tool's raw JSON as plain text content. If the
	// bug regresses, the tool_result row leaks through
	// with content=`{"questions":[...],"answers":{...}}`
	// and shows up here.
	for i, m := range resp.DeletedMsgs {
		if m.Role == "tool" {
			t.Errorf("msg[%d] role=tool should have been filtered, got %+v", i, m)
		}
		if strings.Contains(m.Content, `"answers"`) {
			t.Errorf("msg[%d] content contains raw question tool JSON (the bug): %s", i, m.Content)
		}
	}
}
