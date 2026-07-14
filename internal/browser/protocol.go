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

// JSON-RPC 2.0 wire types. These are intentionally simple and
// mirror internal/mcp/types.go so the extension protocol is
// consistent with MCP.

// Request is a JSON-RPC 2.0 request sent from server to extension.
type Request struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      int64       `json:"id"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params,omitempty"`
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
	Method string     `json:"method"`
	Params HelloParams `json:"params"`
}

// HelloParams is the body of the hello handshake.
type HelloParams struct {
	BrowserName string `json:"browser_name"`
	TabsCount   int    `json:"tabs_count"`
	ID          string `json:"id,omitempty"` // empty on first connect, set on reconnect
}

// HelloResponse is sent by the server after accepting a connection.
type HelloResponse struct {
	Type      string `json:"type"` // "hello-ok" or "hello-error"
	BrowserID string `json:"browser_id,omitempty"`
	Error     string `json:"error,omitempty"`
}

// BrowserClient represents a single browser extension WebSocket connection.
type BrowserClient struct {
	mu   sync.Mutex
	id   string
	name string // "Chrome 132", "Edge 131", etc.

	conn    *websocket.Conn
	pending map[int64]chan Response
	seq     int64 // atomic; incremented per request

	done chan struct{} // closed when connection tears down
}

// NewBrowserClient wraps an upgraded WebSocket connection.
func NewBrowserClient(id, name string, conn *websocket.Conn) *BrowserClient {
	return &BrowserClient{
		id:      id,
		name:    name,
		conn:    conn,
		pending: make(map[int64]chan Response),
		done:    make(chan struct{}),
	}
}

// ID returns the browser's unique identifier.
func (c *BrowserClient) ID() string { return c.id }

// Name returns the browser's display name (e.g. "Chrome 132").
func (c *BrowserClient) Name() string { return c.name }

// Done returns a channel that closes when the connection tears down.
func (c *BrowserClient) Done() <-chan struct{} { return c.done }

// SendCommand sends a JSON-RPC request to the extension and blocks
// until a response arrives or the context expires. The timeout
// parameter controls the per-request WebSocket write deadline; the
// call itself respects ctx.
func (c *BrowserClient) SendCommand(ctx context.Context, method string, params interface{}, timeout time.Duration) (*Response, error) {
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
		return nil, fmt.Errorf("write command %s: %w", method, err)
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case resp, ok := <-ch:
		if !ok {
			return nil, fmt.Errorf("connection closed while waiting for response")
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
			// Connection closed or errored; tear down.
			c.closeAllPending()
			return
		}
		c.mu.Lock()
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
