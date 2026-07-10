package subagent

import (
	"context"
	"testing"

	"github.com/p-chat/pchat/internal/style"
	"github.com/p-chat/pchat/internal/tool"
)

// TestBuildSubAgentChatRequest_PropagatesProjectRoot is the
// regression test for the 2026-07 "tool runs in the wrong folder"
// bug. The sub-agent runner was constructing a ChatRequest without
// setting ProjectRoot, so the child's tool calls (exec_command,
// read_file, write_file) resolved relative paths against the server
// startup CWD instead of the user's selected project directory.
func TestBuildSubAgentChatRequest_PropagatesProjectRoot(t *testing.T) {
	req := Request{
		Description: "explore src/",
		ProjectRoot: "D:\\projects\\myapp",
		TaskID:      "t1",
	}
	cr := buildSubAgentChatRequest(req, style.Tech, "cs", "mimo-v2.5", "you are an explore agent", "explore", "#5AAE5A")
	if cr.ProjectRoot != req.ProjectRoot {
		t.Fatalf("ProjectRoot = %q, want %q", cr.ProjectRoot, req.ProjectRoot)
	}
}

// TestTool_ProjectRootFromCtx_Roundtrip ensures the exported
// accessor added for sub-agent consumption reads back what
// WithProjectRoot wrote, so the task handler can pull the
// session's working directory out of ctx at dispatch time.
func TestTool_ProjectRootFromCtx_Roundtrip(t *testing.T) {
	ctx := tool.WithProjectRoot(context.Background(), "D:\\projects\\myapp")
	if got := tool.ProjectRootFromCtx(ctx); got != "D:\\projects\\myapp" {
		t.Fatalf("ProjectRootFromCtx = %q, want %q", got, "D:\\projects\\myapp")
	}
	// Empty ctx -> empty string.
	if got := tool.ProjectRootFromCtx(context.Background()); got != "" {
		t.Fatalf("ProjectRootFromCtx on empty ctx = %q, want \"\"", got)
	}
	// WithProjectRoot on empty root is a no-op (must not write "").
	ctx2 := tool.WithProjectRoot(context.Background(), "")
	if got := tool.ProjectRootFromCtx(ctx2); got != "" {
		t.Fatalf("WithProjectRoot(\"\") should not write, got %q", got)
	}
}
