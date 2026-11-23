package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/p-chat/pchat/internal/export"
	"github.com/p-chat/pchat/internal/httpcli"
	"github.com/p-chat/pchat/internal/memory"
)

// Format is the CLI's alias for the export format
// enum. Re-exported so the existing CLI command
// registrations and tests don't have to switch the
// spelling in one shot.
type Format = export.Format

const (
	FormatMarkdown = export.FormatMarkdown
	FormatHTML     = export.FormatHTML
	FormatPDF      = export.FormatPDF
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
		case lower == "html":
			opts.format = FormatHTML
		case lower == "pdf":
			opts.format = FormatPDF
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
	ext := string(format)
	if format == FormatMarkdown {
		ext = "md"
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

	var body []byte
	switch opts.format {
	case FormatMarkdown:
		body = []byte(export.ToMarkdown(conv, msgs))
	case FormatHTML:
		body = []byte(export.ToHTML(conv, msgs))
	case FormatPDF:
		body = export.ToPDF(conv, msgs)
	case FormatJSON:
		text, jsonErr := export.ToJSON(conv, msgs)
		err = jsonErr
		if err != nil {
			return "", err
		}
		body = []byte(text)
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

	if err := os.WriteFile(outPath, body, 0o644); err != nil {
		return "", fmt.Errorf("写文件失败: %w", err)
	}
	return outPath, nil
}

func doHTTPExport(ctx context.Context, client *httpcli.Client, currentSessionID, projectPath, args string) (string, error) {
	opts := parseExportArgs(args)
	sessionID, err := resolveHTTPExportSession(ctx, client, currentSessionID, projectPath, opts.sessionID)
	if err != nil {
		return "", err
	}
	file, err := client.ExportSession(ctx, sessionID, string(opts.format))
	if err != nil {
		return "", err
	}
	outPath := opts.outFile
	if outPath == "" {
		outPath = file.Filename
		if outPath == "" {
			outPath = defaultHTTPExportFilename(sessionID, opts.format)
		}
	}
	if !filepath.IsAbs(outPath) {
		cwd, getwdErr := os.Getwd()
		if getwdErr != nil {
			return "", fmt.Errorf("读取当前目录失败: %w", getwdErr)
		}
		outPath = filepath.Join(cwd, outPath)
	}
	if err := os.WriteFile(outPath, file.Body, 0o644); err != nil {
		return "", fmt.Errorf("写文件失败: %w", err)
	}
	return outPath, nil
}

func resolveHTTPExportSession(ctx context.Context, client *httpcli.Client, currentSessionID, projectPath, requestedID string) (string, error) {
	if requestedID == "" {
		if currentSessionID == "" {
			return "", fmt.Errorf("当前没有活跃会话")
		}
		return currentSessionID, nil
	}
	if requestedID == "last" {
		sessions, err := client.ListSessionsForProject(ctx, &projectPath)
		if err != nil {
			return "", err
		}
		if len(sessions) == 0 {
			return "", fmt.Errorf("当前项目范围没有会话可导出")
		}
		return sessions[0].ID, nil
	}
	session, err := client.GetSession(ctx, requestedID)
	if err != nil {
		return "", fmt.Errorf("读取会话失败: %w", err)
	}
	if session.ProjectPath != projectPath {
		return "", fmt.Errorf("会话 %s 不属于当前项目范围", requestedID)
	}
	return session.ID, nil
}

func defaultHTTPExportFilename(sessionID string, format export.Format) string {
	shortID := sessionID
	if len(shortID) > 12 {
		shortID = shortID[:12]
	}
	return fmt.Sprintf("pchat-%s.%s", shortID, exportExtension(format))
}

func exportExtension(format export.Format) string {
	if format == FormatMarkdown {
		return "md"
	}
	return string(format)
}
