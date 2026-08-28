package cli

import (
	"strings"
	"testing"

	"github.com/p-chat/pchat/internal/agent"
)

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
