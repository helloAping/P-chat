package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGlobalDir_UsesHome(t *testing.T) {
	// We can't change $HOME mid-test in a portable way, so just
	// verify GlobalDir() ends with ".p-chat".
	got := GlobalDir()
	if filepath.Base(got) != ".p-chat" {
		t.Errorf("GlobalDir() = %q, want basename '.p-chat'", got)
	}
}

func TestProjectDir_UsesCwd(t *testing.T) {
	cwd, _ := os.Getwd()
	want := filepath.Join(cwd, ".p-chat")
	if got := ProjectDir(); got != want {
		t.Errorf("ProjectDir() = %q, want %q", got, want)
	}
}

func TestEnsureGlobal_CreatesAllSubdirs(t *testing.T) {
	// Use a temp HOME so we don't touch the real config.
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)
	t.Setenv("HOME", tmp)

	if err := EnsureGlobal(); err != nil {
		t.Fatal(err)
	}

	expected := []string{
		GlobalDir(),
		GlobalSkillsDir(),
		GlobalRulesDir(),
		GlobalPromptsDir(),
		MemoryDir(),
		ToolsDir(),
	}
	for _, d := range expected {
		info, err := os.Stat(d)
		if err != nil {
			t.Errorf("missing dir %s: %v", d, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("%s is not a directory", d)
		}
	}
}

func TestEnsureProject_CreatesAllSubdirs(t *testing.T) {
	tmp := t.TempDir()
	oldCwd, _ := os.Getwd()
	if err := os.Chdir(tmp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldCwd) })

	if err := EnsureProject(); err != nil {
		t.Fatal(err)
	}

	expected := []string{
		ProjectDir(),
		ProjectSkillsDir(),
		ProjectRulesDir(),
	}
	for _, d := range expected {
		if _, err := os.Stat(d); err != nil {
			t.Errorf("missing %s: %v", d, err)
		}
	}
}

func TestPathConstants(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("USERPROFILE", tmp)

	cases := map[string]struct {
		prefix string
		suffix string
	}{
		"GlobalConfig":     {GlobalDir(), "config.yaml"},
		"ProjectConfig":   {ProjectDir(), "config.yaml"},
		"GlobalAgents":     {GlobalDir(), "AGENTS.md"},
		"GlobalSkillsDir":  {GlobalDir(), "skills"},
		"ProjectSkillsDir": {ProjectDir(), "skills"},
		"GlobalRulesDir":   {GlobalDir(), "rules"},
		"ProjectRulesDir":  {ProjectDir(), "rules"},
		"GlobalPromptsDir": {GlobalDir(), "prompts"},
		"MemoryDir":        {GlobalDir(), "memory"},
		"MemoryDB":         {MemoryDir(), "store.db"},
		"MemoryFile":       {MemoryDir(), "conversations.json"},
		"KnowledgeDir":     {GlobalDir(), "knowledge"},
		"ToolsDir":         {GlobalDir(), "tools"},
	}
	for name, want := range cases {
		var got string
		switch name {
		case "GlobalConfig":
			got = GlobalConfig()
		case "ProjectConfig":
			got = ProjectConfig()
		case "GlobalAgents":
			got = GlobalAgents()
		case "GlobalSkillsDir":
			got = GlobalSkillsDir()
		case "ProjectSkillsDir":
			got = ProjectSkillsDir()
		case "GlobalRulesDir":
			got = GlobalRulesDir()
		case "ProjectRulesDir":
			got = ProjectRulesDir()
		case "GlobalPromptsDir":
			got = GlobalPromptsDir()
		case "MemoryDir":
			got = MemoryDir()
		case "MemoryDB":
			got = MemoryDB()
		case "MemoryFile":
			got = MemoryFile()
		case "KnowledgeDir":
			got = KnowledgeDir()
		case "ToolsDir":
			got = ToolsDir()
		}
		if filepath.Base(got) != want.suffix {
			t.Errorf("%s() = %q, want suffix %q", name, got, want.suffix)
		}
		if !strings.HasPrefix(got, want.prefix) {
			t.Errorf("%s() = %q, want prefix %q", name, got, want.prefix)
		}
	}
}
