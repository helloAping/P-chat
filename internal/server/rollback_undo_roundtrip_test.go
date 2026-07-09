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

// TestRollbackUndo_RoundTrip simulates the full frontend flow:
//   (1) user sends a few messages
//   (2) user clicks rollback on a middle message — server
//       returns deleted_messages in MessageResponse shape
//       (with parts decoded, not raw metadata)
//   (3) frontend stores deleted_messages in rollbackUndo
//   (4) user clicks 撤销 — frontend POSTs the same messages
//       back to /rollback/undo as the request body
//   (5) server's UndoRollback handler must accept this shape
//       AND the messages must be re-inserted with their
//       metadata (parts) preserved
//
// The bug we're catching: pre-fix the /rollback/undo handler
// expected `[]memory.Message` but the frontend sends
// `[]MessageResponse` (after the rollback wire-format fix).
// Go's json.Unmarshal is lenient (extra fields are dropped,
// missing fields get zero values), so the request doesn't
// 400 — but `metadata` ends up empty, which means
// `RestoreMessages` re-inserts the rows WITHOUT the parts
// snapshot, so on the next reload the chat bubbles show
// only plain `content` (no thinking / tool / sub-agent).
//
// The 200 OK is misleading: the row exists but is silently
// degraded. The frontend's in-memory splice is fine, but on
// any reload the parts are gone. This test pins the
// contract: the round-trip through /rollback/undo must
// preserve the `parts` data.
func TestRollbackUndo_RoundTrip(t *testing.T) {
	s, _ := newTestServer(t)
	store := s.store

	if _, err := store.NewConversation(); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrent(store.CurrentConversationID()); err != nil {
		t.Fatal(err)
	}

	// Setup: user msg + assistant msg with rich parts.
	store.AddMessage(llm.Message{Role: "user", Content: "hi"})
	partsBlob := []agent.MessagePart{
		{Kind: "text", Text: "hello back"},
		{Kind: "thinking", Text: "let me think"},
		{Kind: "tool", ToolID: "call_1", Name: "read_file", Args: `{"path":"x"}`, Status: "ok", Result: "data", Elapsed: "5ms"},
	}
	partsJSON, _ := json.Marshal(partsBlob)
	store.AddMessageWithMeta(llm.Message{Role: "assistant", Content: "hello back"}, map[string]string{
		"thinking": "let me think",
		"parts":    string(partsJSON),
	})
	store.AddMessage(llm.Message{Role: "user", Content: "second user msg"})
	store.AddMessageWithMeta(llm.Message{Role: "assistant", Content: "second reply"}, map[string]string{
		"parts": "[{\"kind\":\"text\",\"text\":\"second reply\"}]",
	})
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}

	// Get the assistant message's id for the rollback anchor.
	_, _, _, rowIDs := store.GetChatMessagesWithMetaPage(store.CurrentConversationID(), 0, 0)
	if len(rowIDs) < 4 {
		t.Fatalf("setup: want 4 rows, got %d", len(rowIDs))
	}
	// Rollback the second user msg (index 2, id=rowIDs[2]).
	rollbackID := rowIDs[2]

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
		DeletedCount   int               `json:"deleted_count"`
		DeletedMsgs    []MessageResponse `json:"deleted_messages"`
	}
	if err := json.NewDecoder(w.Body).Decode(&rollbackResp); err != nil {
		t.Fatalf("decode rollback: %v", err)
	}
	if len(rollbackResp.DeletedMsgs) == 0 {
		t.Fatal("no deleted messages returned")
	}

	// Find the second assistant message (the one with the
	// parts blob). Pre-fix this is a memory.Message with
	// raw metadata; post-fix it's a MessageResponse with
	// parts decoded.
	var asst *MessageResponse
	for i := range rollbackResp.DeletedMsgs {
		if rollbackResp.DeletedMsgs[i].Role == "assistant" {
			asst = &rollbackResp.DeletedMsgs[i]
			break
		}
	}
	if asst == nil {
		t.Fatal("no assistant in deleted_messages")
	}
	if len(asst.Parts) == 0 {
		t.Fatalf("assistant.parts is empty pre-undo — the rollback fix from earlier isn't taking effect")
	}

	// Step 2: simulate the frontend — store deleted_messages
	// as-is (no conversion), then POST them to /rollback/undo
	// exactly as the frontend would.
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
	t.Logf("undo response: %s", w2.Body.String())

	// Step 3: re-read the conversation from the DB and
	// check the assistant message's metadata still has the
	// parts blob. Pre-fix this fails because the server's
	// UndoRollback parses MessageResponse into memory.Message,
	// and the `metadata` field is empty (MessageResponse
	// doesn't carry it). The RestoreMessages insert then
	// writes metadata="" into the DB, losing the parts.
	// Post-fix the round-trip should be lossless.
	msgs, metas, _, ids := store.GetChatMessagesWithMetaPage(store.CurrentConversationID(), 0, 0)
	if len(msgs) != 4 {
		t.Fatalf("after undo: want 4 messages, got %d", len(msgs))
	}

	// Find the second assistant message (the one with
	// parts blob).
	var asstMeta string
	for i, m := range msgs {
		if m.Role == "assistant" && strings.Contains(metas[i], "let me think") {
			asstMeta = metas[i]
			break
		}
	}
	if asstMeta == "" {
		t.Fatal("after undo: assistant message metadata is empty — the parts blob was LOST during the undo round-trip")
	}
	// Sanity: the parts blob should still contain the
	// thinking + tool + text parts.
	//
	// The stored metadata is `{"parts": "<json string>"}` —
	// a double-encoded blob. The agent's snapshotStructural
	// writes the parts array as a JSON string under the
	// "parts" key; decodePartsFromMeta unwraps it on read.
	var meta map[string]string
	if err := json.Unmarshal([]byte(asstMeta), &meta); err != nil {
		t.Fatalf("parse meta envelope: %v", err)
	}
	if meta["parts"] == "" {
		t.Fatal("meta.parts is empty — parts lost during undo round-trip")
	}
	var partsFromMeta []agent.MessagePart
	if err := json.Unmarshal([]byte(meta["parts"]), &partsFromMeta); err != nil {
		t.Fatalf("parse meta.parts: %v", err)
	}
	wantKinds := []string{"text", "thinking", "tool"}
	if len(partsFromMeta) != len(wantKinds) {
		t.Fatalf("parts from meta = %d, want %d: %+v", len(partsFromMeta), len(wantKinds), partsFromMeta)
	}
	for i, want := range wantKinds {
		if partsFromMeta[i].Kind != want {
			t.Errorf("partsFromMeta[%d].Kind = %q, want %q", i, partsFromMeta[i].Kind, want)
		}
	}
	_ = ids // silence unused warning if we drop the rowid check
}
