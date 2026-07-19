package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/p-chat/pchat/internal/memory"
)

// ExportFormat identifies the output format of /export.
type ExportFormat string

const (
	FormatMarkdown ExportFormat = "markdown"
	FormatJSON     ExportFormat = "json"
)

// Schema version for the exported envelope. Bumped to /2
// alongside the attachment + parts[] additions — the v1
// schema only carried llm.Message rows without any
// rendering metadata.
const exportSchemaVersion = "pchat-export/2"

// exportOpts holds the parsed command-line options for /export.
type exportOpts struct {
	format ExportFormat
	// sessionID is either a specific conversation id, "" (use current),
	// or "last" (use the most recent conversation).
	sessionID string
	outFile   string // empty = auto-generate
}

// parseExportArgs parses the /export command's argument string.
//
//	/export                       → current session, markdown, auto filename
//	/export markdown             → current session, markdown, auto filename
//	/export json conv_xxx         → conv_xxx, json, auto filename
//	/export -o chat.md            → current session, markdown, ./chat.md
//	/export json -o chat.json     → current session, json, ./chat.json
func parseExportArgs(s string) exportOpts {
	opts := exportOpts{format: FormatMarkdown}
	fields := strings.Fields(s)

	for i := 0; i < len(fields); i++ {
		f := fields[i]
		lower := strings.ToLower(f)
		switch {
		case f == "-o" || f == "--output":
			if i+1 < len(fields) {
				opts.outFile = fields[i+1]
				i++
			}
		case lower == "markdown" || lower == "md":
			opts.format = FormatMarkdown
		case lower == "json":
			opts.format = FormatJSON
		default:
			// First positional = session spec
			if opts.sessionID == "" {
				opts.sessionID = f
			}
		}
	}
	return opts
}

// resolveSession finds the conversation to export.
//
//	id       → look up directly
//	"last"   → most recent conversation
//	""       → current session
//	missing  → current session
func resolveSession(store *memory.Store, id string) (*memory.Conversation, error) {
	if id == "last" || id == "" {
		// Current is updated as the user chats; "last" picks the
		// most recently updated conversation.
		if id == "last" {
			convs := store.ListConversations()
			if len(convs) == 0 {
				return nil, fmt.Errorf("没有任何会话可导出")
			}
			return &convs[0], nil
		}
		current := store.CurrentConversationID()
		if current == "" {
			return nil, fmt.Errorf("当前没有活跃会话")
		}
		id = current
	}

	convs := store.ListConversations()
	for i := range convs {
		if convs[i].ID == id {
			return &convs[i], nil
		}
	}
	return nil, fmt.Errorf("会话 %s 不存在", id)
}

// defaultExportFilename produces a sensible filename when the user
// didn't supply one. Format:
//
//	pchat-<short-id>-<YYYYMMDD-HHMMSS>.md
func defaultExportFilename(conv *memory.Conversation, format ExportFormat) string {
	ext := "md"
	if format == FormatJSON {
		ext = "json"
	}
	shortID := conv.ID
	if len(shortID) > 12 {
		shortID = shortID[:12]
	}
	ts := time.Now().Format("20060102-150405")
	return fmt.Sprintf("pchat-%s-%s.%s", shortID, ts, ext)
}

// ====================================================================
// Markdown rendering
// ====================================================================

// exportPart is the wire shape used for the markdown
// renderer's part-by-part walk. Mirrors the agent's
// MessagePart struct (we duplicate it here to avoid pulling
// the entire server package into the CLI binary, which would
// drag in gin + every internal subpackage). Only the fields
// the markdown writer actually needs are present.
type exportPart struct {
	Kind   string        `json:"kind"`
	Text   string        `json:"text,omitempty"`
	Name   string        `json:"name,omitempty"`
	Args   string        `json:"args,omitempty"`
	Status string        `json:"status,omitempty"`
	Result string        `json:"result,omitempty"`
	Error  string        `json:"error,omitempty"`
	Task   string        `json:"task,omitempty"`
	Parts  []exportPart  `json:"parts,omitempty"`
	// QuestionStatus is preserved so the markdown writer
	// can render a brief "the LLM asked this" marker.
	QuestionStatus string `json:"question_status,omitempty"`
}

// attachmentToMarkdown renders a single user-uploaded file
// (sourced from the message's multi_content metadata) as a
// markdown block.
//
//   - image_url  → !\[name\](data:image/...) inline; https
//     URLs are kept as link text since we can't inline
//     remote content
//   - audio_url / video_url → link with a 🔊/🎬 prefix
//     (markdown has no native media syntax)
//   - text       → fenced code block, MIME-tagged when
//     we can guess a language (json, csv, …)
func attachmentToMarkdown(att memory.Attachment) string {
	if att.URL == "" {
		return ""
	}
	name := att.Name
	if name == "" {
		name = att.Type
	}
	if att.Type == "image_url" {
		if strings.HasPrefix(att.URL, "data:") || strings.HasPrefix(att.URL, "http") {
			return fmt.Sprintf("![%s](%s)\n\n", name, att.URL)
		}
		return fmt.Sprintf("[🖼 %s](%s)\n\n", name, att.URL)
	}
	if att.Type == "audio_url" {
		return fmt.Sprintf("[🔊 %s](%s)\n\n", name, att.URL)
	}
	if att.Type == "video_url" {
		return fmt.Sprintf("[🎬 %s](%s)\n\n", name, att.URL)
	}
	if att.Type == "text" {
		// The text body lives in the URL field for
		// type=text attachments (see
		// memory.attachmentsFromMultiContent). Use the
		// MIME as a code-block language hint where it
		// maps cleanly to a known syntax.
		lang := langForMime(att.Mime)
		body := att.URL
		if body == "" {
			return fmt.Sprintf("*_(empty file: %s)_*\n\n", name)
		}
		return fmt.Sprintf("```%s\n%s\n```\n\n*— %s*\n\n", lang, body, name)
	}
	return fmt.Sprintf("[📎 %s](%s)\n\n", name, att.URL)
}

// langForMime maps a MIME type to a fenced-code language tag.
// Empty string means "no language" (renders as plain
// preformatted text in most viewers).
func langForMime(mime string) string {
	m := strings.ToLower(mime)
	switch m {
	case "application/json", "":
		// Don't tag plain text/* as 'json' even when the
		// mime is unknown; fall through to default.
		if m == "application/json" {
			return "json"
		}
	case "text/markdown", "text/x-markdown":
		return "markdown"
	case "text/yaml", "application/x-yaml":
		return "yaml"
	case "text/xml", "application/xml":
		return "xml"
	case "text/html":
		return "html"
	case "text/csv":
		return "csv"
	case "application/javascript", "text/javascript":
		return "javascript"
	case "text/x-go":
		return "go"
	case "text/x-python", "application/x-python":
		return "python"
	case "text/x-rust":
		return "rust"
	case "text/x-shellscript", "application/x-sh":
		return "bash"
	case "text/x-sql":
		return "sql"
	}
	if strings.HasSuffix(m, "+json") {
		return "json"
	}
	if strings.HasPrefix(m, "text/") {
		return "text"
	}
	return ""
}

// partToMarkdown renders one MessagePart as markdown. The
// recursion handles sub_agent.Parts (nested) and the
// `depth` argument indents each level so a deeply-nested
// sub-agent run reads cleanly.
func partToMarkdown(p exportPart, depth int) string {
	indent := strings.Repeat("  ", depth)
	switch p.Kind {
	case "text":
		return p.Text
	case "thinking":
		// Indent subsequent lines so the details
		// block doesn't get re-flowed by markdown
		// list/quote rules.
		body := strings.ReplaceAll(p.Text, "\n", "\n"+indent)
		return fmt.Sprintf("%s<details><summary>💭 thinking</summary>\n\n%s%s\n\n%s</details>\n\n",
			indent, indent, body, indent)
	case "tool":
		head := fmt.Sprintf("%s🔧 **%s** — `%s`", indent, p.Name, p.Status)
		if p.Args != "" {
			head += fmt.Sprintf("\n\n%s```json\n%s%s\n%s```",
				indent, indent, p.Args, indent)
		}
		if p.Result != "" {
			// Use the same sniff heuristic as the
			// frontend so base64 images render as
			// <img>, JSON as json-fenced, etc.
			head += "\n\n" + resultBlockToMarkdown(p.Result, indent)
		}
		if p.Error != "" {
			head += fmt.Sprintf("\n\n%s> ❌ %s", indent, p.Error)
		}
		return head + "\n\n"
	case "sub_agent":
		head := fmt.Sprintf("%s### 🤖 sub-agent: %s (%s)\n\n",
			indent, p.Task, p.Status)
		var body strings.Builder
		for _, inner := range p.Parts {
			body.WriteString(partToMarkdown(inner, depth+1))
		}
		return head + body.String()
	case "question":
		return fmt.Sprintf("%s> ❓ question (%s)\n\n",
			indent, defaultStr(p.QuestionStatus, "open"))
	default:
		return ""
	}
}

// resultBlockToMarkdown is the CLI mirror of the
// frontend's resultSniff.ts: a small heuristic that picks
// the right code-fence language for a tool result string.
// Base64 data: URLs are inlined as markdown images so the
// reader sees the screenshot directly.
func resultBlockToMarkdown(s, indent string) string {
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "data:image/") {
		return fmt.Sprintf("%s![tool result](%s)\n\n", indent, s)
	}
	if isJSON(s) {
		return fmt.Sprintf("%s```json\n%s%s\n%s```\n\n", indent, indent, s, indent)
	}
	if looksLikeCode(s) {
		return fmt.Sprintf("%s```\n%s%s\n%s```\n\n", indent, indent, s, indent)
	}
	if strings.Contains(s, "\n") {
		// Multi-line prose → blockquote.
		var b strings.Builder
		for _, line := range strings.Split(s, "\n") {
			b.WriteString(fmt.Sprintf("%s> %s\n", indent, line))
		}
		b.WriteString("\n")
		return b.String()
	}
	return indent + s + "\n\n"
}

func isJSON(s string) bool {
	t := strings.TrimSpace(s)
	if t == "" {
		return false
	}
	first, last := t[0], t[len(t)-1]
	if (first != '{' && first != '[') || (last != '}' && last != ']') {
		return false
	}
	var v any
	return json.Unmarshal([]byte(t), &v) == nil
}

func looksLikeCode(s string) bool {
	if !strings.Contains(s, "\n") {
		return false
	}
	for _, c := range []string{"=", "(", "{", ";", ":"} {
		if strings.Contains(s, c) {
			return true
		}
	}
	return false
}

func defaultStr(s, dflt string) string {
	if s == "" {
		return dflt
	}
	return s
}

// exportToMarkdown renders the conversation as human-readable
// markdown. The output is byte-stable for a given conversation +
// style + provider combo (so re-exports produce diff-friendly
// files).
//
// v2 changes (vs /export/1):
//   - Attachments are inlined as markdown images / links /
//     code blocks at the top of each message
//   - The assistant message's `parts` array (thinking /
//     tool / sub_agent / question) is rendered when
//     available; falls back to the legacy `content` field
//     for messages that pre-date the parts snapshot
func exportToMarkdown(conv *memory.Conversation, msgs []memory.MessageFull) string {
	var b strings.Builder

	title := conv.Title
	if title == "" {
		title = "(untitled)"
	}
	fmt.Fprintf(&b, "# %s\n\n", title)
	fmt.Fprintf(&b, "- **Session ID**: `%s`\n", conv.ID)
	fmt.Fprintf(&b, "- **Created**: %s\n", conv.CreatedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "- **Updated**: %s\n", conv.UpdatedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "- **Messages**: %d\n\n", len(msgs))
	fmt.Fprintln(&b, "---")
	fmt.Fprintln(&b)

	for i, mf := range msgs {
		m := mf.Msg
		roleIcon := roleEmoji(m.Role)
		fmt.Fprintf(&b, "## %s %s · msg #%d\n\n", roleIcon, titleCase(m.Role), i+1)
		// Attachments first (so the reader sees the
		// upload before the question / response that
		// references it).
		for _, att := range mf.Attachments {
			b.WriteString(attachmentToMarkdown(att))
		}
		// Parts (assistant) when present; legacy
		// `content` field otherwise.
		if len(mf.Parts) > 0 {
			var parts []exportPart
			if err := json.Unmarshal(mf.Parts, &parts); err == nil {
				for _, p := range parts {
					b.WriteString(partToMarkdown(p, 0))
				}
			} else {
				// Fall back to content when
				// the parts blob is
				// unparseable — better to emit
				// *something* than nothing.
				b.WriteString(renderBody(m.Content))
			}
		} else {
			b.WriteString(renderBody(m.Content))
		}
		if m.Name != "" {
			fmt.Fprintf(&b, "*tool: `%s`*\n\n", m.Name)
		}
		if m.ToolCallID != "" {
			fmt.Fprintf(&b, "*tool_call_id: `%s`*\n\n", m.ToolCallID)
		}
		// Thinking persisted as a top-level field
		// (older rows) gets a details block so the
		// reader can still see it.
		if mf.Thinking != "" {
			fmt.Fprintf(&b, "<details><summary>💭 thinking</summary>\n\n%s\n\n</details>\n\n",
				mf.Thinking)
		}
	}
	return b.String()
}

// renderBody applies the same "wrap code in a fence"
// heuristic the original export used. Kept as a small
// helper so the main loop reads cleanly.
func renderBody(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return ""
	}
	if looksLikeCode(body) {
		return "```\n" + body + "\n```\n\n"
	}
	return body + "\n\n"
}

// ====================================================================
// JSON rendering
// ====================================================================

// exportJSONEnvelope is the v2 wire shape. Fields:
//
//   - Version: schema id, always "pchat-export/2"
//   - ExportedAt: RFC3339 UTC timestamp
//   - Session: free-form session metadata (id, title,
//     timestamps) for downstream tooling that wants to
//     re-link the export to the original conversation
//   - Messages: array of exportJSONMessage (one per row)
//     in oldest-first order
type exportJSONEnvelope struct {
	Version    string              `json:"version"`
	ExportedAt string              `json:"exported_at"`
	Session    map[string]any      `json:"session"`
	Messages   []exportJSONMessage `json:"messages"`
}

// exportJSONMessage is the per-row shape inside the
// envelope. The Parts field carries the raw assistant
// `parts` JSON when present; Attachments carries the
// typed attachment list (always present, may be empty).
type exportJSONMessage struct {
	Index       int                `json:"index"`
	Role        string             `json:"role"`
	Content     string             `json:"content"`
	Thinking    string             `json:"thinking,omitempty"`
	Parts       json.RawMessage    `json:"parts,omitempty"`
	Attachments []memory.Attachment `json:"attachments"`
	ToolCallID  string             `json:"tool_call_id,omitempty"`
	Name        string             `json:"name,omitempty"`
	CreatedAt   int64              `json:"created_at,omitempty"`
}

// exportToJSON serializes the conversation as JSON. The
// schema is stable and machine-readable. The v2 envelope
// carries parts and attachments so downstream tooling
// can re-render the session with the same fidelity as the
// chat UI.
func exportToJSON(conv *memory.Conversation, msgs []memory.MessageFull) (string, error) {
	env := exportJSONEnvelope{
		Version:    exportSchemaVersion,
		ExportedAt: time.Now().UTC().Format(time.RFC3339),
		Session: map[string]any{
			"id":         conv.ID,
			"title":      conv.Title,
			"created_at": conv.CreatedAt,
			"updated_at": conv.UpdatedAt,
		},
		Messages: make([]exportJSONMessage, 0, len(msgs)),
	}
	for i, mf := range msgs {
		m := mf.Msg
		out := exportJSONMessage{
			Index:       i + 1,
			Role:        m.Role,
			Content:     m.Content,
			Thinking:    mf.Thinking,
			ToolCallID:  m.ToolCallID,
			Name:        m.Name,
			CreatedAt:   mf.CreatedAt,
			Attachments: mf.Attachments,
		}
		// Empty (or JSON null) parts get serialised as
		// "null" without the omitempty guard — the
		// shape is the same either way and consumers
		// can rely on `parts` being present on every
		// message.
		if len(mf.Parts) > 0 {
			out.Parts = mf.Parts
		}
		// Guarantee an empty (not nil) Attachments
		// slice so consumers can iterate without a
		// nil check.
		if out.Attachments == nil {
			out.Attachments = []memory.Attachment{}
		}
		env.Messages = append(env.Messages, out)
	}
	b, err := json.MarshalIndent(env, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// doExport runs the full export pipeline: parse args, resolve session,
// fetch messages, format, write file. Returns the absolute path of the
// written file plus a short human summary.
func doExport(store *memory.Store, args string) (string, error) {
	opts := parseExportArgs(args)

	conv, err := resolveSession(store, opts.sessionID)
	if err != nil {
		return "", err
	}

	// The Store doesn't expose a "load messages by id" method; we
	// have to switch the current conversation and read its
	// history. This is OK for single-process use (REPL or single
	// client); for multi-process support we'd need a dedicated
	// ConversationByID() method.
	prev := store.CurrentConversationID()
	if err := store.SetCurrent(conv.ID); err != nil {
		return "", fmt.Errorf("切到会话失败: %w", err)
	}
	defer func() {
		// Best-effort restore of previous current.
		if prev != "" && prev != conv.ID {
			_ = store.SetCurrent(prev)
		}
	}()

	msgs := store.GetMessagesFull()
	if len(msgs) == 0 {
		color.HiBlack("    (会话没有消息, 仍然导出空文件)")
	}

	var body string
	switch opts.format {
	case FormatMarkdown:
		body = exportToMarkdown(conv, msgs)
	case FormatJSON:
		body, err = exportToJSON(conv, msgs)
		if err != nil {
			return "", err
		}
	default:
		return "", fmt.Errorf("未知格式: %s", opts.format)
	}

	// Resolve output path.
	outPath := opts.outFile
	if outPath == "" {
		outPath = defaultExportFilename(conv, opts.format)
	}
	if !filepath.IsAbs(outPath) {
		cwd, _ := os.Getwd()
		outPath = filepath.Join(cwd, outPath)
	}

	if err := os.WriteFile(outPath, []byte(body), 0o644); err != nil {
		return "", fmt.Errorf("写文件失败: %w", err)
	}
	return outPath, nil
}

func roleEmoji(role string) string {
	switch role {
	case "user":
		return "🧑"
	case "assistant":
		return "🤖"
	case "system":
		return "⚙️"
	case "tool":
		return "🔧"
	default:
		return "•"
	}
}

func titleCase(s string) string {
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:]
}
