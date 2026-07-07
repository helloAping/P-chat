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

// LoadAllWithRoot loads global AGENTS.md plus AGENTS.md from an explicit project root.
// When root is empty, falls back to os.Getwd()-based ProjectAgents().
func LoadAllWithRoot(root string) string {
	var sb strings.Builder
	sb.WriteString("## Agent Instructions\n\n")

	global := LoadGlobal()
	var project string
	if root != "" {
		project = LoadProjectWithRoot(root)
	} else {
		project = LoadProject()
	}

	if global == "" && project == "" {
		sb.WriteString("(none)\n")
		return sb.String()
	}

	if global != "" {
		sb.WriteString("### Global\n\n")
		sb.WriteString(global)
		sb.WriteString("\n\n")
	}
	if project != "" {
		sb.WriteString("### Project\n\n")
		sb.WriteString(project)
		sb.WriteString("\n\n")
	}
	return sb.String()
}
