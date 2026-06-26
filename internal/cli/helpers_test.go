package cli

import (
	"os"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/paths"
	"gopkg.in/yaml.v3"
)

// buildTestLLMClient loads the global config (which the test sets
// up via USERPROFILE / HOME env) and returns a fresh LLM client.
// Used by tests that exercise the in-memory client + config.
func buildTestLLMClient() (*llm.Client, error) {
	cfg, err := config.Load("")
	if err != nil {
		return nil, err
	}
	return llm.NewClient(&cfg.LLM)
}

// addModelForTest wraps config.AddModel so the test exercises the
// same code path the CLI does.
func addModelForTest(provider, model, display, desc string) error {
	_, err := config.AddModel(provider, config.ModelConfig{
		Name:        model,
		DisplayName: display,
		Description: desc,
	})
	return err
}

// configLoadForTest is shorthand for config.Load("") used by tests.
func configLoadForTest() (*config.Config, error) {
	return config.Load("")
}

// configSaveForTest serializes a config back to the global yaml.
// Tests use this to simulate edits made outside of the
// config.AddModel / RemoveModel helpers.
func configSaveForTest(cfg *config.Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return _writeFile(paths.GlobalConfig(), data)
}

// _writeFile is a tiny helper to write a file with mode 0o644.
func _writeFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}

