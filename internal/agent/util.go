package agent

// util.go — stateless helpers used by ChatWithTools. Categories:
//
//   * Time / size formatting (formatElapsed, truncateToFit)
//   * Tool-result cleanup (truncateToolResult, redactPhantomErrors)
//   * LLM-side markdown-tool-call parsing (parseMarkdownToolCalls,
//     cleanMarkdownToolCalls) — some proxy LLMs emit tool calls
//     as ```json fenced blocks instead of native tool_calls
//   * Retry policy (isRetryable)
//   * Tool-result history pruning (pruneOldToolResults)
//
// Split from agent.go in T05. Behaviour unchanged.

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/p-chat/pchat/internal/llm"
)

func formatElapsed(d time.Duration) string {
	if d < time.Second {
		return fmt.Sprintf("%dms", d.Milliseconds())
	}
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", d.Seconds())
	}
	return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
}

// resetGuardCounters clears the stuck-loop guard state. Called
// when a stronger intervention fires (e.g. sameToolErrCount
// injects a "改用其他方式" hint) so the LLM gets a fresh
// stuck budget — otherwise a stubborn LLM could trigger the
// stuck exit before the hint has a chance to take effect.
// See P2-1 in docs/plans/auto-continue-plan.md.

func (a *Agent) truncateToolResult(name string, content string) string {
	execCap := maxToolResultExec
	readCap := maxToolResultRead
	defaultCap := maxToolResultDefault
	if a.cfg != nil {
		if a.cfg.Limits.ToolResultExecCap > 0 {
			execCap = a.cfg.Limits.ToolResultExecCap
		}
		if a.cfg.Limits.ToolResultReadCap > 0 {
			readCap = a.cfg.Limits.ToolResultReadCap
		}
		if a.cfg.Limits.ToolResultDefaultCap > 0 {
			defaultCap = a.cfg.Limits.ToolResultDefaultCap
		}
	}

	var cap_ int
	keepHead := true
	switch name {
	case "exec_command", "bash", "shell":
		cap_ = execCap
		keepHead = false
	case "read_file", "list_files", "recall":
		cap_ = readCap
	default:
		cap_ = defaultCap
	}

	// The previous version had a `len(content) <= defaultCap`
	// short-circuit here, which incorrectly skipped truncation
	// for exec_command when execCap < defaultCap. For example,
	// with defaultCap=6000 and execCap=4000, an exec result
	// of 5000 bytes would pass the early return and be sent
	// to the LLM untruncated (exceeding the configured
	// exec_cap). Always go through the per-name cap check.
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

// phantomVisionErrorRe matches Claude-style "Cannot read \"image.png\"
// (this model does not support image input). Inform the user." style
// phantoms that DeepSeek-trained models parrot when they encounter
// the vision-denier marker we inject via ExpandAttachmentsCM.
//
// The pattern is deliberately loose on the filename and the
// "does not support image" wording, but it anchors on the
// trailing "Inform the user" fragment — that's the part that
// distinguishes a phantom from a legitimate "I can't read this
// file" error. We want to redact the former, not the latter.
//
// Flags: `(?is)` = case-insensitive + dotall.
//
// The middle match `[\s\S]{0,400}?` crosses line breaks (so
// phantoms that wrap "Inform the user." onto a new line are
// still caught — this is a very common formatting the LLM
// produces). The 400-character cap is much larger than any
// legitimate phantom but small enough that a multi-paragraph
// response that happens to mention both trigger phrases won't
// be nuked wholesale.
// phantomVisionErrorRe mirrors the regex used to strip Claude-style
// "Cannot read image.png ... Inform the user." phantoms that
// DeepSeek-trained models parrot when they encounter vision
// attachments they can't actually decode. The cap on distance
// (400 chars) distinguishes a phantom from a legitimate
// "I can't read this" diagnostic in a longer response.
//
// Multiple alternates catch the various phrasings different
// models use: "Cannot read" (Claude), "Unable to read" /
// "Failed to read" (some proxies), "I cannot view" / "I can't
// view" (OpenAI-flavoured). The trailing "Inform the user" is
// what really identifies a phantom — real diagnostic messages
// don't end that way.
var phantomVisionErrorRe = regexp.MustCompile(
	`(?is)(?:Cannot|Unable to|Failed to|cannot|unable to) (?:read|view|process)[\s\S]{0,400}?[Ii]nform the user\.?`,
)

// phantomVisionErrorReplacement is the clean user-facing message
// shown in place of the phantom. It's deliberately short and tells
// the user the actionable next step (switch model) without any
// "Inform the user" wording the LLM might later parrot back.
const phantomVisionErrorReplacement = "（当前模型不支持读取图片。请在「设置 → 提供商/模型」中切换到支持视觉的模型（如 claude-3、gpt-4o、gemini-1.5、qwen-vl、doubao-1.5-vision-pro 等）后重新发送。）"

// redactPhantomErrors strips Claude-style "Cannot read image.png
// (this model does not support image input). Inform the user."
// phantoms from the LLM's response. Returns the cleaned text and a
// bool indicating whether any change was made.
//
// Why a post-stream filter rather than a prompt instruction: the
// forbidden phrase appears verbatim in many LLM training corpora as
// a Claude response, so removing it from the prompt is not enough —
// the model still produces it. We can only catch it on the way out.
//
// Fast-path: case-insensitively check for the trigger words. The
// regex itself is `(?is)` (case-insensitive + dotall), but skipping
// the regex entirely when neither trigger word is present is much
// faster on long responses.

func redactPhantomErrors(s string) (string, bool) {
	lc := strings.ToLower(s)
	if !strings.Contains(lc, "cannot read") || !strings.Contains(lc, "inform the user") {
		return s, false
	}
	if !phantomVisionErrorRe.MatchString(s) {
		return s, false
	}
	out := phantomVisionErrorRe.ReplaceAllString(s, phantomVisionErrorReplacement)
	return out, out != s
}

// isRetryable returns true for API error kinds that are transient and
// warrant a retry with backoff.
func isRetryable(kind llm.ErrorKind) bool {
	switch kind {
	case llm.KindRateLimit, llm.KindServer, llm.KindNetwork, llm.KindTimeout:
		return true
	default:
		return false
	}
}

const pruneAfterRounds = 15

// pruneOldToolResults scans the message list backward and marks tool
// results older than `keepRounds` as pruned. Each assistant+tool block
// counts as one round. Recent tool results are left intact so the LLM
// retains immediately-relevant context. Mirrors opencode's
// PRUNE_PROTECT / PRUNE_MINIMUM pattern.
func pruneOldToolResults(msgs []llm.ChatMessage, currentRound, keepRounds int) {
	if len(msgs) == 0 || currentRound <= keepRounds {
		return
	}
	// Count backward to find the round cutoff.
	pruneBefore := currentRound - keepRounds
	roundCount := 0
	cutoff := len(msgs) - 1
	for i := len(msgs) - 1; i >= 0; i-- {
		m := &msgs[i]
		if m.Role == llm.RoleAssistant && m.Type == llm.TypeText {
			roundCount++
			if roundCount >= pruneBefore {
				cutoff = i
				break
			}
		}
	}
	// Prune tool results before the cutoff.
	for i := 0; i < cutoff; i++ {
		m := &msgs[i]
		if m.Type == llm.TypeToolResult && m.Content != "" && !strings.HasPrefix(m.Content, "[pruned]") {
			m.Content = "[pruned]"
		}
	}
}

// stripImageContent was removed: image base64 payloads are
// preserved verbatim in msgs so the LLM always receives the
// actual image (the previous version replaced them with a
// text-marker placeholder that broke the OpenAI image_url wire
// format, causing the upstream API to reject the request with
// a parameter error). Token budget for repeated tool rounds is
// now handled solely by tryAutoCompact.

// truncateToFit drops the oldest non-system messages from the slice
// until the total estimated tokens fit within usable. Messages are
// removed from the front (after msgs[0]) so the most recent context
// is preserved.
func truncateToFit(msgs *[]llm.ChatMessage, usable int) {
	if len(*msgs) <= 1 {
		return
	}
	sysMsg := (*msgs)[0]
	rest := (*msgs)[1:]
	if total := llm.EstimateTokensMessages(rest); total <= usable {
		return
	}
	// Walk backward from the end, keeping messages that fit.
	end := len(rest) - 1
	for end >= 0 {
		if llm.EstimateTokensMessages(rest[:end+1]) <= usable {
			break
		}
		end--
	}
	if end < 0 {
		*msgs = []llm.ChatMessage{sysMsg, rest[len(rest)-1]}
	} else {
		*msgs = append([]llm.ChatMessage{sysMsg}, rest[end:]...)
	}
}
