package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/p-chat/pchat/internal/paths"
	"gopkg.in/yaml.v3"
)

type Config struct {
	Server   ServerConfig   `yaml:"server"`
	LLM      LLMConfig      `yaml:"llm"`
	Style    StyleConfig    `yaml:"style"`
	Tools    ToolsConfig    `yaml:"tools"`
	Memory   MemoryConfig   `yaml:"memory"`
	Sandbox  SandboxConfig  `yaml:"sandbox"`
	SubAgent SubAgentConfig `yaml:"subagent"`
}

// SubAgentConfig controls how the `task` tool spawns sub-agents.
//
// AllowedTools is a whitelist: when non-empty, only these tool names
// are passed to the sub-agent. The `task` tool itself is always excluded
// to prevent recursion. When the list is empty, all non-task tools
// (excluding any listed in DeniedTools) are passed.
//
// DeniedTools is a blacklist applied after AllowedTools. Useful for
// blocking dangerous tools like `exec_command` while keeping the rest.
type SubAgentConfig struct {
	AllowedTools []string `yaml:"allowed_tools,omitempty"`
	DeniedTools  []string `yaml:"denied_tools,omitempty"`

	// Timeout is the per-sub-agent execution cap. Parsed from yaml
	// (e.g. "5m", "30s"). Zero means no explicit cap (the runner
	// applies a sensible default of 5 minutes).
	Timeout string `yaml:"timeout,omitempty"`

	// CacheTTL is how long a sub-agent result stays cached. Parsed
	// from yaml. Zero disables caching.
	CacheTTL string `yaml:"cache_ttl,omitempty"`
}

// TimeoutDuration returns the parsed timeout, or 5m if unset/invalid.
func (s SubAgentConfig) TimeoutDuration() time.Duration {
	if s.Timeout == "" {
		return 5 * time.Minute
	}
	d, err := time.ParseDuration(s.Timeout)
	if err != nil || d <= 0 {
		return 5 * time.Minute
	}
	return d
}

// CacheTTLDuration returns the parsed cache TTL, or 0 (disabled) if
// unset/invalid.
func (s SubAgentConfig) CacheTTLDuration() time.Duration {
	if s.CacheTTL == "" {
		return 0
	}
	d, err := time.ParseDuration(s.CacheTTL)
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

// ToolAllowed reports whether the given tool name is allowed for a
// sub-agent. The `task` tool is never allowed (recursion prevention).
func (s SubAgentConfig) ToolAllowed(name string) bool {
	if name == "task" {
		return false
	}
	// Whitelist has priority: if set, only listed tools pass.
	if len(s.AllowedTools) > 0 {
		for _, t := range s.AllowedTools {
			if t == name {
				return true
			}
		}
		return false
	}
	// Otherwise, blacklist.
	for _, t := range s.DeniedTools {
		if t == name {
			return false
		}
	}
	return true
}

type ServerConfig struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

type LLMConfig struct {
	Default    string           `yaml:"default"`
	Providers  []ProviderConfig `yaml:"providers"`

	// Sampling parameters applied to every chat call unless overridden
	// by the provider. Set to 0 to use the API default.
	Temperature float64 `yaml:"temperature,omitempty"`
	TopP        float64 `yaml:"top_p,omitempty"`
	MaxTokens   int     `yaml:"max_tokens,omitempty"`

	// Output is a presentation hint.
	Output OutputConfig `yaml:"output"`
}

type OutputConfig struct {
	// Language is the language the assistant should respond in.
	// One of "zh", "en", "auto" (default "auto" = match the user).
	Language string `yaml:"language,omitempty"`

	// Verbose controls how much detail the UI shows for tool calls.
	// When true, full arguments and longer result previews are shown.
	Verbose bool `yaml:"verbose,omitempty"`
}

// ProviderConfig defines an LLM provider. A single provider can
// expose multiple models (e.g. "openai" with gpt-4o, gpt-4o-mini, gpt-3.5-turbo)
// that share the same base URL and API key. Use either the legacy
// `model` field (one model) or `models` (multiple, with optional
// default). When both are present the first entry of `models` with
// `default: true` wins; otherwise `models[0]` is used.
type ProviderConfig struct {
	Name     string        `yaml:"name"`
	Protocol string        `yaml:"protocol"` // "openai" | "anthropic"
	Type     string        `yaml:"type"`     // alias for Protocol (backward compat)
	BaseURL  string        `yaml:"base_url"`
	APIKey   string        `yaml:"api_key"`
	Model    string        `yaml:"model"`     // legacy: single model
	Models   []ModelConfig `yaml:"models,omitempty"`
}

// ModelConfig describes a single model under a provider.
type ModelConfig struct {
	// Name is the model identifier sent to the API (e.g. "gpt-4o").
	Name string `yaml:"name"`
	// DisplayName is shown in /model and /config. Optional.
	DisplayName string `yaml:"display_name,omitempty"`
	// Default marks one of the provider's models as the default.
	// At most one model per provider may set this to true.
	Default bool `yaml:"default,omitempty"`
	// Description is shown in /model.
	Description string `yaml:"description,omitempty"`
	// Capabilities carries per-model hints (vision/audio/thinking).
	// Optional; providers may also discover these at runtime.
	Capabilities Capabilities `yaml:"capabilities,omitempty" json:"capabilities,omitempty"`
}

// GetProtocol returns the protocol, falling back to Type, then "openai".
func (p ProviderConfig) GetProtocol() string {
	if p.Protocol != "" {
		return p.Protocol
	}
	if p.Type != "" {
		return p.Type
	}
	return "openai"
}

// EffectiveModel returns the model identifier that should be used
// when the user has selected this provider. It looks at `models`
// (multi-model) first, then falls back to the legacy `model` field.
// Returns the name of the first model when no default is marked.
func (p ProviderConfig) EffectiveModel() string {
	if len(p.Models) > 0 {
		// Prefer the model marked default=true.
		for _, m := range p.Models {
			if m.Default {
				return m.Name
			}
		}
		// Otherwise the first one.
		return p.Models[0].Name
	}
	return p.Model
}

// AllModels returns the list of model names for this provider, in
// user-facing order. Always includes at least one entry (the
// effective model).
func (p ProviderConfig) AllModels() []ModelConfig {
	if len(p.Models) > 0 {
		out := make([]ModelConfig, len(p.Models))
		copy(out, p.Models)
		return out
	}
	// Synthesize a single-model entry from the legacy field.
	return []ModelConfig{{Name: p.Model}}
}

// DisplayModel returns a human-friendly name for the effective model
// (display name if set, otherwise the model id).
func (p ProviderConfig) DisplayModel() string {
	eff := p.EffectiveModel()
	for _, m := range p.Models {
		if m.Name == eff && m.DisplayName != "" {
			return m.DisplayName
		}
	}
	return eff
}

// GetProtocol returns the protocol, falling back to Type, then "openai".
// (moved above; this is the canonical definition)

type StyleConfig struct {
	Default string `yaml:"default"`
}

type ToolsConfig struct {
	Enabled []string           `yaml:"enabled"`
	Servers []ToolServerConfig `yaml:"servers"`
}

type ToolServerConfig struct {
	Name    string   `yaml:"name"`
	Command string   `yaml:"command"`
	Args    []string `yaml:"args"`
}

type MemoryConfig struct {
	Enabled    bool `yaml:"enabled"`
	MaxHistory int  `yaml:"max_history"`
}

// SandboxConfig controls which actions LLM-driven tools can take
// without explicit user confirmation.
type SandboxConfig struct {
	// Enabled turns all sandbox checks on/off. When false, every tool
	// call runs unimpeded (use with care).
	Enabled bool `yaml:"enabled"`

	// RequireConfirm controls when a tool call must wait for user
	// approval before running. Allowed values:
	//   "never"     - never ask; rely on the rules below to block
	//   "dangerous" - ask only for hits against dangerous patterns
	//   "always"    - ask for every tool call (very conservative)
	// Default: "dangerous".
	RequireConfirm string `yaml:"require_confirm,omitempty"`

	// WriteProtectedPaths is a list of path prefixes that write_file /
	// edit_file must not touch. The "~" prefix expands to the user's
	// home directory. Matches are case-insensitive on Windows.
	WriteProtectedPaths []string `yaml:"write_protected_paths,omitempty"`

	// ExecDangerousPatterns is a list of regex patterns. If the
	// command passed to exec_command matches any pattern, the call is
	// blocked (or held for confirmation, per RequireConfirm).
	ExecDangerousPatterns []string `yaml:"exec_dangerous_patterns,omitempty"`

	// MaxCommandLength caps the size of a single exec_command. Default
	// 4096 bytes.
	MaxCommandLength int `yaml:"max_command_length,omitempty"`
}

// Load merges global (~/.p-chat/config.yaml) and project (.p-chat/config.yaml) configs.
// Project config overrides global config.
func Load(customPath string) (*Config, error) {
	cfg := Default()

	// Load global config
	if data, err := os.ReadFile(paths.GlobalConfig()); err == nil {
		_ = yaml.Unmarshal(data, cfg)
	}

	// Load project config (overrides global)
	if data, err := os.ReadFile(paths.ProjectConfig()); err == nil {
		_ = yaml.Unmarshal(data, cfg)
	}

	// Load custom path (highest priority)
	if customPath != "" {
		data, err := os.ReadFile(customPath)
		if err != nil {
			return nil, fmt.Errorf("read config %s: %w", customPath, err)
		}
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", customPath, err)
		}
	}

	return cfg, nil
}

func Default() *Config {
	return &Config{
		Server: ServerConfig{
			Host: "127.0.0.1",
			Port: 8960,
		},
		LLM: LLMConfig{
			Default: "ollama",
			Providers: []ProviderConfig{
				{
					Name:     "ollama",
					Protocol: "openai",
					BaseURL:  "http://localhost:11434/v1",
					APIKey:   "ollama",
					Model:    "llama3",
				},
			},
		},
		Style: StyleConfig{
			Default: "tech",
		},
		Tools: ToolsConfig{
			Enabled: []string{"fs-tool", "shell-tool"},
		},
		Memory: MemoryConfig{
			Enabled:    true,
			MaxHistory: 50,
		},
		Sandbox: SandboxConfig{
			Enabled:             true,
			RequireConfirm:      "dangerous",
			MaxCommandLength:    4096,
			WriteProtectedPaths: defaultWriteProtectedPaths(),
			ExecDangerousPatterns: defaultDangerousPatterns(),
		},
		SubAgent: SubAgentConfig{
			// Default safety stance: deny exec_command in sub-agents.
			// Users can override by setting allowed_tools explicitly.
			DeniedTools: []string{"exec_command"},
		},
	}
}

// PromptDir returns the prompts directory (project/prompts first, then ~/.p-chat/prompts)
func PromptDir() string {
	// Check project-level prompts first
	cwd, _ := os.Getwd()
	projectPrompts := filepath.Join(cwd, "prompts")
	if _, err := os.Stat(projectPrompts); err == nil {
		return projectPrompts
	}
	return paths.GlobalPromptsDir()
}

// defaultWriteProtectedPaths returns the conservative baseline list of
// paths that LLM-driven tools should never touch. Users can override
// or extend this list in their config.yaml.
func defaultWriteProtectedPaths() []string {
	home, _ := os.UserHomeDir()
	prefixes := []string{}
	if home != "" {
		prefixes = append(prefixes, home)
	}
	// Common system paths (Linux/macOS); on Windows these simply won't
	// match any real path.
	return append(prefixes,
		".ssh/",
		".bashrc",
		".zshrc",
		".profile",
		".gnupg/",
		".aws/credentials",
		".config/gh/hosts",
		"/etc/",
		"/usr/",
		"/boot/",
		"/var/",
		"~/.ssh/",
		"~/.bashrc",
		"~/.zshrc",
		"~/.gnupg/",
	)
}

// defaultDangerousPatterns returns the baseline list of regexes that
// flag a shell command as potentially destructive.
func defaultDangerousPatterns() []string {
	return []string{
		`\brm\s+(-[a-zA-Z]*[rfRF]+\s+)+/`,       // rm -rf /
		`\brm\s+-rf\s+~`,                          // rm -rf ~
		`\bmkfs\.`,                                 // mkfs.ext4 etc.
		`\bdd\s+.*\bof=/dev/(sd|nvme|hd)`,         // dd to disk
		`\bcurl\s+.*\|\s*(sudo\s+)?sh`,            // curl|sh
		`\bwget\s+.*\|\s*(sudo\s+)?sh`,            // wget|sh
		`\b(sh|bash)\s+<\(\s*curl`,                 // process substitution
		`\bchmod\s+-R\s+777\s+/`,                   // chmod 777 /
		`\bchown\s+-R\s+.*\s+/`,                    // chown /
		`:(){\s*:\|:&\s*};:`,                       // fork bomb
		`\bshutdown\b`,                            // shutdown
		`\breboot\b`,                              // reboot
		`\bpoweroff\b`,                            // poweroff
		`\bhalt\b`,                                // halt
		`\bmkfs\b.*\s+/dev/`,                      // format a device
		`>\s*/dev/sd`,                             // redirect to disk
		`\bnc\s+-l\s+-p\s+\d+`,                    // netcat listener
		`\beval\s+.*\$\(`,                          // eval + command substitution
	}
}
