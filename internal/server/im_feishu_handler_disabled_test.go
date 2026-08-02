package server

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/p-chat/pchat/internal/im"
)

func TestFeishuWebhookRequiresEnabledIMConfig(t *testing.T) {
	s, cfg := newTestServerWithConfig(t, richTestConfigJSON)
	s.SetIMGateway(im.NewGateway(cfg.IM))

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/im/feishu/webhook", strings.NewReader(`{"type":"url_verification","token":"verify-token","challenge":"challenge-123"}`))
	req.Header.Set("Content-Type", "application/json")
	s.engine.ServeHTTP(w, req)

	if w.Code != 404 {
		t.Fatalf("status = %d, want 404 for disabled/unconfigured IM", w.Code)
	}
}
