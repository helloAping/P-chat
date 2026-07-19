package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/p-chat/pchat/internal/export"
	"github.com/p-chat/pchat/internal/memory"
)

// Format is the CLI's alias for the export format
// enum. Re-exported so the existing CLI command
// registrations and tests don't have to switch the
// spelling in one shot.
type Format = export.Format

const (
	FormatMarkdown = export.FormatMarkdown
	FormatJSON     = export.FormatJSON
)

// exportOpts holds the parsed command-line options for
// /export.
type exportOpts struct {
	format export.Format
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

// defaultExportFilename produces a sensible filename
// when the user didn't supply one. Format:
//
//	pchat-<short-id>-<YYYYMMDD-HHMMSS>.md
func defaultExportFilename(conv *memory.Conversation, format export.Format) string {
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

// doExport runs the full export pipeline: parse args,
// resolve session, fetch messages, format, write file.
// Returns the absolute path of the written file plus a
// short human summary.
//
// The actual rendering is delegated to the
// internal/export package so the CLI and the HTTP
// /export endpoint produce byte-identical output.
func doExport(store *memory.Store, args string) (string, error) {
	opts := parseExportArgs(args)

	conv, err := resolveSession(store, opts.sessionID)
	if err != nil {
		return "", err
	}

	// Switch the store's current conversation so
	// GetMessagesFull reads the right row set. The
	// switch is restored on exit so the rest of the
	// REPL session isn't disturbed.
	prev := store.CurrentConversationID()
	if err := store.SetCurrent(conv.ID); err != nil {
		return "", fmt.Errorf("切到会话失败: %w", err)
	}
	defer func() {
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
		body = export.ToMarkdown(conv, msgs)
	case FormatJSON:
		body, err = export.ToJSON(conv, msgs)
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
