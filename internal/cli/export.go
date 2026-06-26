package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/memory"
)

// ExportFormat identifies the output format of /export.
type ExportFormat string

const (
	FormatMarkdown ExportFormat = "markdown"
	FormatJSON     ExportFormat = "json"
)

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

// exportToMarkdown renders the conversation as human-readable
// markdown. The output is byte-stable for a given conversation +
// style + provider combo (so re-exports produce diff-friendly files).
func exportToMarkdown(conv *memory.Conversation, msgs []llm.Message) string {
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

	for i, m := range msgs {
		roleIcon := roleEmoji(m.Role)
		fmt.Fprintf(&b, "## %s %s · msg #%d\n\n", roleIcon, titleCase(m.Role), i+1)
		body := strings.TrimSpace(m.Content)
		if body != "" {
			// Triple-backtick code blocks render well in markdown.
			if looksLikeCode(body) {
				fmt.Fprintln(&b, "```")
				fmt.Fprintln(&b, body)
				fmt.Fprintln(&b, "```")
			} else {
				fmt.Fprintln(&b, body)
			}
			fmt.Fprintln(&b)
		}
		if m.Name != "" {
			fmt.Fprintf(&b, "*tool: `%s`*\n\n", m.Name)
		}
		if m.ToolCallID != "" {
			fmt.Fprintf(&b, "*tool_call_id: `%s`*\n\n", m.ToolCallID)
		}
	}
	return b.String()
}

// exportToJSON serializes the conversation as JSON. The schema is
// stable and machine-readable. The `messages` field is the raw
// LLM message list (no transformation), so downstream tooling can
// re-feed it to an LLM directly.
func exportToJSON(conv *memory.Conversation, msgs []llm.Message) (string, error) {
	out := struct {
		Version    string         `json:"version"`
		ExportedAt string         `json:"exported_at"`
		Session    map[string]any `json:"session"`
		Messages   []llm.Message  `json:"messages"`
	}{
		Version:    "pchat-export/1",
		ExportedAt: time.Now().UTC().Format(time.RFC3339),
		Session: map[string]any{
			"id":         conv.ID,
			"title":      conv.Title,
			"created_at": conv.CreatedAt,
			"updated_at": conv.UpdatedAt,
		},
		Messages: msgs,
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// doExport runs the full export pipeline: parse args, resolve session,
// fetch messages, format, write file. Returns the absolute path of
// the written file plus a short human summary.
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

	msgs := store.GetMessages()
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

// looksLikeCode returns true when the message content looks like
// source code (multiple lines, mostly non-text characters, no
// surrounding punctuation). Used by the markdown exporter to
// decide whether to wrap the body in a code fence.
func looksLikeCode(s string) bool {
	if !strings.Contains(s, "\n") {
		return false
	}
	// Heuristic: at least 2 lines and the content has at least one
	// of the tell-tale characters (`=`, `(`, `)`, `{`, `;`).
	for _, c := range []string{"=", "(", "{", ";"} {
		if strings.Contains(s, c) {
			return true
		}
	}
	return false
}
