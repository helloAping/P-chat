// Package stylegen 从当前对话自动生成/优化 AI 人格风格（persona）。
//
// Package stylegen generates or optimizes an AI persona style from a
// conversation. It is the pure-logic core shared by the HTTP API
// (internal/server/stylegen.go), the CLI /stylegen command and the
// background JobManager (jobs.go) — it never touches HTTP or the
// terminal, so it can be unit-tested in isolation with a mock LLM.
//
// Read-only contract: Generate only ever READS the source conversation
// (via memory.Store.GetChatMessagesWithMetaFor) and never writes any
// message or metadata back to it. The only writes performed are to the
// styles table (create a new style, or update / copy an existing one).
package stylegen

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"strings"

	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/memory"
	"github.com/p-chat/pchat/internal/style"
)

// defaultMaxChars is the transcript truncation cap used when Deps.MaxChars
// is zero. The excerpt keeps the NEWEST messages up to this many bytes and
// drops the oldest ones beyond the cap.
const defaultMaxChars = 40000

// LLMClient is the streaming LLM interface Generate needs. It is declared
// as an interface (instead of *llm.Client) so tests can substitute a fake
// that streams canned output through a channel.
type LLMClient interface {
	ChatStream(ctx context.Context, provider, model string, msgs []llm.Message) <-chan llm.StreamChunk
}

// Deps carries the read-only dependencies for a stylegen run.
type Deps struct {
	Store    *memory.Store  // reads conversation history (read-only)
	StyleMgr *style.Manager // creates / updates styles
	LLM      LLMClient      // streaming generation
	Provider string         // default provider name
	Model    string         // model override; empty = provider default
	MaxChars int            // transcript truncation cap; <=0 uses default
}

// Params is a single stylegen request.
type Params struct {
	Mode           string // "create" | "optimize"
	StyleID        string // optimize target style id
	Label          string // new / updated label (Chinese is fine)
	Requirement    string // user's extra requirements ("想改哪里" / "语气/人设/要记住的事")
	ConversationID string // source conversation (read-only snapshot)
}

// Result describes a finished stylegen run.
type Result struct {
	ID      string `json:"id"`
	Label   string `json:"label"`
	Prompt  string `json:"prompt"`
	Memory  string `json:"memory"`
	Mode    string `json:"mode"`
	Updated bool   `json:"updated"` // true = target updated in place; false = a new style was created
}

// ProgressEvent is streamed to the caller as the run progresses.
// A stage-transition event carries only Stage + Label (no Content /
// Thinking); streamed generation carries Content / Thinking with
// Stage set to "generating".
type ProgressEvent struct {
	Stage    string // reading | analyzing | generating | saving
	Label    string // human-readable stage label
	Content  string // streamed prompt/memory text delta
	Thinking string // streamed reasoning delta
}

// Generate runs a full stylegen pipeline:
//
//  1. read a read-only snapshot of the source conversation
//  2. clean + truncate it into a transcript excerpt (newest-first cap)
//  3. build the Chinese generation prompt (6-section persona skeleton +
//     user requirement + transcript; optimize mode also attaches the
//     original prompt + memory)
//  4. stream the LLM output, forwarding content/thinking deltas to emit
//  5. parse the JSON {id?, prompt, memory} response (fenced / bare /
//     whole-text fallback)
//  6. persist: create → Create(); optimize custom → Update() in place;
//     optimize built-in → copy to a new style (built-ins are read-only)
//
// emit may be nil. The context is threaded through; cancellation stops the
// LLM stream naturally.
func Generate(ctx context.Context, deps Deps, p Params, emit func(ProgressEvent)) (*Result, error) {
	if emit == nil {
		emit = func(ProgressEvent) {}
	}
	if deps.MaxChars <= 0 {
		deps.MaxChars = defaultMaxChars
	}

	mode := strings.ToLower(strings.TrimSpace(p.Mode))
	if mode == "" {
		mode = "create"
	}
	if mode != "create" && mode != "optimize" {
		return nil, ErrArgs(fmt.Errorf("mode must be create or optimize, got %q", p.Mode))
	}
	label := strings.TrimSpace(p.Label)
	labelGiven := label != ""
	// The label is the new style's name. It is required for create (there
	// is no existing style to inherit a name from), but optional for
	// optimize — an empty label keeps the target style's current name.
	if mode == "create" && label == "" {
		return nil, ErrArgs(errors.New("label is required for create"))
	}
	if mode == "optimize" && strings.TrimSpace(p.StyleID) == "" {
		return nil, ErrArgs(errors.New("optimize requires a style id"))
	}

	// ── optimize target: resolve the existing style (read-only) ──
	var (
		origPrompt, origMemory string
		targetIsBuiltin        bool
	)
	if mode == "optimize" {
		st := style.Style(strings.TrimSpace(p.StyleID))
		for _, b := range deps.StyleMgr.List() {
			if st == b {
				targetIsBuiltin = true
				break
			}
		}
		var err error
		origPrompt, err = deps.StyleMgr.GetSystemPrompt(st)
		if err != nil {
			return nil, ErrNotFound(fmt.Errorf("style %q not found: %w", st, err))
		}
		origMemory, _ = deps.StyleMgr.GetMemory(st)
		// optimize with no new name: keep the target's current name so the
		// generation prompt has a "# label" heading and the result reports
		// a meaningful label.
		if label == "" {
			label = deps.StyleMgr.DisplayLabel(st)
		}
	}

	// ── stage 1: read-only snapshot of the conversation ──
	emit(ProgressEvent{Stage: "reading", Label: "读取当前对话…"})
	transcript := buildTranscript(deps, p.ConversationID)
	if transcript == "" && mode == "create" && strings.TrimSpace(p.Requirement) == "" {
		return nil, ErrEmpty(errors.New("current conversation is empty; please chat a bit first, or add a requirement"))
	}

	// ── stage 2: build the generation prompt ──
	emit(ProgressEvent{Stage: "analyzing", Label: "分析对话与要求…"})
	genPrompt := buildGenerationPrompt(mode, label, p.Requirement, origPrompt, origMemory, transcript)

	// ── stage 3: stream generation ──
	emit(ProgressEvent{Stage: "generating", Label: "生成人格与记忆…"})
	raw, _, err := streamLLM(ctx, deps, genPrompt, emit)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrLLM(err)
	}

	// ── stage 4: parse + persist ──
	gen := parseGenOutput(raw)
	prompt := gen.Prompt
	if strings.TrimSpace(prompt) == "" {
		prompt = raw // whole-text fallback: treat the raw output as the prompt
	}
	memory := gen.Memory

	emit(ProgressEvent{Stage: "saving", Label: "保存风格…"})

	result := &Result{
		Label:  label,
		Prompt: prompt,
		Memory: memory,
		Mode:   mode,
	}

	switch mode {
	case "create":
		id := resolveNewID(gen.ID, label, label)
		st, err := deps.StyleMgr.Create(id, label, prompt, memory)
		if err != nil {
			return nil, dupOrErr(err)
		}
		result.ID = string(st)
		result.Updated = false

	case "optimize":
		if targetIsBuiltin {
			// Built-in styles are read-only (style.Manager.Update rejects
			// them). Copy to a NEW style instead; the original stays
			// untouched — this is the "内置只读，已另存新风格" branch.
			newID := resolveNewID(gen.ID, label, strings.TrimSpace(p.StyleID))
			// When the user didn't supply a name, mark the copy as an
			// optimized variant of the original (label was defaulted to the
			// original display name above).
			newLabel := label
			if !labelGiven {
				newLabel = deps.StyleMgr.DisplayLabel(style.Style(p.StyleID)) + "·优化版"
			}
			st, err := deps.StyleMgr.Create(newID, newLabel, prompt, memory)
			if err != nil {
				return nil, dupOrErr(err)
			}
			result.ID = string(st)
			result.Label = newLabel
			result.Updated = false
		} else {
			// Custom style: update in place.
			if err := deps.StyleMgr.Update(p.StyleID, label, prompt, memory); err != nil {
				return nil, err
			}
			result.ID = strings.TrimSpace(p.StyleID)
			result.Updated = true
		}
	}

	emit(ProgressEvent{Stage: "done", Label: "完成"})
	return result, nil
}

// buildTranscript reads a read-only snapshot of the conversation and
// renders a cleaned, truncated transcript (oldest-first). Only
// user/assistant text messages survive; tool calls, tool results,
// thinking blocks and command output are dropped, as are empty
// contents. When the total exceeds the cap the OLDEST messages are
// dropped first (newest messages win).
func buildTranscript(deps Deps, convID string) string {
	if convID == "" || deps.Store == nil {
		return ""
	}
	msgs, _, _ := deps.Store.GetChatMessagesWithMetaFor(convID, 0)
	if len(msgs) == 0 {
		return ""
	}

	// Collect candidate lines newest-first so we can keep the newest
	// and drop the oldest beyond the cap.
	var lines []string
	total := 0
	for i := len(msgs) - 1; i >= 0; i-- {
		m := msgs[i]
		if m.Role != llm.RoleUser && m.Role != llm.RoleAssistant {
			continue
		}
		if m.MsgType == llm.MsgTypeTool || m.MsgType == llm.MsgTypeCommand {
			continue
		}
		switch m.Type {
		case llm.TypeToolCall, llm.TypeToolResult, llm.TypeThinking, "command":
			continue
		}
		content := strings.TrimSpace(m.Content)
		if content == "" {
			continue
		}
		// Cap a single over-long message so it can't dominate the excerpt.
		const perMessageCap = 4000
		if len(content) > perMessageCap {
			content = content[:perMessageCap] + "…"
		}
		line := roleName(m.Role) + "：" + content
		if total+len(line) > deps.MaxChars {
			break
		}
		lines = append(lines, line)
		total += len(line)
	}

	// Reverse back to chronological order.
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return strings.Join(lines, "\n\n")
}

func roleName(role string) string {
	if role == llm.RoleAssistant {
		return "AI"
	}
	return "用户"
}

// buildGenerationPrompt composes the Chinese prompt handed to the LLM.
// Both modes share the "6-section persona skeleton + requirement +
// transcript" shape; optimize mode additionally attaches the original
// prompt + memory so the LLM can refine rather than rewrite.
func buildGenerationPrompt(mode, label, requirement, origPrompt, origMemory, transcript string) string {
	var b strings.Builder

	if mode == "optimize" {
		b.WriteString("你需要基于用户当前对话，优化下面这套已存在的 AI 人格风格（prompt）与记忆（memory）。\n")
		b.WriteString("保持原风格的核心人设与说话方式不变，结合对话中的最新内容与用户要求补充完善，修复不贴合的细节。\n\n")
	} else {
		b.WriteString("你需要根据用户当前对话，为 P-Chat 生成一套全新的 AI 人格风格（prompt）与记忆（memory）。\n\n")
	}

	b.WriteString("输出要求：只输出一个 JSON 对象，不要输出任何解释性文字，不要用 ``` 代码块围栏。\n")
	b.WriteString("JSON 结构：{\"id\": \"ascii_id\", \"prompt\": \"完整人格markdown\", \"memory\": \"用户偏好要点\"}\n\n")

	b.WriteString("字段要求：\n")
	b.WriteString("- id：仅由小写字母、数字、- _ . 组成的 ASCII 标识（禁止中文、禁止空格）。风格 label 的中文会被规范化成空串，所以你必须给出可用的 ASCII id，例如 gentle-teacher、code-sensei。\n")
	b.WriteString("- prompt：完整 markdown 人格。第一行是 \"# " + label + "\"，必须包含以下六段：## 人设 / ## 性格 / ## 说话风格 / ## 表达模板 / ## 禁止项 / ## 示例。示例用「用户：…」与「AI：…」的对话形式示范。\n")
	b.WriteString("- memory：从对话中提取用户的稳定偏好与事实（语气偏好、对 AI 的称呼、常用术语、正在追求的目标、禁忌话题等），用要点式，200-300 字。\n\n")

	b.WriteString("风格名称（label）：" + label + "\n")
	if requirement != "" {
		b.WriteString("用户补充要求：" + requirement + "\n")
	} else {
		b.WriteString("用户补充要求：无\n")
	}

	if mode == "optimize" {
		b.WriteString("\n--- 原风格 prompt ---\n" + origPrompt + "\n")
		if origMemory != "" {
			b.WriteString("\n--- 原风格 memory ---\n" + origMemory + "\n")
		}
	}

	b.WriteString("\n--- 对话摘录（参考，最新优先保留）---\n")
	if transcript == "" {
		b.WriteString("（当前对话为空）\n")
	} else {
		b.WriteString(transcript + "\n")
	}
	b.WriteString("\n--- 摘录结束 ---\n")
	return b.String()
}

// streamLLM drives the streaming call, forwarding content/thinking
// deltas to emit while accumulating the full text for JSON parsing.
func streamLLM(ctx context.Context, deps Deps, prompt string, emit func(ProgressEvent)) (string, string, error) {
	msgs := []llm.Message{{Role: llm.RoleUser, Content: prompt}}
	ch := deps.LLM.ChatStream(ctx, deps.Provider, deps.Model, msgs)

	var content, thinking strings.Builder
	for chunk := range ch {
		if chunk.Err != nil {
			return "", "", chunk.Err
		}
		if chunk.Done {
			break
		}
		if chunk.Content != "" {
			content.WriteString(chunk.Content)
			emit(ProgressEvent{Stage: "generating", Content: chunk.Content})
		}
		if chunk.Thinking != "" {
			thinking.WriteString(chunk.Thinking)
			emit(ProgressEvent{Stage: "generating", Thinking: chunk.Thinking})
		}
	}
	return content.String(), thinking.String(), nil
}

// genOutput is the JSON contract the LLM is asked to return.
type genOutput struct {
	ID     string `json:"id"`
	Prompt string `json:"prompt"`
	Memory string `json:"memory"`
}

// parseGenOutput extracts {id, prompt, memory} from the LLM's raw
// output. It tries, in order: fenced code block stripped + JSON parse,
// bare JSON parse, a JSON object embedded in surrounding prose, and
// finally the whole raw text as the prompt with empty memory.
func parseGenOutput(raw string) genOutput {
	raw = strings.TrimSpace(raw)

	// Strip a fenced code block (```json ... ```) if present. When the
	// raw text has no fence, stripped == raw — dedupe so we never probe
	// the same string twice.
	stripped := stripFence(raw)
	candidates := []string{stripped}
	if stripped != raw {
		candidates = append(candidates, raw)
	}

	for _, candidate := range candidates {
		var out genOutput
		if err := json.Unmarshal([]byte(candidate), &out); err == nil {
			return out
		}
		// Find a JSON object embedded in prose.
		if idx := strings.Index(candidate, "{"); idx >= 0 {
			if end := strings.LastIndex(candidate, "}"); end > idx {
				sub := candidate[idx : end+1]
				if err := json.Unmarshal([]byte(sub), &out); err == nil {
					return out
				}
			}
		}
	}

	// Fallback: whole text as prompt, memory empty.
	return genOutput{Prompt: raw}
}

// stripFence removes a leading/trailing ``` (optionally with a language
// tag) so the inner JSON can be parsed directly.
func stripFence(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "```") {
		if nl := strings.Index(s, "\n"); nl >= 0 {
			s = s[nl+1:]
		} else {
			s = ""
		}
	}
	if strings.HasSuffix(s, "```") {
		s = strings.TrimSpace(s[:len(s)-3])
	}
	return strings.TrimSpace(s)
}

// deriveID ASCII-normalises a label into a style id (lowercase, only
// a-z 0-9 - _ .). Chinese labels normalise to "", so the caller must
// fall back to an LLM-provided id or a generated one.
func deriveID(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// resolveNewID derives a style id that is guaranteed to survive
// style.Manager's normaliseStyleID (ASCII-only, never empty), so Create
// can never fail with "style id must contain at least one letter".
//
// Priority: LLM-provided id (ASCII-normalised) → label-derived → a
// deterministic hash-based fallback. The LLM is instructed to return an
// ASCII id but may still emit Chinese / punctuation-only ids — that is
// exactly the case this fallback chain absorbs. fallbackSeed anchors the
// hash (e.g. the target style id for an optimize-copy).
func resolveNewID(genID, label, fallbackSeed string) string {
	if id := deriveID(genID); id != "" {
		return id
	}
	if id := deriveID(label); id != "" {
		return id
	}
	return "style-" + shortHash(fallbackSeed)
}

// shortHash returns a stable 6-hex-digit suffix for fallback style ids
// (deterministic per input, so tests and re-runs behave consistently).
func shortHash(s string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(s))
	return fmt.Sprintf("%06x", h.Sum32())
}

// dupOrErr classifies a style.Manager write error: a duplicate /
// reserved / empty-id error surfaces as ErrDup (or ErrArgs for the
// empty-id case), everything else passes through untouched.
func dupOrErr(err error) error {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "reserved"),
		strings.Contains(msg, "already exists"):
		return ErrDup(err)
	case strings.Contains(msg, "must contain at least one letter"):
		return ErrArgs(err)
	default:
		return err
	}
}

// ───────────────────────── error model ─────────────────────────

// ErrorKind classifies stylegen failures. The string values double as
// the SSE `error_kind` codes (E_*), matching the existing tool error
// code style.
type ErrorKind string

const (
	KindEmpty    ErrorKind = "E_EMPTY"     // source conversation is empty and no requirement was given
	KindNotFound ErrorKind = "E_NOT_FOUND" // optimize target style does not exist
	KindLLM      ErrorKind = "E_LLM"       // LLM call / stream failed
	KindArgs     ErrorKind = "E_ARGS"      // invalid request arguments
	KindDup      ErrorKind = "E_DUP"       // generated style id already exists / reserved
)

// GenError is a stylegen error carrying an ErrorKind for API mapping.
type GenError struct {
	Kind ErrorKind
	Err  error
}

func (e *GenError) Error() string {
	if e.Err == nil {
		return string(e.Kind)
	}
	return string(e.Kind) + ": " + e.Err.Error()
}

func (e *GenError) Unwrap() error { return e.Err }

// ErrArgs builds an E_ARGS error.
func ErrArgs(err error) error { return &GenError{Kind: KindArgs, Err: err} }

// ErrNotFound builds an E_NOT_FOUND error.
func ErrNotFound(err error) error { return &GenError{Kind: KindNotFound, Err: err} }

// ErrLLM builds an E_LLM error.
func ErrLLM(err error) error { return &GenError{Kind: KindLLM, Err: err} }

// ErrEmpty builds an E_EMPTY error.
func ErrEmpty(err error) error { return &GenError{Kind: KindEmpty, Err: err} }

// ErrDup builds an E_DUP error.
func ErrDup(err error) error { return &GenError{Kind: KindDup, Err: err} }

// KindOf extracts the ErrorKind from an error chain. Returns "" for
// errors that are not GenErrors.
func KindOf(err error) ErrorKind {
	var ge *GenError
	if errors.As(err, &ge) {
		return ge.Kind
	}
	return ""
}
