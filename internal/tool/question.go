package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"
)

type QuestionOption struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

type Question struct {
	Question    string           `json:"question"`
	Header      string           `json:"header"`
	Options     []QuestionOption `json:"options"`
	MultiSelect bool             `json:"multi_select,omitempty"`
}

type QuestionResponse struct {
	Questions []Question        `json:"questions"`
	Answers   map[string]string `json:"answers"`
}

type questionRequest struct {
	Questions []Question `json:"questions"`
}

// questionStore manages per-session pending questions.
var (
	questionMu  sync.Mutex
	questionChs = make(map[string]chan QuestionResponse)
)

// eventSenderKey is the context key for a function that sends
// JSON strings to the SSE event stream. Set by the agent before
// tool execution.
type eventSenderKey struct{}

// WithEventSender stores a send function in ctx.
func WithEventSender(ctx context.Context, send func(jsonData string)) context.Context {
	return context.WithValue(ctx, eventSenderKey{}, send)
}

func eventSenderFromCtx(ctx context.Context) func(string) {
	if v, ok := ctx.Value(eventSenderKey{}).(func(string)); ok {
		return v
	}
	return nil
}

// WaitForAnswer sends the question payload through a callback
// (which should write to the tool event channel for SSE) and
// blocks until the user responds or the context is cancelled.
func WaitForAnswer(ctx context.Context, sessionID string, questions []Question, sendFn func(jsonData string)) (QuestionResponse, error) {
	ch := make(chan QuestionResponse, 1)

	questionMu.Lock()
	if old, ok := questionChs[sessionID]; ok {
		close(old)
	}
	questionChs[sessionID] = ch
	questionMu.Unlock()

	defer func() {
		questionMu.Lock()
		if questionChs[sessionID] == ch {
			delete(questionChs, sessionID)
		}
		questionMu.Unlock()
	}()

	qj, _ := json.Marshal(questions)
	if sendFn != nil {
		log.Printf("[question] sending %d questions to eventCh (session=%s)", len(questions), sessionID)
		sendFn(string(qj))
		log.Printf("[question] sendFn returned (session=%s)", sessionID)
	} else {
		log.Printf("[question] WARNING: sendFn is nil (session=%s)", sessionID)
	}

	log.Printf("[question] waiting for user answer (session=%s, timeout=5m)", sessionID)
	select {
	case <-ctx.Done():
		log.Printf("[question] ctx cancelled (session=%s): %v", sessionID, ctx.Err())
		return QuestionResponse{}, ctx.Err()
	case resp := <-ch:
		log.Printf("[question] received answer (session=%s, %d answers)", sessionID, len(resp.Answers))
		return resp, nil
	case <-time.After(5 * time.Minute):
		log.Printf("[question] timeout (session=%s)", sessionID)
		return QuestionResponse{}, fmt.Errorf("question timed out waiting for user response")
	}
}

// SubmitAnswer delivers the user's answer to the waiting question
// handler. Returns false if no question is pending for the session.
func SubmitAnswer(sessionID string, resp QuestionResponse) bool {
	questionMu.Lock()
	ch, ok := questionChs[sessionID]
	questionMu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- resp:
		return true
	default:
		return false
	}
}

func handleQuestion(ctx context.Context, argsRaw json.RawMessage) (*CallResult, error) {
	var req questionRequest
	if err := json.Unmarshal(argsRaw, &req); err != nil {
		return &CallResult{Content: "参数错误: " + err.Error(), IsError: true}, nil
	}
	if len(req.Questions) == 0 {
		return &CallResult{Content: "questions 数组不能为空", IsError: true}, nil
	}

	sid, _ := ctx.Value(SessionIDKey{}).(string)
	sendFn := eventSenderFromCtx(ctx)

	resp, err := WaitForAnswer(ctx, sid, req.Questions, sendFn)
	if err != nil {
		return &CallResult{
			Content: fmt.Sprintf("等待用户回答失败: %v", err),
			IsError: true,
		}, nil
	}

	data, _ := json.Marshal(resp)
	return &CallResult{Content: string(data)}, nil
}

