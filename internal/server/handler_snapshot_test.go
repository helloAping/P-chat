// P0-1 snapshot recovery endpoint tests. The endpoint
// returns the delta of assistant messages with seq > N so
// the frontend can rebuild trailing state after a dropped
// SSE stream.
package server

import (
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/p-chat/pchat/internal/llm"
)

// TestSnapshotRecovery_AfterSeqFilter verifies that the
// endpoint honours ?after_seq=N and only returns
// assistant messages with seq > N, oldest first.
func TestSnapshotRecovery_AfterSeqFilter(t *testing.T) {
	s, _ := newTestServer(t)
	store := s.store
	convID, err := store.NewConversation()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrent(convID); err != nil {
		t.Fatal(err)
	}

	// Insert 4 messages in interleaved order. Only
	// assistant rows should appear in the snapshot.
	for i := 0; i < 4; i++ {
		var role string
		if i%2 == 0 {
			role = "assistant"
		} else {
			role = "user"
		}
		store.AddMessage(llm.Message{Role: role, Content: "msg-" + string(rune('a'+i))})
	}
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}

	// Get the current seq of every row so we can pick a
	// meaningful "after_seq" cursor. The test is robust
	// to insertion order / seq numbering because we read
	// the rows back first.
	_, _, _, _, seqs, _, _ := store.GetChatMessagesWithMetaPage(convID, 0, 10)
	// seqs are in oldest-first order after the helper reverses.
	// We want "after seq of the second assistant row" — but
	// we don't know which index is assistant. So instead
	// just request after_seq=0 and verify the response
	// contains only the assistant rows we inserted.
	t.Logf("seqs in conversation: %v", seqs)

	// after_seq=0 → should return all assistant rows.
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET",
		"/api/v1/sessions/"+convID+"/snapshot?after_seq=0",
		nil)
	s.engine.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Messages []MessageResponse `json:"messages"`
		NextSeq  int64             `json:"next_seq"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	// We inserted 2 assistant rows (i=0, 2).
	if len(body.Messages) != 2 {
		t.Fatalf("want 2 assistant messages, got %d (body=%s)", len(body.Messages), w.Body.String())
	}
	for _, m := range body.Messages {
		if m.Role != "assistant" {
			t.Errorf("non-assistant row leaked: role=%q", m.Role)
		}
	}
	if body.NextSeq <= 0 {
		t.Errorf("next_seq = %d, want > 0 (highest returned seq)", body.NextSeq)
	}
}

// TestSnapshotRecovery_AfterSeqSkipsEarlier verifies that
// the cursor is honoured strictly: rows with seq <= N
// must NOT appear in the response.
func TestSnapshotRecovery_AfterSeqSkipsEarlier(t *testing.T) {
	s, _ := newTestServer(t)
	store := s.store
	convID, err := store.NewConversation()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrent(convID); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 3; i++ {
		store.AddMessage(llm.Message{Role: "assistant", Content: "a"})
	}
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}

	// Pull the seqs and pick the middle one as the cursor.
	_, _, _, _, seqs, _, _ := store.GetChatMessagesWithMetaPage(convID, 0, 10)
	if len(seqs) < 3 {
		t.Fatalf("expected 3 seqs, got %d", len(seqs))
	}
	// seqs is oldest-first, so seqs[1] is the middle row.
	midSeq := seqs[1]

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET",
		"/api/v1/sessions/"+convID+"/snapshot?after_seq="+strconv.FormatInt(midSeq, 10),
		nil)
	s.engine.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Messages []MessageResponse `json:"messages"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	// Should return the rows strictly newer than midSeq.
	if len(body.Messages) != 1 {
		t.Fatalf("want 1 message (the one after midSeq), got %d", len(body.Messages))
	}
	if body.Messages[0].Seq <= midSeq {
		t.Errorf("returned seq=%d, want > %d", body.Messages[0].Seq, midSeq)
	}
}

// TestSnapshotRecovery_EmptySession verifies that a session
// with no assistant messages returns an empty list (not
// null, not 500).
func TestSnapshotRecovery_EmptySession(t *testing.T) {
	s, _ := newTestServer(t)
	store := s.store
	convID, err := store.NewConversation()
	if err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET",
		"/api/v1/sessions/"+convID+"/snapshot?after_seq=0",
		nil)
	s.engine.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Messages []MessageResponse `json:"messages"`
		NextSeq  int64             `json:"next_seq"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Messages == nil {
		t.Error("messages should be an empty array, not null")
	}
	if len(body.Messages) != 0 {
		t.Errorf("want 0 messages, got %d", len(body.Messages))
	}
	if body.NextSeq != 0 {
		t.Errorf("next_seq = %d, want 0 for empty result", body.NextSeq)
	}
}
