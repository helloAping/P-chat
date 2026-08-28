package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/p-chat/pchat/internal/memory"
)

type recordingSubagentCanceller struct {
	calledID string
}

func (r *recordingSubagentCanceller) Cancel(id string) bool {
	r.calledID = id
	return true
}

func TestSubagentJobsAPI_ListGetAndCancel(t *testing.T) {
	s, _ := newTestServer(t)
	sessionID, err := s.store.NewConversation()
	if err != nil {
		t.Fatal(err)
	}
	job, err := s.store.CreateSubagentJob(memory.SubagentJob{
		TaskID:       "task_async_1",
		SessionID:    sessionID,
		Status:       memory.SubagentJobRunning,
		SubagentType: "research",
		Description:  "collect evidence",
	})
	if err != nil {
		t.Fatal(err)
	}
	canceller := &recordingSubagentCanceller{}
	s.handler.SetSubagentJobCanceller(canceller)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sessionID+"/subagent-jobs?limit=5", nil)
	s.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list status = %d, want %d; body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	var listBody struct {
		Jobs []memory.SubagentJob `json:"jobs"`
	}
	if err := json.NewDecoder(w.Body).Decode(&listBody); err != nil {
		t.Fatal(err)
	}
	if len(listBody.Jobs) != 1 || listBody.Jobs[0].TaskID != "task_async_1" {
		t.Fatalf("jobs = %#v, want task_async_1", listBody.Jobs)
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sessionID+"/subagent-jobs/task_async_1", nil)
	s.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("get status = %d, want %d; body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	var got memory.SubagentJob
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.ID != job.ID {
		t.Fatalf("job id = %q, want %q", got.ID, job.ID)
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+sessionID+"/subagent-jobs/task_async_1/cancel", nil)
	s.engine.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("cancel status = %d, want %d; body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	var cancelBody struct {
		Job           memory.SubagentJob `json:"job"`
		CancelledLive bool               `json:"cancelled_live"`
	}
	if err := json.NewDecoder(w.Body).Decode(&cancelBody); err != nil {
		t.Fatal(err)
	}
	if !cancelBody.CancelledLive {
		t.Fatal("cancelled_live = false, want true")
	}
	if canceller.calledID != job.ID {
		t.Fatalf("canceller called id = %q, want %q", canceller.calledID, job.ID)
	}
	if cancelBody.Job.Status != memory.SubagentJobCancelled {
		t.Fatalf("job status = %q, want %q", cancelBody.Job.Status, memory.SubagentJobCancelled)
	}
}

func TestSubagentJobsAPI_NotFound(t *testing.T) {
	s, _ := newTestServer(t)
	sessionID, err := s.store.NewConversation()
	if err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+sessionID+"/subagent-jobs/missing", nil)
	s.engine.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body=%s", w.Code, http.StatusNotFound, w.Body.String())
	}
}
