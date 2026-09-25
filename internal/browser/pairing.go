// pairing.go — Browser Extension 配对原型（决策票 #3）。
// Pairing prototype for Browser Extensions (decision ticket #3).
//
// 设计要点（与 docs/plans/runtime-trust-decision-map.md #2/#3 对齐）：
//   - 配对在 WS 通道内完成：hello 无凭据 → 连接进入待配对（不注册进 Hub、
//     命令不可达）→ Local Operator 在 GUI 单击批准 → server 经 WS 下发凭据。
//   - 配对凭据是 Runtime Profile 级长期凭据，持久化于 Data Home（0600 JSON），
//     跨 Runtime Instance 重启保持有效，并按 browserID 独立撤销。
//   - 旧协议扩展 fail closed：先回 update_required（popup 已有更新提示 UI），
//     再以 4xxx 关闭；扩展现有逻辑把 4000-4999 视为永久失败并停止重连。
//   - 本文件只提供状态机与握手驱动；接入生产 HTTP handler、扩展端 JS 与
//     GUI 批准界面属于 #7 的实现切片。
//
// Design notes (aligned with docs/plans/runtime-trust-decision-map.md #2/#3):
//   - Pairing rides the WebSocket channel: a hello without a credential
//     parks the connection in a pending state (never registered in the
//     Hub, unreachable by commands) until a Local Operator approves it
//     in the GUI; approval pushes the credential over the WS.
//   - The pairing credential is a Runtime Profile level long-term
//     credential persisted in the Data Home (0600 JSON); it survives
//     Runtime Instance restarts and is revocable per browser ID.
//   - Old-protocol extensions fail closed: they get update_required
//     first (the popup already has the update UI), then a 4xxx close;
//     the extension already treats 4000-4999 as permanent failure.
//   - This file provides only the state machine and the handshake
//     driver; wiring it into the production HTTP handler, the extension
//     JS and the GUI approval UI are #7 slices.
package browser

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

// 配对关闭码（4000-4999）。扩展已把该区间视为永久失败：停止自动重连，
// popup 展示拒绝原因。
// Pairing close codes (4000-4999). The extension already treats this
// range as permanent failure: it stops auto-reconnecting and the popup
// surfaces the rejection.
const (
	// CloseCodePairingTimeout 待配对超时（无人批准）。
	// CloseCodePairingTimeout indicates the pending request expired.
	CloseCodePairingTimeout = websocket.StatusCode(4000)
	// CloseCodeUpdateRequired 旧协议扩展必须重新下载安装。
	// CloseCodeUpdateRequired tells old extensions to reinstall.
	CloseCodeUpdateRequired = websocket.StatusCode(4001)
	// CloseCodeCredentialRejected 凭据无效或已撤销；扩展应清除本地凭据后重新配对。
	// CloseCodeCredentialRejected means the credential is invalid or
	// revoked; the extension should clear it and re-pair.
	CloseCodeCredentialRejected = websocket.StatusCode(4002)
	// CloseCodePairingDenied 用户拒绝了配对请求。
	// CloseCodePairingDenied means the user denied the pairing request.
	CloseCodePairingDenied = websocket.StatusCode(4003)
)

// PairRequiredMessage 告知扩展：连接已进入待配对，等待 GUI 批准。
// PairRequiredMessage tells the extension the connection is parked
// pending GUI approval.
type PairRequiredMessage struct {
	Type      string `json:"type"` // "pair_required"
	RequestID string `json:"request_id"`
}

// PairApprovedMessage 在批准后下发配对凭据；扩展持久化后凭此重连。
// PairApprovedMessage delivers the pairing credential after approval;
// the extension persists it and reconnects with it.
type PairApprovedMessage struct {
	Type      string `json:"type"` // "pair_approved"
	BrowserID string `json:"browser_id"`
	PairToken string `json:"pair_token"`
}

// PairDeniedMessage 告知扩展配对请求被拒绝。
// PairDeniedMessage tells the extension the pairing request was denied.
type PairDeniedMessage struct {
	Type   string `json:"type"` // "pair_denied"
	Reason string `json:"reason,omitempty"`
}

// PairRecord 是一个已配对扩展的 Runtime Profile 级长期凭据记录。
// PairRecord is the Runtime Profile level long-term credential record
// of one paired extension.
type PairRecord struct {
	Token      string `json:"token"`
	Name       string `json:"name"`
	PairedAt   string `json:"paired_at"`
	LastSeenAt string `json:"last_seen_at,omitempty"`
}

// pairingFile 是 pairing.json 的磁盘格式。
// pairingFile is the on-disk format of pairing.json.
type pairingFile struct {
	Version int                   `json:"version"`
	Pairs   map[string]PairRecord `json:"pairs"`
}

// PairingStore 持久化已配对扩展凭据；文件路径由调用方注入（Data Home），
// 便于测试用临时目录隔离。
// PairingStore persists paired-extension credentials; the file path is
// injected by the caller (Data Home) so tests can isolate with a temp dir.
type PairingStore struct {
	path  string
	mu    sync.Mutex
	pairs map[string]PairRecord
}

// OpenPairingStore 加载（或初始化）配对凭据文件。
// OpenPairingStore loads (or initialises) the pairing credential file.
func OpenPairingStore(path string) (*PairingStore, error) {
	s := &PairingStore{path: path, pairs: map[string]PairRecord{}}
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		var f pairingFile
		if err := json.Unmarshal(data, &f); err != nil {
			return nil, fmt.Errorf("browser pairing: decode %s: %w", path, err)
		}
		for id, rec := range f.Pairs {
			s.pairs[id] = rec
		}
	case errors.Is(err, os.ErrNotExist):
		// 首次运行：空库，首次批准时才落盘。
		// First run: empty store, persisted on first approval.
	default:
		return nil, fmt.Errorf("browser pairing: read %s: %w", path, err)
	}
	return s, nil
}

// Validate 校验 browserID + token 组合是否仍然有效。
// Validate reports whether the browserID + token pair is still valid.
func (s *PairingStore) Validate(browserID, token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.pairs[browserID]
	return ok && token != "" && rec.Token == token
}

// Put 写入一条配对记录并落盘。
// Put stores one pairing record and persists it.
func (s *PairingStore) Put(browserID string, rec PairRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pairs[browserID] = rec
	return s.persistLocked()
}

// Revoke 删除单个浏览器的配对凭据；幂等。
// Revoke removes one browser's credential; idempotent.
func (s *PairingStore) Revoke(browserID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.pairs[browserID]; !ok {
		return nil
	}
	delete(s.pairs, browserID)
	return s.persistLocked()
}

// Touch 刷新最近可见时间（尽力而为，失败仅记日志）。
// Touch refreshes last-seen (best-effort; failures are only logged).
func (s *PairingStore) Touch(browserID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.pairs[browserID]
	if !ok {
		return
	}
	rec.LastSeenAt = time.Now().Format(time.RFC3339)
	s.pairs[browserID] = rec
	if err := s.persistLocked(); err != nil {
		log.Printf("[browser] pairing persist last_seen: %v", err)
	}
}

// List 返回全部配对记录的副本。
// List returns a copy of all pairing records.
func (s *PairingStore) List() map[string]PairRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]PairRecord, len(s.pairs))
	for id, rec := range s.pairs {
		out[id] = rec
	}
	return out
}

// persistLocked 原子写盘（tmp + rename，0600），沿用启动公告的保存模式。
// persistLocked atomically writes the store (tmp + rename, 0600),
// following the startup-announcement persistence pattern.
func (s *PairingStore) persistLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("browser pairing: create dir: %w", err)
	}
	payload, err := json.MarshalIndent(pairingFile{Version: 1, Pairs: s.pairs}, "", "  ")
	if err != nil {
		return fmt.Errorf("browser pairing: encode: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.path), ".pairing-*.tmp")
	if err != nil {
		return fmt.Errorf("browser pairing: create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("browser pairing: protect temp file: %w", err)
	}
	if _, err := tmp.Write(payload); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("browser pairing: write temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("browser pairing: close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		return fmt.Errorf("browser pairing: publish: %w", err)
	}
	return nil
}

// PendingPair 是一个等待 GUI 批准的配对请求。
// PendingPair is a pairing request waiting for GUI approval.
type PendingPair struct {
	RequestID        string
	HelloID          string // 扩展自报的 browserID（重配对时复用）
	BrowserName      string
	ExtensionVersion string
	RemoteAddr       string
	RequestedAt      time.Time
	expiresAt        time.Time
	approveCh        chan PairApprovedMessage
	denyCh           chan string
}

// PendingPairInfo 是待配对请求的可序列化视图（供未来的 GUI 列表）。
// PendingPairInfo is the serialisable view of a pending request (for
// the future GUI list).
type PendingPairInfo struct {
	RequestID        string `json:"request_id"`
	BrowserName      string `json:"browser_name"`
	ExtensionVersion string `json:"extension_version,omitempty"`
	RemoteAddr       string `json:"remote_addr"`
	RequestedAt      string `json:"requested_at"`
	ExpiresAt        string `json:"expires_at"`
}

// HelloDecision 是 Pairer 对一次 hello 的决策。
// HelloDecision is the Pairer's verdict for one hello.
type HelloDecision int

const (
	// DecidePaired 凭据有效，允许注册进 Hub。
	// DecidePaired means the credential is valid; registration allowed.
	DecidePaired HelloDecision = iota
	// DecidePending 无凭据：进入待配对，等待 GUI 批准。
	// DecidePending means no credential: park until GUI approval.
	DecidePending
	// DecideUpdateRequired 旧协议扩展：fail closed 并提示更新。
	// DecideUpdateRequired means an old-protocol extension: fail closed.
	DecideUpdateRequired
	// DecideRejected 凭据无效或已撤销。
	// DecideRejected means the credential is invalid or revoked.
	DecideRejected
)

// Pairer 是扩展配对状态机。它只关心配对决策与待配对请求的生命周期；
// WebSocket 读写由 RunPairingHandshake 完成，在线踢出由接线层完成。
// Pairer is the extension pairing state machine. It owns pairing
// decisions and the pending-request lifecycle; WebSocket IO lives in
// RunPairingHandshake, and kicking live connections is the wiring
// layer's job.
type Pairer struct {
	store            *PairingStore
	expectedProtocol string
	ttl              time.Duration

	mu      sync.Mutex
	pending map[string]*PendingPair
}

// NewPairer 创建配对状态机。expectedProtocol 是要求配对时扩展必须上报的
// 协议版本；它与生产 ProtocolVersion 解耦，版本提升节奏由 #6 决定。
// NewPairer builds the state machine. expectedProtocol is the protocol
// version an extension must report when pairing is required; it is
// decoupled from the production ProtocolVersion because the bump
// cadence is decided by ticket #6.
func NewPairer(store *PairingStore, expectedProtocol string) *Pairer {
	return &Pairer{
		store:            store,
		expectedProtocol: expectedProtocol,
		ttl:              2 * time.Minute,
		pending:          map[string]*PendingPair{},
	}
}

// HandleHello 把一次 hello 映射为配对决策。
// HandleHello maps one hello to a pairing decision.
func (p *Pairer) HandleHello(h HelloParams, remoteAddr string) (HelloDecision, *PendingPair) {
	if h.ProtocolVersion != p.expectedProtocol {
		return DecideUpdateRequired, nil
	}
	if h.PairToken != "" {
		if p.store.Validate(h.ID, h.PairToken) {
			p.store.Touch(h.ID)
			return DecidePaired, nil
		}
		return DecideRejected, nil
	}
	// 无凭据：登记待配对请求。扩展自报 ID 时保留，批准时复用。
	// No credential: register a pending request. A self-reported ID is
	// kept and reused on approval.
	pp := &PendingPair{
		RequestID:        "pair-" + NewBrowserID()[8:], // 复用随机段，避免再引随机源
		HelloID:          h.ID,
		BrowserName:      h.BrowserName,
		ExtensionVersion: h.ExtensionVersion,
		RemoteAddr:       remoteAddr,
		RequestedAt:      time.Now(),
		expiresAt:        time.Now().Add(p.ttl),
		approveCh:        make(chan PairApprovedMessage, 1),
		denyCh:           make(chan string, 1),
	}
	p.mu.Lock()
	p.pending[pp.RequestID] = pp
	p.mu.Unlock()
	return DecidePending, pp
}

// Approve 批准待配对请求：复用或生成 browserID、签发凭据并持久化，
// 然后通知等待中的握手 goroutine。
// Approve approves a pending request: it reuses or mints the browser ID,
// issues and persists the credential, then signals the waiting handshake.
func (p *Pairer) Approve(requestID string) (PairApprovedMessage, error) {
	p.mu.Lock()
	pp, ok := p.pending[requestID]
	if ok {
		delete(p.pending, requestID)
	}
	p.mu.Unlock()
	if !ok {
		return PairApprovedMessage{}, fmt.Errorf("browser pairing: unknown request %q", requestID)
	}
	if time.Now().After(pp.expiresAt) {
		return PairApprovedMessage{}, fmt.Errorf("browser pairing: request %q expired", requestID)
	}
	browserID := pp.HelloID
	if browserID == "" {
		browserID = NewBrowserID()
	}
	token, err := newPairToken()
	if err != nil {
		return PairApprovedMessage{}, err
	}
	rec := PairRecord{
		Token:    token,
		Name:     pp.BrowserName,
		PairedAt: time.Now().Format(time.RFC3339),
	}
	if err := p.store.Put(browserID, rec); err != nil {
		return PairApprovedMessage{}, err
	}
	msg := PairApprovedMessage{Type: "pair_approved", BrowserID: browserID, PairToken: token}
	// 通道带缓冲，等待者缺席（连接已断开）也不阻塞。
	// The channel is buffered, so a missing waiter (dropped connection)
	// never blocks.
	select {
	case pp.approveCh <- msg:
	default:
	}
	return msg, nil
}

// Deny 拒绝待配对请求并通知等待中的握手 goroutine。
// Deny rejects a pending request and signals the waiting handshake.
func (p *Pairer) Deny(requestID, reason string) error {
	p.mu.Lock()
	pp, ok := p.pending[requestID]
	if ok {
		delete(p.pending, requestID)
	}
	p.mu.Unlock()
	if !ok {
		return fmt.Errorf("browser pairing: unknown request %q", requestID)
	}
	select {
	case pp.denyCh <- reason:
	default:
	}
	return nil
}

// Revoke 撤销单个浏览器的配对凭据。在线连接的踢出（hub.Unregister +
// CloseCodeCredentialRejected）由接线层完成。
// Revoke revokes one browser's credential. Kicking a live connection
// (hub.Unregister + CloseCodeCredentialRejected) is the wiring layer's job.
func (p *Pairer) Revoke(browserID string) error {
	return p.store.Revoke(browserID)
}

// ListPending 返回未过期的待配对请求（按时间升序），并顺手清理过期项。
// ListPending returns unexpired pending requests (oldest first) and
// prunes expired ones along the way.
func (p *Pairer) ListPending() []PendingPairInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now()
	out := make([]PendingPairInfo, 0, len(p.pending))
	for id, pp := range p.pending {
		if now.After(pp.expiresAt) {
			delete(p.pending, id)
			continue
		}
		out = append(out, PendingPairInfo{
			RequestID:        pp.RequestID,
			BrowserName:      pp.BrowserName,
			ExtensionVersion: pp.ExtensionVersion,
			RemoteAddr:       pp.RemoteAddr,
			RequestedAt:      pp.RequestedAt.Format(time.RFC3339),
			ExpiresAt:        pp.expiresAt.Format(time.RFC3339),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RequestedAt < out[j].RequestedAt })
	return out
}

// removePending 在握手结束（无论成败）后清理待配对项。
// removePending drops the pending entry once the handshake ends.
func (p *Pairer) removePending(requestID string) {
	p.mu.Lock()
	delete(p.pending, requestID)
	p.mu.Unlock()
}

// RunPairingHandshake 在 WebSocket Accept 之后、Hub.Register 之前执行配对握手。
// 仅当凭据有效时返回 paired=true 与填充好的 hello；其余路径负责把决策
// 写到线上并关闭连接。
// RunPairingHandshake runs the pairing handshake after the WebSocket
// Accept and before Hub.Register. It returns paired=true with the hello
// to register only when the credential is valid; every other path writes
// the decision on the wire and closes the connection.
func (p *Pairer) RunPairingHandshake(ctx context.Context, conn *websocket.Conn, hi HelloParams, remoteAddr string) (HelloParams, bool) {
	decision, pp := p.HandleHello(hi, remoteAddr)
	switch decision {
	case DecidePaired:
		if !p.writeJSON(ctx, conn, HelloResponse{
			Type:                    "hello-ok",
			BrowserID:               hi.ID,
			ProtocolVersion:         hi.ProtocolVersion,
			ExpectedProtocolVersion: p.expectedProtocol,
		}) {
			return HelloParams{}, false
		}
		return hi, true

	case DecideUpdateRequired:
		// 旧扩展：先给 hello-ok + update_required（popup 已有更新提示 UI），
		// 再以 4001 关闭；扩展现有逻辑把 4xxx 视为永久失败并停止重连。
		// Old extension: hello-ok + update_required first (the popup
		// already has the update UI), then close 4001; the extension
		// treats 4xxx as permanent and stops reconnecting.
		p.writeJSON(ctx, conn, HelloResponse{
			Type:                    "hello-ok",
			ProtocolVersion:         hi.ProtocolVersion,
			ExpectedProtocolVersion: p.expectedProtocol,
			UpdateRequired:          true,
			UpdateMessage:           pairingUpdateMessage(hi.ProtocolVersion, p.expectedProtocol),
		})
		p.close(conn, CloseCodeUpdateRequired, "protocol update required")
		return HelloParams{}, false

	case DecideRejected:
		p.writeJSON(ctx, conn, HelloResponse{
			Type:  "hello-error",
			Error: "pairing credential rejected",
		})
		p.close(conn, CloseCodeCredentialRejected, "credential rejected")
		return HelloParams{}, false

	case DecidePending:
		defer p.removePending(pp.RequestID)
		if !p.writeJSON(ctx, conn, PairRequiredMessage{Type: "pair_required", RequestID: pp.RequestID}) {
			return HelloParams{}, false
		}
		timer := time.NewTimer(p.ttl)
		defer timer.Stop()
		select {
		case msg := <-pp.approveCh:
			// 批准后凭据落盘成功才下发；扩展持久化后重连，下一条连接
			// 走 DecidePaired 单一路径。
			// The credential is persisted before it is sent; the
			// extension stores it and reconnects, so the next connection
			// takes the single DecidePaired path.
			p.writeJSON(ctx, conn, msg)
			p.close(conn, websocket.StatusNormalClosure, "pairing complete, reconnect")
		case reason := <-pp.denyCh:
			p.writeJSON(ctx, conn, PairDeniedMessage{Type: "pair_denied", Reason: reason})
			p.close(conn, CloseCodePairingDenied, "pairing denied")
		case <-timer.C:
			p.close(conn, CloseCodePairingTimeout, "pairing approval timeout")
		case <-ctx.Done():
			p.close(conn, websocket.StatusGoingAway, "server shutting down")
		}
		return HelloParams{}, false
	}
	return HelloParams{}, false
}

// writeJSON 带超时写一帧；失败返回 false。
// writeJSON writes one frame with a timeout; false on failure.
func (p *Pairer) writeJSON(ctx context.Context, conn *websocket.Conn, v any) bool {
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := wsjson.Write(wctx, conn, v); err != nil {
		log.Printf("[browser] pairing write %T: %v", v, err)
		return false
	}
	return true
}

// close 以指定码关闭连接（尽力而为）。
// close closes the connection with the given code (best-effort).
func (p *Pairer) close(conn *websocket.Conn, code websocket.StatusCode, reason string) {
	if err := conn.Close(code, reason); err != nil {
		log.Printf("[browser] pairing close %d: %v", int(code), err)
	}
}

// newPairToken 生成 256 位随机配对凭据。
// newPairToken mints a 256-bit random pairing credential.
func newPairToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("browser pairing: mint token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// pairingUpdateMessage 与 UpdateMessage 同形，但期望版本由 Pairer 注入。
// pairingUpdateMessage mirrors UpdateMessage but takes the expected
// version from the Pairer.
func pairingUpdateMessage(got, expected string) string {
	if got == "" {
		return "浏览器扩展未上报协议版本，请重新下载并安装最新扩展。"
	}
	return fmt.Sprintf("浏览器扩展协议版本 %s 与服务端期望版本 %s 不一致，请重新下载并安装最新扩展。", got, expected)
}
