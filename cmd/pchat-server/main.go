package main

import (
	"embed"
	"fmt"
	"io/fs"
	"net/http"
	"os"

	"github.com/spf13/cobra"
	"github.com/p-chat/pchat/internal/agent"
	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/memory"
	"github.com/p-chat/pchat/internal/paths"
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

	agt := agent.New(cfg, llmClient, styleMgr, memStore, toolReg)

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
	srv := server.NewWithStaticFS(cfg, agt, memStore, staticFS)

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
