package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/p-chat/pchat/internal/paths"
)

// Config is the on-disk configuration of P-Chat, stored as JSON in
// ~/.p-chat/config.json (with a per-project .p-chat/config.json
// overlay). Project overrides global.
//
// Field tags use `json` exclusively; the previous yaml tags have
// been removed. The loader still accepts a legacy config.yaml as
// a one-shot migration source — see Load.
type Config struct {
	Server    ServerConfig    `json:"server"`
	LLM       LLMConfig       `json:"llm"`
	Style     StyleConfig     `json:"style"`
	Tools     ToolsConfig     `json:"tools"`
	Memory    MemoryConfig    `json:"memory"`
	Sandbox   SandboxConfig   `json:"sandbox"`
	SubAgent  SubAgentConfig  `json:"subagent"`
	MCP       MCPConfig       `json:"mcp"`
	Knowledge KnowledgeConfig `json:"knowledge"`
	Limits    LimitsConfig    `json:"limits"`
	Search    SearchConfig    `json:"search"`
	Browser   BrowserConfig   `json:"browser"`
	// Dynamic is the P3-2 per-tool config table. The
	// user writes `dynamic.<tool_name>.config: {…}`
	// in their config.json and the dynamic tool's
	// templates can reference it as
	// `{{.config.<key>}}`. Nil = no overrides
	// (the most common case for users who don't have
	// any custom tools).
	Dynamic map[string]map[string]any `json:"dynamic,omitempty"`
}

// LimitsConfig controls resource caps for the agent loop.
type LimitsConfig struct {
	// AutoCompactBuffer is the token headroom reserved before
	// auto-compression triggers. Default 20000.
	AutoCompactBuffer int `json:"auto_compact_buffer"`
	// ToolResultExecCap is the max output chars fed to the LLM
	// from exec_command. Default 4000.
	ToolResultExecCap int `json:"tool_result_exec_cap"`
	// ToolResultReadCap is the max output chars from read_file.
	// Default 8000.
	ToolResultReadCap int `json:"tool_result_read_cap"`
	// ToolResultDefaultCap is the max output chars for all other
	// tools. Default 6000.
	ToolResultDefaultCap int `json:"tool_result_default_cap"`
	// PruneAfterRounds marks tool results older than this many
	// rounds as [pruned]. Default 15. 0 = disable pruning.
	PruneAfterRounds int `json:"prune_after_rounds"`
	// MaxRounds overrides the agent's built-in safety-net round cap.
	// Default 300. 0 = unlimited.
	MaxRounds int `json:"max_rounds"`
	// MaxStoredMessages caps the SQLite messages table. When set,
	// messages beyond this count per conversation are deleted oldest-
	// first. 0 = unlimited (default).
	MaxStoredMessages int `json:"max_stored_messages"`
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
	AllowedTools []string `json:"allowed_tools,omitempty"`
	DeniedTools  []string `json:"denied_tools,omitempty"`

	// Timeout is the per-sub-agent execution cap. Parsed from JSON
	// (e.g. "5m", "30s"). Zero means no explicit cap (the runner
	// applies a sensible default of 5 minutes).
	Timeout string `json:"timeout,omitempty"`

	// CacheTTL is how long a sub-agent result stays cached. Parsed
	// from JSON. Zero disables caching.
	CacheTTL string `json:"cache_ttl,omitempty"`
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
	Host string `json:"host"`
	Port int    `json:"port"`
}

// LLMConfig is the LLM block of the user config.
//
// `default` selects which provider is used when no provider is
// requested explicitly. Each provider may expose multiple models
// (see ProviderConfig) that share the same base URL + API key.
type LLMConfig struct {
	Default   string           `json:"default"`
	Providers []ProviderConfig `json:"providers"`

	// Sampling parameters applied to every chat call unless overridden
	// by a specific model. Set to 0 to use the API default.
	Temperature float64 `json:"temperature,omitempty"`
	TopP        float64 `json:"top_p,omitempty"`
	MaxTokens   int     `json:"max_tokens,omitempty"`

	// Output is a presentation hint.
	Output OutputConfig `json:"output"`
}

type OutputConfig struct {
	// Language is the language the assistant should respond in.
	// One of "zh", "en", "auto" (default "auto" = match the user).
	Language string `json:"language,omitempty"`

	// Verbose controls how much detail the UI shows for tool calls.
	// When true, full arguments and longer result previews are shown.
	Verbose bool `json:"verbose,omitempty"`
}

// ProviderConfig defines an LLM provider. A single provider can
// expose multiple models (e.g. "openai" with gpt-4o, gpt-4o-mini, gpt-3.5-turbo)
// that share the same base URL and API key.
//
// Use `models` (multi-model) for new entries. The legacy `model`
// field is still recognized for backward compat: when `models` is
// empty the value of `model` is used as a single-model provider.
type ProviderConfig struct {
	Name     string        `json:"name"`
	Protocol string        `json:"protocol,omitempty"` // "openai" | "anthropic"
	Type     string        `json:"type,omitempty"`     // alias for Protocol (backward compat)
	BaseURL  string        `json:"base_url"`
	APIKey   string        `json:"api_key"`
	Model    string        `json:"model,omitempty"`     // legacy: single model
	Models   []ModelConfig `json:"models,omitempty"`
}

// ModelConfig describes a single model under a provider.
//
// A model can override the provider-level sampling defaults with
// MaxTokensContext (the model's input window — informational;
// the chat client does not currently truncate) and MaxTokensOutput
// (the per-response cap, sent as `max_tokens` to the API; 0 = use
// the global config value).
type ModelConfig struct {
	// Name is the model identifier sent to the API (e.g. "gpt-4o").
	Name string `json:"name"`
	// DisplayName is shown in /model and /config. Optional.
	DisplayName string `json:"display_name,omitempty"`
	// Default marks one of the provider's models as the default.
	// At most one model per provider may set this to true.
	Default bool `json:"default,omitempty"`
	// Description is shown in /model.
	Description string `json:"description,omitempty"`

	// MaxTokensContext is the model's input context window size
	// (informational). Examples: 8192 (gpt-3.5), 128000 (gpt-4o),
	// 200000 (claude-sonnet). Used by the UI to display "8k / 16k"
	// hints. Zero means "unknown".
	MaxTokensContext int `json:"max_tokens_context,omitempty"`

	// MaxTokensOutput is the per-response cap. Sent to the API as
	// `max_tokens`. Zero means "fall back to LLMConfig.MaxTokens",
	// and that in turn defaults to 0 (= "no cap, let the API
	// decide"). Note: some newer OpenAI models (o1, gpt-5) require
	// `max_completion_tokens` instead; the client transparently
	// maps MaxTokensOutput into the right field.
	MaxTokensOutput int `json:"max_tokens_output,omitempty"`

	// Capabilities carries per-model hints (vision/audio/thinking).
	// Optional; providers may also discover these at runtime.
	Capabilities Capabilities `json:"capabilities,omitempty"`

	// PricePer1KInput is the cost in USD per 1000 input tokens.
	// Used for cost estimation in the token dashboard. Zero means unknown.
	PricePer1KInput float64 `json:"price_per_1k_input,omitempty"`

	// PricePer1KOutput is the cost in USD per 1000 output tokens.
	PricePer1KOutput float64 `json:"price_per_1k_output,omitempty"`
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

// FindModel returns a pointer to the named model under this provider
// (or nil if absent). The pointer aliases the in-memory config, so
// callers can mutate fields directly and have the change reflected
// in the slice.
//
// When the provider uses the legacy single-`model` form, this
// function returns a pointer to a synthetic copy. Mutating it does
// NOT persist; callers should fall back to AddModel / RemoveModel
// for legacy single-model providers.
func (p *ProviderConfig) FindModel(name string) *ModelConfig {
	for i := range p.Models {
		if p.Models[i].Name == name {
			return &p.Models[i]
		}
	}
	if p.Model == name {
		// Returned pointer aliases a stack-local copy; safe
		// for read-only access.
		m := ModelConfig{Name: p.Model}
		return &m
	}
	return nil
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

type StyleConfig struct {
	Default string `json:"default"`
}

type ToolsConfig struct {
	Enabled []string           `json:"enabled"`
	Servers []ToolServerConfig `json:"servers"`
}

type ToolServerConfig struct {
	Name    string   `json:"name"`
	Command string   `json:"command"`
	Args    []string `json:"args"`
}

type MCPConfig struct {
	Enabled bool              `json:"enabled"`
	Servers []MCPServerConfig `json:"servers"`
}

type MCPServerConfig struct {
	Name    string            `json:"name"`
	Type    string            `json:"type,omitempty"`   // "stdio" (default) | "sse"
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`    // for SSE transport
	Enabled bool              `json:"enabled"`
	Timeout string            `json:"timeout,omitempty"`
}

// KnowledgeConfig controls the wiki-based knowledge base system.
// Uses SQLite FTS5 for full-text search; no vector embeddings required.
type KnowledgeConfig struct {
	Enabled     bool            `json:"enabled"`
	AutoIndex   bool            `json:"auto_index"`
	Bases       []KnowledgeBase `json:"bases,omitempty"`
	Initialized bool            `json:"initialized,omitempty"`
}

// KnowledgeBase defines a local directory to scan into the wiki index.
// Text files are parsed zero-cost via wiki parser. Media files (image/video/
// audio/pdf) require an AI model selected via ScanModel.
type KnowledgeBase struct {
	Name      string   `json:"name"`
	Path      string   `json:"path"`
	Enabled   bool     `json:"enabled"`
	FileTypes []string `json:"file_types,omitempty"`

	// ScanModel is "{provider}/{model}" for AI media processing, or "" for text-only.
	ScanModel      string   `json:"scan_model"`
	ScanMediaTypes []string `json:"scan_media_types"` // "image","video","audio","pdf"
	AutoScan       bool     `json:"auto_scan"`
	ExcludePatterns []string `json:"exclude_patterns"`
	MaxFileSize    int64    `json:"max_file_size"` // 0 = default (5MB)
}

type MemoryConfig struct {
	Enabled    bool `json:"enabled"`
	MaxHistory int  `json:"max_history"`
}

// SearchConfig configures the `web_search` tool.
//
// The tool is registered only when Enabled=true AND a usable
// API key + provider is configured. When disabled, the
// `web_search` tool is invisible to the LLM (handled at
// RegisterBuiltin time) so the agent doesn't waste a turn
// discovering the feature is off.
//
// Provider selects the backend implementation:
//
//   - "tavily" (default): https://api.tavily.com/search, requires APIKey
//   - "openai_compat": any HTTP endpoint that accepts
//     `{query, max_results, ...}` and returns
//     `{results: [{title, url, snippet}, ...]}`; requires BaseURL
type SearchConfig struct {
	// Enabled is the master switch. When false, the `web_search`
	// tool is not registered and the LLM cannot call it.
	Enabled bool `json:"enabled"`

	// Provider selects the backend. Empty string = "tavily".
	Provider string `json:"provider,omitempty"`

	// APIKey is the provider's auth token. Not used by
	// self-hosted providers that don't require auth.
	APIKey string `json:"api_key,omitempty"`

	// BaseURL overrides the provider's default endpoint.
	//   - "tavily": leave empty (defaults to https://api.tavily.com)
	//   - "openai_compat": required (e.g. "https://s.jina.ai")
	BaseURL string `json:"base_url,omitempty"`

	// Path overrides the request path appended to BaseURL.
	// Defaults to "/search". Useful for proxies that mount
	// search at a non-standard path.
	Path string `json:"path,omitempty"`

	// Topic restricts the search corpus for providers that
	// support it (currently "tavily" only). Valid values:
	// "general" (default), "news", "finance".
	Topic string `json:"topic,omitempty"`

	// RequestTimeout is the per-search HTTP timeout. Zero
	// (or negative) means 20s. The agent loop also enforces
	// its own deadline so a stuck search can't block a turn
	// indefinitely.
	RequestTimeout time.Duration `json:"request_timeout,omitempty"`

	// DailyQuota caps the number of searches the local
	// process will dispatch per UTC day. 0 = unlimited.
	// Enforced by internal/search.QuotaTracker; the
	// counter resets at 00:00 UTC and is *not* persisted
	// across server restarts (a deliberate choice — quota
	// is a soft budget, not a billing boundary).
	DailyQuota int `json:"daily_quota,omitempty"`
}

// BrowserConfig controls the browser-control feature. When
// Enabled=true, the server accepts WebSocket connections from
// Chrome extensions and exposes browser_* tools to the LLM.
// When Enabled=false, the WebSocket endpoint returns 403 and
// browser tools are invisible to the LLM.
type BrowserConfig struct {
	// Enabled is the master switch.
	Enabled bool `json:"enabled"`

	// ScreenshotQuality is the JPEG quality for browser_screenshot
	// requests (1-100). Default 80.
	ScreenshotQuality int `json:"screenshot_quality,omitempty"`

	// MaxScreenshotsInHistory caps how many screenshot bytes are
	// retained in conversation history before being stripped to
	// "[creenshot omitted]". Default 3.
	MaxScreenshotsInHistory int `json:"max_screenshots_in_history,omitempty"`

	// AllowEvalWrite permits browser_evaluate to execute JS that
	// writes to the page (fetch, postMessage, etc.). When false
	// (default), only read-only expressions are allowed.
	AllowEvalWrite bool `json:"allow_eval_write,omitempty"`
}

// SandboxConfig controls which actions LLM-driven tools can take
// without explicit user confirmation.
type SandboxConfig struct {
	// Enabled turns all sandbox checks on/off. When false, every tool
	// call runs unimpeded (use with care).
	Enabled bool `json:"enabled"`

	// RequireConfirm controls when a tool call must wait for user
	// approval before running. Allowed values:
	//   "never"     - never ask; rely on the rules below to block
	//   "dangerous" - ask only for hits against dangerous patterns
	//   "always"    - ask for every tool call (very conservative)
	// Default: "dangerous".
	RequireConfirm string `json:"require_confirm,omitempty"`

	// WriteProtectedPaths is a list of path prefixes that write_file /
	// edit_file must not touch. The "~" prefix expands to the user's
	// home directory. Matches are case-insensitive on Windows.
	WriteProtectedPaths []string `json:"write_protected_paths,omitempty"`

	// ExecDangerousPatterns is a list of regex patterns. If the
	// command passed to exec_command matches any pattern, the call is
	// blocked (or held for confirmation, per RequireConfirm).
	ExecDangerousPatterns []string `json:"exec_dangerous_patterns,omitempty"`

	// MaxCommandLength caps the size of a single exec_command. Default
	// 4096 bytes.
	MaxCommandLength int `json:"max_command_length,omitempty"`

	// ExtraAllowedPaths is the per-user whitelist (Phase 2 UI
	// in 2026-07). Paths here get the same policy as the
	// project root (reads: Allow; writes: Allow) regardless
	// of projectRoot — a deliberate "open this directory up"
	// gesture. The "~" prefix expands to the user's home.
	//
	// Phase 1 ships the plumbing; the UI to manage the list
	// is Phase 2.
	ExtraAllowedPaths []string `json:"extra_allowed_paths,omitempty"`
}

// utf8BOM is the byte order mark some Windows editors (Notepad,
// PowerShell's [IO.File]::WriteAllText with a UTF-8 BOM, etc.)
// prepend to UTF-8 files. Go's encoding/json refuses to parse a
// file starting with BOM, so we strip it here. JSON spec says BOM
// is allowed but not required; for config files we treat it as
// "harmless" and just remove it.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

// stripBOM returns data with a leading UTF-8 BOM removed. If no
// BOM is present, data is returned unchanged.
func stripBOM(data []byte) []byte {
	if len(data) >= 3 && data[0] == utf8BOM[0] && data[1] == utf8BOM[1] && data[2] == utf8BOM[2] {
		return data[3:]
	}
	return data
}

// trailingCommaRE matches a comma immediately followed by
// optional whitespace and a closing brace or bracket. JSON
// spec (RFC 8259 §2.3) forbids trailing commas; Go's
// encoding/json follows the spec. We strip them so a config
// that survived a botched editor / regex / set-content pass
// can still boot — the loader writes the cleaned result back
// to disk so subsequent startups are fast and the user can see
// the fix.
var trailingCommaRE = regexp.MustCompile(`,\s*([}\]])`)

// stripTrailingCommas returns a copy of data with all
// trailing commas before } or ] removed. Does not recurse
// into strings (a `,` inside a quoted JSON string is left
// alone, since strings can contain anything).
func stripTrailingCommas(data []byte) []byte {
	// Fast path: scan for the pattern manually so we don't
	// touch commas inside string literals. encoding/json's
	// own scanner would be ideal but we want a single
	// non-allocating pass over the typical ~3KB file.
	out := make([]byte, 0, len(data))
	inString := false
	escape := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inString {
			out = append(out, c)
			if escape {
				escape = false
			} else if c == '\\' {
				escape = true
			} else if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
			out = append(out, c)
		case ',':
			// Look ahead for a } or ] (after whitespace).
			j := i + 1
			for j < len(data) && (data[j] == ' ' || data[j] == '\t' || data[j] == '\n' || data[j] == '\r') {
				j++
			}
			if j < len(data) && (data[j] == '}' || data[j] == ']') {
				// Drop the comma. Whitespace is preserved.
				continue
			}
			out = append(out, c)
		default:
			out = append(out, c)
		}
	}
	return out
}

// tryUnmarshalWithTolerance runs json.Unmarshal, and on
// failure tries once more after stripping trailing commas.
// Returns the unmarshaled data, a bool indicating whether
// the trailing-comma fallback was used, and any error from
// the final attempt.
//
// The fallback is intentionally narrow: a strict spec
// parser is the ground truth, and we only deviate to keep
// a hand-edited config bootable. We never silently fix
// missing fields or rename keys.
func tryUnmarshalWithTolerance(data []byte, v any) ([]byte, bool, error) {
	if err := json.Unmarshal(data, v); err == nil {
		return data, false, nil
	} else {
		cleaned := stripTrailingCommas(data)
		if err := json.Unmarshal(cleaned, v); err == nil {
			return cleaned, true, nil
		} else {
			return nil, false, err
		}
	}
}

// Load merges global (~/.p-chat/config.json) and project
// (.p-chat/config.json) configs, then a custom path on top.
//
// Behavior:
//   - First call attempts to load the JSON files.
//   - If the JSON global file is missing BUT a legacy YAML
//     (config.yaml) exists, the YAML is read once and migrated
//     to JSON. The original YAML is preserved (we copy, not move)
//     so users can roll back if needed.
//   - Missing files are not an error; the default config is used
//     for the missing layer.
//   - A leading UTF-8 BOM in the JSON file is silently stripped
//     (some Windows tools add one).
//   - When a customPath is passed and cannot be read / parsed,
//     an error is returned (the user explicitly asked for it).
func Load(customPath string) (*Config, error) {
	return LoadWithProjectRoot(customPath, "")
}

// LoadWithProjectRoot merges global, project-root, and custom configs.
// When projectRoot is non-empty, the project layer is loaded from
// `<projectRoot>/.p-chat/config.json` instead of os.Getwd().
func LoadWithProjectRoot(customPath, projectRoot string) (*Config, error) {
	cfg := Default()

	// Global layer.
	if data, err := os.ReadFile(paths.GlobalConfig()); err == nil {
		data = stripBOM(data)
		cleaned, tolerated, perr := tryUnmarshalWithTolerance(data, cfg)
		if perr != nil {
			return nil, fmt.Errorf("parse global config %s: %w", paths.GlobalConfig(), perr)
		}
		if tolerated {
			// Botched trailing comma. Rewrite the file with the
			// cleaned bytes so subsequent starts are fast and the
			// user can see what was wrong by diffing. We keep the
			// atomic-rename pattern from writeConfigJSON so a crash
			// mid-write doesn't leave the file half-written.
			if err := writeConfigJSON(paths.GlobalConfig(), cfg); err != nil {
				// Not fatal — the in-memory cfg is fine for this
				// session. Log so the user can fix by hand.
				fmt.Printf("pchat: rewrite cleaned global config: %v\n", err)
			}
			_ = cleaned // silence unused
		}
	} else if os.IsNotExist(err) {
		// One-shot yaml → json migration.
		if yamlData, yerr := os.ReadFile(paths.GlobalConfigYAML()); yerr == nil {
			if jerr := unmarshalYAML(yamlData, cfg); jerr == nil {
				_ = writeConfigJSON(paths.GlobalConfig(), cfg)
				_ = os.WriteFile(paths.GlobalConfigYAML(), yamlData, 0o644)
			}
		} else {
			// No config at all — fresh install. Write the
			// default config so it exists on disk for future
			// edits and the --config argument works next time.
			_ = writeConfigJSON(paths.GlobalConfig(), cfg)
		}
	} else {
		return nil, fmt.Errorf("read global config: %w", err)
	}

	// Project layer (overrides global).
	var projectConfigPath string
	if projectRoot != "" {
		projectConfigPath = paths.ProjectConfigWithRoot(projectRoot)
	} else {
		projectConfigPath = paths.ProjectConfig()
	}
	if data, err := os.ReadFile(projectConfigPath); err == nil {
		data = stripBOM(data)
		_, _, perr := tryUnmarshalWithTolerance(data, cfg)
		if perr != nil {
			return nil, fmt.Errorf("parse project config %s: %w", projectConfigPath, perr)
		}
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read project config: %w", err)
	}

	// Custom path (highest priority).
	if customPath != "" {
		data, err := os.ReadFile(customPath)
		if err != nil {
			if os.IsNotExist(err) {
				// User passed --config pointing to a file that
				// doesn't exist yet (fresh install).  Treat it
				// as a no-op so the caller gets a valid default
				// config that can be saved later.
				return cfg, nil
			}
			return nil, fmt.Errorf("read config %s: %w", customPath, err)
		}
		data = stripBOM(data)
		_, _, perr := tryUnmarshalWithTolerance(data, cfg)
		if perr != nil {
			return nil, fmt.Errorf("parse config %s: %w", customPath, perr)
		}
	}

	migrateKnowledgeDefaults(cfg)

	return cfg, nil
}

// writeConfigJSON is the package-private helper used by both the
// Manager and the migration path. Errors are returned so the caller
// can decide whether to surface them (Manager surfaces, migration
// swallows).
//
// The write is atomic: marshal to a sibling temp file, fsync, then
// rename over the destination. A crash mid-write leaves the
// original config intact instead of producing a truncated/
// unparseable file.
func writeConfigJSON(path string, cfg *Config) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".config-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		// Best-effort cleanup if rename was never reached.
		if _, statErr := os.Stat(tmpName); statErr == nil {
			_ = os.Remove(tmpName)
		}
	}()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
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
			MaxHistory: 0,
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
			// Enable result caching by default so repeated sub-agent
			// tasks (e.g. searching the same file) hit the cache.
			CacheTTL: "10m",
		},
		MCP: MCPConfig{
			Enabled: false,
		},
		Knowledge: KnowledgeConfig{
			Enabled:   false,
			AutoIndex: false,
		},
		Search: SearchConfig{
			Enabled:        false,
			Provider:       "tavily",
			RequestTimeout: 20 * time.Second,
		},
		Browser: BrowserConfig{
			Enabled:                 false,
			ScreenshotQuality:       80,
			MaxScreenshotsInHistory: 3,
		},
	}
}

// defaultWriteProtectedPaths returns the conservative baseline list of
// paths that LLM-driven tools should never touch. Users can override
// or extend this list in their config.json.
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
