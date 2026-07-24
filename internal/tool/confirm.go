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
	// ResolvedPath is the absolute path the LLM will actually
	// touch (post resolveToProjectRoot, post `..` cleanup).
	// Surfaced in the modal so the user can SEE the real target
	// even when the LLM passed a relative or traversal form.
	ResolvedPath string `json:"resolved_path,omitempty"`
	// PathClass is one of "project", "global", "external",
	// "protected", "allowed". Drives the modal's path-class
	// label and the colour of the "this is outside the project"
	// warning. Empty means the tool isn't path-bearing
	// (exec_command) and the modal omits the chip.
	PathClass string `json:"path_class,omitempty"`
	// RiskLevel is one of "low", "medium", "high". Drives the
	// button colour and modal title. "high" for writes and
	// arbitrary-exec, "low" for reads. Phase 1 uses a static
	// mapping; Phase 2 will derive it from the matched
	// dangerous-pattern severity.
	RiskLevel string `json:"risk_level,omitempty"`
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
				// Copy to avoid slice aliasing (same as SubmitConfirm).
				newList := make([]chan ConfirmResponse, 0, len(list)-1)
				newList = append(newList, list[:i]...)
				newList = append(newList, list[i+1:]...)
				confirmChs[sessionID] = newList
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
	// Copy the tail into a fresh slice so a concurrent
	// WaitForConfirm append cannot write into the slot we
	// just released via list[1:] (slice aliasing bug).
	rest := make([]chan ConfirmResponse, len(list)-1)
	copy(rest, list[1:])
	confirmChs[sessionID] = rest
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

// confirmEmitterKey stores a function that pushes a ConfirmRequest
// onto the SSE stream (as ToolConfirmJSON). Browser tools and other
// non-agent-dispatch confirm gates use this so they can emit the
// modal without going through confirmTargetFor.
type confirmEmitterKey struct{}

// WithConfirmEmitter attaches an SSE emitter for ConfirmRequest.
// The agent sets this before invoking tool handlers so browser_*
// (and future MCP/dynamic tools that self-gate) can surface the
// same ToolConfirmModal the path/exec sandbox uses.
func WithConfirmEmitter(ctx context.Context, emit func(ConfirmRequest)) context.Context {
	if emit == nil {
		return ctx
	}
	return context.WithValue(ctx, confirmEmitterKey{}, emit)
}

func confirmEmitterFromCtx(ctx context.Context) func(ConfirmRequest) {
	if v, ok := ctx.Value(confirmEmitterKey{}).(func(ConfirmRequest)); ok {
		return v
	}
	return nil
}

// PermissionLevelFromCtx returns the per-session permission level
// ("ask" / "auto" / "full"), defaulting to PermissionAsk.
func PermissionLevelFromCtx(ctx context.Context) string {
	if ctx == nil {
		return PermissionAsk
	}
	if v, ok := ctx.Value(PermissionLevelKey{}).(string); ok && v != "" {
		return v
	}
	return PermissionAsk
}

// RequireConfirm runs the shared confirm modal flow for tools that
// self-gate (browser_*, future MCP tools). Behaviour:
//
//   - permission "full"  → skip (caller still owns any hard blocks)
//   - permission "auto"  → skip modal, return approved=true
//   - no emitter / no session → fail-closed (approved=false) so a
//     missing wire-up cannot silently open a high-risk tool
//   - otherwise emit ConfirmRequest and WaitForConfirm
//
// Returns (approved, error). error is non-nil on timeout / cancel.
func RequireConfirm(ctx context.Context, req ConfirmRequest) (bool, error) {
	level := PermissionLevelFromCtx(ctx)
	if level == PermissionFull || level == PermissionAuto {
		return true, nil
	}

	sid, _ := ctx.Value(SessionIDKey{}).(string)
	emit := confirmEmitterFromCtx(ctx)
	if emit == nil || sid == "" {
		return false, fmt.Errorf("confirm unavailable (session or emitter missing)")
	}

	emit(req)
	return WaitForConfirm(ctx, sid, req)
}
