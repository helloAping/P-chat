package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/fatih/color"
	"github.com/spf13/cobra"
	"github.com/p-chat/pchat/internal/agent"
	"github.com/p-chat/pchat/internal/cli"
	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/knowledge"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/memory"
	"github.com/p-chat/pchat/internal/paths"
	"github.com/p-chat/pchat/internal/recall"
	"github.com/p-chat/pchat/internal/sandbox"
	"github.com/p-chat/pchat/internal/style"
	"github.com/p-chat/pchat/internal/subagent"
	"github.com/p-chat/pchat/internal/tool"
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

	cfg, err := config.Load(cfgFile)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Resolve style
	s := style.Style(cfg.Style.Default)
	if styleStr != "" {
		var err error
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

	// Sub-agent runner for the `task` tool. Sub-agents spawned by `task`
	// will get a tool registry that excludes `task` itself, so we can
	// safely register it on the same registry.
	cacheTTL := cfg.SubAgent.CacheTTLDuration()
	if cacheTTL == 0 {
		cacheTTL = 5 * time.Minute
	}
	subRunner := &subagent.Default{
		Cfg:            cfg,
		LLM:            llmClient,
		StyleMgr:       styleMgr,
		ParentTools:    toolReg,
		ParentStyle:    s,
		ParentProvider: prov,
		Cache:          subagent.NewCache(cacheTTL),
	}
	taskTool, taskHandler := subRunner.Tool()
	toolReg.Register(taskTool, taskHandler)

	agt := agent.New(cfg, llmClient, styleMgr, memStore, toolReg)
	agt.SetChatOptions(llm.OptionsFromConfig(cfg.LLM))

	// Build the sandbox from config and attach to the agent so all
	// tool calls go through it.
	sbx, err := sandbox.New(cfg.Sandbox)
	if err != nil {
		return fmt.Errorf("init sandbox: %w", err)
	}
	agt.SetSandbox(sbx)

	// Knowledge base + recall engine. Uses the local hash embedder by
	// default; users can switch to OpenAI in the future.
	embedder := knowledge.NewLocalHashEmbedder()
	indexer := knowledge.NewIndexer(memStore.DB(), embedder)
	retriever := knowledge.NewRetriever(memStore.DB(), embedder)
	recallEngine := recall.New(memStore, retriever, embedder)
	kbMgr := cli.NewKBManager(indexer, nil)

	// Register the recall tool on the parent's registry only. The
	// sub-agent's `Run` already excludes it from its clone.
	registerRecallTool(toolReg, recallEngine)

	repl := cli.NewREPL(cfg, agt, llmClient, styleMgr, toolReg, s, prov)
	repl.SetSubAgentCache(subRunner.Cache)
	repl.SetStore(memStore)
	repl.SetRecallEngine(recallEngine)
	repl.SetKBManager(kbMgr)
	return repl.Run()
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
