package rules

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/p-chat/pchat/internal/paths"
)

type Rule struct {
	Name    string `json:"name"`
	Content string `json:"content"`
	Path    string `json:"path"`
}

// LoadAll loads rules from both global and project directories.
// Project rules are appended after global rules.
// Output is sorted by name for deterministic ordering (LLM cache stability).
func LoadAll() ([]Rule, error) {
	var rules []Rule

	// Load global rules
	globalRules, _ := loadFromDir(paths.GlobalRulesDir())
	rules = append(rules, globalRules...)

	// Load project rules (appended)
	projectRules, _ := loadFromDir(paths.ProjectRulesDir())
	rules = append(rules, projectRules...)

	// Sort by source (global < project) then by name for a stable, byte-
	// identical output across calls. The LLM's prefix cache keys on byte
	// equality, so non-deterministic order causes cache misses.
	sort.SliceStable(rules, func(i, j int) bool {
		gi := strings.Contains(rules[i].Path, "global")
		gj := strings.Contains(rules[j].Path, "global")
		if gi != gj {
			return gi
		}
		return rules[i].Name < rules[j].Name
	})
	return rules, nil
}

func loadFromDir(dir string) ([]Rule, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}

	var rules []Rule
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		name := entry.Name()
		if !strings.HasSuffix(name, ".md") {
			continue
		}

		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}

		ruleName := strings.TrimSuffix(name, ".md")
		rules = append(rules, Rule{
			Name:    ruleName,
			Content: string(data),
			Path:    path,
		})
	}
	return rules, nil
}

// BuildRulesContext builds the rules context for system prompt.
// Output is byte-stable: the section is always present, even when there are
// no rules (so the resulting system prompt is identical between calls when
// no rules change).
func BuildRulesContext(rules []Rule) string {
	var sb strings.Builder
	sb.WriteString("## Rules\n\n")
	if len(rules) == 0 {
		sb.WriteString("(none)\n")
		return sb.String()
	}
	sb.WriteString("You must follow these rules:\n\n")
	for _, r := range rules {
		sb.WriteString("### ")
		sb.WriteString(r.Name)
		sb.WriteString("\n")
		sb.WriteString(r.Content)
		sb.WriteString("\n\n")
	}
	return sb.String()
}
