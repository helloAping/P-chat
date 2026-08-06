package agent

import (
	"strings"
	"testing"

	"github.com/p-chat/pchat/internal/llm"
)

// TestAppendSummaryInjection_ReplacesNotAccumulates is the core T2
// regression: injecting the summary twice must produce ONE block (the
// second replaces the first), never two. The 2026-08-05 my-blog dead-loop
// grew the system message by ~54KB per round precisely because each
// auto-compaction APPENDED the full accumulated summary instead of
// replacing it.
func TestAppendSummaryInjection_ReplacesNotAccumulates(t *testing.T) {
	system := "static prompt\n\n---\n\n## 我的上下文\n\nstyle-memory"
	system = appendSummaryInjection(system, "summary-v1")
	if got := strings.Count(system, summaryBlockStart); got != 1 {
		t.Fatalf("first injection produced %d summary blocks, want 1", got)
	}
	if !strings.Contains(system, "summary-v1") {
		t.Fatal("first injection missing the summary text")
	}

	system = appendSummaryInjection(system, "summary-v2")
	if got := strings.Count(system, summaryBlockStart); got != 1 {
		t.Fatalf("second injection produced %d summary blocks, want exactly 1", got)
	}
	if strings.Contains(system, "summary-v1") {
		t.Fatal("stale summary-v1 survived the replacement — summaries accumulate")
	}
	if !strings.Contains(system, "summary-v2") {
		t.Fatal("summary-v2 missing after replacement")
	}
	// The non-summary sections must survive the strip+append.
	if !strings.Contains(system, "static prompt") || !strings.Contains(system, "style-memory") {
		t.Fatal("strip+append dropped non-summary system sections")
	}
}

// TestAppendSummaryInjection_CapsSize ensures a pathological summaries
// table (hundreds of summaries, tens of KB) cannot bloat the system
// prompt beyond MaxSummaryPromptTokens.
func TestAppendSummaryInjection_CapsSize(t *testing.T) {
	system := "static"
	huge := strings.Repeat("summary content ", 200_000) // ≈ 100k+ tokens
	out := appendSummaryInjection(system, huge)
	// Extract the block and estimate it.
	start := strings.Index(out, summaryBlockStart)
	end := strings.Index(out[start+len(summaryBlockStart):], summaryBlockEnd)
	if start < 0 || end < 0 {
		t.Fatal("summary block markers missing after injection")
	}
	block := out[start+len(summaryBlockStart) : start+len(summaryBlockStart)+end]
	if est := llm.EstimateTokens(block); est > MaxSummaryPromptTokens {
		t.Fatalf("injected summary = %d tokens, want ≤ %d", est, MaxSummaryPromptTokens)
	}
	if !strings.Contains(block, "summary content") {
		t.Fatal("truncated summary lost its head content")
	}
}

// TestAppendSummaryInjection_EmptySummary keeps the system prompt
// untouched (and strips any stale block) when there is nothing to inject.
func TestAppendSummaryInjection_EmptySummary(t *testing.T) {
	system := "static"
	if out := appendSummaryInjection(system, ""); out != system {
		t.Fatal("empty summary must leave the system prompt unchanged")
	}
	withBlock := appendSummaryInjection(system, "old")
	if out := appendSummaryInjection(withBlock, ""); strings.Contains(out, summaryBlockStart) {
		t.Fatal("empty summary must strip a stale block")
	}
}

// TestStripSummaryInjection_PreservesFollowingSections covers the layout
// where sections (skill context / style memory / todo guard) follow the
// summary block: stripping must remove only the bracketed block and keep
// the rest.
func TestStripSummaryInjection_PreservesFollowingSections(t *testing.T) {
	system := "static" + summaryBlockStart + "old summary" + summaryBlockEnd + "\n\n## 激活的技能上下文\n\nskill"
	out := stripSummaryInjection(system)
	if strings.Contains(out, "old summary") {
		t.Fatal("strip left the summary text behind")
	}
	if !strings.Contains(out, "## 激活的技能上下文") || !strings.Contains(out, "skill") {
		t.Fatal("strip removed sections that follow the summary block")
	}
}
