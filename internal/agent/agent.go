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

	openai "github.com/sashabaranov/go-openai"
)

type Agent struct {
	llm      *llm.Client
	styleMgr *style.Manager
	store    *memory.Store
	tools    *tool.Registry
	cfg      *config.Config
	skills   []skill.Skill
	rules    []rules.Rule
	sandbox  tool.SandboxChecker // optional; nil disables sandbox enforcement
	options  llm.ChatOptions     // per-request sampling; populated from cfg

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

type ChatRequest struct {
	Style    style.Style   `json:"style"`
	Messages []llm.Message `json:"messages"`
	Provider string        `json:"provider,omitempty"`
	// Model is the per-request model name. When non-empty, the LLM
	// client uses it for this call (overriding the shared
	// providerEntry.model). When empty, the provider's default
	// applies. This is what lets multiple sessions on the same
	// provider use different models concurrently without racing.
	Model    string        `json:"model,omitempty"`
	// PlanMode, when true, asks the LLM to produce a step-by-step
	// plan in plain text instead of executing tools. The agent will
	// inject a system hint and skip the tool loop.
	PlanMode bool `json:"plan_mode,omitempty"`
}

type ChatStreamChunk struct {
	Content string `json:"content"`
	Done    bool   `json:"done"`
	Error   string `json:"error,omitempty"`

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

	// SubAgent is true when this chunk originated from a sub-agent
	// (e.g. a `task` tool call). The UI should render such events as
	// nested / indented.
	SubAgent bool `json:"sub_agent,omitempty"`
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
func (a *Agent) buildStaticSystemPrompt(s style.Style, openAITools []openai.Tool) (string, string, error) {
	// Build a signature so we can skip rebuilding when nothing changed.
	toolNames := make([]string, 0, len(openAITools))
	for _, t := range openAITools {
		toolNames = append(toolNames, t.Function.Name)
	}
	lang := ""
	if a.cfg != nil {
		lang = a.cfg.LLM.Output.Language
	}
	sig := strings.Join([]string{
		string(s),
		agentsSignature(),
		rulesSignature(a.rules),
		skillSignature(a.skills),
		strings.Join(toolNames, ","),
		lang,
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
	sb.WriteString(agents.LoadAll())
	sb.WriteString("\n---\n\n")

	// 3. Rules — stable until files change.
	sb.WriteString(rules.BuildRulesContext(a.rules))
	sb.WriteString("\n---\n\n")

	// 4. Skills — stable until files change.
	sb.WriteString(skill.BuildSkillContext(a.skills))
	sb.WriteString("\n---\n\n")

	// 5. Tool hint — stable per session (tools don't change at runtime).
	if len(openAITools) > 0 {
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
	}

	// 6. Output language hint — also part of the cacheable prefix
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
		openAITools := llm.ToolsFromRegistry(availableTools)
		if len(openAITools) > 0 {
			names := make([]string, 0, len(availableTools))
			for _, t := range availableTools {
				names = append(names, t.Name)
			}
			ch <- ChatStreamChunk{Phase: "tools", Step: "tools", Message: fmt.Sprintf("可用工具 (%d): %s", len(availableTools), strings.Join(names, ", "))}
		} else {
			ch <- ChatStreamChunk{Phase: "tools", Step: "tools", Message: "未注册任何工具"}
			openAITools = nil
		}

		ch <- ChatStreamChunk{Phase: "system", Step: "load-system", Message: "合并风格 / AGENTS.md / 规则 / 技能..."}
		// The static system prompt is built ONCE per session (or whenever the
		// underlying files / style change). It's identical between rounds,
		// so the LLM's prefix cache hits on it across rounds.
		systemPrompt, _, err := a.buildStaticSystemPrompt(req.Style, openAITools)
		if err != nil {
			ch <- ChatStreamChunk{Phase: "system", Error: err.Error(), Done: true}
			return
		}
		ch <- ChatStreamChunk{Phase: "system", Step: "ok", Message: fmt.Sprintf("系统提示已就绪 (%d 字符)", len(systemPrompt)), Duration: formatElapsed(time.Since(start))}

		msgs := []llm.Message{
			{Role: openai.ChatMessageRoleSystem, Content: systemPrompt},
		}
		msgs = append(msgs, req.Messages...)

		if a.store != nil {
			ch <- ChatStreamChunk{Phase: "memory", Step: "memory", Message: fmt.Sprintf("写入 %d 条消息到记忆", len(req.Messages))}
			for _, m := range req.Messages {
				a.store.AddMessage(m)
			}
		}

		// Plan mode: tell the LLM to produce a step-by-step plan in
		// pure text (no tool calls). The user will review the plan
		// before actually executing it.
		maxRounds := 5
		if req.PlanMode {
			openAITools = nil // disable tool calls for this turn
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
			ch <- ChatStreamChunk{Phase: "plan", Step: "plan", Message: fmt.Sprintf("规划: 最多 %d 轮 ReAct 循环", maxRounds)}
		}

		var totalIn, totalOut int

		useNativeTools := len(openAITools) > 0

		for round := 0; round < maxRounds; round++ {
			roundStart := time.Now()
			roundNum := round + 1

			ch <- ChatStreamChunk{Phase: "llm", Step: fmt.Sprintf("round-%d", roundNum), Message: fmt.Sprintf("[第 %d/%d 轮] 调用 LLM", roundNum, maxRounds), Round: roundNum, MaxRound: maxRounds}

			var (
				fullContent string
				toolCalls   []nativeToolCall
				argsAccum   = make(map[int]*nativeToolCall)
			)

			stream := a.llm.ChatStreamWithOptions(ctx, req.Provider, req.Model, msgs, openAITools, a.options)
			for chunk := range stream {
				if chunk.Err != nil {
					ch <- ChatStreamChunk{Phase: "llm", Error: chunk.Err.Error(), Done: true}
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
					ch <- ChatStreamChunk{Content: chunk.Content, TokensIn: totalIn, TokensOut: totalOut}
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

			ch <- ChatStreamChunk{Phase: "llm", Step: fmt.Sprintf("round-%d-done", roundNum), Message: fmt.Sprintf("[第 %d/%d 轮] 模型响应: %d 字符 / 耗时 %s", roundNum, maxRounds, len(fullContent), formatElapsed(time.Since(roundStart))), Round: roundNum, MaxRound: maxRounds, TokensIn: totalIn, TokensOut: totalOut}

			// If the LLM did not emit native tool calls, fall back to the
			// legacy markdown block parser.
			if len(toolCalls) == 0 {
				toolCalls = parseMarkdownToolCalls(fullContent)
				if len(toolCalls) > 0 {
					useNativeTools = false
				}
			}

			// Build the assistant message we record in the conversation.
			assistantMsg := openai.ChatCompletionMessage{
				Role:    openai.ChatMessageRoleAssistant,
				Content: fullContent,
			}
			if useNativeTools && len(toolCalls) > 0 {
				assistantMsg.ToolCalls = make([]openai.ToolCall, 0, len(toolCalls))
				for _, tc := range toolCalls {
					// Each tool call needs an ID and a Type field.
					id := tc.ID
					if id == "" {
						id = "call_" + uuid.NewString()
					}
					assistantMsg.ToolCalls = append(assistantMsg.ToolCalls, openai.ToolCall{
						ID:   id,
						Type: openai.ToolTypeFunction,
						Function: openai.FunctionCall{
							Name:      tc.Name,
							Arguments: tc.ArgsJSON,
						},
					})
				}
			}
			msgs = append(msgs, assistantMsg)

			// Always save the assistant message to the store, even when
			// it has tool_calls (so the next turn can replay the full
			// conversation including the tool calls). For empty content
			// (rare; LLM responded only with tool calls) we still save it
			// because the tool_calls field is what matters.
			if a.store != nil {
				a.store.AddMessageWithMeta(assistantMsg, map[string]string{
					"role":   assistantMsg.Role,
					"reason": "assistant",
				})
			}

			if len(toolCalls) == 0 {
				ch <- ChatStreamChunk{Phase: "done", Step: "done", Message: fmt.Sprintf("完成 (总耗时 %s, 共 %d 轮)", formatElapsed(time.Since(start)), roundNum), Round: roundNum, MaxRound: maxRounds, TokensIn: totalIn, TokensOut: totalOut}
				ch <- ChatStreamChunk{Done: true}
				return
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
				ch <- ChatStreamChunk{Phase: "tool", Step: fmt.Sprintf("call-%d", i+1), Message: fmt.Sprintf("  -> 工具 %d/%d: %s", i+1, len(toolCalls), tc.Name), ToolName: tc.Name, ToolArgs: argsPreview, Round: roundNum, MaxRound: maxRounds}
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
				tctx, cancel := context.WithTimeout(tctx, 5*time.Minute)

				fwd := forwarder{done: make(chan struct{})}
				forwarders = append(forwarders, fwd)
				go func() {
					defer close(fwd.done)
					for ev := range eventCh {
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
					ch <- ChatStreamChunk{Phase: "tool", Step: fmt.Sprintf("call-%d-err", i+1), Message: fmt.Sprintf("     X %s 执行失败 (%s): %s", tc.Name, toolElapsed, errMsg), ToolName: tc.Name, ToolError: errMsg, ToolElapsed: toolElapsed, Round: roundNum, MaxRound: maxRounds}
					toolMsg := openai.ChatCompletionMessage{
						Role:       openai.ChatMessageRoleTool,
						Content:    fmt.Sprintf("error: %s", errMsg),
						ToolCallID: tc.ID,
					}
					msgs = append(msgs, toolMsg)
					if a.store != nil {
						a.store.AddMessageWithMeta(toolMsg, map[string]string{
							"tool_call_id": tc.ID,
						})
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
					ch <- ChatStreamChunk{Phase: "tool", Step: fmt.Sprintf("call-%d-warn", i+1), Message: fmt.Sprintf("     ! %s 返回错误 (%s)", tc.Name, toolElapsed), ToolName: tc.Name, ToolResult: resultPreview, ToolError: "tool returned error", ToolElapsed: toolElapsed, Round: roundNum, MaxRound: maxRounds}
				} else {
					ch <- ChatStreamChunk{Phase: "tool", Step: fmt.Sprintf("call-%d-ok", i+1), Message: fmt.Sprintf("     ok %s 完成 (%s, %d 字节)", tc.Name, toolElapsed, len(result.Content)), ToolName: tc.Name, ToolResult: resultPreview, ToolElapsed: toolElapsed, Round: roundNum, MaxRound: maxRounds}
				}

				msgs = append(msgs, openai.ChatCompletionMessage{
					Role:       openai.ChatMessageRoleTool,
					Content:    result.Content,
					ToolCallID: tc.ID,
				})
				if a.store != nil {
					a.store.AddMessageWithMeta(openai.ChatCompletionMessage{
						Role:       openai.ChatMessageRoleTool,
						Content:    result.Content,
						ToolCallID: tc.ID,
					}, map[string]string{
						"tool_call_id": tc.ID,
						"tool_name":    tc.Name,
					})
				}
			}
		}

		ch <- ChatStreamChunk{Phase: "done", Step: "max-rounds", Message: fmt.Sprintf("达到最大轮次 %d, 强制结束 (总耗时 %s)", maxRounds, formatElapsed(time.Since(start))), Round: maxRounds, MaxRound: maxRounds, TokensIn: totalIn, TokensOut: totalOut}
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

type nativeToolCall struct {
	ID       string
	Name     string
	ArgsJSON string
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
