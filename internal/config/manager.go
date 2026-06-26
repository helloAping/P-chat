package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/p-chat/pchat/internal/paths"
	"gopkg.in/yaml.v3"
)

// Manager handles reading and writing config files.
type Manager struct {
	globalPath  string
	projectPath string
}

func NewManager() *Manager {
	return &Manager{
		globalPath:  paths.GlobalConfig(),
		projectPath: paths.ProjectConfig(),
	}
}

func (m *Manager) Load() (*Config, error) {
	return Load("")
}

// SaveGlobal saves the config to ~/.p-chat/config.yaml
func (m *Manager) SaveGlobal(cfg *Config) error {
	if err := os.MkdirAll(filepath.Dir(m.globalPath), 0o755); err != nil {
		return err
	}
	return m.writeConfig(m.globalPath, cfg)
}

// SaveProject saves the config to .p-chat/config.yaml
func (m *Manager) SaveProject(cfg *Config) error {
	if err := os.MkdirAll(filepath.Dir(m.projectPath), 0o755); err != nil {
		return err
	}
	return m.writeConfig(m.projectPath, cfg)
}

func (m *Manager) writeConfig(path string, cfg *Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

// AddProvider adds or replaces a provider in the global config
func AddProvider(p ProviderConfig) error {
	cfg, err := Load("")
	if err != nil {
		return err
	}

	found := false
	for i, existing := range cfg.LLM.Providers {
		if existing.Name == p.Name {
			cfg.LLM.Providers[i] = p
			found = true
			break
		}
	}
	if !found {
		cfg.LLM.Providers = append(cfg.LLM.Providers, p)
	}

	mgr := NewManager()
	return mgr.SaveGlobal(cfg)
}

// RemoveProvider removes a provider from the global config
func RemoveProvider(name string) error {
	cfg, err := Load("")
	if err != nil {
		return err
	}

	for i, p := range cfg.LLM.Providers {
		if p.Name == name {
			cfg.LLM.Providers = append(cfg.LLM.Providers[:i], cfg.LLM.Providers[i+1:]...)
			mgr := NewManager()
			return mgr.SaveGlobal(cfg)
		}
	}
	return fmt.Errorf("provider %q not found", name)
}

// SetDefaultProvider sets the default provider in the global config
func SetDefaultProvider(name string) error {
	cfg, err := Load("")
	if err != nil {
		return err
	}

	found := false
	for _, p := range cfg.LLM.Providers {
		if p.Name == name {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("provider %q not found", name)
	}

	cfg.LLM.Default = name
	mgr := NewManager()
	return mgr.SaveGlobal(cfg)
}

// SetProviderAPIKey updates only the API key for a provider
func SetProviderAPIKey(name, apiKey string) error {
	cfg, err := Load("")
	if err != nil {
		return err
	}

	for i, p := range cfg.LLM.Providers {
		if p.Name == name {
			cfg.LLM.Providers[i].APIKey = apiKey
			mgr := NewManager()
			return mgr.SaveGlobal(cfg)
		}
	}
	return fmt.Errorf("provider %q not found", name)
}

// AddModel appends a new model to a provider's Models list. If the
// provider only has the legacy single `model` field, the model is
// migrated into the Models list. Returns the updated provider.
func AddModel(providerName string, m ModelConfig) (*ProviderConfig, error) {
	cfg, err := Load("")
	if err != nil {
		return nil, err
	}
	for i, p := range cfg.LLM.Providers {
		if p.Name != providerName {
			continue
		}
		// Migrate legacy single-model form to multi-model.
		if len(p.Models) == 0 && p.Model != "" && p.Model != m.Name {
			p.Models = []ModelConfig{{Name: p.Model, Default: true}}
		}
		// Reject duplicates by name.
		for _, existing := range p.Models {
			if existing.Name == m.Name {
				return nil, fmt.Errorf("model %q already exists under provider %q", m.Name, providerName)
			}
		}
		// First model in the list is the default unless another is marked.
		if m.Default {
			for i := range p.Models {
				p.Models[i].Default = false
			}
		} else if len(p.Models) == 0 {
			m.Default = true
		}
		p.Models = append(p.Models, m)
		cfg.LLM.Providers[i] = p
		mgr := NewManager()
		if err := mgr.SaveGlobal(cfg); err != nil {
			return nil, err
		}
		return &p, nil
	}
	return nil, fmt.Errorf("provider %q not found", providerName)
}

// RemoveModel removes a model from a provider. If the model was the
// default, the next entry in the list becomes the new default (or the
// first remaining model).
func RemoveModel(providerName, modelName string) error {
	cfg, err := Load("")
	if err != nil {
		return err
	}
	for i, p := range cfg.LLM.Providers {
		if p.Name != providerName {
			continue
		}
		idx := -1
		wasDefault := false
		for j, m := range p.Models {
			if m.Name == modelName {
				idx = j
				wasDefault = m.Default
				break
			}
		}
		if idx < 0 {
			return fmt.Errorf("model %q not found under provider %q", modelName, providerName)
		}
		p.Models = append(p.Models[:idx], p.Models[idx+1:]...)
		// Pick a new default if we removed the old one.
		if wasDefault && len(p.Models) > 0 {
			p.Models[0].Default = true
		}
		cfg.LLM.Providers[i] = p
		mgr := NewManager()
		return mgr.SaveGlobal(cfg)
	}
	return fmt.Errorf("provider %q not found", providerName)
}

// SetDefaultModel marks the given model as the provider's default and
// clears `default: true` on every other model in the same provider.
func SetDefaultModel(providerName, modelName string) error {
	cfg, err := Load("")
	if err != nil {
		return err
	}
	for i, p := range cfg.LLM.Providers {
		if p.Name != providerName {
			continue
		}
		found := false
		for j := range p.Models {
			p.Models[j].Default = p.Models[j].Name == modelName
			if p.Models[j].Name == modelName {
				found = true
			}
		}
		if !found {
			return fmt.Errorf("model %q not found under provider %q", modelName, providerName)
		}
		cfg.LLM.Providers[i] = p
		mgr := NewManager()
		return mgr.SaveGlobal(cfg)
	}
	return fmt.Errorf("provider %q not found", providerName)
}

// ThinkingEffort hints how much "thinking" a model should do before
// answering. It's an enum the UI/CLI write into config; providers
// translate it into their own native parameter.
type ThinkingEffort string

const (
	ThinkingEffortOff    ThinkingEffort = "off"
	ThinkingEffortLow    ThinkingEffort = "low"
	ThinkingEffortMedium ThinkingEffort = "medium"
	ThinkingEffortHigh   ThinkingEffort = "high"
)

// Capabilities carries per-model hints that the agent / llm client
// can read at runtime. They're informational; the provider's own
// /models endpoint is the source of truth where available.
type Capabilities struct {
	ThinkingEffort ThinkingEffort `yaml:"thinking_effort,omitempty" json:"thinking_effort,omitempty"`
	ContextWindow  int            `yaml:"context_window,omitempty" json:"context_window,omitempty"`
	SupportsVision bool           `yaml:"supports_vision,omitempty" json:"supports_vision,omitempty"`
	SupportsAudio  bool           `yaml:"supports_audio,omitempty" json:"supports_audio,omitempty"`
}

// SetModelCapabilities replaces the Capabilities block for a single
// model under a provider. If the model doesn't have a Capabilities
// field yet it is added; otherwise it's overwritten.
func SetModelCapabilities(providerName, modelName string, caps Capabilities) error {
	cfg, err := Load("")
	if err != nil {
		return err
	}
	for i, p := range cfg.LLM.Providers {
		if p.Name != providerName {
			continue
		}
		for j := range p.Models {
			if p.Models[j].Name != modelName {
				continue
			}
			p.Models[j].Capabilities = caps
			cfg.LLM.Providers[i] = p
			mgr := NewManager()
			return mgr.SaveGlobal(cfg)
		}
		return fmt.Errorf("model %q not found under provider %q", modelName, providerName)
	}
	return fmt.Errorf("provider %q not found", providerName)
}
