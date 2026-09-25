package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// newIssuer 构造测试用签发器。
// newIssuer builds a test issuer.
func newIssuer(t *testing.T) (*BootstrapIssuer, Capability) {
	t.Helper()
	cap, err := NewCapability()
	if err != nil {
		t.Fatalf("NewCapability: %v", err)
	}
	return NewBootstrapIssuer(cap, 0), cap
}

// TestBootstrapRoundtrip 验证铸造 → 兑付返回实例凭据。
// TestBootstrapRoundtrip verifies mint → exchange returns the instance
// capability.
func TestBootstrapRoundtrip(t *testing.T) {
	issuer, cap := newIssuer(t)
	token, err := issuer.Mint()
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if len(token) != bootstrapTokenBytes*2 {
		t.Fatalf("token length = %d, want %d", len(token), bootstrapTokenBytes*2)
	}
	got, ok := issuer.Exchange(token)
	if !ok {
		t.Fatal("exchange of fresh token must succeed")
	}
	if got.String() != cap.String() {
		t.Fatal("exchange must return the instance capability")
	}
}

// TestBootstrapSingleUse 验证 token 一次性：第二次兑付失败。
// TestBootstrapSingleUse verifies tokens are one-time: the second exchange
// fails.
func TestBootstrapSingleUse(t *testing.T) {
	issuer, _ := newIssuer(t)
	token, err := issuer.Mint()
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if _, ok := issuer.Exchange(token); !ok {
		t.Fatal("first exchange must succeed")
	}
	if _, ok := issuer.Exchange(token); ok {
		t.Fatal("second exchange of the same token must fail")
	}
}

// TestBootstrapExpiry 验证过期 token 兑付失败且被销毁。
// TestBootstrapExpiry verifies expired tokens fail and are destroyed.
func TestBootstrapExpiry(t *testing.T) {
	issuer, _ := newIssuer(t)
	now := time.Now()
	issuer.now = func() time.Time { return now }
	token, err := issuer.Mint()
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	// 时钟推进超过 TTL。
	// Advance the clock beyond the TTL.
	now = now.Add(DefaultBootstrapTTL + time.Second)
	if _, ok := issuer.Exchange(token); ok {
		t.Fatal("expired token must fail")
	}
	// 过期兑付已销毁 token；回拨时钟也不能复活。
	// The expired exchange destroyed the token; rewinding the clock must not
	// revive it.
	now = time.Now()
	if _, ok := issuer.Exchange(token); ok {
		t.Fatal("destroyed token must stay dead")
	}
}

// TestBootstrapPasteFallback 验证粘贴实例凭据本身也可兑付（引导页的排障
// 兜底），且不消耗任何 token。
// TestBootstrapPasteFallback verifies the pasted instance capability itself
// also redeems (the unlock page's troubleshooting fallback) without consuming
// any token.
func TestBootstrapPasteFallback(t *testing.T) {
	issuer, cap := newIssuer(t)
	got, ok := issuer.Exchange(cap.String())
	if !ok {
		t.Fatal("pasted capability must redeem")
	}
	if got.String() != cap.String() {
		t.Fatal("paste redemption must return the instance capability")
	}
	if len(issuer.pending) != 0 {
		t.Fatal("paste redemption must not touch the token table")
	}
}

// TestBootstrapRejectsUnknown 验证未知 secret 兑付失败。
// TestBootstrapRejectsUnknown verifies unknown secrets fail.
func TestBootstrapRejectsUnknown(t *testing.T) {
	issuer, _ := newIssuer(t)
	if _, ok := issuer.Exchange("no-such-token"); ok {
		t.Fatal("unknown secret must fail")
	}
	if _, ok := issuer.Exchange(""); ok {
		t.Fatal("empty secret must fail")
	}
}

// newUnlockRouter 构造挂了 UnlockHandler 与受保护路由的最小 gin 引擎。
// newUnlockRouter builds a minimal gin engine with UnlockHandler and a
// protected route.
func newUnlockRouter(t *testing.T, issuer *BootstrapIssuer, cap Capability) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/unlock", UnlockHandler(issuer))
	protected := r.Group("/", RequireCapability(cap))
	protected.GET("/protected", func(c *gin.Context) { c.Status(http.StatusOK) })
	return r
}

// unlock 调 /unlock 并返回状态码与 Set-Cookie。
// unlock calls /unlock and returns the status code plus the Set-Cookie.
func unlock(t *testing.T, r *gin.Engine, body string) (int, *http.Cookie) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/unlock", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	r.ServeHTTP(recorder, req)
	var cookie *http.Cookie
	for _, c := range recorder.Result().Cookies() {
		if c.Name == CookieName {
			cookie = c
		}
	}
	return recorder.Code, cookie
}

// TestUnlockHandlerSetsCookie 验证兑付成功 → 204 + HttpOnly + SameSite=Strict
// 会话 cookie，且 cookie 值可通过 RequireCapability。
// TestUnlockHandlerSetsCookie verifies a successful redemption → 204 + an
// HttpOnly + SameSite=Strict session cookie whose value passes
// RequireCapability.
func TestUnlockHandlerSetsCookie(t *testing.T) {
	issuer, cap := newIssuer(t)
	r := newUnlockRouter(t, issuer, cap)
	token, err := issuer.Mint()
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	status, cookie := unlock(t, r, `{"secret": "`+token+`"}`)
	if status != http.StatusNoContent {
		t.Fatalf("unlock status = %d, want 204", status)
	}
	if cookie == nil {
		t.Fatal("unlock must set the capability cookie")
	}
	if !cookie.HttpOnly {
		t.Fatal("capability cookie must be HttpOnly")
	}
	if cookie.SameSite != http.SameSiteStrictMode {
		t.Fatalf("cookie SameSite = %v, want Strict", cookie.SameSite)
	}
	if cookie.Path != "/" {
		t.Fatalf("cookie Path = %q, want /", cookie.Path)
	}
	if cookie.Value != cap.String() {
		t.Fatal("cookie value must be the instance capability")
	}
	// cookie 直通受保护路由。
	// The cookie passes straight through the protected route.
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.AddCookie(cookie)
	recorder := httptest.NewRecorder()
	r.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusOK {
		t.Fatalf("protected with unlock cookie: status = %d, want 200", recorder.Code)
	}
}

// TestUnlockHandlerUniformFailure 验证失败形态统一：错误 token、粘贴错误
// 凭据、畸形 JSON 都是 401 auth_invalid，且都不设 cookie。
// TestUnlockHandlerUniformFailure verifies the uniform failure shape: wrong
// token, wrong pasted capability and malformed JSON all yield 401
// auth_invalid and set no cookie.
func TestUnlockHandlerUniformFailure(t *testing.T) {
	issuer, cap := newIssuer(t)
	r := newUnlockRouter(t, issuer, cap)
	for _, body := range []string{
		`{"secret": "wrong"}`,
		`{"secret": ""}`,
		`not-json`,
	} {
		status, cookie := unlock(t, r, body)
		if status != http.StatusUnauthorized {
			t.Errorf("body %q: status = %d, want 401", body, status)
		}
		if cookie != nil {
			t.Errorf("body %q: failure must not set a cookie", body)
		}
	}
	// 失败不消耗有效 token：铸造一个，先错一次，再成功兑付。
	// Failures must not consume valid tokens: mint one, fail once, then
	// redeem successfully.
	token, err := issuer.Mint()
	if err != nil {
		t.Fatalf("Mint: %v", err)
	}
	if status, _ := unlock(t, r, `{"secret": "wrong"}`); status != http.StatusUnauthorized {
		t.Fatalf("wrong secret status = %d, want 401", status)
	}
	if status, _ := unlock(t, r, `{"secret": "`+token+`"}`); status != http.StatusNoContent {
		t.Fatalf("valid token after a failure: status = %d, want 204", status)
	}
}

// TestUnlockFailureBody 验证失败响应体符合 #4 的 {error, error_kind} 形态。
// TestUnlockFailureBody verifies failure responses match #4's
// {error, error_kind} shape.
func TestUnlockFailureBody(t *testing.T) {
	issuer, cap := newIssuer(t)
	r := newUnlockRouter(t, issuer, cap)
	req := httptest.NewRequest(http.MethodPost, "/unlock", strings.NewReader(`{"secret": "wrong"}`))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	r.ServeHTTP(recorder, req)
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode failure body: %v", err)
	}
	if body["error_kind"] != ErrKindAuthInvalid {
		t.Fatalf("error_kind = %v, want %q", body["error_kind"], ErrKindAuthInvalid)
	}
	if _, ok := body["error"].(string); !ok {
		t.Fatal("failure body must carry an error message")
	}
}
