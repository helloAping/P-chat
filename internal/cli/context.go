package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/p-chat/pchat/internal/agent"
	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/httpcli"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/rules"
	"github.com/p-chat/pchat/internal/skill"
	"github.com/p-chat/pchat/internal/style"
	"github.com/p-chat/pchat/internal/tool"
)

// cliContext abstracts everything slash commands need to know about
// the underlying state. The local REPL satisfies it via direct
// in-process access to memory/agent/llm; the HTTP REPL satisfies it
// via the httpcli.Client. The same cmdModel / cmdHistory / etc.
// handlers work for both modes.
//
// Method-shape notes:
//   - Setter methods may return an error in HTTP mode (e.g. when the
//     server doesn't expose the corresponding endpoint yet) but are
//     always safe to call; local mode is always a no-error operation.
//   - Methods that don't have a meaningful HTTP counterpart (e.g.
//     project-file reads for /init, /skills, /rules, /agents) return
//     *ErrUnsupported so the handler can show a friendly "only
//     available in local mode" message.
type cliContext interface {
	// === Sessions ===
	ListSessions(ctx context.Context) ([]httpcli.Session, error)
	GetCurrentSessionID() string
	SetCurrentSession(id string) error
	GetCurrentSessionMessages(ctx context.Context) ([]httpcli.Message, error)
	NewSession(ctx context.Context, opts httpcli.CreateSessionOpts) (*httpcli.Session, error)
	RenameSession(ctx context.Context, id, title string) error
	DeleteSession(ctx context.Context, id string) error
	CurrentMessageCount() int
	SubmitQuestionAnswer(ctx context.Context, sessionID string, answers map[string]string) error

	// === Models / providers ===
	ListProviders(ctx context.Context) ([]httpcli.ProviderInfo, error)
	ListProviderModels(ctx context.Context, provider string) ([]httpcli.Model, error)
	GetCurrentProvider() string
	SetCurrentProvider(name string) error
	GetCurrentModel() string
	GetProviderProtocol(provider string) string
	SetModel(provider, model string) error
	HasProvider(name string) bool
	DisplayModel(provider string) string
	// ProviderConfig returns the rich config (BaseURL, APIKey, Models
	// list) for a single provider. Used by /provider and /setup which
	// need to read fields not on the public httpcli.ProviderInfo.
	ProviderConfig(name string) (ProviderConfigView, error)
	// AddProvider / RemoveProvider / SetProviderAPIKey / SetDefaultProvider
	// persist a config change and rebuild the in-process LLM client.
	// They return an error in HTTP mode (no server endpoint yet).
	AddProvider(p ProviderConfigInput) error
	RemoveProvider(name string) error
	SetProviderAPIKey(name, key string) error
	SetDefaultProvider(name string) error
	// Model management (for /config model ...)
	AddModel(provider, name, display, desc string) error
	AddModelFull(provider string, m config.ModelConfig) error
	UpdateModel(provider, name string, patch config.ModelConfig) error
	RemoveModel(provider, name string) error
	SetDefaultModel(provider, name string) error
	// ListAllModels returns (provider, model, display, isDefault) for
	// every model across every provider, used by /config model list.
	ListAllModels(provider string) []ModelView
	// GetModelSettings returns the per-model MaxTokensContext /
	// MaxTokensOutput overrides, or zero if unknown.
	GetModelSettings(provider, model string) ModelSettings
	// ReloadConfig re-reads the on-disk config and rebuilds the LLM
	// client. Returns an error in HTTP mode.
	ReloadConfig() error

	// === Chat ===
	// ChatWithTools streams the assistant reply (with optional tool
	// use). The handler is expected to push events to the ChatUI.
	ChatWithTools(ctx context.Context, req agent.ChatRequest) (<-chan agent.ChatStreamChunk, error)
	// ChatStream is the no-tool variant, used by /plan for the
	// single-round plan-mode call. In local mode this routes through
	// the agent's plain stream; in HTTP mode it sends a non-tool
	// message (the server decides).
	ChatStream(ctx context.Context, req agent.ChatRequest) (<-chan agent.ChatStreamChunk, error)

	// === Style / tools ===
	StyleLabel(s style.Style) string
	ListStyles() []style.Style
	StyleName() string
	SetStyle(name string) error
	ListTools() []ToolView
	ToolsEnabled() bool
	SetToolsEnabled(on bool)

	// === Sandbox ===
	SetSandbox(enabled bool)
	BypassSandboxOnce()
	// RebuildSandbox re-creates the sandbox from the current config
	// (used by /unsafe off). HTTP mode is a no-op.
	RebuildSandbox() error

	// === Tool result cache (for /expand) ===
	// ExpandList returns one-line summaries of all stored results.
	ExpandList() []ToolResultView
	// ExpandByIndex returns the full result for the 1-based index.
	ExpandByIndex(idx int) (ToolResultView, bool)
	// ExpandLast returns the most recent result.
	ExpandLast() (ToolResultView, bool)

	// === Diagnostics ===
	// SubAgentStats returns hit/miss/store counts + hit ratio. Returns
	// ok=false if the cache is disabled or unknown (HTTP mode).
	SubAgentStats() (entries, hits, misses, stores int, hitRatio float64, ok bool)
	// MemoryStats returns high-level memory-store stats. Returns
	// ok=false if the store is disabled or unknown (HTTP mode).
	MemoryStats() (conversations, messages int, currentSession string, ok bool)

	// === Persistence ===
	// Flush writes any pending state to disk. No-op for the HTTP
	// client (the server handles persistence).
	Flush() error

	// === Knowledge base (for /kb) ===
	ListKBs() ([]KBView, error)
	AddKB(path string) error
	RemoveKB(path string) error
	ScanKBs() (added, updated int, err error)

	// === Recall (for /recall) ===
	Recall(ctx context.Context, query string, topK int) error

	// === Project context (for /init, /skills, /rules, /agents) ===
	// These are inherently local-filesystem operations. In HTTP
	// mode they return ErrUnsupported so the handler can show a
	// friendly "only available locally" message.
	InitProject(dir string) error
	ListSkills() ([]string, error)
	ListRules() ([]string, error)
	AgentsContext() (global, project string, err error)
}

// ProviderConfigView is a read-only snapshot of a provider's
// on-disk config. Used by /provider and /setup.
type ProviderConfigView struct {
	Name     string
	Protocol string
	BaseURL  string
	APIKey   string
	Model    string
	Models   []ModelView
}

// ProviderConfigInput is the writer-side shape for AddProvider.
type ProviderConfigInput struct {
	Name     string
	Protocol string
	BaseURL  string
	APIKey   string
	Model    string
}

// ModelView is a single (provider, model) entry shown in /model and
// /config model list. The fields are a copy of config.ModelConfig
// trimmed of internal bookkeeping.
type ModelView struct {
	Provider    string
	Name        string
	DisplayName string
	Description string
	Default     bool
}

// ModelSettings is the (MaxTokensContext, MaxTokensOutput) pair for
// a single (provider, model). Zero values mean "unset".
type ModelSettings struct {
	MaxTokensContext int
	MaxTokensOutput  int
}

// ToolView is a single registered tool's metadata for /tools.
type ToolView struct {
	Name        string
	Description string
	Highlight   bool // e.g. true for `task` (sub-agent spawner)
}

// ToolResultView is a snapshot of a cached tool call result.
type ToolResultView struct {
	Seq      int
	Tool     string
	Args     string
	Result   string
	Err      string
	Duration time.Duration
	At       time.Time
}

// KBView is one mounted knowledge base directory.
type KBView struct {
	Path  string
	Files int
	Size  int64
}

// ErrUnsupported is returned by cliContext methods that are inherently
// local-filesystem operations when running in HTTP mode. Handlers
// should detect it (via errors.As) and show a friendly message.
type ErrUnsupported struct{ Op string }

func (e *ErrUnsupported) Error() string {
	return "operation " + e.Op + " is not supported in HTTP mode"
}

// ====================================================================
// localContext: backed by direct in-process access (legacy mode)
// ====================================================================

type localContext struct {
	r *REPL
}

func (c *localContext) ListSessions(ctx context.Context) ([]httpcli.Session, error) {
	convs := c.r.store.ListConversations()
	out := make([]httpcli.Session, 0, len(convs))
	for _, conv := range convs {
		out = append(out, httpcli.Session{
			ID:        conv.ID,
			Title:     conv.Title,
			CreatedAt: conv.CreatedAt.Unix(),
			UpdatedAt: conv.UpdatedAt.Unix(),
		})
	}
	return out, nil
}

func (c *localContext) GetCurrentSessionID() string {
	return c.r.store.CurrentConversationID()
}

func (c *localContext) SetCurrentSession(id string) error {
	return c.r.store.SetCurrent(id)
}

func (c *localContext) GetCurrentSessionMessages(ctx context.Context) ([]httpcli.Message, error) {
	raw := c.r.store.GetMessages()
	out := make([]httpcli.Message, 0, len(raw))
	for _, m := range raw {
		out = append(out, httpcli.Message{
			Role:       m.Role,
			Content:    m.Content,
			Name:       m.Name,
			ToolCallID: m.ToolCallID,
			CreatedAt:  time.Now().Unix(),
		})
	}
	return out, nil
}

func (c *localContext) NewSession(ctx context.Context, opts httpcli.CreateSessionOpts) (*httpcli.Session, error) {
	id, err := c.r.store.NewConversation()
	if err != nil {
		return nil, err
	}
	if opts.Title != "" {
		_ = c.r.store.RenameConversation(id, opts.Title)
	}
	for _, conv := range c.r.store.ListConversations() {
		if conv.ID == id {
			return &httpcli.Session{
				ID:        conv.ID,
				Title:     conv.Title,
				CreatedAt: conv.CreatedAt.Unix(),
				UpdatedAt: conv.UpdatedAt.Unix(),
			}, nil
		}
	}
	return &httpcli.Session{ID: id}, nil
}

func (c *localContext) RenameSession(ctx context.Context, id, title string) error {
	return c.r.store.RenameConversation(id, title)
}

func (c *localContext) DeleteSession(ctx context.Context, id string) error {
	return c.r.store.DeleteConversation(id)
}

func (c *localContext) CurrentMessageCount() int {
	return c.r.store.ConversationMessageCount()
}

func (c *localContext) SubmitQuestionAnswer(ctx context.Context, sessionID string, answers map[string]string) error {
	resp := tool.QuestionResponse{Answers: answers}
	if !tool.SubmitAnswer(sessionID, resp) {
		return fmt.Errorf("no pending question for session %s", sessionID)
	}
	return nil
}

func (c *localContext) ListProviders(ctx context.Context) ([]httpcli.ProviderInfo, error) {
	ps := c.r.cfg.LLM.Providers
	out := make([]httpcli.ProviderInfo, 0, len(ps))
	for _, p := range ps {
		out = append(out, httpcli.ProviderInfo{
			Name:     p.Name,
			Model:    p.EffectiveModel(),
			Protocol: p.GetProtocol(),
		})
	}
	return out, nil
}

func (c *localContext) ListProviderModels(ctx context.Context, provider string) ([]httpcli.Model, error) {
	for _, p := range c.r.cfg.LLM.Providers {
		if p.Name != provider {
			continue
		}
		src := p.AllModels()
		out := make([]httpcli.Model, 0, len(src))
		for _, m := range src {
			out = append(out, httpcli.Model{
				Name:        m.Name,
				DisplayName: m.DisplayName,
				Default:     m.Default,
				Description: m.Description,
			})
		}
		return out, nil
	}
	return nil, nil
}

func (c *localContext) GetCurrentProvider() string  { return c.r.provider }
func (c *localContext) GetCurrentModel() string     { return c.r.llm.GetModel(c.r.provider) }
func (c *localContext) GetProviderProtocol(p string) string {
	for _, pp := range c.r.cfg.LLM.Providers {
		if pp.Name == p {
			return pp.GetProtocol()
		}
	}
	return ""
}

func (c *localContext) SetCurrentProvider(name string) error {
	c.r.provider = name
	return nil
}

func (c *localContext) SetModel(p, m string) error    { return c.r.llm.SetModel(p, m) }
func (c *localContext) HasProvider(n string) bool     { return c.r.llm.HasProvider(n) }
func (c *localContext) DisplayModel(p string) string  { return c.r.llm.DisplayModel(p) }

func (c *localContext) ProviderConfig(name string) (ProviderConfigView, error) {
	for _, p := range c.r.cfg.LLM.Providers {
		if p.Name != name {
			continue
		}
		models := make([]ModelView, 0, len(p.Models))
		for _, m := range p.Models {
			models = append(models, ModelView{
				Provider:    p.Name,
				Name:        m.Name,
				DisplayName: m.DisplayName,
				Description: m.Description,
				Default:     m.Default,
			})
		}
		return ProviderConfigView{
			Name:     p.Name,
			Protocol: p.GetProtocol(),
			BaseURL:  p.BaseURL,
			APIKey:   p.APIKey,
			Model:    p.EffectiveModel(),
			Models:   models,
		}, nil
	}
	return ProviderConfigView{}, &ErrUnsupported{Op: "ProviderConfig: provider not found"}
}

func (c *localContext) AddProvider(p ProviderConfigInput) error {
	if err := config.AddProvider(config.ProviderConfig{
		Name:     p.Name,
		Protocol: p.Protocol,
		BaseURL:  p.BaseURL,
		APIKey:   p.APIKey,
		Model:    p.Model,
	}); err != nil {
		return err
	}
	c.r.reloadConfig()
	return nil
}

func (c *localContext) RemoveProvider(name string) error {
	if err := config.RemoveProvider(name); err != nil {
		return err
	}
	c.r.reloadConfig()
	return nil
}

func (c *localContext) SetProviderAPIKey(name, key string) error {
	if err := config.SetProviderAPIKey(name, key); err != nil {
		return err
	}
	c.r.reloadConfig()
	return nil
}

func (c *localContext) SetDefaultProvider(name string) error {
	if err := config.SetDefaultProvider(name); err != nil {
		return err
	}
	if err := c.r.reloadLLMClient(); err != nil {
		return err
	}
	c.r.provider = name
	return nil
}

func (c *localContext) AddModel(provider, name, display, desc string) error {
	updated, err := config.AddModel(provider, config.ModelConfig{
		Name:        name,
		DisplayName: display,
		Description: desc,
	})
	if err != nil {
		return err
	}
	_ = updated
	return c.r.reloadLLMClient()
}

func (c *localContext) AddModelFull(provider string, m config.ModelConfig) error {
	if _, err := config.AddModel(provider, m); err != nil {
		return err
	}
	return c.r.reloadLLMClient()
}

func (c *localContext) UpdateModel(provider, name string, patch config.ModelConfig) error {
	if _, err := config.UpdateModel(provider, name, patch, false); err != nil {
		return err
	}
	return c.r.reloadLLMClient()
}

func (c *localContext) GetModelSettings(provider, model string) ModelSettings {
	for _, p := range c.r.cfg.LLM.Providers {
		if p.Name != provider {
			continue
		}
		if m := p.FindModel(model); m != nil {
			return ModelSettings{
				MaxTokensContext: m.MaxTokensContext,
				MaxTokensOutput:  m.MaxTokensOutput,
			}
		}
		return ModelSettings{}
	}
	return ModelSettings{}
}

func (c *localContext) RemoveModel(provider, name string) error {
	if err := config.RemoveModel(provider, name); err != nil {
		return err
	}
	return c.r.reloadLLMClient()
}

func (c *localContext) SetDefaultModel(provider, name string) error {
	if err := config.SetDefaultModel(provider, name); err != nil {
		return err
	}
	return c.r.reloadLLMClient()
}

func (c *localContext) ListAllModels(provider string) []ModelView {
	var out []ModelView
	for _, p := range c.r.cfg.LLM.Providers {
		if provider != "" && p.Name != provider {
			continue
		}
		// Legacy single-model form.
		if len(p.Models) == 0 {
			if p.Model != "" {
				out = append(out, ModelView{Provider: p.Name, Name: p.Model})
			}
			continue
		}
		for _, m := range p.Models {
			out = append(out, ModelView{
				Provider:    p.Name,
				Name:        m.Name,
				DisplayName: m.DisplayName,
				Description: m.Description,
				Default:     m.Default,
			})
		}
	}
	return out
}

func (c *localContext) ReloadConfig() error {
	c.r.reloadConfig()
	return nil
}

func (c *localContext) ChatWithTools(ctx context.Context, req agent.ChatRequest) (<-chan agent.ChatStreamChunk, error) {
	return c.r.agent.ChatWithTools(ctx, req), nil
}

func (c *localContext) ChatStream(ctx context.Context, req agent.ChatRequest) (<-chan agent.ChatStreamChunk, error) {
	if c.r.agent == nil {
		ch := make(chan agent.ChatStreamChunk)
		close(ch)
		return ch, nil
	}
	return c.r.agent.ChatStream(ctx, req), nil
}

func (c *localContext) StyleLabel(s style.Style) string { return c.r.styleMgr.Label(s) }
func (c *localContext) ListStyles() []style.Style        { return c.r.styleMgr.List() }
func (c *localContext) StyleName() string                 { return string(c.r.style) }

func (c *localContext) SetStyle(name string) error {
	s, err := style.ParseStyle(name)
	if err != nil {
		return err
	}
	c.r.style = s
	return nil
}

func (c *localContext) ListTools() []ToolView {
	out := make([]ToolView, 0)
	if c.r.tools == nil {
		return out
	}
	for _, t := range c.r.tools.List() {
		out = append(out, ToolView{
			Name:        t.Name,
			Description: t.Description,
			Highlight:   t.Name == "task",
		})
	}
	return out
}

func (c *localContext) ToolsEnabled() bool     { return c.r.useTools }
func (c *localContext) SetToolsEnabled(on bool) { c.r.useTools = on }

func (c *localContext) SubAgentStats() (entries, hits, misses, stores int, hitRatio float64, ok bool) {
	if c.r.subCache == nil {
		return 0, 0, 0, 0, 0, false
	}
	s := c.r.subCache.Stats()
	return s.Entries, int(s.Hits), int(s.Misses), int(s.Stores), s.HitRatio, true
}

func (c *localContext) MemoryStats() (conversations, messages int, currentSession string, ok bool) {
	if c.r.store == nil {
		return 0, 0, "", false
	}
	convs := c.r.store.ListConversations()
	return len(convs), c.r.store.ConversationMessageCount(), c.r.store.CurrentConversationID(), true
}

func (c *localContext) SetSandbox(enabled bool) {
	if enabled {
		sbx, err := newSandbox(c.r.cfg.Sandbox)
		if err == nil {
			c.r.agent.SetSandbox(sbx)
		}
	} else {
		c.r.agent.SetSandbox(nil)
	}
}

func (c *localContext) BypassSandboxOnce() { c.r.agent.BypassSandboxOnce() }

func (c *localContext) RebuildSandbox() error {
	if c.r.cfg == nil {
		return nil
	}
	sbx, err := newSandbox(c.r.cfg.Sandbox)
	if err != nil {
		return err
	}
	c.r.agent.SetSandbox(sbx)
	return nil
}

func (c *localContext) ExpandList() []ToolResultView {
	if c.r.toolCache == nil {
		return nil
	}
	src := c.r.toolCache.list()
	out := make([]ToolResultView, 0, len(src))
	for _, t := range src {
		out = append(out, ToolResultView{
			Seq:      t.seq,
			Tool:     t.tool,
			Args:     t.args,
			Result:   t.result,
			Err:      t.err,
			Duration: t.duration,
			At:       t.at,
		})
	}
	return out
}

func (c *localContext) ExpandByIndex(idx int) (ToolResultView, bool) {
	if c.r.toolCache == nil {
		return ToolResultView{}, false
	}
	r := c.r.toolCache.get(idx)
	if r == nil {
		return ToolResultView{}, false
	}
	return ToolResultView{
		Seq: r.seq, Tool: r.tool, Args: r.args,
		Result: r.result, Err: r.err, Duration: r.duration, At: r.at,
	}, true
}

func (c *localContext) ExpandLast() (ToolResultView, bool) {
	if c.r.toolCache == nil {
		return ToolResultView{}, false
	}
	r := c.r.toolCache.last()
	if r == nil {
		return ToolResultView{}, false
	}
	return ToolResultView{
		Seq: r.seq, Tool: r.tool, Args: r.args,
		Result: r.result, Err: r.err, Duration: r.duration, At: r.at,
	}, true
}

func (c *localContext) ListKBs() ([]KBView, error) {
	if c.r.kbManager == nil {
		return nil, &ErrUnsupported{Op: "ListKBs"}
	}
	return c.r.kbManager.Views(), nil
}

func (c *localContext) AddKB(path string) error {
	if c.r.kbManager == nil {
		return &ErrUnsupported{Op: "AddKB"}
	}
	return c.r.kbManager.AddPath(path)
}

func (c *localContext) RemoveKB(path string) error {
	if c.r.kbManager == nil {
		return &ErrUnsupported{Op: "RemoveKB"}
	}
	return c.r.kbManager.RemovePath(path)
}

func (c *localContext) ScanKBs() (added, updated int, err error) {
	if c.r.kbManager == nil {
		return 0, 0, &ErrUnsupported{Op: "ScanKBs"}
	}
	return c.r.kbManager.ScanStats()
}

func (c *localContext) Recall(ctx context.Context, query string, topK int) error {
	if c.r.recallEngine == nil {
		return &ErrUnsupported{Op: "Recall"}
	}
	return c.r.recallEngine.PrintSearch(ctx, query, topK)
}

func (c *localContext) InitProject(dir string) error {
	return RunInit()
}

func (c *localContext) ListSkills() ([]string, error) {
	all, err := skill.LoadAll()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(all))
	for _, s := range all {
		out = append(out, s.Name)
	}
	return out, nil
}

func (c *localContext) ListRules() ([]string, error) {
	all, err := rules.LoadAll()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(all))
	for _, r := range all {
		out = append(out, r.Name)
	}
	return out, nil
}

func (c *localContext) AgentsContext() (global, project string, err error) {
	home, _ := os.UserHomeDir()
	globalPath := filepath.Join(home, ".p-chat", "AGENTS.md")
	projectPath := "AGENTS.md"

	if data, e := os.ReadFile(globalPath); e == nil {
		global = string(data)
	}
	if data, e := os.ReadFile(projectPath); e == nil {
		project = string(data)
	}
	return global, project, nil
}

func (c *localContext) Flush() error { return c.r.store.Flush() }

// ====================================================================
// httpContext: backed by httpcli.Client
//
// This is a stub that makes the interface satisfiable so the CLI
// can run against a remote pchat-server. Operations that the server
// doesn't yet support return *ErrUnsupported so the handler can
// print a friendly "only available in local mode" message.
// ====================================================================

type httpContext struct {
	c       *httpcli.Client
	style   string
	prov    string
	curSess string
}

// NewHTTPContext creates a cliContext backed by an httpcli.Client.
// All operations go through the pchat-server REST API.
func NewHTTPContext(c *httpcli.Client, style, provider, sessionID string) cliContext {
	return &httpContext{
		c:       c,
		style:   style,
		prov:    provider,
		curSess: sessionID,
	}
}

func (c *httpContext) unsupported(op string) error {
	return &ErrUnsupported{Op: op}
}

func (c *httpContext) ListProviders(ctx context.Context) ([]httpcli.ProviderInfo, error) {
	return c.c.ListProviders(ctx)
}

func (c *httpContext) ListProviderModels(ctx context.Context, provider string) ([]httpcli.Model, error) {
	// The server doesn't expose a per-provider list-models endpoint
	// yet; synthesize from cached provider info if available.
	models, _ := c.c.ModelsFor(provider)
	out := make([]httpcli.Model, 0, len(models))
	for _, m := range models {
		out = append(out, m)
	}
	return out, nil
}

func (c *httpContext) GetCurrentProvider() string  { return c.prov }
func (c *httpContext) GetCurrentModel() string     { return c.c.ProviderModel() }
func (c *httpContext) GetProviderProtocol(p string) string { return "" }
func (c *httpContext) SetCurrentProvider(name string) error {
	c.prov = name
	return nil
}
func (c *httpContext) SetModel(p, m string) error  { return c.c.SetModel(p, m) }
func (c *httpContext) HasProvider(n string) bool   { return true }
func (c *httpContext) DisplayModel(p string) string { return c.c.DisplayModel(p) }
func (c *httpContext) ProviderConfig(name string) (ProviderConfigView, error) {
	return ProviderConfigView{}, c.unsupported("ProviderConfig")
}
func (c *httpContext) AddProvider(p ProviderConfigInput) error    { return c.unsupported("AddProvider") }
func (c *httpContext) RemoveProvider(name string) error           { return c.unsupported("RemoveProvider") }
func (c *httpContext) SetProviderAPIKey(name, key string) error  { return c.unsupported("SetProviderAPIKey") }
func (c *httpContext) SetDefaultProvider(name string) error      { return c.unsupported("SetDefaultProvider") }
func (c *httpContext) AddModel(p, n, d, dd string) error         { return c.unsupported("AddModel") }
func (c *httpContext) AddModelFull(p string, m config.ModelConfig) error { return c.unsupported("AddModelFull") }
func (c *httpContext) UpdateModel(p, n string, patch config.ModelConfig) error { return c.unsupported("UpdateModel") }
func (c *httpContext) RemoveModel(p, n string) error              { return c.unsupported("RemoveModel") }
func (c *httpContext) SetDefaultModel(p, n string) error          { return c.unsupported("SetDefaultModel") }
func (c *httpContext) ListAllModels(provider string) []ModelView  { return nil }
func (c *httpContext) GetModelSettings(p, m string) ModelSettings { return ModelSettings{} }
func (c *httpContext) ReloadConfig() error                        { return c.unsupported("ReloadConfig") }

func (c *httpContext) ListSessions(ctx context.Context) ([]httpcli.Session, error) {
	return c.c.ListSessions(ctx)
}
func (c *httpContext) GetCurrentSessionID() string         { return c.curSess }
func (c *httpContext) SetCurrentSession(id string) error   { c.curSess = id; return nil }
func (c *httpContext) GetCurrentSessionMessages(ctx context.Context) ([]httpcli.Message, error) {
	return c.c.ListMessages(ctx, c.curSess)
}
func (c *httpContext) NewSession(ctx context.Context, opts httpcli.CreateSessionOpts) (*httpcli.Session, error) {
	return c.c.CreateSession(ctx, opts)
}
func (c *httpContext) RenameSession(ctx context.Context, id, title string) error {
	return c.c.RenameSession(ctx, id, title)
}
func (c *httpContext) DeleteSession(ctx context.Context, id string) error {
	return c.c.DeleteSession(ctx, id)
}
func (c *httpContext) CurrentMessageCount() int { return 0 }

func (c *httpContext) SubmitQuestionAnswer(ctx context.Context, sessionID string, answers map[string]string) error {
	return c.c.SubmitQuestionResponse(ctx, sessionID, answers)
}

func (c *httpContext) ChatWithTools(ctx context.Context, req agent.ChatRequest) (<-chan agent.ChatStreamChunk, error) {
	out := make(chan agent.ChatStreamChunk, 16)
	go func() {
		defer close(out)
		opts := httpcli.SendMessageOptions{
			Message: lastUserContent(req.Messages),
			Style:   string(req.Style),
		}
		err := c.c.SendMessage(ctx, c.curSess, opts, func(ev httpcli.StreamEvent) {
			out <- httpEventToChunk(ev)
		})
		if err != nil {
			out <- agent.ChatStreamChunk{Error: err.Error(), Done: true}
			return
		}
		out <- agent.ChatStreamChunk{Done: true}
	}()
	return out, nil
}

func (c *httpContext) ChatStream(ctx context.Context, req agent.ChatRequest) (<-chan agent.ChatStreamChunk, error) {
	// The current server endpoint always runs through the agent;
	// for the "no tools" path we just delegate to ChatWithTools.
	return c.ChatWithTools(ctx, req)
}

func (c *httpContext) StyleLabel(s style.Style) string { return s.DisplayName() }
func (c *httpContext) ListStyles() []style.Style {
	return []style.Style{style.Cute, style.Guofeng, style.Tech}
}
func (c *httpContext) StyleName() string         { return c.style }
func (c *httpContext) SetStyle(name string) error { c.style = name; return nil }
func (c *httpContext) ListTools() []ToolView     { return nil }
func (c *httpContext) ToolsEnabled() bool        { return true }
func (c *httpContext) SetToolsEnabled(on bool)   {}

func (c *httpContext) SetSandbox(bool)             { /* TODO: not supported over HTTP yet */ }
func (c *httpContext) BypassSandboxOnce()         {}
func (c *httpContext) RebuildSandbox() error       { return c.unsupported("RebuildSandbox") }

func (c *httpContext) ExpandList() []ToolResultView                { return nil }
func (c *httpContext) ExpandByIndex(idx int) (ToolResultView, bool) { return ToolResultView{}, false }
func (c *httpContext) ExpandLast() (ToolResultView, bool)          { return ToolResultView{}, false }

func (c *httpContext) SubAgentStats() (int, int, int, int, float64, bool) {
	return 0, 0, 0, 0, 0, false
}
func (c *httpContext) MemoryStats() (int, int, string, bool) {
	return 0, 0, "", false
}

func (c *httpContext) Flush() error { return c.c.Flush() }

func (c *httpContext) ListKBs() ([]KBView, error)        { return nil, c.unsupported("ListKBs") }
func (c *httpContext) AddKB(path string) error          { return c.unsupported("AddKB") }
func (c *httpContext) RemoveKB(path string) error       { return c.unsupported("RemoveKB") }
func (c *httpContext) ScanKBs() (int, int, error)       { return 0, 0, c.unsupported("ScanKBs") }
func (c *httpContext) Recall(ctx context.Context, query string, topK int) error {
	return c.unsupported("Recall")
}
func (c *httpContext) InitProject(dir string) error     { return c.unsupported("InitProject") }
func (c *httpContext) ListSkills() ([]string, error)    { return nil, c.unsupported("ListSkills") }
func (c *httpContext) ListRules() ([]string, error)     { return nil, c.unsupported("ListRules") }
func (c *httpContext) AgentsContext() (string, string, error) {
	return "", "", c.unsupported("AgentsContext")
}

// lastUserContent extracts the trailing user message from a list
// of agent messages, matching the chat command's normal use.
func lastUserContent(msgs []llm.ChatMessage) string {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == llm.RoleUser {
			return msgs[i].Content
		}
	}
	if len(msgs) > 0 {
		return msgs[len(msgs)-1].Content
	}
	return ""
}

// httpEventToChunk converts an HTTP stream event into the
// agent.ChatStreamChunk shape used by the local UI renderer.
func httpEventToChunk(ev httpcli.StreamEvent) agent.ChatStreamChunk {
	return agent.ChatStreamChunk{
		Content:      ev.Content,
		Phase:        ev.Phase,
		Step:         ev.Step,
		Message:      ev.Msg,
		ToolName:     ev.ToolName,
		ToolResult:   ev.ToolResult,
		ToolError:    ev.ToolError,
		ToolElapsed:  ev.ToolElapsed,
		TokensIn:     ev.TokensIn,
		TokensOut:    ev.TokensOut,
		Duration:     ev.Elapsed,
		Error:        ev.Error,
		QuestionJSON: ev.QuestionJSON,
	}
}

// ====================================================================
// Type-predicate helpers used by handlers to decide whether the
// underlying context is the in-process localContext (and therefore
// can perform filesystem / config-write operations the HTTP stub
// can't) or the httpContext (which has the bare-minimum surface for
// sessions / providers / models).
// ====================================================================

// isLocalContext returns true when ctx is backed by the in-process
// REPL. Handlers that need to reach into *REPL (e.g. for the plan
// flow's chat-loop coordination or for /export's file output) check
// this to fall through to the local path.
func isLocalContext(ctx cliContext) bool {
	_, ok := ctx.(*localContext)
	return ok
}

// asLocalContext unwraps ctx to *localContext. Panics if ctx is not
// a *localContext (callers must guard with isLocalContext first).
func asLocalContext(ctx cliContext) *localContext {
	return ctx.(*localContext)
}

// isUnsupported reports whether err is an *ErrUnsupported (i.e. the
// operation isn't available in the current context — most often
// because the CLI is connected to a remote pchat-server that
// doesn't expose the corresponding endpoint).
func isUnsupported(err error) bool {
	if err == nil {
		return false
	}
	var unsup *ErrUnsupported
	return errors.As(err, &unsup)
}

// runLocalPlan is the REPL-side implementation of /plan. It lives in
// repl.go because it depends on *REPL's chat-loop coordination
// (cancel + watchEsc). The cmdPlan handler in commands.go just
// dispatches here when isLocalContext is true.
func runLocalPlan(ctx cliContext, args string) error {
	lc := asLocalContext(ctx)
	r := lc.r

	task := strings.TrimSpace(args)
	if task == "" {
		color.HiBlack("  用法: /plan <任务>")
		return nil
	}
	if r.agent == nil {
		color.HiBlack("  (无 agent 上下文)")
		return nil
	}

	// Build the request: full history + the task as a user message.
	msgs := []llm.ChatMessage{}
	if r.store != nil {
		for _, m := range r.store.GetChatMessages() {
			if m.Type == llm.TypeImage {
				continue
			}
			msgs = append(msgs, m)
		}
	}
	msgs = append(msgs, llm.ChatMessage{
		Role:    llm.RoleUser,
		Type:    llm.TypeText,
		Content: task,
	})

	provModel := r.llm.GetModel(r.provider)
	ui := NewChatUI(r.provider, provModel)
	ui.PrintBannerHeader("/plan " + task)

	// Plan mode disables tools and limits to 1 round (handled in agent).
	req := agent.ChatRequest{
		Style:    r.style,
		Provider: r.provider,
		Messages: msgs,
		PlanMode: true,
	}

	cctx, cancel := context.WithCancel(context.Background())
	r.mu.Lock()
	r.cancelCurrent = cancel
	r.mu.Unlock()
	defer func() {
		cancel()
		r.mu.Lock()
		r.cancelCurrent = nil
		r.mu.Unlock()
	}()

	go r.watchEsc(cctx, cancel)

	stream := r.agent.ChatWithTools(cctx, req)

	// Drain stream and accumulate the plan text.
	var planText strings.Builder
	for chunk := range stream {
		ui.Handle(chunk)
		if chunk.Content != "" {
			planText.WriteString(chunk.Content)
		}
	}
	ui.Finish()

	if planText.Len() == 0 {
		color.HiBlack("  (LLM 未生成计划)")
		return nil
	}

	// Ask the user to approve.
	fmt.Println()
	color.Cyan("  ─────────────────────────────────────────────────")
	color.Cyan("  请审阅上述计划:")
	color.Cyan("    [y] 执行    [n] 取消    [e] 编辑 (进入多行输入)")
	color.Cyan("  ─────────────────────────────────────────────────")
	fmt.Println()

	choice, err := readPlanDecision()
	if err != nil {
		color.HiBlack("  (读取失败: %v)", err)
		return nil
	}

	switch choice {
	case "y", "":
		return runLocalExecutePlan(ctx, msgs, planText.String(), provModel, task)

	case "e":
		edited, ok, err := multilineEditor(
			os.Stdin, os.Stdout,
			color.CyanString("进入编辑模式: 逐行输入, 单独一行 `.` 提交, `.cancel` 放弃, Ctrl+D 中断"),
			planText.String(),
		)
		if err != nil {
			color.Red("  ✗ 编辑失败: %v", err)
			return nil
		}
		if !ok {
			color.HiBlack("  已取消。计划未保存。")
			return nil
		}
		// Echo the edited plan so the user can see what they submitted.
		fmt.Println()
		color.Cyan("  ── 编辑后的计划 ──")
		fmt.Println(edited)
		color.Cyan("  ─────────────────────")
		fmt.Println()
		// Confirm before executing.
		choice2, _ := readPlanDecision()
		if choice2 != "y" && choice2 != "" {
			color.HiBlack("  已取消。计划未保存。")
			return nil
		}
		return runLocalExecutePlan(ctx, msgs, edited, provModel, task)

	default: // n, anything else
		color.HiBlack("  已取消。计划未保存。")
		return nil
	}
}

// runLocalExecutePlan runs the approved plan by sending the
// assistant plan + a "go" user message back to the LLM. Lives in
// repl.go alongside runLocalPlan.
func runLocalExecutePlan(ctx cliContext, msgs []llm.ChatMessage, plan, provModel, task string) error {
	lc := asLocalContext(ctx)
	r := lc.r
	color.Green("  ✓ 已批准。开始执行...")

	planReq := agent.ChatRequest{
		Style:    r.style,
		Provider: r.provider,
		Messages: append(msgs, llm.ChatMessage{
			Role:    llm.RoleAssistant,
			Type:    llm.TypeText,
			Content: plan,
		}, llm.ChatMessage{
			Role:    llm.RoleUser,
			Type:    llm.TypeText,
			Content: "好的，请按计划执行。",
		}),
	}
	ui := NewChatUI(r.provider, provModel)
	ui.PrintBannerHeader(task + " (执行)")

	cctx, cancel := context.WithCancel(context.Background())
	r.mu.Lock()
	r.cancelCurrent = cancel
	r.mu.Unlock()
	defer func() {
		cancel()
		r.mu.Lock()
		r.cancelCurrent = nil
		r.mu.Unlock()
	}()

	go r.watchEsc(cctx, cancel)

	stream := r.agent.ChatWithTools(cctx, planReq)
	for chunk := range stream {
		ui.Handle(chunk)
	}
	ui.Finish()
	return nil
}
