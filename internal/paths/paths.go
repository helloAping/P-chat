package paths

import (
	"os"
	"path/filepath"
)

const (
	GlobalDirName = ".p-chat"
	ProjectDirName = ".p-chat"
)

// GlobalDir returns the active global P-Chat home directory.
//
// Resolution (highest priority first):
//  1. PCHAT_HOME env var — explicit operator override.
//  2. Sibling of the running binary: if the binary lives in
//     a "bin" or "dev-bin" subdirectory, use <parent>/.p-chat
//     so a local build doesn't touch the user's real config.
//  3. $HOME/.p-chat — the original behaviour, used by
//     installed / release builds.
//
// The choice is cached for the process lifetime; to inspect
// which strategy picked the path, call ResolveStrategy().
//
// Why this matters: prior to this change, every local
// `bin/pchat-server.exe` run read and wrote the user's
// real `~/.p-chat/`, so a developer's broken / corrupted
// test config could blow away their actual setup. The
// sibling rule means `bin/pchat-server.exe` and
// `dev-bin/pchat-server.exe` each get their own isolated
// `.p-chat` next to the binary, while installed builds
// (which live in e.g. `C:\Program Files\pchat\pchat-server.exe`)
// keep using `%USERPROFILE%\.p-chat`.
func GlobalDir() string {
	return resolveHome().dir
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

// VectorsDir returns ~/.p-chat/vectors/ (local vector store files)
func VectorsDir() string {
	return filepath.Join(GlobalDir(), "vectors")
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

// ProjectPChatAgentsWithRoot returns <root>/.p-chat/AGENTS.md —
// the "project-level .p-chat" AGENTS.md location. This is the
// secondary project-level slot used by the 2026-07 OR loader
// (see internal/agents.LoadAllWithRoot). The primary
// project-level slot is ProjectAgentsWithRoot (the bare root
// AGENTS.md); this one is the fallback that P-Chat's own
// install script populates with a copy of the canonical
// .agents/AGENTS.md so projects always have a starter
// project-level instruction file even when the user has
// not authored a custom one at the project root.
func ProjectPChatAgentsWithRoot(root string) string {
	return filepath.Join(root, ProjectDirName, "AGENTS.md")
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
