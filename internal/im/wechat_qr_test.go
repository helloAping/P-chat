package im

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/p-chat/pchat/internal/config"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestWeChatQRStartUsesOpenClawEndpoint(t *testing.T) {
	client := WeChatQRClient{
		LocalTokens: []string{"old-token"},
		HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodPost {
				t.Fatalf("method = %s, want POST", req.Method)
			}
			if req.URL.String() != "https://ilinkai.weixin.qq.com/ilink/bot/get_bot_qrcode?bot_type=3" {
				t.Fatalf("url = %s, want OpenClaw iLink QR endpoint", req.URL.String())
			}
			if req.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("content-type = %q, want application/json", req.Header.Get("Content-Type"))
			}
			if req.Header.Get("iLink-App-Id") == "" || req.Header.Get("iLink-App-ClientVersion") == "" {
				t.Fatalf("missing iLink headers: app_id=%q client_version=%q", req.Header.Get("iLink-App-Id"), req.Header.Get("iLink-App-ClientVersion"))
			}
			var body struct {
				LocalTokenList []string `json:"local_token_list"`
			}
			if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
				t.Fatalf("decode request body: %v", err)
			}
			if len(body.LocalTokenList) != 1 || body.LocalTokenList[0] != "old-token" {
				t.Fatalf("local_token_list = %+v, want old-token", body.LocalTokenList)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"code":0,"data":{"qrcode":"qr-1","qrcode_img_content":"data:image/png;base64,abc"}}`)),
				Header:     make(http.Header),
			}, nil
		})},
	}

	session, err := client.Start(context.Background())
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if session.QRCode != "qr-1" || session.QRURL == "" {
		t.Fatalf("session = %+v, want qr code and image", session)
	}
}

func TestWeChatQRManagerKeepsSessionOnTransientPollNetworkError(t *testing.T) {
	var pollCalls int
	client := WeChatQRClient{
		HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.Path {
			case "/ilink/bot/get_bot_qrcode":
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"code":0,"data":{"qrcode":"qr-retry","qrcode_img_content":"data:image/png;base64,abc"}}`)),
					Header:     make(http.Header),
				}, nil
			case "/ilink/bot/get_qrcode_status":
				pollCalls++
				if pollCalls == 1 {
					return nil, errors.New("simulated network outage")
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(`{"code":0,"data":{"status":"confirmed","bot_token":"wx-token","ilink_bot_id":"bot-1"}}`)),
					Header:     make(http.Header),
				}, nil
			default:
				t.Fatalf("unexpected path %s", req.URL.Path)
				return nil, nil
			}
		})},
	}
	manager := NewWeChatQRManager(client)

	start, err := manager.Start(context.Background(), config.IMPlatformConfig{Type: "wechat", Variant: "wechatbot", Enabled: true})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	waiting, cred, err := manager.Poll(context.Background(), start.ID)
	if err != nil {
		t.Fatalf("transient poll error should keep QR session alive, got %v", err)
	}
	if waiting.Status != "waiting" || cred.Token != "" {
		t.Fatalf("first poll = %+v cred=%+v, want waiting without credential", waiting, cred)
	}
	confirmed, cred, err := manager.Poll(context.Background(), start.ID)
	if err != nil {
		t.Fatalf("second poll: %v", err)
	}
	if confirmed.Status != "confirmed" || cred.Token != "wx-token" {
		t.Fatalf("second poll = %+v cred=%+v, want confirmed credential", confirmed, cred)
	}
}

func TestWeChatQRManagerCarriesConfiguredHeadersAcrossPoll(t *testing.T) {
	var gotStartAppID string
	var gotPollAppID string
	var gotStartClientVersion string
	var gotPollClientVersion string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ilink/bot/get_bot_qrcode":
			gotStartAppID = r.Header.Get("iLink-App-Id")
			gotStartClientVersion = r.Header.Get("iLink-App-ClientVersion")
			_, _ = w.Write([]byte(`{"code":0,"data":{"qrcode":"qr-headers","qrcode_img_content":"data:image/png;base64,abc"}}`))
		case "/ilink/bot/get_qrcode_status":
			gotPollAppID = r.Header.Get("iLink-App-Id")
			gotPollClientVersion = r.Header.Get("iLink-App-ClientVersion")
			_, _ = w.Write([]byte(`{"code":0,"data":{"status":"waiting"}}`))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	manager := NewWeChatQRManager(WeChatQRClient{})
	start, err := manager.Start(context.Background(), config.IMPlatformConfig{
		Type:    "wechat",
		Variant: "wechatbot",
		Enabled: true,
		AppID:   "wx-custom",
		Extra: map[string]any{
			"qr_base_url":          srv.URL,
			"ilink_client_version": "9",
		},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, _, err := manager.Poll(context.Background(), start.ID); err != nil {
		t.Fatalf("poll: %v", err)
	}
	if gotStartAppID != "wx-custom" || gotPollAppID != "wx-custom" {
		t.Fatalf("app ids = start:%q poll:%q, want wx-custom", gotStartAppID, gotPollAppID)
	}
	if gotStartClientVersion != "9" || gotPollClientVersion != "9" {
		t.Fatalf("client versions = start:%q poll:%q, want 9", gotStartClientVersion, gotPollClientVersion)
	}
}

func TestWeChatQRManagerFollowsScanRedirectHost(t *testing.T) {
	var redirected bool
	var redirectHost string
	redirectedSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ilink/bot/get_qrcode_status" {
			t.Fatalf("unexpected redirected path %s", r.URL.Path)
		}
		redirected = true
		_, _ = w.Write([]byte(`{"code":0,"data":{"status":"confirmed","bot_token":"wx-token","ilink_bot_id":"bot-1","baseurl":"https://wx.example"}}`))
	}))
	defer redirectedSrv.Close()
	redirectHost = strings.TrimPrefix(redirectedSrv.URL, "http://")

	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ilink/bot/get_bot_qrcode":
			_, _ = w.Write([]byte(`{"code":0,"data":{"qrcode":"qr-redirect","qrcode_img_content":"data:image/png;base64,abc"}}`))
		case "/ilink/bot/get_qrcode_status":
			_, _ = w.Write([]byte(`{"code":0,"data":{"status":"scaned_but_redirect","redirect_host":"` + redirectHost + `"}}`))
		default:
			t.Fatalf("unexpected primary path %s", r.URL.Path)
		}
	}))
	defer primary.Close()

	manager := NewWeChatQRManager(WeChatQRClient{})
	start, err := manager.Start(context.Background(), config.IMPlatformConfig{
		Type:    "wechat",
		Variant: "wechatbot",
		Enabled: true,
		Extra:   map[string]any{"qr_base_url": primary.URL},
	})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	redirect, _, err := manager.Poll(context.Background(), start.ID)
	if err != nil {
		t.Fatalf("redirect poll: %v", err)
	}
	if redirect.Status != "scanned" {
		t.Fatalf("redirect status = %q, want scanned", redirect.Status)
	}

	confirmed, cred, err := manager.Poll(context.Background(), start.ID)
	if err != nil {
		t.Fatalf("confirmed poll: %v", err)
	}
	if !redirected {
		t.Fatal("second poll did not use redirect host")
	}
	if confirmed.Status != "confirmed" || cred.Token != "wx-token" {
		t.Fatalf("confirmed = %+v cred=%+v, want redirected credential", confirmed, cred)
	}
}

func TestNormalizeWeChatQRAssetURL(t *testing.T) {
	rawPNG := "iVBORw0KGgo" + strings.Repeat("A", 128)
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "data uri", raw: "data:image/png;base64,abc", want: "data:image/png;base64,abc"},
		{name: "absolute url", raw: "https://weixin.qq.com/x/abc", want: "https://weixin.qq.com/x/abc"},
		{name: "absolute path", raw: "/x/abc", want: "https://ilinkai.weixin.qq.com/x/abc"},
		{name: "relative path", raw: "x/abc", want: "https://ilinkai.weixin.qq.com/x/abc"},
		{name: "bare png base64", raw: rawPNG, want: "data:image/png;base64," + rawPNG},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeWeChatQRAssetURL(tt.raw, defaultWeChatQRBaseURL); got != tt.want {
				t.Fatalf("normalizeWeChatQRAssetURL() = %q, want %q", got, tt.want)
			}
		})
	}
}
