package feishu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/im"
)

// ErrInvalidToken 表示飞书回调 token 验证失败。
// ErrInvalidToken indicates Feishu callback verification failed.
var ErrInvalidToken = errors.New("invalid feishu verification token")

// ErrUnsupportedEvent 表示飞书事件合法但当前 adapter 暂不处理。
// ErrUnsupportedEvent indicates a valid Feishu event is not handled yet.
var ErrUnsupportedEvent = errors.New("unsupported feishu event")

// ErrEncryptedCallbackUnsupported 表示当前竖切还不支持飞书加密回调。
// ErrEncryptedCallbackUnsupported indicates encrypted Feishu callbacks are not supported yet.
var ErrEncryptedCallbackUnsupported = errors.New("encrypted feishu callback unsupported")

// CallbackResult 是飞书回调解析结果。
// CallbackResult is the parsed result of a Feishu callback.
type CallbackResult struct {
	Challenge string
	Event     *im.IMEvent
}

// Adapter 是飞书 Bot v3 的 adapter 骨架。
// Adapter is the skeleton adapter for Feishu Bot v3.
type Adapter struct {
	cfg       config.IMPlatformConfig
	mu        sync.RWMutex
	started   bool
	startedAt time.Time
}

// NewAdapter 创建飞书 adapter。
// NewAdapter creates a Feishu adapter.
func NewAdapter(cfg config.IMPlatformConfig) *Adapter {
	if cfg.Variant == "" {
		cfg.Variant = "bot"
	}
	return &Adapter{cfg: cfg}
}

// Platform 返回平台名称。
// Platform returns the platform name.
func (a *Adapter) Platform() string { return "feishu" }

// Variants 返回支持的 adapter variant。
// Variants returns supported variants.
func (a *Adapter) Variants() []string { return []string{"bot"} }

// Start 将 webhook-ready adapter 标记为运行中。
// Start marks the webhook-ready skeleton adapter as running.
func (a *Adapter) Start(ctx context.Context, in chan<- im.IMEvent) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.started = true
	a.startedAt = time.Now()
	return nil
}

// Stop 将 adapter 标记为停止。
// Stop marks the adapter as stopped.
func (a *Adapter) Stop(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.started = false
	return nil
}

// Health 返回 adapter 健康状态。
// Health returns adapter health.
func (a *Adapter) Health() im.HealthStatus {
	a.mu.RLock()
	defer a.mu.RUnlock()
	status := "stopped"
	if a.started {
		status = "webhook_ready"
	}
	return im.HealthStatus{
		Platform:  "feishu",
		Variant:   "bot",
		Enabled:   a.cfg.Enabled,
		Status:    status,
		StartedAt: a.startedAt,
	}
}

// ParseCallback 解析飞书 URL verification 或消息回调。
// ParseCallback parses a Feishu URL verification or message callback.
func ParseCallback(data []byte, cfg config.IMPlatformConfig) (CallbackResult, error) {
	var probe struct {
		Encrypt   string `json:"encrypt"`
		Type      string `json:"type"`
		Token     string `json:"token"`
		Challenge string `json:"challenge"`
		Header    struct {
			Token     string `json:"token"`
			EventType string `json:"event_type"`
		} `json:"header"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return CallbackResult{}, fmt.Errorf("parse feishu callback: %w", err)
	}
	if probe.Encrypt != "" {
		return CallbackResult{}, ErrEncryptedCallbackUnsupported
	}
	token := probe.Token
	if token == "" {
		token = probe.Header.Token
	}
	if err := validateToken(token, cfg.VerificationToken); err != nil {
		return CallbackResult{}, err
	}
	if probe.Type == "url_verification" {
		if probe.Challenge == "" {
			return CallbackResult{}, errors.New("feishu url_verification missing challenge")
		}
		return CallbackResult{Challenge: probe.Challenge}, nil
	}
	if probe.Header.EventType != "im.message.receive_v1" {
		return CallbackResult{}, fmt.Errorf("%w: %s", ErrUnsupportedEvent, probe.Header.EventType)
	}
	ev, err := parseReceiveV1(data, cfg)
	if err != nil {
		return CallbackResult{}, err
	}
	return CallbackResult{Event: &ev}, nil
}

func validateToken(got, want string) error {
	if want == "" {
		return nil
	}
	if got != want {
		return ErrInvalidToken
	}
	return nil
}

type receiveV1Envelope struct {
	Header struct {
		EventID    string `json:"event_id"`
		EventType  string `json:"event_type"`
		CreateTime string `json:"create_time"`
	} `json:"header"`
	Event struct {
		Sender struct {
			SenderID feishuID `json:"sender_id"`
		} `json:"sender"`
		Message struct {
			MessageID   string          `json:"message_id"`
			RootID      string          `json:"root_id"`
			ParentID    string          `json:"parent_id"`
			CreateTime  string          `json:"create_time"`
			ChatID      string          `json:"chat_id"`
			ChatType    string          `json:"chat_type"`
			MessageType string          `json:"message_type"`
			Content     string          `json:"content"`
			Mentions    []feishuMention `json:"mentions"`
		} `json:"message"`
	} `json:"event"`
}

type feishuID struct {
	OpenID  string `json:"open_id"`
	UserID  string `json:"user_id"`
	UnionID string `json:"union_id"`
}

type feishuMention struct {
	Key  string   `json:"key"`
	ID   feishuID `json:"id"`
	Name string   `json:"name"`
}

func parseReceiveV1(data []byte, cfg config.IMPlatformConfig) (im.IMEvent, error) {
	var env receiveV1Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return im.IMEvent{}, fmt.Errorf("parse feishu receive_v1: %w", err)
	}
	msg := env.Event.Message
	if msg.MessageID == "" {
		return im.IMEvent{}, errors.New("feishu receive_v1 missing message_id")
	}
	if msg.MessageType != "text" {
		return im.IMEvent{}, fmt.Errorf("%w: message_type=%s", ErrUnsupportedEvent, msg.MessageType)
	}
	threadID := msg.RootID
	if threadID == "" {
		threadID = msg.ParentID
	}
	replyTo := optionalString(msg.ParentID)
	variant := normalizedVariant(cfg.Variant)
	return im.IMEvent{
		ID:       msg.MessageID,
		Platform: "feishu",
		Variant:  variant,
		Chat: im.ChatRef{
			Platform: "feishu",
			Variant:  variant,
			ChatID:   msg.ChatID,
			ChatType: msg.ChatType,
			ThreadID: threadID,
		},
		Sender: im.SenderRef{
			ID: firstNonEmpty(env.Event.Sender.SenderID.OpenID, env.Event.Sender.SenderID.UserID, env.Event.Sender.SenderID.UnionID),
		},
		Text:      parseText(msg.MessageType, msg.Content),
		Mentions:  parseMentions(msg.Mentions, botOpenID(cfg)),
		ReplyTo:   replyTo,
		Timestamp: parseMillis(firstNonEmpty(msg.CreateTime, env.Header.CreateTime)),
		Raw:       append([]byte(nil), data...),
	}, nil
}

func normalizedVariant(variant string) string {
	if variant == "" {
		return "bot"
	}
	return variant
}

func parseText(messageType, content string) string {
	if content == "" {
		return ""
	}
	var textBody struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(content), &textBody); err == nil && textBody.Text != "" {
		return textBody.Text
	}
	if messageType == "text" {
		return content
	}
	return strings.TrimSpace(content)
}

func parseMentions(raw []feishuMention, botID string) []im.Mention {
	out := make([]im.Mention, 0, len(raw))
	for _, mention := range raw {
		id := firstNonEmpty(mention.ID.OpenID, mention.ID.UserID, mention.ID.UnionID)
		out = append(out, im.Mention{
			ID:   id,
			Name: mention.Name,
			Bot:  botID != "" && id == botID,
		})
	}
	return out
}

func botOpenID(cfg config.IMPlatformConfig) string {
	if cfg.Extra == nil {
		return ""
	}
	if v, ok := cfg.Extra["bot_open_id"].(string); ok {
		return v
	}
	return ""
}

func parseMillis(raw string) time.Time {
	if raw == "" {
		return time.Time{}
	}
	ms, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
