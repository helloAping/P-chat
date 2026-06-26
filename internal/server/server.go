package server

import (
	"fmt"
	"net/http"
	"os"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/agent"
	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/memory"
)

type Server struct {
	cfg     *config.Config
	agent   *agent.Agent
	store   *memory.Store
	engine  *gin.Engine
	handler *Handler
}

// Engine returns the underlying gin.Engine so tests (or embedders)
// can drive the server via httptest without exposing internals.
func (s *Server) Engine() *gin.Engine { return s.engine }

// Handler returns the request handler so tests (and embedders)
// can call helpers that aren't bound to an HTTP route — e.g.
// sessionToResponse or the per-session meta resolvers. Stable
// public API; safe to call from outside this package.
func (s *Server) Handler() *Handler { return s.handler }

// New builds the HTTP server. The store is used for session/message
// persistence. The agent is used for chat calls. The web frontend is
// served from an embedded filesystem so the binary is self-contained.
func New(cfg *config.Config, agt *agent.Agent, store *memory.Store) *Server {
	return NewWithStaticFS(cfg, agt, store, nil)
}

// NewWithStaticDir is like New but lets the caller pick where the
// static frontend lives on disk. Used by tests and by the `pchat web`
// parent process, which sets PCHAT_WEB_DIR to the absolute path of the
// web/ output. Pass an empty string to skip static serving entirely
// (useful for tests that don't ship the frontend).
func NewWithStaticDir(cfg *config.Config, agt *agent.Agent, store *memory.Store, staticDir string) *Server {
	if staticDir == "" {
		return NewWithStaticFS(cfg, agt, store, nil)
	}
	return NewWithStaticFS(cfg, agt, store, http.Dir(staticDir))
}

// NewWithStaticFS is the lowest-level constructor: it accepts an
// http.FileSystem directly. Pass nil to skip static serving. In
// production pchat-server passes http.FS(embeddedWebFS) so the
// frontend ships inside the binary and works from any CWD.
func NewWithStaticFS(cfg *config.Config, agt *agent.Agent, store *memory.Store, staticFS http.FileSystem) *Server {
	if os.Getenv("PC_HTTP_DEBUG") == "1" {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}
	r := gin.New()
	r.Use(gin.Recovery())

	h := NewHandler(agt, cfg, store)

	api := r.Group("/api/v1")
	{
		api.GET("/health", h.Health)
		api.GET("/styles", h.Styles)
		api.GET("/providers", h.Providers)
		api.GET("/providers/:name", h.GetProvider)
		api.POST("/providers", h.AddProvider)
		api.DELETE("/providers/:name", h.DeleteProvider)
		api.PATCH("/providers/:name", h.SetProviderAPIKey)
		api.POST("/providers/:name/default", h.SetDefaultProvider)
		api.POST("/providers/:name/models", h.AddModel)
		api.PUT("/providers/:name/models/:model", h.UpdateModel)
		api.DELETE("/providers/:name/models/:model", h.DeleteModel)
		api.POST("/providers/:name/models/:model/default", h.SetDefaultModel)
		api.PATCH("/providers/:name/models/:model/capabilities", h.SetCapabilities)

		// Uploads
		api.POST("/uploads", h.Upload)
		api.GET("/uploads/:id", h.GetUpload)

		// Slash commands
		api.GET("/commands", h.ListCommands)
		api.POST("/commands/:name", h.RunCommand)

		// Sessions
		api.GET("/sessions", h.ListSessions)
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
	}

	// Static files (web frontend). Both the Wails GUI and the
	// browser-based client load their assets from /app.
	if staticFS != nil {
		r.StaticFS("/app", staticFS)
	}

	return &Server{cfg: cfg, agent: agt, store: store, engine: r, handler: h}
}

func (s *Server) Run() error {
	addr := fmt.Sprintf("%s:%d", s.cfg.Server.Host, s.cfg.Server.Port)
	return s.engine.Run(addr)
}

// RunAt binds to the given listen address (e.g. "127.0.0.1:18960").
// Used by pchat-server when the parent process supplies a port via
// the PCHAT_PORT env var.
func (s *Server) RunAt(addr string) error {
	return s.engine.Run(addr)
}
