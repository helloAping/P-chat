package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultIMConfigIsDisabledAndNormalized(t *testing.T) {
	cfg := Default()
	if cfg.IM.Enabled {
		t.Fatal("default IM bridge should be disabled")
	}
	if cfg.IM.Session.Scope != "per_thread" {
		t.Fatalf("scope = %q, want per_thread", cfg.IM.Session.Scope)
	}
	if cfg.IM.Command.Prefix != "/" {
		t.Fatalf("prefix = %q, want /", cfg.IM.Command.Prefix)
	}
	if !cfg.IM.AuditLocalOnly {
		t.Fatal("audit should default to local-only")
	}
	if cfg.IM.Personas["default"].Style != "tech" {
		t.Fatalf("default persona style = %q, want tech", cfg.IM.Personas["default"].Style)
	}
}

func TestIMConfigNormalize(t *testing.T) {
	cfg := IMConfig{
		Session: IMSessionPolicy{Scope: "nope"},
		Personas: map[string]IMPersona{
			"telegram:*": {WorkMode: WorkMode("bad")},
		},
	}
	cfg.Normalize()
	if cfg.Session.Scope != "per_thread" {
		t.Fatalf("scope = %q, want per_thread", cfg.Session.Scope)
	}
	if cfg.Command.Prefix != "/" {
		t.Fatalf("prefix = %q, want /", cfg.Command.Prefix)
	}
	if cfg.Identity.AutoLink.Trust != "manual" {
		t.Fatalf("trust = %q, want manual", cfg.Identity.AutoLink.Trust)
	}
	if cfg.Personas["telegram:*"].WorkMode != WorkModeCoding {
		t.Fatalf("work_mode = %q, want coding fallback", cfg.Personas["telegram:*"].WorkMode)
	}
	if _, ok := cfg.Personas["default"]; !ok {
		t.Fatal("Normalize should add default persona")
	}

	cfg = IMConfig{Personas: map[string]IMPersona{"feishu:*": {Style: "cute"}}}
	cfg.Normalize()
	if cfg.Personas["feishu:*"].WorkMode != "" {
		t.Fatalf("omitted work_mode = %q, want empty inherit", cfg.Personas["feishu:*"].WorkMode)
	}
}

func TestUpdateIMConfigPatchPreservesDefaults(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)
	pchatDir := filepath.Join(dir, ".p-chat")
	if err := os.MkdirAll(pchatDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pchatDir, "config.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	enabled := true
	updated, err := UpdateIMConfigPatch(IMConfigPatch{Enabled: &enabled})
	if err != nil {
		t.Fatalf("patch: %v", err)
	}
	if !updated.IM.Enabled {
		t.Fatal("enabled = false, want true")
	}
	if !updated.IM.AuditLog || !updated.IM.AuditLocalOnly {
		t.Fatalf("audit defaults lost: %+v", updated.IM)
	}
	if len(updated.IM.ToolsAllowlistDefault) == 0 {
		t.Fatal("tools allowlist default was cleared by partial patch")
	}
}
