package im

import (
	"context"
	"fmt"

	"github.com/p-chat/pchat/internal/config"
)

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

// RendererFactory 根据平台配置创建出站 renderer。
// RendererFactory creates an outbound renderer from platform config.
type RendererFactory func(platform config.IMPlatformConfig) (OutboundRenderer, error)

// ErrOutboundDisabled 表示平台配置未启用出站发送能力。
// ErrOutboundDisabled indicates outbound sending is disabled by platform config.
type ErrOutboundDisabled struct {
	Platform string
	Variant  string
	Reason   string
}

// Error 返回用户可读的出站禁用错误。
// Error returns a user-readable outbound disabled error.
func (e ErrOutboundDisabled) Error() string {
	key := e.Platform
	if e.Variant != "" {
		key = e.Platform + ":" + e.Variant
	}
	if e.Reason == "" {
		return fmt.Sprintf("im outbound disabled: %s", key)
	}
	return fmt.Sprintf("im outbound disabled: %s: %s", key, e.Reason)
}
