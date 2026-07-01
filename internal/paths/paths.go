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

// GlobalConfig returns the primary global config file
// (JSON, ~/.p-chat/config.json).
//
// The legacy YAML path (~/.p-chat/config.yaml) is still recognized
// by the config loader as a one-shot migration source — see
// config.Load. New code should not reference the YAML path
// directly; use the config package for I/O.
func GlobalConfig() string {
	return filepath.Join(GlobalDir(), "config.json")
}

// ProjectConfig returns the primary project config file
// (JSON, .p-chat/config.json).
func ProjectConfig() string {
	return filepath.Join(ProjectDir(), "config.json")
}

// GlobalConfigYAML is the legacy global config path. Kept only
// so the loader can detect and migrate old installs.
func GlobalConfigYAML() string {
	return filepath.Join(GlobalDir(), "config.yaml")
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

// UploadsDir returns ~/.p-chat/uploads/ (file uploads)
func UploadsDir() string {
	return filepath.Join(GlobalDir(), "uploads")
}

// ToolsDir returns ~/.p-chat/tools/
func ToolsDir() string {
	return filepath.Join(GlobalDir(), "tools")
}

// ProjectsFile returns ~/.p-chat/projects.json
func ProjectsFile() string {
	return filepath.Join(GlobalDir(), "projects.json")
}

// ProjectsFileDir returns ~/.p-chat (parent of projects.json)
func ProjectsFileDir() string {
	return GlobalDir()
}

// WithRoot variants – return paths relative to an explicit project root
// instead of os.Getwd(). Used when a session has a project_path override.

func ProjectConfigWithRoot(root string) string {
	return filepath.Join(root, ProjectDirName, "config.json")
}

func ProjectAgentsWithRoot(root string) string {
	return filepath.Join(root, "AGENTS.md")
}

func ProjectSkillsDirWithRoot(root string) string {
	return filepath.Join(root, ProjectDirName, "skills")
}

func ProjectRulesDirWithRoot(root string) string {
	return filepath.Join(root, ProjectDirName, "rules")
}

func ProjectPromptsDirWithRoot(root string) string {
	return filepath.Join(root, "prompts")
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
		KnowledgeDir(),
		UploadsDir(),
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
