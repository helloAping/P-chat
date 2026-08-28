package tool

import (
	"context"
	"testing"
	"time"
)

func TestPermissionLevelFromCtxUsesLiveSessionOverride(t *testing.T) {
	sessionID := "permission-live-" + time.Now().Format("150405.000000000")
	ctx := WithSessionID(
		WithPermissionLevel(context.Background(), PermissionAsk),
		sessionID,
	)

	SetSessionPermissionLevel(sessionID, PermissionFull)
	t.Cleanup(func() { SetSessionPermissionLevel(sessionID, "") })

	if got := PermissionLevelFromCtx(ctx); got != PermissionFull {
		t.Fatalf("PermissionLevelFromCtx = %q, want %q", got, PermissionFull)
	}

	SetSessionPermissionLevel(sessionID, PermissionAsk)
	if got := PermissionLevelFromCtx(WithPermissionLevel(ctx, PermissionFull)); got != PermissionAsk {
		t.Fatalf("PermissionLevelFromCtx after ask override = %q, want %q", got, PermissionAsk)
	}
}

func TestIsolatedPermissionLevelIgnoresLiveSessionOverride(t *testing.T) {
	sessionID := "permission-isolated-" + time.Now().Format("150405.000000000")
	ctx := WithSessionID(
		WithIsolatedPermissionLevel(context.Background(), PermissionAsk),
		sessionID,
	)

	SetSessionPermissionLevel(sessionID, PermissionFull)
	t.Cleanup(func() { SetSessionPermissionLevel(sessionID, "") })

	if got := PermissionLevelFromCtx(ctx); got != PermissionAsk {
		t.Fatalf("PermissionLevelFromCtx = %q, want %q", got, PermissionAsk)
	}
}

func TestRequireConfirmParentAuthorityOnlyFailsClosed(t *testing.T) {
	emitted := false
	ctx := WithParentAuthorityOnly(
		WithConfirmEmitter(
			WithSessionID(context.Background(), "subagent-confirm-test"),
			func(ConfirmRequest) { emitted = true },
		),
	)

	approved, err := RequireConfirm(ctx, ConfirmRequest{
		ToolName:  "browser_click",
		Args:      `{}`,
		RiskLevel: "medium",
	})
	if err == nil {
		t.Fatal("RequireConfirm returned nil error, want parent-approval error")
	}
	if approved {
		t.Fatal("RequireConfirm approved in parent-authority-only mode")
	}
	if emitted {
		t.Fatal("RequireConfirm emitted a child confirm event")
	}
}

func TestConfirmAlwaysAllowStoresSessionRule(t *testing.T) {
	sessionID := "confirm-always-" + time.Now().Format("150405.000000000")
	req := ConfirmRequest{
		ToolName:  "exec_command",
		Args:      `{"command":"npm run dev"}`,
		RiskLevel: "high",
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	done := make(chan ConfirmResponse, 1)
	go func() {
		resp, err := WaitForConfirmResponse(ctx, sessionID, req)
		if err != nil {
			t.Errorf("WaitForConfirmResponse returned error: %v", err)
			return
		}
		done <- resp
	}()

	time.Sleep(10 * time.Millisecond)
	if !SubmitConfirmResponse(sessionID, ConfirmResponse{Action: ConfirmActionAlways}) {
		t.Fatal("SubmitConfirmResponse returned false")
	}

	select {
	case resp := <-done:
		if !resp.Approved || resp.Action != ConfirmActionAlways {
			t.Fatalf("response = %+v, want approved always", resp)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for confirm response")
	}

	if !IsConfirmAllowed(sessionID, req) {
		t.Fatal("always allow did not store a session rule")
	}
}

func TestConfirmAllowOnceDoesNotStoreSessionRule(t *testing.T) {
	sessionID := "confirm-once-" + time.Now().Format("150405.000000000")
	req := ConfirmRequest{
		ToolName:  "exec_command",
		Args:      `{"command":"npm run build"}`,
		RiskLevel: "high",
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	done := make(chan ConfirmResponse, 1)
	go func() {
		resp, err := WaitForConfirmResponse(ctx, sessionID, req)
		if err != nil {
			t.Errorf("WaitForConfirmResponse returned error: %v", err)
			return
		}
		done <- resp
	}()

	time.Sleep(10 * time.Millisecond)
	if !SubmitConfirmResponse(sessionID, ConfirmResponse{Approved: true}) {
		t.Fatal("SubmitConfirmResponse returned false")
	}

	select {
	case resp := <-done:
		if !resp.Approved || resp.Action != ConfirmActionOnce {
			t.Fatalf("response = %+v, want approved once", resp)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for confirm response")
	}

	if IsConfirmAllowed(sessionID, req) {
		t.Fatal("allow once should not store a session rule")
	}
}

func TestSetSessionPermissionLevelAutoApprovesPendingConfirm(t *testing.T) {
	sessionID := "confirm-permission-" + time.Now().Format("150405.000000000")
	req := ConfirmRequest{
		ToolName:  "write_file",
		Args:      `{"path":"x"}`,
		RiskLevel: "high",
	}
	t.Cleanup(func() { SetSessionPermissionLevel(sessionID, "") })

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	done := make(chan ConfirmResponse, 1)
	go func() {
		resp, err := WaitForConfirmResponse(ctx, sessionID, req)
		if err != nil {
			t.Errorf("WaitForConfirmResponse returned error: %v", err)
			return
		}
		done <- resp
	}()

	time.Sleep(10 * time.Millisecond)
	SetSessionPermissionLevel(sessionID, PermissionAuto)

	select {
	case resp := <-done:
		if !resp.Approved || resp.Action != ConfirmActionOnce {
			t.Fatalf("response = %+v, want approved once", resp)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for auto approval")
	}
}

func TestWaitForConfirmIgnoresShortToolDeadline(t *testing.T) {
	sessionID := "confirm-interactive-" + time.Now().Format("150405.000000000")
	req := ConfirmRequest{ToolName: "write_file", Args: `{"path":"x"}`, RiskLevel: "high"}

	toolCtx, toolCancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer toolCancel()
	turnCtx, turnCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer turnCancel()
	ctx := WithInteractiveContext(toolCtx, turnCtx)

	done := make(chan error, 1)
	go func() {
		_, err := WaitForConfirm(ctx, sessionID, req)
		done <- err
	}()
	time.Sleep(120 * time.Millisecond)
	if !SubmitConfirm(sessionID, true) {
		t.Fatal("SubmitConfirm returned false after tool deadline")
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("confirm failed after short tool deadline: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for confirm")
	}
}
