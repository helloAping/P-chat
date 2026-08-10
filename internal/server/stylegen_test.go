package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/stylegen"
)

// fakeStyleGenLLM is a scripted streaming LLM for stylegen tests.
type fakeStyleGenLLM struct {
	content string
	err     error
}

func (f *fakeStyleGenLLM) ChatStream(ctx context.Context, provider, model string, msgs []llm.Message) <-chan llm.StreamChunk {
	ch := make(chan llm.StreamChunk)
	go func() {
		defer close(ch)
		select {
		case ch <- llm.StreamChunk{Content: f.content}:
		case <-ctx.Done():
			return
		}
		if f.err != nil {
			select {
			case ch <- llm.StreamChunk{Err: f.err}:
			case <-ctx.Done():
			}
			return
		}
		select {
		case ch <- llm.StreamChunk{Done: true}:
		case <-ctx.Done():
		}
	}()
	return ch
}

// wireStyleGen builds a test server whose stylegen JobManager is backed by
// the given fake LLM.
func wireStyleGen(t *testing.T, llmClient stylegen.LLMClient) (*Server, string) {
	t.Helper()
	s, _ := newTestServer(t)
	jm := stylegen.NewJobManager(stylegen.Deps{
		Store:    s.store,
		StyleMgr: s.styleMgr,
		LLM:      llmClient,
		Provider: "cs",
		MaxChars: 10000,
	})
	t.Cleanup(jm.Stop)
	s.Handler().SetStyleGen(jm)
	convID, err := s.store.NewConversation()
	if err != nil {
		t.Fatalf("NewConversation: %v", err)
	}
	// Seed one user message so the transcript is non-empty and the job
	// actually reaches the LLM (an empty conversation would short-circuit
	// with E_EMPTY before any LLM call).
	s.store.AddChatMessageTo(convID, llm.ChatMessage{
		Role: llm.RoleUser, Type: llm.TypeText, Content: "帮我写一个排序算法",
		MsgType: llm.MsgTypeText, SubmitToLLM: 1,
	})
	_ = s.store.Flush()
	return s, convID
}

func doJSONReq(t *testing.T, s *Server, method, path string, body string) *streamRecorder {
	t.Helper()
	var reader *bytes.Reader
	if body != "" {
		reader = bytes.NewReader([]byte(body))
	} else {
		reader = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	w := newStreamRecorder()
	s.Engine().ServeHTTP(w, req)
	return w
}

// TestStyleGenStart_Validation covers the E_ARGS / E_NOT_FOUND / 503
// branches and the happy 202 path.
func TestStyleGenStart_Validation(t *testing.T) {
	s, convID := wireStyleGen(t, &fakeStyleGenLLM{content: `{"id":"s1","prompt":"# P","memory":""}`})

	// Missing label → 400 E_ARGS.
	w := doJSONReq(t, s, "POST", "/api/v1/stylegen", `{"conversation_id":"`+convID+`"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing label: status = %d, want 400", w.Code)
	}
	if !strings.Contains(w.Body.String(), "E_ARGS") {
		t.Errorf("missing label: body = %s", w.Body.String())
	}

	// Missing conversation_id → 400 E_ARGS.
	w = doJSONReq(t, s, "POST", "/api/v1/stylegen", `{"label":"x"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing conversation_id: status = %d, want 400", w.Code)
	}

	// Bad mode → 400 E_ARGS.
	w = doJSONReq(t, s, "POST", "/api/v1/stylegen", `{"label":"x","conversation_id":"`+convID+`","mode":"wat"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("bad mode: status = %d, want 400", w.Code)
	}

	// Optimize with nonexistent style → 404 E_NOT_FOUND.
	w = doJSONReq(t, s, "POST", "/api/v1/stylegen", `{"mode":"optimize","style_id":"nope","label":"x","conversation_id":"`+convID+`"}`)
	if w.Code != http.StatusNotFound {
		t.Fatalf("optimize missing: status = %d, want 404", w.Code)
	}

	// Valid create → 202 {job_id}.
	w = doJSONReq(t, s, "POST", "/api/v1/stylegen", `{"label":"新风格","requirement":"温柔一点","conversation_id":"`+convID+`"}`)
	if w.Code != http.StatusAccepted {
		t.Fatalf("valid create: status = %d, want 202; body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.JobID == "" {
		t.Fatalf("job_id missing: body=%s err=%v", w.Body.String(), err)
	}
}

// TestStyleGenStart_Unavailable covers the 503 branch when the JobManager
// is not wired.
func TestStyleGenStart_Unavailable(t *testing.T) {
	s, _ := newTestServer(t) // no SetStyleGen
	convID, err := s.store.NewConversation()
	if err != nil {
		t.Fatal(err)
	}
	w := doJSONReq(t, s, "POST", "/api/v1/stylegen", `{"label":"x","conversation_id":"`+convID+`"}`)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", w.Code)
	}
}

// TestStyleGenEvents verifies the SSE sequence: stage → content → result.
func TestStyleGenEvents(t *testing.T) {
	s, convID := wireStyleGen(t, &fakeStyleGenLLM{content: `{"id":"sse-style","prompt":"# 流式风格","memory":"m"}`})

	w := doJSONReq(t, s, "POST", "/api/v1/stylegen", `{"label":"流式风格","conversation_id":"`+convID+`"}`)
	var resp struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("start: %v", err)
	}

	w = doJSONReq(t, s, "GET", "/api/v1/stylegen/"+resp.JobID+"/events", "")
	if w.Code != http.StatusOK {
		t.Fatalf("events status = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}

	types := map[string]bool{}
	var sawResult bool
	var resultID string
	for _, frame := range strings.Split(w.Body.String(), "\n\n") {
		if !strings.HasPrefix(frame, "data:") {
			continue
		}
		var ev struct {
			Type       string          `json:"type"`
			Stage      string          `json:"stage"`
			ResultJSON json.RawMessage `json:"result_json"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(frame, "data:")), &ev); err != nil {
			continue
		}
		types[ev.Type] = true
		if ev.Type == "result" {
			sawResult = true
			var res struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(ev.ResultJSON, &res)
			resultID = res.ID
		}
	}
	for _, want := range []string{"stage", "content", "result"} {
		if !types[want] {
			t.Errorf("missing SSE event type %q; got %v", want, types)
		}
	}
	if !sawResult {
		t.Fatal("no terminal result event")
	}
	if resultID != "sse-style" {
		t.Errorf("result id = %q, want sse-style", resultID)
	}
}

// TestStyleGenEvents_Error verifies the SSE terminal error event.
func TestStyleGenEvents_Error(t *testing.T) {
	s, convID := wireStyleGen(t, &fakeStyleGenLLM{err: context.DeadlineExceeded})
	w := doJSONReq(t, s, "POST", "/api/v1/stylegen", `{"label":"x","conversation_id":"`+convID+`"}`)
	var resp struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("start: %v", err)
	}

	w = doJSONReq(t, s, "GET", "/api/v1/stylegen/"+resp.JobID+"/events", "")
	var sawError bool
	var errKind string
	for _, frame := range strings.Split(w.Body.String(), "\n\n") {
		if !strings.HasPrefix(frame, "data:") {
			continue
		}
		var ev struct {
			Type      string `json:"type"`
			ErrorKind string `json:"error_kind"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(frame, "data:")), &ev); err != nil {
			continue
		}
		if ev.Type == "error" {
			sawError = true
			errKind = ev.ErrorKind
		}
	}
	if !sawError {
		t.Fatal("no terminal error event")
	}
	if errKind != string(stylegen.KindLLM) {
		t.Errorf("error_kind = %q, want E_LLM", errKind)
	}
}

// TestStyleGenStatus polls GET /:job until the job finishes.
func TestStyleGenStatus(t *testing.T) {
	s, convID := wireStyleGen(t, &fakeStyleGenLLM{content: `{"id":"poll-style","prompt":"# P","memory":""}`})
	w := doJSONReq(t, s, "POST", "/api/v1/stylegen", `{"label":"轮询","conversation_id":"`+convID+`"}`)
	var resp struct {
		JobID string `json:"job_id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("start: %v", err)
	}

	var status struct {
		Status string `json:"status"`
		Result *struct {
			ID string `json:"id"`
		} `json:"result"`
	}
	// The fake LLM finishes essentially immediately; a few polls with a
	// short pause should converge to done (stay under the 20 req/s burst).
	for i := 0; i < 15; i++ {
		w = doJSONReq(t, s, "GET", "/api/v1/stylegen/"+resp.JobID, "")
		_ = json.Unmarshal(w.Body.Bytes(), &status)
		if status.Status == "done" || status.Status == "error" {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if status.Status != "done" {
		t.Fatalf("status = %q, want done (body=%s)", status.Status, w.Body.String())
	}
	if status.Result == nil || status.Result.ID != "poll-style" {
		t.Errorf("result = %+v, want id poll-style", status.Result)
	}
}

// TestStyleGenStatus_UnknownJob covers the 404 branch.
func TestStyleGenStatus_UnknownJob(t *testing.T) {
	s, _ := wireStyleGen(t, &fakeStyleGenLLM{content: "{}"})
	w := doJSONReq(t, s, "GET", "/api/v1/stylegen/nope", "")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}
