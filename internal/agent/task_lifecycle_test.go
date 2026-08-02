package agent

import "testing"

func TestAdvanceTodoCheckpointRequiresTodoWriteAfterQuestion(t *testing.T) {
	next, advanced := advanceTodoCheckpoint(todoCheckpointReview, false, true)
	if !advanced || next != todoCheckpointReconcile {
		t.Fatalf("question answer advanced to (%v, %t), want reconcile/true", next, advanced)
	}
	next, advanced = advanceTodoCheckpoint(todoCheckpointReconcile, false, true)
	if advanced || next != todoCheckpointReconcile {
		t.Fatalf("question in reconcile advanced to (%v, %t), want reconcile/false", next, advanced)
	}
	next, advanced = advanceTodoCheckpoint(todoCheckpointReconcile, true, false)
	if !advanced || next != todoCheckpointNone {
		t.Fatalf("todo_write advanced to (%v, %t), want none/true", next, advanced)
	}
}
