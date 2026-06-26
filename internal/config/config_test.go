package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSubAgentConfig_Defaults(t *testing.T) {
	c := Default()
	if !c.SubAgent.ToolAllowed("read_file") {
		t.Error("read_file should be allowed by default")
	}
	if !c.SubAgent.ToolAllowed("write_file") {
		t.Error("write_file should be allowed by default")
	}
	if c.SubAgent.ToolAllowed("exec_command") {
		t.Error("exec_command should be denied by default")
	}
	if c.SubAgent.ToolAllowed("task") {
		t.Error("task should NEVER be allowed (recursion)")
	}
}

func TestSubAgentConfig_Whitelist(t *testing.T) {
	c := &SubAgentConfig{
		AllowedTools: []string{"read_file", "list_files"},
	}
	if !c.ToolAllowed("read_file") {
		t.Error("read_file should be allowed")
	}
	if !c.ToolAllowed("list_files") {
		t.Error("list_files should be allowed")
	}
	if c.ToolAllowed("write_file") {
		t.Error("write_file should be denied in whitelist mode")
	}
	if c.ToolAllowed("exec_command") {
		t.Error("exec_command should be denied in whitelist mode")
	}
}

func TestSubAgentConfig_DenyList(t *testing.T) {
	c := &SubAgentConfig{
		DeniedTools: []string{"exec_command", "write_file"},
	}
	if !c.ToolAllowed("read_file") {
		t.Error("read_file should be allowed")
	}
	if c.ToolAllowed("exec_command") {
		t.Error("exec_command should be denied")
	}
	if c.ToolAllowed("write_file") {
		t.Error("write_file should be denied")
	}
}

func TestSubAgentConfig_Timeout(t *testing.T) {
	// Default
	c := &SubAgentConfig{}
	if got := c.TimeoutDuration(); got != 5*time.Minute {
		t.Errorf("default timeout = %v, want 5m", got)
	}

	// Custom
	c = &SubAgentConfig{Timeout: "30s"}
	if got := c.TimeoutDuration(); got != 30*time.Second {
		t.Errorf("custom timeout = %v, want 30s", got)
	}

	// Invalid
	c = &SubAgentConfig{Timeout: "garbage"}
	if got := c.TimeoutDuration(); got != 5*time.Minute {
		t.Errorf("invalid timeout should fall back to 5m, got %v", got)
	}
}

func TestSubAgentConfig_CacheTTL(t *testing.T) {
	// Default (disabled)
	c := &SubAgentConfig{}
	if got := c.CacheTTLDuration(); got != 0 {
		t.Errorf("default cache TTL = %v, want 0 (disabled)", got)
	}

	// Custom
	c = &SubAgentConfig{CacheTTL: "5m"}
	if got := c.CacheTTLDuration(); got != 5*time.Minute {
		t.Errorf("custom cache TTL = %v, want 5m", got)
	}
}

func TestProviderConfig_MultiModel(t *testing.T) {
	p := ProviderConfig{
		Name:    "openai",
		BaseURL: "https://api.openai.com/v1",
		APIKey:  "sk-test",
		Models: []ModelConfig{
			{Name: "gpt-4o-mini", Default: true, Description: "fast"},
			{Name: "gpt-4o", Description: "smart"},
			{Name: "o1-preview"},
		},
	}

	// EffectiveModel picks the default.
	if got := p.EffectiveModel(); got != "gpt-4o-mini" {
		t.Errorf("EffectiveModel = %q, want gpt-4o-mini", got)
	}

	// AllModels returns all 3.
	models := p.AllModels()
	if len(models) != 3 {
		t.Errorf("AllModels len = %d, want 3", len(models))
	}

	// DisplayModel falls back to the id when no DisplayName.
	if got := p.DisplayModel(); got != "gpt-4o-mini" {
		t.Errorf("DisplayModel = %q, want gpt-4o-mini", got)
	}

	// Set DisplayName.
	p.Models[0].DisplayName = "GPT-4o Mini"
	if got := p.DisplayModel(); got != "GPT-4o Mini" {
		t.Errorf("DisplayModel with display_name = %q", got)
	}
}

func TestProviderConfig_LegacySingleModel(t *testing.T) {
	p := ProviderConfig{
		Name:  "oldstyle",
		Model: "gpt-3.5-turbo",
	}
	if got := p.EffectiveModel(); got != "gpt-3.5-turbo" {
		t.Errorf("legacy model: EffectiveModel = %q, want gpt-3.5-turbo", got)
	}
	models := p.AllModels()
	if len(models) != 1 || models[0].Name != "gpt-3.5-turbo" {
		t.Errorf("legacy AllModels = %+v, want one entry 'gpt-3.5-turbo'", models)
	}
}

func TestProviderConfig_MultiModelFirstWins(t *testing.T) {
	// When no model is marked default, the first one wins.
	p := ProviderConfig{
		Models: []ModelConfig{
			{Name: "first"},
			{Name: "second"},
		},
	}
	if got := p.EffectiveModel(); got != "first" {
		t.Errorf("EffectiveModel = %q, want first", got)
	}
}

func TestAddModel(t *testing.T) {
	// Use a temp HOME so the global config doesn't pollute the user's.
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)

	// Seed: write a known config with one provider that has a
	// legacy `model` field.
	initial := `server:
  host: 127.0.0.1
  port: 8960
llm:
  default: cs
  providers:
    - name: cs
      protocol: openai
      base_url: http://example.com/v1
      api_key: sk-x
      model: doubao-seed-2.0-lite
`
	cfgPath := dir + "/.p-chat/config.yaml"
	if err := osWriteFile(cfgPath, initial); err != nil {
		t.Fatal(err)
	}

	// Add a new model to the existing provider.
	updated, err := AddModel("cs", ModelConfig{
		Name:        "doubao-pro",
		Description: "Pro model",
	})
	if err != nil {
		t.Fatalf("AddModel: %v", err)
	}
	if len(updated.Models) != 2 {
		t.Errorf("expected 2 models, got %d", len(updated.Models))
	}
	// First model (the legacy one) should still be default.
	if updated.Models[0].Name != "doubao-seed-2.0-lite" || !updated.Models[0].Default {
		t.Errorf("first model should still be default, got %+v", updated.Models[0])
	}

	// Reload from disk and confirm persistence.
	cfg, _ := Load("")
	if cfg == nil {
		t.Fatal("config not persisted")
	}
	if len(cfg.LLM.Providers) != 1 {
		t.Fatalf("providers: %d", len(cfg.LLM.Providers))
	}
	if len(cfg.LLM.Providers[0].Models) != 2 {
		t.Errorf("models after reload: %d", len(cfg.LLM.Providers[0].Models))
	}
}

func TestAddModel_RejectsDuplicate(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)

	initial := `llm:
  default: cs
  providers:
    - name: cs
      protocol: openai
      base_url: http://example.com/v1
      api_key: sk-x
      models:
        - name: foo
`
	if err := osWriteFile(dir+"/.p-chat/config.yaml", initial); err != nil {
		t.Fatal(err)
	}

	_, err := AddModel("cs", ModelConfig{Name: "foo"})
	if err == nil {
		t.Error("expected error for duplicate model")
	}
}

func TestAddModel_SetsNewDefaultWhenFirst(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)

	initial := `llm:
  default: cs
  providers:
    - name: cs
      protocol: openai
      base_url: http://example.com/v1
      api_key: sk-x
`
	if err := osWriteFile(dir+"/.p-chat/config.yaml", initial); err != nil {
		t.Fatal(err)
	}
	// No models yet; adding the first one should make it default.
	updated, err := AddModel("cs", ModelConfig{Name: "first"})
	if err != nil {
		t.Fatal(err)
	}
	if !updated.Models[0].Default {
		t.Error("first added model should be default")
	}
}

func TestRemoveModel(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)

	initial := `llm:
  default: cs
  providers:
    - name: cs
      protocol: openai
      base_url: http://example.com/v1
      api_key: sk-x
      models:
        - name: a
        - name: b
        - name: c
`
	if err := osWriteFile(dir+"/.p-chat/config.yaml", initial); err != nil {
		t.Fatal(err)
	}

	if err := RemoveModel("cs", "b"); err != nil {
		t.Fatal(err)
	}
	cfg, _ := Load("")
	got := cfg.LLM.Providers[0].Models
	if len(got) != 2 {
		t.Fatalf("expected 2 models, got %d", len(got))
	}
	if got[0].Name != "a" || got[1].Name != "c" {
		t.Errorf("order wrong: %+v", got)
	}
}

func TestSetDefaultModel(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)

	initial := `llm:
  default: cs
  providers:
    - name: cs
      protocol: openai
      base_url: http://example.com/v1
      api_key: sk-x
      models:
        - name: a
          default: true
        - name: b
`
	if err := osWriteFile(dir+"/.p-chat/config.yaml", initial); err != nil {
		t.Fatal(err)
	}
	if err := SetDefaultModel("cs", "b"); err != nil {
		t.Fatal(err)
	}
	cfg, _ := Load("")
	for _, m := range cfg.LLM.Providers[0].Models {
		if m.Name == "a" && m.Default {
			t.Error("a should no longer be default")
		}
		if m.Name == "b" && !m.Default {
			t.Error("b should now be default")
		}
	}
}

func TestRemoveModel_NotFound(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)

	initial := `llm:
  default: cs
  providers:
    - name: cs
      protocol: openai
      base_url: http://example.com/v1
      api_key: sk-x
      models:
        - name: a
`
	if err := osWriteFile(dir+"/.p-chat/config.yaml", initial); err != nil {
		t.Fatal(err)
	}
	if err := RemoveModel("cs", "missing"); err == nil {
		t.Error("expected error for missing model")
	}
	if err := RemoveModel("nonexistent", "a"); err == nil {
		t.Error("expected error for missing provider")
	}
}

// osWriteFile is a tiny test helper that creates the parent dir
// and writes the file in one call.
func osWriteFile(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(content), 0o644)
}
