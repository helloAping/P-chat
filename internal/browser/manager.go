// Manager coordinates the browser control subsystem lifecycle:
// it owns the BridgeHub and bridges it to the tool.Registry for
// dynamic tool registration.
//
// Lifecycle:
//   1. NewManager(cfg, registry) - creates the hub but doesn't start it
//   2. Start() - starts the hub goroutine, sets enabled state from config
//   3. (hub receives connections → registers/unregisters tools dynamically)
//   4. Stop() - shuts down hub and all connections
package browser

import (
	"log"
	"sync"

	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/tool"
)

// Manager is the browser-control subsystem coordinator.
type Manager struct {
	mu       sync.Mutex
	hub      *BridgeHub
	registry *tool.Registry
	enabled  bool
	started  bool
}

// NewManager creates a Manager. Call Start() to activate.
func NewManager(cfg config.BrowserConfig, registry *tool.Registry) *Manager {
	hub := NewHub()
	hub.SetEnabled(cfg.Enabled)
	return &Manager{
		hub:      hub,
		registry: registry,
		enabled:  cfg.Enabled,
	}
}

// Hub returns the underlying BridgeHub, used by the WebSocket
// handler to accept connections.
func (m *Manager) Hub() *BridgeHub { return m.hub }

// Start launches the hub's background goroutine and wires the
// onToolsChange callback. It is safe to call multiple times
// (subsequent calls are no-ops).
func (m *Manager) Start() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.started {
		return
	}
	m.started = true

	m.hub.SetOnToolsChange(func(hasConnections bool) {
		if hasConnections {
			RegisterBrowserTools(m.registry, m.hub)
			log.Println("[browser] registered browser tools (connection arrived)")
		} else {
			UnregisterBrowserTools(m.registry)
			log.Println("[browser] unregistered browser tools (all connections lost)")
		}
	})

	go m.hub.Run()
	log.Printf("[browser] hub started (enabled=%v)", m.enabled)
}

// Stop shuts down the hub, closing all connections.
func (m *Manager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.started {
		return
	}
	m.hub.Stop()
	m.started = false
	UnregisterBrowserTools(m.registry)
	log.Println("[browser] hub stopped")
}

// SetEnabled toggles the master switch. When disabled, the hub
// stops accepting new connections (enforced in the HTTP handler)
// and browser tools are unregistered.
func (m *Manager) SetEnabled(on bool) {
	m.mu.Lock()
	m.enabled = on
	m.hub.SetEnabled(on)
	m.mu.Unlock()

	if !on {
		UnregisterBrowserTools(m.registry)
		log.Println("[browser] disabled: browser tools unregistered")
	} else if m.hub.HasConnections() {
		RegisterBrowserTools(m.registry, m.hub)
		log.Println("[browser] enabled: browser tools re-registered (connections present)")
	}
}

// IsEnabled reports whether the browser control feature is active.
func (m *Manager) IsEnabled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.enabled
}
