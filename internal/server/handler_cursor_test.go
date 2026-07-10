package server

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/p-chat/pchat/internal/llm"
)

// TestListMessages_OldestIdCursorIsMin pins the pagination cursor
// contract: the `oldest_id` field in the ListMessages response is
// the SMALLEST id in the returned page (the oldest / most-back row),
// not the largest.
//
// This is the regression test for the 2026-07-10 "messages
// duplicate 3-4× on restart" bug. The pre-fix code computed
// `oldestID = rowIDs[len-1]` which is the max id of the page
// (the newest row). The frontend then sent `?before_id=<max>`
// as the next-page cursor, and because the SQL predicate is
// `id < before_id` the next call returned every row in the
// page *except* the very newest one — overlapping with the
// previous page by all-but-one row. Combined with
// `HasOlderMessages(id, <max>)` returning `true` (because
// `id < <max>` is always true when the page is non-empty),
// this produced an infinite scroll that never reached the
// start of the conversation AND duplicated the same
// messages on every page request. The frontend's
// `loadMoreMessages` did `[...r.messages, ...existing]`
// without dedup, so the visible effect was: open a session,
// see N messages, scroll to the top → see N + (N-1) messages,
// scroll again → N + (N-1) + (N-2) messages, etc.
//
// Fix: server uses `rowIDs[0]` (min id) as the cursor; client
// dedups on receipt (separate fix in the chat store).
func TestListMessages_OldestIdCursorIsMin(t *testing.T) {
	s, _ := newTestServer(t)
	store := s.store
	if _, err := store.NewConversation(); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrent(store.CurrentConversationID()); err != nil {
		t.Fatal(err)
	}

	// Insert 5 messages. With AUTOINCREMENT the row ids
	// will be 1..5 in insertion order (no other writers
	// racing in this test).
	for i := 0; i < 5; i++ {
		store.AddMessage(llm.Message{Role: "user", Content: "msg"})
	}
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}

	// Page 1: limit=2, no cursor. Server returns the 2
	// newest messages (ids 4, 5 in oldest-first order).
	// The reported `oldest_id` must be the smallest id in
	// that page — 4 — so the next call's
	// `?before_id=4` selects ids 1, 2, 3 correctly.
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET",
		"/api/v1/sessions/"+store.CurrentConversationID()+"/messages?limit=2",
		nil)
	s.engine.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("page 1: status = %d, body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Messages []MessageResponse `json:"messages"`
		HasMore  bool              `json:"has_more"`
		OldestID int64             `json:"oldest_id"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Messages) != 2 {
		t.Fatalf("page 1: want 2 messages, got %d", len(body.Messages))
	}
	if body.OldestID != 4 {
		t.Errorf("page 1: oldest_id = %d, want 4 (min of page); "+
			"using max_id here caused the infinite-scroll "+
			"duplicate-on-scroll bug", body.OldestID)
	}
	if !body.HasMore {
		t.Errorf("page 1: has_more should be true (rows 1, 2, 3 still exist)")
	}

	// Page 2: ?before_id=4 should return rows 1..3 (id 3, 2 in
	// oldest-first order). Pre-fix this would have returned
	// rows 1..4 (overlap of 1 row) because the cursor was
	// `oldest_id=5` (max) and `id < 5` selects {1,2,3,4}.
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET",
		"/api/v1/sessions/"+store.CurrentConversationID()+"/messages?limit=10&before_id="+
			itoa(body.OldestID), nil)
	s.engine.ServeHTTP(w2, req2)
	if w2.Code != 200 {
		t.Fatalf("page 2: status = %d, body=%s", w2.Code, w2.Body.String())
	}
	var body2 struct {
		Messages []MessageResponse `json:"messages"`
		HasMore  bool              `json:"has_more"`
		OldestID int64             `json:"oldest_id"`
	}
	_ = json.NewDecoder(w2.Body).Decode(&body2)
	if len(body2.Messages) != 3 {
		t.Errorf("page 2: want 3 messages, got %d "+
			"(pre-fix the cursor overlap returned 4, "+
			"re-introducing the duplicate bug)", len(body2.Messages))
	}
	if body2.HasMore {
		t.Errorf("page 2: has_more should be false (row 1 is the "+
			"oldest, nothing older exists)")
	}
	if body2.OldestID != 1 {
		t.Errorf("page 2: oldest_id = %d, want 1 (min of page)", body2.OldestID)
	}
}

// TestListMessages_NoHasMoreWhenAtHead covers the small-session
// case the user reported: a session with only 2 messages
// (id 367, 368 in the bug report) must report `has_more=false`
// on the first page, not `true`. The pre-fix code returned
// `true` because `oldestID=368` (max id) and the EXISTS
// query `id < 368` was satisfied by id 367. Post-fix
// `oldestID=367` (min id) and `id < 367` is empty → false.
func TestListMessages_NoHasMoreWhenAtHead(t *testing.T) {
	s, _ := newTestServer(t)
	store := s.store
	if _, err := store.NewConversation(); err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrent(store.CurrentConversationID()); err != nil {
		t.Fatal(err)
	}
	store.AddMessage(llm.Message{Role: "user", Content: "看看D:\\develop\\project"})
	store.AddMessage(llm.Message{Role: "assistant", Content: "目录内容: ..."})
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET",
		"/api/v1/sessions/"+store.CurrentConversationID()+"/messages?limit=50",
		nil)
	s.engine.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	var body struct {
		Messages []MessageResponse `json:"messages"`
		HasMore  bool              `json:"has_more"`
		OldestID int64             `json:"oldest_id"`
	}
	_ = json.NewDecoder(w.Body).Decode(&body)
	if len(body.Messages) != 2 {
		t.Fatalf("want 2 messages, got %d", len(body.Messages))
	}
	if body.HasMore {
		t.Errorf("has_more = true with 2 msgs and no older rows; "+
			"the user-visible symptom was length=3 after one "+
			"scroll because the next page returned the older "+
			"row and was appended un-deduplicated")
	}
	if body.OldestID != 1 {
		t.Errorf("oldest_id = %d, want 1 (min of page)", body.OldestID)
	}
}

// itoa is a tiny helper that avoids pulling in strconv for one call.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
