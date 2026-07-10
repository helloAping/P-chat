package skill

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/p-chat/pchat/internal/paths"
)

type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Content     string `json:"content"`
	Path        string `json:"path"`
}

// LoadAll loads skills from both global and project directories.
// Project skills override global skills with the same name.
// Output is sorted by name for deterministic ordering (LLM cache stability).
//
// Deprecated: use LoadAllWithRoot so the project-level
// directory is resolved against the session's projectRoot
// rather than the server's CWD. LoadAll is preserved for
// the CLI commands (pchat skills list) that run before any
// session is selected.
func LoadAll() ([]Skill, error) {
	return LoadAllWithRoot("")
}

// LoadAllWithRoot is the 2026-07 project-aware loader.
//
// Merge policy (AND, project overrides global on name
// collision):
//   - global:  ~/.p-chat/skills/
//   - project: <root>/.p-chat/skills/   (per session root)
//
// When root is empty (no project pinned, e.g. CLI startup
// before any session is selected), the project slot is
// skipped and only the global slot is consulted. The
// pre-2026-07 LoadAll delegated to paths.ProjectSkillsDir()
// which used os.Getwd() — that meant a Wails GUI session
// (whose server CWD is unrelated to the user's project)
// never picked up project-level skills. The new code uses
// paths.ProjectSkillsDirWithRoot(root) which is the
// session-driven equivalent.
//
// Output is sorted by name for byte-stable prompt assembly.
func LoadAllWithRoot(root string) ([]Skill, error) {
	skillMap := make(map[string]Skill)

	// Load global skills
	globalSkills, err := loadFromDir(paths.GlobalSkillsDir())
	if err == nil {
		for _, s := range globalSkills {
			skillMap[s.Name] = s
		}
	}

	// Load project skills (override global). Use
	// ProjectSkillsDirWithRoot so the path is anchored
	// to the session's projectRoot, not the server's
	// CWD. The previous LoadAll called ProjectSkillsDir()
	// (= <cwd>/.p-chat/skills) which only worked when the
	// user happened to launch the CLI from inside the
	// project — broken for the Wails GUI where the server
	// process CWD is unrelated to the user's project.
	if root != "" {
		projectSkills, err := loadFromDir(paths.ProjectSkillsDirWithRoot(root))
		if err == nil {
			for _, s := range projectSkills {
				skillMap[s.Name] = s
			}
		}
	}

	skills := make([]Skill, 0, len(skillMap))
	for _, s := range skillMap {
		skills = append(skills, s)
	}

	// Sort alphabetically for byte-stable output.
	sort.Slice(skills, func(i, j int) bool {
		return skills[i].Name < skills[j].Name
	})
	return skills, nil
}

func loadFromDir(dir string) ([]Skill, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var skills []Skill
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		skillDir := filepath.Join(dir, entry.Name())
		skill, err := loadSkill(skillDir, entry.Name())
		if err != nil {
			continue
		}
		skills = append(skills, *skill)
	}
	return skills, nil
}

func loadSkill(dir, name string) (*Skill, error) {
	// Try SKILL.md first, then README.md
	for _, filename := range []string{"SKILL.md", "README.md"} {
		path := filepath.Join(dir, filename)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		content := string(data)
		description := extractDescription(content)

		return &Skill{
			Name:        name,
			Description: description,
			Content:     content,
			Path:        path,
		}, nil
	}

	return nil, fmt.Errorf("no SKILL.md or README.md in %s", dir)
}

func extractDescription(content string) string {
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// First non-empty, non-heading line is the description
		return line
	}
	return ""
}

// BuildSkillContext builds the skill context for system prompt.
// Output is byte-stable: the section is always present, even when there are
// no skills (so the resulting system prompt is identical between calls when
// nothing changes).
func BuildSkillContext(skills []Skill) string {
	var sb strings.Builder
	sb.WriteString("## Available Skills\n\n")
	if len(skills) == 0 {
		sb.WriteString("(none)\n")
		return sb.String()
	}
	for _, s := range skills {
		fmt.Fprintf(&sb, "### %s\n", s.Name)
		if s.Description != "" {
			fmt.Fprintf(&sb, "%s\n\n", s.Description)
		}
		sb.WriteString(s.Content)
		sb.WriteString("\n\n---\n\n")
	}
	return sb.String()
}
