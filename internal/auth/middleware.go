// middleware.go — 实例凭据的 HTTP 传输校验（决策票 #8）。
// HTTP transport verification for the instance capability (ticket #8).
//
// 传输矩阵（与决策地图 #8 Answer 对齐）：
//   - Go 客户端（CLI / GUI 后端）与 Wails webview：Authorization: Bearer 头。
//     webview 的 fetch 可携带自定义头（现有 X-Trace-Id 已证明），且全部
//     SSE 走 fetch（无 EventSource），故头方案覆盖所有前端传输。
//   - 同源浏览器 Web UI：HttpOnly + SameSite=Strict cookie（UnlockHandler
//     设置），fetch/SSE/WS 升级自动携带，JS 不可读（抗 XSS）。
//   - Browser Extension WS：凭据在 hello 首包（#3），不经过本中间件。
//
// Transport matrix (aligned with the decision map #8 Answer):
//   - Go clients (CLI / GUI backend) and the Wails webview:
//     Authorization: Bearer header. Webview fetch can carry custom headers
//     (proven by X-Trace-Id) and every SSE stream is fetch-based (no
//     EventSource), so the header covers all frontend transports.
//   - Same-origin browser Web UI: HttpOnly + SameSite=Strict cookie (set by
//     UnlockHandler); carried automatically by fetch/SSE/WS upgrades and
//     unreadable from JS (XSS resilient).
//   - Browser Extension WS: credential rides the hello first message (#3),
//     not this middleware.
package auth

import (
	"net"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// CookieName 是同源浏览器 Web UI 持有实例凭据的 cookie 名。
// CookieName is the cookie holding the instance capability for the
// same-origin browser Web UI.
const CookieName = "pchat_cap"

// 错误形态与决策票 #4 的失败默认值对齐：{error, error_kind}。
// Error shapes align with ticket #4's failure defaults: {error, error_kind}.
const (
	// ErrKindAuthRequired 请求未携带任何凭据。
	// ErrKindAuthRequired indicates the request carried no credential.
	ErrKindAuthRequired = "auth_required"
	// ErrKindAuthInvalid 请求携带了凭据但不匹配。
	// ErrKindAuthInvalid indicates a presented credential did not match.
	ErrKindAuthInvalid = "auth_invalid"
	// ErrKindHostNotAllowed 请求 Host 不在允许列表（DNS rebinding 拦截）。
	// ErrKindHostNotAllowed indicates a Host outside the allowlist (DNS
	// rebinding interception).
	ErrKindHostNotAllowed = "host_not_allowed"
)

// RequireCapability 要求请求持有有效实例凭据。Authorization 头优先且具
// 排他性（存在时不回退 cookie，避免双凭据语义含糊）；两者皆无 → 401
// auth_required；携带但不匹配 → 401 auth_invalid。
// RequireCapability demands a valid instance capability. The Authorization
// header wins and is exclusive (no cookie fallback when present, keeping
// dual-credential requests unambiguous); neither present → 401
// auth_required; present but mismatched → 401 auth_invalid.
func RequireCapability(cap Capability) gin.HandlerFunc {
	return func(c *gin.Context) {
		candidate, presented := bearerToken(c.GetHeader("Authorization"))
		if !presented {
			if cookie, err := c.Cookie(CookieName); err == nil {
				candidate, presented = cookie, true
			}
		}
		if !presented {
			// 文案面向旧客户端可读：旧前端的 jsonFetch / consumeStreamRequest
			// 会把错误体文本原样抛给用户，因此必须自带可操作的恢复指引。
			// The message must be self-explanatory for old clients: their
			// jsonFetch / consumeStreamRequest surfaces the raw body text to
			// the user, so it carries actionable recovery guidance.
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":      "P-Chat 服务已升级安全策略，请重启或升级客户端后重试 (instance capability required: restart or upgrade the P-Chat client)",
				"error_kind": ErrKindAuthRequired,
			})
			return
		}
		if !cap.Verify(candidate) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{
				"error":      "实例凭据无效或已轮换，请重启客户端后重试 (instance capability rejected: restart the P-Chat client)",
				"error_kind": ErrKindAuthInvalid,
			})
			return
		}
		c.Next()
	}
}

// HostAllowlist 只放行 loopback 与显式配置的 Host，拦截 DNS rebinding：
// 攻击者域名重绑定到 127.0.0.1 后，其页面请求的 Host 仍是攻击者域名。
// extra 用于非本机 Runtime Access Mode 下放行配置的绑定地址/域名。
// HostAllowlist permits only loopback and explicitly configured Hosts,
// intercepting DNS rebinding: after an attacker's domain rebinds to
// 127.0.0.1, their page's requests still carry the attacker's Host.
// extra allows the configured bind address/name under non-loopback
// Runtime Access Modes.
func HostAllowlist(extra ...string) gin.HandlerFunc {
	allowed := make(map[string]struct{}, len(extra))
	for _, h := range extra {
		if normalized := normalizeHost(h); normalized != "" {
			allowed[normalized] = struct{}{}
		}
	}
	return func(c *gin.Context) {
		host := normalizeHost(c.Request.Host)
		if host == "" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error":      "host header missing or invalid",
				"error_kind": ErrKindHostNotAllowed,
			})
			return
		}
		if _, ok := allowed[host]; !ok && !isLoopbackHost(host) {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"error":      "host not allowed",
				"error_kind": ErrKindHostNotAllowed,
			})
			return
		}
		c.Next()
	}
}

// bearerToken 解析 Authorization: Bearer <token>；第二个返回值报告是否
// 携带了 Bearer 凭据（哪怕值为空，也视为"携带了"，以便走 auth_invalid）。
// bearerToken parses Authorization: Bearer <token>; the second return value
// reports whether a Bearer credential was presented at all (even an empty
// value counts as presented, routing to auth_invalid).
func bearerToken(header string) (string, bool) {
	if header == "" {
		return "", false
	}
	scheme, token, ok := strings.Cut(header, " ")
	if !ok || !strings.EqualFold(scheme, "bearer") {
		return "", false
	}
	return strings.TrimSpace(token), true
}

// normalizeHost 去掉端口并规范化大小写；非法输入返回空串。
// normalizeHost strips the port and lowercases; invalid input yields "".
func normalizeHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return ""
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	// IPv6 字面量可能带方括号（[::1]）。
	// IPv6 literals may arrive bracketed ([::1]).
	host = strings.TrimPrefix(strings.TrimSuffix(host, "]"), "[")
	return strings.ToLower(host)
}

// isLoopbackHost 报告主机名是否为 loopback（localhost / 127.0.0.0/8 / ::1）。
// isLoopbackHost reports whether the hostname is loopback (localhost /
// 127.0.0.0/8 / ::1).
func isLoopbackHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
