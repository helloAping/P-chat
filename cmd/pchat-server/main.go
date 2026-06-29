package main

import (
	"embed"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"
	"github.com/p-chat/pchat/internal/agent"
	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/mcp"
	"github.com/p-chat/pchat/internal/memory"
	"github.com/p-chat/pchat/internal/paths"
	"github.com/p-chat/pchat/internal/sandbox"
	"github.com/p-chat/pchat/internal/server"
	"github.com/p-chat/pchat/internal/serverproc"
	"github.com/p-chat/pchat/internal/style"
	"github.com/p-chat/pchat/internal/tool"
)

//go:embed all:web
var embeddedWebRaw embed.FS

var cfgFile string

var rootCmd = &cobra.Command{
	Use:   "pchat-server",
	Short: "P-Chat Agent Server",
	Long:  "P-Chat Agent HTTP Server — 提供对话 API 和静态资源服务",
	RunE:  runServer,
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "配置文件路径")
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func runServer(cmd *cobra.Command, args []string) error {
	// Ensure ~/.p-chat directory structure exists
	if err := paths.EnsureGlobal(); err != nil {
		return fmt.Errorf("init directories: %w", err)
	}

	// Mirror the standard log to a file under ~/.p-chat so
	// the LLM-SSE debug output is easy to find when pchat-server
	// is launched as a child process (e.g. by pchat-gui) and
	// stderr is not visible in a terminal. The first few raw
	// chunks of every stream + a final chunk-count summary
	// are written here.
	//
	// pchat-gui's stderr was previously lost on Windows when
	// the parent process did not have a console — so even with
	// a fix that correctly handles non-standard proxy field
	// names, we still need a way for the user to inspect what
	// the proxy actually sent. This log is the answer.
	if logFile, err := os.OpenFile(filepath.Join(paths.GlobalDir(), "server-debug.log"),
		os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		log.SetOutput(logFile)
	}

	cfg, err := config.Load(cfgFile)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	llmClient, err := llm.NewClient(&cfg.LLM)
	if err != nil {
		return fmt.Errorf("init LLM: %w", err)
	}

	styleMgr, err := style.NewManager("")
	if err != nil {
		return fmt.Errorf("init style: %w", err)
	}

	memStore, err := memory.Open(cfg.Memory.MaxHistory)
	if err != nil {
		return fmt.Errorf("init memory: %w", err)
	}

	toolReg := tool.NewRegistry()
	tool.RegisterBuiltin(toolReg)

	mcpMgr := mcp.NewManager(toolReg)
	mcpMgr.SetGlobalEnabled(cfg.MCP.Enabled)
	for _, srvCfg := range cfg.MCP.Servers {
		if err := mcpMgr.AddServer(configToMCP(srvCfg)); err != nil {
			log.Printf("[mcp] add server %s: %v", srvCfg.Name, err)
		}
	}

	// Register the todo persistence hook so todo lists survive
	// server restarts by writing to SQLite.
	tool.PersistTodos = func(sessionID string, todos []tool.TodoItem) {
		memTodos := make([]memory.TodoItem, len(todos))
		for i, t := range todos {
			memTodos[i] = memory.TodoItem{
				ID:      t.ID,
				Content: t.Content,
				Status:  t.Status,
			}
		}
		_ = memStore.SaveTodos(sessionID, memTodos)
	}

	agt := agent.New(cfg, llmClient, styleMgr, memStore, toolReg)

	sbx, err := sandbox.New(cfg.Sandbox)
	if err != nil {
		log.Printf("[sandbox] init error: %v (sandbox disabled)", err)
	} else {
		agt.SetSandbox(sbx)
	}

	// Static-dir resolution: by default the web frontend is served
	// from the embedded filesystem so the binary is self-contained
	// and works from any CWD (this matters for pchat-gui, which
	// launches us from the install dir where there is no web/
	// directory). When the parent process sets PCHAT_WEB_DIR we
	// honour that override so `pchat web` (which keeps web/ on
	// disk and edits it live) still works.
	//
	// //go:embed all:web stores files under the "web" prefix inside
	// the embed.FS, but Gin's StaticFS expects index.html at the
	// root of the served FS, so we re-root with fs.Sub.
	webSub, err := fs.Sub(embeddedWebRaw, "web")
	if err != nil {
		return fmt.Errorf("locate embedded web/: %w", err)
	}
	var staticFS http.FileSystem = http.FS(webSub)
	if wd := serverproc.WebDirFromEnv(); wd != "" {
		staticFS = http.Dir(wd)
	}
	srv := server.NewWithStaticFS(cfg, agt, memStore, styleMgr, staticFS, mcpMgr)

	// PCHAT_PORT overrides the configured port. This is how the
	// parent process (pchat / pchat-gui) tells us which ephemeral
	// port to bind to. The host stays as configured.
	port := cfg.Server.Port
	if p := serverproc.PortFromEnv(); p > 0 {
		port = p
	}
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, port)
	fmt.Printf("P-Chat Server 启动于 http://%s\n", addr)
	return srv.RunAt(addr)
}

func configToMCP(cfg config.MCPServerConfig) mcp.ServerConfig {
	timeout := 60 * time.Second
	if cfg.Timeout != "" {
		if d, err := time.ParseDuration(cfg.Timeout); err == nil && d > 0 {
			timeout = d
		}
	}
	tp := cfg.Type
	if tp == "" {
		tp = "stdio"
	}
	return mcp.ServerConfig{
		Name:    cfg.Name,
		Type:    tp,
		Command: cfg.Command,
		Args:    cfg.Args,
		Env:     cfg.Env,
		URL:     cfg.URL,
		Enabled: cfg.Enabled,
		Timeout: timeout,
	}
}
