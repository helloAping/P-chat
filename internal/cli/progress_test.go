package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/p-chat/pchat/internal/agent"
)

func TestChatUIBeginTurnDoesNotRepeatVisibleInput(t *testing.T) {
	const input = "唯一用户输入-7f12"
	var output bytes.Buffer
	ui := NewChatUI("test", "test-model")
	ui.turnHeaderOutput = &output

	// TTY 输入组件已经把用户正文留在终端；开始响应时只能输出分隔，不能再复制正文。
	// The TTY input component already left the user text on screen; beginning
	// the response may add separation, but must not print the text again.
	ui.BeginTurn(input, false)
	if count := strings.Count(output.String(), input); count != 0 {
		t.Fatalf("BeginTurn repeated an already-visible input %d time(s): %q", count, output.String())
	}
}

func TestChatUIBeginTurnPrintsSyntheticInputOnce(t *testing.T) {
	const input = "队列自动发送-4c83"
	var output bytes.Buffer
	ui := NewChatUI("test", "test-model")
	ui.turnHeaderOutput = &output

	ui.BeginTurn(input, true)
	if count := strings.Count(output.String(), input); count != 1 {
		t.Fatalf("BeginTurn printed a synthetic input %d time(s), want 1: %q", count, output.String())
	}
}

func TestToolArgsDryRun(t *testing.T) {
	if !toolArgsDryRun(`{"dry_run":true,"path":"a.txt"}`) {
		t.Fatal("dry_run=true was not detected")
	}
	for _, input := range []string{"", "null", `{"dry_run":false}`, `not-json`} {
		if toolArgsDryRun(input) {
			t.Fatalf("toolArgsDryRun(%q) = true, want false", input)
		}
	}
}

func TestToolDisplayTextPrefersStructuredSummary(t *testing.T) {
	got := toolDisplayText(agent.ChatStreamChunk{
		ToolSummary:    "summary",
		ToolResult:     "preview",
		ToolResultFull: "full",
	})
	if got != "summary" {
		t.Fatalf("display text = %q, want summary", got)
	}
}

func TestToolResultExpandable(t *testing.T) {
	if !toolResultExpandable(agent.ChatStreamChunk{ToolResultTruncated: true}) {
		t.Fatal("truncated result should be expandable")
	}
	if !toolResultExpandable(agent.ChatStreamChunk{ToolResultFull: strings.Repeat("x", 201)}) {
		t.Fatal("long full result should be expandable")
	}
	if toolResultExpandable(agent.ChatStreamChunk{ToolResult: "short"}) {
		t.Fatal("short result should not be expandable")
	}
}
