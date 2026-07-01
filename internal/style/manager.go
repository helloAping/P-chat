package style

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

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

// NewManager creates a style manager backed by the styles table.
// The table must already exist and contain at least the three
// built-in rows (seeded by the upgrade package on first run).
func NewManager(db *sql.DB) (*Manager, error) {
	return &Manager{db: db}, nil
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
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := m.db.Exec(
		`INSERT INTO styles (id, label, prompt, memory, is_builtin, created_at, updated_at) VALUES (?, ?, ?, ?, 0, ?, ?)`,
		id, label, prompt, memory, now, now,
	)
	if err != nil {
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
		return nil
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

func (m *Manager) exists(id string) bool {
	var dummy int
	err := m.db.QueryRow(`SELECT 1 FROM styles WHERE id=?`, id).Scan(&dummy)
	return err == nil
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
