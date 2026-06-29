package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/p-chat/pchat/internal/cli"
	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/httpcli"
	"github.com/p-chat/pchat/internal/paths"
	"github.com/p-chat/pchat/internal/serverproc"
	"github.com/p-chat/pchat/internal/style"
)

var (
	cfgFile  string
	styleStr string
	provider string
)

var rootCmd = &cobra.Command{
	Use:   "pchat",
	Short: "P-Chat · 对话式 AI Agent",
	Long:  "P-Chat — 支持多风格人格的跨端对话式 AI Agent",
	RunE:  runREPL,
}

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "在当前目录初始化 .p-chat/ 项目结构",
	RunE:  runInit,
}

var skillsCmd = &cobra.Command{
	Use:   "skills",
	Short: "列出已安装的技能",
	RunE:  runSkills,
}

var rulesCmd = &cobra.Command{
	Use:   "rules",
	Short: "列出已加载的规则",
	RunE:  runRules,
}

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "显示当前合并后的配置",
	RunE:  runConfig,
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "显示版本信息",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println("P-Chat v0.1.0")
	},
}

func init() {
	rootCmd.PersistentFlags().StringVar(&cfgFile, "config", "", "配置文件路径")
	rootCmd.PersistentFlags().StringVarP(&styleStr, "style", "s", "", "默认风格: cute | guofeng | tech")
	rootCmd.PersistentFlags().StringVarP(&provider, "provider", "p", "", "LLM 提供商名)")

	rootCmd.AddCommand(initCmd)
	rootCmd.AddCommand(skillsCmd)
	rootCmd.AddCommand(rulesCmd)
	rootCmd.AddCommand(configCmd)
	rootCmd.AddCommand(webCmd)
	rootCmd.AddCommand(versionCmd)
}

func main() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func runREPL(cmd *cobra.Command, args []string) error {
	if err := paths.EnsureGlobal(); err != nil {
		return fmt.Errorf("init directories: %w", err)
	}

	// Redirect log output to a debug file so raw LLM JSON chunks
	// don't leak to the terminal during "思考中" (thinking).
	if logFile, err := os.OpenFile(filepath.Join(paths.GlobalDir(), "server-debug.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600); err == nil {
		log.SetOutput(logFile)
	}

	cfg, err := config.Load(cfgFile)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Resolve style
	s := style.Style(cfg.Style.Default)
	if styleStr != "" {
		s, err = style.ParseStyle(styleStr)
		if err != nil {
			return err
		}
	}

	// Resolve provider
	prov := cfg.LLM.Default
	if provider != "" {
		prov = provider
	}

	// Start pchat-server as a child process. The CLI no longer runs
	// the agent in-process — all chat goes through the server's SSE
	// endpoint, sharing the same code path as the GUI.
	bin, err := resolveServerBinary("")
	if err != nil {
		return fmt.Errorf("pchat-server: %w", err)
	}
	webDir := resolveWebDir()

	dim := color.New(color.FgHiBlack)
	dim.Printf("  pchat-server: %s\n", bin)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	defer signal.Stop(sigCh)

	srv, err := serverproc.Start(ctx, serverproc.Options{
		ServerBin: bin,
		Port:      0,
		ConfigPath: cfgFile,
		WebDir:    webDir,
	})
	if err != nil {
		return fmt.Errorf("start pchat-server: %w", err)
	}
	defer srv.Stop()

	// Build HTTP client and context. All chat and session operations
	// go through the server's REST API.
	httpClient := httpcli.NewClient(srv.BaseURL)
	if err := httpClient.Ping(context.Background()); err != nil {
		return fmt.Errorf("server not ready: %w", err)
	}

	// Fetch providers so the client can answer ProviderModel().
	providers, err := httpClient.ListProviders(context.Background())
	if err != nil {
		dim.Printf("  (warn: failed to list providers: %v)\n", err)
	}
	httpClient.SetCfgProviders(providers)
	httpClient.SetCurrentProvider(prov)

	// Find or create a session for the REPL.
	sessions, err := httpClient.ListSessions(context.Background())
	if err != nil {
		dim.Printf("  (warn: failed to list sessions: %v)\n", err)
	}
	var curSess string
	if len(sessions) > 0 {
		curSess = sessions[len(sessions)-1].ID
	} else {
		newSess, err := httpClient.CreateSession(context.Background(), httpcli.CreateSessionOpts{
			Style:    string(s),
			Provider: prov,
		})
		if err != nil {
			return fmt.Errorf("create session: %w", err)
		}
		curSess = newSess.ID
	}

	cliCtx := cli.NewHTTPContext(httpClient, string(s), prov, curSess)
	repl := cli.NewREPL(cliCtx, cfg, s, prov)

	// Run REPL in a goroutine; listen for SIGINT in the main goroutine
	// so we can cleanly stop the server on Ctrl+C.
	replDone := make(chan error, 1)
	go func() {
		replDone <- repl.Run()
		cancel()
	}()

	select {
	case err := <-replDone:
		if err != nil {
			dim.Printf("\n  REPL ended: %v\n", err)
		}
	case sig := <-sigCh:
		dim.Printf("\n  收到信号 %v, 正在退出...\n", sig)
		cancel()
	}

	srv.Stop()
	return nil
}

func runInit(cmd *cobra.Command, args []string) error {
	return cli.RunInit()
}

func runSkills(cmd *cobra.Command, args []string) error {
	return cli.RunSkillsList()
}

func runRules(cmd *cobra.Command, args []string) error {
	return cli.RunRulesList()
}

func runConfig(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load(cfgFile)
	if err != nil {
		return err
	}

	c := color.New(color.FgCyan, color.Bold)
	c.Println("当前配置 (合并后)")
	c.Println("─────────────────────────────────────")

	fmt.Printf("  Server:     %s:%d\n", cfg.Server.Host, cfg.Server.Port)
	fmt.Printf("  LLM 默认:   %s\n", cfg.LLM.Default)
	fmt.Printf("  风格默认:   %s\n", cfg.Style.Default)
	fmt.Printf("  记忆启用:   %v (最大 %d 条)\n", cfg.Memory.Enabled, cfg.Memory.MaxHistory)
	requireConfirm := cfg.Sandbox.RequireConfirm
	if requireConfirm == "" {
		requireConfirm = "dangerous"
	}
	fmt.Printf("  沙箱:       %v (模式: %s, 保护路径: %d, 危险模式: %d)\n",
		cfg.Sandbox.Enabled,
		requireConfirm,
		len(cfg.Sandbox.WriteProtectedPaths),
		len(cfg.Sandbox.ExecDangerousPatterns),
	)
	fmt.Println()

	fmt.Println("  LLM 提供商:")
	for _, p := range cfg.LLM.Providers {
		protocol := p.GetProtocol()
		fmt.Printf("    • %-12s  %-30s  %-10s [%s]\n", p.Name, p.BaseURL, p.Model, protocol)
	}

	return nil
}

// resolveServerBinary locates the pchat-server executable. If bin
// is non-empty it's used as the explicit path (and must exist). If
// empty, it first looks next to the current executable (the usual
// install layout) and then falls back to PATH lookup.
func resolveServerBinary(bin string) (string, error) {
	if bin != "" {
		if _, err := os.Stat(bin); err != nil {
			return "", fmt.Errorf("pchat-server not found at %s: %w", bin, err)
		}
		return bin, nil
	}
	name := "pchat-server"
	if exe, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(exe), name+exeSuffix())
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}
	if path, err := exec.LookPath(name); err == nil {
		return path, nil
	}
	return "", fmt.Errorf("pchat-server not found: pass --bin <path> or add it to PATH")
}

func exeSuffix() string {
	if os.PathSeparator == '\\' {
		return ".exe"
	}
	return ""
}
