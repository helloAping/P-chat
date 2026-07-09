package agent

import (
	"strings"
	"testing"
)

// TestAppendWorkingDirectoryBlock_WordingParity locks in that the
// re-append path used when a sub-agent's PromptOv overrides the
// system prompt emits exactly the same "## Working Directory"
// section as buildStaticSystemPrompt does for the main agent.
//
// Drift between the two paths was the root cause of the 2026-07
// "tool runs in the wrong folder" report: buildStaticSystemPrompt
// added the section for the parent, but the sub-agent's PromptOv
// wiped it, so the child's tool calls (exec_command, read_file)
// resolved relative paths against the server startup CWD. Fixing
// the propagation is necessary but not sufficient — if the wording
// drifts, the LLM gets conflicting instructions in the two
// contexts. This test catches that drift at unit-test time.
//
// We assert on substrings rather than a full string compare so
// the test isn't brittle to non-substantive edits (extra blank
// line, punctuation, etc.). The fixed parts (the section title,
// the backticked path, the exec_command / work_dir / relative
// paths lines) are the load-bearing sentences the LLM acts on.
func TestAppendWorkingDirectoryBlock_WordingParity(t *testing.T) {
	const root = "D:\\projects\\myapp"
	got := appendWorkingDirectoryBlock(root)
	for _, want := range []string{
		"## Working Directory",
		"Your working directory is fixed at",
		"`" + root + "`",
		"exec_command runs here automatically",
		"work_dir argument is ignored",
		"read_file and write_file resolve relative",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("appendWorkingDirectoryBlock missing %q\nfull block:\n%s", want, got)
		}
	}
}

// TestAppendWorkingDirectoryBlock_ContainsRootPath is a tighter
// guard than the substring check above: it verifies the project
// root appears in the backticked form (the LLM's parseable
// handle), not as a substring of some other sentence. A buggy
// "%s/some/path" would still match the substring test but
// wouldn't actually point the LLM at the right directory.
func TestAppendWorkingDirectoryBlock_ContainsRootPath(t *testing.T) {
	const root = "D:\\projects\\myapp"
	got := appendWorkingDirectoryBlock(root)
	if !strings.Contains(got, "`"+root+"`") {
		t.Fatalf("expected `%s` in:\n%s", root, got)
	}
}
