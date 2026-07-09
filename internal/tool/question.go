package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
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
	// If a previous question is still pending for this session,
	// just overwrite the map entry — do NOT close the old
	// channel. Closing while SubmitAnswer may still hold a
	// reference and write to it would cause a panic.
	// The old goroutine will return via its 5-minute timeout
	// (or ctx cancel) and the old channel is then GC'd.
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
		// Emit a second question event with the result-with-
		// answers shape so the agent's partsAccumulator can
		// flip the trailing open question part in place to
		// status="ok" + Name=answers. Without this, the part
		// stays at status="open" + Name="" in the persisted
		// snapshot, so a session reload shows "等待回答" with
		// no highlighted picks — even though the user did
		// answer. The frontend's `submitQuestionAnswer` does
		// the same in the live session's in-memory state, but
		// it cannot reach the server-side partsAcc; this
		// second sendFn closes the round-trip.
		//
		// Skipped when the user submitted with zero answers
		// (e.g. SubmitAnswer called with an empty map) — in
		// that case the trailing open question part is left
		// as "open" on purpose, matching the live UI which
		// also shows "等待回答" until something is selected.
		//
		// The partsAcc recognises the {"questions":...,
		// "answers":...} shape and updates the trailing open
		// question part in place. See
		// internal/agent/parts.go:471-541.
		if sendFn != nil && len(resp.Answers) > 0 {
			data, _ := json.Marshal(resp)
			log.Printf("[question] sending answers to eventCh (session=%s, %d answers)", sessionID, len(resp.Answers))
			sendFn(string(data))
		}
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
	// Diagnostic: log the actual answer keys so a future
	// key-mismatch bug (the modal sending question text
	// instead of header) is immediately visible. The LLM
	// reads answers by header (Anthropic contract), so
	// any key not matching a question's `header` is a
	// silent failure that makes the LLM think the user
	// did not answer and re-ask the question — a loop.
	if len(resp.Answers) == 0 {
		log.Printf("[question] WARNING SubmitAnswer: 0 answers (session=%s) — LLM will treat as \"user did not answer\" and may loop", sessionID)
	} else {
		log.Printf("[question] SubmitAnswer keys=%v (session=%s)", sortedKeys(resp.Answers), sessionID)
	}
	select {
	case ch <- resp:
		return true
	default:
		return false
	}
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
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

