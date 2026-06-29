package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/p-chat/pchat/internal/agents"
	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/memory"
	"github.com/p-chat/pchat/internal/paths"
	"github.com/p-chat/pchat/internal/rules"
	"github.com/p-chat/pchat/internal/skill"
	"github.com/p-chat/pchat/internal/style"
	"github.com/p-chat/pchat/internal/tool"
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
	bypassOnce atomic.Bool

	// Cached static system prompt (style + AGENTS + rules + skills + tool hint).
	// Keyed by (style, available-tools-hash). Invalidated by Reload() or when
	// the user changes style. This is the part that's identical across all
	// rounds of a single chat AND between chats within the same
	// session, so the LLM's prefix cache hits on it.
	staticPrompt   string
	staticPromptID string // signature used to detect when to rebuild
}

// SetChatOptions overrides the per-request sampling parameters
// (temperature, top_p, max_tokens). Pass an empty struct to reset to
// the underlying API defaults.
func (a *Agent) SetChatOptions(opts llm.ChatOptions) {
	a.options = opts
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
		return true
	}
	for _, p := range a.cfg.LLM.Providers {
		if p.Name != providerName {
			continue
		}
		for _, m := range p.Models {
			if m.Name == modelName {
				// Explicit opt-out: capabilities.supports_vision = false.
				// Capabilities is a struct (not a pointer), so an absent
				// value zero-fills the field; the only way to land in
				// the `return false` branch is to have explicitly set
				// supports_vision: false in the config (or via the
				// model editor in the UI).
				return m.Capabilities.SupportsVision
			}
		}
		// Provider found, model not in the configured list: permissive
		// default. The API itself will reject if the model is genuinely
		// non-vision, and the agent will surface the classified error
		// to the user.
		return true
	}
	// Provider not found in config: permissive default.
	return true
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
	// MaxRounds caps the ReAct tool-use loop. 0 means default (50).
	// After MaxRounds the loop stops and the user can continue
	// with a follow-up message.
	MaxRounds int `json:"max_rounds,omitempty"`
}

type ChatStreamChunk struct {
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

	ToolName    string `json:"tool_name,omitempty"`
	ToolArgs    string `json:"tool_args,omitempty"`
	ToolResult  string `json:"tool_result,omitempty"`
	ToolError   string `json:"tool_error,omitempty"`
	ToolElapsed string `json:"tool_elapsed,omitempty"`

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
func (a *Agent) buildStaticSystemPrompt(s style.Style, toolDefs []llm.ToolDef, projectRoot string) (string, string, error) {
	toolNames := make([]string, 0, len(toolDefs))
	for _, t := range toolDefs {
		toolNames = append(toolNames, t.Name)
	}
	lang := ""
	if a.cfg != nil {
		lang = a.cfg.LLM.Output.Language
	}
	sig := strings.Join([]string{
		string(s),
		agentsSignatureWithRoot(projectRoot),
		rulesSignature(a.rules),
		skillSignature(a.skills),
		strings.Join(toolNames, ","),
		lang,
		projectRoot,
	}, "|")
	if sig == a.staticPromptID && a.staticPrompt != "" {
		return a.staticPrompt, sig, nil
	}

	var sb strings.Builder

	// 1. Style (identity + soul) — short, stable per session.
	stylePrompt, err := a.styleMgr.GetSystemPrompt(s)
	if err != nil {
		return "", sig, err
	}
	sb.WriteString(stylePrompt)
	sb.WriteString("\n\n---\n\n")

	// 2. AGENTS instructions — stable until files change.
	sb.WriteString(agents.LoadAllWithRoot(projectRoot))
	sb.WriteString("\n---\n\n")

	// 3. Rules — stable until files change.
	sb.WriteString(rules.BuildRulesContext(a.rules))
	sb.WriteString("\n---\n\n")

	// 4. Skills — stable until files change.
	sb.WriteString(skill.BuildSkillContext(a.skills))
	sb.WriteString("\n---\n\n")

	// 5. Tool hint — stable per session (tools don't change at runtime).
	if len(toolDefs) > 0 {
		sb.WriteString(buildToolHint(a.tools.List()))
		// Encourage the LLM to consult its knowledge base before
		// answering questions it might not know. The `recall` tool
		// is added by the CLI at startup; here we just remind the
		// model to use it.
		hasRecall := false
		for _, t := range a.tools.List() {
			if t.Name == "recall" {
				hasRecall = true
				break
			}
		}
		if hasRecall {
			sb.WriteString("\n\n---\n\n## Using recall\n\n" +
				"当你不确定某条信息、需要查代码/文档、或想引用历史对话时，\n" +
				"先用 `recall(query=\"...\")` 工具查一下知识库/历史。\n" +
				"不要凭印象编造 API 名称、文件路径、函数签名。\n")
		}
		// Remind the LLM that uploaded images arrive as vision
		// input in the user message (image_url content parts),
		// NOT as files on disk. Calling read_file on an uploaded
		// image is pointless and produces confusing error
		// messages; the model should just look at the image
		// it was given.
		sb.WriteString("\n\n---\n\n## Uploaded Attachments\n\n" +
			"用户上传的图片/文件以 image_url (data URL) 或文本块的形式\n" +
			"直接包含在 user message 的 content 数组中，你已经能看到了。\n" +
			"绝对不要对上传的图片调用 read_file —— 那是磁盘上的临时文件，\n" +
			"read_file 工具只处理文本文件，对图片会返回 binary 错误。\n" +
			"read_file 报错 === read_file 工具本身的限制，\n" +
			"与「模型不支持图片」完全无关 —— 你已经收到了图片，\n" +
			"直接基于图片内容回答即可，不要向用户转述 read_file 错误，\n" +
			"更不要伪造「ERROR: ... Inform the user.」之类的\n" +
			"用户可见错误信息。\n")
	}

	// 6. Project root — tells the LLM which directory to use
	// as CWD for exec_command and file operations.
	if projectRoot != "" {
		sb.WriteString(fmt.Sprintf("\n---\n\n## 项目目录\n\n你的工作目录已固定为 `%s`。\n"+
			"exec_command 不传 work_dir——已自动使用此目录，\n"+
			"传了 work_dir 也不会生效。\n"+
			"read_file/write_file 的相对路径以此目录为基准。\n", projectRoot))
	}

	// 7. Output language hint — also part of the cacheable prefix
	// because changing it forces a full re-build anyway.
	if lang == "zh" {
		sb.WriteString("\n---\n\n## 输出语言\n\n请用简体中文回答用户的问题。\n")
	} else if lang == "en" {
		sb.WriteString("\n---\n\n## Output Language\n\nPlease answer in English.\n")
	} else if lang == "auto" {
		sb.WriteString("\n---\n\n## Output Language\n\nAuto-detect the user's language from their input and respond in the same language.\n")
	}

	prompt := sb.String()
	a.staticPrompt = prompt
	a.staticPromptID = sig
	return prompt, sig, nil
}

// Reload forces the next call to rebuild the static system prompt
// (e.g. after the user changes AGENTS.md or installs a new skill).
func (a *Agent) Reload() {
	skills, _ := skill.LoadAll()
	rulesList, _ := rules.LoadAll()
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
		p, _ := os.Stat(paths.ProjectAgentsWithRoot(root))
		return fileSig(g) + "|" + fileSig(p) + "|" + root
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

// ChatStream is a single-turn chat with no tool support. For multi-turn
// ReAct with tool use, use ChatWithTools.
func (a *Agent) ChatStream(ctx context.Context, req ChatRequest) <-chan ChatStreamChunk {
	return a.ChatWithTools(ctx, req)
}

// ChatWithTools performs a ReAct-style loop: send messages to the LLM with
// available tools, execute any tool calls, and feed results back to the LLM
// until it gives a final answer.
func (a *Agent) ChatWithTools(ctx context.Context, req ChatRequest) <-chan ChatStreamChunk {
	ch := make(chan ChatStreamChunk, 64)

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
		// Recover from any panic inside the goroutine so a malformed
		// LLM response or a buggy tool handler doesn't kill the whole
		// REPL. The panic stack trace is sent as a final Error chunk.
		defer func() {
			if r := recover(); r != nil {
				stack := debug.Stack()
				ch <- ChatStreamChunk{
					Phase: "system",
					Error: fmt.Sprintf("panic in agent: %v\n\n%s", r, stack),
					Done:  true,
				}
			}
		}()
		start := time.Now()

		ch <- ChatStreamChunk{Phase: "system", Step: "load-tools", Message: "加载工具列表..."}
		availableTools := a.tools.List()
		toolDefs := llm.ToolsFromRegistryDef(availableTools)
		if len(toolDefs) > 0 {
			names := make([]string, 0, len(availableTools))
			for _, t := range availableTools {
				names = append(names, t.Name)
			}
			ch <- ChatStreamChunk{Phase: "tools", Step: "tools", Message: fmt.Sprintf("可用工具 (%d): %s", len(availableTools), strings.Join(names, ", "))}
		} else {
			ch <- ChatStreamChunk{Phase: "tools", Step: "tools", Message: "未注册任何工具"}
			toolDefs = nil
		}

		ch <- ChatStreamChunk{Phase: "system", Step: "load-system", Message: "合并风格 / AGENTS.md / 规则 / 技能..."}
		systemPrompt, _, err := a.buildStaticSystemPrompt(req.Style, toolDefs, req.ProjectRoot)
		if err != nil {
			ch <- ChatStreamChunk{Phase: "system", Error: err.Error(), Done: true}
			return
		}
		ch <- ChatStreamChunk{Phase: "system", Step: "ok", Message: fmt.Sprintf("系统提示已就绪 (%d 字符)", len(systemPrompt)), Duration: formatElapsed(time.Since(start))}

		// Append compressed summary if provided (from /compress).
		if req.CompressedSummary != "" {
			systemPrompt += "\n\n[前文摘要]\n" + req.CompressedSummary
		}
		// Append active skill context (from /skillname slash command).
		if req.SkillContext != "" {
			systemPrompt += "\n\n---\n\n## 激活的技能上下文\n\n" + req.SkillContext + "\n"
		}

		// Build the message list: system prompt + user messages.
		// Each message is a separate protocol-agnostic ChatMessage.
		msgs := []llm.ChatMessage{
			{Role: llm.RoleSystem, Type: llm.TypeText, Content: systemPrompt},
		}
		msgs = append(msgs, req.Messages...)

		// Expand any user-uploaded attachments into separate
		// ChatMessage entries (text msg + image/file msgs).
		if len(req.Attachments) > 0 && a.attach != nil {
			protocol := a.protocolFor(req.Provider)
			vision := func() bool { return a.modelSupportsVision(req.Provider, req.Model) }
			msgs = ExpandAttachmentsCM(protocol, msgs, req.Attachments, a.attach, vision)
			ch <- ChatStreamChunk{Phase: "system", Step: "attachments", Message: fmt.Sprintf("展开 %d 个附件", len(req.Attachments))}
		}

		if a.store != nil {
			ch <- ChatStreamChunk{Phase: "memory", Step: "memory", Message: fmt.Sprintf("写入消息到记忆")}
			// Persist all user-facing messages (including
			// image attachments as separate rows).
			for _, m := range msgs {
				if m.Role == llm.RoleSystem {
					continue
				}
				a.store.AddChatMessage(m)
			}
		}

		// Plan mode: tell the LLM to produce a step-by-step plan in
		// pure text (no tool calls). The user will review the plan
		// before actually executing it.
		maxRounds := 0 // 0 = unlimited; >0 = capped
		if req.PlanMode {
			toolDefs = nil
			msgs[0].Content += "\n\n---\n\n## Plan Mode\n\n" +
				"你正在 PLAN MODE：不要调用任何工具。\n" +
				"请用纯文本给出 step-by-step 执行计划：\n" +
				"1. 每一步做什么\n" +
				"2. 每一步预期使用什么工具 (read_file, write_file, exec_command, list_files, task 等)\n" +
				"3. 每一步的预期结果\n" +
				"4. 风险 / 依赖 / 边界\n" +
				"用户审阅后会用 y/n/e 决定是否执行。\n"
			maxRounds = 1
			ch <- ChatStreamChunk{Phase: "plan", Step: "plan-mode", Message: "Plan Mode 启用 (单轮纯文本，无工具调用)"}
		} else {
			ch <- ChatStreamChunk{Phase: "plan", Step: "plan", Message: "构建模式 — LLM 自主决定何时终止"}
		}

		var totalIn, totalOut int

		for round := 1; maxRounds == 0 || round <= maxRounds; round++ {
			roundStart := time.Now()
			roundNum := round
			partsAcc = newPartsAccumulator()

			ch <- ChatStreamChunk{Phase: "llm", Step: fmt.Sprintf("round-%d", roundNum), Message: fmt.Sprintf("[第 %d 轮] 调用 LLM", roundNum), Round: roundNum, MaxRound: maxRounds}

			var (
				fullContent  string
				fullThinking string
				toolCalls    []nativeToolCall
				argsAccum    = make(map[int]*nativeToolCall)
			)

			opts := a.options
			if req.ReasoningEffort != "" {
				opts.ReasoningEffort = req.ReasoningEffort
			}
			stream := a.llm.ChatStreamCM(ctx, req.Provider, req.Model, normalizeToolResults(msgs), toolDefs, opts)
			for chunk := range stream {
				if chunk.Err != nil {
					classified := llm.ClassifyAPIError(req.Provider, chunk.Err)
					errMsg, errSuggestion, errKind := chunk.Err.Error(), "", ""
					if apiErr, ok := classified.(*llm.APIError); ok {
						errMsg = apiErr.Message
						errSuggestion = apiErr.Suggestion
						errKind = apiErr.Kind.String()
					}
					ch <- ChatStreamChunk{
						Phase:      "llm",
						Error:      errMsg,
						Suggestion: errSuggestion,
						ErrorKind:  errKind,
						Done:       true,
					}
					return
				}
				if chunk.Done {
					break
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
					ch <- ChatStreamChunk{Content: chunk.Content, TokensIn: totalIn, TokensOut: totalOut}
				}
				if chunk.Thinking != "" {
					fullThinking += chunk.Thinking
					partsAcc.update(ChatStreamChunk{Thinking: chunk.Thinking})
					ch <- ChatStreamChunk{Thinking: chunk.Thinking, TokensIn: totalIn, TokensOut: totalOut}
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
			}
			for _, t := range argsAccum {
				toolCalls = append(toolCalls, *t)
			}

			ch <- ChatStreamChunk{Phase: "llm", Step: fmt.Sprintf("round-%d-done", roundNum), Message: fmt.Sprintf("[第 %d 轮] 模型响应: %d 字符 / 耗时 %s", roundNum, len(fullContent), formatElapsed(time.Since(roundStart))), Round: roundNum, MaxRound: maxRounds, TokensIn: totalIn, TokensOut: totalOut}

		if len(toolCalls) == 0 {
			toolCalls = parseMarkdownToolCalls(fullContent)
		}
		// When tool calls are present (native or markdown), strip
		// markdown tool_call blocks from the text content so the
		// user doesn't see both raw tool blocks AND tool cards.
		if len(toolCalls) > 0 {
			fullContent = cleanMarkdownToolCalls(fullContent)
		}

			// Build the assistant message for the conversation.
			// Emit as a single text ChatMessage (tool calls are
			// separate messages appended below).
			assistantMsg := llm.ChatMessage{
				Role:    llm.RoleAssistant,
				Type:    llm.TypeText,
				Content: fullContent,
			}
			msgs = append(msgs, assistantMsg)

			// Persist assistant message later — after tool
			// results are in partsAcc (see end of this round).

			// Append tool_call messages for each tool call.
			for _, tc := range toolCalls {
				id := tc.ID
				if id == "" {
					id = "call_" + uuid.NewString()
				}
				tcm := llm.ChatMessage{
					Role:      llm.RoleAssistant,
					Type:      llm.TypeToolCall,
					ToolID:    id,
					ToolName:  tc.Name,
					ToolInput: tc.ArgsJSON,
				}
				msgs = append(msgs, tcm)
				if a.store != nil {
					a.store.AddChatMessage(tcm)
				}
				tc.ID = id
			}

			if len(toolCalls) == 0 {
				persistAssistant(a.store, assistantMsg, fullThinking, partsAcc)
				ch <- ChatStreamChunk{Phase: "done", Step: "done", Message: fmt.Sprintf("完成 (总耗时 %s, 共 %d 轮)", formatElapsed(time.Since(start)), roundNum), Round: roundNum, MaxRound: maxRounds, TokensIn: totalIn, TokensOut: totalOut}
				ch <- ChatStreamChunk{Done: true}
				return
			}

			// Context window warning: count only meaningful
			// messages (exclude tool_call/tool_result metadata).
			meaningful := countMeaningfulMessages(msgs)
			if meaningful > 120 {
				persistAssistant(a.store, assistantMsg, fullThinking, partsAcc)
				ch <- ChatStreamChunk{Phase: "context_warn", Step: "context-warn", Message: fmt.Sprintf("上下文已达 %d 条有效消息，接近上限，已自动停止。建议执行 /compress 压缩历史后继续。", meaningful), Round: roundNum, MaxRound: maxRounds}
				ch <- ChatStreamChunk{Done: true}
				return
			} else if meaningful > 80 {
				ch <- ChatStreamChunk{Phase: "context_warn", Step: "context-warn", Message: fmt.Sprintf("上下文已达 %d 条有效消息，建议在完成当前任务后执行 /compress 压缩历史。", meaningful), Round: roundNum, MaxRound: maxRounds}
			}

			ch <- ChatStreamChunk{Phase: "tool", Step: fmt.Sprintf("round-%d-tools", roundNum), Message: fmt.Sprintf("[第 %d/%d 轮] 检测到 %d 个工具调用", roundNum, maxRounds, len(toolCalls)), Round: roundNum, MaxRound: maxRounds}

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
				startChunk := ChatStreamChunk{Phase: "tool", Step: fmt.Sprintf("call-%d", i+1), Message: fmt.Sprintf("  -> 工具 %d/%d: %s", i+1, len(toolCalls), tc.Name), ToolName: tc.Name, ToolArgs: argsPreview, Round: roundNum, MaxRound: maxRounds}
				partsAcc.update(startChunk)
				ch <- startChunk
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

				eventCh := make(chan ChatStreamChunk, 16)
			tctx := context.WithValue(ctx, toolEventChanKey{}, eventCh)
				if req.SessionID != "" {
					tctx = tool.WithSessionID(tctx, req.SessionID)
				}
				if req.ProjectRoot != "" {
					tctx = tool.WithProjectRoot(tctx, req.ProjectRoot)
				}
				tctx, cancel := context.WithTimeout(tctx, 5*time.Minute)

				fwd := forwarder{done: make(chan struct{})}
				forwarders = append(forwarders, fwd)
				go func() {
					defer close(fwd.done)
					for ev := range eventCh {
						// Sub-agent / tool events arrive here
						// from the per-tool dispatcher. Feed
						// them into the parts accumulator so
						// nested cards survive a session
						// reload, then forward to the main
						// channel for the live UI.
						partsAcc.update(ev)
						ch <- ev
					}
				}()

				go func(i int, tc nativeToolCall) {
					defer wg.Done()
					defer cancel()
					defer close(eventCh)

					handler, ok := a.tools.Get(tc.Name)
					if !ok {
						errMsg := fmt.Sprintf("error: tool %q not found (available: %s)", tc.Name, availableToolNames(availableTools))
						outcomes[i] = toolOutcome{
							idx:    i,
							tc:     tc,
							result: &tool.CallResult{Content: errMsg, IsError: true},
							err:    fmt.Errorf("not found"),
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
					toolCtx := tctx
					bypass := a.bypassOnce.Swap(false)
					if a.sandbox != nil && !bypass {
						toolCtx = tool.WithSandbox(tctx, a.sandbox)
					}
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
					errMsg := "unknown error"
					if o.result != nil {
						errMsg = o.result.Content
					} else if o.err != nil {
						errMsg = o.err.Error()
					}
					errChunk := ChatStreamChunk{Phase: "tool", Step: fmt.Sprintf("call-%d-err", i+1), Message: fmt.Sprintf("     X %s 执行失败 (%s): %s", tc.Name, toolElapsed, errMsg), ToolName: tc.Name, ToolError: errMsg, ToolElapsed: toolElapsed, Round: roundNum, MaxRound: maxRounds}
					partsAcc.update(errChunk)
					ch <- errChunk
					toolMsg := llm.ChatMessage{
						Role:      llm.RoleTool,
						Type:      llm.TypeToolResult,
						Content:   fmt.Sprintf("error: %s\n\n工具 %s 执行失败。请分析错误原因后调整方案并重试；反复失败请告知用户。", errMsg, tc.Name),
						ToolID:    tc.ID,
						ToolName:  tc.Name,
						ToolError: true,
					}
					msgs = append(msgs, toolMsg)
					if a.store != nil {
						a.store.AddChatMessage(toolMsg)
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
					warnChunk := ChatStreamChunk{Phase: "tool", Step: fmt.Sprintf("call-%d-warn", i+1), Message: fmt.Sprintf("     ! %s 返回错误 (%s)", tc.Name, toolElapsed), ToolName: tc.Name, ToolResult: resultPreview, ToolError: "tool returned error", ToolElapsed: toolElapsed, Round: roundNum, MaxRound: maxRounds}
					partsAcc.update(warnChunk)
					ch <- warnChunk
				} else {
					okChunk := ChatStreamChunk{Phase: "tool", Step: fmt.Sprintf("call-%d-ok", i+1), Message: fmt.Sprintf("     ok %s 完成 (%s, %d 字节)", tc.Name, toolElapsed, len(result.Content)), ToolName: tc.Name, ToolResult: resultPreview, ToolElapsed: toolElapsed, Round: roundNum, MaxRound: maxRounds}
					partsAcc.update(okChunk)
					ch <- okChunk
				}

			llmContent := result.Content
			if result.IsError {
				llmContent = fmt.Sprintf("error: %s\n\n工具 %s 返回了错误状态。请根据以上错误信息分析原因、调整参数或方案后重试；反复失败请告知用户。", result.Content, tc.Name)
			} else {
				llmContent = truncateToolResult(tc.Name, result.Content)
			}
				toolMsg := llm.ChatMessage{
					Role:      llm.RoleTool,
					Type:      llm.TypeToolResult,
					Content:   llmContent,
					ToolID:    tc.ID,
					ToolName:  tc.Name,
					ToolError: result.IsError,
				}
				msgs = append(msgs, toolMsg)
				if a.store != nil {
					a.store.AddChatMessage(toolMsg)
				}
			}
			// Persist assistant message now that tool
			// results are captured in partsAcc.
			persistAssistant(a.store, assistantMsg, fullThinking, partsAcc)
		}

		if maxRounds > 0 {
			ch <- ChatStreamChunk{Phase: "limit", Step: "max-rounds", Message: fmt.Sprintf("已达到 %d 轮上限 (总耗时 %s)", maxRounds, formatElapsed(time.Since(start))), Round: maxRounds, MaxRound: maxRounds, TokensIn: totalIn, TokensOut: totalOut}
		}
		ch <- ChatStreamChunk{Done: true}
	}()

	return ch
}

func formatElapsed(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
}

// normalizeToolResults removes TypeToolCall metadata rows and
// converts TypeToolResult messages to User role. This way providers
// that validate tool_call/tool_result pairing (DeepSeek, etc.) see
// normal user-assistant-user conversation, and the LLM still sees
// tool results as part of the ongoing dialogue.
func normalizeToolResults(msgs []llm.ChatMessage) []llm.ChatMessage {
	out := make([]llm.ChatMessage, 0, len(msgs))
	for _, m := range msgs {
		if m.Type == llm.TypeToolCall {
			continue
		}
		if m.Type == llm.TypeToolResult {
			m.Role = llm.RoleUser
		}
		out = append(out, m)
	}
	return out
}

// countMeaningfulMessages counts messages that contribute to the
// context window (excludes tool_call/tool_result metadata rows).
func countMeaningfulMessages(msgs []llm.ChatMessage) int {
	n := 0
	for _, m := range msgs {
		if m.Type == llm.TypeToolCall || m.Type == llm.TypeToolResult {
			continue
		}
		n++
	}
	return n
}

func persistAssistant(store *memory.Store, msg llm.ChatMessage, fullThinking string, partsAcc *partsAccumulator) {
	if store == nil {
		return
	}
	meta := map[string]string{
		"role":   msg.Role,
		"reason": "assistant",
	}
	if fullThinking != "" {
		meta["thinking"] = fullThinking
	}
	structural := snapshotStructural(partsAcc)
	if len(structural) > 0 {
		if pj, pjErr := json.Marshal(structural); pjErr == nil {
			meta["parts"] = string(pj)
		}
	}
	store.AddChatMessageWithMeta(msg, meta)
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

const (
	// Tool result caps keep the LLM context and SQLite
	// storage bounded even when a tool produces massive
	// output (e.g. systeminfo, a large log file).
	// The UI stream preview is already capped at 300
	// chars; these limits apply to what the LLM sees
	// and what gets persisted in the messages table.
	//
	// Cap choice rationale:
	//   - exec_command: keep the tail (last N chars) —
	//     stdout/stderr errors and summaries are at the
	//     end.
	//   - read_file / list_files: keep the head — the
	//     first N chars are the file/dir contents.
	//   - fallback: keep the head.
	maxToolResultExec    = 4000 // exec_command, bash
	maxToolResultRead    = 8000 // read_file
	maxToolResultDefault = 6000
)

func truncateToolResult(name string, content string) string {
	if len(content) <= maxToolResultDefault {
		return content
	}

	var cap_ int
	keepHead := true
	switch name {
	case "exec_command", "bash", "shell":
		cap_ = maxToolResultExec
		keepHead = false
	case "read_file", "list_files", "recall":
		cap_ = maxToolResultRead
	default:
		cap_ = maxToolResultDefault
	}

	if len(content) <= cap_ {
		return content
	}

	var truncated string
	if keepHead {
		truncated = content[:cap_]
	} else {
		truncated = content[len(content)-cap_:]
	}

	skipped := len(content) - len(truncated)
	return fmt.Sprintf("%s\n\n[truncated: %d bytes skipped, total %d → %d]",
		truncated, skipped, len(content), len(truncated))
}

// parseMarkdownToolCalls extracts ```tool_call ... ``` blocks from the LLM
// response. Each block contains a JSON object {name, arguments}.
func parseMarkdownToolCalls(content string) []nativeToolCall {
	var calls []nativeToolCall
	const start = "```tool_call\n"
	const end = "\n```"

	idx := 0
	for {
		si := strings.Index(content[idx:], start)
		if si < 0 {
			break
		}
		si += idx
		ei := strings.Index(content[si+len(start):], end)
		if ei < 0 {
			break
		}
		ei += si + len(start)
		block := content[si+len(start) : ei]
		var raw struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal([]byte(block), &raw); err != nil {
			idx = ei + len(end)
			continue
		}
		if raw.Name == "" {
			idx = ei + len(end)
			continue
		}
		calls = append(calls, nativeToolCall{
			ID:       "call_" + uuid.NewString(),
			Name:     raw.Name,
			ArgsJSON: string(raw.Arguments),
		})
		idx = ei + len(end)
	}
	return calls
}

// cleanMarkdownToolCalls removes ```tool_call ... ``` blocks from
// assistant text content so the user sees clean text without raw
// tool call JSON mixed in.
func cleanMarkdownToolCalls(content string) string {
	const start = "```tool_call\n"
	const end = "\n```"
	result := content
	for {
		si := strings.Index(result, start)
		if si < 0 {
			break
		}
		ei := strings.Index(result[si+len(start):], end)
		if ei < 0 {
			break
		}
		ei += si + len(start) + len(end)
		// Remove the block and replace with a single newline to
		// avoid joining adjacent text without whitespace.
		result = result[:si] + result[ei:]
	}
	return strings.TrimSpace(result)
}
