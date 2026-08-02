package agent

import (
	"testing"

	"github.com/p-chat/pchat/internal/llm"
)

func TestToolDefsForPhase(t *testing.T) {
	defs := []llm.ToolDef{{Name: "read_file"}, {Name: "todo_write"}, {Name: "question"}}
	if got := toolDefsForPhase(defs, ToolPhaseWork); len(got) != len(defs) {
		t.Fatalf("work phase removed tools: %#v", got)
	}
	got := toolDefsForPhase(defs, ToolPhaseCheckpoint)
	if len(got) != 2 || got[0].Name != "todo_write" || got[1].Name != "question" {
		t.Fatalf("checkpoint tools = %#v", got)
	}
	if toolAllowedInPhase("write_file", ToolPhaseCheckpoint) || !toolAllowedInPhase("todo_write", ToolPhaseCheckpoint) {
		t.Fatal("checkpoint allowlist is incorrect")
	}
}

func TestToolPhaseForRound(t *testing.T) {
	defs := []llm.ToolDef{{Name: "read_file"}, {Name: "todo_write"}, {Name: "question"}}
	if got := toolPhaseForRound(false, todoCheckpointNone); got != ToolPhaseWork {
		t.Fatalf("work phase = %q", got)
	}
	if got := toolPhaseForRound(false, todoCheckpointReview); got != ToolPhaseCheckpoint {
		t.Fatalf("checkpoint phase = %q", got)
	}
	if got := toolPhaseForRound(true, todoCheckpointReview); got != ToolPhasePlan {
		t.Fatalf("plan phase should take precedence, got %q", got)
	}
	if got := toolPhaseForRound(false, todoCheckpointReconcile); got != ToolPhaseReconcile {
		t.Fatalf("reconcile phase = %q", got)
	}
	if got := toolDefsForPhase(defs, ToolPhaseReconcile); len(got) != 1 || got[0].Name != "todo_write" {
		t.Fatalf("reconcile tools = %#v", got)
	}
	if toolAllowedInPhase("question", ToolPhaseReconcile) || !toolAllowedInPhase("todo_write", ToolPhaseReconcile) {
		t.Fatal("reconcile allowlist is incorrect")
	}
}
