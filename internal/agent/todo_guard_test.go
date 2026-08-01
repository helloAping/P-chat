package agent

import (
	"strings"
	"testing"

	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/tool"
)

func TestNormalizeTodoMode(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want TodoMode
	}{
		{name: "empty", in: "", want: TodoModeAuto},
		{name: "unknown", in: "discard", want: TodoModeAuto},
		{name: "case and space", in: " RESUME ", want: TodoModeResume},
		{name: "clear", in: "clear", want: TodoModeClear},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := NormalizeTodoMode(tt.in); got != tt.want {
				t.Fatalf("NormalizeTodoMode(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestUpsertTodoGuardReplacesInPlace(t *testing.T) {
	msgs := []llm.ChatMessage{{Role: llm.RoleSystem, Content: "base"}}
	items := []tool.TodoItem{{ID: "a", Content: "first", Status: "pending"}}

	upsertTodoGuard(&msgs, TodoModeAuto, items, false)
	firstLen := len(msgs[0].Content)
	if strings.Count(msgs[0].Content, todoGuardStart) != 1 {
		t.Fatalf("first guard marker count = %d, want 1", strings.Count(msgs[0].Content, todoGuardStart))
	}

	items[0].Status = "in_progress"
	upsertTodoGuard(&msgs, TodoModeAuto, items, true)
	if got := strings.Count(msgs[0].Content, todoGuardStart); got != 1 {
		t.Fatalf("replacement guard marker count = %d, want 1", got)
	}
	if got := strings.Count(msgs[0].Content, todoGuardEnd); got != 1 {
		t.Fatalf("replacement guard end marker count = %d, want 1", got)
	}
	if !strings.Contains(msgs[0].Content, "in_progress") || !strings.Contains(msgs[0].Content, "状态检查") {
		t.Fatalf("replacement guard did not contain current state: %q", msgs[0].Content)
	}
	if len(msgs[0].Content) > firstLen+len(todoGuardStart)+len(todoGuardEnd)+256 {
		t.Fatalf("guard grew unexpectedly: first=%d current=%d", firstLen, len(msgs[0].Content))
	}
}

func TestTodoOnlyToolDefs(t *testing.T) {
	defs := []llm.ToolDef{
		{Name: "read_file"},
		{Name: "todo_write"},
		{Name: "question"},
		{Name: "exec_command"},
	}
	got := todoOnlyToolDefs(defs)
	if len(got) != 2 || got[0].Name != "todo_write" || got[1].Name != "question" {
		t.Fatalf("todoOnlyToolDefs() = %#v, want todo_write/question", got)
	}
	if defs[0].Name != "read_file" || len(defs) != 4 {
		t.Fatal("todoOnlyToolDefs mutated the input slice")
	}
	if !isTodoCheckpointTool("todo_write") || !isTodoCheckpointTool("question") || isTodoCheckpointTool("exec_command") {
		t.Fatal("checkpoint tool allowlist is incorrect")
	}
}

func TestBuildTodoGuardPromptPrioritizesInProgress(t *testing.T) {
	items := []tool.TodoItem{
		{ID: "pending", Content: "later", Status: "pending"},
		{ID: "active", Content: "now", Status: "in_progress"},
	}
	prompt := buildTodoGuardPrompt(TodoModeResume, items, false)
	if !strings.Contains(prompt, "[active]") || !strings.Contains(prompt, "[pending]") {
		t.Fatalf("guard prompt omitted todo items: %q", prompt)
	}
	if strings.Index(prompt, "[active]") > strings.Index(prompt, "[pending]") {
		t.Fatalf("in_progress item was not listed first: %q", prompt)
	}
}
