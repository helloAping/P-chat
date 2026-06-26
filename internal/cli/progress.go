package cli

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/fatih/color"
	"github.com/p-chat/pchat/internal/agent"
)

// ChatUI renders chat interactions in a clean, Claude-Code-inspired style.
// The output is composed of:
//   1. A thinking indicator (spinner) that runs while the LLM is working
//   2. Compact tool call/result lines (only when tools fire)
//   3. The LLM response as plain streaming text
//   4. A final status bar with model, tokens, elapsed
type ChatUI struct {
	provider string
	model    string

	// Spinner state
	spinner *Spinner

	// Stats
	tokensIn  atomic.Int64
	tokensOut atomic.Int64
	start     time.Time
	round     int
	maxRound  int

	// Streaming text state
	inTextBlock  bool
	firstContent bool
	renderedNL   int // number of newlines we have printed for status update

	// Tool tracking
	toolsRun      int
	toolsActually int // count of tool calls that actually executed
}

// NewChatUI creates a new chat UI renderer.
func NewChatUI(provider, model string) *ChatUI {
	return &ChatUI{
		provider:     provider,
		model:        model,
		start:        time.Now(),
		firstContent: true,
	}
}

// Handle processes a single chunk from the agent stream.
func (u *ChatUI) Handle(chunk agent.ChatStreamChunk) {
	// Track stats
	if chunk.TokensIn > 0 {
		u.tokensIn.Store(int64(chunk.TokensIn))
	}
	if chunk.TokensOut > 0 {
		u.tokensOut.Store(int64(chunk.TokensOut))
	}
	if chunk.Round > 0 {
		u.round = chunk.Round
	}
	if chunk.MaxRound > 0 {
		u.maxRound = chunk.MaxRound
	}

	// Sub-agent events get a dedicated, indented display path so the
	// user can see what's happening inside the task tool.
	if chunk.SubAgent {
		u.handleSubAgentEvent(chunk)
		return
	}

	// Error terminal
	if chunk.Error != "" {
		u.stopSpinner()
		u.ensureLine()
		fmt.Println()
		color.Red("  ✗ %s", chunk.Error)
		u.printErrorHint(chunk.Error)
		fmt.Println()
		u.printStatusBar()
		return
	}

	// Done terminal - actual stream is done, clean up
	if chunk.Done {
		u.stopSpinner()
		return
	}

	// Phase events
	if chunk.Phase != "" {
		u.handlePhaseEvent(chunk)
		return
	}

	// Streaming content
	if chunk.Content != "" {
		u.handleContent(chunk.Content)
	}
}

// handleSubAgentEvent renders a chunk that came from a nested sub-agent.
// Events are shown indented under the parent tool call that triggered them.
func (u *ChatUI) handleSubAgentEvent(chunk agent.ChatStreamChunk) {
	dim := color.New(color.FgHiBlack)
	indent := "    " // 4 spaces: aligned under "  ● task(args)"

	if chunk.Phase == "llm" {
		switch {
		case strings.HasPrefix(chunk.Step, "round-") && !strings.HasSuffix(chunk.Step, "-done"):
			// Sub-agent LLM call starts; no visible line (the
			// task-level spinner covers it).
			return
		case strings.HasSuffix(chunk.Step, "-done"):
			// Sub-agent LLM call completed.
			fmt.Println()
			dim.Printf("%s↳ LLM: %s\n", indent, chunk.Message)
			return
		}
	}

	if chunk.Phase == "tool" {
		if strings.HasPrefix(chunk.Step, "call-") && !strings.Contains(chunk.Step, "ok") &&
			!strings.Contains(chunk.Step, "err") && !strings.Contains(chunk.Step, "warn") {
			dim.Printf("%s↳ %s(%s)\n", indent, chunk.ToolName, u.formatArgs(chunk.ToolArgs))
			return
		}
		if strings.Contains(chunk.Step, "ok") {
			dim.Printf("%s↳ ✓ %s\n", indent, chunk.ToolName)
			return
		}
		if strings.Contains(chunk.Step, "err") || strings.Contains(chunk.Step, "warn") {
			dim.Printf("%s↳ ✗ %s\n", indent, chunk.ToolName)
			return
		}
	}

	if chunk.Content != "" {
		// Sub-agent text output. Don't print directly; the parent's
		// task result will show the full final text.
		return
	}

	if chunk.Done {
		return
	}
}

func (u *ChatUI) handlePhaseEvent(chunk agent.ChatStreamChunk) {
	switch chunk.Phase {
	case "llm":
		u.handleLLMEvent(chunk)
	case "tool":
		u.handleToolEvent(chunk)
	case "done":
		u.stopSpinner()
		u.ensureLine()
		fmt.Println()
		u.printStatusBar()
	default:
		// Silently ignore system/memory/plan/tools phases -
		// they're internal and don't need to be shown by default
	}
}

func (u *ChatUI) handleLLMEvent(chunk agent.ChatStreamChunk) {
	if strings.HasPrefix(chunk.Step, "round-") && !strings.HasSuffix(chunk.Step, "-done") {
		// LLM is being called. Make sure we have a clean line.
		u.stopSpinner()
		u.ensureLine()
		u.startSpinner(u.thinkingMessage())
		return
	}

	if strings.HasSuffix(chunk.Step, "-done") {
		// LLM call completed; spinner will be replaced by content
		u.stopSpinner()
		return
	}

	if !u.spinnerActive() {
		u.startSpinner(u.thinkingMessage())
	}
}

func (u *ChatUI) handleToolEvent(chunk agent.ChatStreamChunk) {
	u.stopSpinner()
	u.ensureLine()

	if strings.HasSuffix(chunk.Step, "tools") {
		// Round started, just skip the "detected N tool calls" header
		return
	}

	if strings.HasPrefix(chunk.Step, "call-") && !strings.Contains(chunk.Step, "ok") &&
		!strings.Contains(chunk.Step, "err") && !strings.Contains(chunk.Step, "warn") {
		// Tool call starting
		u.toolsRun++
		args := u.formatArgs(chunk.ToolArgs)
		icon := color.New(color.FgYellow)
		icon.Printf("  ● %s", chunk.ToolName)
		if args != "" {
			color.HiBlack("(%s)", args)
		}
		fmt.Println()
		u.startSpinner(fmt.Sprintf("执行 %s...", chunk.ToolName))
		return
	}

	if strings.Contains(chunk.Step, "ok") {
		u.stopSpinner()
		u.toolsActually++
		// Tool succeeded
		elapsed := chunk.ToolElapsed
		if elapsed == "" {
			elapsed = ""
		}
		result := chunk.ToolResult
		if len(result) > 200 {
			result = result[:200] + "..."
		}
		// One-line summary
		green := color.New(color.FgGreen)
		green.Printf("  ✓ %s", chunk.ToolName)
		if result != "" {
			color.HiBlack(" → %s", oneLine(result))
		}
		if elapsed != "" {
			color.HiBlack("  (%s)", elapsed)
		}
		// Suggest /expand for non-trivial results.
		if len(chunk.ToolResult) > 200 {
			color.HiBlack("  ▸ /expand last")
		}
		fmt.Println()
		return
	}

	if strings.Contains(chunk.Step, "warn") {
		u.stopSpinner()
		yellow := color.New(color.FgYellow)
		yellow.Printf("  ⚠ %s", chunk.ToolName)
		if chunk.ToolResult != "" {
			color.HiBlack(" → %s", oneLine(chunk.ToolResult))
		}
		if chunk.ToolElapsed != "" {
			color.HiBlack("  (%s)", chunk.ToolElapsed)
		}
		fmt.Println()
		return
	}

	if strings.Contains(chunk.Step, "err") {
		u.stopSpinner()
		red := color.New(color.FgRed)
		red.Printf("  ✗ %s", chunk.ToolName)
		if chunk.ToolError != "" {
			color.HiBlack(" → %s", oneLine(chunk.ToolError))
		}
		if chunk.ToolElapsed != "" {
			color.HiBlack("  (%s)", chunk.ToolElapsed)
		}
		fmt.Println()
		return
	}
}

func (u *ChatUI) handleContent(content string) {
	if u.spinnerActive() {
		u.stopSpinner()
		// No newline - content starts immediately after the cleared line
	}
	if u.firstContent {
		u.firstContent = false
		// Print a small spacing prefix for the first content chunk
		fmt.Print("  ")
		u.inTextBlock = true
	}
	fmt.Print(content)
}

func (u *ChatUI) printStatusBar() {
	dim := color.New(color.FgHiBlack)
	fmt.Println()
	dim.Println("  ─────────────────────────────────────────────────")

	parts := []string{}
	parts = append(parts, fmt.Sprintf("%s/%s", u.provider, u.model))

	elapsed := time.Since(u.start)
	if elapsed < time.Second {
		parts = append(parts, fmt.Sprintf("%.0fms", float64(elapsed.Milliseconds())))
	} else {
		parts = append(parts, fmt.Sprintf("%.1fs", elapsed.Seconds()))
	}

	if u.tokensIn.Load() > 0 || u.tokensOut.Load() > 0 {
		parts = append(parts, fmt.Sprintf("↑%d ↓%d tokens", u.tokensIn.Load(), u.tokensOut.Load()))
	}

	if u.toolsRun > 0 {
		parts = append(parts, fmt.Sprintf("%d tool calls", u.toolsRun))
	}

	if u.maxRound > 1 && u.toolsActually > 0 {
		parts = append(parts, fmt.Sprintf("round %d/%d", u.round, u.maxRound))
	}

	dim.Printf("  ⏵ %s\n", strings.Join(parts, " · "))
}

// PrintBannerHeader prints a small "user message" header before the response.
// This makes the chat look like a conversation.
func (u *ChatUI) PrintBannerHeader(userInput string) {
	u.start = time.Now()
	u.tokensIn.Store(0)
	u.tokensOut.Store(0)
	u.toolsRun = 0
	u.firstContent = true
	u.inTextBlock = false
	dim := color.New(color.FgCyan, color.Bold)
	dim.Printf("  ❯ %s\n", userInput)
	fmt.Println()
}

func (u *ChatUI) startSpinner(msg string) {
	if u.spinner != nil {
		u.spinner.Stop()
	}
	u.spinner = NewSpinner(msg)
	u.spinner.Start()
}

func (u *ChatUI) stopSpinner() {
	if u.spinner != nil {
		u.spinner.Stop()
		u.spinner = nil
	}
}

func (u *ChatUI) spinnerActive() bool {
	return u.spinner != nil
}

func (u *ChatUI) ensureLine() {
	if u.inTextBlock {
		fmt.Println()
		u.inTextBlock = false
	}
}

func (u *ChatUI) thinkingMessage() string {
	if u.maxRound > 1 {
		return "思考中..."
	}
	return "思考中..."
}

func (u *ChatUI) formatArgs(args string) string {
	args = strings.TrimSpace(args)
	if args == "" || args == "null" || args == "{}" {
		return ""
	}
	if len(args) > 80 {
		args = args[:80] + "..."
	}
	return args
}

func (u *ChatUI) printErrorHint(errMsg string) {
	// Match the [kind] prefix produced by *llm.APIError. The full
	// message is already shown; the hint is an extra nudge.
	if strings.HasPrefix(errMsg, "[") {
		end := strings.Index(errMsg, "]")
		if end > 0 {
			kind := errMsg[1:end]
			hint := apiHintForKind(kind)
			if hint != "" {
				fmt.Println()
				color.Yellow("  提示: ")
				color.HiBlack("    " + hint)
			}
			return
		}
	}
	// Legacy fallback: heuristic on the error string.
	if strings.Contains(errMsg, "dial tcp") || strings.Contains(errMsg, "connection") {
		fmt.Println()
		color.Yellow("  提示:")
		color.HiBlack("    - 检查网络连接")
		color.HiBlack("    - 使用 /model 切换提供商")
		color.HiBlack("    - 当前: %s", u.provider)
	}
}

// apiHintForKind returns an actionable hint for a given API error
// kind. Empty string means "no extra hint, the message itself is
// enough".
func apiHintForKind(kind string) string {
	switch kind {
	case "auth_error":
		return "用 /config key <provider> <新key> 更新 API key"
	case "not_found":
		return "/model 切换到该 provider 已配置的模型"
	case "rate_limit":
		return "稍后重试，或考虑切换到更便宜的模型"
	case "server_error":
		return "稍后重试 (上游服务异常)"
	case "timeout":
		return "可增加 max_tokens 或换更快的模型"
	case "network_error":
		return "检查网络 / base_url 是否可达"
	}
	return ""
}

// Finish is called when the stream is done.
func (u *ChatUI) Finish() {
	u.stopSpinner()
	if u.inTextBlock {
		fmt.Println()
		u.inTextBlock = false
	}
}

func oneLine(s string) string {
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\r' || r == '\t' {
			return ' '
		}
		return r
	}, s)
	// Collapse multiple spaces
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	return strings.TrimSpace(s)
}
