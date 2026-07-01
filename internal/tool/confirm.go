package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

type ConfirmRequest struct {
	ToolName string `json:"tool_name"`
	Args     string `json:"args"`
	Reason   string `json:"reason"`
}

type ConfirmResponse struct {
	Approved bool `json:"approved"`
}

var (
	confirmMu  sync.Mutex
	confirmChs = make(map[string]chan ConfirmResponse)
)

func WaitForConfirm(ctx context.Context, sessionID string, req ConfirmRequest) (bool, error) {
	ch := make(chan ConfirmResponse, 1)

	confirmMu.Lock()
	if old, ok := confirmChs[sessionID]; ok {
		close(old)
	}
	confirmChs[sessionID] = ch
	confirmMu.Unlock()

	defer func() {
		confirmMu.Lock()
		if confirmChs[sessionID] == ch {
			delete(confirmChs, sessionID)
		}
		confirmMu.Unlock()
	}()

	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case resp := <-ch:
		return resp.Approved, nil
	case <-time.After(5 * time.Minute):
		return false, fmt.Errorf("confirm timed out")
	}
}

func SubmitConfirm(sessionID string, approved bool) bool {
	confirmMu.Lock()
	ch, ok := confirmChs[sessionID]
	confirmMu.Unlock()
	if !ok {
		return false
	}
	select {
	case ch <- ConfirmResponse{Approved: approved}:
		return true
	default:
		return false
	}
}

func MarshalConfirm(req ConfirmRequest) string {
	data, _ := json.Marshal(req)
	return string(data)
}
