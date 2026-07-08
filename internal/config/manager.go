package config

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/p-chat/pchat/internal/paths"
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

// SaveGlobal saves the config to ~/.p-chat/config.json.
func (m *Manager) SaveGlobal(cfg *Config) error {
	return m.writeConfig(m.globalPath, cfg)
}

// SaveProject saves the config to .p-chat/config.json.
func (m *Manager) SaveProject(cfg *Config) error {
	if err := os.MkdirAll(filepath.Dir(m.projectPath), 0o755); err != nil {
		return err
	}
	return m.writeConfig(m.projectPath, cfg)
}

func (m *Manager) writeConfig(path string, cfg *Config) error {
	return writeConfigJSON(path, cfg)
}

// WatchGlobal polls the global config file every `interval` seconds.
// When the mtime changes, it reloads config and calls onReload(cfg).
// Returns when ctx is cancelled.
func (m *Manager) WatchGlobal(ctx context.Context, interval time.Duration, onReload func(*Config)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	var lastMod time.Time
	if fi, err := os.Stat(m.globalPath); err == nil {
		lastMod = fi.ModTime()
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fi, err := os.Stat(m.globalPath)
			if err != nil {
				continue
			}
			if fi.ModTime().After(lastMod) {
				lastMod = fi.ModTime()
				cfg, err := Load("")
				if err != nil {
					log.Printf("[config] reload failed: %v", err)
					continue
				}
				log.Printf("[config] detected file change, reloaded")
				onReload(cfg)
			}
		}
	}
}

// AddProvider adds a new provider to the global config. If a
// provider with the same name already exists, the call returns
// an error and the config is left unchanged. (The web UI uses
// this strict semantics so a typo doesn't silently overwrite
// an existing provider; the CLI's /config add flow can call
// the upsert variant explicitly if it wants overwrite
// behavior.)
func AddProvider(p ProviderConfig) error {
	cfg, err := Load("")
	if err != nil {
		return err
	}
	// Validate name. A blank name would create a provider
	// that can't be referenced, and a name with whitespace or
	// path separators breaks CLI parsing and the env-var
	// convention used by the agent.
	if p.Name == "" {
		return fmt.Errorf("provider name is required")
	}
	if strings.ContainsAny(p.Name, " \t/\\") || strings.ContainsRune(p.Name, 0) {
		return fmt.Errorf("provider name %q contains invalid characters (no whitespace, path separators, or NUL)", p.Name)
	}
	for _, existing := range cfg.LLM.Providers {
		if existing.Name == p.Name {
			return fmt.Errorf("provider %q already exists; use a different name (or remove the existing one first)", p.Name)
		}
	}
	// Validate the protocol. UpdateProvider validates (line
	// 235-241) but AddProvider did not — a provider added with
	// `protocol: "garbage"` would persist to disk and only fail
	// when a real LLM call is made.
	if p.Protocol != "" {
		switch p.Protocol {
		case "openai", "anthropic":
			// ok
		default:
			return fmt.Errorf("invalid protocol %q (allowed: openai, anthropic)", p.Protocol)
		}
	}
	// Validate the BaseURL is parseable so a typo doesn't get
	// silently persisted and surface as a confusing error at
	// the first LLM call.
	if p.BaseURL != "" {
		if _, err := url.Parse(p.BaseURL); err != nil {
			return fmt.Errorf("invalid base_url %q: %w", p.BaseURL, err)
		}
	}
	cfg.LLM.Providers = append(cfg.LLM.Providers, p)

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

// ProviderPatch is the unified set of fields the web UI can
// change on an existing provider. Every field is optional —
// pass a non-nil pointer / non-empty string to set, leave the
// zero value to leave the field alone.
//
// The zero-value semantics are important: the JSON body the UI
// sends lists only the fields the user actually edited, and the
// handler must not silently wipe fields the user did not
// include in the request.
type ProviderPatch struct {
	// Name, if non-empty, renames the provider. A name
	// collision with another provider returns an error.
	Name string
	// Protocol, if non-empty, switches the dispatch
	// protocol. Accepted values: "openai", "anthropic".
	Protocol string
	// BaseURL, if non-empty, replaces the API base URL.
	BaseURL string
	// APIKey, if non-empty, replaces the API key. (An
	// empty value here means "do not touch the key" — use
	// the dedicated SetProviderAPIKey flow or send a
	// separate request to clear it.)
	APIKey string
	// ClearAPIKey, if true, forces the key to be emptied
	// even when the user-supplied APIKey is "".
	ClearAPIKey bool
	// IsDefault, if true, promotes this provider to be the
	// global default. (False is a no-op so the UI can
	// always include the field without unintended
	// demotions.)
	IsDefault bool
}

// UpdateProvider applies a ProviderPatch to an existing
// provider. The patch is the single source of truth for "what
// changed" — non-provided fields are left untouched.
//
// Renames cascade: if the renamed provider was the global
// default, `cfg.LLM.Default` is updated to the new name. The
// change is persisted to ~/.p-chat/config.json atomically; if
// the rename collides with an existing provider, the file is
// not touched.
func UpdateProvider(oldName string, patch ProviderPatch) (*ProviderConfig, error) {
	cfg, err := Load("")
	if err != nil {
		return nil, err
	}
	idx := -1
	for i, p := range cfg.LLM.Providers {
		if p.Name == oldName {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil, fmt.Errorf("provider %q not found", oldName)
	}
	p := cfg.LLM.Providers[idx]

	// Handle rename first because every subsequent field
	// check needs to look at the new name.
	if patch.Name != "" && patch.Name != oldName {
		for _, other := range cfg.LLM.Providers {
			if other.Name == patch.Name {
				return nil, fmt.Errorf("provider %q already exists", patch.Name)
			}
		}
		if cfg.LLM.Default == oldName {
			cfg.LLM.Default = patch.Name
		}
		p.Name = patch.Name
	}
	if patch.Protocol != "" {
		switch patch.Protocol {
		case "openai", "anthropic":
			p.Protocol = patch.Protocol
		default:
			return nil, fmt.Errorf("invalid protocol %q (allowed: openai, anthropic)", patch.Protocol)
		}
	}
	if patch.BaseURL != "" {
		p.BaseURL = patch.BaseURL
	}
	if patch.ClearAPIKey {
		p.APIKey = ""
	} else if patch.APIKey != "" {
		p.APIKey = patch.APIKey
	}
	if patch.IsDefault {
		cfg.LLM.Default = p.Name
	}
	cfg.LLM.Providers[idx] = p
	mgr := NewManager()
	if err := mgr.SaveGlobal(cfg); err != nil {
		return nil, err
	}
	return &cfg.LLM.Providers[idx], nil
}

// AddModel appends a new model to a provider's Models list. If the
// provider only has the legacy single `model` field, the model is
// migrated into the Models list. Returns the updated provider.
func AddModel(providerName string, m ModelConfig) (*ProviderConfig, error) {
	cfg, err := Load("")
	if err != nil {
		return nil, err
	}
	if m.Name == "" {
		return nil, fmt.Errorf("model name is required")
	}
	if strings.ContainsAny(m.Name, " \t/\\") || strings.ContainsRune(m.Name, 0) {
		return nil, fmt.Errorf("model name %q contains invalid characters (no whitespace, path separators, or NUL)", m.Name)
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

// UpdateModel replaces the fields of a single model under a
// provider. The Name is the immutable lookup key. The supplied
// patch is applied to the existing model (the model is not
// removed-and-readded), so per-model settings such as Default
// stay consistent.
//
// When clearAll is true, DisplayName / Description /
// MaxTokensContext / MaxTokensOutput are all reset to their
// zero values, and only Capabilities (which is always
// replaced) is taken from the patch. Use this to reset a model
// to "just a name".
//
// Otherwise the patch is merged: a zero value in a numeric
// patch field means "leave alone"; an empty DisplayName /
// Description also means "leave alone". The current value is
// kept. This matches the PUT semantics documented in
// internal/server/provider_api.go.
//
// If the provider only has the legacy single `model` field, the
// update is interpreted as a migration: the legacy entry is
// promoted into Models[name] and the supplied patch is applied
// to that entry.
func UpdateModel(providerName, modelName string, patch ModelConfig, clearAll bool) (*ProviderConfig, error) {
	cfg, err := Load("")
	if err != nil {
		return nil, err
	}
	for i, p := range cfg.LLM.Providers {
		if p.Name != providerName {
			continue
		}
		if len(p.Models) == 0 && p.Model != "" {
			p.Models = []ModelConfig{{Name: p.Model, Default: true}}
		}
		idx := -1
		for j := range p.Models {
			if p.Models[j].Name == modelName {
				idx = j
				break
			}
		}
		if idx < 0 {
			return nil, fmt.Errorf("model %q not found under provider %q", modelName, providerName)
		}
		m := &p.Models[idx]
		if clearAll {
			m.DisplayName = ""
			m.Description = ""
			m.MaxTokensContext = 0
			m.MaxTokensOutput = 0
		} else {
			if patch.DisplayName != "" {
				m.DisplayName = patch.DisplayName
			}
			if patch.Description != "" {
				m.Description = patch.Description
			}
			if patch.MaxTokensContext != 0 {
				m.MaxTokensContext = patch.MaxTokensContext
			}
			if patch.MaxTokensOutput != 0 {
				m.MaxTokensOutput = patch.MaxTokensOutput
			}
		}
		// Capabilities is only replaced when the patch actually
		// carries a non-zero value. The HTTP PATCH API never
		// sends Capabilities, so without this guard every
		// DisplayName/Description edit would wipe out the
		// model's per-model capabilities (vision, thinking,
		// tool-use, etc.). If a future caller needs to clear
		// Capabilities, they can use clearAll and a separate
		// reset endpoint.
		if patch.Capabilities != (Capabilities{}) {
			m.Capabilities = patch.Capabilities
		}
		cfg.LLM.Providers[i] = p
		mgr := NewManager()
		if err := mgr.SaveGlobal(cfg); err != nil {
			return nil, err
		}
		return &p, nil
	}
	return nil, fmt.Errorf("provider %q not found", providerName)
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
	ThinkingEffort ThinkingEffort `json:"thinking_effort,omitempty"`
	ContextWindow  int            `json:"context_window,omitempty"`
	SupportsVision bool           `json:"supports_vision,omitempty"`
	SupportsAudio  bool           `json:"supports_audio,omitempty"`
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

// (removed: SetModelIsEmbedding, SetModelEmbedProtocol — vector embedding system deprecated)
