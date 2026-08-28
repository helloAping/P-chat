package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log"
	"sync"
	"time"

	"github.com/p-chat/pchat/internal/tool"
)

type ServerState string

const (
	StateStopped  ServerState = "stopped"
	StateStarting ServerState = "starting"
	StateRunning  ServerState = "running"
	StateError    ServerState = "error"
)

type ServerConfig struct {
	Name    string
	Type    string // "stdio" (default) | "sse"
	Command string
	Args    []string
	Env     map[string]string
	URL     string // for SSE transport
	Enabled bool
	Timeout time.Duration
}

type ServerInfo struct {
	Name      string      `json:"name"`
	State     ServerState `json:"state"`
	ToolCount int         `json:"tool_count"`
	Error     string      `json:"error,omitempty"`
}

type managerServer struct {
	cfg    ServerConfig
	state  ServerState
	errMsg string
	tools  []string
	client *Client
	callMu sync.Mutex
	cancel context.CancelFunc
}

type toolRoute struct {
	server string
	tool   string
}

type Manager struct {
	mu         sync.RWMutex
	servers    map[string]*managerServer
	toolRoutes map[string]toolRoute
	registry   *tool.Registry
	globalOn   bool
}

func NewManager(registry *tool.Registry) *Manager {
	return &Manager{
		servers:    make(map[string]*managerServer),
		toolRoutes: make(map[string]toolRoute),
		registry:   registry,
		globalOn:   true,
	}
}

func (m *Manager) SetGlobalEnabled(on bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.globalOn = on
	if !on {
		for name, srv := range m.servers {
			// Unregister the server's tools before stopping,
			// so the LLM doesn't see dangling tool names
			// pointing at a stopped transport.
			m.unregisterTools(srv)
			m.stopLocked(name)
		}
	} else {
		for name, srv := range m.servers {
			if srv.cfg.Enabled && srv.state == StateStopped {
				m.startLocked(name)
			}
		}
	}
}

func (m *Manager) GlobalEnabled() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.globalOn
}

func (m *Manager) AddServer(cfg ServerConfig) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.servers[cfg.Name]; exists {
		return fmt.Errorf("server %q already exists", cfg.Name)
	}

	if cfg.Timeout == 0 {
		cfg.Timeout = 60 * time.Second
	}

	ms := &managerServer{
		cfg:   cfg,
		state: StateStopped,
	}
	m.servers[cfg.Name] = ms

	if m.globalOn && cfg.Enabled {
		m.startLocked(cfg.Name)
	}
	return nil
}

func (m *Manager) RemoveServer(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	srv, ok := m.servers[name]
	if !ok {
		return fmt.Errorf("server %q not found", name)
	}

	m.stopLocked(name)
	m.unregisterTools(srv)
	delete(m.servers, name)
	return nil
}

func (m *Manager) Start(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if !m.globalOn {
		return fmt.Errorf("MCP is globally disabled")
	}

	srv, ok := m.servers[name]
	if !ok {
		return fmt.Errorf("server %q not found", name)
	}
	if srv.state == StateRunning || srv.state == StateStarting {
		return nil
	}
	srv.cfg.Enabled = true
	m.startLocked(name)
	return nil
}

func (m *Manager) Stop(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	srv, ok := m.servers[name]
	if !ok {
		return fmt.Errorf("server %q not found", name)
	}
	m.stopLocked(name)
	m.unregisterTools(srv)
	srv.cfg.Enabled = false
	return nil
}

func (m *Manager) Restart(name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	srv, ok := m.servers[name]
	if !ok {
		return fmt.Errorf("server %q not found", name)
	}

	if !m.globalOn || !srv.cfg.Enabled {
		return fmt.Errorf("server %q is not enabled", name)
	}

	m.stopLocked(name)
	m.unregisterTools(srv)
	m.startLocked(name)
	return nil
}

func (m *Manager) List() []ServerInfo {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var infos []ServerInfo
	for _, srv := range m.servers {
		infos = append(infos, ServerInfo{
			Name:      srv.cfg.Name,
			State:     srv.state,
			ToolCount: len(srv.tools),
			Error:     srv.errMsg,
		})
	}
	return infos
}

func (m *Manager) GetServer(name string) (ServerConfig, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	srv, ok := m.servers[name]
	if !ok {
		return ServerConfig{}, false
	}
	return srv.cfg, true
}

func (m *Manager) CallTool(ctx context.Context, mcpToolName string, args map[string]any) (*CallToolResult, error) {
	m.mu.RLock()
	route, ok := m.toolRoutes[mcpToolName]
	if !ok {
		m.mu.RUnlock()
		return nil, fmt.Errorf("invalid MCP tool name: %s", mcpToolName)
	}
	srv, ok := m.servers[route.server]
	state := StateStopped
	timeout := 60 * time.Second
	if ok {
		state = srv.state
		timeout = srv.cfg.Timeout
	}
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("MCP server %q not found", route.server)
	}
	if state != StateRunning {
		return nil, fmt.Errorf("MCP server %q is %s", route.server, state)
	}

	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	srv.callMu.Lock()
	defer srv.callMu.Unlock()

	if srv.client == nil {
		return nil, fmt.Errorf("MCP server %q client is nil", route.server)
	}

	return srv.client.CallTool(callCtx, route.tool, args)
}

func (m *Manager) startLocked(name string) {
	srv := m.servers[name]
	if srv.state == StateRunning || srv.state == StateStarting {
		return
	}
	srv.state = StateStarting
	srv.errMsg = ""

	ctx, cancel := context.WithCancel(context.Background())
	srv.cancel = cancel

	go m.runServer(ctx, name)
}

func (m *Manager) stopLocked(name string) {
	srv := m.servers[name]
	if srv.cancel != nil {
		srv.cancel()
		srv.cancel = nil
	}
	if srv.client != nil {
		srv.callMu.Lock()
		srv.client.Close()
		srv.client = nil
		srv.callMu.Unlock()
	}
	srv.state = StateStopped
}

func (m *Manager) unregisterTools(srv *managerServer) {
	for _, tn := range srv.tools {
		if m.registry != nil {
			m.registry.Unregister(tn)
		}
		delete(m.toolRoutes, tn)
	}
	srv.tools = nil
}

func (m *Manager) runServer(ctx context.Context, name string) {
	m.mu.RLock()
	srv := m.servers[name]
	cfg := srv.cfg
	m.mu.RUnlock()

	retries := 0
	maxRetries := 5
	baseDelay := 3 * time.Second
	maxDelay := 60 * time.Second

	for {
		if retries > 0 {
			log.Printf("[mcp/%s] reconnect attempt %d/%d", name, retries, maxRetries)
		}

		err := m.connectServer(ctx, name, cfg)
		if err == nil {
			select {
			case <-ctx.Done():
				m.mu.Lock()
				if s, ok := m.servers[name]; ok {
					m.unregisterTools(s)
					if s.client != nil {
						s.callMu.Lock()
						s.client.Close()
						s.client = nil
						s.callMu.Unlock()
					}
					if s.state != StateError {
						s.state = StateStopped
					}
				}
				m.mu.Unlock()
				log.Printf("[mcp/%s] stopped", name)
			}
			return
		}

		retries++
		if retries >= maxRetries {
			m.setError(name, fmt.Sprintf("failed after %d retries: %v", maxRetries, err))
			return
		}

		// Exponential backoff: 3s, 6s, 12s, 24s, capped at 60s.
		// Previously a fixed 3s delay meant a briefly-flaky
		// server that needed ~10s to recover would hit
		// maxRetries and trip into the permanent error state.
		delay := baseDelay * (1 << (retries - 1))
		if delay > maxDelay {
			delay = maxDelay
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
	}
}

func (m *Manager) connectServer(ctx context.Context, name string, cfg ServerConfig) error {
	var transport Transport
	if cfg.Type == "sse" && cfg.URL != "" {
		transport = NewSSETransport(cfg.URL)
	} else {
		transport = NewStdioTransport(cfg.Command, cfg.Args, cfg.Env)
	}
	client := NewClient(transport)

	if err := client.Start(ctx); err != nil {
		client.Close()
		return fmt.Errorf("start process: %w", err)
	}

	initCtx, initCancel := context.WithTimeout(ctx, 30*time.Second)
	defer initCancel()

	if err := client.Initialize(initCtx); err != nil {
		client.Close()
		return fmt.Errorf("initialize: %w", err)
	}

	tools, err := client.ListTools(initCtx)
	if err != nil {
		client.Close()
		return fmt.Errorf("list tools: %w", err)
	}

	m.registerMCPTools(name, tools)

	m.mu.Lock()
	if s, ok := m.servers[name]; ok {
		s.client = client
		s.state = StateRunning
		s.errMsg = ""
	}
	m.mu.Unlock()

	log.Printf("[mcp/%s] running with %d tools", name, len(tools))
	return nil
}

func (m *Manager) setError(name string, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.servers[name]; ok {
		s.state = StateError
		s.errMsg = msg
	}
	log.Printf("[mcp/%s] error: %s", name, msg)
}

func (m *Manager) registerMCPTools(serverName string, tools []Tool) {
	var oldTools []string
	m.mu.RLock()
	if s, ok := m.servers[serverName]; ok {
		oldTools = append(oldTools, s.tools...)
	}
	m.mu.RUnlock()
	for _, old := range oldTools {
		if m.registry != nil {
			m.registry.Unregister(old)
		}
	}

	var registered []string
	routes := make(map[string]toolRoute, len(tools))
	for _, t := range tools {
		toolName := mcpToolName(serverName, t.Name)
		handler := MakeMCPHandler(m, toolName)
		params := t.InputSchema
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		if m.registry != nil {
			m.registry.Register(tool.Tool{
				Name:        toolName,
				Description: fmt.Sprintf("[MCP:%s] %s", serverName, t.Description),
				Parameters:  params,
			}, handler)
		}
		registered = append(registered, toolName)
		routes[toolName] = toolRoute{server: serverName, tool: t.Name}
	}

	m.mu.Lock()
	if s, ok := m.servers[serverName]; ok {
		for _, old := range s.tools {
			delete(m.toolRoutes, old)
		}
		s.tools = registered
		for name, route := range routes {
			m.toolRoutes[name] = route
		}
	}
	m.mu.Unlock()
}

func mcpToolName(server, tool string) string {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(server))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(tool))
	return fmt.Sprintf("mcp__%s__%s__%08x", safeToolIdent(server, 20), safeToolIdent(tool, 27), hash.Sum32())
}

func safeToolIdent(s string, max int) string {
	var b []byte
	for i := 0; i < len(s) && len(b) < max; i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z':
			b = append(b, c)
		case c >= 'A' && c <= 'Z':
			b = append(b, c)
		case c >= '0' && c <= '9':
			b = append(b, c)
		default:
			b = append(b, '_')
		}
	}
	if len(b) == 0 {
		return "x"
	}
	return string(b)
}
