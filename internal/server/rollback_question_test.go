package server

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/p-chat/pchat/internal/agent"
	"github.com/p-chat/pchat/internal/llm"
)

// TestRollbackUndo_QuestionPartRoundTrip verifies the full
// frontend flow for a question card: rollback a user message
// that came after an assistant question card → server returns
// the deleted_messages in wire shape with the question part
// preserving its `question_status: "ok"` + `name: <answers
// JSON>` → undo restores them. After the round-trip the DB
// must hold the same question state so a session reload
// still shows the question as "已回答" with the picked
// options highlighted.
//
// Bug history: pre-fix the rollback response had `metadata`
// (a raw JSON string) instead of `parts` (decoded array), so
// the frontend's MessageBubble.vue fell back to plain text
// rendering. Later the wire format was changed to `[]MessageResponse`
// but the question part's `question_status` was lost across
// the rollback → undo cycle if the DB had a stale state. This
// test pins the contract: the question part's state must
// round-trip losslessly.
//
// The test uses `AddChatMessageWithMeta` (the
// protocol-agnostic store API) so the row is written with
// the canonical v2 metadata shape (`meta["parts"]`). This
// is the same path the agent uses at runtime, so the test
// exercises the real read path too — the read path bug
// (decodeChatMessages dropping v2 rows) was fixed in
// 2026-07-09 alongside the streaming-flag fix.
func TestRollbackUndo_QuestionPartRoundTrip(t *testing.T) {
	s, _ := newTestServer(t)
	store := s.store

	if _, err := store.NewConversation(); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrent(store.CurrentConversationID()); err != nil {
		t.Fatal(err)
	}

	store.AddMessage(llm.Message{Role: "user", Content: "first question"})

	// Question card: questions array + answers map (the
	// shape WaitForAnswer emits via sendFn #2).
	questionsJSON := `{"questions":[{"header":"心情","question":"今天心情如何？","options":[{"label":"还行","description":"一般般"}]}]}`
	answersJSON := `{"心情":"还行"}`
	questionPart := agent.MessagePart{
		Kind:           "question",
		Text:           questionsJSON,
		Name:           answersJSON,
		QuestionStatus: "ok",
	}
	partsBlob := []agent.MessagePart{questionPart}
	partsJSON, _ := json.Marshal(partsBlob)

	// Use the protocol-agnostic store API so the row is
	// written with the v2 metadata shape — same path the
	// agent uses at runtime. The content column is left
	// empty (matches the live stream: the question card's
	// text lives entirely in parts).
	store.AddChatMessageWithMeta(llm.ChatMessage{
		Role: llm.RoleAssistant,
	}, map[string]string{
		"parts": string(partsJSON),
	})
	store.AddMessage(llm.Message{Role: "user", Content: "next message after answer"})
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}

	// Rollback to the OLDEST row. GetChatMessagesWithMetaPage
	// returns ids in ASC order (see the function's
	// rev[n-1-i] flip), so rowIDs[0] is the oldest.
	_, _, _, rowIDs, _ := store.GetChatMessagesWithMetaPage(store.CurrentConversationID(), 0, 0)
	if len(rowIDs) < 3 {
		t.Fatalf("setup: want 3 rows, got %d", len(rowIDs))
	}
	rollbackID := rowIDs[0]

	// Step 1: POST /rollback
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

	var rollbackResp struct {
		DeletedCount int               `json:"deleted_count"`
		DeletedMsgs  []MessageResponse `json:"deleted_messages"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &rollbackResp); err != nil {
		t.Fatalf("decode rollback: %v", err)
	}
	// We expect user1 + assistant + user2 = 3, none
	// filtered. (The assistant has v2 metadata; the
	// decodeChatMessages fix in 2026-07-09 keeps it in
	// the GetChatMessagesWithMetaPage return; the
	// RollbackMessages buildMessageResponse loop
	// produces a MessageResponse for every non-tool row.)
	if rollbackResp.DeletedCount != 3 {
		t.Fatalf("deleted_count = %d, want 3 (1 user + 1 assistant + 1 user)", rollbackResp.DeletedCount)
	}

	// Find the assistant message in the rollback response.
	var asst *MessageResponse
	for i := range rollbackResp.DeletedMsgs {
		if rollbackResp.DeletedMsgs[i].Role == "assistant" {
			asst = &rollbackResp.DeletedMsgs[i]
			break
		}
	}
	if asst == nil {
		t.Fatal("no assistant message in rollback response (v2 read-path bug?)")
	}
	// The question part must have question_status="ok" and
	// name=<answers JSON>. The QuestionTable.vue reads
	// these two fields to render "已回答" + selection
	// highlights, so losing them on the wire = user sees
	// "等待回答" + no selection after undo.
	var qPart *MessagePart
	for i := range asst.Parts {
		if asst.Parts[i].Kind == "question" {
			qPart = &asst.Parts[i]
			break
		}
	}
	if qPart == nil {
		t.Fatal("no question part in assistant's parts on rollback wire")
	}
	if qPart.QuestionStatus != "ok" {
		t.Errorf("question part question_status = %q, want ok", qPart.QuestionStatus)
	}
	if qPart.Name != answersJSON {
		t.Errorf("question part name = %q, want %q", qPart.Name, answersJSON)
	}

	// Step 2: simulate the frontend — POST the
	// deleted_messages back to /rollback/undo exactly as
	// the frontend would.
	undoBody, _ := json.Marshal(map[string]any{
		"messages": rollbackResp.DeletedMsgs,
	})
	req2 := httptest.NewRequest("POST",
		"/api/v1/sessions/"+store.CurrentConversationID()+"/rollback/undo",
		bytes.NewReader(undoBody))
	req2.Header.Set("Content-Type", "application/json")
	w2 := httptest.NewRecorder()
	s.engine.ServeHTTP(w2, req2)
	if w2.Code != 200 {
		t.Fatalf("undo: status = %d, want 200; body=%s", w2.Code, w2.Body.String())
	}

	// Step 3: read the DB back, confirm the question part's
	// question_status and name survived.
	msgs, metas, _, _, _ := store.GetChatMessagesWithMetaPage(store.CurrentConversationID(), 0, 0)
	if len(msgs) != 3 {
		t.Fatalf("after undo: want 3 messages, got %d", len(msgs))
	}
	var asstMeta string
	for i, m := range msgs {
		if m.Role == "assistant" {
			asstMeta = metas[i]
			break
		}
	}
	if asstMeta == "" {
		t.Fatal("assistant message missing from DB after undo (decodeChatMessages still dropping v2 rows?)")
	}
	var meta map[string]string
	if err := json.Unmarshal([]byte(asstMeta), &meta); err != nil {
		t.Fatalf("parse meta: %v", err)
	}
	if meta["parts"] == "" {
		t.Fatal("meta.parts empty after undo — question part lost")
	}
	var partsFromMeta []agent.MessagePart
	if err := json.Unmarshal([]byte(meta["parts"]), &partsFromMeta); err != nil {
		t.Fatalf("parse meta.parts: %v", err)
	}
	var restoredQ *agent.MessagePart
	for i := range partsFromMeta {
		if partsFromMeta[i].Kind == "question" {
			restoredQ = &partsFromMeta[i]
			break
		}
	}
	if restoredQ == nil {
		t.Fatal("question part missing from DB meta.parts")
	}
	if restoredQ.QuestionStatus != "ok" {
		t.Errorf("DB question_status = %q, want ok", restoredQ.QuestionStatus)
	}
	if restoredQ.Name != answersJSON {
		t.Errorf("DB question name = %q, want %q", restoredQ.Name, answersJSON)
	}
}
