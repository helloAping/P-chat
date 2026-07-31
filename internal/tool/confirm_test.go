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
