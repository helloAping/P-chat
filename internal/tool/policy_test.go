package tool

import "testing"

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
