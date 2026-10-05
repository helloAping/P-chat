package server

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/p-chat/pchat/internal/agents"
	"github.com/p-chat/pchat/internal/rules"
)

// commandSpec 描述 HTTP 命令面板公开的 CLI 命令子集。
// commandSpec describes the CLI command subset exposed by the HTTP palette.
type commandSpec struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Args        string `json:"args,omitempty"`
	Group       string `json:"group"` // "session" | "config" | "info" | "danger"
	// WebSafe is false for commands that need a REPL (e.g. /plan,
	// /export). The web UI hides those, but the endpoint still
	// answers them so power users can curl.
	WebSafe bool `json:"web_safe"`
}

// commandRegistry 是 HTTP 命令面板的目录；CLI 新增命令时应检查是否需要公开。
// commandRegistry is the HTTP palette catalog; review new CLI commands for inclusion.
//
// Each entry is a tiny self-contained spec —the actual command
// logic still lives in internal/cli (run by the CLI) and isn't
// invoked over HTTP for read-only or write commands that go
// through dedicated REST endpoints (e.g. /style is also
// POST /api/v1/commands/style). The endpoint here is a thin
// dispatcher that returns a text result for the "info" group
// (skills/rules/agents/tools/history/etc.) and acknowledged status
// for the others.
var commandRegistry = []commandSpec{
	{Name: "help", Description: "显示所有命令的帮助", Group: "info", WebSafe: true},
	{Name: "style", Description: "切换/查看人格风格 (cute|guofeng|tech)", Args: "[name]", Group: "config", WebSafe: true},
	{Name: "model", Description: "切换/查看模型", Args: "[provider|provider/model|index]", Group: "config", WebSafe: true},
	{Name: "provider", Description: "显示当前提供商信息", Group: "info", WebSafe: true},
	{Name: "setup", Description: "交互式提供商配置 (TUI)", Group: "config", WebSafe: false},
	{Name: "config", Description: "显示当前合并后的配置", Args: "[add|remove|key|test|list|model ...]", Group: "config", WebSafe: true},
	{Name: "history", Description: "会话历史 (list|switch|rename|forget)", Args: "[subcommand]", Group: "session", WebSafe: true},
	{Name: "new", Description: "开启新对话", Group: "session", WebSafe: true},
	{Name: "clear", Description: "清空当前对话", Group: "session", WebSafe: true},
	{Name: "export", Description: "导出当前会话到文件", Args: "<path>", Group: "session", WebSafe: false},
	{Name: "unsafe", Description: "跳过沙箱检查(once|on|off)", Args: "<mode>", Group: "danger", WebSafe: false},
	{Name: "skills", Description: "列出已加载技能", Group: "info", WebSafe: true},
	{Name: "rules", Description: "列出已加载规则", Group: "info", WebSafe: true},
	{Name: "agents", Description: "查看 AGENTS.md 内容", Group: "info", WebSafe: true},
	{Name: "tools", Description: "列出可用工具 + on/off", Args: "[on|off]", Group: "config", WebSafe: true},
	{Name: "init", Description: "在当前目录初始化 .p-chat/", Group: "session", WebSafe: false},
	{Name: "recall", Description: "知识库语义检索", Args: "<query>", Group: "info", WebSafe: true},
	{Name: "kb", Description: "知识库管理(list|add|remove|scan|status)", Args: "[subcommand]", Group: "info", WebSafe: true},
	{Name: "debug", Description: "诊断信息 (cache|memory)", Args: "[topic]", Group: "info", WebSafe: true},
	{Name: "expand", Description: "查看工具调用历史结果", Args: "[index|last]", Group: "info", WebSafe: true},
	{Name: "plan", Description: "让LLM 制定计划后再执行", Args: "<task>", Group: "session", WebSafe: false},
	{Name: "permission", Description: "设置权限级别 (ask|auto|full)", Args: "<level>", Group: "config", WebSafe: true},
	{Name: "quit", Description: "退出REPL", Group: "danger", WebSafe: false},
}

// ListCommands GET /api/v1/commands
//
// Returns the slash-command catalog so the web UI can render a
// "Commands" palette without hard-coding command names. Filtered
// to web-safe commands by default; pass ?all=1 to include the
// REPL-only ones (for power-user use).
func (h *Handler) ListCommands(c *gin.Context) {
	all := c.Query("all") == "1"
	out := make([]commandSpec, 0, len(commandRegistry))
	for _, cmd := range commandRegistry {
		if !all && !cmd.WebSafe {
			continue
		}
		out = append(out, cmd)
	}
	// Stable order for the UI: by group, then alphabetical.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return out[i].Name < out[j].Name
	})
	c.JSON(http.StatusOK, gin.H{"commands": out})
}

// ListSessionRules 返回现有会话项目范围内加载的规则。
// ListSessionRules returns the rules loaded for one existing session. The
// session id anchors project resolution and prevents arbitrary file paths from
// being supplied through this diagnostic endpoint.
func (h *Handler) ListSessionRules(c *gin.Context) {
	projectRoot, ok := h.observationProjectRoot(c, c.Param("id"))
	if !ok {
		return
	}
	loaded, err := rules.LoadAllWithRoot(projectRoot)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	result := make([]RuleObservation, 0, len(loaded))
	for _, rule := range loaded {
		scope := "project"
		if rule.IsGlobal {
			scope = "global"
		}
		result = append(result, RuleObservation{Name: rule.Name, Path: rule.Path, Scope: scope})
	}
	c.JSON(http.StatusOK, gin.H{"rules": result})
}

// GetSessionAgents 返回现有会话选用的 AGENTS.md 候选文件与实际提示词。
// GetSessionAgents returns the AGENTS.md candidates and exact prompt selected
// by the loader's OR policy for one existing session.
func (h *Handler) GetSessionAgents(c *gin.Context) {
	projectRoot, ok := h.observationProjectRoot(c, c.Param("id"))
	if !ok {
		return
	}
	inspection := agents.InspectWithRoot(projectRoot)
	result := AgentsObservation{
		Candidates: make([]AgentsCandidateObservation, 0, len(inspection.Candidates)),
		Prompt:     inspection.Prompt,
	}
	for _, candidate := range inspection.Candidates {
		result.Candidates = append(result.Candidates, AgentsCandidateObservation{
			Slot: candidate.Slot, Label: candidate.Label, Path: candidate.Path,
			Exists: candidate.Exists, Selected: candidate.Selected,
		})
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) observationProjectRoot(c *gin.Context, sessionID string) (string, bool) {
	if h.store == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "memory store not available"})
		return "", false
	}
	if sessionID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "session_id is required"})
		return "", false
	}
	if _, err := h.store.GetConversation(sessionID); err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "session not found"})
		return "", false
	}
	return h.sessionProjectPath(sessionID), true
}

// RunCommandRequest is the body of POST /api/v1/commands/:name.
type RunCommandRequest struct {
	Args      string `json:"args"`
	SessionID string `json:"session_id,omitempty"`
}

// CommandResult is the response of POST /api/v1/commands/:name.
// Most commands produce just a text snippet; the structured fields
// (commands, providers, ...) are populated when the command
// returns richer data.
type CommandResult struct {
	Output string `json:"output"`
}

// RuleObservation 是会话项目范围内可见的一条规则。
// RuleObservation is one rule visible to a session's project scope.
type RuleObservation struct {
	Name  string `json:"name"`
	Path  string `json:"path"`
	Scope string `json:"scope"`
}

// AgentsCandidateObservation 描述一个 AGENTS.md 候选文件，不返回其原始内容。
// AgentsCandidateObservation describes one AGENTS.md candidate without
// returning its raw content. Only the selected prompt body is returned.
type AgentsCandidateObservation struct {
	Slot     string `json:"slot"`
	Label    string `json:"label"`
	Path     string `json:"path"`
	Exists   bool   `json:"exists"`
	Selected bool   `json:"selected"`
}

// AgentsObservation 是会话范围内 AGENTS.md 诊断响应。
// AgentsObservation is the session-scoped AGENTS.md diagnostic response.
type AgentsObservation struct {
	Candidates []AgentsCandidateObservation `json:"candidates"`
	Prompt     string                       `json:"prompt"`
}

// RunCommand POST /api/v1/commands/:name
//
// This is a thin dispatcher —it doesn't re-implement the CLI's
// command logic. Instead, it recognizes a small set of read-only
// commands that the web UI cares about and returns canned output
// (the "info" group in the catalog). State-changing commands
// (style, model, new, clear, ...) are exposed for completeness
// but the web UI is expected to use the dedicated REST endpoints
// instead —they validate, persist, and reload the LLM client.
//
// We deliberately do not invoke the CLI's command functions
// directly: those return errors and print to stdout. Calling them
// here would require either refactoring every handler to return
// a string (a 2000-line change) or capturing stdout (racey,
// lossy). A thin REST surface here is the smaller change.
func (h *Handler) RunCommand(c *gin.Context) {
	name := strings.TrimPrefix(c.Param("name"), "/")
	var req RunCommandRequest
	_ = c.ShouldBindJSON(&req) // body optional

	spec, ok := findCommand(name)
	if !ok {
		c.JSON(http.StatusNotFound, gin.H{"error": "unknown command: " + name})
		return
	}

	// Whitelist the read-only / informational commands that the
	// web palette invokes directly. Anything else returns a
	// "use the dedicated endpoint" hint.
	switch spec.Group {
	case "info":
		projectRoot := ""
		if name == "rules" || name == "agents" {
			var ok bool
			projectRoot, ok = h.observationProjectRoot(c, req.SessionID)
			if !ok {
				return
			}
		}
		out, err := h.runInfoCommand(name, req.Args, projectRoot)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, CommandResult{Output: out})
	case "config":
		// /style, /model, /provider, /tools, /config, /permission —these have
		// dedicated REST endpoints that the UI should call. We
		// accept the command but point the client at the proper
		// endpoint.
		c.JSON(http.StatusOK, CommandResult{
			Output: fmt.Sprintf("命令 /%s 应通过专用端点执行（参见 GET /api/v1/commands 的描述）", name),
		})
	case "session":
		c.JSON(http.StatusOK, CommandResult{
			Output: fmt.Sprintf("命令 /%s 应通过专用端点执行", name),
		})
	default:
		// "danger" —these are REPL-only (touch the chat loop).
		c.JSON(http.StatusForbidden, CommandResult{
			Output: fmt.Sprintf("命令 /%s 不能在Web UI 执行", name),
		})
	}
}

// runInfoCommand produces a text rendering of the "info" group
// commands. The output mirrors what the CLI prints (sometimes
// paraphrased for HTML friendliness).
func (h *Handler) runInfoCommand(name, args, projectRoot string) (string, error) {
	switch name {
	case "help":
		var b strings.Builder
		b.WriteString("可用命令:\n")
		for _, cmd := range commandRegistry {
			if !cmd.WebSafe {
				continue
			}
			line := fmt.Sprintf("  /%-10s %s", cmd.Name, cmd.Description)
			if cmd.Args != "" {
				line += "  (参数: " + cmd.Args + ")"
			}
			b.WriteString(line + "\n")
		}
		return b.String(), nil
	case "provider":
		// /provider shows the active provider's URL/key.
		// We don't expose the API key in plain text —mask it.
		if h.getCfg() == nil {
			return "(config not loaded)", nil
		}
		active := h.getCfg().LLM.Default
		for _, p := range h.getCfg().LLM.Providers {
			if p.Name == active {
				key := "(已设置)"
				if p.APIKey == "" {
					key = "(未设置)"
				}
				return fmt.Sprintf("当前提供商: %s\n  协议: %s\n  BaseURL: %s\n  Model: %s\n  APIKey: %s",
					p.Name, p.GetProtocol(), p.BaseURL, p.EffectiveModel(), key), nil
			}
		}
		return fmt.Sprintf("(active provider %q not in config)", active), nil
	case "skills":
		// Lazy import to avoid a heavy package dep at handler load.
		return infoList("skills", func() ([]string, error) {
			return h.loadSkillsList()
		})
	case "rules":
		loaded, err := rules.LoadAllWithRoot(projectRoot)
		if err != nil {
			return "", err
		}
		if len(loaded) == 0 {
			return "暂无已加载规则", nil
		}
		var b strings.Builder
		fmt.Fprintf(&b, "已加载规则 (%d):\n", len(loaded))
		for _, rule := range loaded {
			scope := "项目"
			if rule.IsGlobal {
				scope = "全局"
			}
			fmt.Fprintf(&b, "  • %s [%s]\n    %s\n", rule.Name, scope, rule.Path)
		}
		return b.String(), nil
	case "agents":
		inspection := agents.InspectWithRoot(projectRoot)
		var b strings.Builder
		b.WriteString("AGENTS.md 候选文件（按优先级）:\n")
		for _, candidate := range inspection.Candidates {
			status := "未找到"
			if candidate.Exists {
				status = "可用，未选中"
			}
			if candidate.Selected {
				status = "胜出"
			}
			fmt.Fprintf(&b, "  • [%s] %s — %s\n", candidate.Label, status, candidate.Path)
		}
		b.WriteString("\n实际注入提示词:\n")
		b.WriteString(inspection.Prompt)
		return b.String(), nil
	case "tools":
		// The CLI prints the tool list from the agent's
		// registry. We don't have a direct accessor here;
		// return a minimal stub.
		return "(use the HTTP /api/v1/agents endpoint or web UI to inspect tools)", nil
	default:
		// Defensive: command name was in the registry but the
		// case above didn't list it. Don't crash.
		return "(no info available for /" + name + " over HTTP)", nil
	}
}

func findCommand(name string) (commandSpec, bool) {
	for _, cmd := range commandRegistry {
		if cmd.Name == name {
			return cmd, true
		}
	}
	return commandSpec{}, false
}

// loadSkillsList reads the same skill catalog as the CLI without capturing
// the CLI command's stdout.

func (h *Handler) loadSkillsList() ([]string, error) {
	all, err := loadAllSkills()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(all))
	for _, s := range all {
		out = append(out, s.Name)
	}
	return out, nil
}

// infoList is a tiny helper for "list N items" commands. Each
// item is rendered as a bullet.
func infoList(label string, load func() ([]string, error)) (string, error) {
	items, err := load()
	if err != nil {
		return "", err
	}
	if len(items) == 0 {
		return fmt.Sprintf("(暂无 %s)", label), nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%d):\n", label, len(items))
	for _, it := range items {
		fmt.Fprintf(&b, "  —%s\n", it)
	}
	return b.String(), nil
}
