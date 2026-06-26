package paths

import (
	"os"
	"path/filepath"
)

const (
	GlobalDirName = ".p-chat"
	ProjectDirName = ".p-chat"
)

// GlobalDir returns ~/.p-chat
func GlobalDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, GlobalDirName)
}

// ProjectDir returns .p-chat in the current working directory
func ProjectDir() string {
	cwd, _ := os.Getwd()
	return filepath.Join(cwd, ProjectDirName)
}

// GlobalConfig returns ~/.p-chat/config.yaml
func GlobalConfig() string {
	return filepath.Join(GlobalDir(), "config.yaml")
}

// ProjectConfig returns .p-chat/config.yaml
func ProjectConfig() string {
	return filepath.Join(ProjectDir(), "config.yaml")
}

// GlobalAgents returns ~/.p-chat/AGENTS.md
func GlobalAgents() string {
	return filepath.Join(GlobalDir(), "AGENTS.md")
}

// ProjectAgents returns ./AGENTS.md (project root)
func ProjectAgents() string {
	cwd, _ := os.Getwd()
	return filepath.Join(cwd, "AGENTS.md")
}

// GlobalSkillsDir returns ~/.p-chat/skills/
func GlobalSkillsDir() string {
	return filepath.Join(GlobalDir(), "skills")
}

// ProjectSkillsDir returns .p-chat/skills/
func ProjectSkillsDir() string {
	return filepath.Join(ProjectDir(), "skills")
}

// GlobalRulesDir returns ~/.p-chat/rules/
func GlobalRulesDir() string {
	return filepath.Join(GlobalDir(), "rules")
}

// ProjectRulesDir returns .p-chat/rules/
func ProjectRulesDir() string {
	return filepath.Join(ProjectDir(), "rules")
}

// GlobalPromptsDir returns ~/.p-chat/prompts/
func GlobalPromptsDir() string {
	return filepath.Join(GlobalDir(), "prompts")
}

// MemoryDir returns ~/.p-chat/memory/
func MemoryDir() string {
	return filepath.Join(GlobalDir(), "memory")
}

// MemoryDB returns ~/.p-chat/memory/store.db (SQLite)
func MemoryDB() string {
	return filepath.Join(MemoryDir(), "store.db")
}

// MemoryFile returns ~/.p-chat/memory/conversations.json (legacy)
func MemoryFile() string {
	return filepath.Join(MemoryDir(), "conversations.json")
}

// KnowledgeDir returns ~/.p-chat/knowledge/ (user-attached knowledge bases)
func KnowledgeDir() string {
	return filepath.Join(GlobalDir(), "knowledge")
}

// ToolsDir returns ~/.p-chat/tools/
func ToolsDir() string {
	return filepath.Join(GlobalDir(), "tools")
}

// EnsureGlobal creates ~/.p-chat and subdirectories if they don't exist
func EnsureGlobal() error {
	dirs := []string{
		GlobalDir(),
		GlobalSkillsDir(),
		GlobalRulesDir(),
		GlobalPromptsDir(),
		MemoryDir(),
		ToolsDir(),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// EnsureProject creates .p-chat and subdirectories if they don't exist
func EnsureProject() error {
	dirs := []string{
		ProjectDir(),
		ProjectSkillsDir(),
		ProjectRulesDir(),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
	}
	return nil
}
