// BridgeHub manages a pool of connected browser extensions and routes
// commands to them by ID. It is safe for concurrent use.
//
// The lifecycle pattern is identical to internal/mcp/manager.go:
// a single goroutine (run) owns the clients map and processes
// register/unregister operations via channels, so no mutex is
// required around the map itself. A mutex (commandMu) guards
// the read-only snapshot operations.
package browser

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"nhooyr.io/websocket"
)

// BrowserInfo is the serialisable summary exposed via the HTTP API.
type BrowserInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	ConnectedAt string `json:"connected_at"`
}

// BridgeHub is the central registry of connected browser clients.
type BridgeHub struct {
	mu       sync.RWMutex
	clients  map[string]*clientEntry
	enabled  bool
	regCh    chan *BrowserClient
	unregCh  chan string // browserID → remove
	stopOnce sync.Once
	stopCh   chan struct{}

	// onToolsChange is called whenever the set of active connections
	// transitions between zero and non-zero (or vice versa), so the
	// manager can register/unregister browser tools in the
	// tool.Registry dynamically.
	onToolsChange func(hasConnections bool)
}

type clientEntry struct {
	client      *BrowserClient
	connectedAt time.Time
}

// NewHub returns a fresh BridgeHub. Call Run() to start the
// background goroutine.
func NewHub() *BridgeHub {
	return &BridgeHub{
		clients: make(map[string]*clientEntry),
		regCh:   make(chan *BrowserClient, 32),
		unregCh: make(chan string, 32),
		stopCh:  make(chan struct{}),
	}
}

// SetOnToolsChange registers a callback invoked when the connection
// pool transitions between empty and non-empty. The manager uses
// this to register/unregister browser tools dynamically.
func (h *BridgeHub) SetOnToolsChange(fn func(bool)) {
	h.mu.Lock()
	h.onToolsChange = fn
	h.mu.Unlock()
}

// Run starts the hub's background goroutine. It blocks until
// Stop() is called.
func (h *BridgeHub) Run() {
	for {
		select {
		case <-h.stopCh:
			h.closeAll()
			return

		case c := <-h.regCh:
			h.registerClient(c)

		case id := <-h.unregCh:
			h.mu.Lock()
			entry, ok := h.clients[id]
			if ok {
				_ = entry.client.conn.Close(websocket.StatusNormalClosure, "unregistered")
				delete(h.clients, id)
			}
			// Fire callback if pool became empty.
			isEmpty := len(h.clients) == 0
			h.mu.Unlock()
			if ok && isEmpty {
				h.fireToolsChange(false)
			}
		}
	}
}

// Stop shuts down the hub, closing all active connections.
func (h *BridgeHub) Stop() {
	h.stopOnce.Do(func() { close(h.stopCh) })
}

// Register adds a newly-connected browser client to the hub.
// It is safe to call from HTTP handlers.
func (h *BridgeHub) Register(c *BrowserClient) {
	select {
	case h.regCh <- c:
	case <-h.stopCh:
	}
}

// Unregister removes a browser client by ID.
func (h *BridgeHub) Unregister(id string) {
	select {
	case h.unregCh <- id:
	case <-h.stopCh:
	}
}

// SendCommand routes a command to the specified browser. If browserID
// is empty, the first available client is used (default browser).
func (h *BridgeHub) SendCommand(ctx context.Context, browserID string, method string, params interface{}, timeout time.Duration) (*Response, error) {
	c, err := h.getClient(browserID)
	if err != nil {
		return nil, err
	}
	return c.SendCommand(ctx, method, params, timeout)
}

// List returns information about all currently connected browsers.
func (h *BridgeHub) List() []BrowserInfo {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]BrowserInfo, 0, len(h.clients))
	for _, e := range h.clients {
		out = append(out, BrowserInfo{
			ID:          e.client.ID(),
			Name:        e.client.Name(),
			ConnectedAt: e.connectedAt.Format(time.RFC3339),
		})
	}
	return out
}

// HasConnections reports whether at least one browser is connected.
func (h *BridgeHub) HasConnections() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients) > 0
}

// Count returns the number of active connections.
func (h *BridgeHub) Count() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

// SetEnabled toggles the hub's master switch. When disabled, new
// connections should be rejected by the HTTP handler (not by the
// hub itself — it's the handler's responsibility).
func (h *BridgeHub) SetEnabled(on bool) {
	h.mu.Lock()
	h.enabled = on
	h.mu.Unlock()
}

// IsEnabled reports whether the browser control feature is active.
func (h *BridgeHub) IsEnabled() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.enabled
}

func (h *BridgeHub) getClient(id string) (*BrowserClient, error) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if id != "" {
		e, ok := h.clients[id]
		if !ok {
			return nil, fmt.Errorf("browser %q not connected", id)
		}
		return e.client, nil
	}
	// Default: return first connected client.
	for _, e := range h.clients {
		return e.client, nil
	}
	return nil, fmt.Errorf("no browser connected")
}

func (h *BridgeHub) registerClient(c *BrowserClient) {
	h.mu.Lock()
	wasEmpty := len(h.clients) == 0
	h.clients[c.ID()] = &clientEntry{
		client:      c,
		connectedAt: time.Now(),
	}
	h.mu.Unlock()
	if wasEmpty {
		h.fireToolsChange(true)
	}
	// Watch for connection teardown and auto-unregister.
	go func() {
		<-c.Done()
		log.Printf("[browser] client %s disconnected", c.ID())
		h.Unregister(c.ID())
	}()
}

func (h *BridgeHub) fireToolsChange(hasConnections bool) {
	h.mu.RLock()
	fn := h.onToolsChange
	h.mu.RUnlock()
	if fn != nil {
		fn(hasConnections)
	}
}

func (h *BridgeHub) closeAll() {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, e := range h.clients {
		_ = e.client.conn.Close(websocket.StatusNormalClosure, "hub shutdown")
		delete(h.clients, id)
	}
	h.fireToolsChange(false)
}

// NewBrowserID generates a new unique identifier for a browser connection.
func NewBrowserID() string {
	return "browser-" + uuid.NewString()[:8]
}
