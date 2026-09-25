package tool

import (
	"testing"
	"time"
)

func TestSubagentMayExpose(t *testing.T) {
	allow := []string{
		"read_file", "list_files", "read_docx", "read_pdf", "grep",
		"wiki_lookup", "wiki_list", "media_recognize", "todo_write", "web_search", "web_fetch",
	}
	for _, name := range allow {
		if !SubagentMayExpose(name) {
			t.Errorf("SubagentMayExpose(%q) = false, want true", name)
		}
	}
	deny := []string{
		"write_file", "edit_file", "exec_command", "start_process",
		"question", "browser_click", "task",
		"mcp__demo__tool__00000000", "custom_dynamic",
	}
	for _, name := range deny {
		if SubagentMayExpose(name) {
			t.Errorf("SubagentMayExpose(%q) = true, want false", name)
		}
	}
}

func TestEffectivePolicyClassifiesBuiltins(t *testing.T) {
	read := (Tool{Name: "read_file"}).EffectivePolicy()
	if !read.CanRunInParallel() || read.Risk != ToolRiskLow {
		t.Fatalf("read_file policy = %#v, want parallel low-risk read", read)
	}
	edit := (Tool{Name: "edit_file"}).EffectivePolicy()
	if edit.CanRunInParallel() || edit.Category != ToolCategoryMutate || !edit.RequiresVerification {
		t.Fatalf("edit_file policy = %#v, want exclusive verified mutation", edit)
	}
	checkpoint := (Tool{Name: "todo_write"}).EffectivePolicy()
	if checkpoint.Category != ToolCategoryCheckpoint || !checkpoint.Idempotent {
		t.Fatalf("todo_write policy = %#v, want idempotent checkpoint", checkpoint)
	}
	task := (Tool{Name: "task"}).EffectivePolicy()
	if task.Timeout() != 0 {
		t.Fatalf("task timeout = %v, want no per-tool deadline", task.Timeout())
	}
	if !task.CanRunInParallel() || task.Category != ToolCategoryOrchestration {
		t.Fatalf("task policy = %#v, want parallel orchestration", task)
	}
}

func TestEffectivePolicyTimeoutsMatchSharedBudgets(t *testing.T) {
	cases := []struct {
		name string
		want time.Duration
	}{
		{"read_file", ReadToolTimeout},
		{"write_file", WriteToolTimeout},
		{"exec_command", DefaultToolTimeout},
		{"todo_write", CheckpointToolTimeout},
		{"question", QuestionWaitTimeout},
		{"web_search", WebSearchTimeout},
		{"web_fetch", WebFetchTimeout},
		{"browser_click", WebFetchTimeout},
		{"unknown_custom", DefaultToolTimeout},
	}
	for _, tc := range cases {
		got := (Tool{Name: tc.name}).EffectivePolicy().Timeout()
		if got != tc.want {
			t.Errorf("%s timeout = %s, want %s", tc.name, got, tc.want)
		}
	}
	if WebSearchTimeout != 60*time.Second {
		t.Fatalf("WebSearchTimeout = %s, want 60s to match search.pickTimeout cap", WebSearchTimeout)
	}
}

func TestCallResultNormalizePreservesLegacyFields(t *testing.T) {
	result := &CallResult{Content: "blocked by policy", Status: CallStatusBlocked}
	result.Normalize()
	if !result.IsError || result.Summary != result.Content {
		t.Fatalf("normalized result = %#v, want error and summary", result)
	}
	waiting := &CallResult{Content: "answer required", RequiresUser: true}
	waiting.Normalize()
	if waiting.Status != CallStatusWaiting {
		t.Fatalf("waiting result status = %q", waiting.Status)
	}
}
