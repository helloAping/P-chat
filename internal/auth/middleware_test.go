package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// newProtectedRouter 构造挂了 RequireCapability 的最小 gin 引擎。
// newProtectedRouter builds a minimal gin engine guarded by
// RequireCapability.
func newProtectedRouter(t *testing.T, cap Capability) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequireCapability(cap))
	r.GET("/protected", func(c *gin.Context) { c.Status(http.StatusOK) })
	return r
}

// doProtected 发起一次带可选凭据的请求并返回状态码与错误体。
// doProtected issues one request with optional credentials and returns the
// status code plus the decoded error body.
func doProtected(t *testing.T, r *gin.Engine, authHeader, cookie string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: CookieName, Value: cookie})
	}
	recorder := httptest.NewRecorder()
	r.ServeHTTP(recorder, req)
	var body map[string]any
	if recorder.Body.Len() > 0 {
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode error body: %v", err)
		}
	}
	return recorder.Code, body
}

// TestRequireCapabilityBearer 验证 Authorization: Bearer 头路径（CLI / GUI
// 后端 / Wails webview 的传输形态）。
// TestRequireCapabilityBearer covers the Authorization: Bearer path (the
// transport shape for CLI / GUI backend / Wails webview).
func TestRequireCapabilityBearer(t *testing.T) {
	cap, err := NewCapability()
	if err != nil {
		t.Fatalf("NewCapability: %v", err)
	}
	r := newProtectedRouter(t, cap)
	status, _ := doProtected(t, r, "Bearer "+cap.String(), "")
	if status != http.StatusOK {
		t.Fatalf("bearer status = %d, want 200", status)
	}
}

// TestRequireCapabilityCookie 验证 HttpOnly cookie 路径（同源浏览器 Web UI
// 的传输形态）。
// TestRequireCapabilityCookie covers the HttpOnly cookie path (the transport
// shape for the same-origin browser Web UI).
func TestRequireCapabilityCookie(t *testing.T) {
	cap, err := NewCapability()
	if err != nil {
		t.Fatalf("NewCapability: %v", err)
	}
	r := newProtectedRouter(t, cap)
	status, _ := doProtected(t, r, "", cap.String())
	if status != http.StatusOK {
		t.Fatalf("cookie status = %d, want 200", status)
	}
}

// TestRequireCapabilityMissing 验证无凭据 → 401 auth_required。
// TestRequireCapabilityMissing verifies no credential → 401 auth_required.
func TestRequireCapabilityMissing(t *testing.T) {
	cap, err := NewCapability()
	if err != nil {
		t.Fatalf("NewCapability: %v", err)
	}
	r := newProtectedRouter(t, cap)
	status, body := doProtected(t, r, "", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("missing status = %d, want 401", status)
	}
	if body["error_kind"] != ErrKindAuthRequired {
		t.Fatalf("error_kind = %v, want %q", body["error_kind"], ErrKindAuthRequired)
	}
}

// TestRequireCapabilityWrong 验证凭据不匹配 → 401 auth_invalid。
// TestRequireCapabilityWrong verifies a mismatched credential → 401
// auth_invalid.
func TestRequireCapabilityWrong(t *testing.T) {
	cap, err := NewCapability()
	if err != nil {
		t.Fatalf("NewCapability: %v", err)
	}
	r := newProtectedRouter(t, cap)
	status, body := doProtected(t, r, "Bearer wrong-value", "")
	if status != http.StatusUnauthorized {
		t.Fatalf("wrong status = %d, want 401", status)
	}
	if body["error_kind"] != ErrKindAuthInvalid {
		t.Fatalf("error_kind = %v, want %q", body["error_kind"], ErrKindAuthInvalid)
	}
}

// TestRequireCapabilityHeaderExclusive 验证 Authorization 头的排他性：头存在
// 但不匹配时，即使 cookie 正确也拒绝（双凭据请求必须语义明确）。
// TestRequireCapabilityHeaderExclusive verifies Authorization exclusivity: a
// present-but-wrong header rejects even with a valid cookie (dual-credential
// requests must be unambiguous).
func TestRequireCapabilityHeaderExclusive(t *testing.T) {
	cap, err := NewCapability()
	if err != nil {
		t.Fatalf("NewCapability: %v", err)
	}
	r := newProtectedRouter(t, cap)
	status, body := doProtected(t, r, "Bearer wrong-value", cap.String())
	if status != http.StatusUnauthorized {
		t.Fatalf("header-exclusive status = %d, want 401", status)
	}
	if body["error_kind"] != ErrKindAuthInvalid {
		t.Fatalf("error_kind = %v, want %q", body["error_kind"], ErrKindAuthInvalid)
	}
}

// TestHostAllowlist 验证 DNS rebinding 拦截：loopback 各形态放行，攻击者
// 域名（含伪装形态）拒绝，非本机模式的配置 Host 放行。
// TestHostAllowlist verifies DNS rebinding interception: loopback forms
// pass, attacker domains (including disguised forms) are rejected, and the
// configured Host of a non-loopback mode passes.
func TestHostAllowlist(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(HostAllowlist("pchat.internal"))
	r.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	cases := []struct {
		host string
		want int
	}{
		{"localhost:15150", http.StatusOK},
		{"localhost", http.StatusOK},
		{"127.0.0.1:8080", http.StatusOK},
		{"[::1]:8080", http.StatusOK},
		{"pchat.internal", http.StatusOK},     // 非本机模式配置 Host / configured host
		{"PCHAT.INTERNAL:443", http.StatusOK}, // 大小写与端口不敏感 / case and port insensitive
		{"evil.com", http.StatusForbidden},
		{"127.0.0.1.evil.com", http.StatusForbidden}, // 伪装 loopback / disguised loopback
		{"localhost.evil.com", http.StatusForbidden},
		{"", http.StatusForbidden},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Host = tc.host
		recorder := httptest.NewRecorder()
		r.ServeHTTP(recorder, req)
		if recorder.Code != tc.want {
			t.Errorf("host %q: status = %d, want %d", tc.host, recorder.Code, tc.want)
		}
	}
}
