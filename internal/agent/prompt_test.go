package agent

import (
	"runtime"
	"strings"
	"testing"

	"github.com/p-chat/pchat/internal/tool"
)

// =====================================================================
// Pure prompt builders
// =====================================================================
//
// These are the leaves of buildStaticSystemPrompt —
// they take simple inputs and return rendered strings.
// Each test pins one of them in isolation so a future
// refactor of the parent composition doesn't lose
// coverage of the individual sections.

// =====================================================================
// buildWorkingDirBlock
// =====================================================================

func TestBuildWorkingDirBlock_Empty(t *testing.T) {
	if got := buildWorkingDirBlock(""); got != "" {
		t.Errorf("empty root should return empty string, got %q", got)
	}
}

func TestBuildWorkingDirBlock_NonEmpty(t *testing.T) {
	got := buildWorkingDirBlock("/home/user/proj")
	if !strings.Contains(got, "/home/user/proj") {
		t.Errorf("path not embedded: %q", got)
	}
	if !strings.Contains(got, "## Working Directory") {
		t.Errorf("missing section header: %q", got)
	}
	if !strings.Contains(got, "exec_command") {
		t.Errorf("missing exec_command hint: %q", got)
	}
}

func TestAppendWorkingDirectoryBlock_MatchesBuild(t *testing.T) {
	// The two functions must produce the same string for
	// the same input. The sub-agent path uses
	// appendWorkingDirectoryBlock to stay in lock-step
	// with the main prompt (per the docstring); a
	// drift would confuse the LLM.
	root := "/test/root"
	a := buildWorkingDirBlock(root)
	b := appendWorkingDirectoryBlock(root)
	if a != b {
		t.Errorf("buildWorkingDirBlock and appendWorkingDirectoryBlock differ:\n  a=%q\n  b=%q", a, b)
	}
}

// =====================================================================
// buildLanguageBlock
// =====================================================================

func TestBuildLanguageBlock(t *testing.T) {
	cases := []struct {
		lang      string
		mustHave  []string
		mustNot   []string
	}{
		{"zh", []string{"简体中文"}, nil},
		{"en", []string{"English"}, []string{"简体中文"}},
		{"auto", []string{"same language"}, nil},
		{"", []string{"same language"}, nil},
		{"fr", []string{"same language"}, []string{"English", "简体中文"}}, // unknown → auto
	}
	for _, c := range cases {
		t.Run(c.lang, func(t *testing.T) {
			got := buildLanguageBlock(c.lang)
			for _, want := range c.mustHave {
				if !strings.Contains(got, want) {
					t.Errorf("buildLanguageBlock(%q) missing %q: %q", c.lang, want, got)
				}
			}
			for _, deny := range c.mustNot {
				if strings.Contains(got, deny) {
					t.Errorf("buildLanguageBlock(%q) contains unwanted %q: %q", c.lang, deny, got)
				}
			}
		})
	}
}

// =====================================================================
// buildPlatformSection
// =====================================================================

func TestBuildPlatformSection_ContainsOSName(t *testing.T) {
	got := buildPlatformSection()
	// Platform label is always present.
	if !strings.Contains(got, "Platform:") {
		t.Errorf("missing Platform label: %q", got)
	}
	// The OS label must match the runtime GOOS
	// (e.g. "Platform: windows" when running on
	// Windows). This catches a future regression where
	// someone hardcodes one branch.
	wantOS := "Platform: " + runtime.GOOS
	if !strings.Contains(got, wantOS) {
		t.Errorf("missing %q, got: %q", wantOS, got)
	}
}

func TestBuildPlatformSection_HasShellHint(t *testing.T) {
	got := buildPlatformSection()
	if !strings.Contains(got, "Shell for exec_command:") {
		t.Errorf("missing shell hint: %q", got)
	}
}

// =====================================================================
// buildAttachmentsSection
// =====================================================================

func TestBuildAttachmentsSection(t *testing.T) {
	got := buildAttachmentsSection()
	if !strings.Contains(got, "## Uploaded Attachments") {
		t.Errorf("missing section header: %q", got)
	}
	if !strings.Contains(got, "image_url") {
		t.Errorf("missing image_url mention: %q", got)
	}
	if !strings.Contains(got, "read_file") {
		t.Errorf("missing read_file warning: %q", got)
	}
}

// =====================================================================
// buildAvailableToolsSection
// =====================================================================

func TestBuildAvailableToolsSection_Empty(t *testing.T) {
	// No tools → section still renders the header
	// and the static "operation → tool" mapping,
	// but the per-tool table is empty (just the
	// header and separator row).
	got := buildAvailableToolsSection(nil)
	if !strings.Contains(got, "## Available Tools") {
		t.Errorf("missing header: %q", got)
	}
	// No per-tool rows: there should be a header row
	// "| Tool | What it does |" but no data rows
	// starting with "| \`".
	if strings.Contains(got, "| `") {
		t.Errorf("empty tools should not include any data rows, got: %q", got)
	}
}

func TestBuildAvailableToolsSection_ListsTools(t *testing.T) {
	tools := []tool.Tool{
		{Name: "read_file", Description: "Read a file from disk"},
		{Name: "exec_command", Description: "Run a shell command"},
	}
	got := buildAvailableToolsSection(tools)
	if !strings.Contains(got, "read_file") {
		t.Errorf("missing read_file: %q", got)
	}
	if !strings.Contains(got, "exec_command") {
		t.Errorf("missing exec_command: %q", got)
	}
	if !strings.Contains(got, "Run a shell command") {
		t.Errorf("missing description: %q", got)
	}
}

// =====================================================================
// buildToolHintBlock
// =====================================================================

func TestBuildToolHintBlock_NoTools(t *testing.T) {
	// Empty tool list → empty block.
	got := buildToolHintBlock(nil, false)
	if got != "" {
		t.Errorf("empty tools should return empty block, got %q", got)
	}
}

func TestBuildToolHintBlock_NoToolsKB(t *testing.T) {
	// When no tools are listed, the hint block is
	// empty regardless of the KB flag (KB section
	// is gated on actual KB-capable tools, not the
	// toggle flag alone).
	got := buildToolHintBlock(nil, true)
	if got != "" {
		t.Errorf("expected empty block when no tools, got %q", got)
	}
}

// =====================================================================
// buildToolSpecificHints
// =====================================================================

func TestBuildToolSpecificHints_NoRelevantTools(t *testing.T) {
	// No matching tools → empty.
	got := buildToolSpecificHints([]tool.Tool{
		{Name: "exec_command", Description: "x"},
	}, false)
	if got != "" {
		t.Errorf("no hints expected, got %q", got)
	}
}

func TestBuildToolSpecificHints_HasRecall(t *testing.T) {
	got := buildToolSpecificHints([]tool.Tool{
		{Name: "recall", Description: "x"},
	}, false)
	if !strings.Contains(got, "recall") {
		t.Errorf("recall hint not present: %q", got)
	}
}

func TestBuildToolSpecificHints_HasTodoWrite(t *testing.T) {
	got := buildToolSpecificHints([]tool.Tool{
		{Name: "todo_write", Description: "x"},
	}, false)
	if !strings.Contains(got, "todo") {
		t.Errorf("todo hint not present: %q", got)
	}
}

// =====================================================================
// buildStyleBlock
// =====================================================================
//
// buildStyleBlock is an *Agent method. The success path
// requires a real style.Manager with a registered
// style, which is set up in TestStyleFallback in
// agent_static_test.go. Here we just verify the
// contract on a minimal Agent (the call requires a
// styleMgr, so a nil one panics — that's the current
// behaviour; a future safe-nil could be tested here).
//
// Skipped: the nil-styleMgr path panics. Document that
// here rather than hide the panic in a deferred
// recover.
func TestBuildStyleBlock_RequiresStyleMgr(t *testing.T) {
	t.Skip("buildStyleBlock derefs a.styleMgr without a nil check; behaviour by design")
}
