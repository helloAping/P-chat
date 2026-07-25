package im

import "context"

// Adapter 是 IM 平台入站事件适配器。
// Adapter is an inbound adapter for an IM platform.
type Adapter interface {
	Platform() string
	Variants() []string
	Start(ctx context.Context, in chan<- IMEvent) error
	Stop(ctx context.Context) error
	Health() HealthStatus
}

// MarkdownDialect 描述平台支持的 Markdown 方言。
// MarkdownDialect describes the Markdown dialect a platform supports.
type MarkdownDialect string

const (
	MarkdownPlain      MarkdownDialect = "plain"
	MarkdownCommonMark MarkdownDialect = "commonmark"
	MarkdownTelegramV2 MarkdownDialect = "telegram_v2"
	MarkdownFeishuPost MarkdownDialect = "feishu_post"
)

// OutboundRenderer 是 IM 平台出站渲染器。
// OutboundRenderer is an outbound renderer for an IM platform.
type OutboundRenderer interface {
	Send(ctx context.Context, chunk IMOutChunk) error
	Edit(ctx context.Context, ref ChatRef, msgID string, chunk IMOutChunk) error
	Typing(ctx context.Context, ref ChatRef) error
	MaxTextLen() int
	MarkdownDialect() MarkdownDialect
}
