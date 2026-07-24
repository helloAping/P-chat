package server

// mcp.go — Model Context Protocol server CRUD endpoints.
//
//   GET    /api/v1/mcp/servers
//   POST   /api/v1/mcp/servers
//   DELETE /api/v1/mcp/servers/:name
//   POST   /api/v1/mcp/servers/:name/restart
//   PATCH  /api/v1/mcp/servers/:name/global
//
// Split from handler.go in T04. Behaviour unchanged.

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/mcp"
)

func (h *Handler) ListMCPServers(c *gin.Context) {
	if h.mcpMgr == nil {
		c.JSON(http.StatusOK, gin.H{"servers": []mcp.ServerInfo{}, "global_enabled": false})
		return
	}
	servers := h.mcpMgr.List()
	c.JSON(http.StatusOK, gin.H{"servers": servers, "global_enabled": h.mcpMgr.GlobalEnabled()})
}

// AddMCPServer POST /api/v1/mcp/servers
func (h *Handler) AddMCPServer(c *gin.Context) {
	if h.mcpMgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "MCP manager not available"})
		return
	}

	var body struct {
		Name    string            `json:"name"`
		Type    string            `json:"type,omitempty"`
		Command string            `json:"command"`
		Args    []string          `json:"args"`
		Env     map[string]string `json:"env,omitempty"`
		URL     string            `json:"url,omitempty"`
		Enabled bool              `json:"enabled"`
		Timeout string            `json:"timeout,omitempty"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if body.Name == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "name is required"})
		return
	}
	if body.Type != "sse" && body.Command == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "command is required for stdio transport"})
		return
	}
	if body.Type == "sse" && body.URL == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "url is required for SSE transport"})
		return
	}
	// For stdio transports, require the command to be an
	// absolute path that points to an existing executable.
	// Without this, a typo (e.g. "pyhton" instead of "python")
	// would only fail when the user tries to use the MCP server,
	// with a confusing exec error. Catching it at config time
	// produces a clearer error.
	if body.Command != "" {
		if !filepath.IsAbs(body.Command) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "command must be an absolute path; the MCP server runs without a shell"})
			return
		}
		if info, err := os.Stat(body.Command); err != nil || info.IsDir() {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("command %q is not an executable file: %v", body.Command, err)})
			return
		}
	}
	// Reject shell-interpreter invocations that could run arbitrary
	// commands. The MCP manager execs the command verbatim; if the
	// caller wants to run a shell pipeline they should bundle it
	// into a script with its own shebang/permissions.
	if body.Command != "" {
		base := filepath.Base(strings.ToLower(body.Command))
		switch base {
		case "cmd", "cmd.exe", "sh", "bash", "zsh", "fish", "powershell", "powershell.exe", "pwsh", "csh", "tcsh", "ksh":
			c.JSON(http.StatusBadRequest, gin.H{"error": "command must be a direct executable, not a shell interpreter; bundle your script and invoke it directly"})
			return
		}
	}
	// Cap env and args to prevent resource abuse.
	if len(body.Args) > 64 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "args must be 64 or fewer entries"})
		return
	}
	if len(body.Env) > 64 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "env must be 64 or fewer entries"})
		return
	}

	var timeout time.Duration
	if body.Timeout != "" {
		if d, err := time.ParseDuration(body.Timeout); err == nil && d > 0 {
			timeout = d
		}
	}
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	tp := body.Type
	if tp == "" {
		tp = "stdio"
	}

	if err := h.mcpMgr.AddServer(mcp.ServerConfig{
		Name:    body.Name,
		Type:    tp,
		Command: body.Command,
		Args:    body.Args,
		Env:     body.Env,
		URL:     body.URL,
		Enabled: body.Enabled,
		Timeout: timeout,
	}); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	h.persistMCPServers()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// RemoveMCPServer DELETE /api/v1/mcp/servers/:name
func (h *Handler) RemoveMCPServer(c *gin.Context) {
	if h.mcpMgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "MCP manager not available"})
		return
	}
	name := c.Param("name")
	if err := h.mcpMgr.RemoveServer(name); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	h.persistMCPServers()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// RestartMCPServer POST /api/v1/mcp/servers/:name/restart
func (h *Handler) RestartMCPServer(c *gin.Context) {
	if h.mcpMgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "MCP manager not available"})
		return
	}
	name := c.Param("name")
	if err := h.mcpMgr.Restart(name); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

// SetMCPGlobal PATCH /api/v1/mcp/global
func (h *Handler) SetMCPGlobal(c *gin.Context) {
	if h.mcpMgr == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "MCP manager not available"})
		return
	}
	var body struct {
		Enabled bool `json:"enabled"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	h.mcpMgr.SetGlobalEnabled(body.Enabled)
	h.persistMCPServers()
	c.JSON(http.StatusOK, gin.H{"global_enabled": h.mcpMgr.GlobalEnabled()})
}

func (h *Handler) persistMCPServers() {
	srvInfos := h.mcpMgr.List()
	servers := make([]config.MCPServerConfig, 0, len(srvInfos))
	for _, info := range srvInfos {
		srv, ok := h.mcpMgr.GetServer(info.Name)
		if !ok {
			continue
		}
		timeoutStr := ""
		if srv.Timeout > 0 {
			timeoutStr = srv.Timeout.String()
		}
		servers = append(servers, config.MCPServerConfig{
			Name:    srv.Name,
			Type:    srv.Type,
			Command: srv.Command,
			Args:    srv.Args,
			Env:     srv.Env,
			URL:     srv.URL,
			Enabled: srv.Enabled,
			Timeout: timeoutStr,
		})
	}
	h.getCfg().MCP.Servers = servers
	h.getCfg().MCP.Enabled = h.mcpMgr.GlobalEnabled()
	if mgr := config.NewManager(); mgr != nil {
		if err := mgr.SaveGlobal(h.getCfg()); err != nil {
			log.Printf("[mcp] persist config: %v", err)
		}
	}
}
