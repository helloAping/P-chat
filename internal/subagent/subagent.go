// Package subagent implements spawning a fresh agent.Agent for a single
// focused sub-task. It is decoupled from the tool package (which only
// defines the tool interface) to avoid an import cycle:
//
//	tool -> agent -> tool   (not allowed)
//	tool -> subagent -> agent    (allowed)
package subagent

// Package subagent 实现 P-Chat 的子代理系统（task 工具）。
//
// 核心入口：Tool() 函数（task 工具的 handler），Default.Run() 是子代理的执行器。
//
// 数据流：
//
//	父 LLM 调用 task 工具 → Tool() → Default.Run()
//	  → 创建独立 agent 实例 → ChatWithTools()
//	  → 每个流事件通过 tryForward → OnEvent 回调 → per-tool eventCh
//	  → 父级 forwarder goroutine → 主通道 → SSE → 前端 SubAgentCard
//
// 关键设计决策：
//   - 子代理的 Done=true chunk 不转发（subagent.go:755），防止触发父 SSE 过早关闭
//   - 关闭事件（sub_agent_ok/err）是子代理完成的外部唯一信号
//   - 三层工具隔离：硬排除(task/recall) → 全局配置 → per-agent 白名单
//
// 修改指南 → docs/modules/subagent.md

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"regexp"
	"runtime/debug"
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
)

// Request is what the main agent passes to the runner. The runner
// resolves SubagentType, Model, and PromptOverride against the
// registry / parent context before invoking the child.
type Request struct {
	Description    string
	SubagentType  string
	Model         string
	PromptOverride string
	Style          style.Style
	Provider       string
	TaskID         string
}

// Result is what the runner returns. Content is the final assistant text
// (may be empty on error).
type Result struct {
	Content   string
	TokensIn  int
	TokensOut int
	Elapsed   time.Duration
	Rounds    int
	// SubagentType echoes the resolved agent name (from the
	// registry, or the request's explicit value, or
	// "general-purpose" as the default).
	SubagentType string
	// Model echoes the model the child actually used.
	Model string
	// Color echoes the agent's accent color (empty for
	// general-purpose or unknown agents).
	Color string
	// TaskID echoes the request's task_id (empty for ad-hoc
	// runs). Useful for the parent's "resume by task_id"
	// code path.
	TaskID string
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
	// ParentProviderModel is the parent's specific
	// "providerID/modelID" (when known). Used as the
	// fallback when a sub-agent does not specify its own
	// model. May be empty.
	ParentProviderModel string
	// Registry is the sub-agent catalog used to resolve
	// subagent_type → AgentInfo. When nil, only built-in
	// defaults are available (general-purpose).
	Registry *Registry

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

// defaultCacheMaxEntries caps the cache size. A long-running
// process that issues many distinct sub-agent tasks would
// otherwise grow this map without bound. When the cap is
// reached, the oldest entry (by storedAt) is evicted.
const defaultCacheMaxEntries = 256

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

// evictOldest drops the entry with the oldest storedAt under
// the lock. Caller must hold c.mu.
func (c *Cache) evictOldest() {
	if len(c.entries) == 0 {
		return
	}
	var oldestKey string
	var oldestAt time.Time
	first := true
	for k, e := range c.entries {
		if first || e.storedAt.Before(oldestAt) {
			oldestKey = k
			oldestAt = e.storedAt
			first = false
		}
	}
	if oldestKey != "" {
		delete(c.entries, oldestKey)
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
	if len(c.entries) > defaultCacheMaxEntries {
		c.evictOldest()
	}
	c.mu.Unlock()
	c.stores.Add(1)
}

// GetByKey looks up a result by an arbitrary string key (used
// for the task_id resume path). Returns the cached Result and
// true on a hit. Same TTL / eviction semantics as Get.
func (c *Cache) GetByKey(key string) (Result, bool) {
	if c == nil {
		return Result{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok {
		c.misses.Add(1)
		return Result{}, false
	}
	if time.Since(e.storedAt) > c.ttl {
		delete(c.entries, key)
		c.misses.Add(1)
		return Result{}, false
	}
	e.hitCount++
	c.entries[key] = e
	c.hits.Add(1)
	return e.result, true
}

// PutByKey stores a result under an arbitrary string key.
// Mirrors Put for the (description, style, provider) tuple.
func (c *Cache) PutByKey(key string, r Result) {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.entries[key] = cacheEntry{
		result:   r,
		storedAt: time.Now(),
	}
	if len(c.entries) > defaultCacheMaxEntries {
		c.evictOldest()
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
//
// The fields are organized in three groups:
//
//	Identity (always present):
//	  - Description: required; what the sub-agent should do.
//	  - SubagentType: optional; name of a registered agent
//	    (built-in like "explore", or user-defined in
//	    .p-chat/agent/*.md). Defaults to "general-purpose" when
//	    empty.
//
//	Per-call overrides:
//	  - Model: "providerID/modelID" override. Defaults to the
//	    parent's model.
//	  - Prompt: system prompt override. Replaces the parent's
//	    prompt for this run. Useful for very specialized tasks.
//	  - Style: personality style (e.g. "cute", "guofeng", "tech").
//	    Defaults to the parent's style.
//	  - Provider: which LLM provider entry from the parent
//	    config to use. Defaults to the parent's provider.
//
//	Resume / dedupe:
//	  - TaskID: optional stable identifier. Two calls with the
//	    same (TaskID, SubagentType, Model) return the cached
//	    result of the first run without re-running.
//
// Mirrors opencode's TaskTool parameter schema
// (packages/opencode/src/tool/task.ts:43-62) and Claude Code's
// AgentTool input schema
// (src/tools/AgentTool/AgentTool.tsx:82-102).
type Args struct {
	Description  string `json:"description"`
	SubagentType string `json:"subagent_type,omitempty"`
	Model        string `json:"model,omitempty"`
	Prompt       string `json:"prompt,omitempty"`
	Style        string `json:"style,omitempty"`
	Provider     string `json:"provider,omitempty"`
	TaskID       string `json:"task_id,omitempty"`
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
// The server calls this once at startup and registers the result.
//
// The tool's description is built dynamically from the registry so
// the parent LLM always sees an up-to-date list of available
// agents. If the registry is empty, the description falls back to
// the static text that names general-purpose as the default.
func (d *Default) Tool() (tool.Tool, tool.ToolHandler) {
	baseDesc := "Spawn an isolated sub-agent to handle a focused, well-defined sub-task. " +
		"Use this when the work can be split into independent parts (parallelism), or when a " +
		"sub-task benefits from a fresh context (no shared history). " +
		"The sub-agent gets its own system prompt, its own tool set (file/shell ops, but NOT task), " +
		"and runs synchronously. Returns the sub-agent's final answer. " +
		"Prefer calling this multiple times in parallel (multiple task calls in one assistant turn) " +
		"over chaining them when the sub-tasks are independent."
	if d.Registry != nil {
		if list := d.Registry.Describe(); list != "" {
			baseDesc += "\n\n" + list + "\n\n" +
				"Prefer specialized agents (explore, plan) over general-purpose when they fit."
		}
	}

	t := tool.Tool{
		Name:        "task",
		Description: baseDesc,
		Parameters: tool.ObjectSchema(map[string]any{
			"description": tool.StringProp("Clear, self-contained description of the sub-task. " +
				"The sub-agent has no knowledge of the parent conversation, so include all necessary context."),
			"subagent_type": tool.StringProp("Optional. Name of a registered sub-agent (e.g. 'explore', 'plan', 'general-purpose', or a custom agent from .p-chat/agent/*.md). " +
				"Defaults to 'general-purpose' when omitted."),
			"model": tool.StringProp("Optional 'providerID/modelID' override. " +
				"Defaults to the parent's model. Use a cheaper/faster model for simple sub-tasks."),
			"prompt": tool.StringProp("Optional system prompt override. " +
				"Replaces the parent's prompt for this run. Useful for very specialized tasks."),
			"style": tool.StringEnumProp("Optional personality for the sub-agent. "+
				"Defaults to the parent agent's style.", "cute", "guofeng", "tech"),
			"provider": tool.StringProp("Optional LLM provider name from the parent config. "+
				"Defaults to the parent agent's provider."),
			"task_id": tool.StringProp("Optional stable identifier. Two calls with the same (task_id, subagent_type, model) " +
				"return the cached result of the first run without re-executing the sub-agent. " +
				"Useful for retries and resuming."),
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
		runner := *d // shallow copy: OnEvent + ParentProvider/Model may differ
		runner.OnEvent = func(c agent.ChatStreamChunk) {
			if ch := agent.GetToolEventChan(ctx); ch != nil {
				c.SubAgent = true
				// Lifecycle close events (sub_agent_ok / sub_agent_err)
				// are critical: dropping them leaves the GUI's nested
				// card stuck in the "running" spinner forever, AND the
				// parts accumulator never flips its Status away from
				// "start" — so the persisted assistant message records
				// status="start", which on session reload comes back as
				// "运行中…" for a stale run.
				//
				// For close events we block (with a short timeout and a
				// parent-cancel guard) until the per-tool eventCh drains.
				// Normal content/thinking/tool deltas stay non-blocking
				// — losing one delta is a small visual hiccup, while
				// losing the close event is a state-machine corruption.
				isClose := c.SubAgentStatus != "" && c.SubAgentStatus != "start"
				if isClose {
					select {
					case ch <- c:
					case <-time.After(2 * time.Second):
						log.Printf("[subagent] close event dropped after 2s (eventCh full): task=%q status=%q",
							c.SubAgentTask, c.SubAgentStatus)
					case <-ctx.Done():
						// parent tool cancelled; give up quietly.
					}
				} else {
					select {
					case ch <- c:
					default:
						// drop on backpressure — these are
						// best-effort stream deltas, not lifecycle.
					}
				}
			}
		}

		// Inherit the parent turn's current (provider, model) pair.
		// Without this the sub-agent would fall back to whatever
		// the server's startup default was, which silently breaks
		// model selection when the user has switched providers/
		// models mid-session via the GUI picker — the symptom is
		// an "openai proxy error: model_not_found" because the
		// server's default provider ("cs" in the user's setup)
		// doesn't expose the model the user actually selected.
		if pp, pm := agent.GetParentModel(ctx); pp != "" || pm != "" {
			runner.ParentProvider = pp
			runner.ParentProviderModel = pm
		}

		res, err := runner.Run(ctx, Request{
			Description:     a.Description,
			SubagentType:    strings.TrimSpace(a.SubagentType),
			Model:           strings.TrimSpace(a.Model),
			PromptOverride:  a.Prompt,
			Style:           style.Style(strings.ToLower(strings.TrimSpace(a.Style))),
			Provider:        strings.TrimSpace(a.Provider),
			TaskID:          strings.TrimSpace(a.TaskID),
		})
		if err != nil {
			return &tool.CallResult{
				Content: fmt.Sprintf("sub-agent failed: %v", err),
				IsError: true,
			}, nil
		}

		// Tool result format. The parent LLM is the consumer and
		// treats this as plain text in the tool result message.
		// Returning a raw JSON blob here confuses the parent (it
		// can't easily extract the content) and was a common
		// cause of "subagent finished but parent produced no
		// summary" — the parent would echo the JSON keys back
		// instead of synthesising a real response.
		//
		// Format:
		//   <text content from the sub-agent, verbatim>
		//
		//   ---
		//   [subagent stats: model=<m>, elapsed=<t>, rounds=<n>, tokens=<in>/<out>]
		//
		// The text content is the primary payload (what the
		// parent LLM should read and act on). The stats footer
		// is a small, recognisable block the LLM can ignore or
		// surface to the user. The metadata is also kept in the
		// Result struct for callers that want the structured
		// form (e.g. the Wails GUI's session inspector).
		var content string
		if res.Content != "" {
			content = res.Content
		} else {
			content = "(sub-agent returned no content)"
		}
		// Append a brief metadata footer so the parent LLM
		// (and the user, when /tools shows the raw tool
		// result) can see what happened. Kept terse — the
		// stats are not the answer.
		stats := fmt.Sprintf("\n\n---\n[subagent stats: model=%s, elapsed=%s, rounds=%d, tokens=%d/%d]",
			res.Model,
			res.Elapsed.Round(10*time.Millisecond),
			res.Rounds,
			res.TokensIn,
			res.TokensOut,
		)
		content += stats
		_ = ResultPayload{ // keep the helper exported for callers that want the structured form
			Content:   res.Content,
			TokensIn:  res.TokensIn,
			TokensOut: res.TokensOut,
			Elapsed:   res.Elapsed.Round(10 * time.Millisecond).String(),
			Rounds:    res.Rounds,
		}
		return &tool.CallResult{Content: content}, nil
	}

	return t, h
}

// Run implements Runner.
func (d *Default) Run(ctx context.Context, req Request) (_ Result, retErr error) {
	defer func() {
		if r := recover(); r != nil {
			retErr = fmt.Errorf("subagent panic: %v", r)
			log.Printf("[subagent] panic recovered: %v\n%s", r, string(debug.Stack()))
		}
	}()

	// Resolve defaults from the parent.
	s := req.Style
	if s == "" {
		s = d.ParentStyle
	}
	prov := req.Provider
	if prov == "" {
		prov = d.ParentProvider
	}

	// Resolve the sub-agent type, prompt override, model override,
	// and per-agent color from the registry. Unknown / empty
	// subagent_type falls back to "general-purpose" so the LLM
	// can always spawn a sub-agent without first having to know
	// the registered name.
	subType := req.SubagentType
	if subType == "" {
		subType = "general-purpose"
	}
	var (
		agentInfo  AgentInfo
		agentKnown bool
		promptOv   = req.PromptOverride
		modelOv    = req.Model
		color      string
	)
	if d.Registry != nil {
		if a, ok := d.Registry.Get(subType); ok {
			agentInfo = a
			agentKnown = true
			color = a.Color
			// Per-agent prompt: when the agent's prompt is
			// non-empty AND the request didn't supply its
			// own override, use the agent's prompt.
			if promptOv == "" && a.Prompt != "" {
				promptOv = a.Prompt
			}
			// Per-agent model: same priority rule. The
			// request-level override beats the agent's
			// default.
			if modelOv == "" && a.Model != "" {
				modelOv = a.Model
			}
		}
	}

	// Cache hit? Key on (task_id, subagent_type, model) when
	// task_id is provided, else fall back to the legacy
	// (description, style, provider) key. task_id takes
	// priority because it is the explicit resume signal.
	if d.Cache != nil && req.TaskID != "" {
		cacheKey := req.TaskID + "|" + subType + "|" + modelOv + "|" + string(s) + "|" + prov
		if hit, ok := d.Cache.GetByKey(cacheKey); ok {
			return hit, nil
		}
	}
	if hit, ok := d.Cache.Get(req.Description, s, prov); ok {
		return hit, nil
	}

	// Emit a single synthetic "start" event for the parent's UI so it
	// can open a nested sub-agent card immediately. The card header
	// shows the task description; the card body fills in as content /
	// thinking / tool events flow through below. SubAgent=true tags
	// the event so the parent knows to route it to the matching
	// nested card (keyed by task description, since the wire doesn't
	// carry a sub-agent id).
	if d.OnEvent != nil {
		d.OnEvent(agent.ChatStreamChunk{
			Phase:               "sub_agent_start",
			SubAgent:            true,
			SubAgentTask:        req.Description,
			SubAgentStatus:      "start",
			SubAgentType:        subType,
			SubAgentColor:       color,
			SubAgentDescription: agentInfo.Description,
			SubAgentTaskID:      req.TaskID,
		})
	}

	// Per-sub-agent timeout. Default 5 minutes, or use the config if set.
	timeout := 5 * time.Minute
	if d.Cfg != nil {
		timeout = d.Cfg.SubAgent.TimeoutDuration()
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Build a sub-agent's tool registry. Three layers of filter
	// (in priority order, each can further narrow):
	//   1. Hard exclusions: task, recall (recursion / coordination).
	//   2. Global config allow/deny list (`subagent.allowed_tools` /
	//      `subagent.denied_tools` in ~/.p-chat/config.json).
	//   3. Per-agent whitelist (agentInfo.Tools). When non-empty,
	//      only those tools are exposed (still minus the hard
	//      exclusions).
	subTools := tool.NewRegistry()
	parentAllowed := func(name string) bool { return true }
	if d.Cfg != nil {
		parentAllowed = d.Cfg.SubAgent.ToolAllowed
	}
	for _, name := range d.ParentTools.Names() {
		// Sub-agents can't spawn sub-agents or call recall themselves;
		// both are coordination tools that should stay at the top level.
		if name == "task" || name == "recall" {
			continue
		}
		if !parentAllowed(name) {
			continue
		}
		// Per-agent whitelist (overrides global config).
		if agentKnown && len(agentInfo.Tools) > 0 {
			ok := false
			for _, t := range agentInfo.Tools {
				if t == name {
					ok = true
					break
				}
			}
			if !ok {
				continue
			}
		}
		if tt, hh, ok := d.ParentTools.Lookup(name); ok {
			subTools.Register(tt, hh)
		}
	}

	// Fresh in-memory store; no shared history with the parent.
	store, _ := memory.Open(100)

	subAgent := agent.New(d.Cfg, d.LLM, d.StyleMgr, store, subTools)

	// Wire auto-compaction for sub-agents so they can handle
	// large contexts without hitting round / token limits.
	if d.LLM != nil && prov != "" {
		sm := memory.NewSummarizer(store, d.LLM, prov, 50)
		subAgent.SetSummarizer(sm)
	}

	// Resolve the model string. Order of priority:
	//   1. Per-request model override (Args.Model)
	//   2. Per-agent model (Registry entry's Model)
	//   3. Parent's specific provider/model
	//   4. Parent's provider name (legacy fallback)
	chatModel := modelOv
	if chatModel == "" {
		chatModel = d.ParentProviderModel
	}
	if chatModel == "" {
		chatModel = prov
	}

	chatReq := agent.ChatRequest{
		Style:         s,
		Provider:      prov,
		Model:         chatModel,
		PromptOv:      promptOv,
		SubagentType:  subType,
		SubagentColor: color,
		SubagentTaskID: req.TaskID,
		SessionID:     "subagent-" + subType + "-" + req.TaskID,
		Messages: []llm.ChatMessage{
			{Role: llm.RoleUser, Type: llm.TypeText, Content: req.Description},
		},
	}

	start := time.Now()
	stream := subAgent.ChatWithTools(runCtx, chatReq)

	var (
		content             string
		contentProduced     bool // any non-empty Content chunk seen?
		tokensIn, tokensOut int
		rounds              int
		failed              bool
		failureReason       string
	)
	for chunk := range stream {
		// Tag every chunk as sub-agent so the parent can route it
		// into the matching nested card. Stamp the task on each
		// event too — the parent UI keys cards by task description
		// (since the wire has no sub-agent id), so the tag must
		// ride along. The agent's name and color ride along too so
		// the SubAgentCard can render "explore" (green) vs
		// "plan" (orange) etc.
		chunk.SubAgent = true
		chunk.SubAgentTask = req.Description
		chunk.SubAgentType = subType
		chunk.SubAgentColor = color
		chunk.SubAgentTaskID = req.TaskID

		if chunk.Error != "" {
			// Two flavours of error:
			//
			//   * HARD failure (no content produced): the
			//     system prompt build failed, the LLM
			//     connection died before any token arrived,
			//     the model returned an error, etc. We
			//     can't give the parent anything useful, so
			//     mark the subagent as failed.
			//
			//   * SOFT failure (content already produced):
			//     the LLM started streaming, maybe even
			//     finished a round, and then something went
			//     wrong at the tail (e.g. a final
			//     round's tool call had a phantom error
			//     classification, or the LLM was cut off
			//     after the user-cancel). In these cases
			//     we DO have useful content for the parent
			//     — the error is a tail event, not a
			//     gate. Mark as ok so the card shows
			//     "已完成" and the parent's tool result
			//     carries the partial text.
			//
			// The previous behaviour (mark as failed on
			// any Error chunk) left cards stuck on
			// "失败" even when the LLM had already
			// produced a complete answer.
			failureReason = chunk.Error
			if !contentProduced {
				failed = true
			}
			tryForward(chunk, d.OnEvent)
			break
		}
		if chunk.Content != "" {
			content += chunk.Content
			contentProduced = true
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

		// ★ 子代理的 Done=true chunk 是本地循环终止信号，不是"整个对话结束"。
		// 若转发到父主通道 → handler.go chunkToEvent 将其映射为 type:"done"
		// → SSE 连接在 chunk.Done 时 return false 并关闭
		// → 紧随其后的 sub_agent_ok 关闭事件永远无法送达前端
		// → 前端安全网（chat.ts:1033）将子代理强制标记为 "err"
		// → 父 goroutine 在关闭的 channel 上写入并永久阻塞
		//
		// 对外部消费者而言，子代理完成的正式信号是下方发送的合成事件
		// sub_agent_ok / sub_agent_err，而非内部的 Done 标记。 // The Done=true chunk from the sub-agent's own
		// ChatWithTools loop is a local signal — it means
		// "the sub-agent's LLM stream ended".  It must NOT
		// be forwarded to the parent because the parent's
		// SSE handler (handler.go:1470) closes the
		// connection on ANY Done=true chunk, which would
		// tear down the SSE stream before the
		// sub_agent_ok/err close event reaches the client.
		// The result: the nested card's safety net forces
		// status=err, the parent goroutine deadlocks on
		// channel writes, and the conversation stops.
		//
		// The canonical "the sub-agent is done" signal for
		// the parent is the synthetic close event emitted
		// below (sub_agent_ok / sub_agent_err), not the
		// internal Done bookmark.
		if chunk.Done {
			break
		}

		// 转发所有非错误、非 Done 的事件到父级，让嵌套卡片的 body
		// 能实时流式显示文本、思考和工具调用。
		// Forward EVERY non-error, non-Done event to the
		// parent so the nested card's body can stream
		// text, thinking, and tool calls in real time.
		tryForward(chunk, d.OnEvent)
	}

	// Emit the closing status event. The parent UI uses this
	// to flip the card from "running" to "ok" / "err" and
	// to collapse / stop the spinner.
	status := "ok"
	if failed {
		status = "err"
		// Hard failure: surface the underlying error so the
		// GUI can show "失败: <reason>" in the card header.
	} else if contentProduced && failureReason != "" {
		// Soft failure: the LLM produced content but the
		// stream ended with an error. We changed `failed`
		// to false above (per the soft-fail rule) but the
		// UI should still know what happened at the tail
		// so it can show a hint like "completed with a
		// tail-end hiccup" instead of a clean "已完成".
		failureReason = "tail-end hiccup (output still returned): " + failureReason
	}
	if d.OnEvent != nil {
		d.OnEvent(agent.ChatStreamChunk{
			Phase:               "sub_agent_" + status,
			SubAgent:            true,
			SubAgentTask:        req.Description,
			SubAgentStatus:      status,
			SubAgentType:        subType,
			SubAgentColor:       color,
			SubAgentModel:       chatModel,
			SubAgentDescription: agentInfo.Description,
			SubAgentFailureReason: failureReason,
			SubAgentTaskID:      req.TaskID,
			Duration:            time.Since(start).Round(10 * time.Millisecond).String(),
		})
	}

	if failed {
		// We already forwarded the error chunk above; just
		// return without a result. The tool layer will turn
		// the lack of result into a "sub-agent failed"
		// CallResult.
		return Result{}, fmt.Errorf("sub-agent stream errored")
	}

	res := Result{
		// Triple-scrub the result content. The agent-level
		// redactor runs at end-of-round, so for the happy
		// path this is a no-op. For the soft-fail path
		// (stream errored at the tail after producing
		// content) the agent-level redactor may have been
		// skipped — this last-pass scrub makes sure the
		// parent LLM never sees the raw phantom through
		// the tool result channel.
		Content:       redactPhantomErrorsServer(strings.TrimSpace(content)),
		TokensIn:      tokensIn,
		TokensOut:     tokensOut,
		Elapsed:       time.Since(start),
		Rounds:        rounds,
		SubagentType:  subType,
		Model:         chatModel,
		Color:         color,
		TaskID:        req.TaskID,
	}

	// Store in cache. task_id path uses a stable key so two
	// calls with the same task_id return the same result even
	// if the description wording drifts. The legacy key is
	// kept for ad-hoc calls.
	if d.Cache != nil && req.TaskID != "" {
		cacheKey := req.TaskID + "|" + subType + "|" + modelOv + "|" + string(s) + "|" + prov
		d.Cache.PutByKey(cacheKey, res)
	}
	d.Cache.Put(req.Description, s, prov, res)
	_ = agentKnown // silence linter when agent lookup happens but is unused beyond populating res
	return res, nil
}

// phantomScrubRe mirrors the regex in internal/agent/agent.go
// (phantomVisionErrorRe). We re-declare it here so the
// subagent runner can scrub its own Result.Content
// without taking a dependency on the agent package's
// unexported helper. Same shape, same replacement.
var phantomScrubRe = regexp.MustCompile(
	`(?is)Cannot read[\s\S]{0,400}?Inform the user\.?`,
)
const phantomScrubReplacement = "（当前模型不支持读取图片。请在「设置 → 提供商/模型」中切换到支持视觉的模型（如 claude-3、gpt-4o、gemini-1.5、qwen-vl、doubao-1.5-vision-pro 等）后重新发送。）"

// redactPhantomErrorsServer is the subagent runner's last
// defence against the phantom. The agent-level redactor
// runs at end-of-round, so on the happy path this is a
// no-op; for the soft-fail path (stream errored at the
// tail after producing content) the agent-level redactor
// may have been skipped. This is cheap (single regex
// pass) and guarantees the parent LLM never sees the
// raw phantom through the tool result channel.
func redactPhantomErrorsServer(s string) string {
	if s == "" {
		return s
	}
	if !strings.Contains(strings.ToLower(s), "cannot read") ||
		!strings.Contains(strings.ToLower(s), "inform the user") {
		return s
	}
	return phantomScrubRe.ReplaceAllString(s, phantomScrubReplacement)
}
