package im

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestWeChatQRStartUsesOpenClawEndpoint(t *testing.T) {
	client := WeChatQRClient{
		HTTPClient: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != http.MethodGet {
				t.Fatalf("method = %s, want GET", req.Method)
			}
			if req.URL.String() != "https://ilinkai.weixin.qq.com/ilink/bot/get_bot_qrcode?bot_type=3" {
				t.Fatalf("url = %s, want OpenClaw iLink QR endpoint", req.URL.String())
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
