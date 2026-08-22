package server

import (
	"testing"
)

// TestMergeAssistantRun_DoesNotRegenerateContent is the
// bug #5 regression test. The old code did
//
//	contents := []string{}
//	for _, p := range parts {
//	    if p.Kind == "text" { contents = append(contents, p.Text) }
//	}
//	base.Content = strings.Join(contents, "\n")
//
// which re-projected the multi-round text into a single
// string. This was dead: MessageBubble.vue renders
// assistant messages off `message.parts` exclusively
// (line 601-623), never reading `message.content`. And it
// was wrong: `\n`-joining rounds diverged from the live
// streaming view, where each round is its own styled text
// part. The fix drops the projection and lets `base`
// inherit run[0].Content as-is (the shallow copy at
// `base := run[0]` does that automatically).
func TestMergeAssistantRun_DoesNotRegenerateContent(t *testing.T) {
	// Two consecutive assistant messages, each with its
	// own text part and content. The live-streaming view
	// renders both as separate parts. The reload-merged
	// view should do the same.
	run := []MessageResponse{
		{
			Role:    "assistant",
			Content: "round 1 text",
			Parts: []MessagePart{
				{Kind: "text", Text: "round 1 text"},
				{Kind: "thinking", Text: "thinking 1"},
			},
		},
		{
			Role:    "assistant",
			Content: "round 2 text",
			Parts: []MessagePart{
				{Kind: "text", Text: "round 2 text"},
			},
		},
	}
	got := mergeAssistantRun(run)
	if got.Content != "round 1 text" {
		t.Errorf("Content = %q, want %q (must NOT be re-projected; round 1's text only)",
			got.Content, "round 1 text")
	}
	// The joined-Content antipattern would have produced
	// "round 1 text\nround 2 text" (or the merged-text
	// version after part concatenation). Asserting the
	// absence of that string defends against re-introducing
	// the bug.
	if got.Content == "round 1 text\nround 2 text" {
		t.Errorf("Content = %q, was being \\n-joined across rounds (regression of bug #5)",
			got.Content)
	}
	// Parts preserved in order. Round 1 has [text, thinking]
	// and round 2 has [text]; the consecutive-merge logic
	// only kicks in when same-kind parts sit next to each
	// other, so the merged run is [text, thinking, text]
	// — the round-2 text is NOT joined onto the round-1
	// text because thinking is in between. Each round's
	// prose keeps its own text part.
	if len(got.Parts) != 3 {
		t.Fatalf("Parts len = %d, want 3 (text, thinking, text — round 2's text can't merge across thinking)", len(got.Parts))
	}
	if got.Parts[0].Kind != "text" || got.Parts[0].Text != "round 1 text" {
		t.Errorf("Parts[0] = %+v, want text \"round 1 text\"", got.Parts[0])
	}
	if got.Parts[1].Kind != "thinking" || got.Parts[1].Text != "thinking 1" {
		t.Errorf("Parts[1] = %+v, want thinking \"thinking 1\"", got.Parts[1])
	}
	if got.Parts[2].Kind != "text" || got.Parts[2].Text != "round 2 text" {
		t.Errorf("Parts[2] = %+v, want text \"round 2 text\"", got.Parts[2])
	}
}

// TestMergeAssistantRun_SingleMessage is a no-op fast path;
// the run of length 1 should pass through unchanged.
func TestMergeAssistantRun_SingleMessage(t *testing.T) {
	run := []MessageResponse{
		{
			Role:    "assistant",
			Content: "only round",
			Parts:   []MessagePart{{Kind: "text", Text: "only round"}},
		},
	}
	got := mergeAssistantRun(run)
	if got.Content != "only round" {
		t.Errorf("Content = %q, want %q", got.Content, "only round")
	}
	if len(got.Parts) != 1 {
		t.Errorf("Parts len = %d, want 1", len(got.Parts))
	}
}

func TestMergeAssistantRun_AsyncSubAgentsFollowTaskToolsInCallOrder(t *testing.T) {
	run := []MessageResponse{
		{
			Role: "assistant",
			Parts: []MessagePart{
				{Kind: "sub_agent", Task: "second background task", TaskID: "task-b", RunMode: "async"},
				{Kind: "sub_agent", Task: "first background task", TaskID: "task-a", RunMode: "async"},
			},
		},
		{
			Role: "assistant",
			Parts: []MessagePart{
				{Kind: "thinking", Text: "decide to launch tasks"},
				{Kind: "tool", Name: "task", Status: "ok", Args: `{"mode":"async","description":"first background task"}`, Result: "sub-agent launched in background: task_id=task-a, status=running"},
				{Kind: "tool", Name: "task", Status: "ok", Args: `{"mode":"async","description":"second background task"}`, Result: "sub-agent launched in background: task_id=task-b, status=running"},
				{Kind: "thinking", Text: "wait for one result"},
				{Kind: "tool", Name: "task_wait", Status: "ok", Result: "task-a completed"},
			},
		},
	}

	got := mergeAssistantRun(run)
	kinds := make([]string, 0, len(got.Parts))
	for _, p := range got.Parts {
		if p.Kind == "tool" {
			kinds = append(kinds, "tool:"+p.Name)
			continue
		}
		if p.Kind == "sub_agent" {
			kinds = append(kinds, "sub:"+p.TaskID)
			continue
		}
		kinds = append(kinds, p.Kind)
	}
	want := []string{
		"thinking",
		"tool:task",
		"tool:task",
		"sub:task-a",
		"sub:task-b",
		"thinking",
		"tool:task_wait",
	}
	if len(kinds) != len(want) {
		t.Fatalf("parts order len = %d (%v), want %d (%v)", len(kinds), kinds, len(want), want)
	}
	for i := range want {
		if kinds[i] != want[i] {
			t.Fatalf("parts order = %v, want %v", kinds, want)
		}
	}
}
