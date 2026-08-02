package im

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/p-chat/pchat/internal/config"
)

const defaultWeChatQRBaseURL = "https://ilinkai.weixin.qq.com"

// WeChatQRServiceError is returned when the QR service cannot be reached.
type WeChatQRServiceError struct {
	Err error
}

func (e WeChatQRServiceError) Error() string {
	return "微信扫码服务暂时无法访问，请检查网络后重试"
}

func (e WeChatQRServiceError) Unwrap() error {
	return e.Err
}

// WeChatQRClient starts and polls a WeChat Bot QR login flow.
type WeChatQRClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

// WeChatQRSession is the user-facing snapshot of a QR login flow.
type WeChatQRSession struct {
	ID          string            `json:"id"`
	Status      string            `json:"status"`
	QRCode      string            `json:"qrcode,omitempty"`
	QRData      string            `json:"qr_data,omitempty"`
	QRURL       string            `json:"qr_url,omitempty"`
	Message     string            `json:"message,omitempty"`
	ExpiresAt   time.Time         `json:"expires_at,omitempty"`
	PollAfterMS int               `json:"poll_after_ms"`
	Account     map[string]string `json:"account,omitempty"`
	BaseURL     string            `json:"-"`
}

// WeChatCredential contains the persisted credential returned after scan.
type WeChatCredential struct {
	Token    string
	BotID    string
	UserID   string
	BaseURL  string
	Nickname string
}

// WeChatQRManager keeps short-lived QR sessions between start and poll calls.
type WeChatQRManager struct {
	mu       sync.Mutex
	client   WeChatQRClient
	sessions map[string]WeChatQRSession
}

// NewWeChatQRManager creates a manager for WeChat Bot QR login.
func NewWeChatQRManager(client WeChatQRClient) *WeChatQRManager {
	if client.HTTPClient == nil {
		client.HTTPClient = &http.Client{Timeout: 12 * time.Second}
	}
	return &WeChatQRManager{
		client:   client,
		sessions: map[string]WeChatQRSession{},
	}
}

// Start creates a QR login session with the configured iLink-compatible endpoint.
func (m *WeChatQRManager) Start(ctx context.Context, platform config.IMPlatformConfig) (WeChatQRSession, error) {
	client := m.clientFor(platform)
	qr, err := client.Start(ctx)
	if err != nil {
		return WeChatQRSession{}, err
	}
	if qr.ID == "" {
		qr.ID = qr.QRCode
	}
	if qr.ID == "" {
		qr.ID = fmt.Sprintf("wechat-%d", time.Now().UnixNano())
	}
	if qr.Status == "" {
		qr.Status = "waiting"
	}
	if qr.PollAfterMS <= 0 {
		qr.PollAfterMS = 2000
	}
	if qr.ExpiresAt.IsZero() {
		qr.ExpiresAt = time.Now().Add(3 * time.Minute)
	}
	qr.BaseURL = client.baseURL()
	m.mu.Lock()
	m.sessions[qr.ID] = qr
	m.mu.Unlock()
	return qr, nil
}

// Poll refreshes a QR login session and returns credentials once confirmed.
func (m *WeChatQRManager) Poll(ctx context.Context, id string) (WeChatQRSession, WeChatCredential, error) {
	m.mu.Lock()
	session, ok := m.sessions[id]
	m.mu.Unlock()
	if !ok {
		return WeChatQRSession{}, WeChatCredential{}, errors.New("wechat qr session not found")
	}
	client := m.client
	client.BaseURL = session.BaseURL
	if client.HTTPClient == nil {
		client.HTTPClient = &http.Client{Timeout: 12 * time.Second}
	}
	next, cred, err := client.Poll(ctx, session.QRCode)
	if err != nil {
		return session, WeChatCredential{}, err
	}
	next.ID = session.ID
	if next.QRCode == "" {
		next.QRCode = session.QRCode
	}
	if next.QRURL == "" {
		next.QRURL = session.QRURL
	}
	if next.QRData == "" {
		next.QRData = session.QRData
	}
	if next.ExpiresAt.IsZero() {
		next.ExpiresAt = session.ExpiresAt
	}
	if next.PollAfterMS <= 0 {
		next.PollAfterMS = 2000
	}
	next.BaseURL = session.BaseURL
	m.mu.Lock()
	m.sessions[id] = next
	if next.Status == "confirmed" || next.Status == "expired" || next.Status == "canceled" {
		delete(m.sessions, id)
	}
	m.mu.Unlock()
	return next, cred, nil
}

func (m *WeChatQRManager) clientFor(platform config.IMPlatformConfig) WeChatQRClient {
	client := m.client
	if client.HTTPClient == nil {
		client.HTTPClient = &http.Client{Timeout: 12 * time.Second}
	}
	if base := stringFromExtra(platform.Extra, "qr_base_url", "base_url"); base != "" {
		client.BaseURL = base
	}
	return client
}

func (c WeChatQRClient) Start(ctx context.Context) (WeChatQRSession, error) {
	endpoint := strings.TrimRight(c.baseURL(), "/") + "/ilink/bot/get_bot_qrcode?bot_type=3"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return WeChatQRSession{}, err
	}
	var raw map[string]any
	if err := c.doJSON(req, &raw); err != nil {
		return WeChatQRSession{}, err
	}
	data := mapValue(raw, "data")
	if len(data) == 0 {
		data = raw
	}
	qrcode := firstString(data, "qrcode", "qr_code", "code")
	img := normalizeWeChatQRAssetURL(firstString(data, "qrcode_img_content", "qr_url", "qrcode_url", "url"), c.baseURL())
	qrData := firstString(data, "qr_data", "qrcode_content", "content")
	if qrcode == "" && qrData == "" && img == "" {
		return WeChatQRSession{}, errors.New("wechat qr response did not include qrcode")
	}
	return WeChatQRSession{
		ID:          qrcode,
		Status:      "waiting",
		QRCode:      qrcode,
		QRData:      qrData,
		QRURL:       img,
		Message:     "等待扫码",
		PollAfterMS: 2000,
		ExpiresAt:   time.Now().Add(3 * time.Minute),
	}, nil
}

func (c WeChatQRClient) Poll(ctx context.Context, qrcode string) (WeChatQRSession, WeChatCredential, error) {
	if qrcode == "" {
		return WeChatQRSession{}, WeChatCredential{}, errors.New("qrcode is required")
	}
	endpoint := strings.TrimRight(c.baseURL(), "/") + "/ilink/bot/get_qrcode_status?qrcode=" + url.QueryEscape(qrcode)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return WeChatQRSession{}, WeChatCredential{}, err
	}
	req.Header.Set("iLink-App-ClientVersion", "1")
	var raw map[string]any
	if err := c.doJSON(req, &raw); err != nil {
		return WeChatQRSession{}, WeChatCredential{}, err
	}
	data := mapValue(raw, "data")
	if len(data) == 0 {
		data = raw
	}
	status := normalizeWeChatQRStatus(firstString(data, "status", "state", "qrcode_status", "qr_status"))
	msg := firstString(data, "message", "msg", "errmsg")
	cred := WeChatCredential{
		Token:    firstString(data, "bot_token", "botToken", "bot_access_token", "botAccessToken", "token", "access_token", "accessToken"),
		BotID:    firstString(data, "ilink_bot_id", "bot_id", "account_id"),
		UserID:   firstString(data, "ilink_user_id", "user_id"),
		BaseURL:  firstString(data, "baseurl", "base_url"),
		Nickname: firstString(data, "nickname", "name"),
	}
	if cred.Token == "" {
		for _, key := range []string{"credential", "credentials", "login_info", "loginInfo", "auth"} {
			if nested := mapValue(data, key); len(nested) > 0 {
				cred.Token = firstString(nested, "bot_token", "botToken", "bot_access_token", "botAccessToken", "token", "access_token", "accessToken")
				if cred.BotID == "" {
					cred.BotID = firstString(nested, "ilink_bot_id", "bot_id", "account_id")
				}
				if cred.UserID == "" {
					cred.UserID = firstString(nested, "ilink_user_id", "user_id")
				}
				if cred.Token != "" {
					break
				}
			}
		}
	}
	if status == "" && cred.Token != "" {
		status = "confirmed"
	}
	if status == "" {
		status = "waiting"
	}
	session := WeChatQRSession{
		Status:      status,
		QRCode:      qrcode,
		Message:     msg,
		PollAfterMS: 2000,
	}
	if cred.Token != "" {
		session.Account = map[string]string{
			"bot_id":   cred.BotID,
			"user_id":  cred.UserID,
			"nickname": cred.Nickname,
		}
	}
	return session, cred, nil
}

func (c WeChatQRClient) doJSON(req *http.Request, out any) error {
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 12 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return WeChatQRServiceError{Err: err}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("wechat qr HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("decode wechat qr response: %w", err)
	}
	if m, ok := out.(*map[string]any); ok {
		if code := firstString(*m, "code", "errcode", "ret"); code != "" && code != "0" {
			msg := firstString(*m, "message", "msg", "errmsg")
			if msg == "" {
				msg = code
			}
			return errors.New(msg)
		}
	}
	return nil
}

func (c WeChatQRClient) baseURL() string {
	if strings.TrimSpace(c.BaseURL) != "" {
		return strings.TrimSpace(c.BaseURL)
	}
	return defaultWeChatQRBaseURL
}

func normalizeWeChatQRStatus(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "confirmed", "confirm", "success", "login_success", "logged_in":
		return "confirmed"
	case "scaned", "scanned", "scanned_wait_confirm", "wait_confirm":
		return "scanned"
	case "expired", "expire", "timeout":
		return "expired"
	case "canceled", "cancelled", "cancel":
		return "canceled"
	case "wait", "waiting", "":
		return "waiting"
	default:
		return raw
	}
}

func firstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := m[key]; ok {
			switch v := value.(type) {
			case string:
				return v
			case float64:
				return fmt.Sprintf("%.0f", v)
			case json.Number:
				return v.String()
			}
		}
	}
	return ""
}

func mapValue(m map[string]any, key string) map[string]any {
	value, ok := m[key]
	if !ok {
		return nil
	}
	out, _ := value.(map[string]any)
	return out
}

func stringFromExtra(extra map[string]any, keys ...string) string {
	if extra == nil {
		return ""
	}
	for _, key := range keys {
		if value, ok := extra[key]; ok {
			if s, ok := value.(string); ok {
				return s
			}
		}
	}
	return ""
}

func normalizeWeChatQRAssetURL(raw, baseURL string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	lower := strings.ToLower(raw)
	if strings.HasPrefix(lower, "data:") || strings.HasPrefix(lower, "blob:") {
		return raw
	}
	if looksLikeBase64Image(raw) {
		return "data:image/png;base64," + raw
	}
	if u, err := url.Parse(raw); err == nil && u.IsAbs() {
		return raw
	}
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		return raw
	}
	if strings.HasPrefix(raw, "/") {
		return base + raw
	}
	return base + "/" + raw
}

func looksLikeBase64Image(raw string) bool {
	if len(raw) < 128 {
		return false
	}
	prefix := raw
	if len(prefix) > 16 {
		prefix = prefix[:16]
	}
	switch {
	case strings.HasPrefix(prefix, "iVBORw0KGgo"):
		return true
	case strings.HasPrefix(prefix, "/9j/"):
		return true
	case strings.HasPrefix(prefix, "R0lGOD"):
		return true
	default:
		return false
	}
}
