package agents

import (
	"os"
	"strings"

	"github.com/p-chat/pchat/internal/paths"
)

// LoadGlobal loads ~/.p-chat/AGENTS.md
func LoadGlobal() string {
	data, err := os.ReadFile(paths.GlobalAgents())
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(data), "\n")
}

// LoadProject loads ./.p-chat/AGENTS.md (project root)
func LoadProject() string {
	data, err := os.ReadFile(paths.ProjectAgents())
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(data), "\n")
}

// LoadAll loads both global and project AGENTS.md and returns combined content.
// Output is byte-stable: the section is always present, even when both files
// are missing, so the resulting system prompt is identical between calls.
func LoadAll() string {
	var sb strings.Builder
	sb.WriteString("## Agent Instructions\n\n")

	global := LoadGlobal()
	project := LoadProject()

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
