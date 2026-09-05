package skill

import (
	"context"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

type Skill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Content     string `json:"content"`
	Path        string `json:"path"`
}

// LoadAll 在没有项目覆盖层时加载 Manager 的生效目录。
// LoadAll loads the effective Manager catalog without a project overlay.
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
// 发现范围包含 P-Chat 托管目录和标准 .agents/skills。
// Discovery includes P-Chat managed directories and standard .agents/skills.
// 优先级、frontmatter 名称与诊断均由 Manager 负责。
// Manager owns precedence, frontmatter names, and diagnostics.
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
	catalog, err := NewManager().Catalog(context.Background(), CatalogQuery{ProjectRoot: root})
	if err != nil {
		return nil, err
	}
	skills := make([]Skill, 0, len(catalog.Skills))
	for _, info := range catalog.Skills {
		data, readErr := os.ReadFile(info.Path)
		if readErr != nil {
			continue
		}
		skills = append(skills, Skill{Name: info.Name, Description: info.Description, Content: string(data), Path: info.Path})
	}
	return skills, nil
}

func extractDescription(content string) string {
	body := content
	if meta, rest, ok := splitFrontmatter(content); ok {
		var fm struct {
			Description string `yaml:"description"`
		}
		if err := yaml.Unmarshal([]byte(meta), &fm); err == nil && strings.TrimSpace(fm.Description) != "" {
			return strings.TrimSpace(fm.Description)
		}
		body = rest
	}
	lines := strings.Split(body, "\n")
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

func splitFrontmatter(content string) (meta, rest string, ok bool) {
	normalized := strings.ReplaceAll(content, "\r\n", "\n")
	if !strings.HasPrefix(normalized, "---\n") {
		return "", content, false
	}
	end := strings.Index(normalized[4:], "\n---")
	if end < 0 {
		return "", content, false
	}
	metaEnd := 4 + end
	restStart := metaEnd + len("\n---")
	if restStart < len(normalized) {
		if normalized[restStart] != '\n' {
			return "", content, false
		}
		restStart++
	}
	return normalized[4:metaEnd], normalized[restStart:], true
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
		fmt.Fprintf(&sb, "- `%s`: %s", s.Name, s.Description)
		if s.Path != "" {
			fmt.Fprintf(&sb, " (%s)", s.Path)
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}
