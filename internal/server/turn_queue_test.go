package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestTurnQueue_EnqueueListAndClaim(t *testing.T) {
	s, _ := newTestServer(t)
	sessionID, err := s.store.NewConversation()
	if err != nil {
		t.Fatalf("NewConversation: %v", err)
	}

	enqueued := doTurnQueueRequest(t, s, http.MethodPost, "/api/v1/sessions/"+sessionID+"/turn-queue",
		`{"message":"first queued","client_msg_id":1730000000001001,"style":"tech","attachments":[{"type":"text","text":"note","name":"a.txt","kind":"text","mime":"text/plain"}]}`)
	if enqueued.Code != http.StatusCreated {
		t.Fatalf("enqueue status = %d, want 201; body=%s", enqueued.Code, enqueued.Body.String())
	}
	var env TurnQueueItemEnvelope
	if err := json.NewDecoder(enqueued.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	if env.Item.ID == 0 || env.Item.Message != "first queued" || env.Item.AttachmentCount != 1 {
		t.Fatalf("unexpected enqueue response: %+v", env.Item)
	}
	if env.Item.Payload != nil {
		t.Fatalf("enqueue response should not echo payload: %+v", env.Item.Payload)
	}

	listed := doTurnQueueRequest(t, s, http.MethodGet, "/api/v1/sessions/"+sessionID+"/turn-queue", "")
	if listed.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200; body=%s", listed.Code, listed.Body.String())
	}
	var list TurnQueueListResponse
	if err := json.NewDecoder(listed.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Items[0].ID != env.Item.ID || list.Items[0].Payload != nil {
		t.Fatalf("unexpected list response: %+v", list.Items)
	}

	claimed := doTurnQueueRequest(t, s, http.MethodPost, "/api/v1/sessions/"+sessionID+"/turn-queue/claim", "")
	if claimed.Code != http.StatusOK {
		t.Fatalf("claim status = %d, want 200; body=%s", claimed.Code, claimed.Body.String())
	}
	var claimedEnv TurnQueueItemEnvelope
	if err := json.NewDecoder(claimed.Body).Decode(&claimedEnv); err != nil {
		t.Fatal(err)
	}
	if claimedEnv.Item.Status != "running" || claimedEnv.Item.Payload == nil {
		t.Fatalf("claim should return a running item with payload: %+v", claimedEnv.Item)
	}
	if claimedEnv.Item.Payload.Message != "first queued" || claimedEnv.Item.Payload.ClientMsgID != 1730000000001001 {
		t.Fatalf("unexpected claimed payload: %+v", claimedEnv.Item.Payload)
	}
}

func TestTurnQueue_ClaimRejectsBusySession(t *testing.T) {
	s, _ := newTestServer(t)
	sessionID, err := s.store.NewConversation()
	if err != nil {
		t.Fatalf("NewConversation: %v", err)
	}
	_ = doTurnQueueRequest(t, s, http.MethodPost, "/api/v1/sessions/"+sessionID+"/turn-queue",
		`{"message":"queued","client_msg_id":1730000000002001}`)

	s.handler.sessionLocks.Store(sessionID, struct{}{})
	t.Cleanup(func() { s.handler.sessionLocks.Delete(sessionID) })

	claimed := doTurnQueueRequest(t, s, http.MethodPost, "/api/v1/sessions/"+sessionID+"/turn-queue/claim", "")
	if claimed.Code != http.StatusConflict {
		t.Fatalf("claim status = %d, want 409; body=%s", claimed.Code, claimed.Body.String())
	}

	items, err := s.store.ListTurnQueueItems(sessionID)
	if err != nil {
		t.Fatalf("ListTurnQueueItems: %v", err)
	}
	if len(items) != 1 || items[0].Status != "queued" {
		t.Fatalf("busy claim should not mutate queue: %+v", items)
	}
}

func TestTurnQueue_CompleteFailRetryAndDelete(t *testing.T) {
	s, _ := newTestServer(t)
	sessionID, err := s.store.NewConversation()
	if err != nil {
		t.Fatalf("NewConversation: %v", err)
	}
	_ = doTurnQueueRequest(t, s, http.MethodPost, "/api/v1/sessions/"+sessionID+"/turn-queue",
		`{"message":"queued","client_msg_id":1730000000003001}`)

	claimed := doTurnQueueRequest(t, s, http.MethodPost, "/api/v1/sessions/"+sessionID+"/turn-queue/claim", "")
	var claimedEnv TurnQueueItemEnvelope
	if err := json.NewDecoder(claimed.Body).Decode(&claimedEnv); err != nil {
		t.Fatal(err)
	}

	failed := doTurnQueueRequest(t, s, http.MethodPost,
		"/api/v1/sessions/"+sessionID+"/turn-queue/"+strconvInt(claimedEnv.Item.ID)+"/fail",
		`{"error":"network down"}`)
	if failed.Code != http.StatusOK {
		t.Fatalf("fail status = %d, want 200; body=%s", failed.Code, failed.Body.String())
	}
	var failedEnv TurnQueueItemEnvelope
	if err := json.NewDecoder(failed.Body).Decode(&failedEnv); err != nil {
		t.Fatal(err)
	}
	if failedEnv.Item.Status != "failed" || failedEnv.Item.Error != "network down" {
		t.Fatalf("unexpected failed response: %+v", failedEnv.Item)
	}

	retried := doTurnQueueRequest(t, s, http.MethodPost,
		"/api/v1/sessions/"+sessionID+"/turn-queue/"+strconvInt(claimedEnv.Item.ID)+"/retry", "")
	if retried.Code != http.StatusOK {
		t.Fatalf("retry status = %d, want 200; body=%s", retried.Code, retried.Body.String())
	}
	var retriedEnv TurnQueueItemEnvelope
	if err := json.NewDecoder(retried.Body).Decode(&retriedEnv); err != nil {
		t.Fatal(err)
	}
	if retriedEnv.Item.Status != "queued" || retriedEnv.Item.Error != "" {
		t.Fatalf("unexpected retry response: %+v", retriedEnv.Item)
	}

	deleted := doTurnQueueRequest(t, s, http.MethodDelete,
		"/api/v1/sessions/"+sessionID+"/turn-queue/"+strconvInt(claimedEnv.Item.ID), "")
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want 200; body=%s", deleted.Code, deleted.Body.String())
	}
	listed := doTurnQueueRequest(t, s, http.MethodGet, "/api/v1/sessions/"+sessionID+"/turn-queue", "")
	var list TurnQueueListResponse
	if err := json.NewDecoder(listed.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 0 {
		t.Fatalf("deleted item should be hidden, got %+v", list.Items)
	}
}

func TestTurnQueue_CompleteHidesItem(t *testing.T) {
	s, _ := newTestServer(t)
	sessionID, err := s.store.NewConversation()
	if err != nil {
		t.Fatalf("NewConversation: %v", err)
	}
	_ = doTurnQueueRequest(t, s, http.MethodPost, "/api/v1/sessions/"+sessionID+"/turn-queue",
		`{"message":"queued","client_msg_id":1730000000003501}`)

	claimed := doTurnQueueRequest(t, s, http.MethodPost, "/api/v1/sessions/"+sessionID+"/turn-queue/claim", "")
	var claimedEnv TurnQueueItemEnvelope
	if err := json.NewDecoder(claimed.Body).Decode(&claimedEnv); err != nil {
		t.Fatal(err)
	}

	completed := doTurnQueueRequest(t, s, http.MethodPost,
		"/api/v1/sessions/"+sessionID+"/turn-queue/"+strconvInt(claimedEnv.Item.ID)+"/complete", "")
	if completed.Code != http.StatusOK {
		t.Fatalf("complete status = %d, want 200; body=%s", completed.Code, completed.Body.String())
	}

	listed := doTurnQueueRequest(t, s, http.MethodGet, "/api/v1/sessions/"+sessionID+"/turn-queue", "")
	var list TurnQueueListResponse
	if err := json.NewDecoder(listed.Body).Decode(&list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 0 {
		t.Fatalf("completed item should be hidden, got %+v", list.Items)
	}
}

func TestTurnQueue_FailedHeadBlocksClaim(t *testing.T) {
	s, _ := newTestServer(t)
	sessionID, err := s.store.NewConversation()
	if err != nil {
		t.Fatalf("NewConversation: %v", err)
	}
	_ = doTurnQueueRequest(t, s, http.MethodPost, "/api/v1/sessions/"+sessionID+"/turn-queue",
		`{"message":"one","client_msg_id":1730000000003601}`)
	_ = doTurnQueueRequest(t, s, http.MethodPost, "/api/v1/sessions/"+sessionID+"/turn-queue",
		`{"message":"two","client_msg_id":1730000000003602}`)

	claimed := doTurnQueueRequest(t, s, http.MethodPost, "/api/v1/sessions/"+sessionID+"/turn-queue/claim", "")
	var claimedEnv TurnQueueItemEnvelope
	if err := json.NewDecoder(claimed.Body).Decode(&claimedEnv); err != nil {
		t.Fatal(err)
	}
	_ = doTurnQueueRequest(t, s, http.MethodPost,
		"/api/v1/sessions/"+sessionID+"/turn-queue/"+strconvInt(claimedEnv.Item.ID)+"/fail",
		`{"error":"network down"}`)

	blocked := doTurnQueueRequest(t, s, http.MethodPost, "/api/v1/sessions/"+sessionID+"/turn-queue/claim", "")
	if blocked.Code != http.StatusConflict {
		t.Fatalf("claim status = %d, want 409; body=%s", blocked.Code, blocked.Body.String())
	}
	if !strings.Contains(blocked.Body.String(), "blocked") {
		t.Fatalf("blocked response should explain queue state: %s", blocked.Body.String())
	}
}

func TestTurnQueue_RunningHeadBlocksClaim(t *testing.T) {
	s, _ := newTestServer(t)
	sessionID, err := s.store.NewConversation()
	if err != nil {
		t.Fatalf("NewConversation: %v", err)
	}
	_ = doTurnQueueRequest(t, s, http.MethodPost, "/api/v1/sessions/"+sessionID+"/turn-queue",
		`{"message":"one","client_msg_id":1730000000003701}`)
	_ = doTurnQueueRequest(t, s, http.MethodPost, "/api/v1/sessions/"+sessionID+"/turn-queue",
		`{"message":"two","client_msg_id":1730000000003702}`)

	claimed := doTurnQueueRequest(t, s, http.MethodPost, "/api/v1/sessions/"+sessionID+"/turn-queue/claim", "")
	if claimed.Code != http.StatusOK {
		t.Fatalf("claim status = %d, want 200; body=%s", claimed.Code, claimed.Body.String())
	}

	blocked := doTurnQueueRequest(t, s, http.MethodPost, "/api/v1/sessions/"+sessionID+"/turn-queue/claim", "")
	if blocked.Code != http.StatusConflict {
		t.Fatalf("claim status = %d, want 409; body=%s", blocked.Code, blocked.Body.String())
	}
	if !strings.Contains(blocked.Body.String(), "already running") {
		t.Fatalf("running response should explain queue state: %s", blocked.Body.String())
	}
}

func TestTurnQueue_CompleteAndFailRejectQueuedItems(t *testing.T) {
	s, _ := newTestServer(t)
	sessionID, err := s.store.NewConversation()
	if err != nil {
		t.Fatalf("NewConversation: %v", err)
	}
	enqueued := doTurnQueueRequest(t, s, http.MethodPost, "/api/v1/sessions/"+sessionID+"/turn-queue",
		`{"message":"queued","client_msg_id":1730000000003801}`)
	var env TurnQueueItemEnvelope
	if err := json.NewDecoder(enqueued.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}

	completed := doTurnQueueRequest(t, s, http.MethodPost,
		"/api/v1/sessions/"+sessionID+"/turn-queue/"+strconvInt(env.Item.ID)+"/complete", "")
	if completed.Code != http.StatusNotFound {
		t.Fatalf("complete queued status = %d, want 404; body=%s", completed.Code, completed.Body.String())
	}

	failed := doTurnQueueRequest(t, s, http.MethodPost,
		"/api/v1/sessions/"+sessionID+"/turn-queue/"+strconvInt(env.Item.ID)+"/fail",
		`{"error":"stale failure"}`)
	if failed.Code != http.StatusNotFound {
		t.Fatalf("fail queued status = %d, want 404; body=%s", failed.Code, failed.Body.String())
	}
}

func TestTurnQueue_Clear(t *testing.T) {
	s, _ := newTestServer(t)
	sessionID, err := s.store.NewConversation()
	if err != nil {
		t.Fatalf("NewConversation: %v", err)
	}
	_ = doTurnQueueRequest(t, s, http.MethodPost, "/api/v1/sessions/"+sessionID+"/turn-queue",
		`{"message":"one","client_msg_id":1730000000004001}`)
	_ = doTurnQueueRequest(t, s, http.MethodPost, "/api/v1/sessions/"+sessionID+"/turn-queue",
		`{"message":"two","client_msg_id":1730000000004002}`)

	cleared := doTurnQueueRequest(t, s, http.MethodDelete, "/api/v1/sessions/"+sessionID+"/turn-queue", "")
	if cleared.Code != http.StatusOK {
		t.Fatalf("clear status = %d, want 200; body=%s", cleared.Code, cleared.Body.String())
	}
	if !strings.Contains(cleared.Body.String(), `"cleared":2`) {
		t.Fatalf("clear response missing count: %s", cleared.Body.String())
	}
}

func doTurnQueueRequest(t *testing.T, s *Server, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
	}
	s.engine.ServeHTTP(w, req)
	return w
}

func strconvInt(v int64) string {
	return strconv.FormatInt(v, 10)
}
