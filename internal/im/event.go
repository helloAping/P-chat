package im

import (
	"encoding/json"
	"time"
)

// ChatRef 标识一个 IM 平台聊天位置。
// ChatRef identifies a chat location on an IM platform.
type ChatRef struct {
	Platform string `json:"platform"`
	Variant  string `json:"variant,omitempty"`
	ChatID   string `json:"chat_id"`
	ChatType string `json:"chat_type,omitempty"`
	ThreadID string `json:"thread_id,omitempty"`
}

// SenderRef 标识 IM 消息发送者。
// SenderRef identifies the sender of an IM message.
type SenderRef struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name,omitempty"`
}

// Mention 描述一条 IM @ 提及。
// Mention describes one IM mention.
type Mention struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
	Bot  bool   `json:"bot,omitempty"`
}

// Attachment 描述一条规范化后的 IM 附件。
// Attachment describes a normalized IM attachment.
type Attachment struct {
	ID       string            `json:"id,omitempty"`
	Type     string            `json:"type"`
	Name     string            `json:"name,omitempty"`
	URL      string            `json:"url,omitempty"`
	MimeType string            `json:"mime_type,omitempty"`
	Bytes    int64             `json:"bytes,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// IMEvent 是 Gateway 消费的规范化入站事件。
// IMEvent is the normalized inbound event consumed by the Gateway.
type IMEvent struct {
	ID           string          `json:"id"`
	TraceID      string          `json:"trace_id,omitempty"`
	Platform     string          `json:"platform"`
	Variant      string          `json:"variant,omitempty"`
	Chat         ChatRef         `json:"chat"`
	Sender       SenderRef       `json:"sender"`
	ContextToken string          `json:"context_token,omitempty"`
	Text         string          `json:"text,omitempty"`
	Mentions     []Mention       `json:"mentions,omitempty"`
	ReplyTo      *string         `json:"reply_to,omitempty"`
	Attachments  []Attachment    `json:"attachments,omitempty"`
	Timestamp    time.Time       `json:"timestamp"`
	Raw          json.RawMessage `json:"raw,omitempty"`
}

// IMOutChunk 是 Gateway 发给平台 renderer 的规范化出站事件。
// IMOutChunk is the normalized outbound event sent to platform renderers.
type IMOutChunk struct {
	TraceID  string            `json:"trace_id,omitempty"`
	Platform string            `json:"platform"`
	Chat     ChatRef           `json:"chat"`
	MsgID    string            `json:"msg_id,omitempty"`
	Kind     string            `json:"kind"`
	Text     string            `json:"text,omitempty"`
	Parts    []any             `json:"parts,omitempty"`
	Done     bool              `json:"done,omitempty"`
	Error    string            `json:"error,omitempty"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

// LifecycleEvent 描述 Gateway 生命周期事件。
// LifecycleEvent describes a Gateway lifecycle event.
type LifecycleEvent struct {
	Time     time.Time `json:"time"`
	Type     string    `json:"type"`
	Platform string    `json:"platform,omitempty"`
	Variant  string    `json:"variant,omitempty"`
	Message  string    `json:"message,omitempty"`
	Error    string    `json:"error,omitempty"`
}
