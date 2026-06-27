package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/fatih/color"
	"github.com/p-chat/pchat/internal/agent"
	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/memory"
	"github.com/p-chat/pchat/internal/paths"
	"github.com/p-chat/pchat/internal/recall"
	"github.com/p-chat/pchat/internal/style"
	"github.com/p-chat/pchat/internal/subagent"
	"github.com/p-chat/pchat/internal/tool"

	"golang.org/x/term"
)

type REPL struct {
	cfg          *config.Config
	agent        *agent.Agent
	llm          *llm.Client
	styleMgr     *style.Manager
	tools        *tool.Registry
	store        *memory.Store
	style        style.Style
	provider     string
	useTools     bool
	subCache     *subagent.Cache
	recallEngine *recall.Engine
	kbManager    *KBManager
	toolCache    *toolResultCache

	mu             sync.Mutex
	cancelCurrent  context.CancelFunc // set while a chat is in flight
}

func NewREPL(cfg *config.Config, agt *agent.Agent, llmClient *llm.Client, styleMgr *style.Manager, tools *tool.Registry, s style.Style, provider string) *REPL {
	return &REPL{
		cfg:       cfg,
		agent:     agt,
		llm:       llmClient,
		styleMgr:  styleMgr,
		tools:     tools,
		style:     s,
		provider:  provider,
		useTools:  true,
		toolCache: newToolResultCache(20),
	}
}

// SetSubAgentCache attaches a cache instance for the subagent package.
// The cache is exposed via /debug cache. Pass nil to disable.
func (r *REPL) SetSubAgentCache(c *subagent.Cache) {
	r.subCache = c
}

// SetStore attaches the memory store so the REPL can flush on shutdown.
func (r *REPL) SetStore(s *memory.Store) {
	r.store = s
}

// SetLLMClient swaps the in-memory LLM client. Used after commands
// like `/config model add` that mutate the on-disk config — the
// REPL's pre-built client still has the old model list, so callers
// must replace it before the next `/model` switch.
func (r *REPL) SetLLMClient(c *llm.Client) {
	r.llm = c
}

// SetRecallEngine attaches the semantic-search engine for /recall and
// future agent-injected recall.
func (r *REPL) SetRecallEngine(e *recall.Engine) {
	r.recallEngine = e
}

// SetKBManager attaches the knowledge-base manager for /kb commands.
func (r *REPL) SetKBManager(m *KBManager) {
	r.kbManager = m
}

// asContext returns a cliContext backed by this REPL. Slash
// commands receive this view instead of the raw *REPL so the same
// handlers can run in HTTP mode (backed by httpcli.Client) without
// changes.
func (r *REPL) asContext() cliContext {
	return &localContext{r: r}
}

func (r *REPL) Run() error {
	if err := paths.EnsureGlobal(); err != nil {
		return fmt.Errorf("init directories: %w", err)
	}

	model := r.llm.GetModel(r.provider)
	printWelcomeBanner(r.styleMgr.Label(r.style), r.provider, model)

	for {
		input, isSlash, err := InputLine(r.style, r.provider)
		if err != nil {
			if err.Error() == "interrupted" {
				continue
			}
			break
		}

		input = strings.TrimSpace(input)
		if input == "" {
			continue
		}

		// Handle multi-line input
		if input == "```" {
			scanner := bufio.NewScanner(os.Stdin)
			scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
			input = r.readMultiLine(scanner)
			if input == "" {
				continue
			}
			isSlash = false
		}

		// Handle slash commands
		if isSlash || strings.HasPrefix(input, "/") {
			cmd, args := matchCommand(input)
			if cmd != nil {
				err := cmd.Handler(r.asContext(), args)
				if errors.Is(err, errQuit) {
					return nil
				}
				if err != nil {
					color.Red("  错误: %v", err)
				}
				continue
			}
			color.Red("  未知命令: %s  (输入 /help 查看帮助)", input)
			continue
		}

		r.chat(input)
	}

	if r.store != nil {
		_ = r.store.Flush()
	}
	return nil
}

func (r *REPL) readMultiLine(scanner *bufio.Scanner) string {
	color.HiBlack("  多行模式 (输入 ``` 结束)")
	var lines []string
	lineNum := 1
	for {
		fmt.Printf("  \033[90m%2d │\033[0m ", lineNum)
		if !scanner.Scan() {
			break
		}
		line := scanner.Text()
		if strings.TrimSpace(line) == "```" {
			break
		}
		lines = append(lines, line)
		lineNum++
	}
	return strings.Join(lines, "\n")
}

func (r *REPL) chat(input string) {
	// Build the request messages: history of the current conversation
	// + the new user input. The agent will then add the system prompt
	// at the front, so the LLM sees: [system, history..., user_input].
	//
	// We deliberately keep this list scoped to the *current* conversation.
	// /new starts a fresh conversation, so this naturally isolates
	// sessions even if the same REPL stays open.
	msgs := []llm.ChatMessage{}
	if r.store != nil {
		msgs = append(msgs, r.store.GetChatMessages()...)
	}
	msgs = append(msgs, llm.ChatMessage{
		Role:    llm.RoleUser,
		Type:    llm.TypeText,
		Content: input,
	})

	req := agent.ChatRequest{
		Style:    r.style,
		Provider: r.provider,
		Messages: msgs,
	}

	provModel := r.llm.GetModel(r.provider)
	ui := NewChatUI(r.provider, provModel)
	ui.PrintBannerHeader(input)

	// Set up a cancellable context. The user can press Esc to abort
	// the in-flight LLM/tool call; the watcher goroutine below
	// listens for raw-mode Esc input and triggers cancel().
	ctx, cancel := context.WithCancel(context.Background())
	r.mu.Lock()
	r.cancelCurrent = cancel
	r.mu.Unlock()
	defer func() {
		cancel()
		r.mu.Lock()
		r.cancelCurrent = nil
		r.mu.Unlock()
	}()

	go r.watchEsc(ctx, cancel)

	var stream <-chan agent.ChatStreamChunk
	if r.useTools && r.tools != nil && len(r.tools.List()) > 0 {
		stream = r.agent.ChatWithTools(ctx, req)
	} else {
		stream = r.agent.ChatStream(ctx, req)
	}

	// Track the most recent tool call's metadata so we can record
	// the result into the tool result cache when the call completes.
	var lastToolName, lastToolArgs string
	var lastToolStart time.Time

	for chunk := range stream {
		ui.Handle(chunk)
		r.recordToolChunk(chunk, &lastToolName, &lastToolArgs, &lastToolStart)
	}
	ui.Finish()
}

// recordToolChunk updates the per-tool-call tracker state and, when a
// tool call completes, pushes a result into the cache for /expand.
func (r *REPL) recordToolChunk(chunk agent.ChatStreamChunk, lastName, lastArgs *string, lastStart *time.Time) {
	if chunk.SubAgent {
		return // ignore sub-agent tool events (not in main chat cache)
	}
	if chunk.Phase != "tool" {
		return
	}
	step := chunk.Step
	switch {
	case len(step) > 5 && step[len(step)-2:] == "ok":
		errStr := ""
		if chunk.ToolError != "" {
			errStr = chunk.ToolError
		}
		dur := time.Duration(0)
		if !lastStart.IsZero() {
			dur = time.Since(*lastStart)
		}
		r.toolCache.record(*lastName, *lastArgs, chunk.ToolResult, errStr, dur)
		*lastName, *lastArgs = "", ""
		*lastStart = time.Time{}
	case len(step) > 5 && step[len(step)-2:] == "rr" && step[len(step)-3] == 'e':
		// call-N-err (handler error)
		errStr := chunk.ToolError
		dur := time.Duration(0)
		if !lastStart.IsZero() {
			dur = time.Since(*lastStart)
		}
		r.toolCache.record(*lastName, *lastArgs, "", errStr, dur)
		*lastName, *lastArgs = "", ""
		*lastStart = time.Time{}
	case len(step) > 5 && step[len(step)-2:] == "rn" && step[len(step)-3] == 'a':
		// call-N-warn (tool returned error in result)
		errStr := "tool returned error"
		dur := time.Duration(0)
		if !lastStart.IsZero() {
			dur = time.Since(*lastStart)
		}
		r.toolCache.record(*lastName, *lastArgs, chunk.ToolResult, errStr, dur)
		*lastName, *lastArgs = "", ""
		*lastStart = time.Time{}
	case strings.HasPrefix(step, "call-") && !strings.Contains(step, "ok") &&
		!strings.Contains(step, "err") && !strings.Contains(step, "warn"):
		*lastName = chunk.ToolName
		*lastArgs = chunk.ToolArgs
		*lastStart = time.Now()
	}
}

// reloadConfig reloads the config and recreates the LLM client
func (r *REPL) reloadConfig() {
	cfg, err := config.Load("")
	if err != nil {
		color.Red("  重载配置失败: %v", err)
		return
	}
	r.cfg = cfg

	llmClient, err := llm.NewClient(&cfg.LLM)
	if err != nil {
		color.Red("  重建 LLM 客户端失败: %v", err)
		return
	}
	r.llm = llmClient
}

func printWelcomeBanner(label string, provider string, model string) {
	fmt.Println()
	c := color.New(color.FgCyan, color.Bold)
	c.Println("  ╔═══════════════════════════════════════════════════╗")
	c.Println("  ║         P-Chat  ·  对话式 AI Agent               ║")
	c.Println("  ╠═══════════════════════════════════════════════════╣")
	c.Printf("  ║  人格: %-18s 模型: %-16s ║\n", label, provider+"/"+model)
	c.Println("  ╠═══════════════════════════════════════════════════╣")
	c.Println("  ║  输入消息直接对话  │  /help 查看命令             ║")
	c.Println("  ║  / 自动补全命令    │  /setup 配置提供商          ║")
	c.Println("  ╚═══════════════════════════════════════════════════╝")
	fmt.Println()
}

func printPrompt(s style.Style, provider string) {
	var icon string
	switch s {
	case style.Cute:
		icon = "🐹"
	case style.Guofeng:
		icon = "📜"
	case style.Tech:
		icon = "⚡"
	default:
		icon = "❯"
	}
	fmt.Printf("  %s \033[90m[%s]\033[0m ", icon, provider)
}

// watchEsc polls stdin in raw mode (non-blocking) while a chat is
// in flight. When the user presses Esc (a bare 0x1B with no follow-up
// bytes), it triggers cancel so the in-flight LLM call aborts.
//
// We don't share state with InputLine here because InputLine has
// already returned by the time the chat loop starts; the watch
// goroutine re-opens the tty in raw mode and exits when the chat
// loop's ctx is cancelled.
func (r *REPL) watchEsc(ctx context.Context, cancel context.CancelFunc) {
	defer cancel() // safety: if we return for any reason, abort the chat

	fd := int(os.Stdin.Fd())
	if !term.IsTerminal(fd) {
		return
	}
	oldState, err := term.MakeRaw(fd)
	if err != nil {
		return
	}
	defer term.Restore(fd, oldState)

	buf := make([]byte, 1)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		// Use SetReadDeadline so we can periodically re-check ctx.
		// A 200ms timeout is small enough to feel snappy on cancel
		// and large enough not to hammer the syscall.
		_ = os.Stdin.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
		n, err := os.Stdin.Read(buf)
		if err != nil {
			// Timeout → loop; real error → exit.
			continue
		}
		if n > 0 && buf[0] == 0x1B {
			// Bare Esc. Print a small notice and trigger cancel.
			fmt.Fprintln(os.Stderr, "\n  ✗ [Esc] 已取消当前生成")
			cancel()
			return
		}
		// Other keys: ignore (the user can't type a new message
		// while a chat is running).
	}
}
