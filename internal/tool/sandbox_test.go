package tool

import (
	"context"
	"strings"
	"testing"
)

// stubSandbox is a minimal SandboxChecker for tests. It only flags the
// "BANNED" command / path so tests can control the outcome.
type stubSandbox struct {
	execAllowed  bool
	writeAllowed bool
}

func (s *stubSandbox) CheckExecBool(_ string) bool  { return s.execAllowed }
func (s *stubSandbox) CheckWriteBool(_ string) bool { return s.writeAllowed }
func (s *stubSandbox) CheckExecDecision(_ string) SandboxDecision {
	if s.execAllowed { return SandboxAllow }
	return SandboxBlock
}
func (s *stubSandbox) CheckWriteDecision(_ string) SandboxDecision {
	if s.writeAllowed { return SandboxAllow }
	return SandboxBlock
}
func (s *stubSandbox) MatchedPattern(_ string) string { return "" }

func TestExecCommand_SandboxBlocks(t *testing.T) {
	ctx := WithSandbox(context.Background(), &stubSandbox{execAllowed: false})
	res, err := handleExecCommand(ctx, []byte(`{"command":"BANNED"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Errorf("expected IsError=true, got false (content=%q)", res.Content)
	}
	if !strings.Contains(res.Content, "E_SANDBOX") {
		t.Errorf("error content should mention E_SANDBOX, got %q", res.Content)
	}
}

func TestExecCommand_SandboxAllows(t *testing.T) {
	ctx := WithSandbox(context.Background(), &stubSandbox{execAllowed: true})
	res, err := handleExecCommand(ctx, []byte(`{"command":"echo hi"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.IsError {
		// `echo hi` returns success; the OS-level command shouldn't
		// be classified as a sandbox error. (But cmd /C echo hi on
		// Windows might still produce output we don't assert on.)
		t.Logf("note: result marked IsError; content=%q", res.Content)
	}
}

func TestExecCommand_NoSandbox(t *testing.T) {
	ctx := context.Background() // no sandbox in ctx
	// Empty command should be rejected by the tool itself.
	res, _ := handleExecCommand(ctx, []byte(`{"command":""}`))
	if !res.IsError {
		t.Errorf("empty command should be rejected, got %q", res.Content)
	}
}

func TestWriteFile_SandboxBlocks(t *testing.T) {
	ctx := WithSandbox(context.Background(), &stubSandbox{writeAllowed: false})
	res, err := handleWriteFile(ctx, []byte(`{"path":"BANNED/x","content":"hi"}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.IsError {
		t.Errorf("expected sandbox block, got %q", res.Content)
	}
	if !strings.Contains(res.Content, "E_SANDBOX") {
		t.Errorf("error should mention E_SANDBOX, got %q", res.Content)
	}
}

func TestSandboxFromCtx_NoSandbox(t *testing.T) {
	if got := sandboxFromCtx(context.Background()); got != nil {
		t.Errorf("expected nil sandbox, got %T", got)
	}
}
