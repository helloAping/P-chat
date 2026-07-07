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
	confirmChs = make(map[string][]chan ConfirmResponse)
)

func WaitForConfirm(ctx context.Context, sessionID string, req ConfirmRequest) (bool, error) {
	ch := make(chan ConfirmResponse, 1)

	confirmMu.Lock()
	confirmChs[sessionID] = append(confirmChs[sessionID], ch)
	confirmMu.Unlock()

	defer func() {
		confirmMu.Lock()
		list := confirmChs[sessionID]
		for i, c := range list {
			if c == ch {
				confirmChs[sessionID] = append(list[:i], list[i+1:]...)
				break
			}
		}
		if len(confirmChs[sessionID]) == 0 {
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
	list := confirmChs[sessionID]
	if len(list) == 0 {
		confirmMu.Unlock()
		return false
	}
	ch := list[0]
	confirmChs[sessionID] = list[1:]
	if len(confirmChs[sessionID]) == 0 {
		delete(confirmChs, sessionID)
	}
	confirmMu.Unlock()

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
