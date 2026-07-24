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
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/google/uuid"
	"nhooyr.io/websocket"
)

// BrowserInfo is the serialisable summary exposed via the HTTP API.
type BrowserInfo struct {
	ID                 string `json:"id"`
	Name               string `json:"name"`
	ConnectedAt        string `json:"connected_at"`
	TabsCount          int    `json:"tabs_count"`
	ActiveTabID        int    `json:"active_tab_id,omitempty"`
	ActiveTabTitle     string `json:"active_tab_title,omitempty"`
	ActiveTabURL       string `json:"active_tab_url,omitempty"`
	ExtensionVersion   string `json:"extension_version,omitempty"`
	ProtocolVersion    string `json:"protocol_version,omitempty"`
	ProtocolCompatible bool   `json:"protocol_compatible"`
	UpdateRequired     bool   `json:"update_required"`
	UpdateMessage      string `json:"update_message,omitempty"`
	LastSeenAt         string `json:"last_seen_at,omitempty"`
	LastError          string `json:"last_error,omitempty"`
	LastErrorAt        string `json:"last_error_at,omitempty"`
}

// TabInfo is one browser tab reported by the extension.
type TabInfo struct {
	ID        int    `json:"id"`
	Index     int    `json:"index"`
	WindowID  int    `json:"window_id,omitempty"`
	Title     string `json:"title"`
	URL       string `json:"url"`
	Active    bool   `json:"active"`
	Preferred bool   `json:"preferred"`
}

// TabsListResult is the payload returned by browser/tabs action=list.
type TabsListResult struct {
	PreferredTabID *int      `json:"preferred_tab_id"`
	Tabs           []TabInfo `json:"tabs"`
}

// ClientSnapshot is the browser client's current diagnostic state.
type ClientSnapshot struct {
	ID               string
	Name             string
	TabsCount        int
	ActiveTabID      int
	ActiveTabTitle   string
	ActiveTabURL     string
	ExtensionVersion string
	ProtocolVersion  string
	LastSeen         time.Time
	LastError        string
	LastErrorAt      time.Time
}

// BridgeHub is the central registry of connected browser clients.
type BridgeHub struct {
	mu          sync.RWMutex
	clients     map[string]*clientEntry
	enabled     bool
	lastError   string
	lastErrorAt time.Time
	regCh       chan *BrowserClient
	unregCh     chan string // browserID → remove
	stopOnce    sync.Once
	stopCh      chan struct{}

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
				if entry.client.conn != nil {
						_ = entry.client.conn.Close(websocket.StatusNormalClosure, "unregistered")
					}
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

// RecordError stores the most recent browser-control subsystem error.
// It is surfaced in /browser/status so the settings page can explain
// why the extension did not connect.
func (h *BridgeHub) RecordError(err string) {
	if err == "" {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.lastError = err
	h.lastErrorAt = time.Now()
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
func (h *BridgeHub) SendCommand(ctx context.Context, browserID string, method string, params any, timeout time.Duration) (*Response, error) {
	c, err := h.getClient(browserID)
	if err != nil {
		return nil, err
	}
	return c.SendCommand(ctx, method, params, timeout)
}

// ListTabs asks the extension for the current tab list and refreshes
// the client's cached preferred-tab metadata.
func (h *BridgeHub) ListTabs(ctx context.Context, browserID string) (*TabsListResult, error) {
	c, err := h.getClient(browserID)
	if err != nil {
		return nil, err
	}
	resp, err := c.SendCommand(ctx, "browser/tabs", map[string]any{"action": "list"}, defaultCommandTimeout)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	var result TabsListResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		return nil, fmt.Errorf("decode tabs list: %w", err)
	}
	// Refresh cached active tab from preferred or currently-active tab.
	activeID := 0
	title, url := "", ""
	if result.PreferredTabID != nil && *result.PreferredTabID != 0 {
		activeID = *result.PreferredTabID
	}
	for _, t := range result.Tabs {
		if activeID != 0 && t.ID == activeID {
			title, url = t.Title, t.URL
			break
		}
		if activeID == 0 && t.Active {
			activeID = t.ID
			title, url = t.Title, t.URL
		}
	}
	if activeID != 0 || len(result.Tabs) > 0 {
		c.SetActiveTabMeta(activeID, title, url, len(result.Tabs))
	}
	return &result, nil
}

// SetActiveTab sets the preferred control target tab for a browser.
// Prefer tab_id; index is accepted as a fallback for older clients.
func (h *BridgeHub) SetActiveTab(ctx context.Context, browserID string, tabID int, index *int) (*TabsListResult, error) {
	c, err := h.getClient(browserID)
	if err != nil {
		return nil, err
	}
	params := map[string]any{"action": "select"}
	if tabID != 0 {
		params["tab_id"] = tabID
	} else if index != nil {
		params["index"] = *index
	} else {
		return nil, fmt.Errorf("tab_id or index is required")
	}
	resp, err := c.SendCommand(ctx, "browser/tabs", params, defaultCommandTimeout)
	if err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	// Re-list to return full tab state and refresh cache.
	return h.ListTabs(ctx, browserID)
}

// List returns information about all currently connected browsers.
func (h *BridgeHub) List() []BrowserInfo {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]BrowserInfo, 0, len(h.clients))
	for _, e := range h.clients {
		snap := e.client.Snapshot()
		info := BrowserInfo{
			ID:                 snap.ID,
			Name:               snap.Name,
			ConnectedAt:        e.connectedAt.Format(time.RFC3339),
			TabsCount:          snap.TabsCount,
			ActiveTabID:        snap.ActiveTabID,
			ActiveTabTitle:     snap.ActiveTabTitle,
			ActiveTabURL:       snap.ActiveTabURL,
			ExtensionVersion:   snap.ExtensionVersion,
			ProtocolVersion:    snap.ProtocolVersion,
			ProtocolCompatible: ProtocolCompatible(snap.ProtocolVersion),
		}
		info.UpdateRequired = !info.ProtocolCompatible
		info.UpdateMessage = UpdateMessage(snap.ProtocolVersion)
		if !snap.LastSeen.IsZero() {
			info.LastSeenAt = snap.LastSeen.Format(time.RFC3339)
		}
		if snap.LastError != "" {
			info.LastError = snap.LastError
		}
		if !snap.LastErrorAt.IsZero() {
			info.LastErrorAt = snap.LastErrorAt.Format(time.RFC3339)
		}
		out = append(out, info)
	}
	return out
}

// ProtocolCompatible reports whether a browser extension can safely use
// the server's current browser-control command protocol.
func ProtocolCompatible(version string) bool {
	return version == ProtocolVersion
}

// UpdateMessage returns a user-facing compatibility hint.
func UpdateMessage(version string) string {
	if ProtocolCompatible(version) {
		return ""
	}
	if version == "" {
		return "浏览器扩展未上报协议版本，请重新下载并安装最新扩展。"
	}
	return fmt.Sprintf("浏览器扩展协议版本 %s 与服务端期望版本 %s 不一致，请重新下载并安装最新扩展。", version, ProtocolVersion)
}

// LastError returns the most recent browser subsystem error.
func (h *BridgeHub) LastError() (string, string) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if h.lastError == "" {
		return "", ""
	}
	at := ""
	if !h.lastErrorAt.IsZero() {
		at = h.lastErrorAt.Format(time.RFC3339)
	}
	return h.lastError, at
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
		if e.client.conn != nil {
			_ = e.client.conn.Close(websocket.StatusNormalClosure, "hub shutdown")
		}
		delete(h.clients, id)
	}
	h.fireToolsChange(false)
}

// NewBrowserID generates a new unique identifier for a browser connection.
func NewBrowserID() string {
	return "browser-" + uuid.NewString()[:8]
}
