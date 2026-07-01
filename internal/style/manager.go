package style

import (
	"database/sql"
	"embed"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

//go:embed prompts/*.md
var builtinFS embed.FS

type Style string

const (
	Cute    Style = "cute"
	Guofeng Style = "guofeng"
	Tech    Style = "tech"
)

type Section struct {
	Prompt string
	Memory string
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

func (s Style) DisplayName() string {
	if name, ok := styleDisplayName[s]; ok {
		return name
	}
	return string(s)
}

type Manager struct {
	db *sql.DB
}

// NewManager opens the style database. On first run (empty styles table)
// it seeds the three built-in styles and attempts to migrate user-defined
// styles from the legacy prompts/ directory on disk.
func NewManager(db *sql.DB) (*Manager, error) {
	m := &Manager{db: db}

	count, err := m.count()
	if err != nil {
		return nil, fmt.Errorf("style: count: %w", err)
	}
	if count == 0 {
		if err := m.seed(); err != nil {
			return nil, fmt.Errorf("style: seed: %w", err)
		}
	}
	return m, nil
}

// count returns the number of rows in the styles table.
func (m *Manager) count() (int, error) {
	var n int
	err := m.db.QueryRow(`SELECT COUNT(*) FROM styles`).Scan(&n)
	return n, err
}

// seed populates the database on first run. Built-in styles are loaded
// from the embedded FS; user-defined styles are imported from the legacy
// prompts/ (v1 identity+soul or v2 style/) directory, then the directory
// is renamed to .migrated-v1 so it never runs twice.
func (m *Manager) seed() error {
	// 1. Built-in styles from embedded FS.
	entries, err := builtinFS.ReadDir("prompts")
	if err != nil {
		return fmt.Errorf("read embedded prompts: %w", err)
	}
	for _, e := range entries {
		id := strings.TrimSuffix(e.Name(), ".md")
		if id == "" {
			continue
		}
		data, err := builtinFS.ReadFile("prompts/" + e.Name())
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", e.Name(), err)
		}
		s := Style(id)
		isBuiltin := s == Cute || s == Guofeng || s == Tech
		label := styleLabels[s]
		if label == "" {
			label = id
		}
		if err := m.insert(id, label, string(data), "", isBuiltin); err != nil {
			return fmt.Errorf("insert %s: %w", id, err)
		}
	}

	// 2. Try importing user-defined styles from the legacy prompts/ dir.
	promptDir := resolvePromptDir()
	m.importLegacy(promptDir)

	return nil
}

// resolvePromptDir returns the prompts directory path (used only during seed).
func resolvePromptDir() string {
	cwd, _ := os.Getwd()
	projectPrompts := filepath.Join(cwd, "prompts")
	if _, err := os.Stat(projectPrompts); err == nil {
		return projectPrompts
	}
	return filepath.Join(os.Getenv("USERPROFILE"), ".p-chat", "prompts")
}

// importLegacy reads v1 (identity/+soul/) or v2 (style/) files and inserts
// user-defined styles into the database. Built-in styles in the legacy dir
// are skipped (already seeded from embedded FS). Only user-style files are
// renamed to .v1.bak — the directory itself is left intact.
func (m *Manager) importLegacy(dir string) {
	imported := 0

	// v2 layout first
	styleDir := filepath.Join(dir, "style")
	if entries, err := os.ReadDir(styleDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			id := strings.TrimSuffix(e.Name(), ".md")
			if id == "" {
				continue
			}
			s := Style(id)
			if s == Cute || s == Guofeng || s == Tech {
				continue
			}
			data, err := os.ReadFile(filepath.Join(styleDir, e.Name()))
			if err != nil {
				log.Printf("[style] import %s: read error: %v", id, err)
				continue
			}
			label := readLabelFile(dir, id)
			if err := m.insert(id, label, string(data), "", false); err != nil {
				log.Printf("[style] import %s: insert error: %v", id, err)
				continue
			}
			imported++
			log.Printf("[style] imported %q from style/%s.md", id, id)
		}
	}

	// v1 layout: identity/ + soul/
	idDir := filepath.Join(dir, "identity")
	soDir := filepath.Join(dir, "soul")
	if entries, err := os.ReadDir(idDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			id := strings.TrimSuffix(e.Name(), ".md")
			if id == "" {
				continue
			}
			s := Style(id)
			if s == Cute || s == Guofeng || s == Tech {
				continue
			}
			if m.exists(id) {
				continue
			}
			var parts []string
			if data, err := os.ReadFile(filepath.Join(idDir, id+".md")); err == nil {
				parts = append(parts, string(data))
			}
			if data, err := os.ReadFile(filepath.Join(soDir, id+".md")); err == nil {
				parts = append(parts, string(data))
			}
			if len(parts) == 0 {
				continue
			}
			prompt := strings.Join(parts, "\n\n---\n\n")
			label := readLabelFile(dir, id)
			if err := m.insert(id, label, prompt, "", false); err != nil {
				log.Printf("[style] import %s: insert error: %v", id, err)
				continue
			}
			imported++
			log.Printf("[style] imported %q from identity+soul", id)
		}
	}

	if imported > 0 {
		log.Printf("[style] seed: imported %d user-defined styles from %s", imported, dir)
	}
}

func readLabelFile(dir, id string) string {
	data, err := os.ReadFile(filepath.Join(dir, id+".label"))
	if err != nil {
		return id
	}
	return strings.TrimSpace(string(data))
}

// exists checks whether a style is already in the database.
func (m *Manager) exists(id string) bool {
	var dummy int
	err := m.db.QueryRow(`SELECT 1 FROM styles WHERE id=?`, id).Scan(&dummy)
	return err == nil
}

// insert adds a single style row.
func (m *Manager) insert(id, label, prompt, memory string, builtin bool) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := m.db.Exec(
		`INSERT OR IGNORE INTO styles (id, label, prompt, memory, is_builtin, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, label, prompt, memory, boolToInt(builtin), now, now,
	)
	return err
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// GetSystemPrompt returns the full style prompt.
func (m *Manager) GetSystemPrompt(s Style) (string, error) {
	var prompt string
	err := m.db.QueryRow(`SELECT prompt FROM styles WHERE id=?`, string(s)).Scan(&prompt)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("unknown style: %s", s)
	}
	return prompt, err
}

// GetMemory returns the user-defined memory for a style.
func (m *Manager) GetMemory(s Style) (string, error) {
	var memory string
	err := m.db.QueryRow(`SELECT memory FROM styles WHERE id=?`, string(s)).Scan(&memory)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("unknown style: %s", s)
	}
	return memory, err
}

// GetIdentity returns the full prompt (compat alias for GetSystemPrompt).
func (m *Manager) GetIdentity(s Style) (string, error) {
	return m.GetSystemPrompt(s)
}

// GetSoul returns empty (deprecated, kept for API compat).
func (m *Manager) GetSoul(s Style) (string, error) {
	if !m.exists(string(s)) {
		return "", fmt.Errorf("unknown style: %s", s)
	}
	return "", nil
}

func (m *Manager) Label(s Style) string {
	return m.DisplayLabel(s)
}

func (m *Manager) List() []Style {
	return []Style{Cute, Guofeng, Tech}
}

// ListAll returns all styles, built-in first then user-defined alphabetically.
func (m *Manager) ListAll() []Style {
	rows, err := m.db.Query(`SELECT id, is_builtin FROM styles ORDER BY is_builtin DESC, id ASC`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	out := []Style{}
	extras := []Style{}
	for rows.Next() {
		var id string
		var builtin int
		if err := rows.Scan(&id, &builtin); err != nil {
			continue
		}
		s := Style(id)
		if builtin == 1 {
			out = append(out, s)
		} else {
			extras = append(extras, s)
		}
	}
	out = append(out, extras...)
	if len(out) == 0 {
		out = []Style{Cute, Guofeng, Tech}
	}
	return out
}

// DisplayLabel returns the human-readable label for a style.
func (m *Manager) DisplayLabel(s Style) string {
	if label, ok := styleLabels[s]; ok {
		return label
	}
	var label string
	if err := m.db.QueryRow(`SELECT label FROM styles WHERE id=?`, string(s)).Scan(&label); err == nil && label != "" {
		return label
	}
	return string(s)
}

// Create adds a new user-defined style.
func (m *Manager) Create(id, label, prompt, memory string) (Style, error) {
	id = normaliseStyleID(id)
	if id == "" {
		return "", fmt.Errorf("style id must contain at least one letter")
	}
	for _, b := range m.List() {
		if Style(id) == b {
			return "", fmt.Errorf("style id %q is reserved (built-in)", id)
		}
	}
	if m.exists(id) {
		return "", fmt.Errorf("style %q already exists; delete it first", id)
	}
	if prompt == "" {
		prompt = fmt.Sprintf("# %s\n\n你是一个 AI 助手。", id)
	}
	if err := m.insert(id, label, prompt, memory, false); err != nil {
		return "", fmt.Errorf("create style %q: %w", id, err)
	}
	return Style(id), nil
}

// Update overwrites an existing style.
func (m *Manager) Update(id, label, prompt, memory string) error {
	id = normaliseStyleID(id)
	if id == "" {
		return fmt.Errorf("style id must contain at least one letter")
	}
	for _, b := range m.List() {
		if Style(id) == b {
			return fmt.Errorf("built-in style %q is read-only; create a new style to customise", id)
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	sets := []string{"updated_at = ?"}
	args := []any{now}
	if label != "" {
		sets = append(sets, "label = ?")
		args = append(args, label)
	}
	if prompt != "" {
		sets = append(sets, "prompt = ?")
		args = append(args, prompt)
	}
	if memory != "" {
		sets = append(sets, "memory = ?")
		args = append(args, memory)
	}
	if len(sets) == 1 {
		return nil // nothing to update
	}
	args = append(args, id)
	_, err := m.db.Exec(`UPDATE styles SET `+strings.Join(sets, ", ")+` WHERE id=?`, args...)
	return err
}

// Delete removes a user-defined style.
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
	_, err := m.db.Exec(`DELETE FROM styles WHERE id=? AND is_builtin=0`, id)
	return err
}

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
