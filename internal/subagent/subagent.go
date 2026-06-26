// Package subagent implements spawning a fresh agent.Agent for a single
// focused sub-task. It is decoupled from the tool package (which only
// defines the tool interface) to avoid an import cycle:
//
//	tool -> agent -> tool   (not allowed)
//	tool -> subagent -> agent    (allowed)
package subagent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/p-chat/pchat/internal/agent"
	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/memory"
	"github.com/p-chat/pchat/internal/style"
	"github.com/p-chat/pchat/internal/tool"
	openai "github.com/sashabaranov/go-openai"
)

// Request is what the main agent passes to the runner.
type Request struct {
	Description string
	Style       style.Style
	Provider    string
}

// Result is what the runner returns. Content is the final assistant text
// (may be empty on error).
type Result struct {
	Content   string
	TokensIn  int
	TokensOut int
	Elapsed   time.Duration
	Rounds    int
}

// Runner spawns a fresh sub-agent and runs it synchronously. The CLI
// implements this; the `task` tool (in tool package) holds an interface
// field pointing at one of these.
type Runner interface {
	Run(ctx context.Context, req Request) (Result, error)
}

// Default is the production implementation: it builds a new agent.Agent
// with the parent's config, a fresh in-memory store, and a tool registry
// that **excludes the `task` tool** to prevent infinite recursion.
type Default struct {
	Cfg            *config.Config
	LLM            *llm.Client
	StyleMgr       *style.Manager
	ParentTools    *tool.Registry
	ParentStyle    style.Style
	ParentProvider string

	// Optional in-process result cache. When non-nil, identical
	// (description, style, provider) requests within the TTL return the
	// cached Result without re-running the sub-agent.
	Cache *Cache

	// OnEvent is an optional callback invoked for every sub-agent chunk
	// (with Content stripped). The parent uses this to forward events
	// to its own UI stream. When nil, sub-agent events are not surfaced
	// to the parent (the parent only sees the final result).
	OnEvent func(chunk agent.ChatStreamChunk)
}

// SubAgentToolFilter is a subset of config.SubAgentConfig that the
// `task` tool handler needs at call time. We pass a small struct (not
// the full config) so the subagent package doesn't depend on internal
// config plumbing.
type SubAgentToolFilter struct {
	// Allowed returns whether a given tool name is permitted.
	Allowed func(name string) bool
	// Timeout is the per-call cap.
	Timeout time.Duration
}

// tryForward sends a sub-agent event to the parent's OnEvent callback
// (if set). The callback is invoked synchronously and must not block; if
// it does, the sub-agent goroutine will be slowed down.
func tryForward(chunk agent.ChatStreamChunk, onEvent func(agent.ChatStreamChunk)) {
	if onEvent == nil {
		return
	}
	onEvent(chunk)
}

// Cache stores recent sub-agent results keyed by a hash of
// (description, style, provider). Safe for concurrent use.
type Cache struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]cacheEntry

	// Counters for /debug cache.
	hits   atomic.Int64
	misses atomic.Int64
	stores atomic.Int64
}

type cacheEntry struct {
	result   Result
	storedAt time.Time
	hitCount int
}

// NewCache returns a Cache with the given TTL. Use a non-positive TTL to
// disable caching.
func NewCache(ttl time.Duration) *Cache {
	if ttl <= 0 {
		return nil
	}
	return &Cache{
		ttl:     ttl,
		entries: make(map[string]cacheEntry),
	}
}

func (c *Cache) key(description string, s style.Style, provider string) string {
	h := sha256.New()
	h.Write([]byte(string(s)))
	h.Write([]byte{0})
	h.Write([]byte(provider))
	h.Write([]byte{0})
	h.Write([]byte(description))
	return hex.EncodeToString(h.Sum(nil))[:32]
}

// Get returns the cached result if present and not expired. The second
// return value is true on a hit.
func (c *Cache) Get(description string, s style.Style, provider string) (Result, bool) {
	if c == nil {
		return Result{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	k := c.key(description, s, provider)
	e, ok := c.entries[k]
	if !ok {
		c.misses.Add(1)
		return Result{}, false
	}
	if time.Since(e.storedAt) > c.ttl {
		delete(c.entries, k)
		c.misses.Add(1)
		return Result{}, false
	}
	e.hitCount++
	c.entries[k] = e
	c.hits.Add(1)
	return e.result, true
}

// Put stores the result under (description, style, provider).
func (c *Cache) Put(description string, s style.Style, provider string, r Result) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.entries[c.key(description, s, provider)] = cacheEntry{
		result:   r,
		storedAt: time.Now(),
	}
	c.mu.Unlock()
	c.stores.Add(1)
}

// Len returns the number of live entries (for /debug or tests).
func (c *Cache) Len() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}

// Stats returns a snapshot of cache counters.
type CacheStats struct {
	Entries  int
	Hits     int64
	Misses   int64
	Stores   int64
	HitRatio float64
}

func (c *Cache) Stats() CacheStats {
	if c == nil {
		return CacheStats{}
	}
	h := c.hits.Load()
	m := c.misses.Load()
	total := h + m
	var ratio float64
	if total > 0 {
		ratio = float64(h) / float64(total)
	}
	return CacheStats{
		Entries:  c.Len(),
		Hits:     h,
		Misses:   m,
		Stores:   c.stores.Load(),
		HitRatio: ratio,
	}
}

// Args is the JSON the parent agent sends into the task tool.
type Args struct {
	Description string `json:"description"`
	Style       string `json:"style,omitempty"`
	Provider    string `json:"provider,omitempty"`
}

// ResultPayload is the JSON we send back to the parent agent. The parent
// sees this in the `tool` role message.
type ResultPayload struct {
	Content   string `json:"content"`
	TokensIn  int    `json:"tokens_in"`
	TokensOut int    `json:"tokens_out"`
	Elapsed   string `json:"elapsed"`
	Rounds    int    `json:"rounds"`
}

// Tool returns the (Tool, ToolHandler) pair that registers the task tool.
// The CLI calls this once at startup and registers the result.
func (d *Default) Tool() (tool.Tool, tool.ToolHandler) {
	t := tool.Tool{
		Name: "task",
		Description: "Spawn an isolated sub-agent to handle a focused, well-defined sub-task. " +
			"Use this when the work can be split into independent parts (parallelism), or when a " +
			"sub-task benefits from a fresh context (no shared history). " +
			"The sub-agent gets its own system prompt, its own tool set (file/shell ops, but NOT task), " +
			"and runs synchronously. Returns the sub-agent's final answer. " +
			"Prefer calling this multiple times in parallel (multiple task calls in one assistant turn) " +
			"over chaining them when the sub-tasks are independent.",
		Parameters: tool.ObjectSchema(map[string]any{
			"description": tool.StringProp("Clear, self-contained description of the sub-task. " +
				"The sub-agent has no knowledge of the parent conversation, so include all necessary context."),
			"style": tool.StringEnumProp("Optional personality for the sub-agent. "+
				"Defaults to the parent agent's style.", "cute", "guofeng", "tech"),
			"provider": tool.StringProp("Optional LLM provider name from the parent config. "+
				"Defaults to the parent agent's provider. Use a cheaper/faster model for simple sub-tasks."),
		}, []string{"description"}),
	}

	h := func(ctx context.Context, args json.RawMessage) (*tool.CallResult, error) {
		var a Args
		if err := json.Unmarshal(args, &a); err != nil {
			return &tool.CallResult{Content: "invalid arguments: " + err.Error(), IsError: true}, nil
		}
		if strings.TrimSpace(a.Description) == "" {
			return &tool.CallResult{Content: "description is required", IsError: true}, nil
		}

		// If the agent's tool dispatcher provided a per-call event
		// channel, wire it up so sub-agent progress is forwarded to the
		// parent's UI in real time. Use a fresh field here so we
		// don't mutate the shared `d` for concurrent calls.
		runner := *d // shallow copy is fine: only OnEvent differs
		runner.OnEvent = func(c agent.ChatStreamChunk) {
			if ch := agent.GetToolEventChan(ctx); ch != nil {
				c.SubAgent = true
				select {
				case ch <- c:
				default:
					// drop on backpressure
				}
			}
		}

		res, err := runner.Run(ctx, Request{
			Description: a.Description,
			Style:       style.Style(strings.ToLower(strings.TrimSpace(a.Style))),
			Provider:    strings.TrimSpace(a.Provider),
		})
		if err != nil {
			return &tool.CallResult{
				Content: fmt.Sprintf("sub-agent failed: %v", err),
				IsError: true,
			}, nil
		}

		payload, _ := json.Marshal(ResultPayload{
			Content:   res.Content,
			TokensIn:  res.TokensIn,
			TokensOut: res.TokensOut,
			Elapsed:   res.Elapsed.Round(10 * time.Millisecond).String(),
			Rounds:    res.Rounds,
		})

		content := string(payload)
		if res.Content == "" {
			content = "(sub-agent returned no content)\n" + content
		}
		return &tool.CallResult{Content: content}, nil
	}

	return t, h
}

// Run implements Runner.
func (d *Default) Run(ctx context.Context, req Request) (Result, error) {
	// Resolve defaults from the parent.
	s := req.Style
	if s == "" {
		s = d.ParentStyle
	}
	prov := req.Provider
	if prov == "" {
		prov = d.ParentProvider
	}

	// Cache hit? Return immediately.
	if hit, ok := d.Cache.Get(req.Description, s, prov); ok {
		return hit, nil
	}

	// Per-sub-agent timeout. Default 5 minutes, or use the config if set.
	timeout := 5 * time.Minute
	if d.Cfg != nil {
		timeout = d.Cfg.SubAgent.TimeoutDuration()
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Build a sub-agent's tool registry: copy parent's tools but exclude
	// `task` and apply the user's allowed/denied lists.
	allowed := func(name string) bool { return true }
	if d.Cfg != nil {
		allowed = d.Cfg.SubAgent.ToolAllowed
	}
	subTools := tool.NewRegistry()
	for _, name := range d.ParentTools.Names() {
		// Sub-agents can't spawn sub-agents or call recall themselves;
		// both are coordination tools that should stay at the top level.
		if name == "task" || name == "recall" {
			continue
		}
		if !allowed(name) {
			continue
		}
		if tt, hh, ok := d.ParentTools.Lookup(name); ok {
			subTools.Register(tt, hh)
		}
	}

	// Fresh in-memory store; no shared history with the parent.
	store, _ := memory.Open(20)

	subAgent := agent.New(d.Cfg, d.LLM, d.StyleMgr, store, subTools)

	chatReq := agent.ChatRequest{
		Style:    s,
		Provider: prov,
		Messages: []llm.Message{
			{Role: openai.ChatMessageRoleUser, Content: req.Description},
		},
	}

	start := time.Now()
	stream := subAgent.ChatWithTools(runCtx, chatReq)

	var (
		content             string
		tokensIn, tokensOut int
		rounds              int
	)
	for chunk := range stream {
		if chunk.Error != "" {
			stripped := chunk
			stripped.Content = ""
			tryForward(stripped, d.OnEvent)
			return Result{}, fmt.Errorf("llm error: %s", chunk.Error)
		}
		if chunk.Content != "" {
			content += chunk.Content
		}
		if chunk.TokensIn > tokensIn {
			tokensIn = chunk.TokensIn
		}
		if chunk.TokensOut > tokensOut {
			tokensOut = chunk.TokensOut
		}
		if chunk.Round > rounds {
			rounds = chunk.Round
		}

		// Forward non-content events to the parent (Phase + Done) so it
		// can render nested progress. We strip Content because the
		// parent's UI shows the sub-agent's full result separately.
		if chunk.Phase != "" || chunk.Done {
			stripped := chunk
			stripped.Content = ""
			tryForward(stripped, d.OnEvent)
		}

		if chunk.Done {
			break
		}
	}

	res := Result{
		Content:   strings.TrimSpace(content),
		TokensIn:  tokensIn,
		TokensOut: tokensOut,
		Elapsed:   time.Since(start),
		Rounds:    rounds,
	}

	// Store in cache (best-effort, never blocks the return).
	d.Cache.Put(req.Description, s, prov, res)
	return res, nil
}
