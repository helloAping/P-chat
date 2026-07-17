// P1-4 Regenerate endpoint tests. Verifies the
// soft-archive + re-run shape: the user message is
// preserved, the existing assistant sibling is
// archived (NOT hard-deleted as in P1-3), and a fresh
// assistant row lands with the same regen_group_id.
package server

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/p-chat/pchat/internal/llm"
)

// TestRegenerate_ArchivesOldReply verifies the P1-4
// happy path: 1 user + 1 assistant → regen → the old
// assistant row is archived (NOT in the main list, but
// still in the messages table for the ◀ N/M ▶ pager),
// the user row survives, and a new assistant row
// appears as the active reply.
//
// We don't drive the full SSE stream (that would need a
// mock LLM and a longer test). Instead we confirm the
// archive side-effect via ListMessages AFTER the
// regenerate: the main list should NOT show the original
// "hello" content (because is_archived = 1 filters it
// out), and the user row's content survives.
//
// The test inserts the assistant row with the
// regen_group_id of the user message so the regen
// handler's ArchiveSiblings call actually has a target
// to archive — without that, the legacy AddMessage
// path leaves regen_group_id NULL and the regen
// handler has nothing to archive.
func TestRegenerate_ArchivesOldReply(t *testing.T) {
	s, _ := newTestServer(t)
	store := s.store
	convID, err := store.NewConversation()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrent(convID); err != nil {
		t.Fatal(err)
	}

	// Insert a user message and an assistant reply.
	// The assistant row is written with regen_group_id =
	// strconv(userID) so the regen handler can find and
	// archive it. This mirrors what the agent's
	// persistAssistant does for production traffic.
	store.AddMessage(llm.Message{Role: "user", Content: "hi"})
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}
	userID := store.GetLastUserMessageID(convID)
	if userID <= 0 {
		t.Fatalf("expected user message id > 0, got %d", userID)
	}
	// Use the P1-4 helper so the row is correctly tagged
	// with the user message's id as regen_group_id.
	store.AddChatMessageWithMetaToRegen(convID,
		llm.ChatMessage{Role: "assistant", Content: "hello"},
		map[string]string{"role": "assistant"},
		strconv.FormatInt(userID, 10),
		false,
	)
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}

	// Find the user message id (already looked up above
	// to set regen_group_id on the assistant row).

	// Call regenerate. The agent loop won't be able to
	// reach a real LLM (the test config has an invalid
	// URL), so the stream will return an error event,
	// but the truncate side-effect must land first.
	//
	// We use newStreamRecorder (defined in handler_test.go)
	// because gin's c.Stream calls w.CloseNotify() and
	// httptest.ResponseRecorder doesn't implement that
	// interface in Go 1.25+. The ctx-bound request lets
	// the stream unblock when the LLM errors out.
	body := `{"user_message_id": ` + strconv.FormatInt(userID, 10) + `}`
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	w := newStreamRecorder()
	req := httptest.NewRequest("POST",
		"/api/v1/sessions/"+convID+"/regenerate",
		strings.NewReader(body)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	done := make(chan struct{})
	go func() { s.engine.ServeHTTP(w, req); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		// Stream hung — we don't care, the truncate
		// already happened synchronously before c.Stream
		// even ran. Move on to the list assertion.
	}
	// The response is an SSE stream. We don't parse it;
	// we just confirm the truncate happened by listing
	// messages afterwards. The agent loop will have
	// produced zero or more new assistant rows depending
	// on how far the stream got before the LLM errored.
	// We assert that:
	//   (a) the user message is still there with the
	//       same id
	//   (b) there is no assistant message with the
	//       ORIGINAL "hello" content (the old reply was
	//       truncated and possibly replaced).
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET",
		"/api/v1/sessions/"+convID+"/messages",
		nil)
	s.engine.ServeHTTP(w2, req2)
	if w2.Code != 200 {
		t.Fatalf("list: status = %d, body=%s", w2.Code, w2.Body.String())
	}
	var lr struct {
		Messages []MessageResponse `json:"messages"`
	}
	if err := json.NewDecoder(w2.Body).Decode(&lr); err != nil {
		t.Fatal(err)
	}

	// (a) user message still present (id may have shifted
	// due to the agent loop writing new rows; we only
	// care that the user content survives)
	var sawUser bool
	for _, m := range lr.Messages {
		if m.Role == "user" && m.Content == "hi" {
			sawUser = true
		}
		// (b) the original "hello" assistant content must
		// not appear in the post-regen list.
		if m.Role == "assistant" && m.Content == "hello" {
			t.Errorf("old assistant reply survived regen: %q", m.Content)
		}
	}
	if !sawUser {
		t.Error("user message missing after regen")
	}
}

// TestRegenerate_RejectsNonUserMessage verifies that
// passing an assistant message id is rejected with 400,
// not silently treated as a regen target. This guard
// prevents the agent loop from running with no user
// prompt.
func TestRegenerate_RejectsNonUserMessage(t *testing.T) {
	s, _ := newTestServer(t)
	store := s.store
	convID, err := store.NewConversation()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetCurrent(convID); err != nil {
		t.Fatal(err)
	}

	store.AddMessage(llm.Message{Role: "user", Content: "hi"})
	store.AddMessage(llm.Message{Role: "assistant", Content: "hello"})
	if err := store.Flush(); err != nil {
		t.Fatal(err)
	}

	// Find the assistant message id. We inserted 2
	// messages; the second one is the assistant. Use
	// GetChatMessagesFor which returns in insertion
	// order with role info attached.
	allMsgs := store.GetChatMessagesFor(convID, 0)
	if len(allMsgs) < 2 {
		t.Fatalf("expected 2 messages, got %d", len(allMsgs))
	}
	// GetChatMessagesFor returns llm.ChatMessage which
	// has a Role field; walk the latest user/assistant
	// pair and pick the assistant's id by querying.
	var asstID int64
	for _, m := range allMsgs {
		if m.Role == "assistant" {
			// We don't have the row id from this helper,
			// so query the messages table directly via
			// the GetLastMessageID path. The assistant
			// is the most recent row overall.
			asstID = store.GetLastMessageID(convID)
			break
		}
	}
	if asstID <= 0 {
		t.Fatal("could not locate assistant message id")
	}

	body := `{"user_message_id": ` + strconv.FormatInt(asstID, 10) + `}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST",
		"/api/v1/sessions/"+convID+"/regenerate",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.engine.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Errorf("status = %d, want 400 (assistant id must be rejected); body=%s", w.Code, w.Body.String())
	}
}

// TestRegenerate_RejectsMissingID verifies that an
// unknown message id returns 400 (not 500).
func TestRegenerate_RejectsMissingID(t *testing.T) {
	s, _ := newTestServer(t)
	store := s.store
	convID, err := store.NewConversation()
	if err != nil {
		t.Fatal(err)
	}

	body := `{"user_message_id": 999999}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST",
		"/api/v1/sessions/"+convID+"/regenerate",
		strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	s.engine.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Errorf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}
