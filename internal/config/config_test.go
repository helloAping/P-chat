package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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
	initial := `{
  "server": { "host": "127.0.0.1", "port": 8960 },
  "llm": {
    "default": "cs",
    "providers": [
      {
        "name": "cs",
        "protocol": "openai",
        "base_url": "http://example.com/v1",
        "api_key": "sk-x",
        "model": "doubao-seed-2.0-lite"
      }
    ]
  }
}`
	cfgPath := dir + "/.p-chat/config.json"
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

	initial := `{
  "llm": {
    "default": "cs",
    "providers": [
      { "name": "cs", "protocol": "openai", "base_url": "http://example.com/v1", "api_key": "sk-x", "models": [{ "name": "foo" }] }
    ]
  }
}`
	if err := osWriteFile(dir+"/.p-chat/config.json", initial); err != nil {
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

	initial := `{
  "llm": {
    "default": "cs",
    "providers": [
      { "name": "cs", "protocol": "openai", "base_url": "http://example.com/v1", "api_key": "sk-x" }
    ]
  }
}`
	if err := osWriteFile(dir+"/.p-chat/config.json", initial); err != nil {
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

	initial := `{
  "llm": {
    "default": "cs",
    "providers": [
      { "name": "cs", "protocol": "openai", "base_url": "http://example.com/v1", "api_key": "sk-x", "models": [{ "name": "a" }, { "name": "b" }, { "name": "c" }] }
    ]
  }
}`
	if err := osWriteFile(dir+"/.p-chat/config.json", initial); err != nil {
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

	initial := `{
  "llm": {
    "default": "cs",
    "providers": [
      { "name": "cs", "protocol": "openai", "base_url": "http://example.com/v1", "api_key": "sk-x", "models": [{ "name": "a", "default": true }, { "name": "b" }] }
    ]
  }
}`
	if err := osWriteFile(dir+"/.p-chat/config.json", initial); err != nil {
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

	initial := `{
  "llm": {
    "default": "cs",
    "providers": [
      { "name": "cs", "protocol": "openai", "base_url": "http://example.com/v1", "api_key": "sk-x", "models": [{ "name": "a" }] }
    ]
  }
}`
	if err := osWriteFile(dir+"/.p-chat/config.json", initial); err != nil {
		t.Fatal(err)
	}
	if err := RemoveModel("cs", "missing"); err == nil {
		t.Error("expected error for missing model")
	}
	if err := RemoveModel("nonexistent", "a"); err == nil {
		t.Error("expected error for missing provider")
	}
}

func TestUpdateModel_PerModelMaxTokens(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)

	initial := `{
  "llm": {
    "default": "cs",
    "providers": [
      { "name": "cs", "protocol": "openai", "base_url": "http://example.com/v1", "api_key": "sk-x", "models": [{ "name": "small", "max_tokens_output": 1024 }, { "name": "big" }] }
    ]
  }
}`
	if err := osWriteFile(dir+"/.p-chat/config.json", initial); err != nil {
		t.Fatal(err)
	}

	// Apply a patch: set big's display_name + output cap.
	_, err := UpdateModel("cs", "big", ModelConfig{
		DisplayName:     "Big Model",
		MaxTokensOutput: 8192,
	}, false)
	if err != nil {
		t.Fatalf("UpdateModel: %v", err)
	}

	cfg, _ := Load("")
	big := cfg.LLM.Providers[0].Models[1]
	if big.DisplayName != "Big Model" {
		t.Errorf("display_name = %q, want %q", big.DisplayName, "Big Model")
	}
	if big.MaxTokensOutput != 8192 {
		t.Errorf("max_tokens_output = %d, want 8192", big.MaxTokensOutput)
	}
	// The other model should be untouched.
	small := cfg.LLM.Providers[0].Models[0]
	if small.MaxTokensOutput != 1024 {
		t.Errorf("small max_tokens_output = %d, want 1024 (unchanged)", small.MaxTokensOutput)
	}
}

func TestUpdateModel_ClearAll(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)

	initial := `{
  "llm": {
    "default": "cs",
    "providers": [
      { "name": "cs", "protocol": "openai", "base_url": "http://example.com/v1", "api_key": "sk-x", "models": [{ "name": "m", "display_name": "M", "max_tokens_output": 4096 }] }
    ]
  }
}`
	if err := osWriteFile(dir+"/.p-chat/config.json", initial); err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateModel("cs", "m", ModelConfig{}, true); err != nil {
		t.Fatal(err)
	}
	cfg, _ := Load("")
	m := cfg.LLM.Providers[0].Models[0]
	if m.DisplayName != "" || m.MaxTokensOutput != 0 {
		t.Errorf("clear_all didn't reset: %+v", m)
	}
}

func TestUpdateModel_NotFound(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)

	initial := `{
  "llm": {
    "default": "cs",
    "providers": [
      { "name": "cs", "protocol": "openai", "base_url": "http://example.com/v1", "api_key": "sk-x", "models": [{ "name": "a" }] }
    ]
  }
}`
	if err := osWriteFile(dir+"/.p-chat/config.json", initial); err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateModel("cs", "missing", ModelConfig{MaxTokensOutput: 100}, false); err == nil {
		t.Error("expected error for missing model")
	}
	if _, err := UpdateModel("nonexistent", "a", ModelConfig{MaxTokensOutput: 100}, false); err == nil {
		t.Error("expected error for missing provider")
	}
}

func TestLoad_MigratesLegacyYAMLToJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)

	// Write a legacy yaml file.
	yamlPath := dir + "/.p-chat/config.yaml"
	yamlContent := `server:
  host: 127.0.0.1
  port: 8960
llm:
  default: cs
  providers:
    - name: cs
      protocol: openai
      base_url: http://example.com/v1
      api_key: sk-x
      models:
        - name: doubao-seed-2.0-lite
          max_tokens_output: 4096
        - name: deepseek-v4-flash
          max_tokens_context: 128000
`
	if err := osWriteFile(yamlPath, yamlContent); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.LLM.Default != "cs" {
		t.Errorf("default = %q, want cs", cfg.LLM.Default)
	}
	if len(cfg.LLM.Providers) != 1 {
		t.Fatalf("providers: %d", len(cfg.LLM.Providers))
	}
	p := cfg.LLM.Providers[0]
	if len(p.Models) != 2 {
		t.Fatalf("models: %d, want 2", len(p.Models))
	}
	if p.Models[0].Name != "doubao-seed-2.0-lite" || p.Models[0].MaxTokensOutput != 4096 {
		t.Errorf("migrated model 0 wrong: %+v", p.Models[0])
	}
	if p.Models[1].Name != "deepseek-v4-flash" || p.Models[1].MaxTokensContext != 128000 {
		t.Errorf("migrated model 1 wrong: %+v", p.Models[1])
	}

	// After migration, the JSON file should exist.
	jsonPath := dir + "/.p-chat/config.json"
	if _, err := os.Stat(jsonPath); err != nil {
		t.Errorf("expected config.json to be created: %v", err)
	}
	// The yaml file is preserved (so the user can roll back).
	if _, err := os.Stat(yamlPath); err != nil {
		t.Errorf("expected config.yaml to be preserved: %v", err)
	}
}

func TestLoad_NoConfigFallsBackToDefault(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Server.Host != "127.0.0.1" {
		t.Errorf("default server.host = %q, want 127.0.0.1", cfg.Server.Host)
	}
}

func TestProviderConfig_FindModel(t *testing.T) {
	p := ProviderConfig{
		Name: "x",
		Models: []ModelConfig{
			{Name: "a", Default: true, MaxTokensOutput: 1024},
			{Name: "b", MaxTokensContext: 128000},
		},
	}
	if p.FindModel("a") == nil {
		t.Error("FindModel(a) = nil")
	}
	if p.FindModel("missing") != nil {
		t.Error("FindModel(missing) should be nil")
	}
}

func TestModelConfig_JSONRoundTrip(t *testing.T) {
	// Make sure MaxTokensContext / MaxTokensOutput survive
	// JSON encode + decode (no yaml tag interference).
	orig := ModelConfig{
		Name:             "x",
		DisplayName:      "X",
		Default:          true,
		MaxTokensContext: 128000,
		MaxTokensOutput:  8192,
		Capabilities: Capabilities{
			ThinkingEffort: ThinkingEffortHigh,
		},
	}
	data, err := jsonMarshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(data, `"max_tokens_context":128000`) {
		t.Errorf("json should contain max_tokens_context:128000: %s", data)
	}
	if !contains(data, `"max_tokens_output":8192`) {
		t.Errorf("json should contain max_tokens_output:8192: %s", data)
	}
	var got ModelConfig
	if err := jsonUnmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got.MaxTokensContext != 128000 || got.MaxTokensOutput != 8192 {
		t.Errorf("roundtrip lost fields: %+v", got)
	}
	if got.Capabilities.ThinkingEffort != ThinkingEffortHigh {
		t.Errorf("roundtrip lost thinking effort: %+v", got)
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

// jsonMarshal / jsonUnmarshal are thin wrappers so the
// TestModelConfig_JSONRoundTrip test can verify the field tags
// without depending on the implementation file.
func jsonMarshal(v any) ([]byte, error)    { return json.Marshal(v) }
func jsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }

// contains is a tiny helper around strings.Contains.
func contains(haystack []byte, needle string) bool {
	return strings.Contains(string(haystack), needle)
}

func TestLoad_StripsBOMFromConfig(t *testing.T) {
	// Windows tools (Notepad, PowerShell Set-Content with -Encoding
	// UTF8, .NET StreamWriter with BOM=true) often save UTF-8
	// files with a leading EF BB BF byte sequence. Go's
	// encoding/json refuses to parse such a file, so Load must
	// strip the BOM before unmarshalling.
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)

	bom := []byte{0xEF, 0xBB, 0xBF}
	clean := []byte(`{"llm":{"default":"cs","providers":[{"name":"cs","protocol":"openai","base_url":"http://x","api_key":"k","model":"m"}]}}`)
	data := append(append([]byte{}, bom...), clean...)

	if err := osWriteFile(dir+"/.p-chat/config.json", string(data)); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load with BOM should succeed, got: %v", err)
	}
	if cfg.LLM.Default != "cs" {
		t.Errorf("default = %q, want cs", cfg.LLM.Default)
	}
}

func TestStripBOM(t *testing.T) {
	clean := []byte(`{"a":1}`)
	if got := stripBOM(clean); string(got) != string(clean) {
		t.Errorf("no BOM should pass through unchanged")
	}
	bom := append([]byte{0xEF, 0xBB, 0xBF}, clean...)
	if got := stripBOM(bom); string(got) != string(clean) {
		t.Errorf("BOM should be stripped, got %q", got)
	}
	short := []byte{0xEF, 0xBB}
	if got := stripBOM(short); string(got) != string(short) {
		t.Errorf("short data should pass through unchanged")
	}
}

// TestUpdateProvider_Rename_CascadesDefault verifies that
// renaming a provider that is the global default also
// updates cfg.LLM.Default.
func TestUpdateProvider_Rename_CascadesDefault(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)
	initial := `{
  "llm": {
    "default": "cs",
    "providers": [
      { "name": "cs", "protocol": "openai", "base_url": "http://x", "api_key": "k", "model": "m" }
    ]
  }
}`
	if err := osWriteFile(dir+"/.p-chat/config.json", initial); err != nil {
		t.Fatal(err)
	}
	updated, err := UpdateProvider("cs", ProviderPatch{Name: "cs-renamed"})
	if err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}
	if updated.Name != "cs-renamed" {
		t.Errorf("returned name = %q, want cs-renamed", updated.Name)
	}
	cfg, _ := Load("")
	if cfg.LLM.Default != "cs-renamed" {
		t.Errorf("default = %q, want cs-renamed (cascade)", cfg.LLM.Default)
	}
	if len(cfg.LLM.Providers) != 1 || cfg.LLM.Providers[0].Name != "cs-renamed" {
		t.Errorf("provider list = %+v", cfg.LLM.Providers)
	}
}

// TestUpdateProvider_Rename_CollisionRejected ensures a
// rename that lands on an existing name is rejected before
// disk is touched.
func TestUpdateProvider_Rename_CollisionRejected(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)
	initial := `{
  "llm": {
    "default": "cs",
    "providers": [
      { "name": "cs", "protocol": "openai", "base_url": "http://a", "api_key": "k1", "model": "m" },
      { "name": "other", "protocol": "openai", "base_url": "http://b", "api_key": "k2", "model": "n" }
    ]
  }
}`
	if err := osWriteFile(dir+"/.p-chat/config.json", initial); err != nil {
		t.Fatal(err)
	}
	_, err := UpdateProvider("cs", ProviderPatch{Name: "other"})
	if err == nil {
		t.Fatal("expected collision error")
	}
	cfg, _ := Load("")
	if cfg.LLM.Providers[0].Name != "cs" {
		t.Errorf("provider name changed on error: %q", cfg.LLM.Providers[0].Name)
	}
}

// TestUpdateProvider_ProtocolChange verifies that the
// protocol field can be switched between openai/anthropic and
// is persisted.
func TestUpdateProvider_ProtocolChange(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)
	initial := `{
  "llm": {
    "default": "cs",
    "providers": [
      { "name": "cs", "protocol": "openai", "base_url": "http://x", "api_key": "k", "model": "m" }
    ]
  }
}`
	if err := osWriteFile(dir+"/.p-chat/config.json", initial); err != nil {
		t.Fatal(err)
	}
	updated, err := UpdateProvider("cs", ProviderPatch{Protocol: "anthropic"})
	if err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}
	if updated.GetProtocol() != "anthropic" {
		t.Errorf("protocol = %q, want anthropic", updated.GetProtocol())
	}
	cfg, _ := Load("")
	if cfg.LLM.Providers[0].GetProtocol() != "anthropic" {
		t.Errorf("reloaded protocol = %q", cfg.LLM.Providers[0].GetProtocol())
	}
}

// TestUpdateProvider_ProtocolInvalid verifies a typo in
// the protocol field is rejected.
func TestUpdateProvider_ProtocolInvalid(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)
	initial := `{
  "llm": {
    "default": "cs",
    "providers": [
      { "name": "cs", "protocol": "openai", "base_url": "http://x", "api_key": "k", "model": "m" }
    ]
  }
}`
	if err := osWriteFile(dir+"/.p-chat/config.json", initial); err != nil {
		t.Fatal(err)
	}
	_, err := UpdateProvider("cs", ProviderPatch{Protocol: "gopher"})
	if err == nil {
		t.Fatal("expected invalid protocol error")
	}
}

// TestUpdateProvider_PartialPatch verifies that omitted
// fields are left untouched: changing the base URL must not
// wipe the API key.
func TestUpdateProvider_PartialPatch(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)
	initial := `{
  "llm": {
    "default": "cs",
    "providers": [
      { "name": "cs", "protocol": "openai", "base_url": "http://old", "api_key": "sk-original", "model": "m" }
    ]
  }
}`
	if err := osWriteFile(dir+"/.p-chat/config.json", initial); err != nil {
		t.Fatal(err)
	}
	updated, err := UpdateProvider("cs", ProviderPatch{BaseURL: "http://new"})
	if err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}
	if updated.BaseURL != "http://new" {
		t.Errorf("BaseURL = %q, want http://new", updated.BaseURL)
	}
	if updated.APIKey != "sk-original" {
		t.Errorf("APIKey = %q, want sk-original (must be preserved on partial patch)", updated.APIKey)
	}
}

// TestUpdateProvider_SetDefault verifies IsDefault promotes
// the target provider to the global default and leaves the
// others alone.
func TestUpdateProvider_SetDefault(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)
	initial := `{
  "llm": {
    "default": "cs",
    "providers": [
      { "name": "cs", "protocol": "openai", "base_url": "http://a", "api_key": "k1", "model": "m" },
      { "name": "newdefault", "protocol": "openai", "base_url": "http://b", "api_key": "k2", "model": "n" }
    ]
  }
}`
	if err := osWriteFile(dir+"/.p-chat/config.json", initial); err != nil {
		t.Fatal(err)
	}
	if _, err := UpdateProvider("newdefault", ProviderPatch{IsDefault: true}); err != nil {
		t.Fatalf("UpdateProvider: %v", err)
	}
	cfg, _ := Load("")
	if cfg.LLM.Default != "newdefault" {
		t.Errorf("default = %q, want newdefault", cfg.LLM.Default)
	}
}

// TestUpdateProvider_NotFound covers the simple
// error case: target provider doesn't exist.
func TestUpdateProvider_NotFound(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)
	initial := `{"llm":{"default":"cs","providers":[{"name":"cs","protocol":"openai","base_url":"http://x","api_key":"k","model":"m"}]}}`
	if err := osWriteFile(dir+"/.p-chat/config.json", initial); err != nil {
		t.Fatal(err)
	}
	_, err := UpdateProvider("ghost", ProviderPatch{Name: "new"})
	if err == nil {
		t.Fatal("expected not-found error")
	}
}

func TestKnowledgeConfig_Defaults(t *testing.T) {
	cfg := Default()
	if cfg.Knowledge.Enabled {
		t.Error("knowledge should be disabled by default")
	}
	if cfg.Knowledge.AutoIndex {
		t.Error("auto_index should be false by default")
	}
	if len(cfg.Knowledge.Bases) != 0 {
		t.Errorf("expected 0 default knowledge bases, got %d", len(cfg.Knowledge.Bases))
	}
}

func TestKnowledgeConfig_MigrationOldJSON(t *testing.T) {
	oldJSON := `{"llm":{"default":"cs","providers":[{"name":"cs","protocol":"openai","base_url":"http://x","api_key":"k","model":"m"}]}}`
	var cfg Config
	if err := json.Unmarshal([]byte(oldJSON), &cfg); err != nil {
		t.Fatal(err)
	}
	migrateKnowledgeDefaults(&cfg)

	if cfg.Knowledge.Enabled {
		t.Error("migration should leave knowledge disabled")
	}
	if cfg.Knowledge.AutoIndex {
		t.Error("migration should leave auto_index false")
	}
}

func TestKnowledgeConfig_MigrationPreservesExisting(t *testing.T) {
	existingJSON := `{"llm":{"default":"cs"},"knowledge":{"enabled":true}}`
	var cfg Config
	if err := json.Unmarshal([]byte(existingJSON), &cfg); err != nil {
		t.Fatal(err)
	}
	migrateKnowledgeDefaults(&cfg)

	if !cfg.Knowledge.Enabled {
		t.Error("Enabled should still be true")
	}
}

func TestKnowledgeConfig_LoadOldConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)

	oldJSON := `{"llm":{"default":"cs","providers":[{"name":"cs","protocol":"openai","base_url":"http://x","api_key":"k","model":"m"}]}}`
	if err := osWriteFile(filepath.Join(dir, ".p-chat", "config.json"), oldJSON); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Knowledge.Enabled {
		t.Error("knowledge should be disabled after loading old config")
	}
	if cfg.LLM.Default != "cs" {
		t.Errorf("llm.default should be 'cs', got %q", cfg.LLM.Default)
	}
}