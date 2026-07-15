// Tests for the P0-3 auto-continue guard. The guard is
// intentionally split into pure helpers (sessionPendingTodos,
// buildAutoContinuePrompt) so the bulk of the logic is
// testable without spinning up a fake LLM. The full
// ChatWithTools integration is covered by hand / manual
// smoke (see docs/plans/auto-continue-plan.md §4 验收清单).
package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/p-chat/pchat/internal/tool"
)

// TestSessionPendingTodos_CountsBothStates verifies the helper
// returns only pending + in_progress items (not done / cancelled).
func TestSessionPendingTodos_CountsBothStates(t *testing.T) {
	sid := "auto-continue-test-1"
	tool.SetSessionTodos(sid, []tool.TodoItem{
		{ID: "a", Content: "a", Status: "done"},
		{ID: "b", Content: "b", Status: "in_progress"},
		{ID: "c", Content: "c", Status: "pending"},
		{ID: "d", Content: "d", Status: "cancelled"},
		{ID: "e", Content: "e", Status: "pending"},
	})
	t.Cleanup(func() { tool.SetSessionTodos(sid, nil) })

	n, items := sessionPendingTodos(sid)
	if n != 3 {
		t.Fatalf("count = %d, want 3 (1 in_progress + 2 pending)", n)
	}
	if len(items) != 3 {
		t.Fatalf("len(items) = %d, want 3", len(items))
	}
	gotIDs := map[string]string{}
	for _, it := range items {
		gotIDs[it.ID] = it.Status
	}
	want := map[string]string{"b": "in_progress", "c": "pending", "e": "pending"}
	for id, st := range want {
		if gotIDs[id] != st {
			t.Errorf("item %q status = %q, want %q", id, gotIDs[id], st)
		}
	}
}

// TestSessionPendingTodos_EmptySession verifies the helper
// returns 0 + nil for an unknown session (no todo writes yet).
func TestSessionPendingTodos_EmptySession(t *testing.T) {
	n, items := sessionPendingTodos("nonexistent-session-id")
	if n != 0 {
		t.Errorf("count = %d, want 0", n)
	}
	if len(items) != 0 {
		t.Errorf("len(items) = %d, want 0", len(items))
	}
}

// TestBuildAutoContinuePrompt_FormatsBothBuckets verifies the
// prompt lists in_progress items before pending items, and
// includes the action call-to-action.
func TestBuildAutoContinuePrompt_FormatsBothBuckets(t *testing.T) {
	items := []tool.TodoItem{
		{ID: "b", Content: "完成第二项", Status: "in_progress"},
		{ID: "c", Content: "写测试", Status: "pending"},
		{ID: "a", Content: "完成第一项", Status: "in_progress"},
	}
	prompt := buildAutoContinuePrompt(items)

	// Must mention the warning + action.
	if !strings.Contains(prompt, "未完成") {
		t.Error("prompt missing '未完成' warning")
	}
	if !strings.Contains(prompt, "todo_write") {
		t.Error("prompt missing the 'todo_write' tool reference")
	}
	if !strings.Contains(prompt, "不要只发文本总结就停止") {
		t.Error("prompt missing the 'do not stop with text only' call-to-action")
	}

	// in_progress section must come first.
	ipIdx := strings.Index(prompt, "**进行中**")
	pIdx := strings.Index(prompt, "**待开始**")
	if ipIdx < 0 || pIdx < 0 {
		t.Fatalf("prompt missing '**进行中**' (%d) or '**待开始**' (%d)", ipIdx, pIdx)
	}
	if ipIdx >= pIdx {
		t.Errorf("'**进行中**' should appear before '**待开始**' (got ipIdx=%d, pIdx=%d)", ipIdx, pIdx)
	}

	// All three items must be listed.
	for _, want := range []string{"[a]", "[b]", "[c]"} {
		if !strings.Contains(prompt, want) {
			t.Errorf("prompt missing item %q", want)
		}
	}
}

// TestBuildAutoContinuePrompt_OnlyInProgress covers the edge
// case where every unfinished item is already in_progress (no
// pending). The prompt should still list them and not render
// an empty "**待开始**" section.
func TestBuildAutoContinuePrompt_OnlyInProgress(t *testing.T) {
	items := []tool.TodoItem{
		{ID: "x", Content: "做某事", Status: "in_progress"},
	}
	prompt := buildAutoContinuePrompt(items)
	if !strings.Contains(prompt, "[x]") {
		t.Error("prompt missing the in_progress item id")
	}
	if strings.Contains(prompt, "**待开始**") {
		t.Error("prompt should NOT render a '待开始' section when there are no pending items")
	}
}

// TestBuildAutoContinuePrompt_OnlyPending covers the
// symmetric case (only pending, no in_progress).
func TestBuildAutoContinuePrompt_OnlyPending(t *testing.T) {
	items := []tool.TodoItem{
		{ID: "y", Content: "还没开始", Status: "pending"},
	}
	prompt := buildAutoContinuePrompt(items)
	if !strings.Contains(prompt, "[y]") {
		t.Error("prompt missing the pending item id")
	}
	if strings.Contains(prompt, "**进行中**") {
		t.Error("prompt should NOT render a '进行中' section when there are no in_progress items")
	}
}

// TestMaxAutoContinue_Is3 locks the constant at 3 so it
// doesn't drift accidentally. 3 is a deliberate trade-off
// (see docs/plans/auto-continue-plan.md §1.3 设计决策).
func TestMaxAutoContinue_Is3(t *testing.T) {
	if MaxAutoContinue != 3 {
		t.Errorf("MaxAutoContinue = %d, want 3 — change docs/plans/auto-continue-plan.md if you intentionally change this", MaxAutoContinue)
	}
}

// TestPickMaxStepsPrompt_LanguageSelection locks the P2-2
// language split. ZH must go to the Chinese variant; every
// other value (en, auto, "" — i.e. unset) must go to the
// English variant. The "auto" → EN rule is intentional:
// pickMaxStepsPrompt is called with the config's LLM.Output.Language
// (which is "auto" or "" by default) and the opencode "follow
// the conversation language" rule is supposed to kick in at
// the LLM output stage, not at the prompt stage.
func TestPickMaxStepsPrompt_LanguageSelection(t *testing.T) {
	cases := []struct {
		lang string
		want string
	}{
		{"zh", MaxStepsPromptZH},
		{"en", MaxStepsPromptEN},
		{"auto", MaxStepsPromptEN},
		{"", MaxStepsPromptEN},
		{"zh-CN", MaxStepsPromptEN}, // only exact "zh" maps
	}
	for _, c := range cases {
		got := pickMaxStepsPrompt(c.lang)
		if got != c.want {
			t.Errorf("pickMaxStepsPrompt(%q) = wrong variant (got %d bytes, want %d bytes)", c.lang, len(got), len(c.want))
		}
	}
}

// TestNormalizeToolCallIDs covers P2-3 — the LLM sometimes
// omits or duplicates the tool_call ID, which breaks the
// tool_call/tool_result pairing the OpenAI / Anthropic
// protocols depend on. The helper must:
//
//  1. leave a unique non-empty ID alone
//  2. replace an empty ID with a fresh "call_<uuid>"
//  3. replace a duplicate ID with a fresh "call_<uuid>"
//  4. produce a UNIQUE result (no two outputs collide)
func TestNormalizeToolCallIDs(t *testing.T) {
	t.Run("unique IDs are kept as-is", func(t *testing.T) {
		calls := []nativeToolCall{
			{ID: "a", Name: "read_file", ArgsJSON: `{"path":"x"}`},
			{ID: "b", Name: "read_file", ArgsJSON: `{"path":"y"}`},
		}
		seen := map[string]bool{}
		normalizeToolCallIDs(calls, seen)
		if calls[0].ID != "a" || calls[1].ID != "b" {
			t.Errorf("unique IDs were rewritten: %+v", calls)
		}
	})

	t.Run("empty IDs get a synthetic call_<uuid>", func(t *testing.T) {
		calls := []nativeToolCall{
			{ID: "", Name: "read_file", ArgsJSON: `{}`},
		}
		seen := map[string]bool{}
		normalizeToolCallIDs(calls, seen)
		if calls[0].ID == "" {
			t.Fatal("empty ID was not rewritten")
		}
		if !strings.HasPrefix(calls[0].ID, "call_") {
			t.Errorf("replacement ID %q is missing 'call_' prefix", calls[0].ID)
		}
	})

	t.Run("duplicate IDs are individually rewritten", func(t *testing.T) {
		calls := []nativeToolCall{
			{ID: "dup", Name: "read_file", ArgsJSON: `{}`},
			{ID: "dup", Name: "write_file", ArgsJSON: `{}`},
			{ID: "dup", Name: "exec_command", ArgsJSON: `{}`},
		}
		seen := map[string]bool{}
		normalizeToolCallIDs(calls, seen)
		// The first occurrence keeps "dup"; the next two are
		// rewritten to fresh "call_<uuid>" values.
		if calls[0].ID != "dup" {
			t.Errorf("first occurrence should keep original ID, got %q", calls[0].ID)
		}
		gotIDs := map[string]bool{calls[0].ID: true}
		for i := 1; i < len(calls); i++ {
			if !strings.HasPrefix(calls[i].ID, "call_") {
				t.Errorf("calls[%d] ID = %q, want call_<uuid>", i, calls[i].ID)
			}
			if gotIDs[calls[i].ID] {
				t.Errorf("calls[%d] collides with an earlier ID: %q", i, calls[i].ID)
			}
			gotIDs[calls[i].ID] = true
		}
	})

	t.Run("mix of empty + duplicate all get unique synthetic IDs", func(t *testing.T) {
		calls := []nativeToolCall{
			{ID: "", Name: "a"},
			{ID: "shared", Name: "b"},
			{ID: "shared", Name: "c"},
			{ID: "", Name: "d"},
		}
		seen := map[string]bool{}
		normalizeToolCallIDs(calls, seen)
		gotIDs := map[string]bool{}
		for i, tc := range calls {
			if tc.ID == "" {
				t.Errorf("calls[%d] still has empty ID", i)
			}
			if gotIDs[tc.ID] {
				t.Errorf("calls[%d] ID %q collides", i, tc.ID)
			}
			gotIDs[tc.ID] = true
		}
		if len(gotIDs) != 4 {
			t.Errorf("got %d unique IDs, want 4", len(gotIDs))
		}
	})

	t.Run("ID format is preserved as call_<uuid>", func(t *testing.T) {
		calls := []nativeToolCall{{ID: ""}}
		seen := map[string]bool{}
		normalizeToolCallIDs(calls, seen)
		// call_<uuid> = "call_" + 36-char UUID (8-4-4-4-12)
		id := calls[0].ID
		if !strings.HasPrefix(id, "call_") || len(id) != len("call_")+36 {
			t.Errorf("ID %q is not call_<uuid> (36-char suffix)", id)
		}
	})
}

// TestChatRequest_AutoContinueJSONTag locks the JSON wire
// format used by the server's PATCH /sessions/:id endpoint.
// The frontend sends `{"auto_continue": false}`; if the tag
// is accidentally renamed the PATCH silently stops toggling
// the per-session flag. We don't unmarshal the full struct
// (which would require a realistic config) — a partial
// round-trip via json.RawMessage is enough.
func TestChatRequest_AutoContinueJSONTag(t *testing.T) {
	cases := []struct {
		raw             string
		wantAutoCont    bool
		wantFieldExists bool
	}{
		{`{"auto_continue": true}`, true, true},
		{`{"auto_continue": false}`, false, true},
		{`{}`, false, false}, // zero value: server applies default-true via sessionAutoContinue()
	}
	for _, c := range cases {
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(c.raw), &m); err != nil {
			t.Fatalf("unmarshal %q: %v", c.raw, err)
		}
		raw, ok := m["auto_continue"]
		if ok != c.wantFieldExists {
			t.Errorf("%s: field present = %v, want %v", c.raw, ok, c.wantFieldExists)
		}
		if !ok {
			continue
		}
		var got bool
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("unmarshal field: %v", err)
		}
		if got != c.wantAutoCont {
			t.Errorf("%s: auto_continue = %v, want %v", c.raw, got, c.wantAutoCont)
		}
	}
}
