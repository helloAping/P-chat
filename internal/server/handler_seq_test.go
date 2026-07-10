package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/p-chat/pchat/internal/llm"
)

// TestListMessages_SeqCursor covers the new seq-based
// pagination cursor (migration 8). The client should be able
// to send `?before_seq=N` and get the rows with seq < N.
// The response includes both `oldest_id` (legacy) and
// `oldest_seq` (preferred).
func TestListMessages_SeqCursor(t *testing.T) {
	s, _ := newTestServer(t)
	store := s.store
	if _, err := store.NewConversation(); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrent(store.CurrentConversationID()); err != nil {
		t.Fatal(err)
	}

	// Insert 5 messages.
	for i := 0; i < 5; i++ {
		store.AddMessage(llm.Message{Role: "user", Content: "msg"})
	}
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}

	// Page 1: no cursor, limit=2. Should return seq 4, 5
	// (the two newest). The response must expose both
	// `oldest_id` and `oldest_seq`.
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET",
		"/api/v1/sessions/"+store.CurrentConversationID()+"/messages?limit=2",
		nil)
	s.engine.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("page 1: status = %d, body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Messages  []MessageResponse `json:"messages"`
		HasMore   bool              `json:"has_more"`
		OldestID  int64             `json:"oldest_id"`
		OldestSeq int64             `json:"oldest_seq"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Messages) != 2 {
		t.Fatalf("page 1: want 2 messages, got %d", len(body.Messages))
	}
	if body.OldestID <= 0 {
		t.Errorf("page 1: oldest_id = %d, want > 0", body.OldestID)
	}
	if body.OldestSeq != 4 {
		t.Errorf("page 1: oldest_seq = %d, want 4 (per-conversation counter; "+
			"5 messages → seqs 1..5, page 1 contains 4 and 5)", body.OldestSeq)
	}
	if !body.HasMore {
		t.Errorf("page 1: has_more should be true (seq 1, 2, 3 still exist)")
	}

	// Every message response must carry a non-zero seq so
	// the client can use it for stable :key + cursor.
	for i, m := range body.Messages {
		if m.Seq == 0 {
			t.Errorf("messages[%d].seq = 0, want > 0 (the new MessageResponse contract "+
				"requires seq to be populated so the client can use it for "+
				"Vue :key and pagination cursor)", i)
		}
	}

	// Page 2: ?before_seq=4, limit=10. Should return seq
	// 1, 2, 3 (3 messages, not 4 like the buggy pre-fix
	// ?before_id=4 path that returned the overlap). This
	// is the regression lock for the seq cursor: the page
	// must not overlap.
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET",
		"/api/v1/sessions/"+store.CurrentConversationID()+"/messages?limit=10&before_seq=4",
		nil)
	s.engine.ServeHTTP(w2, req2)
	if w2.Code != 200 {
		t.Fatalf("page 2: status = %d, body=%s", w2.Code, w2.Body.String())
	}
	var body2 struct {
		Messages  []MessageResponse `json:"messages"`
		HasMore   bool              `json:"has_more"`
		OldestID  int64             `json:"oldest_id"`
		OldestSeq int64             `json:"oldest_seq"`
	}
	_ = json.NewDecoder(w2.Body).Decode(&body2)
	if len(body2.Messages) != 3 {
		t.Errorf("page 2 (before_seq=4): want 3 messages, got %d", len(body2.Messages))
	}
	if body2.HasMore {
		t.Errorf("page 2: has_more should be false (seq 1 is the oldest)")
	}
	if body2.OldestSeq != 1 {
		t.Errorf("page 2: oldest_seq = %d, want 1", body2.OldestSeq)
	}
}

// TestListMessages_SeqCursorSurvivesRollback is the
// architectural regression lock: the whole point of seq is
// that it survives rollback+undo (a restored message has
// the same seq it had before). The id-based cursor would
// become stale (different rows) after such a round-trip.
// We don't run a full undo here — we just verify that the
// seq column on a freshly-loaded page matches the seq
// the client needs to pass back.
func TestListMessages_SeqCursorStableForRoundTrip(t *testing.T) {
	s, _ := newTestServer(t)
	store := s.store
	if _, err := store.NewConversation(); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrent(store.CurrentConversationID()); err != nil {
		t.Fatal(err)
	}
	store.AddMessage(llm.Message{Role: "user", Content: "msg"})
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET",
		"/api/v1/sessions/"+store.CurrentConversationID()+"/messages",
		nil)
	s.engine.ServeHTTP(w, req)
	var body struct {
		Messages  []MessageResponse `json:"messages"`
		OldestSeq int64             `json:"oldest_seq"`
	}
	_ = json.NewDecoder(w.Body).Decode(&body)
	if body.OldestSeq != 1 {
		t.Errorf("oldest_seq = %d, want 1", body.OldestSeq)
	}
	if len(body.Messages) != 1 || body.Messages[0].Seq != 1 {
		t.Errorf("messages[0].seq = %d, want 1 (the first message in a "+
			"fresh conversation has seq 1, not 0 or random)", body.Messages[0].Seq)
	}
}
