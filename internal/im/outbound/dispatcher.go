package outbound

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// MarkdownDialect 描述平台支持的 Markdown 方言。
// MarkdownDialect describes the Markdown dialect a platform supports.
type MarkdownDialect string

const (
	MarkdownPlain      MarkdownDialect = "plain"
	MarkdownCommonMark MarkdownDialect = "commonmark"
	MarkdownTelegramV2 MarkdownDialect = "telegram_v2"
	MarkdownFeishuPost MarkdownDialect = "feishu_post"
)

// ChatRef 标识一个出站平台聊天位置。
// ChatRef identifies a chat location for outbound delivery.
type ChatRef struct {
	Platform string
	Variant  string
	ChatID   string
	ChatType string
	ThreadID string
}

// Chunk 是 OutboundDispatcher 消费的规范化出站片段。
// Chunk is a normalized outbound fragment consumed by OutboundDispatcher.
type Chunk struct {
	TraceID  string
	Platform string
	Chat     ChatRef
	MsgID    string
	Kind     string
	Text     string
	Parts    []any
	Done     bool
	Error    string
	Metadata map[string]string
}

// Renderer 是 OutboundDispatcher 依赖的平台出站能力。
// Renderer is the platform outbound capability required by OutboundDispatcher.
type Renderer interface {
	Send(ctx context.Context, chunk Chunk) error
	Edit(ctx context.Context, ref ChatRef, msgID string, chunk Chunk) error
	Typing(ctx context.Context, ref ChatRef) error
	MaxTextLen() int
	MarkdownDialect() MarkdownDialect
}

// DefaultEditMinInterval 是流式 edit 的默认最小间隔。
// DefaultEditMinInterval is the default minimum interval between streaming edits.
const DefaultEditMinInterval = 500 * time.Millisecond

// Dispatcher 将规范化出站 chunk 派发到平台 renderer。
// Dispatcher dispatches normalized outbound chunks to a platform renderer.
type Dispatcher struct {
	renderer        Renderer
	editMinInterval time.Duration
	mu              sync.Mutex
	lastEdit        map[string]time.Time
}

// NewDispatcher 创建一个出站派发器。
// NewDispatcher creates an outbound dispatcher.
func NewDispatcher(renderer Renderer) *Dispatcher {
	return &Dispatcher{
		renderer:        renderer,
		editMinInterval: DefaultEditMinInterval,
		lastEdit:        map[string]time.Time{},
	}
}

// SetEditMinInterval 设置流式 edit 的最小间隔。
// SetEditMinInterval sets the minimum interval between streaming edits.
func (d *Dispatcher) SetEditMinInterval(interval time.Duration) {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.editMinInterval = interval
}

// Dispatch 派发一个出站 chunk。
// Dispatch dispatches one outbound chunk.
func (d *Dispatcher) Dispatch(ctx context.Context, chunk Chunk) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if d == nil || d.renderer == nil {
		return errors.New("outbound renderer is nil")
	}
	kind := strings.ToLower(strings.TrimSpace(chunk.Kind))
	switch kind {
	case "", "text":
		return d.sendTextChunks(ctx, chunk)
	case "edit":
		if err := d.waitEditTurn(ctx, chunk); err != nil {
			return err
		}
		return d.editTextChunks(ctx, chunk)
	case "typing":
		if err := d.renderer.Typing(ctx, chunk.Chat); err != nil {
			return fmt.Errorf("send im typing: %w", err)
		}
		return nil
	default:
		return fmt.Errorf("unsupported im outbound kind: %s", chunk.Kind)
	}
}

func (d *Dispatcher) sendTextChunks(ctx context.Context, chunk Chunk) error {
	parts := SplitText(chunk.Text, d.renderer.MaxTextLen())
	for i, text := range parts {
		next := chunk
		next.Kind = "text"
		next.MsgID = ""
		next.Text = text
		next.Parts = nil
		next.Done = chunk.Done && i == len(parts)-1
		if err := d.renderer.Send(ctx, next); err != nil {
			return fmt.Errorf("send im outbound: %w", err)
		}
	}
	return nil
}

func (d *Dispatcher) editTextChunks(ctx context.Context, chunk Chunk) error {
	parts := SplitText(chunk.Text, d.renderer.MaxTextLen())
	first := chunk
	first.Text = parts[0]
	first.Parts = nil
	first.Done = chunk.Done && len(parts) == 1
	if err := d.renderer.Edit(ctx, first.Chat, first.MsgID, first); err != nil {
		return fmt.Errorf("edit im outbound: %w", err)
	}
	for i, text := range parts[1:] {
		next := chunk
		next.Kind = "text"
		next.MsgID = ""
		next.Text = text
		next.Parts = nil
		next.Done = chunk.Done && i == len(parts[1:])-1
		if err := d.renderer.Send(ctx, next); err != nil {
			return fmt.Errorf("send im outbound continuation: %w", err)
		}
	}
	return nil
}

func (d *Dispatcher) waitEditTurn(ctx context.Context, chunk Chunk) error {
	d.mu.Lock()
	interval := d.editMinInterval
	if interval <= 0 {
		d.lastEdit[editThrottleKey(chunk)] = time.Now()
		d.mu.Unlock()
		return nil
	}
	key := editThrottleKey(chunk)
	wait := time.Until(d.lastEdit[key].Add(interval))
	if wait <= 0 {
		d.lastEdit[key] = time.Now()
		d.mu.Unlock()
		return nil
	}
	d.mu.Unlock()

	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("wait im edit throttle: %w", ctx.Err())
	case <-timer.C:
	}
	d.mu.Lock()
	d.lastEdit[key] = time.Now()
	d.mu.Unlock()
	return nil
}

func editThrottleKey(chunk Chunk) string {
	if chunk.MsgID != "" {
		return chunk.Chat.Platform + ":" + chunk.Chat.Variant + ":" + chunk.Chat.ChatID + ":" + chunk.MsgID
	}
	return chunk.Chat.Platform + ":" + chunk.Chat.Variant + ":" + chunk.Chat.ChatID
}
