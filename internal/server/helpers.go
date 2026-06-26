package server

import (
	"os"
	"path/filepath"

	"github.com/p-chat/pchat/internal/paths"
	"github.com/p-chat/pchat/internal/rules"
	"github.com/p-chat/pchat/internal/skill"
)

// readAgentsContext returns the global and project AGENTS.md
// content. Either can be empty if the file doesn't exist. The
// global file is at ~/.p-chat/AGENTS.md; the project file is at
// ./AGENTS.md (cwd-relative).
func readAgentsContext() (global, project string, err error) {
	home, _ := os.UserHomeDir()
	if home != "" {
		data, e := os.ReadFile(filepath.Join(home, ".p-chat", "AGENTS.md"))
		if e == nil {
			global = string(data)
		}
	}
	data, e := os.ReadFile("AGENTS.md")
	if e == nil {
		project = string(data)
	}
	return global, project, nil
}

// loadAllSkills wraps skill.LoadAll with a small test override
// (t.Setenv("USERPROFILE", tmp)) so the resolver picks up the
// per-test temp dir.
func loadAllSkills() ([]skill.Skill, error) {
	return skill.LoadAll()
}

// loadAllRules is the rules counterpart of loadAllSkills.
func loadAllRules() ([]rules.Rule, error) {
	return rules.LoadAll()
}

// ensurePaths is a no-op exported for future use; the paths
// package is the only allowed caller of os.UserHomeDir().
var _ = paths.GlobalDir
