// bootstrap.go — 同源浏览器 Web UI 的一次性引导令牌（决策票 #8）。
// One-time bootstrap tokens for the same-origin browser Web UI (ticket #8).
//
// 问题：用户经 `pchat web` 或 GUI「在浏览器中打开」进入 Web UI 时，页面 JS
// 需要实例凭据，但凭据不得注入 HTML（任何 loopback 客户端都能 GET / 读到它，
// 等于把"loopback 可达"重新变成身份本身）。
//
// 方案：启动方（持凭据的父进程或独立 server 自己）铸造一次性、短 TTL 的
// bootstrap token，放进它打开的 URL（?pchat_bootstrap=<token>）；页面 JS 立即
// 用 token 调 Unlock 换取 HttpOnly cookie，随后 history.replaceState 擦除
// URL。token 一次性 + 短 TTL + 服务端无 access 日志（gin.New 无 Logger 中
// 间件，已核实），URL 泄露窗口收敛到交换前的一瞬间。
//
// Problem: when the user enters the Web UI via `pchat web` or the GUI's
// "open in browser", the page JS needs the instance capability, but the
// capability must not be injected into the served HTML (any loopback client
// could GET / and read it, turning "loopback reachable" back into identity).
//
// Solution: the launcher (the credential-holding parent, or a standalone
// server itself) mints a one-time, short-TTL bootstrap token placed in the
// URL it opens (?pchat_bootstrap=<token>); the page JS immediately exchanges
// it via Unlock for an HttpOnly cookie, then scrubs the URL with
// history.replaceState. One-time + short TTL + no server access log
// (verified: gin.New without the Logger middleware) shrink the URL exposure
// to the instant before exchange.
package auth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// DefaultBootstrapTTL 是 bootstrap token 的默认有效期。
// DefaultBootstrapTTL is the default bootstrap token lifetime.
const DefaultBootstrapTTL = 60 * time.Second

// bootstrapTokenBytes 是 token 的随机熵长度（hex 编码后 32 字符）。
// bootstrapTokenBytes is the token entropy size (32 hex chars encoded).
const bootstrapTokenBytes = 16

// BootstrapIssuer 铸造并兑付一次性 bootstrap token。兑付成功返回实例凭据，
// 由 HTTP 层写入 HttpOnly cookie；实例凭据本身永不进入响应体。
// BootstrapIssuer mints and redeems one-time bootstrap tokens. A successful
// redemption returns the instance capability so the HTTP layer can set it as
// an HttpOnly cookie; the capability itself never enters a response body.
type BootstrapIssuer struct {
	capability Capability
	ttl        time.Duration
	now        func() time.Time // 可注入时钟，便于测试 / injectable clock for tests

	mu      sync.Mutex
	pending map[string]time.Time // token → 过期时刻 / token → expiry
}

// NewBootstrapIssuer 以实例凭据构造签发器；ttl <= 0 时用默认值。
// NewBootstrapIssuer builds an issuer around the instance capability; a
// non-positive ttl falls back to the default.
func NewBootstrapIssuer(capability Capability, ttl time.Duration) *BootstrapIssuer {
	if ttl <= 0 {
		ttl = DefaultBootstrapTTL
	}
	return &BootstrapIssuer{
		capability: capability,
		ttl:        ttl,
		now:        time.Now,
		pending:    make(map[string]time.Time),
	}
}

// Mint 铸造一个一次性 bootstrap token。
// Mint creates a one-time bootstrap token.
func (b *BootstrapIssuer) Mint() (string, error) {
	buf := make([]byte, bootstrapTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("auth: mint bootstrap token: %w", err)
	}
	token := hex.EncodeToString(buf)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sweepLocked()
	b.pending[token] = b.now().Add(b.ttl)
	return token, nil
}

// Exchange 兑付引导 secret：一次性 token（兑付即销毁）或实例凭据本身
// （用户粘贴兜底）。成功返回实例凭据；失败返回 false。
// Exchange redeems a bootstrap secret: a one-time token (destroyed on
// redemption) or the instance capability itself (the paste fallback). On
// success it returns the instance capability; otherwise false.
func (b *BootstrapIssuer) Exchange(secret string) (Capability, bool) {
	if secret == "" {
		return Capability{}, false
	}
	// 粘贴兜底：常数时间比较，不消耗任何 token。
	// Paste fallback: constant-time compare, consumes no token.
	if b.capability.Verify(secret) {
		return b.capability, true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.sweepLocked()
	expiry, ok := b.pending[secret]
	if !ok {
		return Capability{}, false
	}
	// 无论是否过期都销毁：token 一次性，且过期 token 不留残迹。
	// Destroy regardless of expiry: tokens are one-time, and expired tokens
	// leave no residue.
	delete(b.pending, secret)
	if b.now().After(expiry) {
		return Capability{}, false
	}
	return b.capability, true
}

// sweepLocked 惰性清理过期 token；调用方须持锁。
// sweepLocked lazily sweeps expired tokens; the caller must hold the lock.
func (b *BootstrapIssuer) sweepLocked() {
	now := b.now()
	for token, expiry := range b.pending {
		if now.After(expiry) {
			delete(b.pending, token)
		}
	}
}

// unlockRequest 是 UnlockHandler 的请求体。
// unlockRequest is UnlockHandler's request body.
type unlockRequest struct {
	Secret string `json:"secret"`
}

// UnlockHandler 把 bootstrap token（或粘贴的实例凭据）换成 HttpOnly +
// SameSite=Strict 的会话 cookie。cookie 值即实例凭据（无状态，随实例生灭），
// JS 不可读；同源 fetch/SSE/WS 自动携带。失败一律 401 auth_invalid，不区分
// token 过期/不存在/凭据错误，避免泄露 token 状态。
// UnlockHandler exchanges a bootstrap token (or a pasted instance
// capability) for an HttpOnly + SameSite=Strict session cookie. The cookie
// value is the capability itself (stateless, living and dying with the
// instance), unreadable from JS, and carried automatically by same-origin
// fetch/SSE/WS. Failures are uniformly 401 auth_invalid — no distinction
// between expired/missing token and wrong capability, so token state is
// not leaked.
func UnlockHandler(issuer *BootstrapIssuer) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req unlockRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":      "bootstrap secret rejected",
				"error_kind": ErrKindAuthInvalid,
			})
			return
		}
		capability, ok := issuer.Exchange(req.Secret)
		if !ok {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":      "bootstrap secret rejected",
				"error_kind": ErrKindAuthInvalid,
			})
			return
		}
		// 会话 cookie（无 MaxAge/Expires）：随浏览器会话结束；实例凭据本身
		// 也随 server 进程生灭。SameSite=Strict 阻断跨站携带；HttpOnly 阻断
		// JS 读取。loopback 为 http，不设 Secure；非本机模式启用 HTTPS 时应
		// 补 Secure（记录于决策地图 #8 约束）。
		// Session cookie (no MaxAge/Expires): dies with the browser session,
		// and the capability itself dies with the server process anyway.
		// SameSite=Strict blocks cross-site carriage; HttpOnly blocks JS
		// reads. Loopback is http, so no Secure; non-loopback mode behind
		// HTTPS must add it (recorded in the decision map #8 constraints).
		http.SetCookie(c.Writer, &http.Cookie{
			Name:     CookieName,
			Value:    capability.String(),
			Path:     "/",
			HttpOnly: true,
			SameSite: http.SameSiteStrictMode,
		})
		c.Status(http.StatusNoContent)
	}
}
