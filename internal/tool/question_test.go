package tool

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"
)

// clearQuestionState removes the test session from the
// package-level questionChs map. Call as
//
//	defer clearQuestionState(t, sessionID)()
//
// at the top of each test so concurrent tests cannot
// pollute each other's state.
func clearQuestionState(t *testing.T, sessionID string) func() {
	t.Helper()
	return func() {
		questionMu.Lock()
		delete(questionChs, sessionID)
		questionMu.Unlock()
	}
}

// TestSubmitAnswerDeliversResponse verifies the happy path:
// a goroutine waiting on WaitForAnswer is unblocked by
// SubmitAnswer with the same QuestionResponse payload.
func TestSubmitAnswerDeliversResponse(t *testing.T) {
	sessionID := "test-submit-ok"
	defer clearQuestionState(t, sessionID)()

	questions := []Question{{Header: "心情", Question: "你今天心情怎么样？"}}

	gotCh := make(chan QuestionResponse, 1)
	go func() {
		resp, err := WaitForAnswer(context.Background(), sessionID, questions, nil)
		if err != nil {
			t.Errorf("WaitForAnswer returned error: %v", err)
			return
		}
		gotCh <- resp
	}()
	// Give the goroutine a moment to register the channel.
	time.Sleep(20 * time.Millisecond)

	answers := map[string]string{"心情": "很棒 😎"}
	if !SubmitAnswer(sessionID, QuestionResponse{Questions: questions, Answers: answers}) {
		t.Fatal("SubmitAnswer returned false (no pending question)")
	}

	select {
	case resp := <-gotCh:
		if v, ok := resp.Answers["心情"]; !ok || v != "很棒 😎" {
			t.Errorf("Answers[\"心情\"] = %q (want %q)", v, "很棒 😎")
		}
		if len(resp.Answers) != 1 {
			t.Errorf("len(Answers) = %d (want 1)", len(resp.Answers))
		}
	case <-time.After(time.Second):
		t.Fatal("WaitForAnswer did not unblock within 1s")
	}
}

// TestSubmitAnswerKeyedByHeader locks in the wire contract:
// SubmitAnswer does NOT transform keys. The map the caller
// passes is the map the question tool handler sees. Keying
// by `header` is the LLM-side contract; a regression on the
// frontend (modal keying by question text) would still pass
// this test, but the loop would surface immediately in
// integration tests because the LLM would re-ask.
func TestSubmitAnswerKeyedByHeader(t *testing.T) {
	sessionID := "test-submit-header"
	defer clearQuestionState(t, sessionID)()

	questions := []Question{{Header: "心情", Question: "你今天心情怎么样？"}}

	gotCh := make(chan QuestionResponse, 1)
	go func() {
		resp, err := WaitForAnswer(context.Background(), sessionID, questions, nil)
		if err != nil {
			t.Errorf("WaitForAnswer returned error: %v", err)
			return
		}
		gotCh <- resp
	}()
	time.Sleep(20 * time.Millisecond)

	// Pass both keys: header (correct) + question text
	// (the old bug). SubmitAnswer must not drop either.
	SubmitAnswer(sessionID, QuestionResponse{
		Questions: questions,
		Answers:   map[string]string{"心情": "很棒 😎", "你今天心情怎么样？": "stale"},
	})

	select {
	case resp := <-gotCh:
		if resp.Answers["心情"] != "很棒 😎" {
			t.Errorf("Answers[\"心情\"] = %q (want %q)", resp.Answers["心情"], "很棒 😎")
		}
		if resp.Answers["你今天心情怎么样？"] != "stale" {
			t.Errorf("text-keyed answer was dropped; SubmitAnswer must not transform keys")
		}
	case <-time.After(time.Second):
		t.Fatal("WaitForAnswer did not unblock within 1s")
	}
}

// TestSubmitAnswerNoPending verifies SubmitAnswer returns
// false when no question is pending for the session.
func TestSubmitAnswerNoPending(t *testing.T) {
	if SubmitAnswer("test-no-pending", QuestionResponse{Answers: map[string]string{"x": "y"}}) {
		t.Fatal("SubmitAnswer returned true with no pending question")
	}
}

// TestWaitForAnswerEmitsTwoSendFnCalls locks in the persistence
// contract: when the user answers, WaitForAnswer must call
// sendFn TWICE — once with the raw question array (so the
// frontend can pop the modal) and once with the result-with-
// answers shape (so the agent's partsAcc can flip the trailing
// open question part to status="ok" + Name=answers).
//
// Without the second call, the part stays at "open" + empty
// Name in the persisted snapshot, and a session reload shows
// "等待回答" with no highlighted picks — even though the user
// did answer. This test fails if anyone removes the second
// sendFn call.
func TestWaitForAnswerEmitsTwoSendFnCalls(t *testing.T) {
	sessionID := "test-sendfn-twice"
	defer clearQuestionState(t, sessionID)()

	questions := []Question{{Header: "心情", Question: "你今天心情怎么样？"}}

	// Record every sendFn call. We use a mutex because
	// WaitForAnswer calls sendFn from the same goroutine
	// (it blocks until the user submits), so a plain slice
	// append would be safe — but the explicit mutex makes
	// the test robust if the call site ever moves.
	var (
		mu     sync.Mutex
		calls  []string
	)
	record := func(data string) {
		mu.Lock()
		calls = append(calls, data)
		mu.Unlock()
	}

	done := make(chan struct{})
	go func() {
		_, _ = WaitForAnswer(context.Background(), sessionID, questions, record)
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)

	answers := map[string]string{"心情": "还行"}
	if !SubmitAnswer(sessionID, QuestionResponse{Questions: questions, Answers: answers}) {
		t.Fatal("SubmitAnswer returned false")
	}

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("WaitForAnswer did not unblock within 1s")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 2 {
		t.Fatalf("sendFn called %d times, want 2 (prompt + result-with-answers)", len(calls))
	}

	// First call: raw question array. The frontend uses
	// this to know which question to ask.
	var firstArr []Question
	if err := json.Unmarshal([]byte(calls[0]), &firstArr); err != nil {
		t.Fatalf("sendFn #1 payload is not a JSON array: %v\n  payload: %s", err, calls[0])
	}
	if len(firstArr) != 1 || firstArr[0].Header != "心情" {
		t.Errorf("sendFn #1: got %+v, want 1 question with header=心情", firstArr)
	}

	// Second call: {"questions":..., "answers":...} object.
	// The agent's partsAcc recognises this shape and
	// updates the trailing open question part to "ok" +
	// Name=answers (see internal/agent/parts.go:471-541).
	var secondObj struct {
		Questions []Question            `json:"questions"`
		Answers   map[string]string     `json:"answers"`
	}
	if err := json.Unmarshal([]byte(calls[1]), &secondObj); err != nil {
		t.Fatalf("sendFn #2 payload is not a JSON object: %v\n  payload: %s", err, calls[1])
	}
	if len(secondObj.Questions) != 1 || secondObj.Questions[0].Header != "心情" {
		t.Errorf("sendFn #2 questions: got %+v, want 1 question with header=心情", secondObj.Questions)
	}
	if secondObj.Answers["心情"] != "还行" {
		t.Errorf("sendFn #2 answers[心情] = %q, want %q", secondObj.Answers["心情"], "还行")
	}
}

// TestWaitForAnswerSkipsSecondSendFnWhenAnswersEmpty verifies
// the corner case: if SubmitAnswer is called with an empty
// answers map (e.g. the user closed the modal without
// selecting anything), the second sendFn is NOT called. The
// part should stay at "open" in this case — both in the
// persisted snapshot and in the live UI, which shows
// "等待回答" until the user actually picks something.
func TestWaitForAnswerSkipsSecondSendFnWhenAnswersEmpty(t *testing.T) {
	sessionID := "test-sendfn-empty"
	defer clearQuestionState(t, sessionID)()

	questions := []Question{{Header: "心情", Question: "你今天心情怎么样？"}}

	var (
		mu    sync.Mutex
		calls []string
	)
	record := func(data string) {
		mu.Lock()
		calls = append(calls, data)
		mu.Unlock()
	}

	done := make(chan struct{})
	go func() {
		_, _ = WaitForAnswer(context.Background(), sessionID, questions, record)
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)

	// Submit with empty answers — the agent's partsAcc
	// would not update the part in this case (the
	// "answers > 0" check fails), so we don't need the
	// second event.
	SubmitAnswer(sessionID, QuestionResponse{Questions: questions, Answers: map[string]string{}})

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("WaitForAnswer did not unblock within 1s")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(calls) != 1 {
		t.Errorf("sendFn called %d times, want 1 (empty-answers path should skip the result event)", len(calls))
	}
}
