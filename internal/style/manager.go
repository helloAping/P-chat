package style

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
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
//   - Memory: 用户自定义的背景知识，动态注入到每轮对话上下文末尾，
//     修改即生效、不破坏 LLM 静态缓存
type SoulSection struct {
	Identity string
	Soul     string
	Memory   string
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

// loadAll loads both identity and soul files for each style
// (built-in + user-added). Directory layout:
//
//	<prompts>/
//	  identity/{cute,guofeng,tech,...}.md
//	  soul/{cute,guofeng,tech,...}.md
func (m *Manager) loadAll() error {
	all := m.ListAll()
	// Drop any previously loaded user-added styles so deletions
	// take effect on reload.
	m.sections = make(map[Style]SoulSection, len(all))
	for _, s := range all {
		var sec SoulSection

		identityPath := filepath.Join(m.dir, "identity", string(s)+".md")
		if data, err := os.ReadFile(identityPath); err == nil {
			sec.Identity = string(data)
		}

		soulPath := filepath.Join(m.dir, "soul", string(s)+".md")
		if data, err := os.ReadFile(soulPath); err == nil {
			sec.Soul = string(data)
		}

		memPath := filepath.Join(m.dir, "memory", string(s)+".md")
		if data, err := os.ReadFile(memPath); err == nil {
			sec.Memory = string(data)
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

// GetMemory returns the user-defined memory for a style.
// 记忆内容从 prompts/memory/{id}.md 加载，独立于身份和灵魂，
// 使用方负责决定是否注入到上下文中。
func (m *Manager) GetMemory(s Style) (string, error) {
	sec, ok := m.sections[s]
	if !ok {
		return "", fmt.Errorf("unknown style: %s", s)
	}
	return sec.Memory, nil
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
	return m.DisplayLabel(s)
}

func (m *Manager) List() []Style {
	return []Style{Cute, Guofeng, Tech}
}

// ListAll returns every style known to the manager: the built-in
// three (cute/guofeng/tech) plus any user-added styles found on
// disk under <dir>/identity and <dir>/soul. The result is sorted
// (built-ins first, then alphabetically) so the UI is stable.
func (m *Manager) ListAll() []Style {
	seen := map[Style]bool{}
	for _, s := range m.List() {
		seen[s] = true
	}
	// Scan the prompts dir for any extra identity files.
	identityDir := filepath.Join(m.dir, "identity")
	if entries, err := os.ReadDir(identityDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".md")
			if name == "" {
				continue
			}
			s := Style(name)
			if !seen[s] {
				seen[s] = true
			}
		}
	}
	out := make([]Style, 0, len(seen))
	for _, s := range m.List() {
		if seen[s] {
			out = append(out, s)
		}
	}
	extras := []Style{}
	for s := range seen {
		isBuiltin := false
		for _, b := range m.List() {
			if s == b {
				isBuiltin = true
				break
			}
		}
		if !isBuiltin {
			extras = append(extras, s)
		}
	}
	sort.Slice(extras, func(i, j int) bool { return extras[i] < extras[j] })
	out = append(out, extras...)
	return out
}

// DisplayLabel returns the human-readable label for a style, e.g.
// "小P (PiPi)". Falls back to the raw id when not in the built-in
// map (the user-added case where the label is stored in metadata).
func (m *Manager) DisplayLabel(s Style) string {
	if label, ok := styleLabels[s]; ok {
		return label
	}
	// User-added style: try the metadata sidecar next to the prompt
	// files, or fall back to the id.
	metaPath := filepath.Join(m.dir, string(s)+".label")
	if data, err := os.ReadFile(metaPath); err == nil {
		return strings.TrimSpace(string(data))
	}
	return string(s)
}

// Create adds a new user-defined style by writing the identity
// and soul files. The id is normalised (lowercased, non-alpha
// stripped) so it is safe to use as a filename and as a session
// meta value. Returns an error if the id is empty, already taken,
// or contains no letter (would yield a useless filename).
func (m *Manager) Create(id, label, identity, soul, memory string) (Style, error) {
	id = normaliseStyleID(id)
	if id == "" {
		return "", fmt.Errorf("style id must contain at least one letter")
	}
	for _, b := range m.List() {
		if Style(id) == b {
			return "", fmt.Errorf("style id %q is reserved (built-in)", id)
		}
	}
	// Refuse to overwrite an existing style — user must DELETE
	// first or PATCH the existing one.
	if _, err := os.Stat(filepath.Join(m.dir, "identity", id+".md")); err == nil {
		return "", fmt.Errorf("style %q already exists; delete it first", id)
	}
	if err := os.MkdirAll(filepath.Join(m.dir, "identity"), 0o755); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(m.dir, "soul"), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(m.dir, "identity", id+".md"), []byte(identity), 0o644); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(m.dir, "soul", id+".md"), []byte(soul), 0o644); err != nil {
		return "", err
	}
	if label != "" {
		_ = os.WriteFile(filepath.Join(m.dir, id+".label"), []byte(label), 0o644)
	}
	if memory != "" {
		if err := os.MkdirAll(filepath.Join(m.dir, "memory"), 0o755); err != nil {
			return "", err
		}
		_ = os.WriteFile(filepath.Join(m.dir, "memory", id+".md"), []byte(memory), 0o644)
	}
	// Reload the in-memory cache so the new style is immediately
	// available to existing sessions.
	if err := m.loadAll(); err != nil {
		return "", err
	}
	return Style(id), nil
}

// Update overwrites the label / identity / soul / memory of an existing
// style. Empty fields are skipped (caller can leave them off the
// request body to keep the existing value).
func (m *Manager) Update(id, label, identity, soul, memory string) error {
	id = normaliseStyleID(id)
	if id == "" {
		return fmt.Errorf("style id must contain at least one letter")
	}
	// Block edits to the three built-ins — they're a system
	// contract. Users who want their own variant should
	// create a new style and edit that instead.
	for _, b := range m.List() {
		if Style(id) == b {
			return fmt.Errorf("built-in style %q is read-only; create a new style to customise", id)
		}
	}
	if identity != "" {
		if err := os.WriteFile(filepath.Join(m.dir, "identity", id+".md"), []byte(identity), 0o644); err != nil {
			return err
		}
	}
	if soul != "" {
		if err := os.WriteFile(filepath.Join(m.dir, "soul", id+".md"), []byte(soul), 0o644); err != nil {
			return err
		}
	}
	if label != "" {
		if err := os.WriteFile(filepath.Join(m.dir, id+".label"), []byte(label), 0o644); err != nil {
			return err
		}
	}
	if memory != "" {
		if err := os.WriteFile(filepath.Join(m.dir, "memory", id+".md"), []byte(memory), 0o644); err != nil {
			return err
		}
	}
	return m.loadAll()
}

// Delete removes a user-defined style. The built-ins are protected
// to avoid leaving the running sessions with no usable system
// prompt; deleting them would be a foot-gun.
func (m *Manager) Delete(id string) error {
	id = normaliseStyleID(id)
	if id == "" {
		return fmt.Errorf("style id must contain at least one letter")
	}
	for _, b := range m.List() {
		if Style(id) == b {
			return fmt.Errorf("built-in style %q cannot be deleted", id)
		}
	}
	if err := os.Remove(filepath.Join(m.dir, "identity", id+".md")); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(filepath.Join(m.dir, "soul", id+".md")); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(filepath.Join(m.dir, id+".label")); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Remove(filepath.Join(m.dir, "memory", id+".md")); err != nil && !os.IsNotExist(err) {
		return err
	}
	return m.loadAll()
}

// normaliseStyleID lowercases the id and strips out anything that
// isn't [a-z0-9-_.]. The result is safe to use as a filename and
// as a session-meta value.
func normaliseStyleID(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		}
	}
	return b.String()
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
