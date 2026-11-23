package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/p-chat/pchat/internal/knowledge"
)

// AttachmentReadAsset 是宿主 Agent 已按当前会话校验的上传文件。
// AttachmentReadAsset is an upload path already validated by the host agent.
type AttachmentReadAsset struct {
	UploadID string
	Name     string
	MIME     string
	Path     string
	Size     int64
}

// AttachmentReadResolver 仅解析属于当前会话的上传，绝不接受任意文件路径。
// AttachmentReadResolver resolves uploads only within the active conversation.
type AttachmentReadResolver func(context.Context, string, string) (AttachmentReadAsset, error)

type attachmentReadResolverKey struct{}

// WithAttachmentReadResolver 注入 read_attachment 使用的会话级解析器。
// WithAttachmentReadResolver attaches the conversation-scoped resolver.
func WithAttachmentReadResolver(ctx context.Context, resolver AttachmentReadResolver) context.Context {
	if resolver == nil {
		return ctx
	}
	return context.WithValue(ctx, attachmentReadResolverKey{}, resolver)
}

type readAttachmentArgs struct {
	UploadID  string   `json:"upload_id"`
	UploadIDs []string `json:"upload_ids"`
	Offset    int      `json:"offset,omitempty"`
	Limit     int      `json:"limit,omitempty"`
}

const (
	maxAttachmentReadFiles = 8
	maxAttachmentReadBytes = 24 << 10
)

var readableAttachmentExtensions = map[string]struct{}{
	".txt": {}, ".md": {}, ".csv": {}, ".json": {}, ".yaml": {}, ".yml": {}, ".xml": {},
	".html": {}, ".htm": {}, ".js": {}, ".ts": {}, ".tsx": {}, ".jsx": {}, ".go": {},
	".py": {}, ".rs": {}, ".java": {}, ".c": {}, ".cpp": {}, ".h": {}, ".hpp": {},
	".cs": {}, ".rb": {}, ".php": {}, ".sh": {}, ".bash": {}, ".zsh": {}, ".ps1": {},
	".ini": {}, ".toml": {}, ".env": {}, ".log": {}, ".sql": {}, ".css": {}, ".scss": {},
	".less": {}, ".vue": {}, ".svelte": {}, ".swift": {}, ".kt": {}, ".scala": {}, ".r": {},
	".pdf": {}, ".docx": {}, ".docm": {}, ".xlsx": {}, ".xlsm": {}, ".pptx": {}, ".pptm": {},
}

// SupportsAttachmentRead 判断文件名或 MIME 是否存在确定性的文本提取器。
// SupportsAttachmentRead reports whether read_attachment can extract the file.
func SupportsAttachmentRead(name, mimeType string) bool {
	if _, ok := readableAttachmentExtensions[strings.ToLower(filepath.Ext(name))]; ok {
		return true
	}
	mimeType = strings.ToLower(strings.TrimSpace(strings.SplitN(mimeType, ";", 2)[0]))
	return strings.HasPrefix(mimeType, "text/") || mimeType == "application/json" ||
		mimeType == "application/xml" || mimeType == "application/yaml"
}

func handleReadAttachment(ctx context.Context, argsRaw json.RawMessage) (*CallResult, error) {
	var args readAttachmentArgs
	if err := json.Unmarshal(argsRaw, &args); err != nil {
		return &CallResult{Content: "invalid arguments: " + err.Error(), IsError: true}, nil
	}
	ids := make([]string, 0, 1+len(args.UploadIDs))
	seen := make(map[string]struct{}, 1+len(args.UploadIDs))
	for _, id := range append([]string{args.UploadID}, args.UploadIDs...) {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, exists := seen[id]; !exists {
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return &CallResult{Content: "upload_id or upload_ids is required", IsError: true}, nil
	}
	if len(ids) > maxAttachmentReadFiles {
		return &CallResult{Content: fmt.Sprintf("read_attachment accepts at most %d upload_ids", maxAttachmentReadFiles), IsError: true}, nil
	}
	if args.Offset < 0 || args.Limit < 0 {
		return &CallResult{Content: "offset and limit must be non-negative", IsError: true}, nil
	}
	sessionID, _ := ctx.Value(SessionIDKey{}).(string)
	resolver, _ := ctx.Value(attachmentReadResolverKey{}).(AttachmentReadResolver)
	if sessionID == "" || resolver == nil {
		return &CallResult{Content: "read_attachment is not available in this session", IsError: true}, nil
	}
	perFileLimit := args.Limit
	if perFileLimit == 0 {
		perFileLimit = maxAttachmentReadBytes / len(ids)
	}
	if perFileLimit > maxAttachmentReadBytes/len(ids) {
		perFileLimit = maxAttachmentReadBytes / len(ids)
	}
	var out strings.Builder
	succeeded := 0
	failed := 0
	for _, id := range ids {
		asset, err := resolver(ctx, sessionID, id)
		if err != nil {
			failed++
			fmt.Fprintf(&out, "Attachment upload_id=%s: [error: failed to resolve upload: %v]\n", id, err)
			continue
		}
		if !SupportsAttachmentRead(asset.Name, asset.MIME) {
			failed++
			fmt.Fprintf(&out, "Attachment %s (upload_id=%s): [error: unsupported attachment format (%s)]\n", asset.Name, id, asset.MIME)
			continue
		}
		asset.Name = truncateAttachmentLabel(asset.Name, 256)
		if err := ctx.Err(); err != nil {
			return &CallResult{Content: "read_attachment cancelled: " + err.Error(), IsError: true}, nil
		}
		content, err := knowledge.ReadFileText(asset.Path)
		if err != nil {
			failed++
			fmt.Fprintf(&out, "Attachment %s (upload_id=%s): [error: extraction failed: %v]\n", asset.Name, id, err)
			continue
		}
		content = strings.TrimSpace(content)
		if content == "" {
			failed++
			fmt.Fprintf(&out, "Attachment %s (upload_id=%s): [error: no extractable text; the PDF may be scanned/image-only and OCR is not available]\n", asset.Name, id)
			continue
		}
		succeeded++
		if args.Offset >= len(content) {
			fmt.Fprintf(&out, "Attachment %s (upload_id=%s): [range bytes %d-%d of %d; no content; next offset=%d]\n", asset.Name, asset.UploadID, args.Offset, args.Offset, len(content), len(content))
			continue
		}
		start, end := utf8AttachmentRange(content, args.Offset, perFileLimit)
		next := end
		fmt.Fprintf(&out, "Attachment %s (upload_id=%s): [range bytes %d-%d of %d; next offset=%d]\n%s\n", asset.Name, asset.UploadID, start, end, len(content), next, content[start:end])
	}
	content := strings.TrimSpace(out.String())
	if len(content) > maxAttachmentReadBytes {
		_, end := utf8AttachmentRange(content, 0, maxAttachmentReadBytes)
		content = content[:end]
	}
	return &CallResult{Content: content, IsError: succeeded == 0, Summary: fmt.Sprintf("Read %d attachment(s); %d failed", succeeded, failed)}, nil
}

func truncateAttachmentLabel(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	_, end := utf8AttachmentRange(value, 0, limit)
	return value[:end]
}

func utf8AttachmentRange(text string, offset, limit int) (int, int) {
	start := offset
	if start < 0 {
		start = 0
	}
	if start > len(text) {
		start = len(text)
	}
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	end := start + limit
	if limit <= 0 || end > len(text) {
		end = len(text)
	}
	for end > start && end < len(text) && !utf8.RuneStart(text[end]) {
		end--
	}
	if end == start && start < len(text) {
		_, width := utf8.DecodeRuneInString(text[start:])
		end = start + width
	}
	return start, end
}
