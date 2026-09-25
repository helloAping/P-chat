// pairing_test.go — 决策票 #3 原型的验收测试。
// Acceptance tests for the ticket #3 pairing prototype.
//
// 覆盖票要求的全部场景：首次配对、旧扩展升级（fail closed）、撤销、
// Runtime Instance 重启（凭据持久化）、多浏览器各自独立、拒绝与超时。
// Covers every scenario the ticket calls out: first pairing, old
// extension upgrade (fail closed), revocation, Runtime Instance restart
// (credential persistence), independent multi-browser pairing, denial
// and timeout.
package browser

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

// pairTestProtocol 是原型要求配对时的期望协议版本（与生产 "3" 解耦）。
// pairTestProtocol is the expected protocol version when the prototype
// requires pairing (decoupled from production "3").
const pairTestProtocol = "4"

// pairingEnv 是装配好的配对测试环境：httptest server + Pairer + Hub。
// 测试 handler 复刻生产 BrowserWebSocket 的 accept → hello → 注册流程，
// 仅在注册前插入配对握手。
// pairingEnv is a wired pairing rig: httptest server + Pairer + Hub.
// The test handler mirrors the production BrowserWebSocket flow
// (accept → hello → register) with the pairing handshake inserted
// before registration.
type pairingEnv struct {
	t      *testing.T
	dir    string
	store  *PairingStore
	pairer *Pairer
	hub    *BridgeHub
	srv    *httptest.Server
}

func startPairingEnv(t *testing.T) *pairingEnv {
	t.Helper()
	dir := t.TempDir()
	store, err := OpenPairingStore(filepath.Join(dir, "pairing.json"))
	if err != nil {
		t.Fatalf("OpenPairingStore: %v", err)
	}
	pairer := NewPairer(store, pairTestProtocol)
	hub := NewHub()
	go hub.Run()
	t.Cleanup(hub.Stop)

	env := &pairingEnv{t: t, dir: dir, store: store, pairer: pairer, hub: hub}
	env.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		ctx := r.Context()
		hello, ok := readTestHello(ctx, conn)
		if !ok {
			return
		}
		paired, ok := pairer.RunPairingHandshake(ctx, conn, hello, r.RemoteAddr)
		if !ok {
			return
		}
		client := NewBrowserClient(paired.ID, paired.BrowserName, conn, paired)
		go client.StartReadPump(ctx)
		hub.Register(client)
		<-client.Done()
	}))
	t.Cleanup(env.srv.Close)
	return env
}

// readTestHello 与生产 readHello 同等宽容：接受 {method,params} 包装或裸参数。
// readTestHello is as lenient as the production readHello: it accepts
// the {method,params} wrapper or bare params.
func readTestHello(ctx context.Context, conn *websocket.Conn) (HelloParams, bool) {
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var raw json.RawMessage
	if err := wsjson.Read(rctx, conn, &raw); err != nil {
		_ = conn.Close(websocket.StatusProtocolError, "hello failed")
		return HelloParams{}, false
	}
	var wrapped struct {
		Method string      `json:"method"`
		Params HelloParams `json:"params"`
	}
	if err := json.Unmarshal(raw, &wrapped); err == nil && wrapped.Method == "hello" {
		return wrapped.Params, true
	}
	var direct HelloParams
	if err := json.Unmarshal(raw, &direct); err == nil {
		return direct, true
	}
	_ = conn.Close(websocket.StatusProtocolError, "hello parse failed")
	return HelloParams{}, false
}

// dialHello 模拟扩展：拨号并发送包装形式的 hello。
// dialHello simulates the extension: dials and sends the wrapped hello.
func dialHello(t *testing.T, srv *httptest.Server, h HelloParams) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/v1/browser/ws"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	wctx, wcancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer wcancel()
	if err := wsjson.Write(wctx, conn, map[string]any{"method": "hello", "params": h}); err != nil {
		t.Fatalf("write hello: %v", err)
	}
	return conn
}

// readServerMsg 读一帧 server → extension 消息。
// readServerMsg reads one server → extension frame.
func readServerMsg(t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	rctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var msg map[string]any
	if err := wsjson.Read(rctx, conn, &msg); err != nil {
		t.Fatalf("read server message: %v", err)
	}
	return msg
}

// expectClose 断言连接以指定码关闭。
// expectClose asserts the connection closes with the given code.
func expectClose(t *testing.T, conn *websocket.Conn, code websocket.StatusCode) {
	t.Helper()
	var msg map[string]any
	err := wsjson.Read(context.Background(), conn, &msg)
	var cerr websocket.CloseError
	if !errors.As(err, &cerr) {
		t.Fatalf("want close error %d, got err=%v msg=%v", int(code), err, msg)
	}
	if cerr.Code != code {
		t.Fatalf("close code = %d, want %d (reason %q)", int(cerr.Code), int(code), cerr.Reason)
	}
}

// waitHubCount 轮询等待 Hub 连接数达到期望值（注册经 channel 异步完成）。
// waitHubCount polls until the Hub reaches the wanted connection count
// (registration is async via a channel).
func waitHubCount(t *testing.T, hub *BridgeHub, want int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if hub.Count() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("hub count = %d, want %d", hub.Count(), want)
}

// completePairing 走完整首次配对并返回 (browserID, token)。
// completePairing runs a full first-pairing round trip and returns
// (browserID, token).
func completePairing(t *testing.T, env *pairingEnv, browserName string) (string, string) {
	t.Helper()
	conn := dialHello(t, env.srv, HelloParams{BrowserName: browserName, ProtocolVersion: pairTestProtocol})
	defer conn.Close(websocket.StatusNormalClosure, "test done")

	msg := readServerMsg(t, conn)
	if msg["type"] != "pair_required" {
		t.Fatalf("first message = %v, want pair_required", msg)
	}
	requestID, _ := msg["request_id"].(string)
	if requestID == "" {
		t.Fatalf("pair_required missing request_id: %v", msg)
	}
	if _, err := env.pairer.Approve(requestID); err != nil {
		t.Fatalf("Approve: %v", err)
	}
	msg = readServerMsg(t, conn)
	if msg["type"] != "pair_approved" {
		t.Fatalf("second message = %v, want pair_approved", msg)
	}
	browserID, _ := msg["browser_id"].(string)
	token, _ := msg["pair_token"].(string)
	if browserID == "" || token == "" {
		t.Fatalf("pair_approved missing fields: %v", msg)
	}
	// server 以 1000 关闭，提示扩展凭据落盘后重连。
	// The server closes with 1000, telling the extension to persist the
	// credential and reconnect.
	expectClose(t, conn, websocket.StatusNormalClosure)
	return browserID, token
}

// TestPairingFirstConnectFlow 验证首次配对路径：
// 无凭据 hello → pair_required → GUI 批准 → pair_approved → 凭据重连 → 注册进 Hub。
// TestPairingFirstConnectFlow verifies the first-pairing path:
// credential-less hello → pair_required → GUI approval → pair_approved
// → reconnect with credential → registered in the Hub.
func TestPairingFirstConnectFlow(t *testing.T) {
	env := startPairingEnv(t)

	conn := dialHello(t, env.srv, HelloParams{BrowserName: "Edge 141", ProtocolVersion: pairTestProtocol})

	msg := readServerMsg(t, conn)
	if msg["type"] != "pair_required" {
		t.Fatalf("first message = %v, want pair_required", msg)
	}
	requestID, _ := msg["request_id"].(string)

	// 待配对请求应对 GUI 可见，且携带请求方信息。
	// The pending request must be visible to the GUI with requester info.
	pending := env.pairer.ListPending()
	if len(pending) != 1 || pending[0].RequestID != requestID {
		t.Fatalf("ListPending = %+v, want request %q", pending, requestID)
	}
	if pending[0].BrowserName != "Edge 141" || pending[0].RemoteAddr == "" {
		t.Fatalf("pending entry missing requester info: %+v", pending[0])
	}
	// 待配对期间连接不得注册进 Hub。
	// A pending connection must never be registered in the Hub.
	if env.hub.HasConnections() {
		t.Fatal("pending connection must not be registered in the hub")
	}

	approved, err := env.pairer.Approve(requestID)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	msg = readServerMsg(t, conn)
	if msg["type"] != "pair_approved" || msg["browser_id"] != approved.BrowserID {
		t.Fatalf("pair_approved = %v, want browser_id %q", msg, approved.BrowserID)
	}
	expectClose(t, conn, websocket.StatusNormalClosure)
	conn.Close(websocket.StatusNormalClosure, "test done")

	// 凭据落盘后才算配对完成。
	// Pairing is complete only once the credential hits disk.
	if !env.store.Validate(approved.BrowserID, approved.PairToken) {
		t.Fatal("store does not validate the issued credential")
	}

	// 扩展凭据重连 → hello-ok → 注册进 Hub。
	// The extension reconnects with the credential → hello-ok → Hub.
	conn2 := dialHello(t, env.srv, HelloParams{
		BrowserName:     "Edge 141",
		ProtocolVersion: pairTestProtocol,
		ID:              approved.BrowserID,
		PairToken:       approved.PairToken,
	})
	defer conn2.Close(websocket.StatusNormalClosure, "test done")
	msg = readServerMsg(t, conn2)
	if msg["type"] != "hello-ok" || msg["browser_id"] != approved.BrowserID {
		t.Fatalf("reconnect hello-ok = %v, want browser_id %q", msg, approved.BrowserID)
	}
	waitHubCount(t, env.hub, 1)
}

// TestPairingOldExtensionFailsClosed 验证旧扩展升级路径：
// v3 hello（无配对字段）→ hello-ok + update_required → 4001 关闭 → 不进 Hub。
// TestPairingOldExtensionFailsClosed verifies the old-extension upgrade
// path: a v3 hello (no pairing fields) → hello-ok + update_required →
// 4001 close → never registered.
func TestPairingOldExtensionFailsClosed(t *testing.T) {
	env := startPairingEnv(t)

	// 旧扩展：协议 v3，完全不知道配对字段。
	// Old extension: protocol v3, unaware of any pairing fields.
	conn := dialHello(t, env.srv, HelloParams{BrowserName: "Chrome 132", ProtocolVersion: "3"})
	defer conn.Close(websocket.StatusNormalClosure, "test done")

	msg := readServerMsg(t, conn)
	if msg["type"] != "hello-ok" {
		t.Fatalf("old extension first message = %v, want hello-ok", msg)
	}
	if msg["update_required"] != true {
		t.Fatalf("update_required = %v, want true", msg["update_required"])
	}
	if um, _ := msg["update_message"].(string); !strings.Contains(um, "重新下载") {
		t.Fatalf("update_message = %q, want reinstall hint", um)
	}
	expectClose(t, conn, CloseCodeUpdateRequired)
	if env.hub.HasConnections() {
		t.Fatal("old extension must not be registered in the hub")
	}
	if got := len(env.pairer.ListPending()); got != 0 {
		t.Fatalf("old extension must not create pending requests, got %d", got)
	}
}

// TestPairingRevokedCredentialRejected 验证撤销路径：
// 撤销后旧凭据重连被拒（4002），且不得进 Hub。
// TestPairingRevokedCredentialRejected verifies revocation: reconnecting
// with a revoked credential is rejected (4002) and never registered.
func TestPairingRevokedCredentialRejected(t *testing.T) {
	env := startPairingEnv(t)
	browserID, token := completePairing(t, env, "Edge 141")

	if err := env.pairer.Revoke(browserID); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if env.store.Validate(browserID, token) {
		t.Fatal("revoked credential still validates")
	}

	conn := dialHello(t, env.srv, HelloParams{
		BrowserName:     "Edge 141",
		ProtocolVersion: pairTestProtocol,
		ID:              browserID,
		PairToken:       token,
	})
	defer conn.Close(websocket.StatusNormalClosure, "test done")
	msg := readServerMsg(t, conn)
	if msg["type"] != "hello-error" {
		t.Fatalf("revoked reconnect first message = %v, want hello-error", msg)
	}
	expectClose(t, conn, CloseCodeCredentialRejected)
	if env.hub.HasConnections() {
		t.Fatal("revoked connection must not be registered in the hub")
	}
}

// TestPairingPersistsAcrossStoreReload 验证 Runtime Instance 重启路径：
// 凭据落盘后，新 Pairer + 重新加载的 store 仍然放行。
// TestPairingPersistsAcrossStoreReload verifies the Runtime Instance
// restart path: after the credential hits disk, a fresh Pairer on a
// reloaded store still accepts it.
func TestPairingPersistsAcrossStoreReload(t *testing.T) {
	env := startPairingEnv(t)
	browserID, token := completePairing(t, env, "Chrome 141")

	// 模拟进程重启：从同一文件重新加载 store，换一个新 Pairer。
	// Simulate a process restart: reload the store from the same file
	// and build a fresh Pairer on it.
	reopened, err := OpenPairingStore(filepath.Join(env.dir, "pairing.json"))
	if err != nil {
		t.Fatalf("reopen store: %v", err)
	}
	if !reopened.Validate(browserID, token) {
		t.Fatal("reloaded store does not validate the credential")
	}
	pairer2 := NewPairer(reopened, pairTestProtocol)
	decision, _ := pairer2.HandleHello(HelloParams{
		BrowserName:     "Chrome 141",
		ProtocolVersion: pairTestProtocol,
		ID:              browserID,
		PairToken:       token,
	}, "127.0.0.1:5555")
	if decision != DecidePaired {
		t.Fatalf("decision after reload = %v, want DecidePaired", decision)
	}
}

// TestPairingMultiBrowserIndependent 验证多浏览器各自独立配对、独立撤销。
// TestPairingMultiBrowserIndependent verifies multiple browsers pair
// independently and revoke independently.
func TestPairingMultiBrowserIndependent(t *testing.T) {
	env := startPairingEnv(t)
	idA, tokenA := completePairing(t, env, "Edge 141")
	idB, tokenB := completePairing(t, env, "Chrome 141")

	if idA == idB || tokenA == tokenB {
		t.Fatal("two browsers must get independent identities and credentials")
	}
	if err := env.pairer.Revoke(idA); err != nil {
		t.Fatalf("Revoke A: %v", err)
	}
	if env.store.Validate(idA, tokenA) {
		t.Fatal("A should be revoked")
	}
	if !env.store.Validate(idB, tokenB) {
		t.Fatal("B must stay valid after revoking A")
	}
}

// TestPairingDeniedClosesConnection 验证拒绝路径：pair_denied + 4003。
// TestPairingDeniedClosesConnection verifies denial: pair_denied + 4003.
func TestPairingDeniedClosesConnection(t *testing.T) {
	env := startPairingEnv(t)
	conn := dialHello(t, env.srv, HelloParams{BrowserName: "Edge 141", ProtocolVersion: pairTestProtocol})
	defer conn.Close(websocket.StatusNormalClosure, "test done")

	msg := readServerMsg(t, conn)
	requestID, _ := msg["request_id"].(string)
	if err := env.pairer.Deny(requestID, "user denied"); err != nil {
		t.Fatalf("Deny: %v", err)
	}
	msg = readServerMsg(t, conn)
	if msg["type"] != "pair_denied" {
		t.Fatalf("message = %v, want pair_denied", msg)
	}
	expectClose(t, conn, CloseCodePairingDenied)
	if env.store.List(); len(env.store.List()) != 0 {
		t.Fatal("denied pairing must not persist a credential")
	}
}

// TestPairingUnknownCredentialRejected 验证伪造凭据被拒（4002）。
// TestPairingUnknownCredentialRejected verifies a forged credential is
// rejected (4002).
func TestPairingUnknownCredentialRejected(t *testing.T) {
	env := startPairingEnv(t)
	conn := dialHello(t, env.srv, HelloParams{
		BrowserName:     "Evil",
		ProtocolVersion: pairTestProtocol,
		ID:              "browser-nope",
		PairToken:       "bogus-token",
	})
	defer conn.Close(websocket.StatusNormalClosure, "test done")
	msg := readServerMsg(t, conn)
	if msg["type"] != "hello-error" {
		t.Fatalf("message = %v, want hello-error", msg)
	}
	expectClose(t, conn, CloseCodeCredentialRejected)
}

// TestPairingPendingExpiry 验证待配对请求过期：批准后失败、握手超时关闭 4000。
// TestPairingPendingExpiry verifies pending expiry: approval fails after
// the TTL and the handshake closes with 4000.
func TestPairingPendingExpiry(t *testing.T) {
	env := startPairingEnv(t)
	env.pairer.ttl = 80 * time.Millisecond

	conn := dialHello(t, env.srv, HelloParams{BrowserName: "Edge 141", ProtocolVersion: pairTestProtocol})
	defer conn.Close(websocket.StatusNormalClosure, "test done")
	msg := readServerMsg(t, conn)
	requestID, _ := msg["request_id"].(string)

	time.Sleep(200 * time.Millisecond)
	if _, err := env.pairer.Approve(requestID); err == nil {
		t.Fatal("Approve on expired request must fail")
	}
	if got := len(env.pairer.ListPending()); got != 0 {
		t.Fatalf("expired pending entries must be pruned, got %d", got)
	}
	expectClose(t, conn, CloseCodePairingTimeout)
}

// TestHandleHelloDecisions 覆盖状态机的纯决策表。
// TestHandleHelloDecisions covers the pure decision table.
func TestHandleHelloDecisions(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenPairingStore(filepath.Join(dir, "pairing.json"))
	if err != nil {
		t.Fatalf("OpenPairingStore: %v", err)
	}
	if err := store.Put("browser-good", PairRecord{Token: "tok", Name: "Edge"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	pairer := NewPairer(store, pairTestProtocol)

	cases := []struct {
		name string
		hello HelloParams
		want HelloDecision
	}{
		{"valid credential", HelloParams{ProtocolVersion: pairTestProtocol, ID: "browser-good", PairToken: "tok"}, DecidePaired},
		{"wrong token", HelloParams{ProtocolVersion: pairTestProtocol, ID: "browser-good", PairToken: "bad"}, DecideRejected},
		{"unknown id", HelloParams{ProtocolVersion: pairTestProtocol, ID: "browser-x", PairToken: "tok"}, DecideRejected},
		{"no credential", HelloParams{ProtocolVersion: pairTestProtocol}, DecidePending},
		{"id but no token (re-pair)", HelloParams{ProtocolVersion: pairTestProtocol, ID: "browser-good"}, DecidePending},
		{"old protocol", HelloParams{ProtocolVersion: "3"}, DecideUpdateRequired},
		{"missing protocol", HelloParams{}, DecideUpdateRequired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, pp := pairer.HandleHello(tc.hello, "127.0.0.1:1")
			if got != tc.want {
				t.Fatalf("HandleHello = %v, want %v", got, tc.want)
			}
			if got == DecidePending {
				if pp == nil {
					t.Fatal("pending decision must return a PendingPair")
				}
				pairer.removePending(pp.RequestID)
			}
		})
	}
}

// TestPairingStorePersistence 覆盖凭据文件的落盘、重载、幂等撤销与损坏检测。
// TestPairingStorePersistence covers persistence, reload, idempotent
// revocation and corruption detection for the credential file.
func TestPairingStorePersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pairing.json")

	store, err := OpenPairingStore(path)
	if err != nil {
		t.Fatalf("open missing file: %v", err)
	}
	if err := store.Put("browser-1", PairRecord{Token: "tok-1", Name: "Edge 141"}); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// 原子写不留临时文件。
	// The atomic write leaves no temp files behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".pairing-") {
			t.Fatalf("leftover temp file: %s", e.Name())
		}
	}
	// 凭据文件必须 0600（Windows 的权限模型不同，跳过）。
	// The credential file must be 0600 (skipped on Windows where the
	// permission model differs).
	if runtime.GOOS != "windows" {
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		if perm := fi.Mode().Perm(); perm != 0o600 {
			t.Fatalf("file perm = %o, want 600", perm)
		}
	}

	reopened, err := OpenPairingStore(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if !reopened.Validate("browser-1", "tok-1") {
		t.Fatal("reloaded store lost the credential")
	}
	if err := reopened.Revoke("browser-1"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if err := reopened.Revoke("browser-1"); err != nil {
		t.Fatalf("Revoke must be idempotent: %v", err)
	}
	reopened2, err := OpenPairingStore(path)
	if err != nil {
		t.Fatalf("reopen after revoke: %v", err)
	}
	if reopened2.Validate("browser-1", "tok-1") {
		t.Fatal("revoked credential survived a reload")
	}

	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("corrupt file: %v", err)
	}
	if _, err := OpenPairingStore(path); err == nil {
		t.Fatal("corrupt file must fail to open")
	}
}
