package feishu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/im"
)

const defaultAPIBase = "https://open.feishu.cn"

// Renderer 把规范化出站 chunk 渲染成飞书 OpenAPI 调用。
// Renderer renders normalized outbound chunks into Feishu OpenAPI calls.
type Renderer struct {
	cfg    config.IMPlatformConfig
	client *http.Client

	mu          sync.Mutex
	tenantToken string
	tokenExpiry time.Time
}

// NewRenderer 创建飞书出站 renderer。
// NewRenderer creates a Feishu outbound renderer.
func NewRenderer(cfg config.IMPlatformConfig, client *http.Client) *Renderer {
	if client == nil {
		client = http.DefaultClient
	}
	return &Renderer{cfg: cfg, client: client}
}

// Send 向飞书 chat 发送文本消息。
// Send sends a text message to a Feishu chat.
func (r *Renderer) Send(ctx context.Context, chunk im.IMOutChunk) error {
	_, err := r.SendMessage(ctx, chunk)
	return err
}

// SendMessage 发送文本消息并返回飞书平台 message_id。
// SendMessage sends a text message and returns the Feishu platform message_id.
func (r *Renderer) SendMessage(ctx context.Context, chunk im.IMOutChunk) (string, error) {
	if chunk.Chat.ChatID == "" {
		return "", errors.New("feishu send requires chat_id")
	}
	text := strings.TrimSpace(chunk.Text)
	if text == "" {
		return "", nil
	}
	if len([]rune(text)) > r.MaxTextLen() {
		return "", fmt.Errorf("feishu send text exceeds max length %d", r.MaxTextLen())
	}
	token, err := r.tenantAccessToken(ctx)
	if err != nil {
		return "", fmt.Errorf("get feishu tenant token: %w", err)
	}
	content, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return "", fmt.Errorf("marshal feishu text content: %w", err)
	}
	body := map[string]string{
		"receive_id": chunk.Chat.ChatID,
		"msg_type":   "text",
		"content":    string(content),
	}
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			MessageID string `json:"message_id"`
		} `json:"data"`
	}
	if err := r.doJSON(ctx, http.MethodPost, r.apiURL("/open-apis/im/v1/messages")+"?receive_id_type=chat_id", token, body, &resp); err != nil {
		return "", fmt.Errorf("send feishu message: %w", err)
	}
	if resp.Code != 0 {
		return "", fmt.Errorf("feishu send message failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return resp.Data.MessageID, nil
}

// Edit 更新一条已发送的飞书消息。
// Edit updates a previously sent Feishu message.
func (r *Renderer) Edit(ctx context.Context, ref im.ChatRef, msgID string, chunk im.IMOutChunk) error {
	if msgID == "" {
		return errors.New("feishu edit requires message id")
	}
	text := strings.TrimSpace(chunk.Text)
	if text == "" {
		return nil
	}
	if len([]rune(text)) > r.MaxTextLen() {
		return fmt.Errorf("feishu edit text exceeds max length %d", r.MaxTextLen())
	}
	token, err := r.tenantAccessToken(ctx)
	if err != nil {
		return fmt.Errorf("get feishu tenant token: %w", err)
	}
	content, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return fmt.Errorf("marshal feishu text content: %w", err)
	}
	body := map[string]string{
		"msg_type": "text",
		"content":  string(content),
	}
	var resp struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := r.doJSON(ctx, http.MethodPatch, r.apiURL("/open-apis/im/v1/messages/"+msgID), token, body, &resp); err != nil {
		return fmt.Errorf("edit feishu message: %w", err)
	}
	if resp.Code != 0 {
		return fmt.Errorf("feishu edit message failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	return nil
}

// Typing 在飞书侧是 no-op，因为飞书没有原生 typing action。
// Typing is a no-op because Feishu has no native typing action.
func (r *Renderer) Typing(ctx context.Context, ref im.ChatRef) error {
	return nil
}

// MaxTextLen 返回保守的飞书文本消息长度上限。
// MaxTextLen returns the conservative Feishu text message limit.
func (r *Renderer) MaxTextLen() int {
	return 4096
}

// MarkdownDialect 返回 renderer 偏好的出站 Markdown 方言。
// MarkdownDialect returns the renderer's preferred outbound dialect.
func (r *Renderer) MarkdownDialect() im.MarkdownDialect {
	return im.MarkdownPlain
}

func (r *Renderer) tenantAccessToken(ctx context.Context) (string, error) {
	r.mu.Lock()
	if r.tenantToken != "" && time.Now().Before(r.tokenExpiry) {
		token := r.tenantToken
		r.mu.Unlock()
		return token, nil
	}
	r.mu.Unlock()

	if r.cfg.AppID == "" || r.cfg.AppSecret == "" {
		return "", errors.New("feishu renderer requires app_id and app_secret")
	}
	body := map[string]string{
		"app_id":     r.cfg.AppID,
		"app_secret": r.cfg.AppSecret,
	}
	var resp struct {
		Code              int    `json:"code"`
		Msg               string `json:"msg"`
		TenantAccessToken string `json:"tenant_access_token"`
		Expire            int    `json:"expire"`
	}
	if err := r.doJSON(ctx, http.MethodPost, r.apiURL("/open-apis/auth/v3/tenant_access_token/internal"), "", body, &resp); err != nil {
		return "", fmt.Errorf("request feishu tenant token: %w", err)
	}
	if resp.Code != 0 || resp.TenantAccessToken == "" {
		return "", fmt.Errorf("feishu tenant token failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
	expire := time.Duration(resp.Expire) * time.Second
	if expire <= 0 {
		expire = time.Hour
	}
	skew := time.Minute
	if expire <= skew {
		skew = expire / 10
	}
	r.mu.Lock()
	r.tenantToken = resp.TenantAccessToken
	r.tokenExpiry = time.Now().Add(expire - skew)
	r.mu.Unlock()
	return resp.TenantAccessToken, nil
}

func (r *Renderer) doJSON(ctx context.Context, method, url, token string, in any, out any) error {
	data, err := json.Marshal(in)
	if err != nil {
		return fmt.Errorf("marshal request body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()
	data, err = io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read response body: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("feishu api status %d", resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("decode feishu response: %w", err)
	}
	return nil
}

func (r *Renderer) apiURL(path string) string {
	base := strings.TrimRight(r.cfg.Out.APIBase, "/")
	if base == "" {
		base = defaultAPIBase
	}
	return base + path
}
