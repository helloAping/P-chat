package main

import (
	"embed"
	"encoding/json"
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
	"github.com/p-chat/pchat/internal/knowledge"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/mcp"
	"github.com/p-chat/pchat/internal/memory"
	"github.com/p-chat/pchat/internal/paths"
	"github.com/p-chat/pchat/internal/sandbox"
	"github.com/p-chat/pchat/internal/search"
	"github.com/p-chat/pchat/internal/server"
	"github.com/p-chat/pchat/internal/serverproc"
	"github.com/p-chat/pchat/internal/style"
	"github.com/p-chat/pchat/internal/subagent"
	"github.com/p-chat/pchat/internal/tool"
	"github.com/p-chat/pchat/internal/upgrade"
	"github.com/p-chat/pchat/internal/version"
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

	memStore, err := memory.Open(cfg.Memory.MaxHistory)
	if err != nil {
		return fmt.Errorf("init memory: %w", err)
	}

	// Run the upgrade system: checks ~/.p-chat/version and applies
	// any pending structural migrations (SQL + files + config).
	if err := upgrade.Run(memStore.DB()); err != nil {
		return fmt.Errorf("upgrade: %w", err)
	}

	styleMgr, err := style.NewManager(memStore.DB())
	if err != nil {
		return fmt.Errorf("init style: %w", err)
	}

	toolReg := tool.NewRegistry()
	tool.RegisterBuiltin(toolReg)
	tool.RegisterWebSearch(toolReg, cfg.Search)
	// Build the search provider and install it as the
	// process-global. The tool handler reads it via
	// search.Global() on every call, so we update it here
	// AND whenever the user saves new web_search settings
	// (the UpdateConfig handler calls SetGlobal again with
	// the refreshed config).
	search.SetGlobal(search.BuildProvider(cfg.Search))
	if cfg.Search.Enabled {
		log.Printf("[search] web_search enabled, provider=%s", search.Global().Name())
	} else {
		log.Printf("[search] web_search disabled (no provider configured)")
	}
	if cfg.Knowledge.Enabled {
		tool.RegisterGrep(toolReg, cfg)
		tool.RegisterWiki(toolReg, cfg)
		// Migrate legacy wiki_sections → three-level index_nodes.
		var bases []knowledge.BaseRef
		for _, b := range cfg.Knowledge.Bases {
			bases = append(bases, knowledge.BaseRef{Name: b.Name, Path: b.Path, Enabled: b.Enabled})
		}
		knowledge.EnsureMigrated(bases)
	}

	// Build the sub-agent catalog. Three sources, in priority
	// order (last wins):
	//   1. Built-in agents: general-purpose, explore, plan
	//   2. User-defined agents in ~/.p-chat/agent/*.md
	//   3. Per-project agents in <project>/.p-chat/agent/*.md
	// (Project wins over user wins over built-in on name
	// collision — mirrors the config layering rule.)
	subagentReg := subagent.NewRegistry()
	subagentReg.RegisterAll(subagent.Builtins())
	if userAgents, err := subagent.LoadFromDir(filepath.Join(paths.GlobalDir(), "agent")); err != nil {
		log.Printf("[subagent] load user agents: %v", err)
	} else {
		subagentReg.RegisterAll(userAgents)
	}
	// Per-project agents: walk registered projects. For each
	// project, load <root>/.p-chat/agent/*.md and overlay.
	for _, proj := range loadProjectRoots(cfg) {
		if proj == "" {
			continue
		}
		dir := filepath.Join(proj, ".p-chat", "agent")
		if pa, err := subagent.LoadFromDir(dir); err != nil {
			log.Printf("[subagent] load project %s agents: %v", proj, err)
		} else if len(pa) > 0 {
			subagentReg.RegisterAll(pa)
		}
	}
	log.Printf("[subagent] registered %d agent(s): %v", len(subagentReg.List()), agentNames(subagentReg))

	// Build the sub-agent runner. The CLI registers a parallel
	// runner, but the server is the canonical path. Wiring
	// happens here so the `task` tool is live for every
	// session from the first turn.
	runner := &subagent.Default{
		Cfg:            cfg,
		LLM:            llmClient,
		StyleMgr:       styleMgr,
		ParentTools:    toolReg,
		ParentStyle:    style.Style(currentStyleName(cfg)),
		ParentProvider: defaultProviderName(cfg),
		Registry:       subagentReg,
		Cache:          subagent.NewCache(cfg.SubAgent.CacheTTLDuration()),
	}
	tt, hh := runner.Tool()
	toolReg.Register(tt, hh)
	log.Printf("[subagent] task tool registered (timeout=%s cache_ttl=%s)",
		cfg.SubAgent.TimeoutDuration(), cfg.SubAgent.CacheTTLDuration())

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
	// Expose the sub-agent catalog to the agent's tool
	// dispatcher so the `task` tool can resolve
	// subagent_type at call time. The adapter is a thin
	// shim that hides the subagent package's full AgentInfo
	// (which has UI / source fields the tool doesn't need)
	// behind the agent package's smaller SubagentInfo view.
	agt.SetSubagentRegistry(subagent.SubagentRegistryAdapter{R: subagentReg})

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

	// Auto-index knowledge bases on startup (if enabled).
	srv.Handler().AutoIndexKnowledgeBases()

	// PCHAT_PORT overrides the configured port. This is how the
	// parent process (pchat / pchat-gui) tells us which ephemeral
	// port to bind to. The host stays as configured.
	port := cfg.Server.Port
	if p := serverproc.PortFromEnv(); p > 0 {
		port = p
	}
	addr := fmt.Sprintf("%s:%d", cfg.Server.Host, port)
	fmt.Printf("P-Chat Server 启动于 http://%s\n", addr)
	log.Printf("pchat-server version=%s question-tracing=enabled", version.FullString())
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

// loadProjectRoots returns the absolute paths of every project
// registered in the user's config, or an empty slice if no
// projects are registered. Used at startup to overlay
// per-project sub-agent definitions on top of the global
// agents.
//
// The implementation is intentionally lightweight — it just
// reads ~/.p-chat/projects.json (the same file the projects API
// serves). We don't pull in the full project package because
// main.go is the bootstrap path and we want it free of optional
// failure modes.
func loadProjectRoots(cfg *config.Config) []string {
	pf := filepath.Join(paths.GlobalDir(), "projects.json")
	data, err := os.ReadFile(pf)
	if err != nil {
		return nil
	}
	// Minimal parse: we only need the `path` field of each
	// entry. The project package's full Project struct is
	// more than we need here.
	var entries []struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		log.Printf("[subagent] parse projects.json: %v", err)
		return nil
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.Path != "" {
			out = append(out, e.Path)
		}
	}
	return out
}

// agentNames returns the sorted names of all registered agents.
// Used only for the startup log line.
func agentNames(r *subagent.Registry) []string {
	list := r.List()
	out := make([]string, 0, len(list))
	for _, a := range list {
		out = append(out, a.Name)
	}
	return out
}

// currentStyleName returns the user's chosen style. Used as
// the sub-agent runner's ParentStyle fallback (when the LLM
// doesn't pass an explicit `style` arg to the `task` tool).
//
// Priority: PCHAT_STYLE env var → cfg.Style.Default → "tech"
// (built-in absolute fallback). "tech" is the most neutral of
// the three built-in styles and is the same value the config
// loader uses as the out-of-the-box default.
//
// IMPORTANT: this must return a value that resolves to a
// registered style. The earlier "default" fallback silently
// broke every sub-agent call: the runner inherited it as
// ParentStyle, the sub-agent's system prompt build then
// errored with "unknown style: default", and the conversation
// stopped mid-turn. See subagent_test.go for the regression
// test that guards against this.
func currentStyleName(cfg *config.Config) string {
	if v := os.Getenv("PCHAT_STYLE"); v != "" {
		return v
	}
	if cfg != nil && cfg.Style.Default != "" {
		return cfg.Style.Default
	}
	return "tech"
}

// defaultProviderName returns the user's chosen LLM provider
// (the `llm.default` config key, or the first configured
// provider if unset). Used as the sub-agent's fallback
// provider.
func defaultProviderName(cfg *config.Config) string {
	if cfg == nil {
		return ""
	}
	if cfg.LLM.Default != "" {
		return cfg.LLM.Default
	}
	if len(cfg.LLM.Providers) > 0 {
		return cfg.LLM.Providers[0].Name
	}
	return ""
}
