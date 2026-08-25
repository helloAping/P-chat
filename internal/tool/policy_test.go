package tool

import (
	"testing"
	"time"
)

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
