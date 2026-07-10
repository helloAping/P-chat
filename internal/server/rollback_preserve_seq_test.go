package server

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/p-chat/pchat/internal/agent"
	"github.com/p-chat/pchat/internal/llm"
)

// TestRollbackUndo_PreservesSeqAndSubmitToLLM is the
// regression lock for the C5 fix. The previous undo path:
//
//  1. Hard-coded SubmitToLLM=1 on every restored row
//     (the wire payload didn't carry it), so an undone
//     thinking row (originally SubmitToLLM=0) was
//     re-inserted as a normal assistant row that the LLM
//     would see as fresh context and likely echo back.
//
//  2. Didn't carry the original seq (migration 8 added
//     it; before that RestoreMessages had no seq column
//     at all). The restored row would collide with the
//     current MAX(seq)+1, breaking the seq-based cursor.
//
// The fix is on both sides: DeleteMessagesFrom snapshots
// the row's seq + submit_to_llm, the rollback response
// includes them in the MessageResponse payload, the undo
// handler passes them through to RestoreMessages which
// writes them into the restored row.
func TestRollbackUndo_PreservesSeqAndSubmitToLLM(t *testing.T) {
	s, _ := newTestServer(t)
	store := s.store
	if _, err := store.NewConversation(); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrent(store.CurrentConversationID()); err != nil {
		t.Fatal(err)
	}

	// A user message + a thinking-flavored assistant
	// message. The assistant's SubmitToLLM is 0 because
	// the agent writes thinking rows with that flag —
	// the LLM context should NOT include the chain of
	// thought. After rollback+undo, the restored row
	// must still have SubmitToLLM=0.
	store.AddMessage(llm.Message{Role: "user", Content: "hi"})
	partsBlob := []agent.MessagePart{
		{Kind: "thinking", Text: "secret chain of thought"},
	}
	partsJSON, _ := json.Marshal(partsBlob)
	// Use AddChatMessageWithMeta (ChatMessage-shaped
	// constructor) so we can set SubmitToLLM explicitly.
	// AddMessageWithMeta takes the simpler llm.Message
	// (= openai.ChatCompletionMessage) which doesn't
	// have that field.
	store.AddChatMessageWithMeta(llm.ChatMessage{
		Role:        "assistant",
		Content:     "",
		SubmitToLLM: 0, // thinking rows are not submitted to the LLM
	}, map[string]string{
		"parts": string(partsJSON),
	})
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}

	// Snapshot the assistant row's id and seq so we can
	// verify they're preserved across the round-trip.
	_, _, _, rowIDs, seqs := store.GetChatMessagesWithMetaPage(store.CurrentConversationID(), 0, 0)
	if len(rowIDs) < 2 {
		t.Fatalf("setup: want 2 rows, got %d", len(rowIDs))
	}
	asstID := rowIDs[1]
	asstSeq := seqs[1]
	if asstSeq == 0 {
		t.Fatalf("setup: asst seq = 0; seq should be 2 for a "+
			"second message in a fresh conversation")
	}

	// Rollback to the assistant message — deletes it
	// and returns deleted_messages in wire shape.
	body, _ := json.Marshal(map[string]any{"before_id": asstID})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST",
		"/api/v1/sessions/"+store.CurrentConversationID()+"/rollback",
		bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.engine.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("rollback: status = %d, body=%s", w.Code, w.Body.String())
	}
	var rb struct {
		DeletedMsgs []MessageResponse `json:"deleted_messages"`
	}
	_ = json.NewDecoder(w.Body).Decode(&rb)
	if len(rb.DeletedMsgs) == 0 {
		t.Fatal("rollback returned no deleted_messages")
	}

	// The wire payload must include both seq and
	// submit_to_llm. Pre-fix these were absent.
	var wireAsst *MessageResponse
	for i := range rb.DeletedMsgs {
		if rb.DeletedMsgs[i].ID == asstID {
			wireAsst = &rb.DeletedMsgs[i]
			break
		}
	}
	if wireAsst == nil {
		t.Fatal("assistant not in deleted_messages")
	}
	if wireAsst.Seq != asstSeq {
		t.Errorf("rollback response: seq = %d, want %d "+
			"(must round-trip so undo preserves the seq)", wireAsst.Seq, asstSeq)
	}
	if wireAsst.SubmitToLLM != 0 {
		t.Errorf("rollback response: submit_to_llm = %d, want 0 "+
			"(thinking rows must not be re-submitted to the LLM "+
			"on undo — the chain-of-thought would leak)", wireAsst.SubmitToLLM)
	}

	// Undo the rollback.
	undoBody, _ := json.Marshal(map[string]any{"messages": rb.DeletedMsgs})
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("POST",
		"/api/v1/sessions/"+store.CurrentConversationID()+"/rollback/undo",
		bytes.NewReader(undoBody))
	req2.Header.Set("Content-Type", "application/json")
	s.engine.ServeHTTP(w2, req2)
	if w2.Code != 200 {
		t.Fatalf("undo: status = %d, body=%s", w2.Code, w2.Body.String())
	}

	// Re-read the conversation and check the assistant
	// row's seq + submit_to_llm survived the round-trip.
	msgs, metas, _, ids, seqsAfter := store.GetChatMessagesWithMetaPage(store.CurrentConversationID(), 0, 0)
	if len(msgs) != 2 {
		t.Fatalf("after undo: want 2 msgs, got %d", len(msgs))
	}
	asstIdx := -1
	for i, m := range msgs {
		if m.Role == "assistant" {
			asstIdx = i
			break
		}
	}
	if asstIdx < 0 {
		t.Fatal("no assistant after undo")
	}
	if ids[asstIdx] != asstID {
		t.Errorf("after undo: assistant id = %d, want %d "+
			"(original id should be preserved)", ids[asstIdx], asstID)
	}
	if seqsAfter[asstIdx] != asstSeq {
		t.Errorf("after undo: assistant seq = %d, want %d "+
			"(the seq-based cursor relies on this)", seqsAfter[asstIdx], asstSeq)
	}
	if msgs[asstIdx].SubmitToLLM != 0 {
		t.Errorf("after undo: assistant submit_to_llm = %d, want 0 "+
			"(hard-coded 1 on undo would leak the thinking chain "+
			"back into the LLM context)", msgs[asstIdx].SubmitToLLM)
	}
	// Parts (the thinking block) must also survive.
	if metas[asstIdx] == "" || !contains(metas[asstIdx], "secret chain of thought") {
		t.Errorf("after undo: assistant metadata lost the thinking "+
			"block: %q", metas[asstIdx])
	}
}

// contains is a tiny helper so the test doesn't import
// strings just for one Contains check.
func contains(haystack, needle string) bool {
	if len(needle) > len(haystack) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
