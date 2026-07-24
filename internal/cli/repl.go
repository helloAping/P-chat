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
	"github.com/p-chat/pchat/internal/httpcli"
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
	ctx cliContext // primary context (HTTP mode)
	// runCtx is the long-lived context the REPL was
	// started with. Passed down to background operations
	// (recall search, snapshot fetch, etc.) so a SIGINT
	// (or any other cancellation) propagates and aborts
	// in-flight work. Defaults to context.Background() so
	// callers that don't pass one still get a working
	// (uncancellable) context — better than nil-deref.
	runCtx context.Context

	cfg      *config.Config
	agent    *agent.Agent
	llm      *llm.Client
	styleMgr *style.Manager
	tools    *tool.Registry
	store    *memory.Store
	style    style.Style
	mode     config.WorkMode
	provider string
	useTools bool

	subCache     *subagent.Cache
	recallEngine *recall.Engine
	kbManager    *KBManager
	toolCache    *toolResultCache

	mu            sync.Mutex
	cancelCurrent context.CancelFunc // set while a chat is in flight

	rollbackUndo map[string][]httpcli.Message // session-id → last rollback snapshot
}

func NewREPL(ctx cliContext, cfg *config.Config, s style.Style, provider string) *REPL {
	return &REPL{
		ctx:          ctx,
		runCtx:       context.Background(),
		cfg:          cfg,
		style:        s,
		mode:         cfg.WorkMode.Default.Normalize(),
		provider:     provider,
		useTools:     true,
		rollbackUndo: make(map[string][]httpcli.Message),
	}
}

// SetRunContext replaces the long-lived context used for
// background operations (recall, snapshot, etc.). Call this
// from main() before Run() so SIGINT propagates.
func (r *REPL) SetRunContext(ctx context.Context) {
	if ctx == nil {
		return
	}
	r.runCtx = ctx
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

// asContext returns a cliContext backed by this REPL.
func (r *REPL) asContext() cliContext {
	if r.ctx != nil {
		return r.ctx
	}
	return &localContext{r: r}
}

func (r *REPL) Run() error {
	if err := paths.EnsureGlobal(); err != nil {
		return fmt.Errorf("init directories: %w", err)
	}

	model := r.providerModel()
	label := r.styleLabel()
	printWelcomeBanner(label, r.provider, model)

	for {
		input, isSlash, err := InputLine(r.style, r.provider, r.mode)
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
	req := agent.ChatRequest{
		Style:    r.style,
		WorkMode: r.mode.Normalize(),
		Provider: r.provider,
		Messages: []llm.ChatMessage{
			{Role: llm.RoleUser, Type: llm.TypeText, Content: input},
		},
	}

	provModel := r.providerModel()
	ui := NewChatUI(r.provider, provModel)

	ctx, cancel := context.WithCancel(context.Background())
	ui.SetQuestionHandler(r.ctx.GetCurrentSessionID(), func(sid string, answers map[string]string) error {
		return r.ctx.SubmitQuestionAnswer(ctx, sid, answers)
	})
	ui.PrintBannerHeader(input)

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

	stream, err := r.ctx.ChatWithTools(ctx, req)
	if err != nil {
		ui.Handle(agent.ChatStreamChunk{Error: err.Error(), Done: true})
		ui.Finish()
		return
	}

	for chunk := range stream {
		ui.Handle(chunk)
	}
	ui.Finish()
}

// providerModel returns the current model name for display.
func (r *REPL) providerModel() string {
	if r.ctx != nil {
		return r.ctx.GetCurrentModel()
	}
	if r.llm != nil {
		return r.llm.GetModel(r.provider)
	}
	return ""
}

// styleLabel returns the human-readable label for the current style.
func (r *REPL) styleLabel() string {
	if r.ctx != nil {
		return r.ctx.StyleLabel(r.style)
	}
	if r.styleMgr != nil {
		return r.styleMgr.DisplayLabel(r.style)
	}
	return string(r.style)
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

func printPrompt(s style.Style, provider string, mode config.WorkMode) {
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
	fmt.Printf("  %s \033[90m[%s mode:%s]\033[0m ", icon, provider, mode.Normalize())
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
