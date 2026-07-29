package im

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/paths"
)

const (
	defaultWeChatAPIBase       = "https://ilinkai.weixin.qq.com"
	wechatChannelVersion       = "2.0.0"
	wechatBotAgent             = "P-Chat/1.0"
	wechatPollInterval         = 2 * time.Second
	wechatRequestTimeout       = 40 * time.Second
	wechatStateFileName        = "wechat-state.json"
	wechatLongPollPath         = "/ilink/bot/getupdates"
	wechatSendMessagePath      = "/ilink/bot/sendmessage"
	wechatNotifyStartPath      = "/ilink/bot/msg/notifystart"
	wechatNotifyStopPath       = "/ilink/bot/msg/notifystop"
	wechatAuthorizationType    = "ilink_bot_token"
	wechatSessionExpiredError  = "wechat session expired"
	wechatSessionNotReadyError = "wechat session not ready"
)

type wechatState struct {
	Cursor        string            `json:"cursor,omitempty"`
	ContextTokens map[string]string `json:"context_tokens,omitempty"`
	LastUpdateAt  time.Time         `json:"last_update_at,omitempty"`
}

// WeChatAdapter handles the long-poll inbound loop and outbound replies
// for the iLink-compatible WeChat bot protocol.
type WeChatAdapter struct {
	cfg    config.IMPlatformConfig
	client *http.Client

	mu            sync.RWMutex
	started       bool
	startedAt     time.Time
	status        string
	currentError  string
	lastError     string
	lastPollAt    time.Time
	lastInboundAt time.Time
	cancel        context.CancelFunc
	wg            sync.WaitGroup
	state         wechatState
	statePath     string
}

// NewWeChatAdapter creates a WeChat Bot adapter.
func NewWeChatAdapter(cfg config.IMPlatformConfig) *WeChatAdapter {
	if cfg.Variant == "" {
		cfg.Variant = "wechatbot"
	}
	stateDir := filepath.Join(paths.GlobalDir(), "im")
	return &WeChatAdapter{
		cfg:    cfg,
		client: &http.Client{Timeout: wechatRequestTimeout},
		status: "stopped",
		state: wechatState{
			ContextTokens: map[string]string{},
		},
		statePath: filepath.Join(stateDir, wechatStateFileName),
	}
}

// RegisterConfiguredWeChatAdapters wires WeChat adapters and renderers
// for all enabled WeChat platforms in the current IM config.
func RegisterConfiguredWeChatAdapters(gateway *Gateway, cfg config.IMConfig) {
	if gateway == nil {
		return
	}
	cfg.Normalize()
	for _, platform := range cfg.Platforms {
		RegisterWeChatAdapter(gateway, platform)
	}
}

// RegisterWeChatAdapter wires one WeChat platform into the Gateway.
func RegisterWeChatAdapter(gateway *Gateway, platform config.IMPlatformConfig) {
	if gateway == nil || platform.Type != "wechat" || !platform.Enabled {
		return
	}
	if platform.Variant == "" {
		platform.Variant = "wechatbot"
	}
	adapter := NewWeChatAdapter(platform)
	gateway.RegisterAdapter(adapter)
	gateway.RegisterRenderer("wechat", platform.Variant, adapter)
}

// Platform returns the platform name.
func (a *WeChatAdapter) Platform() string { return "wechat" }

// Variants returns the supported adapter variants.
func (a *WeChatAdapter) Variants() []string { return []string{"wechatbot"} }

// Start launches the polling loop and restores cached context tokens.
func (a *WeChatAdapter) Start(ctx context.Context, in chan<- IMEvent) error {
	if a.cfg.Token == "" {
		return errors.New("wechat adapter requires token")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	a.mu.Lock()
	if a.started {
		a.mu.Unlock()
		return nil
	}
	_ = a.loadStateLocked()
	runCtx, cancel := context.WithCancel(ctx)
	a.cancel = cancel
	a.started = true
	a.startedAt = time.Now()
	a.status = "polling"
	a.currentError = ""
	a.lastError = ""
	a.mu.Unlock()

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.pollLoop(runCtx, in)
	}()
	return nil
}

// Stop stops the polling loop.
func (a *WeChatAdapter) Stop(ctx context.Context) error {
	a.mu.Lock()
	if !a.started {
		a.mu.Unlock()
		return nil
	}
	cancel := a.cancel
	a.cancel = nil
	a.started = false
	a.status = "stopped"
	a.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	a.wg.Wait()
	return nil
}

// Health returns the current adapter status.
func (a *WeChatAdapter) Health() HealthStatus {
	a.mu.RLock()
	defer a.mu.RUnlock()
	status := a.status
	if !a.started {
		if a.cfg.Token != "" {
			status = "authenticated"
		} else {
			status = "stopped"
		}
	}
	if status == "" {
		status = "stopped"
	}
	return HealthStatus{
		Platform:      "wechat",
		Variant:       a.cfg.Variant,
		Enabled:       a.cfg.Enabled,
		Status:        status,
		Error:         a.currentError,
		LastError:     a.lastError,
		StartedAt:     a.startedAt,
		LastPollAt:    a.lastPollAt,
		LastInboundAt: a.lastInboundAt,
	}
}

// Send sends a reply to the matched WeChat conversation.
func (a *WeChatAdapter) Send(ctx context.Context, chunk IMOutChunk) error {
	return a.sendMessage(ctx, chunk)
}

// Edit falls back to sending a fresh message because WeChat does not
// offer a reliable message edit path for this protocol.
func (a *WeChatAdapter) Edit(ctx context.Context, ref ChatRef, msgID string, chunk IMOutChunk) error {
	return a.sendMessage(ctx, IMOutChunk{
		TraceID:  chunk.TraceID,
		Platform: chunk.Platform,
		Chat:     ref,
		Kind:     "text",
		Text:     chunk.Text,
		Metadata: chunk.Metadata,
	})
}

// Typing is a no-op for the current MVP.
func (a *WeChatAdapter) Typing(ctx context.Context, ref ChatRef) error {
	return nil
}

// MaxTextLen returns a conservative WeChat message cap.
func (a *WeChatAdapter) MaxTextLen() int { return 2000 }

// MarkdownDialect returns the renderer's Markdown preference.
func (a *WeChatAdapter) MarkdownDialect() MarkdownDialect { return MarkdownPlain }

func (a *WeChatAdapter) pollLoop(ctx context.Context, in chan<- IMEvent) {
	a.notifyState(ctx, wechatNotifyStartPath)
	defer a.notifyState(context.Background(), wechatNotifyStopPath)

	cursor := a.currentCursor()
	backoff := wechatPollInterval
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		a.markPollAttempt()
		resp, err := a.getUpdates(ctx, cursor)
		if err != nil {
			a.setError(err)
			if ctx.Err() != nil {
				return
			}
			time.Sleep(backoff)
			if backoff < 10*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = wechatPollInterval
		a.markPollOK()

		if resp.Ret == -14 || resp.ErrCode == -14 {
			a.setError(errors.New(wechatSessionExpiredError))
			return
		}

		if resp.Cursor != "" {
			cursor = resp.Cursor
			a.setCursor(cursor)
		}

		for _, raw := range resp.Messages {
			ev, ok, err := parseWeChatEvent(raw, a.cfg)
			if err != nil {
				a.setError(err)
				continue
			}
			if !ok {
				continue
			}
			if ev.ContextToken != "" && ev.Chat.ChatID != "" {
				a.setContextToken(ev.Chat.ChatID, ev.ContextToken)
			}
			select {
			case <-ctx.Done():
				return
			case in <- ev:
				a.markInbound()
			}
		}
	}
}

func (a *WeChatAdapter) sendMessage(ctx context.Context, chunk IMOutChunk) error {
	if strings.TrimSpace(chunk.Text) == "" {
		return nil
	}
	target := strings.TrimSpace(chunk.Chat.ChatID)
	if target == "" {
		return errors.New("wechat send requires chat_id")
	}
	contextToken := a.contextToken(target)
	if contextToken == "" {
		if chunk.Metadata != nil {
			contextToken = strings.TrimSpace(chunk.Metadata["context_token"])
		}
	}
	if contextToken == "" {
		return errors.New(wechatSessionNotReadyError)
	}
	body := map[string]any{
		"msg": map[string]any{
			"from_user_id":  "",
			"to_user_id":    target,
			"client_id":     uuid.NewString(),
			"message_type":  2,
			"message_state": 2,
			"context_token": contextToken,
			"item_list":     []map[string]any{{"type": 1, "text_item": map[string]string{"text": chunk.Text}}},
		},
	}
	var resp map[string]any
	if err := a.postJSON(ctx, wechatSendMessagePath, body, &resp); err != nil {
		return err
	}
	if code := firstInt64Map(resp, "ret", "code", "errcode"); code == -14 {
		a.setError(errors.New(wechatSessionExpiredError))
		return errors.New(wechatSessionExpiredError)
	}
	if code := firstInt64Map(resp, "ret", "code", "errcode"); code != 0 {
		msg := firstString(resp, "msg", "message", "errmsg")
		if msg == "" {
			msg = fmt.Sprintf("wechat send failed: %d", code)
		}
		return errors.New(msg)
	}
	return nil
}

func (a *WeChatAdapter) notifyState(ctx context.Context, path string) {
	var noop map[string]any
	_ = a.postJSON(ctx, path, map[string]any{}, &noop)
}

func (a *WeChatAdapter) getUpdates(ctx context.Context, cursor string) (wechatUpdatesResponse, error) {
	body := map[string]any{
		"get_updates_buf": cursor,
	}
	var resp wechatUpdatesResponse
	if err := a.postJSON(ctx, wechatLongPollPath, body, &resp); err != nil {
		return wechatUpdatesResponse{}, err
	}
	resp.normalize()
	return resp, nil
}

func (a *WeChatAdapter) postJSON(ctx context.Context, path string, body any, out any) error {
	if ctx == nil {
		ctx = context.Background()
	}
	reqBody := map[string]any{
		"base_info": map[string]any{
			"channel_version": wechatChannelVersion,
			"bot_agent":       wechatBotAgent,
		},
	}
	if bodyMap, ok := body.(map[string]any); ok {
		for k, v := range bodyMap {
			reqBody[k] = v
		}
	} else if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal wechat body: %w", err)
		}
		var bodyMap map[string]any
		if err := json.Unmarshal(raw, &bodyMap); err != nil {
			return fmt.Errorf("build wechat body: %w", err)
		}
		for k, v := range bodyMap {
			reqBody[k] = v
		}
	}
	encoded, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("encode wechat request: %w", err)
	}
	url := strings.TrimRight(a.baseURL(), "/") + path
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(encoded))
	if err != nil {
		return fmt.Errorf("build wechat request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("AuthorizationType", wechatAuthorizationType)
	req.Header.Set("Authorization", "Bearer "+a.cfg.Token)
	req.Header.Set("X-WECHAT-UIN", randomWeChatUIN())
	resp, err := a.client.Do(req)
	if err != nil {
		return fmt.Errorf("wechat request failed: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read wechat response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("wechat HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode wechat response: %w", err)
	}
	return nil
}

func (a *WeChatAdapter) baseURL() string {
	if a.cfg.Endpoint != "" {
		return a.cfg.Endpoint
	}
	if base := stringFromExtra(a.cfg.Extra, "base_url", "qr_base_url"); base != "" {
		return base
	}
	return defaultWeChatAPIBase
}

func (a *WeChatAdapter) currentCursor() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.state.Cursor
}

func (a *WeChatAdapter) setCursor(cursor string) {
	a.mu.Lock()
	a.state.Cursor = cursor
	a.state.LastUpdateAt = time.Now()
	_ = a.saveStateLocked()
	a.mu.Unlock()
}

func (a *WeChatAdapter) contextToken(userID string) string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.state.ContextTokens == nil {
		return ""
	}
	return a.state.ContextTokens[userID]
}

func (a *WeChatAdapter) setContextToken(userID, token string) {
	a.mu.Lock()
	if a.state.ContextTokens == nil {
		a.state.ContextTokens = map[string]string{}
	}
	a.state.ContextTokens[userID] = token
	a.state.LastUpdateAt = time.Now()
	_ = a.saveStateLocked()
	a.mu.Unlock()
}

func (a *WeChatAdapter) setError(err error) {
	if err == nil {
		return
	}
	a.mu.Lock()
	a.currentError = err.Error()
	a.lastError = err.Error()
	if !a.started {
		a.status = "stopped"
	} else if strings.Contains(strings.ToLower(err.Error()), "expired") {
		a.status = "expired"
	} else {
		a.status = "error"
	}
	a.mu.Unlock()
}

func (a *WeChatAdapter) markPollAttempt() {
	a.mu.Lock()
	a.lastPollAt = time.Now()
	a.mu.Unlock()
}

func (a *WeChatAdapter) markPollOK() {
	a.mu.Lock()
	a.currentError = ""
	if a.started && a.status != "expired" {
		a.status = "polling"
	}
	a.mu.Unlock()
}

func (a *WeChatAdapter) markInbound() {
	a.mu.Lock()
	a.lastInboundAt = time.Now()
	a.currentError = ""
	if a.started && a.status != "expired" {
		a.status = "polling"
	}
	a.mu.Unlock()
}

func (a *WeChatAdapter) loadStateLocked() error {
	data, err := os.ReadFile(a.statePath)
	if err != nil {
		return nil
	}
	var st wechatState
	if err := json.Unmarshal(data, &st); err != nil {
		return err
	}
	if st.ContextTokens == nil {
		st.ContextTokens = map[string]string{}
	}
	a.state = st
	return nil
}

func (a *WeChatAdapter) saveStateLocked() error {
	if err := os.MkdirAll(filepath.Dir(a.statePath), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(a.state, "", "  ")
	if err != nil {
		return err
	}
	tmp := a.statePath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, a.statePath)
}

type wechatUpdatesResponse struct {
	Ret      int               `json:"ret,omitempty"`
	ErrCode  int               `json:"errcode,omitempty"`
	Cursor   string            `json:"get_updates_buf,omitempty"`
	Messages []json.RawMessage `json:"msgs,omitempty"`
	Data     map[string]any    `json:"data,omitempty"`
}

func (r *wechatUpdatesResponse) normalize() {
	if r == nil || len(r.Data) == 0 {
		return
	}
	if r.Cursor == "" {
		r.Cursor = firstString(r.Data, "get_updates_buf", "cursor", "next_cursor")
	}
	if r.Ret == 0 {
		r.Ret = int(firstInt64(r.Data, "ret", "code"))
	}
	if r.ErrCode == 0 {
		r.ErrCode = int(firstInt64(r.Data, "errcode", "error_code"))
	}
	if len(r.Messages) == 0 {
		r.Messages = rawMessagesFromAny(r.Data["msgs"])
	}
	if len(r.Messages) == 0 {
		r.Messages = rawMessagesFromAny(r.Data["messages"])
	}
}

func rawMessagesFromAny(value any) []json.RawMessage {
	switch v := value.(type) {
	case []json.RawMessage:
		return v
	case []any:
		out := make([]json.RawMessage, 0, len(v))
		for _, item := range v {
			raw, err := json.Marshal(item)
			if err != nil {
				continue
			}
			out = append(out, raw)
		}
		return out
	default:
		return nil
	}
}

func parseWeChatEvent(raw json.RawMessage, cfg config.IMPlatformConfig) (IMEvent, bool, error) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return IMEvent{}, false, fmt.Errorf("parse wechat update: %w", err)
	}
	payload = unwrapWeChatPayload(payload)
	if len(payload) == 0 {
		return IMEvent{}, false, nil
	}
	variant := cfg.Variant
	if variant == "" {
		variant = "wechatbot"
	}
	senderID := firstString(payload, "from_user_id", "sender_user_id", "author_user_id", "user_id")
	if senderID == "" {
		senderID = firstString(mapValue(payload, "sender"), "id", "user_id", "open_id")
	}
	if isWeChatSelfSender(senderID, cfg) {
		return IMEvent{}, false, nil
	}
	chatID := firstString(payload, "chat_id", "room_id", "group_id", "conversation_id")
	chatType := "private"
	if roomID := firstString(payload, "room_id", "group_id"); roomID != "" {
		chatID = roomID
		chatType = "group"
	}
	if chatID == "" {
		chatID = senderID
	}
	if chatID == "" {
		return IMEvent{}, false, nil
	}
	text := firstString(payload, "text")
	if text == "" {
		if content := firstString(payload, "content"); content != "" {
			text = content
		}
	}
	if text == "" {
		text = wechatTextFromItems(payload)
	}
	if text == "" {
		return IMEvent{}, false, nil
	}
	ctxToken := firstString(payload, "context_token")
	if ctxToken == "" {
		ctxToken = firstString(mapValue(payload, "context"), "token", "context_token")
	}
	ts := parseWeChatTimestamp(firstInt64(payload, "timestamp", "create_time", "time"))
	if senderID == "" {
		senderID = chatID
	}
	return IMEvent{
		ID:       firstString(payload, "message_id", "id", "msg_id"),
		Platform: "wechat",
		Variant:  variant,
		Chat: ChatRef{
			Platform: "wechat",
			Variant:  variant,
			ChatID:   chatID,
			ChatType: chatType,
		},
		Sender: SenderRef{
			ID:          senderID,
			DisplayName: firstString(payload, "sender_name", "nickname", "from_user_name"),
		},
		ContextToken: ctxToken,
		Text:         text,
		Timestamp:    ts,
		Raw:          append([]byte(nil), raw...),
	}, true, nil
}

func unwrapWeChatPayload(payload map[string]any) map[string]any {
	for _, key := range []string{"data", "msg", "message"} {
		if next := mapValue(payload, key); len(next) > 0 {
			payload = next
		}
	}
	return payload
}

func isWeChatSelfSender(senderID string, cfg config.IMPlatformConfig) bool {
	senderID = strings.TrimSpace(senderID)
	if senderID == "" {
		return false
	}
	for _, key := range []string{"ilink_bot_id", "bot_id", "ilink_user_id"} {
		if senderID == strings.TrimSpace(stringFromExtra(cfg.Extra, key)) {
			return true
		}
	}
	return false
}

func wechatTextFromItems(payload map[string]any) string {
	items, ok := payload["item_list"].([]any)
	if !ok {
		items, ok = payload["items"].([]any)
	}
	if !ok {
		return ""
	}
	var parts []string
	for _, item := range items {
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		text := firstString(itemMap, "text")
		if text == "" {
			text = firstString(mapValue(itemMap, "text_item"), "text", "content")
		}
		if text == "" {
			text = firstString(mapValue(itemMap, "content"), "text")
		}
		text = strings.TrimSpace(text)
		if text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n")
}

func parseWeChatTimestamp(raw int64) time.Time {
	if raw <= 0 {
		return time.Time{}
	}
	if raw > 1e12 {
		return time.UnixMilli(raw)
	}
	return time.Unix(raw, 0)
}

func firstInt64(m map[string]any, keys ...string) int64 {
	for _, key := range keys {
		if value, ok := m[key]; ok {
			switch v := value.(type) {
			case int64:
				return v
			case int:
				return int64(v)
			case float64:
				return int64(v)
			case json.Number:
				if n, err := v.Int64(); err == nil {
					return n
				}
			case string:
				if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil {
					return n
				}
			}
		}
	}
	return 0
}

func firstInt64Map(m map[string]any, keys ...string) int64 {
	return firstInt64(m, keys...)
}

func randomWeChatUIN() string {
	var buf [4]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return base64.StdEncoding.EncodeToString([]byte(strconv.FormatInt(time.Now().UnixNano(), 10)))
	}
	n := uint32(buf[0])<<24 | uint32(buf[1])<<16 | uint32(buf[2])<<8 | uint32(buf[3])
	return base64.StdEncoding.EncodeToString([]byte(strconv.FormatUint(uint64(n), 10)))
}
