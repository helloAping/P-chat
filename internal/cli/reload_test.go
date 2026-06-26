package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// TestReloadLLMClient_AfterAddModel reproduces the bug where
// `/config model add` wrote to yaml but the REPL's in-memory
// LLM client still had the old model list, so `/model` couldn't
// find the newly added model.
func TestReloadLLMClient_AfterAddModel(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("USERPROFILE", dir)
	t.Setenv("HOME", dir)

	// Seed config with one provider that has only the legacy
	// single-model form.
	initial := `llm:
  default: cs
  providers:
    - name: cs
      protocol: openai
      base_url: http://example.com/v1
      api_key: sk-x
      model: doubao-seed-2.0-lite
`
	if err := os.MkdirAll(filepath.Join(dir, ".p-chat"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".p-chat", "config.yaml"), []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	// Simulate the REPL starting up.
	llmClient, err := buildTestLLMClient()
	if err != nil {
		t.Fatal(err)
	}
	r := &REPL{llm: llmClient, provider: "cs"}

	// Before add: only the legacy model is visible.
	models, _ := r.llm.ModelsFor("cs")
	if len(models) != 1 || models[0].Name != "doubao-seed-2.0-lite" {
		t.Fatalf("pre-add: expected 1 model 'doubao-seed-2.0-lite', got %+v", models)
	}

	// Add a new model via the same code path the CLI uses.
	if err := addModelForTest("cs", "deepseek-v4-flash", "DeepSeek", ""); err != nil {
		t.Fatal(err)
	}

	// Reload the REPL's LLM client.
	if err := r.reloadLLMClient(); err != nil {
		t.Fatal(err)
	}

	// After add: both models should be visible.
	models, _ = r.llm.ModelsFor("cs")
	if len(models) != 2 {
		t.Fatalf("post-add: expected 2 models, got %d", len(models))
	}
	found := false
	for _, m := range models {
		if m.Name == "deepseek-v4-flash" {
			found = true
		}
	}
	if !found {
		t.Errorf("post-add: deepseek-v4-flash not found in %+v", models)
	}

	// And the simulated /model cs/deepseek-v4-flash switch should work.
	if err := r.llm.SetModel("cs", "deepseek-v4-flash"); err != nil {
		t.Fatalf("SetModel: %v", err)
	}
	if got := r.llm.GetModel("cs"); got != "deepseek-v4-flash" {
		t.Errorf("GetModel = %q, want deepseek-v4-flash", got)
	}
}

// TestReloadLLMClient_PreservesActiveModel verifies that reloading
// doesn't lose the user's current model selection.
func TestReloadLLMClient_PreservesActiveModel(t *testing.T) {
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
      model: doubao-seed-2.0-lite
`
	if err := os.MkdirAll(filepath.Join(dir, ".p-chat"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".p-chat", "config.yaml"), []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	llmClient, _ := buildTestLLMClient()
	r := &REPL{llm: llmClient, provider: "cs"}

	// Add a second model.
	if err := addModelForTest("cs", "deepseek-v4-flash", "", ""); err != nil {
		t.Fatal(err)
	}
	// Reload so the client sees it.
	if err := r.reloadLLMClient(); err != nil {
		t.Fatal(err)
	}
	if err := r.llm.SetModel("cs", "deepseek-v4-flash"); err != nil {
		t.Fatal(err)
	}
	if r.llm.GetModel("cs") != "deepseek-v4-flash" {
		t.Fatalf("expected to be on deepseek-v4-flash before reload")
	}

	// Reload again (simulating another /config add).
	if err := addModelForTest("cs", "another-model", "", ""); err != nil {
		t.Fatal(err)
	}
	if err := r.reloadLLMClient(); err != nil {
		t.Fatal(err)
	}

	// Active model should still be deepseek-v4-flash.
	if r.llm.GetModel("cs") != "deepseek-v4-flash" {
		t.Errorf("after reload, model should be preserved; got %q", r.llm.GetModel("cs"))
	}
}

// TestReloadLLMClient_FallsBackToDefaultWhenProviderRemoved
// verifies the safety net: if the user deletes the current
// provider, the REPL falls back to the configured default.
func TestReloadLLMClient_FallsBackToDefaultWhenProviderRemoved(t *testing.T) {
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
      model: doubao-seed-2.0-lite
    - name: other
      protocol: openai
      base_url: http://other.com/v1
      api_key: sk-y
      model: foo
`
	if err := os.MkdirAll(filepath.Join(dir, ".p-chat"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".p-chat", "config.yaml"), []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	llmClient, _ := buildTestLLMClient()
	r := &REPL{llm: llmClient, provider: "other"}

	// Remove the "other" provider from yaml.
	cfg, _ := configLoadForTest()
	cfg.LLM.Providers = cfg.LLM.Providers[:1] // keep only "cs"
	if err := configSaveForTest(cfg); err != nil {
		t.Fatal(err)
	}

	if err := r.reloadLLMClient(); err != nil {
		t.Fatal(err)
	}
	if r.provider != "cs" {
		t.Errorf("expected fallback to default 'cs', got %q", r.provider)
	}
}
