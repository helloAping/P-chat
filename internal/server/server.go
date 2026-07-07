package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/agent"
	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/mcp"
	"github.com/p-chat/pchat/internal/memory"
	"github.com/p-chat/pchat/internal/rules"
	"github.com/p-chat/pchat/internal/style"
)

type Server struct {
	cfg      *config.Config
	agent    *agent.Agent
	store    *memory.Store
	styleMgr *style.Manager
	engine   *gin.Engine
	handler  *Handler
	srv      *http.Server

	// rulesWatchStop stops the file watcher that polls the
	// rules directories for changes. Started in
	// NewWithStaticFS, stopped in Shutdown.
	rulesWatchStop func()
}

// Engine returns the underlying gin.Engine so tests (or embedders)
// can drive the server via httptest without exposing internals.
func (s *Server) Engine() *gin.Engine { return s.engine }

// Handler returns the request handler so tests (and embedders)
// can call helpers that aren't bound to an HTTP route 鈥?e.g.
// sessionToResponse or the per-session meta resolvers. Stable
// public API; safe to call from outside this package.
func (s *Server) Handler() *Handler { return s.handler }

// New builds the HTTP server. The store is used for session/message
// persistence. The agent is used for chat calls. The web frontend is
// served from an embedded filesystem so the binary is self-contained.
func New(cfg *config.Config, agt *agent.Agent, store *memory.Store, styleMgr *style.Manager, mcpMgr *mcp.Manager) *Server {
	return NewWithStaticFS(cfg, agt, store, styleMgr, nil, mcpMgr)
}

// NewWithStaticDir is like New but lets the caller pick where the
// static frontend lives on disk. Used by tests and by the `pchat web`
// parent process, which sets PCHAT_WEB_DIR to the absolute path of the
// web/ output. Pass an empty string to skip static serving entirely
// (useful for tests that don't ship the frontend).
func NewWithStaticDir(cfg *config.Config, agt *agent.Agent, store *memory.Store, styleMgr *style.Manager, staticDir string, mcpMgr *mcp.Manager) *Server {
	if staticDir == "" {
		return NewWithStaticFS(cfg, agt, store, styleMgr, nil, mcpMgr)
	}
	return NewWithStaticFS(cfg, agt, store, styleMgr, http.Dir(staticDir), mcpMgr)
}

// NewWithStaticFS is the lowest-level constructor: it accepts an
// http.FileSystem directly. Pass nil to skip static serving. In
// production pchat-server passes http.FS(embeddedWebFS) so the
// frontend ships inside the binary and works from any CWD.
func NewWithStaticFS(cfg *config.Config, agt *agent.Agent, store *memory.Store, styleMgr *style.Manager, staticFS http.FileSystem, mcpMgr *mcp.Manager) *Server {
	if os.Getenv("PC_HTTP_DEBUG") == "1" {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	r.Use(gin.Recovery())
	// Cap ALL request bodies at 25 MiB so a malicious client
	// cannot OOM the server by posting a multi-GB JSON. The
	// upload endpoint has its own larger cap (it streams files)
	// and re-applies MaxBytesReader before this middleware sees
	// the route. Handlers that need a smaller cap (e.g.
	// SendMessage) layer their own MaxBytesReader on top.
	r.Use(maxBodyMiddleware(25 << 20))

	// CORS: pchat-server is normally hit same-origin (browser at
	// http://127.0.0.1:PORT, server at the same URL). The Wails
	// desktop app is the exception: its WebView2 lives on
	// http://wails.localhost and proxies through the AssetServer.
	// For SSE endpoints that can't be streamed through the
	// AssetServer's buffered response writer, the webview opens
	// a direct connection to the backend. The middleware below
	// permits that one cross-origin case without weakening the
	// default same-origin policy.
	r.Use(corsMiddleware())

	h := NewHandler(agt, cfg, store, styleMgr, mcpMgr)

	// Wire the summarizer so /compress and auto-compact work.
	if lc := agt.LLM(); lc != nil && cfg.LLM.Default != "" {
		sm := memory.NewSummarizer(store, lc, cfg.LLM.Default, 50)
		h.SetSummarizer(sm)
		agt.SetSummarizer(sm)
	}

	api := r.Group("/api/v1")
	{
		api.GET("/health", h.Health)
		api.GET("/version", h.VersionHandler)
		api.GET("/migrations", h.MigrationStatus)
		api.POST("/migrations/rollback", h.MigrationRollback)
		api.GET("/styles", h.Styles)
		api.POST("/styles", h.CreateStyle)
		api.GET("/styles/:id", h.GetStyle)
		api.PATCH("/styles/:id", h.UpdateStyle)
		api.DELETE("/styles/:id", h.DeleteStyle)
		api.GET("/providers", h.Providers)
		api.GET("/providers/:name", h.GetProvider)
		api.POST("/providers", h.AddProvider)
		api.DELETE("/providers/:name", h.DeleteProvider)
		api.PATCH("/providers/:name", h.UpdateProvider)
		api.POST("/providers/:name/default", h.SetDefaultProvider)
		api.POST("/providers/:name/models", h.AddModel)
		api.PUT("/providers/:name/models/:model", h.UpdateModel)
		api.DELETE("/providers/:name/models/:model", h.DeleteModel)
		api.POST("/providers/:name/models/:model/default", h.SetDefaultModel)
		api.PATCH("/providers/:name/models/:model/capabilities", h.SetCapabilities)
		api.GET("/providers/:name/upstream-models", h.FetchUpstreamModels)

		// System config
		api.GET("/config", h.GetSystemConfig)
		api.PATCH("/config", h.UpdateSystemConfig)

		// Uploads
		api.POST("/uploads", h.Upload)
		api.GET("/uploads/:id", h.GetUpload)

		// Slash commands
		api.GET("/commands", h.ListCommands)
		api.POST("/commands/:name", h.RunCommand)

		// Sessions
		api.GET("/sessions", h.ListSessions)
		api.GET("/search", h.SearchMessages)
		api.GET("/token-stats", h.TokenStats)
		api.POST("/sessions", h.CreateSession)
		api.GET("/sessions/:id", h.GetSession)
		// PATCH /sessions/:id handles both "rename only" (legacy
		// body shape: {"title": "..."}) and "update per-session
		// provider/model/style" (pointer fields). The handler
		// dispatches based on the body.
		api.PATCH("/sessions/:id", h.UpdateSessionMeta)
		api.DELETE("/sessions/:id", h.DeleteSession)

		// Messages
		api.GET("/sessions/:id/messages", h.ListMessages)
		api.POST("/sessions/:id/messages", h.SendMessage)
		api.POST("/sessions/:id/compress", h.CompressConversation)
		api.PATCH("/sessions/:id/reasoning-effort", h.SetReasoningEffort)
		api.POST("/sessions/:id/system-message", h.SaveSystemMessage)
		api.GET("/sessions/:id/todos", h.GetTodos)
		api.POST("/sessions/:id/question-response", h.QuestionResponse)
		api.POST("/sessions/:id/confirm-response", h.ConfirmResponse)
		api.POST("/sessions/:id/execute-plan", h.ExecutePlan)

		// Archive
		api.POST("/sessions/:id/archive", h.ArchiveSession)
		api.POST("/sessions/:id/unarchive", h.UnarchiveSession)
		api.GET("/sessions/archived", h.ListArchived)
		api.DELETE("/sessions/:id/permanent", h.PermanentDeleteSession)
		api.DELETE("/sessions/:id/messages", h.ClearSessionMessages)
		api.POST("/sessions/:id/rollback", h.RollbackMessages)
		api.POST("/sessions/:id/rollback/undo", h.UndoRollback)
		api.POST("/sessions/:id/fork", h.ForkSession)

		// Projects
		api.GET("/projects", h.ListProjects)
		api.POST("/projects", h.AddProject)
		api.DELETE("/projects", h.RemoveProject)

		// Dialog
		api.POST("/dialog/folder", h.PickFolder)

		// Skills
		api.GET("/skills", h.ListSkills)
		api.GET("/skills/:name", h.GetSkill)
		api.POST("/skills/install", h.InstallSkill)
		api.DELETE("/skills/:name", h.DeleteSkill)
		api.GET("/skills/search", h.SearchSkills)
		api.GET("/skills/repos", h.ListSkillRepos)
		api.POST("/skills/repos", h.AddSkillRepo)
		api.DELETE("/skills/repos", h.RemoveSkillRepo)

		// MCP
		api.GET("/mcp/servers", h.ListMCPServers)
		api.POST("/mcp/servers", h.AddMCPServer)
		api.DELETE("/mcp/servers/:name", h.RemoveMCPServer)
		api.POST("/mcp/servers/:name/restart", h.RestartMCPServer)
		api.PATCH("/mcp/global", h.SetMCPGlobal)

		// Knowledge
		api.GET("/knowledge/config", h.GetKnowledgeConfig)
		api.PATCH("/knowledge/config", h.UpdateKnowledgeConfig)
		api.GET("/knowledge/models", h.GetKnowledgeModels)
		api.GET("/knowledge/bases", h.ListKnowledgeBases)
		api.POST("/knowledge/bases", h.AddKnowledgeBase)
		api.DELETE("/knowledge/bases/:name", h.RemoveKnowledgeBase)
		api.POST("/knowledge/bases/:name/scan", h.ScanKnowledgeBase)
		api.DELETE("/knowledge/bases/:name/scan", h.CancelScan)
		api.DELETE("/knowledge/bases/:name/clear", h.ClearKnowledgeBase)
		api.GET("/knowledge/bases/:name/scan/status", h.ScanStatus)
		api.GET("/knowledge/bases/:name/nodes", h.ListNodes)
		api.GET("/knowledge/bases/:name/nodes/:id/content", h.GetNodeContent)
		api.DELETE("/knowledge/bases/:name/nodes/:id", h.DeleteNode)
		api.POST("/knowledge/search", h.SearchKnowledge)
	}

	// Static files (web frontend). Both the Wails GUI and the
	// browser-based client load their assets from /app.
	if staticFS != nil {
		r.StaticFS("/app", staticFS)
	}

	s := &Server{cfg: cfg, agent: agt, store: store, styleMgr: styleMgr, engine: r, handler: h}
	// Hot-reload rules when the user edits files in the rules
	// directories. The agent's Reload() re-reads AGENTS.md,
	// rules, and skills and rebuilds the cached system prompt.
	// Polling (every 5s) is used instead of fsnotify because it
	// works uniformly across Windows/macOS/Linux without
	// additional dependencies.
	s.rulesWatchStop = rules.Watch(func() {
		if agt != nil {
			agt.Reload()
		}
	}, 5*time.Second)
	return s
}

func (s *Server) Run() error {
	addr := fmt.Sprintf("%s:%d", s.cfg.Server.Host, s.cfg.Server.Port)
	return s.RunAt(addr)
}

func (s *Server) RunAt(addr string) error {
	s.srv = &http.Server{
		Addr:    addr,
		Handler: s.engine,
	}
	return s.srv.ListenAndServe()
}

// RunWithGracefulShutdown starts the server and blocks until a
// shutdown signal (SIGINT/SIGTERM) is received. On signal, it
// drains active connections for up to 30s, then exits.
func (s *Server) RunWithGracefulShutdown(addr string) error {
	s.srv = &http.Server{
		Addr:    addr,
		Handler: s.engine,
	}

	idleConnsClosed := make(chan struct{})
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		log.Println("[server] shutdown signal received, draining connections...")
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.Shutdown(ctx); err != nil {
			log.Printf("[server] shutdown error: %v", err)
		}
		close(idleConnsClosed)
	}()

	if err := s.srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	<-idleConnsClosed
	log.Println("[server] graceful shutdown complete")
	return nil
}

// Shutdown gracefully shuts down the server, draining active
// connections. The context deadline caps the total drain time.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.rulesWatchStop != nil {
		s.rulesWatchStop()
	}
	if s.srv != nil {
		return s.srv.Shutdown(ctx)
	}
	return nil
}

// corsMiddleware adds the headers Chromium/WebView2 needs to allow
// the Wails webview (origin http://wails.localhost) to call the
// backend at http://127.0.0.1:<port> directly. The webview needs
// this bypass for the streaming /api/v1/sessions/:id/messages SSE
// endpoint because the Wails AssetServer's response writer buffers
// the entire body and only sends it when the request handler
// returns 鈥?useless for an SSE stream that parks for minutes
// waiting on the user (the `question` tool flow).
//
// Same-origin browser clients (origin == backend address) are
// unaffected: the wildcard "*" origin header is the spec-compliant
// response for "no credentialed cross-origin", and we don't send
// Access-Control-Allow-Credentials. The 127.0.0.1 listen address
// already prevents WAN access, so a permissive CORS policy is safe
// in practice.
//
// We also handle Chromium's Private Network Access (PNA) check.
// The Wails origin is treated as a "public" origin by Chromium,
// and 127.0.0.1 is a private network, so the browser sends a
// preflight with `Access-Control-Request-Private-Network: true`
// and refuses to make the request unless we echo back
// `Access-Control-Allow-Private-Network: true`. Without this,
// `fetch()` from the Wails webview to the child server just
// times out.
func corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		// Same-origin requests send Origin too; reflect it back so
		// credentialed clients stay on a strict allow-list. For
		// cross-origin (wails.localhost) we echo the origin as well,
		// which is required for `fetch(..., { credentials: 'omit' })`.
		if origin != "" {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Vary", "Origin")
		} else {
			c.Header("Access-Control-Allow-Origin", "*")
		}
		c.Header("Access-Control-Allow-Methods", "GET, POST, PATCH, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With")
		c.Header("Access-Control-Max-Age", "600")
		// Private Network Access (Chromium 94+). The Wails origin
		// is a "public" origin and 127.0.0.1 is private; without
		// echoing the request flag, the browser blocks the request
		// outright. See https://developer.chrome.com/docs/privacy-security/private-network-access
		if c.GetHeader("Access-Control-Request-Private-Network") == "true" {
			c.Header("Access-Control-Allow-Private-Network", "true")
		}
		// Preflight: short-circuit and let the request through.
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}
		c.Next()
	}
}

// maxBodyMiddleware caps the request body size for every
// request. Handlers that need a smaller cap (e.g. SendMessage)
// layer their own MaxBytesReader on top — Go's http package
// respects nested MaxBytesReader and will return the smallest
// limit.
func maxBodyMiddleware(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request != nil && c.Request.Body != nil {
			c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		}
		c.Next()
	}
}
