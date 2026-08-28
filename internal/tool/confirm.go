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
	Approved bool   `json:"approved"`
	Action   string `json:"action,omitempty"`
}

const (
	ConfirmActionReject = "reject"
	ConfirmActionOnce   = "once"
	ConfirmActionAlways = "always"
)

type pendingConfirm struct {
	req ConfirmRequest
	ch  chan ConfirmResponse
}

var (
	confirmMu          sync.Mutex
	confirmChs         = make(map[string][]pendingConfirm)
	confirmAllowed     = make(map[string]map[string]struct{})
	sessionPermissions = make(map[string]string)
)

func WaitForConfirmResponse(ctx context.Context, sessionID string, req ConfirmRequest) (ConfirmResponse, error) {
	ch := make(chan ConfirmResponse, 1)

	confirmMu.Lock()
	confirmChs[sessionID] = append(confirmChs[sessionID], pendingConfirm{req: req, ch: ch})
	confirmMu.Unlock()

	defer func() {
		confirmMu.Lock()
		list := confirmChs[sessionID]
		for i, item := range list {
			if item.ch == ch {
				// Copy to avoid slice aliasing (same as SubmitConfirm).
				newList := make([]pendingConfirm, 0, len(list)-1)
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

	waitCtx := InteractiveContextFrom(ctx)
	timer := time.NewTimer(ConfirmWaitTimeout)
	defer timer.Stop()
	select {
	case <-waitCtx.Done():
		return ConfirmResponse{Approved: false, Action: ConfirmActionReject}, waitCtx.Err()
	case resp := <-ch:
		resp = normalizeConfirmResponse(resp)
		return resp, nil
	case <-timer.C:
		return ConfirmResponse{Approved: false, Action: ConfirmActionReject}, fmt.Errorf("confirm timed out")
	}
}

func WaitForConfirm(ctx context.Context, sessionID string, req ConfirmRequest) (bool, error) {
	resp, err := WaitForConfirmResponse(ctx, sessionID, req)
	if err != nil {
		return false, err
	}
	return resp.Approved, nil
}

func SubmitConfirmResponse(sessionID string, resp ConfirmResponse) bool {
	resp = normalizeConfirmResponse(resp)
	confirmMu.Lock()
	list := confirmChs[sessionID]
	if len(list) == 0 {
		confirmMu.Unlock()
		return false
	}
	item := list[0]
	// Copy the tail into a fresh slice so a concurrent
	// WaitForConfirm append cannot write into the slot we
	// just released via list[1:] (slice aliasing bug).
	rest := make([]pendingConfirm, len(list)-1)
	copy(rest, list[1:])
	confirmChs[sessionID] = rest
	if len(confirmChs[sessionID]) == 0 {
		delete(confirmChs, sessionID)
	}
	if resp.Action == ConfirmActionAlways && resp.Approved {
		rememberAllowedConfirmLocked(sessionID, item.req)
	}
	confirmMu.Unlock()

	select {
	case item.ch <- resp:
		return true
	default:
		return false
	}
}

func SubmitConfirm(sessionID string, approved bool) bool {
	action := ConfirmActionReject
	if approved {
		action = ConfirmActionOnce
	}
	return SubmitConfirmResponse(sessionID, ConfirmResponse{Approved: approved, Action: action})
}

func normalizeConfirmResponse(resp ConfirmResponse) ConfirmResponse {
	switch resp.Action {
	case ConfirmActionAlways, ConfirmActionOnce:
		resp.Approved = true
	case ConfirmActionReject:
		resp.Approved = false
	default:
		if resp.Approved {
			resp.Action = ConfirmActionOnce
		} else {
			resp.Action = ConfirmActionReject
		}
	}
	return resp
}

// NormalizePermissionLevel normalises persisted/session permission
// values into the three supported levels.
func NormalizePermissionLevel(level string) string {
	switch level {
	case PermissionAuto, PermissionFull:
		return level
	default:
		return PermissionAsk
	}
}

// SetSessionPermissionLevel publishes a live permission override for
// a session. It is intentionally process-local: conversations persist
// the durable value in metadata, while this map lets an already-running
// tool dispatch observe a GUI permission change immediately.
func SetSessionPermissionLevel(sessionID, level string) {
	if sessionID == "" {
		return
	}
	var approvePending []pendingConfirm
	confirmMu.Lock()
	if level == "" {
		delete(sessionPermissions, sessionID)
		confirmMu.Unlock()
		return
	}
	level = NormalizePermissionLevel(level)
	sessionPermissions[sessionID] = level
	if level == PermissionAuto || level == PermissionFull {
		approvePending = append(approvePending, confirmChs[sessionID]...)
		delete(confirmChs, sessionID)
	}
	confirmMu.Unlock()

	for _, item := range approvePending {
		select {
		case item.ch <- ConfirmResponse{Approved: true, Action: ConfirmActionOnce}:
		default:
		}
	}
}

// SessionPermissionLevel returns the live process-local permission
// override for a session, or "" when no override is installed.
func SessionPermissionLevel(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	confirmMu.Lock()
	defer confirmMu.Unlock()
	return sessionPermissions[sessionID]
}

func IsConfirmAllowed(sessionID string, req ConfirmRequest) bool {
	key := confirmAllowKey(req)
	if sessionID == "" || key == "" {
		return false
	}
	confirmMu.Lock()
	defer confirmMu.Unlock()
	_, ok := confirmAllowed[sessionID][key]
	return ok
}

func rememberAllowedConfirmLocked(sessionID string, req ConfirmRequest) {
	key := confirmAllowKey(req)
	if sessionID == "" || key == "" {
		return
	}
	if confirmAllowed[sessionID] == nil {
		confirmAllowed[sessionID] = make(map[string]struct{})
	}
	confirmAllowed[sessionID][key] = struct{}{}
}

func confirmAllowKey(req ConfirmRequest) string {
	if req.ToolName == "" {
		return ""
	}
	if req.ResolvedPath != "" {
		return req.ToolName + "|path|" + req.PathClass + "|" + req.ResolvedPath
	}
	return req.ToolName + "|args|" + normalizeConfirmArgs(req.Args)
}

func normalizeConfirmArgs(args string) string {
	var payload map[string]any
	if err := json.Unmarshal([]byte(args), &payload); err == nil {
		if command, ok := payload["command"].(string); ok {
			return compactSpaces(command)
		}
	}
	return compactSpaces(args)
}

func compactSpaces(s string) string {
	out := make([]rune, 0, len(s))
	lastSpace := false
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			if !lastSpace {
				out = append(out, ' ')
				lastSpace = true
			}
			continue
		}
		out = append(out, r)
		lastSpace = false
	}
	return string(out)
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

type isolatedPermissionLevelKey struct{}

// WithIsolatedPermissionLevel attaches a fixed permission level and prevents
// process-local session permission overrides from replacing it. Sub-agents use
// this so a parent session's live "full"/"auto" setting cannot leak into child
// tool dispatch.
func WithIsolatedPermissionLevel(ctx context.Context, level string) context.Context {
	ctx = context.WithValue(ctx, isolatedPermissionLevelKey{}, true)
	return WithPermissionLevel(ctx, level)
}

func isolatedPermissionLevelFromCtx(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(isolatedPermissionLevelKey{}).(bool)
	return v
}

type parentAuthorityOnlyKey struct{}

// WithParentAuthorityOnly marks a tool context as unable to ask or decide
// confirmations directly. Sub-agent handlers run with this flag so self-gated
// tools fail closed and hand the decision back to the parent conversation.
func WithParentAuthorityOnly(ctx context.Context) context.Context {
	return context.WithValue(ctx, parentAuthorityOnlyKey{}, true)
}

func ParentAuthorityOnlyFromCtx(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(parentAuthorityOnlyKey{}).(bool)
	return v
}

// PermissionLevelFromCtx returns the per-session permission level
// ("ask" / "auto" / "full"), defaulting to PermissionAsk.
func PermissionLevelFromCtx(ctx context.Context) string {
	if ctx == nil {
		return PermissionAsk
	}
	if sid, ok := ctx.Value(SessionIDKey{}).(string); ok && !isolatedPermissionLevelFromCtx(ctx) {
		if level := SessionPermissionLevel(sid); level != "" {
			return level
		}
	}
	if v, ok := ctx.Value(PermissionLevelKey{}).(string); ok && v != "" {
		return NormalizePermissionLevel(v)
	}
	return PermissionAsk
}

// RequireConfirm runs the shared confirm modal flow for tools that
// self-gate (browser_*, future MCP tools). Behaviour:
//
//   - parent-authority-only → fail closed; the caller must hand the
//     decision back to the parent conversation
//   - permission "full"  → skip (caller still owns any hard blocks)
//   - permission "auto"  → skip modal, return approved=true
//   - no emitter / no session → fail-closed (approved=false) so a
//     missing wire-up cannot silently open a high-risk tool
//   - otherwise emit ConfirmRequest and WaitForConfirm
//
// Returns (approved, error). error is non-nil on timeout / cancel.
func RequireConfirm(ctx context.Context, req ConfirmRequest) (bool, error) {
	if ParentAuthorityOnlyFromCtx(ctx) {
		return false, fmt.Errorf("confirm requires parent conversation approval")
	}

	level := PermissionLevelFromCtx(ctx)
	if level == PermissionFull || level == PermissionAuto {
		return true, nil
	}

	sid, _ := ctx.Value(SessionIDKey{}).(string)
	emit := confirmEmitterFromCtx(ctx)
	if emit == nil || sid == "" {
		return false, fmt.Errorf("confirm unavailable (session or emitter missing)")
	}
	if IsConfirmAllowed(sid, req) {
		return true, nil
	}

	emit(req)
	return WaitForConfirm(ctx, sid, req)
}
