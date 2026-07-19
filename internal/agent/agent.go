// Package agent 实现 P-Chat 的核心 ReAct 工具调用循环。
//
// ChatWithTools 是入口函数，负责：
//  1. 构建系统提示词（风格 + AGENTS.md + 规则 + 技能）
//  2. 调用 LLM 获取流式响应
//  3. 解析工具调用（原生 tool_calls 或 markdown ```tool_call 块回退）
//  4. 并行执行工具（每个工具在独立 goroutine 中运行，通过 per-tool eventCh 通信）
//  5. 将工具结果反馈给 LLM，继续循环直到 LLM 决定结束或达到轮次上限
//
// 数据流：LLM → ChatStreamChunk channel → 工具派发 → per-tool eventCh → forwarder → 主 channel → SSE → 前端
//
// 修改指南：
//   - ChatWithTools 在 agent.go 约 900 行
//   - 工具派发逻辑在 agent.go:1150-1471
//   - parts 累加器在 parts.go
//   - 相关模块文档：docs/modules/agent.md
package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/memory"
	"github.com/p-chat/pchat/internal/paths"
	"github.com/p-chat/pchat/internal/rules"
	"github.com/p-chat/pchat/internal/skill"
	"github.com/p-chat/pchat/internal/style"
	"github.com/p-chat/pchat/internal/tool"
	"github.com/p-chat/pchat/internal/trace"
)

type Agent struct {
	llm      *llm.Client
	styleMgr *style.Manager
	store    *memory.Store
	tools    *tool.Registry
	cfg      *config.Config
	skills   []skill.Skill
	rules    []rules.Rule
	sandbox  tool.SandboxChecker    // optional; nil disables sandbox enforcement
	options  llm.ChatOptions        // per-request sampling; populated from cfg
	attach   AttachmentResolver     // optional; turns Attachment IDs into file paths for upload expansion

	// bypassOnce, when true, makes the NEXT tool call skip the
	// sandbox check (set by /unsafe once). Reset after the call.
	// Swap(false) is atomic, so a panic between the swap and
	// the actual tool call consumes the bypass without using
	// it — the user will need to re-issue /unsafe. This is the
	// correct trade-off (better to err on the side of an extra
	// prompt than to leak the bypass across turns).
	bypassOnce atomic.Bool

	// subagentRegistry, if non-nil, is published to the tool
	// dispatcher's context so the `task` tool can resolve
	// `subagent_type` to a registered AgentInfo. The server
	// wires a registry built from the built-in agents + any
	// user-defined `.p-chat/agent/*.md` files at startup.
	subagentRegistry SubagentRegistry

	// Cached static system prompt (style + AGENTS + rules + skills + tool hint).
	// Keyed by (style, available-tools-hash). Invalidated by Reload() or when
	// the user changes style. This is the part that's identical across all
	// rounds of a single chat AND between chats within the same
	// session, so the LLM's prefix cache hits on it.
	staticPrompt   string
	staticPromptID string // signature used to detect when to rebuild

	// KB index cache — rebuild only on Reload() or after 60s TTL.
	kbIndexCache     string
	kbIndexCacheKey  string // base name used to build the cache
	kbIndexCacheTime int64  // unix timestamp of last build

	// summarizer, when non-nil, enables auto-compression.
	summarizer *memory.Summarizer

	// subAgentSem limits concurrent sub-agent launches. nil = unlimited.
	subAgentSem chan struct{}

	// lastProjectRoot is the projectRoot the current skills /
	// rules slice was loaded against. Compared by string in
	// ReloadWithRootIfChanged to skip the re-load when the
	// user sends a follow-up message in the same session
	// (root doesn't change between turns of one session).
	// 2026-07: prior to this field, switching projectRoot
	// mid-session did not reload project-level skills or
	// rules — they were loaded once at agent construction
	// (New) and only reloaded by Reload() (which the rules
	// watcher triggers on mtime change of the CWD-based
	// rules dir). The field lets buildStaticSystemPrompt
	// detect a project switch and invalidate the cache.
	lastProjectRoot string
}

// SetChatOptions overrides the per-request sampling parameters
// (temperature, top_p, max_tokens). Pass an empty struct to reset to
// the underlying API defaults.
func (a *Agent) SetChatOptions(opts llm.ChatOptions) {
	a.options = opts
}

// getStyleMemory 读取当前风格的 Memory 内容。空字符串表示未配置。
// Memory 存储在数据库 styles.memory 列，是用户自定义的背景知识，
// 动态注入到每轮对话末尾。
func (a *Agent) getStyleMemory(s style.Style) string {
	if a.styleMgr == nil {
		return ""
	}
	m, _ := a.styleMgr.GetMemory(s)
	return m
}

// SetSandbox installs a sandbox checker. The same checker is forwarded
// to every tool call's context, so the tool handlers can short-circuit
// dangerous operations. Pass nil to disable sandboxing.
func (a *Agent) SetSandbox(s tool.SandboxChecker) {
	a.sandbox = s
}

func (a *Agent) LLM() *llm.Client { return a.llm }

// SetLLM swaps the agent's LLM client. Used by the HTTP layer after
// a config change so new providers / API keys take effect immediately.
func (a *Agent) SetLLM(c *llm.Client) {
	a.llm = c
}

// BypassSandboxOnce makes the next tool call skip sandbox checks.
// /unsafe once uses this; the bypass is automatically cleared after
// the next tool call.
func (a *Agent) BypassSandboxOnce() {
	a.bypassOnce.Store(true)
}

// SetSubagentRegistry installs the registry that the `task` tool
// uses to resolve `subagent_type` arguments. The registry carries
// the built-in agents (general-purpose, explore, plan) plus any
// user-defined `.p-chat/agent/*.md` agents. The tool itself lives
// in internal/subagent; the agent package only needs a small
// read-only interface so the tool can stay decoupled.
func (a *Agent) SetSubagentRegistry(r SubagentRegistry) {
	a.subagentRegistry = r
}

func New(cfg *config.Config, llmClient *llm.Client, styleMgr *style.Manager, store *memory.Store, tools *tool.Registry) *Agent {
	skills, _ := skill.LoadAll()
	rulesList, _ := rules.LoadAll()

	return &Agent{
		llm:      llmClient,
		styleMgr: styleMgr,
		store:    store,
		tools:    tools,
		cfg:      cfg,
		skills:   skills,
		rules:    rulesList,
	}
}

// SetAttachmentResolver installs the resolver that turns
// ChatRequest.Attachments (file ids posted by the client) into
// on-disk paths. Pass nil to disable attachment expansion (the
// caller is responsible for setting a non-nil resolver when
// SendMessageRequest.Attachments may be non-empty).
func (a *Agent) SetAttachmentResolver(r AttachmentResolver) {
	a.attach = r
}

// SetSummarizer wires the summarizer for auto-compression support.
func (a *Agent) SetSummarizer(sm *memory.Summarizer) {
	a.summarizer = sm
}

// SetSubAgentConcurrency limits the number of concurrently executing
// sub-agent task calls. max <= 0 means unlimited (default).
func (a *Agent) SetSubAgentConcurrency(max int) {
	if max > 0 {
		a.subAgentSem = make(chan struct{}, max)
	} else {
		a.subAgentSem = nil
	}
}

// protocolFor returns the configured protocol ("openai" /
// "anthropic" / ...) for the given provider name. Falls back to
// "openai" when the provider is unknown — the existing LLM
// client already falls back to OpenAI shape in that case so
// attachment expansion does the same.
func (a *Agent) protocolFor(providerName string) string {
	if a.cfg == nil {
		return "openai"
	}
	for _, p := range a.cfg.LLM.Providers {
		if p.Name == providerName {
			return p.GetProtocol()
		}
	}
	return "openai"
}

// modelSupportsVision reports whether the active (provider, model)
// pair accepts image_url inputs.
//
// Policy: **permissive by default**, with one exception. If the
// user has explicitly marked the model as text-only via
// `capabilities: { supports_vision: false }` in the config, we
// return false so the agent drops the image and writes the
// "this model does not support image input" marker instead of
// round-tripping a request the API will reject.
//
// "No opinion" (capabilities: {} or capabilities absent) keeps
// the old permissive behaviour: send the image and let the API
// surface a "does not support image input" error if it really
// can't accept the image. That error is then caught by
// ClassifyAPIError → KindVisionUnsupported and shown to the user
// as a clear, actionable warning chip on their message.
func (a *Agent) modelSupportsVision(providerName, modelName string) bool {
	if a.cfg == nil {
		return visionCapableByHeuristic(providerName, modelName)
	}
	for _, p := range a.cfg.LLM.Providers {
		if p.Name != providerName {
			continue
		}
		for _, m := range p.Models {
			if m.Name == modelName {
				// Explicit opt-in: capabilities.supports_vision = true.
				// The user has confirmed this model handles images.
				if m.Capabilities.SupportsVision {
					return true
				}
				// "No opinion" (capabilities: {} or capabilities
				// absent) keeps the permissive behaviour: ask the
				// heuristic, then fall through to the LLM-API error
				// path if the model actually can't accept images.
				// That error is then caught by ClassifyAPIError →
				// KindVisionUnsupported and surfaced to the user as
				// a clear, actionable warning chip on their message.
				//
				// (Capabilities is a struct, not a pointer, so its
				// zero value reads as false here; that is the same
				// as "no opinion", NOT an explicit opt-out. The
				// previous code's `!SupportsVision ⇒ deny` therefore
				// silently denied vision for every model whose
				// capabilities block was empty — i.e. essentially
				// every user-added model — and the user saw a
				// text-placeholder instead of their image even when
				// the upstream model supports vision fine. See
				// handler_regenerate_test.go:TestModelSupportsVision_*
				// for the regression tests.)
				return visionCapableByHeuristic(providerName, modelName)
			}
		}
		// Provider found, model not in the configured list. Don't
		// trust the API to surface the "doesn't support image input"
		// error — the LLM, when it gets that error back, has been
		// observed to fabricate a clean "Cannot read \"image.png\"
		// (this model does not support image input). Inform the
		// user." message back to the user as if it were a real
		// tool error. Better to deny up front and tell the user
		// in clear text that their image couldn't be sent.
		return visionCapableByHeuristic(providerName, modelName)
	}
	// Provider not found in config: same deny-by-default.
	return visionCapableByHeuristic(providerName, modelName)
}

// visionCapableByHeuristic returns a best-guess vision
// capability for an unknown (provider, model) pair. The
// goal is to NOT trust the LLM API to surface the error
// gracefully — instead, look at the model name itself and
// short-circuit obvious non-vision models.
//
// opencode's model catalog (https://models.dev) is the
// authoritative source in production; we don't fetch from
// it here, but a static prefix table covers the most common
// offenders observed in the field.
func visionCapableByHeuristic(providerName, modelName string) bool {
	m := strings.ToLower(modelName)

	// Always-vision model families (as of 2026).
	visionPrefixes := []string{
		"gpt-4o", "gpt-4-vision", "gpt-5", "gpt-4.1",
		"claude-3", "claude-4", "claude-opus-4", "claude-sonnet-4",
		"gemini-1.5", "gemini-2", "gemini-exp",
		"qwen-vl", "qwen2-vl", "qwen2.5-vl", "qvq",
		"llava", "llama-3.2-vision", "llama-3.3",
		"minimax-m3", "minimax-vl",
		"pixtral", "paligemma",
		// DeepSeek V4 — vision-capable (multimodal). The V2/V3
		// chat models are text-only and live in the deny list
		// below; V4's "flash" and "lite" variants ship with
		// vision. Listed before the deny-list check so a config
		// without capabilities.supports_vision still gets
		// vision when the model name matches.
		"deepseek-v4-flash", "deepseek-v4-lite", "deepseek-v4",
	}
	for _, p := range visionPrefixes {
		if strings.HasPrefix(m, p) {
			return true
		}
	}

	// Known text-only model families. These were the biggest
	// source of the "Cannot read image.png" phantom errors in
	// the wild, because the LLM was talking to a non-vision
	// proxy that returned 400s and the model invented a clean
	// "model doesn't support image input" string.
	textOnlyPrefixes := []string{
		"deepseek-chat", "deepseek-reasoner", "deepseek-coder",
		"deepseek-v2", "deepseek-v3", // V2/V3 chat is text-only
		"gpt-3.5", "gpt-3.5-turbo",
		"text-embedding", "text-davinci",
		"o1-mini", "o1-preview",
	}
	for _, p := range textOnlyPrefixes {
		if strings.HasPrefix(m, p) {
			return false
		}
	}

	// Conservative default: deny. Better to tell the user
	// their image couldn't be sent than to let the LLM
	// invent a plausible-looking error message.
	return false
}

type ChatRequest struct {
	Style    style.Style       `json:"style"`
	Messages []llm.ChatMessage `json:"messages"`
	Provider string            `json:"provider,omitempty"`
	Model    string            `json:"model,omitempty"`
	// Attachments are file ids the user attached to this turn.
	// Expanded into the message list as separate ChatMessage
	// entries (text + image/file) before being sent to the LLM.
	// Nil/empty = no attachments.
	Attachments []Attachment `json:"attachments,omitempty"`
	// ClientMsgID, when non-zero, is the row id the frontend
	// minted at send time (Date.now() × 1000 + random, well
	// outside SQLite's AUTOINCREMENT range). The agent uses
	// it as the explicit row id when persisting this turn's
	// user message so rollback/regenerate have a valid id
	// to target from the moment the user clicks send — the
	// SSE `done` event is no longer the gating factor.
	// Zero means "let the store autoincrement as usual"
	// (the legacy path used by tests, the CLI, and any
	// non-SPA caller that doesn't pre-mint an id).
	ClientMsgID int64 `json:"client_msg_id,omitempty"`
	// PlanMode, when true, asks the LLM to produce a step-by-step
	// plan in plain text instead of executing tools.
	PlanMode bool `json:"plan_mode,omitempty"`
	// ReasoningEffort controls how much reasoning/thinking the LLM
	// does before responding. off|low|medium|high|max. Empty means
	// the model default.
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	// CompressedSummary is a pre-summarized version of older
	// conversation history. When non-empty, it is appended to the
	// system prompt so the LLM has context from before compression.
	CompressedSummary string `json:"compressed_summary,omitempty"`
	// SessionID is the current conversation identifier. Used by
	// tools that need per-session state (e.g. todo_write).
	SessionID string `json:"session_id,omitempty"`
	// ProjectRoot is the absolute path to the project directory
	// this session belongs to. When non-empty, project-level
	// config and AGENTS.md are loaded from this root instead of
	// the server's CWD.
	ProjectRoot string `json:"project_root,omitempty"`
	// SkillContext is the full SKILL.md content for a skill
	// activated via slash command. It is appended to the system
	// prompt so the LLM sees it without cluttering the chat.
	SkillContext string `json:"skill_context,omitempty"`
	// PermissionLevel overrides the sandbox confirm behaviour for
	// this session. Values: "ask", "auto", "full". Default "ask".
	PermissionLevel string `json:"permission_level,omitempty"`
	// RegenGroupID is the P1-4 regen-history tag. When
	// non-empty, the agent loop stamps the new assistant
	// message's regen_group_id column with this value so
	// it joins the user message's regen group. The
	// Regenerate handler sets this to
	// strconv.FormatInt(req.UserMessageID, 10); the
	// SendMessage handler leaves it empty (the resulting
	// assistant message has regen_group_id = NULL, the
	// legacy single-shot behaviour).
	RegenGroupID string `json:"regen_group_id,omitempty"`
	// MaxRounds caps the ReAct tool-use loop. 0 means default (50).
	// After MaxRounds the loop stops and the user can continue
	// with a follow-up message.
	MaxRounds int `json:"max_rounds,omitempty"`
	// KBBase selects the knowledge base for this session.
	// "" = off, "__all__" = all bases, or a specific base name.
	KBBase string `json:"kb_base,omitempty"`
	// AutoContinue controls the "todo-incomplete → re-prompt LLM"
	// guard (P0-3, see agent.go ChatWithTools). When true (the
	// default on the server), if the LLM emits a no-tool-call
	// response but the todo list still has pending or
	// in_progress items, the agent injects a user-style reminder
	// listing the unfinished todos and re-enters the loop, up
	// to MaxAutoContinue times. Set false (via /auto-continue
	// off) to disable per session.
	AutoContinue bool `json:"auto_continue,omitempty"`
	// PromptOv, when non-empty, REPLACES the agent's normal
	// system prompt (style + AGENTS + rules + skills) for this
	// turn. Used by the sub-agent runner to install a
	// specialized persona (e.g. the "explore" or "plan"
	// prompts) without the user having to define a style.
	// Empty = inherit the agent's normal prompt.
	PromptOv string `json:"prompt_override,omitempty"`
	// SubagentType is the agent name when this ChatRequest was
	// issued from the sub-agent runner. Empty for top-level
	// chats. The agent's tool hint and system prompt can
	// branch on this (e.g. hide the `task` tool in the
	// description so the sub-agent doesn't see what it cannot
	// use).
	SubagentType string `json:"subagent_type,omitempty"`
	// SubagentColor is the agent's accent color. Surfaced on
	// the SubAgentCard in the GUI and the agent-name badge in
	// the CLI.
	SubagentColor string `json:"subagent_color,omitempty"`
	// SubagentTaskID is the resume-by-id key. Empty for
	// ad-hoc runs. Populated from Args.TaskID.
	SubagentTaskID string `json:"subagent_task_id,omitempty"`
	// TraceID is the P3-3 end-to-end correlation id for this
	// chat turn. The HTTP server's traceIDMiddleware mints one
	// per request (or adopts a client-supplied one) and stamps
	// it on the request context; SendMessage copies the
	// context value into this field so the agent loop can
	// stamp every emitted chunk without re-reading ctx. Empty
	// when running outside the HTTP path (e.g. CLI REPL, tests).
	TraceID string `json:"trace_id,omitempty"`
}

type ChatStreamChunk struct {
	// Seq is the per-stream monotonic counter (0, 1, 2, …)
	// assigned by sendOrDrop. Stable for the lifetime of one
	// ChatStream call; NOT stable across streams. Surfaced on
	// the wire as the standard SSE `id:` line by handler.go so
	// the frontend (and curl) can attribute logs / events to a
	// specific position. Sub-agent chunks forwarded into the
	// parent stream do NOT carry a parent seq — they break the
	// monotonic sequence intentionally because the sub-agent has
	// its own counter.
	Seq uint64 `json:"seq,omitempty"`
	Content string `json:"content"`
	// Thinking carries a delta of the model's reasoning /
	// chain-of-thought text. Only populated by LLM clients
	// that surface a separate reasoning stream (Anthropic
	// thinking blocks, DeepSeek reasoning_content, OpenAI o1
	// reasoning tokens, etc.). Empty for models that don't
	// emit thinking. The UI renders it as a collapsible
	// gray block (DeepSeek-style) when non-empty.
	Thinking string `json:"thinking,omitempty"`
	Done     bool   `json:"done"`
	Error    string `json:"error,omitempty"`
	// Suggestion is an optional actionable hint that
	// accompanies an Error. Populated by the agent from
	// *llm.APIError.Suggestion after ClassifyAPIError runs.
	// Empty for non-classified errors or non-error chunks.
	Suggestion string `json:"suggestion,omitempty"`
	// ErrorKind is the classification of an Error chunk.
	// One of the strings returned by llm.ErrorKind.String()
	// ("auth_error", "rate_limit", "vision_unsupported", …).
	// Empty when the chunk isn't an error or wasn't
	// classified. The UI uses "vision_unsupported" to
	// render a special chip on the user message.
	ErrorKind string `json:"error_kind,omitempty"`

	Phase    string `json:"phase,omitempty"`
	Message  string `json:"message,omitempty"`
	Round    int    `json:"round,omitempty"`
	MaxRound int    `json:"max_round,omitempty"`
	Step     string `json:"step,omitempty"`
	Duration string `json:"duration,omitempty"`

	ToolID   string `json:"tool_id,omitempty"`
	ToolName string `json:"tool_name,omitempty"`
	ToolArgs string `json:"tool_args,omitempty"`
	ToolResult  string `json:"tool_result,omitempty"`
	// ToolResultFull is the untruncated tool result. ToolResult
	// above is a 300-char preview suitable for human display;
	// ToolResultFull is the full payload for tools whose results
	// the frontend needs to *parse* (todo_write in particular:
	// the truncated preview often cuts the JSON list in half and
	// JSON.parse fails silently, leaving the todo panel empty).
	// The frontend prefers ToolResultFull over ToolResult when
	// it's present.
	ToolResultFull string `json:"tool_result_full,omitempty"`
	ToolError      string `json:"tool_error,omitempty"`
	ToolElapsed    string `json:"tool_elapsed,omitempty"`

	TokensIn  int `json:"tokens_in,omitempty"`
	TokensOut int `json:"tokens_out,omitempty"`

	// SubAgent is true when this chunk originated from a
	// sub-agent (e.g. a `task` tool call). The UI should
	// render such events as nested / indented.
	SubAgent bool `json:"sub_agent,omitempty"`
	// SubAgentTask is the human description of the sub-agent
	// (the `description` argument passed to the `task` tool).
	// Surfaced by the parent as a card header so the user
	// can see what the sub-agent was asked to do.
	SubAgentTask string `json:"sub_agent_task,omitempty"`
	// SubAgentStatus is one of "start" (the sub-agent just
	// began), "ok" (it finished successfully), "error" (it
	// failed). Other phase values are treated as
	// in-progress.
	SubAgentStatus string `json:"sub_agent_status,omitempty"`
	// SubAgentType is the agent name (e.g. "explore",
	// "general-purpose") selected by the parent LLM via the
	// `subagent_type` arg of the `task` tool. Surfaced in the
	// card header so the user can see which agent was used.
	SubAgentType string `json:"sub_agent_type,omitempty"`
	// SubAgentModel is the model the sub-agent is using
	// (e.g. "openai/gpt-4o-mini"). Defaults to the parent's
	// model if the sub-agent does not specify one.
	SubAgentModel string `json:"sub_agent_model,omitempty"`
	// SubAgentColor is the agent's accent color in
	// "#RRGGBB" or a CSS color name. Drives the card's
	// border-left and icon tint.
	SubAgentColor string `json:"sub_agent_color,omitempty"`
	// SubAgentTaskID is an optional stable identifier the
	// LLM can pass back to resume / dedupe the sub-agent
	// run. Opaque string; currently SHA-256 truncated.
	SubAgentTaskID string `json:"sub_agent_task_id,omitempty"`
	// SubAgentDescription is the one-line "when to use" hint
	// for the agent (e.g. "Fast read-only file search.").
	// Surfaced as a hover tooltip on the agent-name badge in
	// the SubAgentCard so the user can read the full hint
	// without expanding the card body.
	SubAgentDescription string `json:"sub_agent_description,omitempty"`

	// ThinkingRewrite is emitted by the post-stream redactor
	// when the LLM's thinking block contained a phantom
	// vision error. Same shape as ContentRewrite but for the
	// thinking part; the UI REPLACES the trailing thinking
	// part's text with this value. Empty when no rewrite is
	// needed.
	ThinkingRewrite string `json:"thinking_rewrite,omitempty"`

	// SubAgentFailureReason is set on the synthetic
	// `sub_agent_err` close event so the UI can show the
	// user *why* the sub-agent failed. Empty on
	// `sub_agent_ok` close events. Mirrors the soft-fail
	// vs hard-fail distinction made by the runner: a
	// "soft" failure (content was produced before the
	// error) reports a friendly reason like
	// "tail-end stream error"; a "hard" failure (no
	// content) reports the actual error message from
	// the underlying chunk.
	SubAgentFailureReason string `json:"sub_agent_failure_reason,omitempty"`

	// TraceID is the P3-3 correlation id (e.g. "T-9f3c4a2b")
	// stamped on every chunk by sendOrDrop from the request
	// context. The frontend surfaces it on error bubbles via
	// the "复制 trace id" button; the server uses it in
	// `X-Trace-Id` response headers and as a log-line prefix.
	// Empty when the request didn't carry one (CLI, tests,
	// direct embedding).
	TraceID string `json:"trace_id,omitempty"`

	// Question fields — when the question tool emits a
	// question event, QuestionJSON carries the serialized
	// question payload (JSON array of Question objects).
	// The frontend renders it as a modal dialog and posts
	// the answer back via POST /sessions/:id/question-response.
	QuestionJSON string `json:"question_json,omitempty"`

	// ToolConfirm fields — when the sandbox decides a tool call
	// needs user confirmation, ToolConfirmJSON carries the serialized
	// ConfirmRequest. The frontend renders a confirm dialog.
	ToolConfirmJSON string `json:"tool_confirm_json,omitempty"`

	// ContentRewrite carries a *replacement* for the assistant's
	// trailing text part. Emitted by the agent when a post-stream
	// redactor (e.g. phantom vision-error filter) rewrites the
	// assistant's prose. The frontend should replace the trailing
	// text part's text with this value rather than append it.
	// Empty when no rewrite occurred. Type field on the SSE event
	// is "content_rewrite" (handled in chunkToEvent).
	ContentRewrite string `json:"content_rewrite,omitempty"`

	// SessionStatus carries the lifecycle state of the chat
	// turn: "busy" at the start of ChatWithTools, "idle" at
	// every exit point (success, error, cancel, max-rounds,
	// stuck-loop). The frontend uses this to drive
	// per-session "working" flags. Without it, the TodoPanel
	// state machine can't tell whether a session is mid-turn
	// (LLM may write more todos) or stopped (stale todos
	// should be cleared). Mirrors opencode's
	// `session.status { type: "busy" | "idle" }` event.
	SessionStatus string `json:"session_status,omitempty"`
}

// buildStaticSystemPrompt builds the **prefix-cacheable** portion of the
// system prompt: identity + soul + AGENTS + rules + skills + tool hint.
// The result is byte-stable across calls when nothing has changed, so the
// LLM's automatic prefix cache hits on it.
//
// The output is split into a single system message whose text is identical
// between rounds within a chat, AND between chats within a session. The
// only thing that should change in this string is the underlying files
// (AGENTS.md, rules, skills) or the chosen style.
// (e.g. after the user changes AGENTS.md or installs a new skill).
func (a *Agent) Reload() {
	skills, _ := skill.LoadAllWithRoot(a.lastProjectRoot)
	rulesList, _ := rules.LoadAllWithRoot(a.lastProjectRoot)
	a.skills = skills
	a.rules = rulesList
	a.staticPrompt = ""
	a.staticPromptID = ""
	a.kbIndexCache = ""
	a.kbIndexCacheKey = ""
	a.kbIndexCacheTime = 0
}

// ReloadWithRootIfChanged reloads skills / rules from the new
// projectRoot if it differs from the last one we loaded
// against. 2026-07: called from buildStaticSystemPrompt so
// switching projectRoot mid-session picks up the new
// project's skills + rules + the AGENTS.md OR loader
// automatically re-selects.  The static-prompt cache is
// invalidated when the root changes, but not when it
// matches — same-session follow-up messages hit the prefix
// cache as before.
//
// Pre-2026-07 the projectRoot never entered the loader
// path: skills and rules were loaded once at agent
// construction (using os.Getwd() inside the loaders), and
// only the rules.Watch mtime-poll reloaded them. The Wails
// GUI server's CWD is unrelated to the user's project, so
// project-level skills / rules never actually loaded.
func (a *Agent) ReloadWithRootIfChanged(root string) {
	if root == a.lastProjectRoot {
		return
	}
	a.lastProjectRoot = root
	skills, _ := skill.LoadAllWithRoot(root)
	rulesList, _ := rules.LoadAllWithRoot(root)
	a.skills = skills
	a.rules = rulesList
	a.staticPrompt = ""
	a.staticPromptID = ""
}

// StaticPromptInfo exposes the current static-prompt cache key for testing.
func (a *Agent) StaticPromptInfo() (prompt string, sig string, built bool) {
	return a.staticPrompt, a.staticPromptID, a.staticPrompt != ""
}

// agentsSignature returns a stable string representing the on-disk state
// of AGENTS files. We use mtime + size to detect changes cheaply.
func agentsSignature() string {
	g, _ := os.Stat(paths.GlobalAgents())
	p, _ := os.Stat(paths.ProjectAgents())
	return fileSig(g) + "|" + fileSig(p)
}

func agentsSignatureWithRoot(root string) string {
	g, _ := os.Stat(paths.GlobalAgents())
	if root != "" {
		// 2026-07: include both project-level slots
		// (root AGENTS.md and .p-chat/AGENTS.md) in the
		// sig so the cache invalidates when either changes.
		// The OR loader only reads one of them per call,
		// but both must be tracked for cache stability.
		p1, _ := os.Stat(paths.ProjectAgentsWithRoot(root))
		p2, _ := os.Stat(paths.ProjectPChatAgentsWithRoot(root))
		return fileSig(g) + "|" + fileSig(p1) + "|" + fileSig(p2) + "|" + root
	}
	p, _ := os.Stat(paths.ProjectAgents())
	return fileSig(g) + "|" + fileSig(p)
}

func rulesSignature(rs []rules.Rule) string {
	parts := make([]string, 0, len(rs))
	for _, r := range rs {
		st, _ := os.Stat(r.Path)
		parts = append(parts, r.Name+":"+fileSig(st))
	}
	return strings.Join(parts, ",")
}

func skillSignature(ss []skill.Skill) string {
	parts := make([]string, 0, len(ss))
	for _, s := range ss {
		st, _ := os.Stat(s.Path)
		parts = append(parts, s.Name+":"+fileSig(st))
	}
	return strings.Join(parts, ",")
}

func fileSig(info os.FileInfo) string {
	if info == nil {
		return "absent"
	}
	// mtime+size is fast but fragile: a user can `touch -t` to
	// backdate the mtime and the cache wouldn't refresh.
	// Including the inode (on POSIX) and a content hash would
	// be more robust, but reading the file at every prompt
	// build is too expensive. We use mtime+size as a fast
	// path; the rules hot-reload watcher (see
	// internal/rules.Watch) explicitly invalidates the cache
	// when it detects a change, so mtime preservation attacks
	// don't affect hot-reload users.
	return fmt.Sprintf("%d_%d", info.Size(), info.ModTime().UnixNano())
}

// toolEventChanKey is the context key under which the agent's tool
// dispatcher publishes a channel for tools (such as `task`) that want to
// stream sub-events back to the parent UI. Tools read this channel via
// GetToolEventChan(ctx) and send non-blocking events through it.
type toolEventChanKey struct{}

// GetToolEventChan returns the per-tool-call event channel published by
// the parent agent, or nil if ctx was not created by an agent tool
// dispatch.
func GetToolEventChan(ctx context.Context) chan<- ChatStreamChunk {
	if v, ok := ctx.Value(toolEventChanKey{}).(chan ChatStreamChunk); ok {
		return v
	}
	return nil
}

// parentModelCtxKey is the context key under which the agent publishes
// the *current turn's* (provider, model) pair. Sub-agents read this via
// GetParentModel(ctx) so the child session inherits the same model the
// user selected for the main conversation, not the server's startup
// default (which can differ when the user has switched providers/models
// mid-session via the GUI picker).
type parentModelCtxKey struct{}

// WithParentModel returns a new ctx carrying the parent turn's
// (provider, model) pair. Either may be empty; the tool handler should
// treat empty values as "no override, use the runner's default".
func WithParentModel(ctx context.Context, provider, model string) context.Context {
	if provider == "" && model == "" {
		return ctx
	}
	return context.WithValue(ctx, parentModelCtxKey{}, [2]string{provider, model})
}

// GetParentModel returns the (provider, model) pair published by the
// parent agent, or empty strings when no value was published.
func GetParentModel(ctx context.Context) (provider, model string) {
	if v, ok := ctx.Value(parentModelCtxKey{}).([2]string); ok {
		return v[0], v[1]
	}
	return "", ""
}

// subagentRegistryCtxKey is the context key under which the agent
// publishes the sub-agent registry to the `task` tool. The tool uses
// this to resolve the `subagent_type` argument to an AgentInfo (name,
// description, prompt, model, color, tools whitelist) and to build a
// dynamic tool description that lists the available agents.
//
// The registry is a small read-only interface (just `Get` and `List`)
// so the tool package can stay decoupled from internal/subagent.
type subagentRegistryCtxKey struct{}

// SubagentRegistry is the read-only view the `task` tool needs.
// Defined here (not in internal/subagent) to keep the dependency
// direction tool → subagent one-way. The concrete implementation
// lives in internal/subagent/registry.go.
type SubagentRegistry interface {
	Get(name string) (SubagentInfo, bool)
	List() []SubagentInfo
}

// SubagentInfo is the registry's view of one agent. Kept minimal:
// just the fields the `task` tool needs to (a) build a description
// and (b) wire the child session.
type SubagentInfo struct {
	Name        string
	Description string
	Prompt      string
	Model       string
	Color       string
	Tools       []string
}

// WithSubagentRegistry returns a new ctx carrying the given
// sub-agent registry. Called by the server's tool dispatcher so
// the `task` tool can resolve subagent_type at call time.
func WithSubagentRegistry(ctx context.Context, r SubagentRegistry) context.Context {
	if r == nil {
		return ctx
	}
	return context.WithValue(ctx, subagentRegistryCtxKey{}, r)
}

// GetSubagentRegistry returns the registry from ctx, or nil.
func GetSubagentRegistry(ctx context.Context) SubagentRegistry {
	if v, ok := ctx.Value(subagentRegistryCtxKey{}).(SubagentRegistry); ok {
		return v
	}
	return nil
}

// ChatStream is a single-turn chat with no tool support. For multi-turn
// ReAct with tool use, use ChatWithTools.
func (a *Agent) ChatStream(ctx context.Context, req ChatRequest) <-chan ChatStreamChunk {
	return a.ChatWithTools(ctx, req)
}

// sendOrDrop attempts to send a chunk to ch. If ctx is cancelled,
// the chunk is silently dropped so the producer can exit cleanly
// rather than blocking forever on a consumer that has disconnected.
//
// When nextSeq is non-nil, the chunk's Seq field is stamped with
// the value returned by nextSeq() BEFORE the send. This gives every
// stream a stable, debuggable per-stream order that the SSE handler
// forwards as the standard `id:` line — see P3-1 in
// docs/plans/round2-stream-and-render-plan.md. Pass nil from paths
// that don't want seq (legacy callers, tests that don't assert
// order, and — critically — sub-agent chunks forwarded through the
// parent stream, which intentionally break the parent's monotonic
// sequence).
//
// P3-3 trace id propagation: every chunk also gets its TraceID
// field populated from the request context (via
// trace.FromContext), so all downstream layers — SSE event JSON,
// log lines in the tool handler, X-Trace-Id on the response header
// — see the same id without callers having to set it on every
// chunk construction. If the chunk already carries a non-empty
// TraceID (e.g. a sub-agent chunk that the parent already
// stamped), we leave it alone.
//
// We pass a closure rather than a *atomic.Uint64 directly so callers
// don't have to thread `&counter` through 40+ call sites — the
// closure captures the local counter variable by reference. The
// returned value is whatever the closure yields, typically
// `seqCounter.Add(1) - 1` for the standard 0,1,2,… sequence.
func sendOrDrop(ctx context.Context, ch chan<- ChatStreamChunk, nextSeq func() uint64, chunk ChatStreamChunk) {
	if nextSeq != nil {
		chunk.Seq = nextSeq()
	}
	if chunk.TraceID == "" {
		if tid := trace.FromContext(ctx); tid != "" {
			chunk.TraceID = tid
		}
	}
	select {
	case ch <- chunk:
	case <-ctx.Done():
	}
}

// ChatWithTools performs a ReAct-style loop: send messages to the LLM with
// available tools, execute any tool calls, and feed results back to the LLM
// until it gives a final answer.
func (a *Agent) ChatWithTools(ctx context.Context, req ChatRequest) <-chan ChatStreamChunk {
	ch := make(chan ChatStreamChunk, 64)

	// P3-3: pull the trace id from req.TraceID first (the
	// SendMessage handler copies it from c.Request.Context()),
	// then fall back to whatever the request context already
	// carries (the traceIDMiddleware on the server side does
	// this for the SSE endpoints). Either way, we re-inject
	// the id under trace.ctxKey so every descendant
	// goroutine — tool forwarders, sub-agent runners, the
	// LLM stream reader — reads the same id via
	// trace.FromContext without having to thread it through
	// their own signatures.
	if req.TraceID != "" {
		ctx = trace.WithID(ctx, req.TraceID)
	} else if tid := trace.FromContext(ctx); tid != "" {
		// ensure the value is set under our key even if the
		// caller passed it via a different layer (defensive)
		ctx = trace.WithID(ctx, tid)
	}

	// Per-stream monotonic counter for P3-1. sendOrDrop
	// stamps each emitted chunk's Seq with nextSeq() (0, 1,
	// 2, …). Surfaced on the wire as the SSE `id:` line by
	// handler.go so the frontend (and curl) can debug
	// reorder / drop issues. Sub-agent chunks forwarded
	// through the parent stream do NOT increment this
	// counter — the parent chunk they replace is dropped
	// before reaching sendOrDrop, which leaves intentional
	// gaps in the parent's sequence.
	var seqCounter atomic.Uint64
	nextSeq := func() uint64 { return seqCounter.Add(1) - 1 }

	go func() {
		defer close(ch)
		// partsAcc accumulates the trailing assistant message's
		// parts (text + thinking + tool + sub_agent) as chunks
		// flow through. It's mutated both by the main LLM-stream
		// loop and by the per-tool forwarders (which carry
		// sub-agent events), so it carries its own mutex. The
		// final snapshot is encoded into the persisted message
		// metadata under "parts" so the same view comes back
		// when the user reopens the session.
		partsAcc := newPartsAccumulator()
		// Two defers, registered LIFO so they run in this order
		// on exit:
		//   1. send idle — always fires (normal or panic), so
		//      the frontend can never get stuck thinking the
		//      session is still busy. The inner recover() guards
		//      against "send on closed channel" if `ch` is
		//      somehow closed (it shouldn't be — close(ch) is
		//      the outermost defer and runs last).
		//   2. recover from panic — catches malformed LLM
		//      responses or buggy tool handlers so the REPL
		//      doesn't die. Sends a final Error chunk.
		defer func() {
			defer func() { _ = recover() }() // guard "send on closed"
			select {
			case ch <- ChatStreamChunk{SessionStatus: "idle"}:
			case <-time.After(2 * time.Second):
			}
		}()
		defer func() {
			if r := recover(); r != nil {
				stack := debug.Stack()
				sendOrDrop(ctx, ch, nextSeq, ChatStreamChunk{
					Phase: "system",
					Error: fmt.Sprintf("panic in agent: %v\n\n%s", r, stack),
					Done:  true,
				})
			}
		}()
		// Announce the start of the turn. The frontend uses this
		// to drive the TodoPanel state machine (`live` becomes
		// true, so a non-empty todo list stays open). Without
		// this signal the UI has no way to tell "the LLM is
		// mid-turn, don't clear stale todos" from "the LLM
		// P3-1: defer the "busy" signal until the system
		// prompt is ready (see the matching sendOrDrop
		// below). Sending it up here means the UI shows
		// "busy" while we're still loading the tool
		// registry, building skills/rules, and assembling
		// the prompt — a 100-500ms gap where the user sees
		// the spinner but no real work is happening. By
		// the time the prompt is built we know the round
		// is about to start, which is what "busy" actually
		// means to the user.
		start := time.Now()

		sendOrDrop(ctx, ch, nextSeq, ChatStreamChunk{Phase: "system", Step: "load-tools", Message: "加载工具列表..."})
		availableTools := a.tools.List()
		// Remove wiki tools when knowledge base is off. grep is a
		// general-purpose search tool and remains available.
		kbEnabled := req.KBBase != "" && req.KBBase != "__off__"
		if !kbEnabled {
			filtered := make([]tool.Tool, 0, len(availableTools))
			for _, t := range availableTools {
				if t.Name == "wiki_lookup" || t.Name == "wiki_list" {
					continue
				}
				filtered = append(filtered, t)
			}
			availableTools = filtered
		}
		toolDefs := llm.ToolsFromRegistryDef(availableTools)
		if len(toolDefs) > 0 {
			names := make([]string, 0, len(availableTools))
			for _, t := range availableTools {
				names = append(names, t.Name)
			}
			sendOrDrop(ctx, ch, nextSeq, ChatStreamChunk{Phase: "tools", Step: "tools", Message: fmt.Sprintf("可用工具 (%d): %s", len(availableTools), strings.Join(names, ", "))})
		} else {
			sendOrDrop(ctx, ch, nextSeq, ChatStreamChunk{Phase: "tools", Step: "tools", Message: "未注册任何工具"})
			toolDefs = nil
		}

		sendOrDrop(ctx, ch, nextSeq, ChatStreamChunk{Phase: "system", Step: "load-system", Message: "合并风格 / AGENTS.md / 规则 / 技能..."})
		systemPrompt, _, err := a.buildStaticSystemPrompt(req.Style, toolDefs, availableTools, req.ProjectRoot, kbEnabled)
		if err != nil {
			sendOrDrop(ctx, ch, nextSeq, ChatStreamChunk{Phase: "system", Error: err.Error(), Done: true})
			return
		}
		// Inject knowledge base context + index.
		if kbEnabled {
			kbIndex := a.buildKBIndex(req.KBBase)
			systemPrompt += kbIndex
		}
		// Sub-agent prompt override. When the sub-agent runner
		// supplies a prompt (from the agent's own prompt or the
		// request's `prompt` arg), it REPLACES the normal
		// system prompt. We still append compressed summary /
		// skill context below so the child retains access to
		// any user context that was in flight.
		if req.PromptOv != "" {
			systemPrompt = req.PromptOv
		}
		// Re-append the "Working Directory" section when a
		// sub-agent overrides the system prompt. The main
		// agent gets this from buildStaticSystemPrompt above
		// (conditional on projectRoot != ""), but the override
		// above wipes the cached prefix, so sub-agents would
		// otherwise have no idea what directory exec_command
		// and read_file are anchored to. Mirrors the wording
		// in buildStaticSystemPrompt so the LLM sees a
		// consistent instruction in both contexts.
		if req.ProjectRoot != "" {
			systemPrompt += appendWorkingDirectoryBlock(req.ProjectRoot)
		}
		sendOrDrop(ctx, ch, nextSeq, ChatStreamChunk{Phase: "system", Step: "ok", Message: fmt.Sprintf("系统提示已就绪 (%d 字符)", len(systemPrompt)), Duration: formatElapsed(time.Since(start))})
		// P3-1: announce "busy" now that the system prompt
		// is assembled and the first LLM call is imminent.
		// The matching "idle" still fires from the
		// outermost defer, so the UI gets a correct
		// busy→idle envelope on every exit path.
		sendOrDrop(ctx, ch, nextSeq, ChatStreamChunk{SessionStatus: "busy"})

		// Append compressed summary if provided (from /compress).
		if req.CompressedSummary != "" {
			systemPrompt += "\n\n[前文摘要]\n" + req.CompressedSummary
		}
		// Append active skill context (from /skillname slash command).
		if req.SkillContext != "" {
			systemPrompt += "\n\n---\n\n## 激活的技能上下文\n\n" + req.SkillContext + "\n"
		}
		// Append style memory (动态注入，不破坏静态缓存)
		if styleMemory := a.getStyleMemory(req.Style); styleMemory != "" {
			systemPrompt += "\n\n---\n\n## 我的上下文\n\n" + styleMemory
		}

		// Build the message list: system prompt + user messages.
		// Each message is a separate protocol-agnostic ChatMessage.
		msgs := []llm.ChatMessage{
			{Role: llm.RoleSystem, Type: llm.TypeText, Content: systemPrompt},
		}
		// When knowledge base is off, strip wiki-related messages
		// (tool calls + results) from history so the LLM doesn't
		// learn about wiki tools from previous turns
		// and try to call them via text-format tool_call blocks.
		if kbEnabled {
			msgs = append(msgs, req.Messages...)
		} else {
			for _, m := range req.Messages {
				if m.ToolName == "wiki_lookup" || m.ToolName == "wiki_list" {
					continue
				}
				msgs = append(msgs, m)
			}
		}

		// NOTE: image base64 payloads are intentionally kept
		// intact in msgs. Earlier code stripped them with a
		// text-marker placeholder after the LLM call to save
		// tokens, but that placeholder was being applied BEFORE
		// the LLM saw the image (call-order bug), producing an
		// invalid `data:image/png;base64,[image: ...]` URL and
		// causing the upstream API to reject the request with a
		// parameter error. We now keep the base64 verbatim so
		// the LLM actually receives the image. Token budget is
		// managed by tryAutoCompact instead.

		// Expand any user-uploaded attachments into separate
		// ChatMessage entries (text msg + image/file msgs).
		if len(req.Attachments) > 0 && a.attach != nil {
			protocol := a.protocolFor(req.Provider)
			vision := func() bool { return a.modelSupportsVision(req.Provider, req.Model) }
			msgs = ExpandAttachmentsCM(protocol, msgs, req.Attachments, a.attach, vision)
			sendOrDrop(ctx, ch, nextSeq, ChatStreamChunk{Phase: "system", Step: "attachments", Message: fmt.Sprintf("展开 %d 个附件", len(req.Attachments))})
		}

		if a.store != nil {
			sendOrDrop(ctx, ch, nextSeq, ChatStreamChunk{Phase: "memory", Step: "memory", Message: fmt.Sprintf("写入消息到记忆")})
			// Persist all user-facing messages (including
			// image attachments as separate rows). Use the
			// per-session variant so concurrent streams on
			// different sessions don't race on the global
			// currentID.
			//
			// The first user message in the batch carries the
			// explicit id minted by the frontend (req.ClientMsgID).
			// We pin it to the row's id on disk so the local
			// msg.id and the SQLite row id match exactly, giving
			// rollback/regen a valid target even when the LLM
			// call fails before the SSE `done` event lands.
			// All other rows in the same batch (subsequent
			// text/image attachments, the agent's own scratch
			// messages) use AUTOINCREMENT as before.
			//
			// P1-4 regen path: req.Messages is the history
			// loaded from the DB by regen.go (user text +
			// image rows that were already persisted on the
			// original send). Re-persisting those rows would
			// create duplicate rows that grow on every regen
			// round — the LLM context would see 2 copies, then
			// 3, etc. Skip the history prefix and only persist
			// the new tail (messages the agent loop produced
			// after the initial LLM context was assembled, e.g.
			// P0-3 auto-continue reminders and round-2+ tool
			// scratch rows).
			isRegen := req.RegenGroupID != ""
			histEnd := 1 + len(req.Messages) // msgs[0] is system, msgs[1:1+histLen] is the loaded history
			pinnedUserID := req.ClientMsgID
			for i, m := range msgs {
				if m.Role == llm.RoleSystem {
					continue
				}
				if isRegen && i >= 1 && i < histEnd {
					// History row — already in DB. Skip.
					continue
				}
				if pinnedUserID > 0 && m.Role == llm.RoleUser {
					a.store.AddChatMessageToWithID(req.SessionID, m, pinnedUserID)
					// Only the first user message gets the
					// pinned id; if the agent later synthesises
					// another user message in this same stream
					// (e.g. P0-3 auto-continue reminder), let
					// it autoincrement as usual.
					pinnedUserID = 0
					continue
				}
				a.store.AddChatMessageTo(req.SessionID, m)
			}
		}

		// Plan mode: the LLM can use `todo_write` to break down
		// the analysis into steps, and `question` to clarify vague
		// requirements. Other tools are disabled — Plan Mode is
		// for planning, not executing.
		//
		// Build mode uses a soft cap (MaxRoundsDefault) instead of
		// "unlimited". The LLM normally terminates by emitting no
		// tool calls; the cap is the safety net for stuck loops
		// (same tool call failing repeatedly, the model not
		// noticing, the loop running forever). On the last round
		// we drop the `tools` field and inject MaxStepsPrompt as
		// a fake assistant message — the model physically cannot
		// call tools and is forced to give a text summary.
		// See opencode's `runner/max-steps.ts:1-16`.
		maxRounds := MaxRoundsDefault
		// Per-request override (takes priority).
		if req.MaxRounds > 0 {
			maxRounds = req.MaxRounds
		} else if a.cfg != nil && a.cfg.Limits.MaxRounds > 0 {
			maxRounds = a.cfg.Limits.MaxRounds
		}
		if req.PlanMode {
			var planTools []llm.ToolDef
			for _, t := range toolDefs {
				if t.Name == "todo_write" || t.Name == "question" {
					planTools = append(planTools, t)
				}
			}
			toolDefs = planTools
			msgs[0].Content += "\n\n---\n\n## Plan Mode\n\n" +
				"你正在 PLAN MODE：不要调用任何执行类工具 (exec_command, write_file, task 等)。\n" +
				"你可以使用 `todo_write` 将分析/计划拆分为步骤，\n" +
				"也可以使用 `question` 向用户澄清模糊需求。\n" +
				"请给出 step-by-step 执行计划：\n" +
				"1. 每一步做什么\n" +
				"2. 每一步预期使用什么工具 (read_file, write_file, exec_command, list_files, task 等)\n" +
				"3. 每一步的预期结果\n" +
				"4. 风险 / 依赖 / 边界\n" +
				"用户审阅后切换回构建模式执行。\n"
			maxRounds = 1
			sendOrDrop(ctx, ch, nextSeq, ChatStreamChunk{Phase: "plan", Step: "plan-mode", Message: "Plan Mode 启用 (可用 todo_write / question，最多单轮)"})
		} else {
			sendOrDrop(ctx, ch, nextSeq, ChatStreamChunk{Phase: "plan", Step: "plan", Message: fmt.Sprintf("构建模式 — LLM 自主决定何时终止 (上限 %d 轮)", maxRounds)})
		}

		var totalIn, totalOut int

		// Stuck-loop guard. Track the signature of the (sorted)
		// tool calls in each round, plus whether the round ended
		// in tool errors. If the signature repeats for
		// StuckThreshold consecutive rounds AND the last round
		// errored, we break out with a "stuck" event rather than
		// letting the LLM hammer the same failing call forever.
		var (
			stuckStreak          int
			prevToolSig          string
			prevErrored          bool
			sameToolErrName      string
			sameToolErrCount     int
			nearLimitWarningSent bool
			// P0-3: auto-continue counter. Resets every
			// user turn (declared outside the for loop,
			// so a new SendMessage call gets a fresh count).
			// Capped at MaxAutoContinue to prevent the LLM
			// from learning to rely on auto-prompting as a
			// substitute for actually finishing work.
			autoContinueCount int
		)
		const stuckThreshold = 3
		const sameToolErrMax = 4

		for round := 1; maxRounds == 0 || round <= maxRounds; round++ {
			roundStart := time.Now()
			roundNum := round
			partsAcc = newPartsAccumulator()

			sendOrDrop(ctx, ch, nextSeq, ChatStreamChunk{Phase: "llm", Step: fmt.Sprintf("round-%d", roundNum), Message: fmt.Sprintf("[第 %d 轮] 调用 LLM", roundNum), Round: roundNum, MaxRound: maxRounds})

			var (
				fullContent         string
				fullThinking        string
				toolCalls           []nativeToolCall
				argsAccum           = make(map[int]*nativeToolCall)
				roundAnyToolErrored bool
			)

			opts := a.options
			if req.ReasoningEffort != "" {
				opts.ReasoningEffort = req.ReasoningEffort
			}

			// Per-round request assembly. On the last round we
			// drop the `tools` field and inject MaxStepsPrompt as
			// a fake assistant message — the model physically
			// cannot call tools and is forced to give a text
			// summary. See opencode's `runner/max-steps.ts:1-16`
			// and `llm.ts:197-209`.
			isLastRound := maxRounds > 0 && round >= maxRounds

			// Pre-limit warning: when within 10 rounds of the
			// cap, inject a gentle heads-up so the LLM can wrap
			// up gracefully instead of being cut off abruptly.
			// Only injected once — the flag ensures no spam.
			if !nearLimitWarningSent && maxRounds > 10 && round >= maxRounds-10 && !isLastRound {
				nearLimitWarningSent = true
				msgs = append(msgs, llm.ChatMessage{
					Role:    llm.RoleSystem,
					Type:    llm.TypeText,
					Content: fmt.Sprintf("注意：当前会话轮次即将达到上限（%d 轮，剩余约 %d 轮）。请开始收尾当前任务，优先完成最关键的未完成工作，避免开启需要多轮的新子任务。", maxRounds, maxRounds-round),
				})
				sendOrDrop(ctx, ch, nextSeq, ChatStreamChunk{
					Phase:    "llm",
					Step:     "near-limit",
					Message:  fmt.Sprintf("轮次接近上限（剩余 %d 轮），提醒 LLM 收尾", maxRounds-round),
					Round:    roundNum,
					MaxRound: maxRounds,
				})
			}

			// Auto-compact before LLM call (skip on last round).
			// If the context exceeds the token budget, compress
			// and rebuild the system prompt so the provider call
			// doesn't fail with a 413. On the last round tools
			// are disabled anyway so compact isn't worth it.
			if !isLastRound && a.tryAutoCompact(ctx, &msgs, req, ch, nextSeq, roundNum, maxRounds) {
				continue
			}

			roundMsgs := msgs
			roundTools := toolDefs
			if isLastRound {
				roundTools = nil
				roundMsgs = append([]llm.ChatMessage{}, msgs...)
				// Pick the language variant of the max-steps
				// prompt from the same config the system
				// prompt uses (a.cfg.LLM.Output.Language).
				// P2-2 — see pickMaxStepsPrompt for the rule.
				maxStepsLang := ""
				if a.cfg != nil {
					maxStepsLang = a.cfg.LLM.Output.Language
				}
				roundMsgs = append(roundMsgs, llm.ChatMessage{
					Role:    llm.RoleAssistant,
					Type:    llm.TypeText,
					Content: pickMaxStepsPrompt(maxStepsLang),
				})
				sendOrDrop(ctx, ch, nextSeq, ChatStreamChunk{Phase: "llm", Step: "max-steps", Message: "已达到轮次上限 — 强制文本回复（不再调用工具）", Round: roundNum, MaxRound: maxRounds})
			}

			// LLM stream with retry for recoverable errors
			// (rate_limit, server_error, network, timeout).
			const maxLLMRetries = 3
			var retryableErr error
			att:
			for attempt := 1; attempt <= maxLLMRetries; attempt++ {
				if attempt > 1 {
					backoff := time.Duration(attempt*attempt) * time.Second
					sendOrDrop(ctx, ch, nextSeq, ChatStreamChunk{
						Phase:    "llm",
						Step:     "retry",
						Message:  fmt.Sprintf("%s 后重试 (第 %d/%d 次)…", backoff, attempt, maxLLMRetries),
						Round:    roundNum,
						MaxRound: maxRounds,
					})
					select {
					case <-ctx.Done():
						return
					case <-time.After(backoff):
					}
				}

				// Only mangle the tool_call/tool_result pairing for
				// protocols that need it (legacy: a handful of
				// OpenAI-compatible proxies that validate the
				// pairing and reject mixed tool_call + tool_result
				// rounds). For standard openai and anthropic the
				// LLM needs to see the pairing so it can recognise
				// the user answer and stop looping — without it,
				// the LLM interprets the tool result as a user
				// message and dutifully re-asks via the question
				// tool, which is exactly the bug we hit with
				// `cs` (Doubao) and `mimo-v2.5` in 2026-07-09.
				//
				// See needsNormalizedToolResults for the
				// provider list.
				msgsForLLM := roundMsgs
				if needsNormalizedToolResults(req.Provider) {
					msgsForLLM = normalizeToolResults(roundMsgs)
				}
				stream := a.llm.ChatStreamCM(ctx, req.Provider, req.Model, msgsForLLM, roundTools, opts)
				for chunk := range stream {
					if chunk.Err != nil {
						classified := llm.ClassifyAPIError(req.Provider, chunk.Err)
						errMsg, errSuggestion, errKind := chunk.Err.Error(), "", ""
						if apiErr, ok := classified.(*llm.APIError); ok {
							errMsg = apiErr.Message
							errSuggestion = apiErr.Suggestion
							errKind = apiErr.Kind.String()
							if isRetryable(apiErr.Kind) && attempt < maxLLMRetries {
								retryableErr = chunk.Err
								break // break inner stream loop, retry outer
							}
						}
						sendOrDrop(ctx, ch, nextSeq, ChatStreamChunk{
							Phase:      "llm",
							Error:      errMsg,
							Suggestion: errSuggestion,
							ErrorKind:  errKind,
							Done:       true,
						})
						return
					}
					if chunk.Done {
						retryableErr = nil
						break att // success — break outer loop too
					}
				if chunk.TokensIn > 0 || chunk.TokensOut > 0 {
					if chunk.TokensIn > totalIn {
						totalIn = chunk.TokensIn
					}
					if chunk.TokensOut > totalOut {
						totalOut = chunk.TokensOut
					}
				}
				if chunk.Content != "" {
					fullContent += chunk.Content
					partsAcc.update(ChatStreamChunk{Content: chunk.Content})
					sendOrDrop(ctx, ch, nextSeq, ChatStreamChunk{Content: chunk.Content, TokensIn: totalIn, TokensOut: totalOut})
				}
				if chunk.Thinking != "" {
					fullThinking += chunk.Thinking
					partsAcc.update(ChatStreamChunk{Thinking: chunk.Thinking})
					sendOrDrop(ctx, ch, nextSeq, ChatStreamChunk{Thinking: chunk.Thinking, TokensIn: totalIn, TokensOut: totalOut})
				}
				if chunk.ToolCallDelta != nil {
					tcd := chunk.ToolCallDelta
					existing, ok := argsAccum[tcd.Index]
					if !ok {
						existing = &nativeToolCall{ID: tcd.ID, Name: tcd.Name}
						argsAccum[tcd.Index] = existing
					}
					if tcd.ID != "" {
						existing.ID = tcd.ID
					}
					if tcd.Name != "" {
						existing.Name = tcd.Name
					}
					existing.ArgsJSON += tcd.ArgsJSON
				}
				} // inner for chunk
			} // outer for attempt (retry loop)

			// If we exhausted retries without success, surface the last error.
			if retryableErr != nil {
				sendOrDrop(ctx, ch, nextSeq, ChatStreamChunk{
					Phase: "llm",
					Error: fmt.Sprintf("重试 %d 次后仍然失败: %v", maxLLMRetries, retryableErr),
					Done:  true,
				})
				return
			}

			for _, t := range argsAccum {
				toolCalls = append(toolCalls, *t)
			}

			sendOrDrop(ctx, ch, nextSeq, ChatStreamChunk{Phase: "llm", Step: fmt.Sprintf("round-%d-done", roundNum), Message: fmt.Sprintf("[第 %d 轮] 模型响应: %d 字符 / 耗时 %s", roundNum, len(fullContent), formatElapsed(time.Since(roundStart))), Round: roundNum, MaxRound: maxRounds, TokensIn: totalIn, TokensOut: totalOut})

		if len(toolCalls) == 0 {
			toolCalls = parseMarkdownToolCalls(fullContent)
		}
		// When tool calls are present (native or markdown), strip
		// markdown tool_call blocks from the text content so the
		// user doesn't see both raw tool blocks AND tool cards.
		if len(toolCalls) > 0 {
			fullContent = cleanMarkdownToolCalls(fullContent)
		}

		// Post-stream redactor: catch phantom "ERROR: Cannot read
		// image.png ... Inform the user." style responses that
		// DeepSeek-trained models parrot when they see the
		// vision-denier marker. We can't fully prevent the model
		// from producing this text (it appears in training data
		// as a Claude response), so we filter it AFTER the stream
		// ends and emit a content_rewrite event so the UI replaces
		// what the user already saw.
		//
		// Redact in BOTH fullContent (text response) and
		// fullThinking (chain-of-thought). The phantom appears
		// in training data and the model sometimes emits it in
		// the thinking block instead of the text response —
		// the user sees thinking as a collapsible panel so the
		// phantom needs to be stripped there too.
		if redacted, changed := redactPhantomErrors(fullContent); changed {
			fullContent = redacted
			sendOrDrop(ctx, ch, nextSeq, ChatStreamChunk{Phase: "llm", Step: "redact", Message: "(已替换 LLM 编造的图片错误消息)", ContentRewrite: redacted})
		}
		if redactedT, changedT := redactPhantomErrors(fullThinking); changedT {
			fullThinking = redactedT
			sendOrDrop(ctx, ch, nextSeq, ChatStreamChunk{Phase: "llm", Step: "redact-thinking", Message: "(已替换 LLM 编造的图片错误消息)", ThinkingRewrite: redactedT})
		}

		// Build the assistant message for the conversation.
		// Emit as a single text ChatMessage (tool calls are
		// separate messages appended below).
		assistantMsg := llm.ChatMessage{
			Role:        llm.RoleAssistant,
			Type:        llm.TypeText,
			Content:     fullContent,
			MsgType:     llm.MsgTypeText,
			SubmitToLLM: 1,
		}
		msgs = append(msgs, assistantMsg)

		// NOTE: per-round stripImageContent sweep removed.
		// Image base64 is preserved verbatim in msgs so the LLM
		// always receives the actual image on every round (and
		// across tool-call follow-ups within the same turn).
		// Token budget for repeated tool rounds is handled by
		// tryAutoCompact.

		// Persist assistant message later — after tool
		// results are in partsAcc (see end of this round).

			// P1-2: run auto-compact BEFORE appending the
			// current round's tool_call messages. The old
			// order (append → compact → re-append) was
			// correct but redundant: compact could absorb
			// the just-appended tool_call, and the
			// re-append loop had to put it back. Moving
			// compact ahead makes the order naturally
			// idempotent — tool_call append happens after
			// compaction so it always survives.
			//
			// The function returns true when compaction
			// actually fired. We do NOT continue here:
			// the LLM already decided to call these tools,
			// the user expects them to run, and skipping
			// execution would leak orphan tool_calls into
			// the next round's history. Fall through to
			// the tool execution block below.
			a.tryAutoCompact(ctx, &msgs, req, ch, nextSeq, roundNum, maxRounds)

			// Append tool_call messages for each tool call.
			//
			// P2-3: the LLM often omits the tool_call ID
			// (older models, or markdown-fallback parsing
			// where the ID field is not in the JSON shape),
			// and occasionally emits duplicate IDs across
			// the same round. Both cases break the
			// tool_call/tool_result pairing the OpenAI /
			// Anthropic protocol depends on — the
			// corresponding tool result needs to reference
			// the same ID, and SQLite's UNIQUE on the
			// (session_id, tool_call_id) column will reject
			// duplicates with an opaque error.
			//
			// The fix: rebuild the ID for any tool_call
			// that has an empty OR already-seen ID. The
			// "call_<uuid>" format is preserved for
			// downstream parsers (handlers, the UI's tool
			// card key) so this is a transparent change.
			seenIDs := make(map[string]bool, len(toolCalls))
			normalizeToolCallIDs(toolCalls, seenIDs)
			for i := range toolCalls {
				tc := &toolCalls[i]
				tcm := llm.ChatMessage{
					Role:        llm.RoleAssistant,
					Type:        llm.TypeToolCall,
					ToolID:      tc.ID,
					ToolName:    tc.Name,
					ToolInput:   tc.ArgsJSON,
					MsgType:     llm.MsgTypeTool,
					SubmitToLLM: 1,
				}
				msgs = append(msgs, tcm)
				if a.store != nil {
					a.store.AddChatMessageTo(req.SessionID, tcm)
				}
			}

			if len(toolCalls) == 0 {
				// P0-3: auto-continue guard. The LLM often
				// finishes a real tool run but emits a
				// "ready to continue" text block instead of
				// the next tool_call. Without this guard the
				// user has to type "继续" manually. We check
				// the todo list: if any item is still
				// pending or in_progress, inject a user-style
				// reminder and re-enter the loop. The cap
				// (MaxAutoContinue) prevents training the
				// LLM to rely on this as a crutch.
				if req.AutoContinue && autoContinueCount < MaxAutoContinue {
					if pending, list := sessionPendingTodos(req.SessionID); len(list) > 0 {
						autoContinueCount++
						msgs = append(msgs, llm.ChatMessage{
							Role:    llm.RoleUser,
							Type:    llm.TypeText,
							Content: buildAutoContinuePrompt(list),
						})
						sendOrDrop(ctx, ch, nextSeq, ChatStreamChunk{
							Phase:    "auto-continue",
							Step:     "todo-incomplete",
							Message:  fmt.Sprintf("⚠ 检测到 %d 项未完成 todo，自动续 LLM (第 %d/%d 次)", pending, autoContinueCount, MaxAutoContinue),
							Round:    roundNum,
							MaxRound: maxRounds,
						})
						continue
					}
				}
				persistAssistant(req.SessionID, a.store, assistantMsg, fullThinking, partsAcc, totalIn, totalOut, req.RegenGroupID)
				sendOrDrop(ctx, ch, nextSeq, ChatStreamChunk{Phase: "done", Step: "done", Message: fmt.Sprintf("完成 (总耗时 %s, 共 %d 轮)", formatElapsed(time.Since(start)), roundNum), Round: roundNum, MaxRound: maxRounds, TokensIn: totalIn, TokensOut: totalOut})
				sendOrDrop(ctx, ch, nextSeq, ChatStreamChunk{Done: true})
				return
			}

			// Auto-compact has moved ahead of the tool_call
			// append (P1-2) so we no longer need the old
			// re-append loop here. The two are merged into
			// the single call above the tool_call append.

			sendOrDrop(ctx, ch, nextSeq, ChatStreamChunk{Phase: "tool", Step: fmt.Sprintf("round-%d-tools", roundNum), Message: fmt.Sprintf("[第 %d/%d 轮] 检测到 %d 个工具调用", roundNum, maxRounds, len(toolCalls)), Round: roundNum, MaxRound: maxRounds})

			// Run tool calls in parallel when the LLM emitted more than one.
			// Each call gets its own per-tool timeout derived from the parent
			// ctx; the parent ctx is checked before sending events so a
			// cancellation cleanly aborts the round.
			type toolOutcome struct {
				idx     int
				tc      nativeToolCall
				result  *tool.CallResult
				err     error
				elapsed time.Duration
			}
			outcomes := make([]toolOutcome, len(toolCalls))

			// Emit "starting" events for all calls (in order) before launching.
			for i, tc := range toolCalls {
				if tc.ID == "" {
					tc.ID = "call_" + uuid.NewString()
					toolCalls[i].ID = tc.ID
				}
				argsPreview := tc.ArgsJSON
				if len(argsPreview) > 200 {
					argsPreview = argsPreview[:200] + "..."
				}
				startChunk := ChatStreamChunk{Phase: "tool", Step: fmt.Sprintf("call-%d", i+1), Message: fmt.Sprintf("  -> 工具 %d/%d: %s", i+1, len(toolCalls), tc.Name), ToolID: tc.ID, ToolName: tc.Name, ToolArgs: argsPreview, Round: roundNum, MaxRound: maxRounds}
				partsAcc.update(startChunk)
				sendOrDrop(ctx, ch, nextSeq, startChunk)
			}

			// Each tool call gets its own event channel. The agent loop
			// launches a forwarder per channel and collects the done
			// signals so we can wait for all events to flush before
			// emitting the per-call result events below.
			type forwarder struct{ done chan struct{} }
			var forwarders []forwarder
			var wg sync.WaitGroup
			for i, tc := range toolCalls {
				wg.Add(1)

				// Per-tool event channel. Buffer is 64 (was 16) so
				// a sub-agent producing many content/thinking/tool
				// chunks can never fill it up and drop the closing
				// sub_agent_ok / sub_agent_err event — which would
				// leave the GUI's nested card stuck in the "running"
				// spinner forever. 64 is comfortably larger than
				// the chunk count a single sub-agent turn produces
				// in practice; if a turn ever needs more, the
				// forwarder will drain it before the next push.
				eventCh := make(chan ChatStreamChunk, 64)
				tctx := context.WithValue(ctx, toolEventChanKey{}, eventCh)
				if a.subagentRegistry != nil {
					tctx = WithSubagentRegistry(tctx, a.subagentRegistry)
				}
				// Publish the parent turn's current (provider, model)
				// pair so the sub-agent tool handler can inherit the
				// same model the user selected for the main
				// conversation. Without this the sub-agent would
				// fall back to whatever the server's startup
				// default was, which silently breaks model
				// selection when the user has switched models
				// mid-session.
				tctx = WithParentModel(tctx, req.Provider, req.Model)
				if req.SessionID != "" {
					tctx = tool.WithSessionID(tctx, req.SessionID)
				}
				if req.PermissionLevel != "" {
					tctx = tool.WithPermissionLevel(tctx, req.PermissionLevel)
				}
				if req.ProjectRoot != "" {
					tctx = tool.WithProjectRoot(tctx, req.ProjectRoot)
				}
				// P3-3: propagate the trace id so tool handlers
				// can prefix their log lines with it. The id
				// is already on the request context (set by
				// traceIDMiddleware upstream and re-injected
				// at the top of ChatWithTools); this call
				// installs it under tool.traceKey for the
				// handler's TraceIDFromCtx accessor.
				if tid := trace.FromContext(ctx); tid != "" {
					tctx = tool.WithTraceID(tctx, tid)
				}
				// Inject event sender so the question tool can
				// emit "question" events through the SSE stream.
				tctx = tool.WithEventSender(tctx, func(jsonData string) {
					select {
					case eventCh <- ChatStreamChunk{QuestionJSON: jsonData}:
					case <-time.After(2 * time.Second):
						log.Printf("%s[question] dropped event (channel full for 2s)", trace.LogPrefix(tctx))
					}
				})
				tctx, cancel := context.WithTimeout(tctx, 5*time.Minute)

				fwd := forwarder{done: make(chan struct{})}
				forwarders = append(forwarders, fwd)
				go func() {
					defer close(fwd.done)
					defer func() {
						if r := recover(); r != nil {
							log.Printf("%s[forwarder] panic: %v", trace.LogPrefix(tctx), r)
						}
					}()
					for ev := range eventCh {
						// Sub-agent / tool events arrive here
						// from the per-tool dispatcher. Feed
						// them into the parts accumulator so
						// nested cards survive a session
						// reload, then forward to the main
						// channel for the live UI.
						if ev.QuestionJSON != "" {
							log.Printf("[forwarder] got question event (%d bytes)", len(ev.QuestionJSON))
						}
						partsAcc.update(ev)
						sendOrDrop(ctx, ch, nextSeq, ev)
						if ev.QuestionJSON != "" {
							log.Printf("[forwarder] forwarded question event to main ch")
						}
					}
				}()

				go func(i int, tc nativeToolCall) {
					defer wg.Done()
					defer cancel()
					defer close(eventCh)

					// Sub-agent concurrency gate: if the
					// semaphore is set, only N task calls
					// can run in parallel. Other tools are
					// not limited.
					if tc.Name == "task" && a.subAgentSem != nil {
						select {
						case a.subAgentSem <- struct{}{}:
							defer func() { <-a.subAgentSem }()
						case <-ctx.Done():
							return
						}
					}

					handler, ok := a.tools.Get(tc.Name)
					if !ok {
						errMsg := fmt.Sprintf("error: tool %q not found (available: %s)", tc.Name, availableToolNames(a.tools.List()))
						// Browser tools may have been unregistered at runtime.
						// Provide a clearer message when all browser_ tools are gone.
						if strings.HasPrefix(tc.Name, "browser_") {
							hasAnyBrowser := false
							for _, n := range a.tools.Names() {
								if strings.HasPrefix(n, "browser_") {
									hasAnyBrowser = true
									break
								}
							}
							if !hasAnyBrowser {
								errMsg = fmt.Sprintf("error: tool %q has been unregistered — the browser extension (P-chat Browser) has disconnected. All browser_* tools are no longer available. Do NOT attempt to retry this tool.", tc.Name)
							}
						}
						outcomes[i] = toolOutcome{
							idx:    i,
							tc:     tc,
							result: &tool.CallResult{Content: errMsg, IsError: true},
							err:    fmt.Errorf("not found"),
						}
						return
					}

					// Refuse wiki/grep tools when knowledge base is
					// disabled for this session. The tools may still
					// be registered globally (KB enabled at startup)
					// but are blocked at dispatch time so the LLM
					// cannot call them via text-format tool_call
					// blocks even if they appear in conversation
					// history from a previous KB-enabled turn.
					if !kbEnabled && (tc.Name == "wiki_lookup" || tc.Name == "wiki_list") {
						errMsg := fmt.Sprintf("error: knowledge base is disabled for this session — enable it in settings to use %s", tc.Name)
						outcomes[i] = toolOutcome{
							idx:    i,
							tc:     tc,
							result: &tool.CallResult{Content: errMsg, IsError: true},
							err:    fmt.Errorf("kb disabled"),
						}
						return
					}

					toolStart := time.Now()
					argsRaw := json.RawMessage(tc.ArgsJSON)
					if len(argsRaw) == 0 {
						argsRaw = json.RawMessage("{}")
					}
					// Inject the sandbox into the tool's context so
					// built-in tools (exec_command, write_file) can
					// short-circuit dangerous operations. If the user
					// ran /unsafe once, the next call bypasses the
					// sandbox check entirely.
					//
					// Permission level (per-session) controls sandbox
					// behaviour:
					//   "full" — skip all sandbox checks
					//   "auto" — auto-approve confirm decisions
					//   "ask"  — normal confirm flow (default)
					toolCtx := tctx
					permLevel, _ := tctx.Value(tool.PermissionLevelKey{}).(string)
					if permLevel == "" {
						permLevel = tool.PermissionAsk
					}
					bypass := a.bypassOnce.Swap(false)
					sandboxActive := a.sandbox != nil && !bypass && permLevel != tool.PermissionFull
					if sandboxActive {
						toolCtx = tool.WithSandbox(tctx, a.sandbox)

						// Confirm-check for dangerous tools.
						// If the sandbox returns Confirm, pause and
						// wait for user approval before executing
						// (unless permission level is "auto").
						//
						// 2026-07: the confirm branch now covers all
						// I/O-bearing tools (exec_command, write_file,
						// read_file, read_docx, read_pdf, list_files)
						// — the read tools went through unchanged
						// before, which let an LLM that had a write
						// confirm approved follow up with read_file on
						// /etc/passwd. The path-class check (project /
						// global / external) drives the Confirm vs
						// Allow decision via CheckReadDecision /
						// CheckWriteDecision.
						cfmTarget, ok := confirmTargetFor(tc.Name, tc.ArgsJSON, req.ProjectRoot, a.sandbox)
						if ok {
							decision, reason, resolvedPath := cfmTarget.Decision, cfmTarget.Reason, cfmTarget.ResolvedPath
							if decision == tool.SandboxConfirm {
								if permLevel == tool.PermissionAuto {
									// Auto-approve: skip confirm modal,
									// emit a brief system notification
									// instead. Use Phase (not Content)
									// so the message isn't persisted into
									// the assistant turn's text — on
									// reload the user would otherwise see
									// "🔓 [自动通过]" mixed in with the
									// LLM's actual reply.
									select {
									case eventCh <- ChatStreamChunk{
										Phase:   "system",
										Message: fmt.Sprintf("🔓 [自动通过] %s", tc.Name),
									}:
									default:
									}
								} else {
									// Normal confirm flow.
									cfm := tool.ConfirmRequest{
										ToolName:     tc.Name,
										Args:         tc.ArgsJSON,
										Reason:       reason,
										ResolvedPath: resolvedPath,
										PathClass:    cfmTarget.PathClass,
										RiskLevel:    cfmTarget.RiskLevel,
									}
									var sessionID string
									if sid, ok := tctx.Value(tool.SessionIDKey{}).(string); ok {
										sessionID = sid
									}
									// Blocking send with a small timeout. The
									// previous `default:` silently dropped
									// the confirm event when the consumer
									// was slow, and the user was left
									// waiting for a UI that never
									// appeared. If the consumer is gone
									// (ctx cancelled), bail out.
									select {
									case eventCh <- ChatStreamChunk{ToolConfirmJSON: tool.MarshalConfirm(cfm)}:
									case <-time.After(5 * time.Second):
										log.Printf("%s[agent] WARN: ToolConfirmJSON send timed out after 5s; the user may not see the prompt (session=%s)", trace.LogPrefix(toolCtx), sessionID)
									case <-toolCtx.Done():
										return
									}
									approved, cfmErr := tool.WaitForConfirm(toolCtx, sessionID, cfm)
									if cfmErr != nil || !approved {
										outcomes[i] = toolOutcome{
											idx: i,
											tc:  tc,
											result: &tool.CallResult{
												Content: "工具调用被用户拒绝",
												IsError: true,
											},
										}
										return
									}
								}
							}
						}
					}
					// Question persistence: handled entirely
					// via the parts accumulator (partsAcc
					// converts the question event into a
					// `question` part and the answer event
					// updates it in place). The whole
					// interaction lives inside the
					// assistant message's persisted parts
					// snapshot — no separate DB rows, no
					// pairQuestionMessages on reload.
					// The handler still BLOCKS here for the
					// user to answer (the result carries
					// {"questions":..., "answers":...}).
					result, err := handler(toolCtx, argsRaw)
					outcomes[i] = toolOutcome{
						idx:     i,
						tc:      tc,
						result:  result,
						err:     err,
						elapsed: time.Since(toolStart),
					}
				}(i, tc)
			}
			// Wait for all tool goroutines to finish, then wait for all
			// forwarders to drain. This ensures per-tool events are
			// emitted in order before the result events below.
			wg.Wait()
			for _, f := range forwarders {
				<-f.done
			}

			// Emit completion events in the original order so the UI shows
			// results in the same order as the calls.
			for i := range outcomes {
				o := &outcomes[i]
				tc := o.tc
				toolElapsed := formatElapsed(o.elapsed)
				argsPreview := tc.ArgsJSON
				if len(argsPreview) > 200 {
					argsPreview = argsPreview[:200] + "..."
				}

				if o.err != nil || o.result == nil {
					roundAnyToolErrored = true
					errMsg := "unknown error"
					if o.result != nil {
						errMsg = o.result.Content
					} else if o.err != nil {
						errMsg = o.err.Error()
					}
					errChunk := ChatStreamChunk{Phase: "tool", Step: fmt.Sprintf("call-%d-err", i+1), Message: fmt.Sprintf("     X %s 执行失败 (%s): %s", tc.Name, toolElapsed, errMsg), ToolID: tc.ID, ToolName: tc.Name, ToolError: errMsg, ToolElapsed: toolElapsed, Round: roundNum, MaxRound: maxRounds}
					partsAcc.update(errChunk)
					sendOrDrop(ctx, ch, nextSeq, errChunk)
					toolMsg := llm.ChatMessage{
						Role:      llm.RoleTool,
						Type:      llm.TypeToolResult,
						Content:   fmt.Sprintf("Tool %s failed: %s", tc.Name, errMsg),
						ToolID:    tc.ID,
						ToolName:  tc.Name,
						ToolError: true,
						// See comment on the success path for
						// why MsgType must be set explicitly.
						MsgType:     llm.MsgTypeTool,
						SubmitToLLM: 1,
					}
					msgs = append(msgs, toolMsg)
					if a.store != nil {
						a.store.AddChatMessageTo(req.SessionID, toolMsg)
					}
					continue
				}

				result := o.result
				resultPreview := result.Content
				if len(resultPreview) > 300 {
					resultPreview = resultPreview[:300] + "..."
				}
				resultPreview = strings.Map(func(r rune) rune {
					if r == '\n' || r == '\r' {
						return ' '
					}
					return r
				}, resultPreview)

				if result.IsError {
					roundAnyToolErrored = true
					warnChunk := ChatStreamChunk{Phase: "tool", Step: fmt.Sprintf("call-%d-warn", i+1), Message: fmt.Sprintf("     ! %s 返回错误 (%s)", tc.Name, toolElapsed), ToolID: tc.ID, ToolName: tc.Name, ToolResult: resultPreview, ToolError: "tool returned error", ToolElapsed: toolElapsed, Round: roundNum, MaxRound: maxRounds}
					partsAcc.update(warnChunk)
					sendOrDrop(ctx, ch, nextSeq, warnChunk)
				} else {
					okChunk := ChatStreamChunk{Phase: "tool", Step: fmt.Sprintf("call-%d-ok", i+1), Message: fmt.Sprintf("     ok %s 完成 (%s, %d 字节)", tc.Name, toolElapsed, len(result.Content)), ToolID: tc.ID, ToolName: tc.Name, ToolResult: resultPreview, ToolElapsed: toolElapsed, Round: roundNum, MaxRound: maxRounds}
					// For tools whose result the frontend needs to
					// *parse* (todo_write), also send the untruncated
					// payload. Truncated newlines → spaces and the 300
					// char cap both corrupt JSON. The frontend uses
					// ToolResultFull in preference to ToolResult when
					// present.
					//
					// Tools that set RawFull (e.g. browser_screenshot)
					// carry large payloads (base64) that must NOT enter
					// the LLM context. RawFull is frontend-only.
					if result.RawFull != "" {
						okChunk.ToolResultFull = result.RawFull
					} else if tc.Name == "todo_write" || tc.Name == "question" {
						okChunk.ToolResultFull = result.Content
					}
					partsAcc.update(okChunk)
					sendOrDrop(ctx, ch, nextSeq, okChunk)
				}

			llmContent := result.Content
			if result.IsError {
				// The structured IsError flag on the
				// ChatMessage is what tells the LLM this
				// is an error; the content is the
				// diagnostic text. Keep it terse and
				// factual — opencode-style. Don't
				// hand-hold the model with "请分析错误
				// 原因后调整方案并重试" boilerplate,
				// and never instruct it to fabricate
				// user-facing error messages.
				llmContent = fmt.Sprintf("Tool %s returned an error: %s", tc.Name, result.Content)
			} else {
				llmContent = a.truncateToolResult(tc.Name, result.Content)
			}
				toolMsg := llm.ChatMessage{
					Role:      llm.RoleTool,
					Type:      llm.TypeToolResult,
					Content:   llmContent,
					ToolID:    tc.ID,
					ToolName:  tc.Name,
					ToolError: result.IsError,
					// MsgType must be set so the read path
					// (buildMessageResponse in handler.go)
					// can drop standalone tool_result rows.
					// Without it, the row's msg_type column
					// defaults to 0 (msg_type for plain
					// text), the `if m.MsgType ==
					// llm.MsgTypeTool` filter never fires,
					// and the tool's raw JSON Content (e.g.
					// the question tool's `{questions,
					// answers}` payload) leaks into the
					// chat as a free-floating text bubble.
					// Surfaces after rollback/undo because
					// the in-memory splice restores the
					// unfiltered row.
					MsgType:     llm.MsgTypeTool,
					SubmitToLLM: 1,
				}
				msgs = append(msgs, toolMsg)
				if a.store != nil {
					a.store.AddChatMessageTo(req.SessionID, toolMsg)
				}
			}
			// Inject vision images: after all tool_result messages
			// have been appended, collect any tool that produced
			// an image payload (e.g. browser_screenshot) and
			// append them as separate role=user, type=image
			// ChatMessages. The LLM then receives the images as
			// proper vision inputs (image_url / image blocks) in
			// the next round, instead of seeing a text placeholder
			// in the tool_result. This mirrors how user-uploaded
			// attachments are handled via ExpandAttachmentsCM.
			//
			// Each image is persisted as its own row so it
			// survives reload / rollback and appears as a
			// standalone image bubble in the chat history. The
			// base64 is kept verbatim in the LLM context so the
			// model can still see the image on every round; the
			// overall context size is managed by tryAutoCompact.
			//
			// When the model doesn't support vision, skip the
			// injection entirely — the tool_result's text
			// placeholder is all the LLM will see.
			visionCapable := a.modelSupportsVision(req.Provider, req.Model)
			for _, o := range outcomes {
				if o.result == nil || o.result.Image == nil || !visionCapable {
					continue
				}
				img := o.result.Image
				imgMsg := llm.ChatMessage{
					Role:        llm.RoleUser,
					Type:        llm.TypeImage,
					Content:     img.Data,
					MimeType:    img.MIMEType,
					Name:        img.Name,
					MsgType:     llm.MsgTypeImage,
					SubmitToLLM: 1,
				}
				msgs = append(msgs, imgMsg)
				if a.store != nil {
					a.store.AddChatMessageTo(req.SessionID, imgMsg)
				}
			}
			// Persist assistant message now that tool
			// results are captured in partsAcc.
			persistAssistant(req.SessionID, a.store, assistantMsg, fullThinking, partsAcc, totalIn, totalOut, req.RegenGroupID)

			// Stuck-loop guard. Compute a stable signature of
			// this round's tool calls and whether any errored.
			// If the same signature repeats for stuckThreshold
			// consecutive rounds AND the last round errored,
			// break out with a "stuck" event — the LLM is
			// clearly not making progress, and the opencode
			// TODO in `llm.ts:54` calls this out as unchecked
			// work.
			curSig := toolCallSignature(toolCalls)
			curErrored := roundAnyToolErrored
			if curSig != "" && curSig == prevToolSig && curErrored && prevErrored {
				stuckStreak++
			} else {
				stuckStreak = 0
			}
			prevToolSig = curSig
			prevErrored = curErrored
			if stuckStreak >= stuckThreshold {
				sendOrDrop(ctx, ch, nextSeq, ChatStreamChunk{
					Phase:   "stuck",
					Step:    "stuck-loop",
					Message: fmt.Sprintf("已连续 %d 轮以相同的工具调用失败，疑似陷入循环。自动停止。", stuckStreak+1),
					Round:   roundNum,
					MaxRound: maxRounds,
					TokensIn: totalIn, TokensOut: totalOut,
				})
				sendOrDrop(ctx, ch, nextSeq, ChatStreamChunk{Done: true})
				return
			}

			// Same-tool-name error counter: if a single tool
			// errors N times in a row (even with different
			// args — the stuck-loop guard only catches
			// identical-args loops), inject a system message
			// telling the LLM to stop retrying.
			if roundAnyToolErrored && len(toolCalls) == 1 {
				name := toolCalls[0].Name
				if name == sameToolErrName {
					sameToolErrCount++
				} else {
					sameToolErrName = name
					sameToolErrCount = 1
				}
			} else {
				sameToolErrName = ""
				sameToolErrCount = 0
			}
			if sameToolErrCount >= sameToolErrMax {
				// P2-1: reset the stuck-loop guard too.
				// Without this, if the LLM ignores the
				// "改用其他方式" hint and keeps calling
				// the same failing tool, the stuck guard
				// (which fires BEFORE this block in the
				// round order) can pre-empt the hint and
				// exit the loop with no message at all —
				// the user just sees "stuck-loop detected"
				// without ever learning that the LLM was
				// told to switch tools. Reseting here
				// gives the LLM a fresh stuck budget
				// after a strong, explicit intervention.
				//
				// Format the messages BEFORE resetting the
				// counters — otherwise the "已连续 N 次"
				// string would render "已连续 0 次".
				systemMsg := fmt.Sprintf("工具 `%s` 已连续失败 %d 次。不要重试 — 改用其他方式完成任务（如 read_file、list_files、或 task 子代理）。", sameToolErrName, sameToolErrCount)
				statusMsg := fmt.Sprintf("%s 已连续失败 %d 次，改用其他方式。", sameToolErrName, sameToolErrCount)
				resetGuardCounters(&stuckStreak, &prevToolSig, &prevErrored)
				resetSameToolErr(&sameToolErrName, &sameToolErrCount)
				msgs = append(msgs, llm.ChatMessage{
					Role:    llm.RoleSystem,
					Type:    llm.TypeText,
					Content: systemMsg,
				})
				sendOrDrop(ctx, ch, nextSeq, ChatStreamChunk{
					Phase:   "limit",
					Step:    "same-tool-err-limit",
					Message: statusMsg,
					Round:   roundNum,
				})
			}

			// Tool result pruning: remove old tool output content
			// after N rounds to keep the context lean. Protects
			// the most recent rounds so the LLM still sees
			// immediately-relevant results.
			pr := pruneAfterRounds
			if a.cfg != nil && a.cfg.Limits.PruneAfterRounds > 0 {
				pr = a.cfg.Limits.PruneAfterRounds
			}
			pruneOldToolResults(msgs, roundNum, pr)
		}

		if maxRounds > 0 {
			sendOrDrop(ctx, ch, nextSeq, ChatStreamChunk{Phase: "limit", Step: "max-rounds", Message: fmt.Sprintf("已达到 %d 轮上限 (总耗时 %s)。LLM 已强制给出文本总结。", maxRounds, formatElapsed(time.Since(start))), Round: maxRounds, MaxRound: maxRounds, TokensIn: totalIn, TokensOut: totalOut})
		}
		sendOrDrop(ctx, ch, nextSeq, ChatStreamChunk{Done: true})
	}()

	return ch
}

func persistAssistant(convID string, store *memory.Store, msg llm.ChatMessage, fullThinking string, partsAcc *partsAccumulator, tokensIn int, tokensOut int, regenGroupID string) {
	if store == nil {
		return
	}
	// P1-4: every assistant row needs a regen_group_id so
	// a later "重答" click can find the regen group and
	// archive the existing siblings. The Regenerate
	// handler sets req.RegenGroupID explicitly; the
	// SendMessage handler does NOT (the assistant's
	// triggering user message id isn't known until the
	// agent inserts it). In the SendMessage path, fall
	// back to the most recent user message in the
	// conversation — that's the row that prompted this
	// reply by definition. The lookup is a single
	// indexed MAX(id) query (see
	// memory.GetLastUserMessageID).
	if regenGroupID == "" {
		if uid := store.GetLastUserMessageID(convID); uid > 0 {
			regenGroupID = strconv.FormatInt(uid, 10)
		}
	}
	meta := map[string]string{
		"role":   msg.Role,
		"reason": "assistant",
	}
	// `thinking` is the v1 metadata key. It's redundant
	// with the v2 path below (snapshotStructural includes
	// thinking parts in the meta["parts"] blob), but the
	// server's decodePartsFromMeta v1 fallback still
	// reads `meta["thinking"]` to rebuild a thinking
	// part when the parts blob is structural-only. Older
	// rows that were persisted before the v2 parts
	// snapshot landed in the agent (commit 8a16a69) have
	// `meta["thinking"]` populated and a structural-only
	// parts blob (or none at all) — the v1 fallback is
	// what makes their reload view correct. Keep the
	// write; without it, a session that started under v1
	// and continued under v2 could lose thinking on
	// reload if any intermediate change re-emits the row
	// through the structural-only path.
	if fullThinking != "" {
		meta["thinking"] = fullThinking
	}
	if tokensIn > 0 {
		meta["tokens_in"] = fmt.Sprintf("%d", tokensIn)
	}
	if tokensOut > 0 {
		meta["tokens_out"] = fmt.Sprintf("%d", tokensOut)
	}
	structural := snapshotStructural(partsAcc)
	if len(structural) > 0 {
		if pj, pjErr := json.Marshal(structural); pjErr == nil {
			meta["parts"] = string(pj)
		}
	}
	store.AddChatMessageWithMetaToRegen(convID, msg, meta, regenGroupID, false)
}

// buildToolHint generates a minimal markdown-block fallback instruction
// for models that don't support native OpenAI tool_calls. The personality-
// specific tool calling style is already documented in the soul/*.md file,
// so this section is intentionally tiny (to maximize the prefix cache hit
// on the system prompt).
func buildToolHint(tools []tool.Tool) string {
	if len(tools) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Tool Fallback\n\n")
	b.WriteString("If native tool_calls is unavailable, emit a single ```tool_call``` block:\n\n")
	b.WriteString("```tool_call\n")
	b.WriteString(`{"name": "<name>", "arguments": {<json>}}`)
	b.WriteString("\n```\n")
	return b.String()
}

func availableToolNames(tools []tool.Tool) string {
	names := make([]string, 0, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
	}
	return strings.Join(names, ", ")
}

// toolCallSignature returns a stable, sorted string
// representing the (name, args) of every tool call in the
// round. Used by the stuck-loop guard to detect the LLM
// hammering the same failing call. Returns "" if there are
// no tool calls (a "no progress" round is not a stuck round —
// the LLM may have answered with text).
// tryAutoCompact checks the message list's estimated token count
// against the usable context window. If exceeded and a summarizer is
// available, it compresses the oldest messages, backfills the summary
// into the system prompt, emits a status chunk, and returns true
// ("caller should continue to the next round"). Returns false when no
// compaction is needed or no summarizer is wired.
func (a *Agent) tryAutoCompact(
	ctx context.Context,
	msgs *[]llm.ChatMessage,
	req ChatRequest,
	ch chan<- ChatStreamChunk,
	nextSeq func() uint64,
	roundNum, maxRounds int,
) bool {
	if a.summarizer == nil || a.store == nil || req.SessionID == "" {
		return false
	}
	total := llm.EstimateTokensMessages(*msgs)
	ctxWindow := a.llm.ContextWindow(req.Provider, req.Model)
	buf := llm.AutoCompactBuffer
	if a.cfg != nil && a.cfg.Limits.AutoCompactBuffer > 0 {
		buf = a.cfg.Limits.AutoCompactBuffer
	}
	if !llm.ShouldCompactWithBuf(total, ctxWindow, buf) {
		return false
	}

	sendOrDrop(ctx, ch, nextSeq, ChatStreamChunk{
		Phase:    "compact",
		Step:     "auto-compact",
		Message:  fmt.Sprintf("上下文接近上限 (≈%d / %d tokens)，自动压缩历史…", total, llm.UsableContextWithBuf(ctxWindow, buf)+buf),
		Round:    roundNum,
		MaxRound: maxRounds,
	})

	ok, summary, err := a.summarizer.Compress(ctx, req.SessionID)
	if err != nil || !ok {
		sendOrDrop(ctx, ch, nextSeq, ChatStreamChunk{
			Phase:   "compact",
			Step:    "auto-compact-fail",
			Message: fmt.Sprintf("自动压缩失败: %v", err),
			Round:   roundNum,
			MaxRound: maxRounds,
		})
		// Fallback: hard-truncate the message list so the LLM
		// call doesn't fail with a 413. Drop oldest non-system
		// messages to stay within the usable context window.
		usable := llm.UsableContextWithBuf(ctxWindow, buf)
		if usable > 0 {
			truncateToFit(msgs, usable)
			sendOrDrop(ctx, ch, nextSeq, ChatStreamChunk{
				Phase:    "compact",
				Step:     "auto-compact-fallback",
				Message:  fmt.Sprintf("压缩失败，已截断上下文至 ≈%d tokens", llm.EstimateTokensMessages(*msgs)),
				Round:    roundNum,
				MaxRound: maxRounds,
			})
			_ = summary
			return true
		}
		return false
	}

	sendOrDrop(ctx, ch, nextSeq, ChatStreamChunk{
		Phase:   "compact",
		Step:    "auto-compact-ok",
		Message: "上下文已压缩，继续执行…",
		Round:   roundNum,
		MaxRound: maxRounds,
	})

	// Backfill: reload messages after compression point and
	// append the summary to the system prompt.
	lastComp := a.store.LastCompressedIDFor(req.SessionID)
	if lastComp > 0 {
		hist, _, _ := a.store.GetChatMessagesAfterIDFor(req.SessionID, 0, lastComp)
		compSum := a.store.CompressedSummaryFor(req.SessionID)

		newMsgs := make([]llm.ChatMessage, 0, len(hist)+2)
		// Keep the system prompt (first message).
		newMsgs = append(newMsgs, (*msgs)[0])
		if compSum != "" {
			newMsgs[0].Content += "\n\n[前文摘要]\n" + compSum
		}
		// Append messages from DB (after compression point).
		for _, m := range hist {
			if m.Role == llm.RoleSystem {
				continue
			}
			newMsgs = append(newMsgs, m)
		}
		*msgs = newMsgs
	}

	_ = summary
	return true
}

func toolCallSignature(calls []nativeToolCall) string {
	if len(calls) == 0 {
		return ""
	}
	type pair struct{ name, args string }
	parts := make([]pair, 0, len(calls))
	for _, c := range calls {
		parts = append(parts, pair{c.Name, c.ArgsJSON})
	}
	sort.Slice(parts, func(i, j int) bool {
		if parts[i].name != parts[j].name {
			return parts[i].name < parts[j].name
		}
		return parts[i].args < parts[j].args
	})
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(p.name)
		b.WriteByte('|')
		b.WriteString(p.args)
		b.WriteByte(';')
	}
	return b.String()
}

// MaxStepsPrompt is injected as a fake assistant message right
// before the LLM's final allowed turn when the agent loop
// reaches its round cap. It explicitly forbids further tool
// calls and forces a text-only summary.
//
// Pattern ported from opencode's `runner/max-steps.ts:1-16`.
// The corresponding LLM-side change is to drop the `tools` field
// from the request on the last round, so the model physically
// cannot emit a tool_call (it tries, the adapter returns no
// tool blocks, and the model is forced into text-only mode).
//
// P2-2: split into EN + ZH variants and pick by the per-call
// `lang` (a.cfg.LLM.Output.Language). The original single
// English version had a self-contradicting "respond in the
// same language as the conversation" line — fine for English
// users, but Chinese users got an English prompt that the LLM
// then had to translate, often incompletely. Selecting by
// language keeps the strict "no tool calls" instruction
// intact while speaking the LLM's expected reply language.
const MaxStepsPromptEN = `CRITICAL - MAXIMUM STEPS REACHED

The maximum number of steps allowed for this task has been reached. Tools are disabled until next user input. Respond with text only.

STRICT REQUIREMENTS:
1. Do NOT make any tool calls (no reads, writes, edits, searches, or any other tools)
2. MUST provide a text response summarizing work done so far
3. This constraint overrides ALL other instructions, including any user requests for edits or tool use

Response must include:
- Statement that maximum steps for this agent have been reached
- Summary of what has been accomplished so far
- List of any remaining tasks that were not completed
- Recommendations for what should be done next

Respond in the same language as the conversation. Any attempt to use tools is a critical violation. Respond with text ONLY.`

// MaxStepsPromptZH is the Chinese counterpart. Same shape as
// the EN version; the "STRICT REQUIREMENTS" voice stays
// uncompromising in translation. "auto"/"" default falls back
// to EN (see pickMaxStepsPrompt) so the opencode rule of
// "follow the conversation language" still applies at the
// LLM output level, just not at the prompt level.
const MaxStepsPromptZH = `⚠ 已达到本任务的最大步数上限

工具已禁用（直到下次用户输入）。请只用文本回复。

严格要求：
1. 禁止调用任何工具（read_file / write_file / exec_command / search 等都不行）
2. 必须用文本总结目前为止完成的工作
3. 本约束覆盖所有其他指令，包括用户对编辑或工具使用的请求

回复必须包含：
- 说明已达到本 Agent 的最大步数
- 总结已完成的工作
- 列出未完成的任务
- 建议下一步该做什么

请用与对话相同的语言回复。任何尝试使用工具的行为都是严重违规。只能文本回复。`

// pickMaxStepsPrompt returns the language-appropriate variant
// of the max-steps prompt. "zh" → ZH, anything else (including
// "auto", "en", "") → EN. Defaulting non-zh to EN keeps the
// prompt stable across the various "follow the conversation
// language" configs and avoids the previous contradiction
// where the prompt asked the LLM to match a language it was
// itself being delivered in.
