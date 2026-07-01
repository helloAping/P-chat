package style

import (
	"database/sql"
	"testing"

	_ "modernc.org/sqlite"
)

func testDB(t *testing.T) *sql.DB {
	db, err := sql.Open("sqlite", ":memory:?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	// Create the styles table (normally done by memory migration V3).
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS styles (
    id          TEXT PRIMARY KEY,
    label       TEXT NOT NULL DEFAULT '',
    prompt      TEXT NOT NULL DEFAULT '',
    memory      TEXT NOT NULL DEFAULT '',
    is_builtin  INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL DEFAULT (datetime('now')),
    updated_at  TEXT NOT NULL DEFAULT (datetime('now'))
)`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestStyle_DisplayName(t *testing.T) {
	cases := map[Style]string{
		Cute:    "可爱风",
		Guofeng: "古风",
		Tech:    "科技风",
	}
	for s, want := range cases {
		if got := s.DisplayName(); got != want {
			t.Errorf("Style(%q).DisplayName() = %q, want %q", s, got, want)
		}
	}
	if got := Style("nonexistent").DisplayName(); got != "nonexistent" {
		t.Errorf("unknown style should fall back to id, got %q", got)
	}
}

func TestParseStyle(t *testing.T) {
	cases := []struct {
		in   string
		want Style
		err  bool
	}{
		{"cute", Cute, false},
		{"Cute", Cute, false},
		{"小P", Cute, false},
		{"可爱", Cute, false},
		{"可爱风", Cute, false},
		{"guofeng", Guofeng, false},
		{"墨言", Guofeng, false},
		{"古风", Guofeng, false},
		{"tech", Tech, false},
		{"NEXUS", Tech, false},
		{"零号", Tech, false},
		{"科技风", Tech, false},
		{"nonexistent", "", true},
		{"", "", true},
	}
	for _, c := range cases {
		got, err := ParseStyle(c.in)
		if c.err {
			if err == nil {
				t.Errorf("ParseStyle(%q) = %q, expected error", c.in, got)
			}
		} else {
			if err != nil {
				t.Errorf("ParseStyle(%q) returned err: %v", c.in, err)
			}
			if got != c.want {
				t.Errorf("ParseStyle(%q) = %q, want %q", c.in, got, c.want)
			}
		}
	}
}

func TestManager_BuiltinSeed(t *testing.T) {
	db := testDB(t)
	m, err := NewManager(db)
	if err != nil {
		t.Fatal(err)
	}

	for _, s := range []Style{Cute, Guofeng, Tech} {
		prompt, err := m.GetSystemPrompt(s)
		if err != nil {
			t.Errorf("GetSystemPrompt(%s): %v", s, err)
		}
		if prompt == "" {
			t.Errorf("style %s has empty prompt", s)
		}
	}
}

func TestManager_UnknownStyle(t *testing.T) {
	db := testDB(t)
	m, err := NewManager(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.GetSystemPrompt("nope"); err == nil {
		t.Error("expected error for unknown style")
	}
}

func TestManager_CRUD_UserStyle(t *testing.T) {
	db := testDB(t)
	m, err := NewManager(db)
	if err != nil {
		t.Fatal(err)
	}

	// Create
	s, err := m.Create("mystyle", "My Label", "# Hello\n\nTest prompt.", "memory content")
	if err != nil {
		t.Fatal(err)
	}
	if s != "mystyle" {
		t.Errorf("expected mystyle, got %s", s)
	}

	// Read
	prompt, err := m.GetSystemPrompt("mystyle")
	if err != nil {
		t.Fatal(err)
	}
	if prompt != "# Hello\n\nTest prompt." {
		t.Errorf("unexpected prompt: %q", prompt)
	}

	mem, _ := m.GetMemory("mystyle")
	if mem != "memory content" {
		t.Errorf("unexpected memory: %q", mem)
	}

	// Label
	if m.DisplayLabel("mystyle") != "My Label" {
		t.Errorf("unexpected label: %q", m.DisplayLabel("mystyle"))
	}

	// Update
	if err := m.Update("mystyle", "Updated", "# Updated", "new mem"); err != nil {
		t.Fatal(err)
	}
	prompt, _ = m.GetSystemPrompt("mystyle")
	if prompt != "# Updated" {
		t.Errorf("prompt not updated: %q", prompt)
	}

	// Delete
	if err := m.Delete("mystyle"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.GetSystemPrompt("mystyle"); err == nil {
		t.Error("expected error after delete")
	}
}

func TestManager_ListAll(t *testing.T) {
	db := testDB(t)
	m, err := NewManager(db)
	if err != nil {
		t.Fatal(err)
	}

	all := m.ListAll()
	if len(all) < 3 {
		t.Fatalf("expected at least 3 built-in styles, got %d", len(all))
	}
	// Built-ins must include the core three.
	has := func(s Style) bool {
		for _, x := range all {
			if x == s {
				return true
			}
		}
		return false
	}
	if !has(Cute) || !has(Guofeng) || !has(Tech) {
		t.Errorf("missing built-in style in list: %v", all)
	}
}

func TestManager_CannotDeleteBuiltin(t *testing.T) {
	db := testDB(t)
	m, err := NewManager(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Delete("tech"); err == nil {
		t.Error("expected error deleting built-in tech")
	}
}

func TestManager_CannotUpdateBuiltin(t *testing.T) {
	db := testDB(t)
	m, err := NewManager(db)
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Update("tech", "x", "x", "x"); err == nil {
		t.Error("expected error updating built-in tech")
	}
}
