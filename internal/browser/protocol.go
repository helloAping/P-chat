// Package browser implements a WebSocket bridge between the P-Chat
// server and Chrome browser extensions, enabling LLM-driven browser
// control through registered tools.
//
// The architecture mirrors the existing MCP integration: a Hub
// manages multiple connected browsers, tools are dynamically
// registered when connections arrive, and JSON-RPC 2.0 messages
// flow between server and extension over WebSocket.
//
// File layout:
//   - protocol.go: JSON-RPC wire types, BrowserClient struct
//   - hub.go: BridgeHub (connection pool, command routing)
//   - manager.go: lifecycle (start/stop, config, tool coordination)
//   - tools.go: (P2) tool definitions and handlers
//   - handler.go: (P4) HTTP API + WebSocket upgrade handler
package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"nhooyr.io/websocket"
	"nhooyr.io/websocket/wsjson"
)

// ProtocolVersion is the browser extension wire protocol version the
// current server expects. Bump this when browser command semantics or
// the handshake change in a way that requires users to reinstall the
// extension package.
const ProtocolVersion = "3"

// JSON-RPC 2.0 wire types. These are intentionally simple and
// mirror internal/mcp/types.go so the extension protocol is
// consistent with MCP.

// Request is a JSON-RPC 2.0 request sent from server to extension.
type Request struct {
	JSONRPC string `json:"jsonrpc"`
	ID      int64  `json:"id"`
	Method  string `json:"method"`
	Params  any    `json:"params,omitempty"`
}

// Response is a JSON-RPC 2.0 response sent from extension to server.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      int64           `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

// RPCError carries a structured error from the extension.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("rpc error %d: %s", e.Code, e.Message)
}

// HelloRequest is the first message the extension sends upon
// connecting. It carries browser metadata so the server can
// identify the connection.
type HelloRequest struct {
	Method string      `json:"method"`
	Params HelloParams `json:"params"`
}

// HelloParams is the body of the hello handshake.
type HelloParams struct {
	BrowserName      string `json:"browser_name"`
	TabsCount        int    `json:"tabs_count"`
	ExtensionVersion string `json:"extension_version,omitempty"`
	ProtocolVersion  string `json:"protocol_version,omitempty"`
	ID               string `json:"id,omitempty"` // empty on first connect, set on reconnect
}

// HelloResponse is sent by the server after accepting a connection.
type HelloResponse struct {
	Type                    string `json:"type"` // "hello-ok" or "hello-error"
	BrowserID               string `json:"browser_id,omitempty"`
	ProtocolVersion         string `json:"protocol_version,omitempty"`
	ExpectedProtocolVersion string `json:"expected_protocol_version,omitempty"`
	UpdateRequired          bool   `json:"update_required,omitempty"`
	UpdateMessage           string `json:"update_message,omitempty"`
	Error                   string `json:"error,omitempty"`
}

// BrowserClient represents a single browser extension WebSocket connection.
type BrowserClient struct {
	mu   sync.Mutex
	id   string
	name string // "Chrome 132", "Edge 131", etc.

	tabsCount        int
	activeTabID      int
	activeTabTitle   string
	activeTabURL     string
	extensionVersion string
	protocolVersion  string
	lastSeen         time.Time
	lastError        string
	lastErrorAt      time.Time

	conn    *websocket.Conn
	pending map[int64]chan Response
	seq     int64 // atomic; incremented per request

	done chan struct{} // closed when connection tears down
}

// NewBrowserClient wraps an upgraded WebSocket connection.
func NewBrowserClient(id, name string, conn *websocket.Conn, meta HelloParams) *BrowserClient {
	now := time.Now()
	return &BrowserClient{
		id:               id,
		name:             name,
		tabsCount:        meta.TabsCount,
		extensionVersion: meta.ExtensionVersion,
		protocolVersion:  meta.ProtocolVersion,
		lastSeen:         now,
		conn:             conn,
		pending:          make(map[int64]chan Response),
		done:             make(chan struct{}),
	}
}

// ID returns the browser's unique identifier.
func (c *BrowserClient) ID() string { return c.id }

// Name returns the browser's display name (e.g. "Chrome 132").
func (c *BrowserClient) Name() string { return c.name }

// SetActiveTabMeta caches the currently preferred control target tab.
// Called after ListTabs / SetActiveTab so /browser/list can show the target
// without an extra round-trip.
func (c *BrowserClient) SetActiveTabMeta(id int, title, url string, tabsCount int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.activeTabID = id
	c.activeTabTitle = title
	c.activeTabURL = url
	if tabsCount > 0 {
		c.tabsCount = tabsCount
	}
	c.recordSeenLocked()
}

// ActiveTabID returns the cached preferred tab id (0 if unknown).
func (c *BrowserClient) ActiveTabID() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.activeTabID
}

// ActiveTabURL returns the cached preferred tab URL ("" if unknown).
// Used by BR-04 permission gating when the tool does not carry a URL arg.
func (c *BrowserClient) ActiveTabURL() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.activeTabURL
}

// Done returns a channel that closes when the connection tears down.
func (c *BrowserClient) Done() <-chan struct{} { return c.done }

// Snapshot returns the metadata used by the settings diagnostics UI.
func (c *BrowserClient) Snapshot() ClientSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return ClientSnapshot{
		ID:               c.id,
		Name:             c.name,
		TabsCount:        c.tabsCount,
		ActiveTabID:      c.activeTabID,
		ActiveTabTitle:   c.activeTabTitle,
		ActiveTabURL:     c.activeTabURL,
		ExtensionVersion: c.extensionVersion,
		ProtocolVersion:  c.protocolVersion,
		LastSeen:         c.lastSeen,
		LastError:        c.lastError,
		LastErrorAt:      c.lastErrorAt,
	}
}

func (c *BrowserClient) recordSeenLocked() {
	c.lastSeen = time.Now()
}

func (c *BrowserClient) recordError(err string) {
	if err == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lastError = err
	c.lastErrorAt = time.Now()
}

// SendCommand sends a JSON-RPC request to the extension and blocks
// until a response arrives or the context expires. The timeout
// parameter controls the per-request WebSocket write deadline; the
// call itself respects ctx.
func (c *BrowserClient) SendCommand(ctx context.Context, method string, params any, timeout time.Duration) (*Response, error) {
	c.mu.Lock()
	c.seq++
	id := c.seq
	ch := make(chan Response, 1)
	c.pending[id] = ch
	conn := c.conn
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()

	req := Request{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}

	wctx, wcancel := context.WithTimeout(ctx, timeout)
	defer wcancel()
	if err := wsjson.Write(wctx, conn, req); err != nil {
		c.recordError(err.Error())
		return nil, fmt.Errorf("write command %s: %w", method, err)
	}

	select {
	case <-ctx.Done():
		c.recordError(ctx.Err().Error())
		return nil, ctx.Err()
	case resp, ok := <-ch:
		if !ok {
			c.recordError("connection closed while waiting for response")
			return nil, fmt.Errorf("connection closed while waiting for response")
		}
		if resp.Error != nil {
			c.recordError(resp.Error.Error())
		}
		return &resp, nil
	}
}

// readPump continuously reads JSON-RPC responses from the WebSocket
// and dispatches them to the matching pending channel. It runs until
// the connection is closed.
func (c *BrowserClient) readPump(ctx context.Context) {
	defer close(c.done)
	for {
		var resp Response
		rctx, rcancel := context.WithTimeout(ctx, time.Minute)
		err := wsjson.Read(rctx, c.conn, &resp) //nolint: bodyclose
		rcancel()
		if err != nil {
			c.recordError(err.Error())
			// Connection closed or errored; tear down.
			c.closeAllPending()
			return
		}
		c.mu.Lock()
		c.recordSeenLocked()
		ch, ok := c.pending[resp.ID]
		c.mu.Unlock()
		if ok {
			select {
			case ch <- resp:
			default:
				// Channel full (shouldn't happen, cap=1), drop.
			}
		}
	}
}

// StartReadPump starts the read loop in the current goroutine. It
// blocks until the connection is closed, so call it from a goroutine.
func (c *BrowserClient) StartReadPump(ctx context.Context) {
	c.readPump(ctx)
}

// closeAllPending sends a zero Response to all waiting callers and
// closes their channels. Called when readPump exits.
func (c *BrowserClient) closeAllPending() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id, ch := range c.pending {
		close(ch)
		delete(c.pending, id)
	}
}
