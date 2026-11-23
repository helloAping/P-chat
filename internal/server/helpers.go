package server

import (
	"github.com/p-chat/pchat/internal/skill"
)

// loadAllSkills wraps skill.LoadAll with a small test override
// (t.Setenv("USERPROFILE", tmp)) so the resolver picks up the
// per-test temp dir.
func loadAllSkills() ([]skill.Skill, error) {
	return skill.LoadAll()
}
