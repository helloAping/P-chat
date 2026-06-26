package style

import (
	"os"
	"path/filepath"
	"testing"
)

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

func TestManager_FallbackToBuiltins(t *testing.T) {
	// Pass a directory that doesn't exist to force the fallback path.
	dir := t.TempDir()
	// Remove the prompts subdir so the manager uses its built-in defaults.
	_ = os.RemoveAll(filepath.Join(dir, "identity"))
	_ = os.RemoveAll(filepath.Join(dir, "soul"))

	m, err := NewManager(dir)
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
	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.GetSystemPrompt("nope"); err == nil {
		t.Error("expected error for unknown style")
	}
}
