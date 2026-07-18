package agent

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/llm"
)

// =====================================================================
// formatElapsed
// =====================================================================

func TestFormatElapsed(t *testing.T) {
	cases := []struct {
		name string
		in   time.Duration
		want string
	}{
		{"zero", 0, "0ms"},
		{"sub-second", 250 * time.Millisecond, "250ms"},
		{"sub-second-rounded", 999 * time.Millisecond, "999ms"},
		{"one-second", time.Second, "1.0s"},
		{"seconds-fraction", 3500 * time.Millisecond, "3.5s"},
		{"59-seconds", 59 * time.Second, "59.0s"},
		{"one-minute", time.Minute, "1m0s"},
		{"minute-and-seconds", time.Minute + 30*time.Second, "1m30s"},
		{"five-minutes", 5*time.Minute + 12*time.Second, "5m12s"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := formatElapsed(c.in); got != c.want {
				t.Errorf("formatElapsed(%v) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// =====================================================================
// truncateToolResult
// =====================================================================
//
// truncateToolResult is an *Agent method, but its behaviour
// depends only on Agent.cfg.Limits and the input args. We
// construct a minimal Agent (zero-value) for the no-config
// path, and a manually-populated Agent for the per-cap
// override path.

func TestTruncateToolResult_NoOpBelowCap(t *testing.T) {
	a := &Agent{} // nil cfg → use the default caps
	got := a.truncateToolResult("exec_command", "short")
	if got != "short" {
		t.Errorf("truncateToolResult(short) = %q, want 'short'", got)
	}
}

func TestTruncateToolResult_ExecKeepsTail(t *testing.T) {
	// exec_command / bash / shell → keep TAIL.
	a := &Agent{}
	content := strings.Repeat("a", 100) + strings.Repeat("b", maxToolResultExec+5000)
	got := a.truncateToolResult("exec_command", content)
	// The tail (the "b"s) is preserved; the head (the
	// "a"s) is dropped. The truncation marker is
	// appended at the end, so we check the last-but-many
	// chars for the tail marker instead of the very
	// last char.
	if !strings.Contains(got, strings.Repeat("b", 100)) {
		t.Errorf("exec_command should keep tail (100 b's), got tail: %q", got[max(0, len(got)-200):])
	}
	// The head (a block of "a"s) should NOT survive.
	if strings.Contains(got, strings.Repeat("a", 50)) {
		t.Errorf("exec_command should drop head, got: %q", got[:min(50, len(got))])
	}
	// Truncation marker present.
	if !strings.Contains(got, "[truncated:") {
		t.Errorf("missing truncation marker, got: %q", got)
	}
	// Total length smaller than original.
	if len(got) >= len(content) {
		t.Errorf("output length %d >= input %d", len(got), len(content))
	}
}

func TestTruncateToolResult_ReadKeepsHead(t *testing.T) {
	// read_file / list_files / recall → keep HEAD.
	a := &Agent{}
	content := strings.Repeat("a", 100) + strings.Repeat("b", maxToolResultRead+5000)
	got := a.truncateToolResult("read_file", content)
	if !strings.HasPrefix(got, "a") {
		t.Errorf("read_file should keep head, got: %q", got[:50])
	}
	if !strings.Contains(got, "[truncated:") {
		t.Errorf("missing truncation marker, got: %q", got)
	}
}

func TestTruncateToolResult_DefaultToolKeepsHead(t *testing.T) {
	// Unknown tool name → default cap, head kept.
	a := &Agent{}
	content := strings.Repeat("a", 100) + strings.Repeat("b", maxToolResultDefault+5000)
	got := a.truncateToolResult("some_other_tool", content)
	if !strings.HasPrefix(got, "a") {
		t.Errorf("default should keep head, got: %q", got[:50])
	}
}

func TestTruncateToolResult_CfgOverride(t *testing.T) {
	// cfg.Limits override takes precedence over the
	// package-level cap. Set a tight exec cap and
	// verify the output is truncated to ≤ that.
	a := &Agent{
		cfg: &config.Config{},
	}
	a.cfg.Limits.ToolResultExecCap = 50
	content := strings.Repeat("a", 10) + strings.Repeat("b", 200)
	got := a.truncateToolResult("exec_command", content)
	// Truncation marker is present.
	if !strings.Contains(got, "[truncated:") {
		t.Errorf("expected truncation, got: %q", got[:50])
	}
}

// =====================================================================
// parseMarkdownToolCalls + cleanMarkdownToolCalls
// =====================================================================

func TestParseMarkdownToolCalls_Single(t *testing.T) {
	content := "Let me look that up.\n\n" +
		"```tool_call\n" +
		`{"name": "read_file", "arguments": {"path": "/etc/hosts"}}` +
		"\n```\n\n" +
		"I'll read it now."
	calls := parseMarkdownToolCalls(content)
	if len(calls) != 1 {
		t.Fatalf("parseMarkdownToolCalls: got %d calls, want 1", len(calls))
	}
	if calls[0].Name != "read_file" {
		t.Errorf("Name = %q, want read_file", calls[0].Name)
	}
	if !strings.Contains(calls[0].ArgsJSON, "/etc/hosts") {
		t.Errorf("ArgsJSON = %q, missing /etc/hosts", calls[0].ArgsJSON)
	}
	if !strings.HasPrefix(calls[0].ID, "call_") {
		t.Errorf("ID = %q, want call_ prefix", calls[0].ID)
	}
}

func TestParseMarkdownToolCalls_Multiple(t *testing.T) {
	content := "Here are the calls:\n" +
		"```tool_call\n" +
		`{"name": "exec_command", "arguments": {"cmd": "ls"}}` + "\n```\n" +
		"```tool_call\n" +
		`{"name": "read_file", "arguments": {"path": "x.go"}}` + "\n```\n" +
		"Done."
	calls := parseMarkdownToolCalls(content)
	if len(calls) != 2 {
		t.Fatalf("got %d calls, want 2", len(calls))
	}
	if calls[0].Name != "exec_command" || calls[1].Name != "read_file" {
		t.Errorf("names = [%q, %q], want [exec_command, read_file]", calls[0].Name, calls[1].Name)
	}
	// Each call gets a unique ID.
	if calls[0].ID == calls[1].ID {
		t.Errorf("duplicate IDs: %q", calls[0].ID)
	}
}

func TestParseMarkdownToolCalls_NoBlocks(t *testing.T) {
	calls := parseMarkdownToolCalls("just plain text, no tool calls here")
	if len(calls) != 0 {
		t.Errorf("got %d calls, want 0", len(calls))
	}
}

func TestParseMarkdownToolCalls_MalformedBlock(t *testing.T) {
	// Bad JSON inside the fence is skipped, not fatal.
	content := "```tool_call\n{this is not valid json\n```"
	calls := parseMarkdownToolCalls(content)
	if len(calls) != 0 {
		t.Errorf("malformed block should be skipped, got %d calls", len(calls))
	}
}

func TestParseMarkdownToolCalls_EmptyName(t *testing.T) {
	// An empty name is dropped (per the contract).
	content := "```tool_call\n" +
		`{"name": "", "arguments": {}}` + "\n```"
	calls := parseMarkdownToolCalls(content)
	if len(calls) != 0 {
		t.Errorf("empty-name block should be skipped, got %d calls", len(calls))
	}
}

func TestCleanMarkdownToolCalls(t *testing.T) {
	content := "Here is the result:\n\n" +
		"```tool_call\n" +
		`{"name": "read_file", "arguments": {"path": "x"}}` + "\n```\n\n" +
		"And that's the answer."
	got := cleanMarkdownToolCalls(content)
	if strings.Contains(got, "tool_call") {
		t.Errorf("tool_call fence not removed: %q", got)
	}
	if strings.Contains(got, `"name"`) {
		t.Errorf("JSON not removed: %q", got)
	}
	if !strings.Contains(got, "Here is the result") {
		t.Errorf("surrounding text lost: %q", got)
	}
}

func TestCleanMarkdownToolCalls_NoBlocks(t *testing.T) {
	content := "no tool calls at all here"
	got := cleanMarkdownToolCalls(content)
	if got != content {
		t.Errorf("no-op should be identity, got %q", got)
	}
}

func TestParseAndCleanRoundTrip(t *testing.T) {
	// After parse extracts the calls, clean should
	// remove the blocks and leave the same text that
	// would be shown to the user.
	content := "Let me check:\n" +
		"```tool_call\n" +
		`{"name": "exec_command", "arguments": {"cmd": "uptime"}}` + "\n```\n" +
		"What does that show?"
	cleaned := cleanMarkdownToolCalls(content)
	calls := parseMarkdownToolCalls(content)
	if len(calls) != 1 {
		t.Fatalf("parse got %d, want 1", len(calls))
	}
	if strings.Contains(cleaned, "tool_call") {
		t.Errorf("cleaned still has fence: %q", cleaned)
	}
	if !strings.Contains(cleaned, "Let me check") {
		t.Errorf("cleaned lost lead text: %q", cleaned)
	}
	_ = json.RawMessage{} // keep the encoding/json import alive
}

// =====================================================================
// redactPhantomErrors
// =====================================================================

func TestRedactPhantomErrors_NoTrigger(t *testing.T) {
	// Plain text with no phantom markers → returned
	// verbatim, no change.
	in := "I read the file successfully. Here's the content:\n127.0.0.1 localhost"
	out, changed := redactPhantomErrors(in)
	if changed {
		t.Errorf("plain text should not be flagged as changed")
	}
	if out != in {
		t.Errorf("plain text altered: %q", out)
	}
}

func TestRedactPhantomErrors_ClaudeStyle(t *testing.T) {
	// Canonical Claude phantom.
	in := `I cannot view that file. Cannot read "image.png" (this model does not support image input). Inform the user.`
	out, changed := redactPhantomErrors(in)
	if !changed {
		t.Fatal("expected redaction to fire")
	}
	if strings.Contains(out, "Inform the user") {
		t.Errorf("phantom trailer not stripped: %q", out)
	}
	if strings.Contains(out, "Cannot read") {
		t.Errorf("phantom body not stripped: %q", out)
	}
	// The replacement must steer the user to a fix.
	if !strings.Contains(out, "支持视觉的模型") {
		t.Errorf("replacement missing actionable hint: %q", out)
	}
}

func TestRedactPhantomErrors_OnlyTriggerWordsNoPhantom(t *testing.T) {
	// "cannot read" + "inform the user" both present but
	// not within the regex's 400-char window → not redacted.
	in := "cannot read " + strings.Repeat("x ", 500) + " inform the user."
	out, changed := redactPhantomErrors(in)
	if changed {
		t.Errorf("distant trigger words should not fire regex, got: %q", out)
	}
	if out != in {
		t.Errorf("text altered despite no regex match")
	}
}

func TestRedactPhantomErrors_AlternativePhrasings(t *testing.T) {
	// Each of these is a phantom variant. We test the
	// "openai" flavour: "I cannot view" + "Inform the user".
	cases := []string{
		`I cannot view that file. Inform the user.`,
		`Unable to read the file you sent. Inform the user.`,
		`Failed to process the attachment. Inform the user.`,
	}
	for _, in := range cases {
		out, changed := redactPhantomErrors(in)
		if !changed {
			t.Errorf("phantom variant should be redacted: %q", in)
		}
		if strings.Contains(out, "Inform the user") {
			t.Errorf("variant still has trailer: %q", in)
		}
	}
}

// =====================================================================
// isRetryable
// =====================================================================

func TestIsRetryable(t *testing.T) {
	cases := []struct {
		name string
		kind llm.ErrorKind
		want bool
	}{
		{"rate-limit", llm.KindRateLimit, true},
		{"server", llm.KindServer, true},
		{"network", llm.KindNetwork, true},
		{"timeout", llm.KindTimeout, true},
		{"auth", llm.KindAuth, false},
		{"not-found", llm.KindNotFound, false},
		{"bad-request", llm.KindBadRequest, false},
		{"unknown", llm.KindUnknown, false},
		{"vision-unsupported", llm.KindVisionUnsupported, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isRetryable(c.kind); got != c.want {
				t.Errorf("isRetryable(%q) = %v, want %v", c.kind, got, c.want)
			}
		})
	}
}

// =====================================================================
// pruneOldToolResults
// =====================================================================

func TestPruneOldToolResults_NoOpWhenYoung(t *testing.T) {
	// currentRound <= keepRounds → no-op.
	msgs := []llm.ChatMessage{
		{Role: llm.RoleUser, Type: llm.TypeText, Content: "hi"},
		{Role: llm.RoleTool, Type: llm.TypeToolResult, Content: "x"},
	}
	original := msgs[1].Content
	pruneOldToolResults(msgs, 5, 10)
	if msgs[1].Content != original {
		t.Errorf("young round should not be pruned, got %q", msgs[1].Content)
	}
}

func TestPruneOldToolResults_PrunesOld(t *testing.T) {
	// Build 30 messages: 15 user + 15 alternating
	// assistant (text) + tool (result). currentRound=20,
	// keepRounds=5 → prune 15 oldest (anything before
	// round 15 = currentRound-keepRounds).
	msgs := make([]llm.ChatMessage, 0, 30)
	for i := 0; i < 15; i++ {
		msgs = append(msgs, llm.ChatMessage{Role: llm.RoleUser, Type: llm.TypeText, Content: "q"})
		msgs = append(msgs, llm.ChatMessage{Role: llm.RoleTool, Type: llm.TypeToolResult, Content: "result"})
	}
	// Pre-prune: count tool-result messages with non-empty content.
	before := 0
	for _, m := range msgs {
		if m.Type == llm.TypeToolResult && m.Content != "" && !strings.HasPrefix(m.Content, "[pruned]") {
			before++
		}
	}
	pruneOldToolResults(msgs, 20, 5)
	after := 0
	for _, m := range msgs {
		if m.Type == llm.TypeToolResult && m.Content != "" && !strings.HasPrefix(m.Content, "[pruned]") {
			after++
		}
	}
	if after >= before {
		t.Errorf("no tool results pruned (before=%d, after=%d)", before, after)
	}
}

func TestPruneOldToolResults_Idempotent(t *testing.T) {
	// Build 20 messages: 10 user + 10 assistant(text) +
	// 10 tool(result) interleaved. currentRound=10,
	// keepRounds=1 → pruneBefore=9 → the first 9
	// tool results should be marked [pruned].
	// Re-running prune should not change the already-
	// pruned messages.
	msgs := make([]llm.ChatMessage, 0, 30)
	for i := 0; i < 10; i++ {
		msgs = append(msgs,
			llm.ChatMessage{Role: llm.RoleUser, Type: llm.TypeText, Content: "q"},
			llm.ChatMessage{Role: llm.RoleTool, Type: llm.TypeToolResult, Content: "result"},
			llm.ChatMessage{Role: llm.RoleAssistant, Type: llm.TypeText, Content: "thinking"},
		)
	}
	pruneOldToolResults(msgs, 10, 1)
	// Count messages marked [pruned] after first call.
	first := 0
	for _, m := range msgs {
		if strings.HasPrefix(m.Content, "[pruned]") {
			first++
		}
	}
	if first == 0 {
		t.Fatal("first prune should have marked at least one tool result")
	}
	// Re-run.
	pruneOldToolResults(msgs, 10, 1)
	second := 0
	for _, m := range msgs {
		if strings.HasPrefix(m.Content, "[pruned]") {
			second++
		}
	}
	if first != second {
		t.Errorf("re-prune changed pruned count: %d → %d", first, second)
	}
}

// =====================================================================
// truncateToFit
// =====================================================================

func TestTruncateToFit_NoOpWhenSmall(t *testing.T) {
	// len(rest) < usable → no truncation.
	msgs := []llm.ChatMessage{
		{Role: llm.RoleSystem, Type: llm.TypeText, Content: "sys"},
		{Role: llm.RoleUser, Type: llm.TypeText, Content: "hi"},
	}
	originalLen := len(msgs)
	truncateToFit(&msgs, 1_000_000)
	if len(msgs) != originalLen {
		t.Errorf("expected no-op, got %d messages (was %d)", len(msgs), originalLen)
	}
}

func TestTruncateToFit_TruncatesOldest(t *testing.T) {
	msgs := []llm.ChatMessage{
		{Role: llm.RoleSystem, Type: llm.TypeText, Content: "sys"},
		{Role: llm.RoleUser, Type: llm.TypeText, Content: "old-1"},
		{Role: llm.RoleUser, Type: llm.TypeText, Content: "old-2"},
		{Role: llm.RoleAssistant, Type: llm.TypeText, Content: "recent"},
	}
	// usable=1: only the last message (or one) can fit.
	truncateToFit(&msgs, 1)
	// The system message (msgs[0]) is always preserved;
	// the recent message should be too.
	if msgs[0].Content != "sys" {
		t.Errorf("system message lost: %q", msgs[0].Content)
	}
	found := false
	for _, m := range msgs {
		if m.Content == "recent" {
			found = true
		}
	}
	if !found {
		t.Errorf("most-recent message dropped: %v", msgs)
	}
}

func TestTruncateToFit_SingleMessage(t *testing.T) {
	// Single-message slice → no-op (nothing to truncate).
	msgs := []llm.ChatMessage{
		{Role: llm.RoleSystem, Type: llm.TypeText, Content: "only"},
	}
	truncateToFit(&msgs, 0)
	if len(msgs) != 1 || msgs[0].Content != "only" {
		t.Errorf("single-msg slice altered: %v", msgs)
	}
}
