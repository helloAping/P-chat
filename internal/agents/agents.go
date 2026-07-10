package agents

import (
	"errors"
	"log"
	"os"
	"strings"

	"github.com/p-chat/pchat/internal/paths"
)

// readFile is a tiny helper that reads a file, treats a
// missing file as empty, and logs any other error so a
// permission problem or a corrupt file isn't silently
// ignored. The previous version returned "" for ALL errors,
// meaning a permission-denied or a corrupted file would just
// look like "no AGENTS.md configured" with no diagnostic.
func readFile(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ""
		}
		log.Printf("[agents] WARN: failed to read %s: %v", path, err)
		return ""
	}
	return strings.TrimRight(string(data), "\n")
}

// LoadGlobal loads ~/.p-chat/AGENTS.md
func LoadGlobal() string {
	return readFile(paths.GlobalAgents())
}

// LoadProject loads ./.p-chat/AGENTS.md (project root)
func LoadProject() string {
	return readFile(paths.ProjectAgents())
}

// LoadProjectWithRoot loads AGENTS.md from an explicit project root.
func LoadProjectWithRoot(root string) string {
	return readFile(paths.ProjectAgentsWithRoot(root))
}

// LoadAll loads both global and project AGENTS.md and returns combined content.
// Output is byte-stable: the section is always present, even when both files
// are missing, so the resulting system prompt is identical between calls.
func LoadAll() string {
	return LoadAllWithRoot("")
}

// LoadAllWithRoot loads the AGENTS.md instruction file for the
// current session, applying the 2026-07 OR policy:
//
//	1. <root>/AGENTS.md           (project root, primary)
//	2. <root>/.p-chat/AGENTS.md   (project .p-chat, fallback)
//	3. ~/.p-chat/AGENTS.md        (global, last resort)
//
// The first file that exists wins; the others are ignored.
// Output is byte-stable: the section is always present, with
// a (none) marker when nothing was found, so the resulting
// system prompt is identical between calls when nothing
// changes (LLM prefix cache invariant).
//
// The section header varies based on which slot was hit so
// the LLM can attribute the instructions correctly:
//
//	### Project (root)   — slot 1
//	### Project (.p-chat) — slot 2
//	### Global           — slot 3
//
// Switching projectRoot may change the header — the
// agentsSignatureWithRoot in agent.go incorporates the mtime
// of all three files plus the root, so the static-prompt
// cache key changes automatically and the LLM sees a
// consistent prompt after a project switch.
//
// The previous design loaded BOTH global and project in
// parallel, which produced conflicting instructions when
// the user had different rules at the two levels (e.g. a
// "always answer in English" project rule vs. a "default
// to Chinese" global rule). The OR strategy picks one
// source per session so there's never a conflict.
//
// When root is empty (no project pinned), the two
// project-level slots are skipped and the global slot
// alone is used. This matches the "global mode" UX where
// the user is intentionally working without a project.
func LoadAllWithRoot(root string) string {
	var sb strings.Builder
	sb.WriteString("## Agent Instructions\n\n")

	// Slot 1: <root>/AGENTS.md (project root, primary)
	// Slot 2: <root>/.p-chat/AGENTS.md (project .p-chat, fallback)
	// Slot 3: ~/.p-chat/AGENTS.md (global, last resort)
	type slot struct {
		header  string
		content string
	}
	var found *slot
	if root != "" {
		if c := readFile(paths.ProjectAgentsWithRoot(root)); c != "" {
			found = &slot{"### Project (root)", c}
		} else if c := readFile(paths.ProjectPChatAgentsWithRoot(root)); c != "" {
			found = &slot{"### Project (.p-chat)", c}
		}
	}
	if found == nil {
		if c := LoadGlobal(); c != "" {
			found = &slot{"### Global", c}
		}
	}
	if found == nil {
		sb.WriteString("(none)\n")
		return sb.String()
	}
	sb.WriteString(found.header)
	sb.WriteString("\n\n")
	sb.WriteString(found.content)
	sb.WriteString("\n\n")
	return sb.String()
}
