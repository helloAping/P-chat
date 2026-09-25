// compat_test.go — 滚动升级兼容矩阵（决策票 #6）的可执行规格。每个测试对应
// 矩阵一格；「旧扩展 + 新 server」一格由 internal/browser/pairing_test.go 的
// update_required + 4001 路径覆盖（旧扩展把 4000-4999 视为永久失败、停止
// 自动重连，不会重新开启 Browser Control）。
// compat_test.go — the rolling-upgrade compatibility matrix (ticket #6) as
// an executable specification. Each test is one matrix cell; the "old
// extension + new server" cell is covered by internal/browser/pairing_test.go
// (update_required + 4001: old extensions treat 4000-4999 as permanent
// failure, stop auto-reconnecting, and never re-enable Browser Control).
package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// matrixCell 跑一次「请求 → 断言状态码」并返回错误体。
// matrixCell performs one "request → assert status" step and returns the
// decoded error body.
func matrixCell(t *testing.T, r *gin.Engine, authHeader string, wantStatus int) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	recorder := httptest.NewRecorder()
	r.ServeHTTP(recorder, req)
	if recorder.Code != wantStatus {
		t.Fatalf("status = %d, want %d", recorder.Code, wantStatus)
	}
	var body map[string]any
	if recorder.Body.Len() > 0 {
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode body: %v", err)
		}
	}
	return body
}

// TestCompatNewParentNewServer 矩阵主路径：新父进程经环境变量下发凭据，
// 新 server 执行校验——标准升级（updater 退出旧 GUI → 换二进制 → 重启新
// GUI）后零人工步骤。
// TestCompatNewParentNewServer is the matrix's main path: the new parent
// hands the capability down via environment and the new server enforces it —
// zero manual steps after a standard upgrade (updater exits the old GUI,
// replaces binaries, relaunches the new GUI).
func TestCompatNewParentNewServer(t *testing.T) {
	t.Setenv(CapabilityEnv, "parent-minted-capability")
	cap, err := ResolveCapability()
	if err != nil {
		t.Fatalf("ResolveCapability: %v", err)
	}
	r := newProtectedRouter(t, cap)
	matrixCell(t, r, "Bearer parent-minted-capability", http.StatusOK)
}

// TestCompatOldParentNewServerFailsClosed 矩阵关键格：旧父进程（不下发凭据）
// spawn 新 server——唯一现实触发点是旧 GUI 运行中 server 崩溃、
// watchAndRestart 拉起已升级的新二进制。server 自铸凭据并 fail closed；
// 旧客户端收到带可操作指引的 401，重启客户端即恢复。
// TestCompatOldParentNewServerFailsClosed is the matrix's key cell: an old
// parent (no capability hand-down) spawns a new server — the only realistic
// trigger is a server crash under a running old GUI, where watchAndRestart
// launches the upgraded binary. The server self-mints and fails closed; old
// clients get a 401 with actionable guidance and recover by restarting.
func TestCompatOldParentNewServerFailsClosed(t *testing.T) {
	t.Setenv(CapabilityEnv, "")
	cap, err := ResolveCapability()
	if err != nil {
		t.Fatalf("ResolveCapability: %v", err)
	}
	if cap.IsZero() {
		t.Fatal("server must self-mint when the parent hands nothing down")
	}
	r := newProtectedRouter(t, cap)
	body := matrixCell(t, r, "", http.StatusUnauthorized)
	if body["error_kind"] != ErrKindAuthRequired {
		t.Fatalf("error_kind = %v, want %q", body["error_kind"], ErrKindAuthRequired)
	}
	// 旧前端会把错误体文本原样抛给用户（jsonFetch: HTTP <status>: <body>），
	// 文案必须自带恢复指引。
	// Old frontends surface the raw body text (jsonFetch: HTTP <status>:
	// <body>), so the message must carry recovery guidance.
	msg, _ := body["error"].(string)
	if !strings.Contains(msg, "重启") || !strings.Contains(msg, "restart") {
		t.Fatalf("error message must guide a client restart, got %q", msg)
	}
}

// TestCompatNewClientOldServer 前向兼容格：新客户端对旧 server（无鉴权
// 中间件）多发 Authorization 头，旧 server 直接忽略——且生产 CORS 预检
// 已放行 Authorization（server.go 的 Access-Control-Allow-Headers）。
// TestCompatNewClientOldServer is the forward-compat cell: a new client
// sends an extra Authorization header to an old server (no auth middleware),
// which ignores it — and production CORS preflight already permits
// Authorization (server.go's Access-Control-Allow-Headers).
func TestCompatNewClientOldServer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New() // 旧 server：无 RequireCapability / old server: no RequireCapability
	r.GET("/protected", func(c *gin.Context) { c.Status(http.StatusOK) })
	matrixCell(t, r, "Bearer ignored-by-old-server", http.StatusOK)
}

// TestCompatRotatedCapabilityRejectsOldCredential 重启即换新格：server 重启
// （含独立 server 场景）后凭据轮换，旧 cookie / 旧粘贴值一律 auth_invalid，
// 且文案指引重启客户端。
// TestCompatRotatedCapabilityRejectsOldCredential is the rotation cell: after
// a server restart (including standalone runs) the capability rotates, and
// stale cookies / pasted values uniformly get auth_invalid with restart
// guidance.
func TestCompatRotatedCapabilityRejectsOldCredential(t *testing.T) {
	oldCap, err := NewCapability()
	if err != nil {
		t.Fatalf("NewCapability: %v", err)
	}
	newCap, err := NewCapability()
	if err != nil {
		t.Fatalf("NewCapability: %v", err)
	}
	r := newProtectedRouter(t, newCap)
	body := matrixCell(t, r, "Bearer "+oldCap.String(), http.StatusUnauthorized)
	if body["error_kind"] != ErrKindAuthInvalid {
		t.Fatalf("error_kind = %v, want %q", body["error_kind"], ErrKindAuthInvalid)
	}
	msg, _ := body["error"].(string)
	if !strings.Contains(msg, "重启") {
		t.Fatalf("error message must guide a client restart, got %q", msg)
	}
}

// TestCompatHealthAdvertisesAuthRequired capability discovery 格：/health
// 广告 auth_required，浏览器端 JS 在首个业务请求前主动展示引导页。
// TestCompatHealthAdvertisesAuthRequired is the capability-discovery cell:
// /health advertises auth_required so browser JS can proactively show the
// unlock page before the first business request.
func TestCompatHealthAdvertisesAuthRequired(t *testing.T) {
	cap, err := NewCapability()
	if err != nil {
		t.Fatalf("NewCapability: %v", err)
	}
	fields := HealthFields(cap)
	if fields[HealthAuthRequiredField] != true {
		t.Fatalf("health must advertise %s=true, got %v", HealthAuthRequiredField, fields[HealthAuthRequiredField])
	}
	var zero Capability
	if HealthFields(zero)[HealthAuthRequiredField] != false {
		t.Fatal("zero capability must advertise auth_required=false")
	}
}
