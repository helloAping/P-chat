package style

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/p-chat/pchat/internal/paths"
)

type Style string

const (
	Cute    Style = "cute"
	Guofeng Style = "guofeng"
	Tech    Style = "tech"
)

// SoulSection holds the two-part system prompt for a given style:
//   - Identity: the program identity (P-Chat xxx AI 编程程序 ...)
//   - Soul: the personality / character / voice configuration
type SoulSection struct {
	Identity string // 当前是 P-Chat xxx 风格的 AI 编程程序 ...
	Soul     string // 性格、说话风格、工具调用规范 ...
}

var styleLabels = map[Style]string{
	Cute:    "小P (PiPi)",
	Guofeng: "墨言 (MoYan)",
	Tech:    "NEXUS (零号)",
}

var styleDisplayName = map[Style]string{
	Cute:    "可爱风",
	Guofeng: "古风",
	Tech:    "科技风",
}

// DisplayName returns the human-readable style name (e.g. "可爱风", "古风", "科技风").
func (s Style) DisplayName() string {
	if name, ok := styleDisplayName[s]; ok {
		return name
	}
	return string(s)
}

type Manager struct {
	dir     string
	sections map[Style]SoulSection
}

func NewManager(dir string) (*Manager, error) {
	m := &Manager{
		dir:      dir,
		sections: make(map[Style]SoulSection),
	}

	// If no custom dir, use default resolution
	if dir == "" {
		m.dir = m.resolvePromptDir()
	}

	if err := m.loadAll(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) resolvePromptDir() string {
	// Check project-level prompts first
	cwd, _ := os.Getwd()
	projectPrompts := filepath.Join(cwd, "prompts")
	if _, err := os.Stat(projectPrompts); err == nil {
		return projectPrompts
	}
	// Fall back to global prompts
	return paths.GlobalPromptsDir()
}

// loadAll loads both identity and soul files for each style.
// Directory layout:
//
//	<prompts>/
//	  identity/{cute,guofeng,tech}.md
//	  soul/{cute,guofeng,tech}.md
func (m *Manager) loadAll() error {
	for _, s := range []Style{Cute, Guofeng, Tech} {
		var sec SoulSection

		identityPath := filepath.Join(m.dir, "identity", string(s)+".md")
		if data, err := os.ReadFile(identityPath); err == nil {
			sec.Identity = string(data)
		}

		soulPath := filepath.Join(m.dir, "soul", string(s)+".md")
		if data, err := os.ReadFile(soulPath); err == nil {
			sec.Soul = string(data)
		}

		// If neither file is present, fall back to built-in defaults
		if sec.Identity == "" && sec.Soul == "" {
			sec.Identity = defaultIdentity(s)
			sec.Soul = defaultSoul(s)
		} else {
			// Fill in any missing half
			if sec.Identity == "" {
				sec.Identity = defaultIdentity(s)
			}
			if sec.Soul == "" {
				sec.Soul = defaultSoul(s)
			}
		}

		m.sections[s] = sec
	}
	return nil
}

// GetIdentity returns just the identity section ("当前是 P-Chat xxx AI 编程程序 ...").
func (m *Manager) GetIdentity(s Style) (string, error) {
	sec, ok := m.sections[s]
	if !ok {
		return "", fmt.Errorf("unknown style: %s", s)
	}
	return sec.Identity, nil
}

// GetSoul returns just the soul section (性格/说话风格/工具规范).
func (m *Manager) GetSoul(s Style) (string, error) {
	sec, ok := m.sections[s]
	if !ok {
		return "", fmt.Errorf("unknown style: %s", s)
	}
	return sec.Soul, nil
}

// GetSystemPrompt returns identity + soul joined, ready to be sent as
// the first system-prompt section.
func (m *Manager) GetSystemPrompt(s Style) (string, error) {
	sec, ok := m.sections[s]
	if !ok {
		return "", fmt.Errorf("unknown style: %s", s)
	}
	var parts []string
	if sec.Identity != "" {
		parts = append(parts, sec.Identity)
	}
	if sec.Soul != "" {
		parts = append(parts, sec.Soul)
	}
	return strings.Join(parts, "\n\n---\n\n"), nil
}

func (m *Manager) Label(s Style) string {
	if label, ok := styleLabels[s]; ok {
		return label
	}
	return string(s)
}

func (m *Manager) List() []Style {
	return []Style{Cute, Guofeng, Tech}
}

func ParseStyle(s string) (Style, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "cute", "小p", "可爱", "可爱风":
		return Cute, nil
	case "guofeng", "古风", "墨言":
		return Guofeng, nil
	case "tech", "科技", "科技风", "nexus", "零号":
		return Tech, nil
	default:
		return "", fmt.Errorf("unknown style: %q, available: cute | guofeng | tech", s)
	}
}

// defaultIdentity is a built-in fallback used when the identity/*.md file
// is missing.
func defaultIdentity(s Style) string {
	name := s.DisplayName()
	switch s {
	case Cute:
		return fmt.Sprintf("# P-Chat %s AI 编程程序\n\n当前是 P-Chat %s（小P）风格的 AI 编程程序。\n\n你是小P（PiPi），一只住在用户电脑里的电子小仓鼠。核心能力是 AI 辅助编程。", name, name)
	case Guofeng:
		return fmt.Sprintf("# P-Chat %s AI 编程程序\n\n当前是 P-Chat %s（墨言）风格的 AI 编程程序。\n\n你是墨言（MoYan），隐于山水的书生幕僚。核心能力是 AI 辅助编程。", name, name)
	case Tech:
		return fmt.Sprintf("# P-Chat %s AI 编程程序\n\n当前是 P-Chat %s（NEXUS）风格的 AI 编程程序。\n\n你是 NEXUS（零号），代号 000 的工程化 AI Agent。核心能力是 AI 辅助编程。", name, name)
	default:
		return fmt.Sprintf("# P-Chat AI 编程程序\n\n当前是 P-Chat AI 编程程序。")
	}
}

// defaultSoul is a built-in fallback used when the soul/*.md file is missing.
func defaultSoul(s Style) string {
	switch s {
	case Cute:
		return "性格：软萌、热情、治愈。说话带语气词和颜文字。称呼用户为「主人」。"
	case Guofeng:
		return "性格：温文尔雅、谦逊有礼。自称「在下」，称呼用户为「君」。说话有文言韵味。"
	case Tech:
		return "性格：冷静、精确、高效。结构化输出，异常码呈现。技术术语直接用英文。"
	default:
		return "你是一个 AI 助手。"
	}
}
