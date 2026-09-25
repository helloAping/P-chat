package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

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
	UploadID string `json:"upload_id"`
}

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
	args.UploadID = strings.TrimSpace(args.UploadID)
	if args.UploadID == "" {
		return &CallResult{Content: "upload_id is required", IsError: true}, nil
	}
	sessionID, _ := ctx.Value(SessionIDKey{}).(string)
	resolver, _ := ctx.Value(attachmentReadResolverKey{}).(AttachmentReadResolver)
	if sessionID == "" || resolver == nil {
		return &CallResult{Content: "read_attachment is not available in this session", IsError: true}, nil
	}
	asset, err := resolver(ctx, sessionID, args.UploadID)
	if err != nil {
		return &CallResult{Content: "read_attachment failed to resolve upload: " + err.Error(), IsError: true}, nil
	}
	if !SupportsAttachmentRead(asset.Name, asset.MIME) {
		return &CallResult{Content: fmt.Sprintf("unsupported attachment format: %s (%s)", asset.Name, asset.MIME), IsError: true}, nil
	}
	if err := ctx.Err(); err != nil {
		return &CallResult{Content: "read_attachment cancelled: " + err.Error(), IsError: true}, nil
	}
	content, err := knowledge.ReadFileText(asset.Path)
	if err != nil {
		return &CallResult{Content: "read_attachment failed: " + err.Error(), IsError: true}, nil
	}
	content = strings.TrimSpace(content)
	if content == "" {
		return &CallResult{Content: fmt.Sprintf("read_attachment found no extractable text in %s", asset.Name), IsError: true}, nil
	}
	return &CallResult{
		Content: fmt.Sprintf("Attachment %s (upload_id=%s):\n%s", asset.Name, asset.UploadID, content),
		Summary: fmt.Sprintf("Read attachment %s", asset.Name),
	}, nil
}
