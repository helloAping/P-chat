package cli

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/p-chat/pchat/internal/agent"
	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/httpcli"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/sandbox"
	"github.com/p-chat/pchat/internal/style"
)

// errQuit is a sentinel error returned by /quit. The REPL loop
// treats it specially: it stops accepting input but lets the
// function return normally so deferred cleanup (e.g. SQLite flush)
// runs.
var errQuit = fmt.Errorf("quit")

type SlashCommand struct {
	Name        string
	Aliases     []string
	Description string
	// Usage, Args, Examples provide the long-form help shown by
	// `/help <cmd>`. All are optional; a command with no Usage line
	// is treated as "no detailed help available".
	Usage    string
	Args     string
	Examples []string
	Handler  func(ctx cliContext, args string) error
}

var slashCmds []SlashCommand

func init() {
	slashCmds = []SlashCommand{
		{
			Name: "/help", Aliases: []string{"/h", "/?"}, Description: "显示帮助信息",
			Usage: "/help [命令名]",
			Args: "[命令名]: 可选。要查看详细帮助的斜杠命令, 例如 /help model。\n" +
				"      不带参数时列出所有命令的一行摘要。",
			Examples: []string{
				"/help",
				"/help model",
				"/help /expand",
				"/help /config",
			},
			Handler: cmdHelp,
		},
		{
			Name: "/style", Aliases: []string{"/s"}, Description: "切换说话风格 [off|cute|guofeng|tech]",
			Usage: "/style [off|cute|guofeng|tech]",
			Args: "off      - 关闭风格，不注入人格提示词和风格记忆\n" +
				"      cute     - 可爱风 (小P)\n" +
				"      guofeng  - 古风 (墨言, 📜)\n" +
				"      tech     - 科技风 (NEXUS, ⚡)\n" +
				"      不带参数 - 显示当前风格 + 列表 + 方向键选",
			Examples: []string{
				"/style",
				"/style cute",
				"/style tech",
			},
			Handler: cmdStyle,
		},
		{
			Name: "/mode", Description: "切换工作侧重点 [coding|daily]",
			Usage: "/mode [coding|daily]",
			Args: "coding   - 编码侧重点：读写代码、调试、测试、命令、git、review\n" +
				"      daily    - 工作侧重点：文档、邮件、摘要、计划、知识检索、信息整理\n" +
				"      不带参数 - 显示当前模式与可用模式",
			Examples: []string{
				"/mode",
				"/mode daily",
				"/mode coding",
			},
			Handler: cmdMode,
		},
		{
			Name: "/model", Aliases: []string{"/m"}, Description: "切换或查看当前模型",
			Usage: "/model [编号|<provider>|<provider>/<model>]",
			Args: "无参           - 列出所有 (provider, model) 组合, 方向键选\n" +
				"      编号           - 1~N 快速选择\n" +
				"      <provider>     - 切到该 provider 的默认模型\n" +
				"      <provider>/<model> - 切到指定模型 (model 区分大小写)\n" +
				"      <provider> <model> - 同上, 空格分隔",
			Examples: []string{
				"/model",
				"/model 3",
				"/model cs",
				"/model cs/doubao-pro",
				"/model openai o1-preview",
			},
			Handler: cmdModel,
		},
		{
			Name: "/provider", Aliases: []string{"/p"}, Description: "查看当前提供商详情",
			Usage: "/provider",
			Args: "无参。显示:\n" +
				"  - 名称 / 协议 / base_url / APIKey (前 4+ 后 4)\n" +
				"  - 当前 model + 显示名\n" +
				"  - 已配置模型数 (多模型时)",
			Examples: []string{
				"/provider",
			},
			Handler: cmdProvider,
		},
		{
			Name: "/setup", Description: "交互式配置提供商 (添加 / 删除 / 设置 Key / 测试)",
			Usage: "/setup",
			Args: "无参。命令菜单驱动, 步骤:\n" +
				"  1. 选动作 (7 个): 添加预设 / 添加自定义 / 设置 Key / 删除 / 测试 / 设默认 / 返回\n" +
				"  2. 按提示输入 name / base_url / model / key 等\n" +
				"  3. 自动写 ~/.p-chat/config.yaml\n" +
				"  4. 提示是否切换为新 provider",
			Examples: []string{
				"/setup",
			},
			Handler: cmdSetup,
		},
		{
			Name: "/config", Description: "快速配置 (命令行版本, 不进 REPL 也能用)",
			Usage: "/config <子命令> [参数]",
			Args: "provider 管理:\n" +
				"  add    <name> <base_url> <protocol>   添加 provider (单模型)\n" +
				"  add    <预设名>                       用预设 (openai/claude/deepseek/...)\n" +
				"  remove <name>                          删除 provider (别名: rm)\n" +
				"  key    <name> <key>                   设置 API key\n" +
				"  test   [name]                          测试连接 (默认所有)\n" +
				"  list                                  列出 (别名: ls)\n" +
				"\n" +
				"model 管理 (provider 下的多个模型):\n" +
				"  model list [provider]                 列出所有/指定 provider 的模型 (别名: ls)\n" +
				"  model add <provider> <model> [display] [desc...]  加模型\n" +
				"  model remove <provider> <model>        删模型 (别名: rm/del/delete)\n" +
				"  model default <provider> <model>      设为默认",
			Examples: []string{
				"/config list",
				"/config add openai https://api.openai.com/v1 openai",
				"/config add deepseek",
				"/config key openai sk-...",
				"/config test",
				"/config model list openai",
				"/config model add openai o1-preview \"o1 Preview\" \"Reasoning\"",
				"/config model default openai o1-preview",
				"/config model remove openai gpt-3.5-turbo",
			},
			Handler: cmdConfig,
		},
		{
			Name: "/new", Aliases: []string{"/reset", "/n"}, Description: "开启新对话 (清空当前上下文)",
			Usage: "/new",
			Args: "无参。\n" +
				"  - 当前会话保留在 /history 中\n" +
				"  - 自动切到一个全新空会话\n" +
				"  - 内存 + SQLite 都不变 (旧会话随时可切回)",
			Examples: []string{
				"/new",
				"/reset",
			},
			Handler: cmdNew,
		},
		{
			Name: "/clear", Aliases: []string{"/cls"}, Description: "清空当前对话的消息 (保留会话本身)",
			Usage: "/clear",
			Args: "无参。\n" +
				"  - 会话 id 不变, 但消息全清\n" +
				"  - 立即开始新话题, 旧消息已被丢弃\n" +
				"  - 与 /new 的区别: /new 切到新会话, /clear 留在原会话",
			Examples: []string{
				"/clear",
			},
			Handler: cmdClear,
		},
		{
			Name: "/export", Aliases: []string{"/save"}, Description: "导出会话到文件 [markdown|json] [id|last] [-o file]",
			Usage: "/export [format] [session] [-o <file>]",
			Args: "format     - markdown (默认) 或 json\n" +
				"      session    - last (默认, 当前会话) 或会话 id\n" +
				"      -o <file>  - 自定义输出文件路径, 否则用默认名\n" +
				"      无参 - 导出当前会话为 markdown 到当前目录",
			Examples: []string{
				"/export",
				"/export markdown",
				"/export json",
				"/export last",
				"/export markdown conv_20260625_001",
				"/export -o mychat.md",
				"/export json -o chat.json",
			},
			Handler: cmdExport,
		},
		{
			Name: "/unsafe", Description: "临时关闭沙箱保护",
			Usage: "/unsafe [on|off|once]",
			Args: "on       - 禁用沙箱直到 /unsafe off (别名: enable)\n" +
				"      off      - 重新启用沙箱 (别名: disable)\n" +
				"      once     - 只跳过下一次工具调用\n" +
				"      无参     - 显示状态和帮助",
			Examples: []string{
				"/unsafe",
				"/unsafe once",
				"/unsafe on",
				"/unsafe off",
			},
			Handler: cmdUnsafe,
		},
		{
			Name: "/auto-continue", Aliases: []string{"/ac"}, Description: "控制 P0-3 自动续 LLM 守卫 (todo 未完成时)",
			Usage: "/auto-continue [on|off]",
			Args: "on       - 启用: LLM 退出但 todo 未完成时,自动注入续提示 (默认)\n" +
				"      off      - 关闭: 严格按 LLM 自然结束处理\n" +
				"      无参     - 显示当前状态",
			Examples: []string{
				"/auto-continue",
				"/ac on",
				"/ac off",
			},
			Handler: cmdAutoContinue,
		},
		{
			Name: "/expand", Aliases: []string{"/x"}, Description: "展开上次的工具调用完整结果",
			Usage: "/expand [编号|last]",
			Args: "无参   - 列出所有缓存的工具结果 (最多 20 条, 编号 + 一行摘要)\n" +
				"      编号   - 1~N 显示第 N 个的完整结果 (换行、原始格式)\n" +
				"      last   - 显示最近一个的完整结果 (最快)",
			Examples: []string{
				"/expand",
				"/expand last",
				"/expand 2",
			},
			Handler: cmdExpand,
		},
		{
			Name: "/history", Description: "管理历史会话 (列出 / 切换 / 重命名 / 删除)",
			Usage: "/history [switch|rename|forget|list [args]]",
			Args: "无参  - 列出所有会话 (最近的在前, ● 标记当前)\n" +
				"      switch <id>            切换到指定会话 (别名: use)\n" +
				"      rename <id> <新标题>    重命名 (空格分隔, 标题可加引号)\n" +
				"      forget <id>            删除 (别名: rm/delete/del)\n" +
				"      list                   列出 (别名: ls)",
			Examples: []string{
				"/history",
				"/history switch conv_20260625_001",
				"/history rename conv_xxx \"项目讨论\"",
				"/history forget conv_xxx",
			},
			Handler: cmdHistory,
		},
		{
			Name: "/recall", Description: "语义检索知识库 + 历史对话",
			Usage: "/recall <查询语句>",
			Args: "<query>  - 任意自然语言查询\n" +
				"      检索结果按余弦相似度排序 (top-5)\n" +
				"      来源: 挂载的 KB 目录 + 历史会话消息\n" +
				"      嵌入器: local-hash (默认) 或 openai (如配置)",
			Examples: []string{
				"/recall P-Chat 架构",
				"/recall Go context cancellation",
				"/recall 上次讨论的子 agent 实现",
			},
			Handler: cmdRecall,
		},
		{
			Name: "/kb", Description: "管理外挂知识库 (挂载目录 → 扫描 → 嵌入 → 可被 /recall 检索)",
			Usage: "/kb [list|add <path>|remove <path>|scan]",
			Args: "list            列出已挂载目录 (别名: ls)\n" +
				"      add <path>      挂载目录 (递归扫描 .md/.txt/.markdown, 跳过 node_modules 等)\n" +
				"      remove <path>   卸载 (别名: rm)\n" +
				"      scan            重新索引全部 (文件 hash 不变则跳过)",
			Examples: []string{
				"/kb list",
				"/kb add ~/Documents/notes",
				"/kb add D:/projects/myapp/docs",
				"/kb scan",
				"/kb remove ~/Documents/notes",
			},
			Handler: cmdKB,
		},
		{
			Name: "/tools", Description: "列出 / 启用 / 禁用工具调用",
			Usage: "/tools [on|off]",
			Args: "无参     - 列出已注册工具 + 当前 on/off 状态 + task 限制说明\n" +
				"      on       启用 (别名: enable / 1)\n" +
				"      off      禁用 (别名: disable / 0)\n" +
				"      禁用后 LLM 看不到任何工具, 进入纯对话模式",
			Examples: []string{
				"/tools",
				"/tools off",
				"/tools on",
			},
			Handler: cmdTools,
		},
		{
			Name: "/debug", Description: "显示调试信息 (缓存命中率等)",
			Usage: "/debug [topic]",
			Args: "无参  - 默认显示 sub-agent 缓存统计\n" +
				"      cache    - 同无参 (sub-agent 缓存: 条目/命中/未命中/存储/命中率)\n" +
				"      sessions - 显示会话存储统计 (会话数 / 当前会话 / 消息数)\n" +
				"      memory   - 同 sessions (旧名兼容)",
			Examples: []string{
				"/debug",
				"/debug cache",
				"/debug sessions",
			},
			Handler: cmdDebug,
		},
		{
			Name: "/plan", Description: "让 LLM 先输出执行计划, 用户审阅后再实际执行",
			Usage: "/plan <任务描述>",
			Args: "<任务>  - 任意自然语言描述\n" +
				"      流程:\n" +
				"        1. LLM 用纯文本输出 step-by-step 计划 (不调工具)\n" +
				"        2. 用户审阅计划, 选 y/n/e\n" +
				"        3. y = 执行, n = 取消, e = 多行编辑后再次确认\n" +
				"        4. 批准后用原上下文继续, 真正调工具执行",
			Examples: []string{
				"/plan 帮我把项目里所有 *.go 文件的 license header 加上",
				"/plan 写一个 Go 函数, 把 markdown 转成 html",
				"/plan 重构 subagent 的并发执行部分",
			},
			Handler: cmdPlan,
		},
		{
			Name: "/init", Description: "在当前目录初始化 .p-chat/ 项目结构",
			Usage: "/init",
			Args: "无参。创建:\n" +
				"  .p-chat/\n" +
				"    AGENTS.md         (空模板, 用户编辑)\n" +
				"    rules/            (空目录, 放规则)\n" +
				"    skills/           (可选, 放项目级 skill)",
			Examples: []string{
				"/init",
			},
			Handler: cmdInit,
		},
		{
			Name: "/skills", Description: "列出已加载的技能 (从 ~/.p-chat/skills/ 和 .p-chat/skills/ 合并)",
			Usage: "/skills",
			Args: "无参。\n" +
				"  - 名字 (项目级优先, 全局兜底)\n" +
				"  - 来源: G (全局) / P (项目)\n" +
				"  - 描述 (第一行)",
			Examples: []string{
				"/skills",
			},
			Handler: cmdSkills,
		},
		{
			Name: "/rules", Description: "列出已加载的规则",
			Usage: "/rules",
			Args: "无参。\n" +
				"  - 同 skills 合并逻辑: 全局 ~/.p-chat/rules/*.md + 项目 .p-chat/rules/*.md\n" +
				"  - 名字 (来源标记 G/P)\n" +
				"  - 路径",
			Examples: []string{
				"/rules",
			},
			Handler: cmdRules,
		},
		{
			Name: "/agents", Description: "查看 AGENTS.md 合并后的内容",
			Usage: "/agents",
			Args: "无参。\n" +
				"  - 全局 ~/.p-chat/AGENTS.md (G)\n" +
				"  - 项目 .p-chat/AGENTS.md (P)\n" +
				"  - 合并后送进 system prompt",
			Examples: []string{
				"/agents",
			},
			Handler: cmdAgents,
		},
		{
			Name: "/quit", Aliases: []string{"/exit", "/q"}, Description: "退出 P-Chat",
			Usage: "/quit",
			Args: "无参。\n" +
				"  - 触发 SQLite 写盘 flush\n" +
				"  - 关闭 DB 连接\n" +
				"  - 进程退出 (os.Exit)",
			Examples: []string{
				"/quit",
			},
			Handler: cmdQuit,
		},
		{
			Name: "/rollback", Aliases: []string{"/rb"}, Description: "撤回消息到指定位置",
			Usage: "/rollback [id|last]",
			Args: "[id]: 消息的整数 ID (≥1)。撤回该消息及其之后的所有消息。\n" +
				"  [last]: (默认) 撤回最后一条用户消息及之后的所有回复。\n" +
				"  不带参数等同于 last。",
			Examples: []string{
				"/rollback",
				"/rollback last",
				"/rollback 42",
			},
			Handler: cmdRollback,
		},
		{
			Name: "/undo", Aliases: []string{}, Description: "撤销最近一次撤回",
			Usage: "/undo",
			Args: "无参。\n" +
				"  - 恢复最近一次 /rollback 删除的消息\n" +
				"  - 仅保留最近一次撤回的撤销信息",
			Examples: []string{
				"/undo",
			},
			Handler: cmdUndo,
		},
		{
			Name: "/fork", Aliases: []string{"/branch"}, Description: "从指定消息处创建分支对话",
			Usage: "/fork [消息序号]",
			Args: "消息序号  - 从第几条消息处分支（1-based，默认列出消息供选择）\n" +
				"      新会话复制序号及其之前的所有消息，原会话保持不变",
			Examples: []string{
				"/fork",
				"/fork 5",
				"/branch 3",
			},
			Handler: cmdFork,
		},
	}

	// Pre-compute a name/alias → *SlashCommand index for fast /help lookups.
	slashCmdIndex = make(map[string]*SlashCommand, len(slashCmds))
	for i := range slashCmds {
		c := &slashCmds[i]
		slashCmdIndex[c.Name] = c
		for _, a := range c.Aliases {
			slashCmdIndex[a] = c
		}
	}
}

var slashCmdIndex map[string]*SlashCommand

// matchCommand resolves a slash-command input to its handler and
// trailing argument. Lookup is O(1) via slashCmdIndex.
func matchCommand(input string) (*SlashCommand, string) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, ""
	}
	parts := strings.SplitN(input, " ", 2)
	cmdName := parts[0]
	if cmd, ok := slashCmdIndex[cmdName]; ok {
		args := ""
		if len(parts) > 1 {
			args = parts[1]
		}
		return cmd, args
	}
	return nil, ""
}

func cmdHelp(ctx cliContext, args string) error {
	args = strings.TrimSpace(args)

	// /help <cmd> → long-form help for one command.
	if args != "" {
		return helpOne(args)
	}

	// /help (no args) → one-line summary of every command.
	fmt.Println()
	c := color.New(color.FgCyan, color.Bold)
	c.Println("  P-Chat 命令")
	c.Println("  ─────────────────────────────────────────────────")

	w := color.New(color.FgWhite)
	g := color.New(color.FgHiBlack)

	for _, cmd := range slashCmds {
		aliases := ""
		if len(cmd.Aliases) > 0 {
			aliases = fmt.Sprintf(" (%s)", strings.Join(cmd.Aliases, ", "))
		}
		w.Printf("  %-20s", cmd.Name+aliases)
		g.Println(cmd.Description)
	}

	fmt.Println()
	w.Println("  快捷操作:")
	g.Println("    输入消息直接对话")
	g.Println("    ``` 进入多行模式，再次 ``` 结束")
	g.Println("    /help <命令>  查看详细帮助 (语法、参数、示例)")
	g.Println("    /model 1~9 快速切换模型")
	fmt.Println()
	return nil
}

// helpOne renders the long-form help for a single command. Accepts
// either the canonical name ("/model") or a friendly alias ("m").
func helpOne(query string) error {
	// Allow both "/cmd" and "cmd" forms.
	q := strings.TrimPrefix(query, "/")
	cmd, ok := slashCmdIndex["/"+q]
	if !ok {
		color.Red("  ✗ 未知命令: %s", query)
		color.HiBlack("    输入 /help 查看所有命令")
		return nil
	}

	fmt.Println()
	cyan := color.New(color.FgCyan, color.Bold)
	cyan.Printf("  %s", cmd.Name)
	if len(cmd.Aliases) > 0 {
		color.HiBlack("  (别名: %s)", strings.Join(cmd.Aliases, ", "))
	}
	fmt.Println()
	cyan.Println("  ─────────────────────────────────────────────────")

	if cmd.Description != "" {
		fmt.Println()
		color.White("  " + cmd.Description)
	}

	if cmd.Usage != "" {
		fmt.Println()
		color.Cyan("  用法")
		fmt.Println()
		fmt.Printf("    %s\n", cmd.Usage)
	}

	if cmd.Args != "" {
		fmt.Println()
		color.Cyan("  参数")
		fmt.Println()
		for _, line := range strings.Split(cmd.Args, "\n") {
			if line == "" {
				fmt.Println()
				continue
			}
			fmt.Printf("    %s\n", line)
		}
	}

	if len(cmd.Examples) > 0 {
		fmt.Println()
		color.Cyan("  示例")
		fmt.Println()
		for _, ex := range cmd.Examples {
			fmt.Printf("    %s\n", color.HiBlackString(ex))
		}
	}

	if cmd.Usage == "" && cmd.Args == "" && len(cmd.Examples) == 0 {
		color.HiBlack("  (该命令没有详细文档)")
	}
	fmt.Println()
	return nil
}

func cmdStyle(ctx cliContext, args string) error {
	current, _ := style.ParseStyle(ctx.StyleName())
	if args == "" {
		fmt.Println()
		color.Cyan("  当前风格: %s\n", ctx.StyleLabel(current))
		fmt.Println("  可用风格:")
		marker := "  "
		if style.Off == current {
			marker = color.GreenString("→ ")
		}
		fmt.Printf("    %s%s (%s)\n", marker, string(style.Off), style.Off.DisplayName())
		for _, s := range ctx.ListStyles() {
			marker := "  "
			if string(s) == ctx.StyleName() {
				marker = color.GreenString("→ ")
			}
			fmt.Printf("    %s%s (%s)\n", marker, string(s), ctx.StyleLabel(s))
		}
		fmt.Println("\n  用法: /style <off|cute|guofeng|tech>")
		return nil
	}

	if err := ctx.SetStyle(args); err != nil {
		color.Red("  未知风格: %s", args)
		return nil
	}
	newStyle, _ := style.ParseStyle(ctx.StyleName())
	color.Green("  已切换到: %s (%s)", ctx.StyleName(), ctx.StyleLabel(newStyle))
	return nil
}

func cmdMode(ctx cliContext, args string) error {
	if args == "" {
		fmt.Println()
		color.Cyan("  当前工作模式: %s\n", ctx.ModeName())
		fmt.Println("  可用模式:")
		for _, wm := range ctx.ListModes() {
			marker := "  "
			if string(wm) == ctx.ModeName() {
				marker = color.GreenString("→ ")
			}
			label := "编码侧重点"
			if wm == config.WorkModeDaily {
				label = "日常工作侧重点"
			}
			fmt.Printf("    %s%s (%s)\n", marker, string(wm), label)
		}
		fmt.Println("\n  用法: /mode <coding|daily>")
		return nil
	}

	if err := ctx.SetMode(strings.TrimSpace(args)); err != nil {
		color.Red("  未知工作模式: %s", args)
		return nil
	}
	color.Green("  已切换工作模式: %s", ctx.ModeName())
	return nil
}

func cmdProvider(ctx cliContext, args string) error {
	prov := ctx.GetCurrentProvider()
	model := ctx.GetCurrentModel()
	protocol := ctx.GetProviderProtocol(prov)

	c := color.New(color.FgCyan, color.Bold)
	c.Println()
	c.Println("  当前提供商")
	c.Println("  ─────────────────────────────────────────────────")

	view, err := ctx.ProviderConfig(prov)
	if err == nil {
		fmt.Printf("    名称:     %s\n", view.Name)
		fmt.Printf("    协议:     %s\n", view.Protocol)
		fmt.Printf("    BaseURL:  %s\n", view.BaseURL)
		fmt.Printf("    Model:    %s\n", view.Model)
		keyDisplay := "(已设置)"
		if view.APIKey == "" {
			keyDisplay = color.YellowString("(未设置 - 将无法调用)")
		} else if len(view.APIKey) > 8 {
			keyDisplay = view.APIKey[:4] + "..." + view.APIKey[len(view.APIKey)-4:]
		}
		fmt.Printf("    APIKey:   %s\n", keyDisplay)
		fmt.Printf("    模型:     %s\n", model)
	} else {
		// HTTP mode: no detailed config available.
		fmt.Printf("    名称:     %s\n", prov)
		fmt.Printf("    协议:     %s\n", protocol)
		fmt.Printf("    模型:     %s\n", model)
		fmt.Printf("    BaseURL:  %s\n", color.HiBlackString("(无详细信息 - HTTP 模式)"))
		fmt.Printf("    APIKey:   %s\n", color.HiBlackString("(无详细信息 - HTTP 模式)"))
	}

	fmt.Println()
	color.HiBlack("  使用 /model 切换提供商")
	fmt.Println()
	return nil
}

// modelEntry is a single (provider, model) pair in /model listings.
type modelEntry struct {
	provider  string
	model     string
	label     string
	value     string // "provider/model"
	isCurrent bool
}

func cmdModel(ctx cliContext, args string) error {
	providers, err := ctx.ListProviders(context.Background())
	if err != nil {
		color.Red("  ✗ %v", err)
		return nil
	}
	if len(providers) == 0 {
		color.HiBlack("  暂无可用提供商")
		return nil
	}

	var entries []modelEntry
	for _, p := range providers {
		models, _ := ctx.ListProviderModels(context.Background(), p.Name)
		if len(models) == 0 {
			continue
		}
		currentModel := ctx.GetCurrentModel()
		for _, m := range models {
			disp := m.Name
			if m.DisplayName != "" {
				disp = m.DisplayName
			}
			desc := m.Description
			if len(desc) > 40 {
				desc = desc[:37] + "..."
			}
			label := fmt.Sprintf("%-14s %-26s [%s]", p.Name, disp, p.Protocol)
			if desc != "" {
				label += "  " + desc
			}
			isCur := p.Name == ctx.GetCurrentProvider() && m.Name == currentModel
			entries = append(entries, modelEntry{
				provider:  p.Name,
				model:     m.Name,
				label:     label,
				value:     p.Name + "/" + m.Name,
				isCurrent: isCur,
			})
		}
	}
	if len(entries) == 0 {
		color.HiBlack("  暂无可用模型")
		return nil
	}

	// Quick switch forms:
	//   /model 1               → pick entry #1
	//   /model <provider>       → switch to provider's default model
	//   /model <provider>/<m>   → switch to specific model
	//   /model <provider> <m>   → same, space-separated
	if args != "" {
		if idx, err := strconv.Atoi(args); err == nil && idx >= 1 && idx <= len(entries) {
			e := entries[idx-1]
			_ = ctx.SetCurrentProvider(e.provider)
			if err := ctx.SetModel(e.provider, e.model); err != nil {
				color.Red("  ✗ %v", err)
				return nil
			}
			color.Green("  ✓ 已切换到: %s / %s", e.provider, e.model)
			return nil
		}
		if prov, model, ok := splitProviderModel(args, entries); ok {
			_ = ctx.SetCurrentProvider(prov)
			if err := ctx.SetModel(prov, model); err != nil {
				color.Red("  ✗ %v", err)
				return nil
			}
			color.Green("  ✓ 已切换到: %s / %s", prov, model)
			return nil
		}
		for _, p := range providers {
			if p.Name == args {
				_ = ctx.SetCurrentProvider(p.Name)
				if err := ctx.SetModel(p.Name, ""); err != nil {
					color.Red("  ✗ %v", err)
					return nil
				}
				display := ctx.DisplayModel(p.Name)
				color.Green("  ✓ 已切换到: %s / %s (default)", p.Name, display)
				return nil
			}
		}
		color.Red("  ✗ 未找到: %s", args)
		color.HiBlack("    用法: /model <provider>  或  /model <provider>/<model>")
		return nil
	}

	fmt.Println()
	currentDisplay := ctx.DisplayModel(ctx.GetCurrentProvider())
	color.Cyan("  当前: %s / %s", ctx.GetCurrentProvider(), currentDisplay)
	fmt.Println()

	options := make([]SelectOption, len(entries))
	defaultVal := ""
	for i, e := range entries {
		marker := "  "
		if e.isCurrent {
			marker = color.GreenString("● ")
		}
		options[i] = SelectOption{
			Label: marker + e.label,
			Value: e.value,
		}
		if e.isCurrent {
			defaultVal = e.value
		}
	}

	idx, err := SelectWithDefault("  选择模型:", options, defaultVal)
	if err != nil {
		color.HiBlack("  已取消")
		return nil
	}
	e := entries[idx]
	_ = ctx.SetCurrentProvider(e.provider)
	if err := ctx.SetModel(e.provider, e.model); err != nil {
		color.Red("  ✗ %v", err)
		return nil
	}
	color.Green("  ✓ 已切换到: %s / %s", e.provider, e.model)
	return nil
}

// splitProviderModel accepts both "provider/model" and "provider model"
// forms and resolves them against the entries list. The third return
// value is true if a unique match was found.
func splitProviderModel(s string, entries []modelEntry) (string, string, bool) {
	for _, e := range entries {
		if s == e.value {
			return e.provider, e.model, true
		}
	}
	parts := strings.SplitN(s, " ", 2)
	if len(parts) == 2 {
		for _, e := range entries {
			if parts[0] == e.provider && parts[1] == e.model {
				return e.provider, e.model, true
			}
		}
	}
	return "", "", false
}

func cmdSetup(ctx cliContext, args string) error {
	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)

	for {
		fmt.Println()
		color.Cyan("  P-Chat 提供商配置")
		color.Cyan("  ─────────────────────────────────────────────────")

		providers, _ := ctx.ListProviders(context.Background())
		if len(providers) > 0 {
			fmt.Println()
			color.White("  当前提供商:")
			for i, p := range providers {
				marker := " "
				if p.Name == ctx.GetCurrentProvider() {
					marker = "→"
				}
				fmt.Printf("    %s %d. %-14s %-28s [%s]\n", marker, i+1, p.Name, p.Model, p.Protocol)
			}
		}

		fmt.Println()
		actionOptions := []SelectOption{
			{Label: "添加预设提供商", Value: "1"},
			{Label: "添加自定义提供商", Value: "2"},
			{Label: "设置 API Key", Value: "3"},
			{Label: "删除提供商", Value: "4"},
			{Label: "测试连接", Value: "5"},
			{Label: "设置默认提供商", Value: "6"},
			{Label: "返回", Value: "7"},
		}

		idx, err := Select("  选择操作:", actionOptions)
		if err != nil {
			return nil
		}

		choice := actionOptions[idx].Value

		switch choice {
		case "1":
			if err := setupAddPreset(ctx, scanner); err != nil {
				color.Red("  错误: %v", err)
			}
		case "2":
			if err := setupAddCustom(ctx, scanner); err != nil {
				color.Red("  错误: %v", err)
			}
		case "3":
			if err := setupSetAPIKey(ctx, scanner); err != nil {
				color.Red("  错误: %v", err)
			}
		case "4":
			if err := setupRemove(ctx, scanner); err != nil {
				color.Red("  错误: %v", err)
			}
		case "5":
			if err := setupTest(ctx, scanner); err != nil {
				color.Red("  错误: %v", err)
			}
		case "6":
			if err := setupSetDefault(ctx, scanner); err != nil {
				color.Red("  错误: %v", err)
			}
		case "7":
			return nil
		}
	}
}

func setupAddPreset(ctx cliContext, scanner *bufio.Scanner) error {
	fmt.Println()
	color.Cyan("  可用预设:")
	for i, t := range ProviderTemplates {
		apiKeyMark := ""
		if t.HasAPIKey {
			apiKeyMark = " (需要 API Key)"
		}
		fmt.Printf("    %d. %-14s %s%s\n", i+1, t.Name, t.Desc, apiKeyMark)
	}
	fmt.Println()
	fmt.Print("  选择预设序号: ")

	if !scanner.Scan() {
		return nil
	}
	idxStr := strings.TrimSpace(scanner.Text())
	idx, err := strconv.Atoi(idxStr)
	if err != nil || idx < 1 || idx > len(ProviderTemplates) {
		color.HiBlack("  无效选择")
		return nil
	}

	tmpl := ProviderTemplates[idx-1]

	// Ask for API key if needed
	apiKey := ""
	if tmpl.HasAPIKey {
		fmt.Printf("  输入 %s 的 API Key (留空跳过): ", tmpl.Name)
		if !scanner.Scan() {
			return nil
		}
		apiKey = strings.TrimSpace(scanner.Text())
	}

	// Ask for model selection
	fmt.Printf("  选择模型 (默认 %s): ", tmpl.Models[0])
	if !scanner.Scan() {
		return nil
	}
	modelInput := strings.TrimSpace(scanner.Text())
	model := tmpl.Models[0]
	if modelInput != "" {
		model = modelInput
	}

	if err := ctx.AddProvider(ProviderConfigInput{
		Name:     tmpl.Name,
		Protocol: tmpl.Protocol,
		BaseURL:  tmpl.BaseURL,
		APIKey:   apiKey,
		Model:    model,
	}); err != nil {
		return err
	}

	color.Green("  ✓ 已添加: %s / %s [%s]", tmpl.Name, model, tmpl.Protocol)
	return nil
}

func setupAddCustom(ctx cliContext, scanner *bufio.Scanner) error {
	fmt.Println()
	color.Cyan("  添加自定义提供商")
	fmt.Println()

	fmt.Print("  名称: ")
	if !scanner.Scan() {
		return nil
	}
	name := strings.TrimSpace(scanner.Text())
	if name == "" {
		color.HiBlack("  已取消")
		return nil
	}

	fmt.Print("  协议 (openai/anthropic) [openai]: ")
	if !scanner.Scan() {
		return nil
	}
	protocol := strings.TrimSpace(scanner.Text())
	if protocol == "" {
		protocol = "openai"
	}

	fmt.Print("  Base URL: ")
	if !scanner.Scan() {
		return nil
	}
	baseURL := strings.TrimSpace(scanner.Text())
	if baseURL == "" {
		color.HiBlack("  已取消")
		return nil
	}

	fmt.Print("  Model: ")
	if !scanner.Scan() {
		return nil
	}
	model := strings.TrimSpace(scanner.Text())
	if model == "" {
		color.HiBlack("  已取消")
		return nil
	}

	fmt.Print("  API Key (留空跳过): ")
	if !scanner.Scan() {
		return nil
	}
	apiKey := strings.TrimSpace(scanner.Text())

	if err := ctx.AddProvider(ProviderConfigInput{
		Name:     name,
		Protocol: protocol,
		BaseURL:  baseURL,
		APIKey:   apiKey,
		Model:    model,
	}); err != nil {
		return err
	}

	color.Green("  ✓ 已添加: %s / %s [%s]", name, model, protocol)
	return nil
}

func setupSetAPIKey(ctx cliContext, scanner *bufio.Scanner) error {
	providers, _ := ctx.ListProviders(context.Background())
	if len(providers) == 0 {
		color.HiBlack("  暂无提供商")
		return nil
	}

	fmt.Println()
	color.Cyan("  设置 API Key")
	for i, p := range providers {
		fmt.Printf("    %d. %s\n", i+1, p.Name)
	}
	fmt.Print("  选择提供商序号: ")

	if !scanner.Scan() {
		return nil
	}
	idx, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
	if err != nil || idx < 1 || idx > len(providers) {
		color.HiBlack("  无效选择")
		return nil
	}

	name := providers[idx-1].Name
	fmt.Printf("  输入 %s 的 API Key: ", name)
	if !scanner.Scan() {
		return nil
	}
	apiKey := strings.TrimSpace(scanner.Text())

	if err := ctx.SetProviderAPIKey(name, apiKey); err != nil {
		return err
	}

	color.Green("  ✓ 已更新 %s 的 API Key", name)
	return nil
}

func setupRemove(ctx cliContext, scanner *bufio.Scanner) error {
	providers, _ := ctx.ListProviders(context.Background())
	if len(providers) == 0 {
		color.HiBlack("  暂无提供商")
		return nil
	}

	fmt.Println()
	color.Cyan("  删除提供商")
	for i, p := range providers {
		fmt.Printf("    %d. %s\n", i+1, p.Name)
	}
	fmt.Print("  选择要删除的序号: ")

	if !scanner.Scan() {
		return nil
	}
	idx, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
	if err != nil || idx < 1 || idx > len(providers) {
		color.HiBlack("  无效选择")
		return nil
	}

	name := providers[idx-1].Name
	if name == ctx.GetCurrentProvider() {
		color.Red("  不能删除当前正在使用的提供商")
		return nil
	}

	if err := ctx.RemoveProvider(name); err != nil {
		return err
	}

	color.Green("  ✓ 已删除: %s", name)
	return nil
}

func setupTest(ctx cliContext, scanner *bufio.Scanner) error {
	providers, _ := ctx.ListProviders(context.Background())
	if len(providers) == 0 {
		color.HiBlack("  暂无提供商")
		return nil
	}

	fmt.Println()
	color.Cyan("  测试连接")
	for i, p := range providers {
		fmt.Printf("    %d. %s\n", i+1, p.Name)
	}
	fmt.Print("  选择要测试的序号: ")

	if !scanner.Scan() {
		return nil
	}
	idx, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
	if err != nil || idx < 1 || idx > len(providers) {
		color.HiBlack("  无效选择")
		return nil
	}

	p := providers[idx-1]
	color.White("  测试中: %s / %s ...", p.Name, p.Model)

	// Simple test: send a short message via the no-tool streaming
	// path. We can't reach llm.Client.ChatStream from here, but
	// ChatStream on the context wraps the agent (in local mode),
	// which is enough to verify connectivity.
	stream, err := ctx.ChatStream(context.Background(), agent.ChatRequest{
		Provider: p.Name,
		Messages: []llm.ChatMessage{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		color.Red("  ✗ 连接失败: %v", err)
		return nil
	}

	timeout := make(chan bool, 1)
	go func() {
		time.Sleep(10 * time.Second)
		timeout <- true
	}()

	select {
	case chunk, ok := <-stream:
		if !ok {
			color.Red("  ✗ 连接失败: 流已关闭")
			return nil
		}
		if chunk.Error != "" {
			color.Red("  ✗ 连接失败: %v", chunk.Error)
			return nil
		}
		color.Green("  ✓ 连接成功")
	case <-timeout:
		color.Red("  ✗ 连接超时 (10秒)")
	}

	return nil
}

func setupSetDefault(ctx cliContext, scanner *bufio.Scanner) error {
	providers, _ := ctx.ListProviders(context.Background())
	if len(providers) == 0 {
		color.HiBlack("  暂无提供商")
		return nil
	}

	fmt.Println()
	color.Cyan("  设置默认提供商")
	for i, p := range providers {
		marker := " "
		if p.Name == ctx.GetCurrentProvider() {
			marker = "→"
		}
		fmt.Printf("    %s %d. %s\n", marker, i+1, p.Name)
	}
	fmt.Print("  选择序号: ")

	if !scanner.Scan() {
		return nil
	}
	idx, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
	if err != nil || idx < 1 || idx > len(providers) {
		color.HiBlack("  无效选择")
		return nil
	}

	name := providers[idx-1].Name
	if err := ctx.SetDefaultProvider(name); err != nil {
		return err
	}

	color.Green("  ✓ 默认提供商已设置为: %s", name)
	return nil
}

func cmdConfig(ctx cliContext, args string) error {
	if args == "" {
		// Show current config
		cfg, _ := config.Load("")
		if cfg == nil {
			color.Red("  加载配置失败")
			return nil
		}

		fmt.Println()
		color.Cyan("  当前配置")
		color.Cyan("  ─────────────────────────────────────────────────")
		fmt.Printf("  LLM 默认:   %s\n", cfg.LLM.Default)
		fmt.Printf("  风格默认:   %s\n", cfg.Style.Default)
		fmt.Printf("  记忆启用:   %v (最大 %d 条)\n", cfg.Memory.Enabled, cfg.Memory.MaxHistory)
		fmt.Println()
		fmt.Println("  LLM 提供商:")
		for _, p := range cfg.LLM.Providers {
			fmt.Printf("    • %-14s %-28s [%s] %s\n", p.Name, p.Model, p.GetProtocol(), p.BaseURL)
		}
		fmt.Println()
		color.HiBlack("  用法: /config add|remove|key|test|list <args>")
		return nil
	}

	parts := strings.Fields(args)
	sub := parts[0]
	subArgs := ""
	if len(parts) > 1 {
		subArgs = strings.Join(parts[1:], " ")
	}

	switch sub {
	case "add":
		err := configAddQuick(ctx, subArgs)
		return err
	case "remove", "rm":
		err := configRemoveQuick(ctx, subArgs)
		return err
	case "key":
		err := configSetKeyQuick(ctx, subArgs)
		return err
	case "test":
		return configTestQuick(ctx, subArgs)
	case "list", "ls":
		return configListQuick()
	case "model":
		// /config model <add|remove|list|default> ...
		return cmdConfigModel(ctx, subArgs)
	default:
		color.Red("  未知子命令: %s", sub)
		fmt.Println("  用法: /config add|remove|key|test|list|model <args>")
		return nil
	}
}

// cmdConfigModel dispatches `/config model ...` to the per-action
// handlers. All actions operate on the global config (write to
// ~/.p-chat/config.yaml) and require the user to reload by
// re-running /config or restarting for the in-memory LLM client
// to pick up the change.
func cmdConfigModel(ctx cliContext, args string) error {
	parts := strings.Fields(args)
	if len(parts) == 0 {
		fmt.Println()
		color.Cyan("  /config model  -  管理 provider 下的模型")
		color.Cyan("  ─────────────────────────────────────────────────")
		fmt.Println()
		fmt.Println("  子命令:")
		fmt.Println("    list    [provider]               - 列出全部 (或指定 provider) 的模型")
		fmt.Println("    add     <provider> <model> [...]  - 加一个新模型到 provider")
		fmt.Println("    edit    <provider> <model> [...]  - 修改模型 (display_name / max_tokens)")
		fmt.Println("    remove  <provider> <model>      - 删除一个模型")
		fmt.Println("    default <provider> <model>      - 设为默认模型")
		fmt.Println()
		fmt.Println("  add/edit 选项 (key=value 形式):")
		fmt.Println("    display_name=<text>     显示名 (edit 留空=清空)")
		fmt.Println("    description=<text>      描述")
		fmt.Println("    max_tokens_context=<n>  上下文窗口大小 (e.g. 8192, 128000)")
		fmt.Println("    max_tokens_output=<n>   单次回复上限 (e.g. 4096, 8192)")
		fmt.Println("    default=true|false      (add 时) 是否设为默认")
		fmt.Println()
		fmt.Println("  示例:")
		fmt.Println("    /config model list")
		fmt.Println("    /config model list cs")
		fmt.Println("    /config model add openai o1-preview \"o1 Preview\" \"Reasoning model\"")
		fmt.Println("    /config model add openai gpt-4o-mini display_name=\"GPT-4o Mini\" max_tokens_context=128000")
		fmt.Println("    /config model edit openai gpt-4o-mini max_tokens_output=8192")
		fmt.Println("    /config model remove openai gpt-3.5-turbo")
		fmt.Println("    /config model default openai o1-preview")
		return nil
	}

	action := parts[0]
	rest := strings.TrimSpace(strings.TrimPrefix(args, action))
	restFields := strings.Fields(rest)
	switch action {
	case "list", "ls":
		target := ""
		if len(restFields) > 0 {
			target = restFields[0]
		}
		return cmdConfigModelList(ctx, target)
	case "add":
		if len(restFields) < 2 {
			color.Red("  用法: /config model add <provider> <model> [display_name] [description] [k=v ...]")
			return nil
		}
		m, extra := parseModelKV(restFields[2:])
		m.Name = restFields[1]
		return cmdConfigModelAddFull(ctx, restFields[0], m, extra)
	case "edit", "set":
		if len(restFields) < 2 {
			color.Red("  用法: /config model edit <provider> <model> [k=v ...]")
			return nil
		}
		patch, _ := parseModelKV(restFields[2:])
		return cmdConfigModelEdit(ctx, restFields[0], restFields[1], patch)
	case "remove", "rm", "delete", "del":
		if len(restFields) != 2 {
			color.Red("  用法: /config model remove <provider> <model>")
			return nil
		}
		return cmdConfigModelRemove(ctx, restFields[0], restFields[1])
	case "default":
		if len(restFields) != 2 {
			color.Red("  用法: /config model default <provider> <model>")
			return nil
		}
		return cmdConfigModelDefault(ctx, restFields[0], restFields[1])
	default:
		color.Red("  未知子命令: %s", action)
		return nil
	}
}

// parseModelKV extracts model fields from a slice of CLI args.
// Positional args (any token that doesn't contain "=" and isn't
// already consumed as display_name/description in the caller's
// pre-processing) are returned as "extra" so callers can decide
// what to do with them.
//
// Recognized keys:
//
//	display_name, description, max_tokens_context, max_tokens_output, default
func parseModelKV(args []string) (config.ModelConfig, []string) {
	m := config.ModelConfig{}
	var extra []string
	for _, a := range args {
		idx := strings.Index(a, "=")
		if idx <= 0 {
			extra = append(extra, a)
			continue
		}
		key := a[:idx]
		val := a[idx+1:]
		switch key {
		case "display_name":
			m.DisplayName = val
		case "description":
			m.Description = val
		case "max_tokens_context", "context":
			n, err := strconv.Atoi(val)
			if err == nil && n > 0 {
				m.MaxTokensContext = n
			}
		case "max_tokens_output", "max_tokens", "output":
			n, err := strconv.Atoi(val)
			if err == nil && n > 0 {
				m.MaxTokensOutput = n
			}
		case "default":
			b, err := strconv.ParseBool(val)
			if err == nil {
				m.Default = b
			}
		default:
			extra = append(extra, a)
		}
	}
	return m, extra
}

func cmdConfigModelList(ctx cliContext, target string) error {
	models := ctx.ListAllModels(target)
	if models == nil && target != "" {
		// Provider not found, but ListAllModels returns nil silently
		// for unknown providers. Distinguish via a probe.
		if !ctx.HasProvider(target) {
			color.Red("  未找到提供商: %s", target)
			return nil
		}
	}

	fmt.Println()
	color.Cyan("  模型列表:")
	fmt.Println()
	if len(models) == 0 {
		color.HiBlack("  (无模型)")
		return nil
	}

	// Group by provider for the display.
	byProvider := make(map[string][]ModelView)
	var order []string
	for _, m := range models {
		if _, ok := byProvider[m.Provider]; !ok {
			order = append(order, m.Provider)
		}
		byProvider[m.Provider] = append(byProvider[m.Provider], m)
	}
	for _, prov := range order {
		protocol := ctx.GetProviderProtocol(prov)
		color.White("  %s  [%s]", prov, protocol)
		fmt.Println()
		for _, m := range byProvider[prov] {
			marker := "  "
			disp := m.Name
			if m.DisplayName != "" {
				disp = m.DisplayName
			}
			if m.Default {
				marker = color.GreenString("● ")
			}
			line := fmt.Sprintf("    %s%s", marker, disp)
			if m.Name != disp {
				line += color.HiBlackString("  (%s)", m.Name)
			}
			// Show per-model max_tokens hints, if set.
			settings := ctx.GetModelSettings(prov, m.Name)
			if settings.MaxTokensContext > 0 || settings.MaxTokensOutput > 0 {
				hint := color.HiBlackString("  [")
				if settings.MaxTokensContext > 0 {
					hint += color.HiBlackString("ctx=%s", humanInt(settings.MaxTokensContext))
				}
				if settings.MaxTokensContext > 0 && settings.MaxTokensOutput > 0 {
					hint += color.HiBlackString(", ")
				}
				if settings.MaxTokensOutput > 0 {
					hint += color.HiBlackString("out=%s", humanInt(settings.MaxTokensOutput))
				}
				hint += color.HiBlackString("]")
				line += " " + hint
			}
			fmt.Println(line)
			if m.Description != "" {
				color.HiBlack("        " + m.Description)
			}
		}
		fmt.Println()
	}
	return nil
}

// humanInt formats an int with a k/m suffix for compact display
// (e.g. 8192 -> "8k", 128000 -> "128k", 2000000 -> "2m"). Used
// by /config model list to keep per-model max_tokens hints on
// one line.
func humanInt(n int) string {
	switch {
	case n >= 1_000_000 && n%1_000_000 == 0:
		return fmt.Sprintf("%dm", n/1_000_000)
	case n >= 1000 && n%1000 == 0:
		return fmt.Sprintf("%dk", n/1000)
	default:
		return strconv.Itoa(n)
	}
}

func cmdConfigModelAdd(ctx cliContext, providerName, modelName, displayName, description string) error {
	if err := ctx.AddModel(providerName, modelName, displayName, description); err != nil {
		color.Red("  ✗ %v", err)
		return nil
	}
	color.Green("  ✓ 已添加模型: %s/%s", providerName, modelName)
	color.HiBlack("    用 /model 切换到 %s 后生效", modelName)
	return nil
}

// cmdConfigModelAddFull is the rich version used by the
// key=value parser: it can carry max_tokens_context,
// max_tokens_output, and the default flag.
func cmdConfigModelAddFull(ctx cliContext, providerName string, m config.ModelConfig, extra []string) error {
	// Tolerate the legacy positional form: first extra = display
	// name, the rest joined = description. This keeps
	// "/config model add openai o1-preview \"O1\" \"Reasoning model\""
	// working.
	if m.DisplayName == "" && len(extra) >= 1 {
		m.DisplayName = extra[0]
	}
	if m.Description == "" && len(extra) >= 2 {
		m.Description = strings.Join(extra[1:], " ")
	}
	if err := ctx.AddModelFull(providerName, m); err != nil {
		color.Red("  ✗ %v", err)
		return nil
	}
	color.Green("  ✓ 已添加模型: %s/%s", providerName, m.Name)
	if m.MaxTokensContext > 0 || m.MaxTokensOutput > 0 {
		color.HiBlack("    上限: context=%d, output=%d", m.MaxTokensContext, m.MaxTokensOutput)
	}
	color.HiBlack("    用 /model 切换到 %s 后生效", m.Name)
	return nil
}

func cmdConfigModelEdit(ctx cliContext, providerName, modelName string, patch config.ModelConfig) error {
	if err := ctx.UpdateModel(providerName, modelName, patch); err != nil {
		color.Red("  ✗ %v", err)
		return nil
	}
	color.Green("  ✓ 已更新模型: %s/%s", providerName, modelName)
	return nil
}

func cmdConfigModelRemove(ctx cliContext, providerName, modelName string) error {
	if err := ctx.RemoveModel(providerName, modelName); err != nil {
		color.Red("  ✗ %v", err)
		return nil
	}
	color.Green("  ✓ 已删除: %s/%s", providerName, modelName)
	return nil
}

func cmdConfigModelDefault(ctx cliContext, providerName, modelName string) error {
	if err := ctx.SetDefaultModel(providerName, modelName); err != nil {
		color.Red("  ✗ %v", err)
		return nil
	}
	color.Green("  ✓ 已设置默认: %s/%s", providerName, modelName)
	return nil
}

func configAddQuick(ctx cliContext, args string) error {
	// /config add <template> or /config add <name> <protocol> <base_url> <model> [api_key]
	if args == "" {
		color.Cyan("  用法:")
		fmt.Println("    /config add <预设名>              使用预设添加")
		fmt.Println("    /config add <name> <protocol> <base_url> <model> [api_key]")
		fmt.Println()
		fmt.Print("  可用预设: ")
		for i, t := range ProviderTemplates {
			if i > 0 {
				fmt.Print(", ")
			}
			fmt.Print(t.Name)
		}
		fmt.Println()
		return nil
	}

	parts := strings.Fields(args)
	if len(parts) == 1 {
		// Preset mode
		tmpl := FindTemplate(parts[0])
		if tmpl == nil {
			color.Red("  未找到预设: %s", parts[0])
			return nil
		}
		if err := ctx.AddProvider(ProviderConfigInput{
			Name:     tmpl.Name,
			Protocol: tmpl.Protocol,
			BaseURL:  tmpl.BaseURL,
			Model:    tmpl.Models[0],
		}); err != nil {
			return err
		}
		color.Green("  ✓ 已添加: %s / %s [%s]", tmpl.Name, tmpl.Models[0], tmpl.Protocol)
		if tmpl.HasAPIKey {
			color.Yellow("  提示: 使用 /config key %s <api_key> 设置 API Key", tmpl.Name)
		}
		return nil
	}

	if len(parts) < 4 {
		color.Red("  参数不足: /config add <name> <protocol> <base_url> <model> [api_key]")
		return nil
	}

	in := ProviderConfigInput{
		Name:     parts[0],
		Protocol: parts[1],
		BaseURL:  parts[2],
		Model:    parts[3],
	}
	if len(parts) > 4 {
		in.APIKey = parts[4]
	}

	if err := ctx.AddProvider(in); err != nil {
		return err
	}
	color.Green("  ✓ 已添加: %s / %s [%s]", in.Name, in.Model, in.Protocol)
	return nil
}

func configRemoveQuick(ctx cliContext, args string) error {
	if args == "" {
		color.Red("  用法: /config remove <name>")
		return nil
	}
	name := strings.TrimSpace(args)
	if err := ctx.RemoveProvider(name); err != nil {
		color.Red("  错误: %v", err)
		return nil
	}
	color.Green("  ✓ 已删除: %s", name)
	return nil
}

func configSetKeyQuick(ctx cliContext, args string) error {
	parts := strings.Fields(args)
	if len(parts) < 2 {
		color.Red("  用法: /config key <name> <api_key>")
		return nil
	}
	name := parts[0]
	apiKey := strings.Join(parts[1:], " ")
	if err := ctx.SetProviderAPIKey(name, apiKey); err != nil {
		color.Red("  错误: %v", err)
		return nil
	}
	color.Green("  ✓ 已更新 %s 的 API Key", name)
	return nil
}

func configTestQuick(ctx cliContext, args string) error {
	if args == "" {
		color.Red("  用法: /config test <name>")
		return nil
	}
	name := strings.TrimSpace(args)
	if !ctx.HasProvider(name) {
		color.Red("  未找到提供商: %s", name)
		return nil
	}

	color.White("  测试中: %s ...", name)

	stream, err := ctx.ChatStream(context.Background(), agent.ChatRequest{
		Provider: name,
		Messages: []llm.ChatMessage{{Role: "user", Content: "Hi"}},
	})
	if err != nil {
		color.Red("  ✗ 连接失败: %v", err)
		return nil
	}

	done := make(chan error, 1)
	go func() {
		for chunk := range stream {
			if chunk.Error != "" {
				done <- fmt.Errorf("%s", chunk.Error)
				return
			}
			if chunk.Done {
				done <- nil
				return
			}
		}
		done <- fmt.Errorf("stream closed")
	}()

	select {
	case err := <-done:
		if err != nil {
			color.Red("  ✗ 连接失败: %v", err)
		} else {
			color.Green("  ✓ 连接成功")
		}
	case <-time.After(15 * time.Second):
		color.Red("  ✗ 连接超时 (15秒)")
	}

	return nil
}

func configListQuick() error {
	cfg, _ := config.Load("")
	if cfg == nil {
		return nil
	}

	fmt.Println()
	color.Cyan("  可用提供商:")
	for i, p := range cfg.LLM.Providers {
		fmt.Printf("    %d. %-14s %-28s [%s]\n", i+1, p.Name, p.Model, p.GetProtocol())
	}
	fmt.Println()
	return nil
}

// cmdClear empties the current conversation's messages. The conversation
// itself is preserved (so /history still lists it) but starts fresh
// the next time the user sends a message. The terminal screen is also
// cleared as a side effect for symmetry with the old behavior.
func cmdClear(ctx cliContext, args string) error {
	convID := ctx.GetCurrentSessionID()
	if convID == "" {
		fmt.Print("\033[2J\033[H")
		return nil
	}
	// Use ClearMessages (not DeleteSession) so the session ID
	// is preserved. DeleteSession deletes the entire row and
	// creates a new one, which silently changes the session ID
	// and breaks any external references (URLs, scripts, the
	// /sessions list, etc.). This was inconsistent with the
	// HTTP API's DELETE /api/v1/sessions/:id/messages which
	// preserves the session row.
	if err := ctx.ClearMessages(context.Background(), convID); err != nil {
		color.Red("  ✗ 清空失败: %v", err)
		return nil
	}
	fmt.Print("\033[2J\033[H")
	color.Green("  ✓ 已清空当前对话 (%s) 的所有消息", convID)
	fmt.Println()
	return nil
}

// cmdRollback deletes messages from the given id (inclusive) onward.
// /rollback or /rollback last → rolls back to the last user message.
// /rollback <id> → rolls back to that specific message id.
func cmdRollback(ctx cliContext, args string) error {
	convID := ctx.GetCurrentSessionID()
	if convID == "" {
		color.Yellow("  当前无活跃会话")
		return nil
	}

	msgs, err := ctx.GetCurrentSessionMessages(context.Background())
	if err != nil {
		color.Red("  ✗ 获取消息失败: %v", err)
		return nil
	}
	if len(msgs) == 0 {
		color.Yellow("  当前会话无消息")
		return nil
	}

	var targetID int64
	args = strings.TrimSpace(args)
	switch args {
	case "", "last":
		for i := len(msgs) - 1; i >= 0; i-- {
			if msgs[i].Role == "user" {
				targetID = msgs[i].ID
				break
			}
		}
		if targetID == 0 {
			color.Yellow("  未找到用户消息")
			return nil
		}
	default:
		id, err := strconv.ParseInt(args, 10, 64)
		if err != nil {
			color.Red("  ✗ 无效的消息 ID: %s", args)
			return nil
		}
		found := false
		for _, m := range msgs {
			if m.ID == id {
				targetID = id
				found = true
				break
			}
		}
		if !found {
			color.Red("  ✗ 消息 ID %d 不存在", id)
			return nil
		}
	}

	delCount := 0
	for _, m := range msgs {
		if m.ID >= targetID {
			delCount++
		}
	}

	color.Yellow("  将撤回 %d 条消息 (ID ≥ %d)", delCount, targetID)
	fmt.Print("  确认撤回? [y/N] ")
	var confirm string
	fmt.Scanln(&confirm)
	if confirm != "y" && confirm != "Y" {
		color.HiBlack("  已取消")
		return nil
	}

	deleted, err := ctx.RollbackMessages(context.Background(), convID, targetID)
	if err != nil {
		color.Red("  ✗ 撤回失败: %v", err)
		return nil
	}

	ctx.SaveRollbackUndo(convID, deleted)

	color.Green("  ✓ 已撤回 %d 条消息 (输入 /undo 撤销)", len(deleted))
	return nil
}

// cmdUndo restores the messages that were deleted by the most recent
// /rollback command.
func cmdUndo(ctx cliContext, args string) error {
	convID := ctx.GetCurrentSessionID()
	if convID == "" {
		color.Yellow("  当前无活跃会话")
		return nil
	}

	undoMsgs := ctx.GetRollbackUndo(convID)
	if len(undoMsgs) == 0 {
		color.Yellow("  没有可撤销的撤回")
		return nil
	}

	if err := ctx.UndoRollback(context.Background(), convID, undoMsgs); err != nil {
		color.Red("  ✗ 撤销失败: %v", err)
		return nil
	}

	ctx.ClearRollbackUndo(convID)
	color.Green("  ✓ 已恢复 %d 条消息", len(undoMsgs))
	return nil
}

func cmdFork(ctx cliContext, args string) error {
	curID := ctx.GetCurrentSessionID()
	if curID == "" {
		color.Yellow("  当前无活跃会话")
		return nil
	}

	msgs, err := ctx.GetCurrentSessionMessages(context.Background())
	if err != nil {
		color.Red("  ✗ 获取消息失败: %v", err)
		return nil
	}
	if len(msgs) == 0 {
		color.HiBlack("  当前会话没有消息可分支")
		return nil
	}

	var targetID int64
	var targetIdx int
	args = strings.TrimSpace(args)

	if args == "" {
		fmt.Println("选择分支点（输入序号）：")
		for i, m := range msgs {
			role := "👤"
			if m.Role == "assistant" {
				role = "🤖"
			}
			preview := m.Content
			if len(preview) > 60 {
				preview = preview[:60] + "..."
			}
			fmt.Printf("  %2d  %s  %s\n", i+1, role, preview)
		}
		fmt.Print("序号> ")
		scanner := bufio.NewScanner(os.Stdin)
		if !scanner.Scan() {
			color.HiBlack("  已取消")
			return nil
		}
		idx, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
		if err != nil || idx < 1 || idx > len(msgs) {
			color.HiBlack("  无效序号")
			return nil
		}
		targetID = msgs[idx-1].ID
		targetIdx = idx - 1
	} else {
		n, err := strconv.Atoi(args)
		if err != nil || n < 1 || n > len(msgs) {
			color.HiBlack("  无效序号，有效范围 1-%d", len(msgs))
			return nil
		}
		targetID = msgs[n-1].ID
		targetIdx = n - 1
	}

	sess, err := ctx.ForkSession(context.Background(), curID, targetID)
	if err != nil {
		color.Red("  ✗ 创建分支失败: %v", err)
		return nil
	}

	ctx.SetCurrentSession(sess.ID)
	color.Green("  ✓ 已创建分支对话 %s（含 %d 条消息）", sess.ID[:16], targetIdx+1)
	color.Cyan("    标题: %s", sess.Title)
	return nil
}

// cmdPlan asks the LLM to produce a step-by-step plan in plain
// text (no tool calls). After the plan is shown, the user can:
//
//	y / Enter  - approve and execute the plan
//	n          - cancel
//	e          - edit the plan before executing (未实现)
//
// The plan is NOT saved to the conversation memory until the user
// explicitly approves.
//
// Note: this handler is REPL-only because it owns the chat-loop
// coordination (cancel / watchEsc). It is registered in the
// slash-command list with a thin adapter that re-binds to *REPL
// (see repl.go's registerPlanCommand). The HTTP cliContext returns
// an "unsupported" message.
func cmdPlan(ctx cliContext, args string) error {
	if !isLocalContext(ctx) {
		color.HiBlack("  (Plan mode 仅在本地 REPL 可用)")
		return nil
	}
	return runLocalPlan(ctx, args)
}

// executePlan runs the approved plan by sending the assistant plan
// + a "go" user message back to the LLM. Extracted from cmdPlan
// so both the y and e flows can share it.
//
// Like cmdPlan, this is REPL-only and lives in repl.go.
func executePlan(ctx cliContext, msgs []llm.ChatMessage, plan, provModel, task string) error {
	return runLocalExecutePlan(ctx, msgs, plan, provModel, task)
}

// cmdNew starts a new conversation and switches to it. The current
// conversation is preserved and accessible via /history switch.
func cmdNew(ctx cliContext, args string) error {
	sess, err := ctx.NewSession(context.Background(), httpcli.CreateSessionOpts{})
	if err != nil {
		color.Red("  ✗ 创建失败: %v", err)
		return nil
	}
	color.Green("  ✓ 已开启新对话: %s", sess.ID)
	color.HiBlack("    使用 /history 查看或切换回旧对话")
	fmt.Println()
	return nil
}

// cmdExport writes the current (or specified) conversation to a
// file in markdown or json form. See export.go for the format
// details.
func cmdExport(ctx cliContext, args string) error {
	if !isLocalContext(ctx) {
		color.HiBlack("  (导出仅在本地 REPL 可用 - HTTP 模式请用 GET /api/v1/sessions/<id>/messages)")
		return nil
	}
	lc := asLocalContext(ctx)
	if lc.r.store == nil {
		color.HiBlack("  (会话存储未启用)")
		return nil
	}

	path, err := doExport(lc.r.store, args)
	if err != nil {
		color.Red("  ✗ 导出失败: %v", err)
		return nil
	}

	color.Green("  ✓ 已导出到: %s", path)
	color.HiBlack("    cat \"%s\"  查看内容", path)
	fmt.Println()
	return nil
}

// cmdUnsafe lets the user bypass the sandbox for one tool call
// (`/unsafe once`) or for the rest of the session (`/unsafe on`).
// Use `/unsafe off` to re-enable.
func cmdUnsafe(ctx cliContext, args string) error {
	arg := strings.TrimSpace(strings.ToLower(args))
	if !isLocalContext(ctx) {
		color.HiBlack("  (沙箱控制仅在本地 REPL 可用)")
		return nil
	}
	switch arg {
	case "once":
		ctx.BypassSandboxOnce()
		color.Yellow("  ⚠ 下一次工具调用将跳过沙箱检查")
	case "on", "enable":
		ctx.SetSandbox(false)
		color.Red("  ⚠ 沙箱已禁用（直到 /unsafe off）")
	case "off", "disable":
		if err := ctx.RebuildSandbox(); err != nil {
			color.Red("  ✗ 重建沙箱失败: %v", err)
			return nil
		}
		color.Green("  ✓ 沙箱已重新启用")
	default:
		fmt.Println()
		color.Cyan("  沙箱状态")
		fmt.Println("  ─────────────────────────────────────")
		color.HiBlack("    /unsafe once  下次调用跳过沙箱")
		color.HiBlack("    /unsafe on    禁用沙箱（到 /unsafe off）")
		color.HiBlack("    /unsafe off   重新启用沙箱")
		fmt.Println()
	}
	return nil
}

// cmdAutoContinue toggles the P0-3 "todo-incomplete → re-prompt
// LLM" guard. The flag is per-session and persisted via
// PATCH /sessions/:id. Unlike /unsafe (which is local-only
// because the sandbox is shared process state), this works
// the same in HTTP and local mode — the agent reads the
// per-session flag from chatReq.AutoContinue.
func cmdAutoContinue(ctx cliContext, args string) error {
	arg := strings.TrimSpace(strings.ToLower(args))
	sid := ctx.GetCurrentSessionID()
	if sid == "" {
		color.HiBlack("  (没有当前会话)")
		return nil
	}

	switch arg {
	case "on", "enable":
		on := true
		if _, err := ctx.PatchSession(context.Background(), sid, httpcli.SessionPatchOpts{AutoContinue: &on}); err != nil {
			color.Red("  ✗ 启用失败: %v", err)
			return nil
		}
		color.Green("  ✓ auto-continue 已启用 (LLM 退出但 todo 未完成时自动续)")
	case "off", "disable":
		off := false
		if _, err := ctx.PatchSession(context.Background(), sid, httpcli.SessionPatchOpts{AutoContinue: &off}); err != nil {
			color.Red("  ✗ 关闭失败: %v", err)
			return nil
		}
		color.Yellow("  ⚠ auto-continue 已关闭 (严格按 LLM 自然结束处理)")
	default:
		// Status display: read the current value from
		// ListSessions. The interface has ListSessions on
		// both backends; GetSession is not yet exposed
		// (and would only be needed for this single
		// status read). Finding the matching session is
		// O(n) but n is small in practice.
		sessions, err := ctx.ListSessions(context.Background())
		if err != nil {
			color.HiBlack("  (无法读取会话状态: %v)", err)
			return nil
		}
		var current *httpcli.Session
		for i := range sessions {
			if sessions[i].ID == sid {
				current = &sessions[i]
				break
			}
		}
		fmt.Println()
		color.Cyan("  auto-continue 状态")
		fmt.Println("  ─────────────────────────────────────")
		if current == nil {
			color.HiBlack("    当前会话不在列表中 — 默认启用")
		} else if current.AutoContinue {
			color.Green("    当前: 启用")
		} else {
			color.Yellow("    当前: 关闭")
		}
		color.HiBlack("    /auto-continue on   启用 (默认)")
		color.HiBlack("    /auto-continue off  关闭")
		fmt.Println()
	}
	return nil
}

func cmdSkills(ctx cliContext, args string) error {
	names, err := ctx.ListSkills()
	if err != nil {
		if isUnsupported(err) {
			color.HiBlack("  (技能列表仅在本地 REPL 可用)")
			return nil
		}
		color.Red("  加载技能失败: %v", err)
		return nil
	}

	fmt.Println()
	if len(names) == 0 {
		color.HiBlack("  暂无已安装技能")
		fmt.Println("  将 SKILL.md 放入 ~/.p-chat/skills/<name>/ 或 .p-chat/skills/<name>/")
	} else {
		color.Cyan("  已安装技能 (%d)\n", len(names))
		for _, n := range names {
			fmt.Printf("    • %s\n", n)
		}
	}
	fmt.Println()
	return nil
}

func cmdRules(ctx cliContext, args string) error {
	names, err := ctx.ListRules()
	if err != nil {
		if isUnsupported(err) {
			color.HiBlack("  (规则列表仅在本地 REPL 可用)")
			return nil
		}
		color.Red("  加载规则失败: %v", err)
		return nil
	}

	fmt.Println()
	if len(names) == 0 {
		color.HiBlack("  暂无已加载规则")
		fmt.Println("  将 .md 文件放入 ~/.p-chat/rules/ 或 .p-chat/rules/")
	} else {
		color.Cyan("  已加载规则 (%d)\n", len(names))
		for _, n := range names {
			fmt.Printf("    • %s\n", n)
		}
	}
	fmt.Println()
	return nil
}

func cmdAgents(ctx cliContext, args string) error {
	global, project, err := ctx.AgentsContext()
	if err != nil {
		if isUnsupported(err) {
			color.HiBlack("  (AGENTS.md 仅在本地 REPL 可用)")
			return nil
		}
		color.Red("  加载 AGENTS.md 失败: %v", err)
		return nil
	}

	fmt.Println()
	color.Cyan("  AGENTS.md")
	color.Cyan("  ─────────────────────────────────────")

	if global != "" {
		fmt.Println()
		color.Yellow("  [全局] ~/.p-chat/AGENTS.md")
		for _, line := range strings.Split(global, "\n") {
			fmt.Printf("    %s\n", line)
		}
	} else {
		color.HiBlack("  [全局] 未找到 ~/.p-chat/AGENTS.md")
	}

	if project != "" {
		fmt.Println()
		color.Yellow("  [项目] ./AGENTS.md")
		for _, line := range strings.Split(project, "\n") {
			fmt.Printf("    %s\n", line)
		}
	} else {
		color.HiBlack("  [项目] 未找到 ./AGENTS.md")
	}

	fmt.Println()
	return nil
}

func cmdTools(ctx cliContext, args string) error {
	arg := strings.TrimSpace(strings.ToLower(args))
	switch arg {
	case "on", "enable", "1":
		ctx.SetToolsEnabled(true)
		color.Green("  ✓ 工具调用已启用")
		return nil
	case "off", "disable", "0":
		ctx.SetToolsEnabled(false)
		color.Yellow("  ✓ 工具调用已禁用")
		return nil
	}

	tools := ctx.ListTools()
	fmt.Println()
	c := color.New(color.FgCyan, color.Bold)
	c.Println("  工具状态")
	c.Println("  ─────────────────────────────────────")
	if ctx.ToolsEnabled() {
		color.Green("    ● 工具调用已启用")
	} else {
		color.HiBlack("    ○ 工具调用已禁用")
	}
	fmt.Println()
	color.Cyan("  已注册工具 (%d):", len(tools))
	if len(tools) == 0 {
		color.HiBlack("    暂无可用工具")
	} else {
		// Highlight the `task` tool specially (it has different
		// semantics: spawns a sub-agent).
		for _, t := range tools {
			marker := "  "
			if t.Highlight {
				marker = color.MagentaString("◆ ")
			}
			desc := t.Description
			// Truncate long descriptions
			if len(desc) > 80 {
				desc = desc[:77] + "..."
			}
			fmt.Printf("    %s\033[1m%-16s\033[0m  %s\n", marker, t.Name, desc)
		}
	}

	// Sub-agent hint
	hasTask := false
	for _, t := range tools {
		if t.Name == "task" {
			hasTask = true
			break
		}
	}
	if hasTask {
		fmt.Println()
		color.Magenta("  ◆ task 工具：派生子 agent")
		color.HiBlack("    - 子 agent 获得独立的 system prompt + 工具集")
		color.HiBlack("    - 排除 task 自身（防止无限递归）")
		color.HiBlack("    - 默认 5 分钟超时，父 ctx 取消会传播")
		color.HiBlack("    - 5 分钟内同 (description, style, provider) 命中缓存")
	}

	fmt.Println()
	color.HiBlack("  使用 /tools on|off 切换工具调用")
	fmt.Println()
	return nil
}

func cmdInit(ctx cliContext, args string) error {
	if err := ctx.InitProject(""); err != nil {
		if isUnsupported(err) {
			color.HiBlack("  (/init 仅在本地 REPL 可用)")
			return nil
		}
		color.Red("  ✗ %v", err)
	}
	return nil
}

func cmdHistory(ctx cliContext, args string) error {
	args = strings.TrimSpace(args)

	// No args: list conversations
	if args == "" {
		convs, err := ctx.ListSessions(context.Background())
		if err != nil {
			color.Red("  ✗ %v", err)
			return nil
		}
		current := ctx.GetCurrentSessionID()
		fmt.Println()
		color.Cyan("  历史会话 (%d):", len(convs))
		fmt.Println()
		for _, c := range convs {
			marker := "  "
			if c.ID == current {
				marker = color.GreenString("● ")
			}
			title := c.Title
			if title == "" {
				title = "(未命名)"
			}
			age := humanizeAge(time.Since(time.Unix(c.UpdatedAt, 0)))
			fmt.Printf("    %s%s\n", marker, c.ID)
			fmt.Printf("        %s · %s\n", title, age)
		}
		fmt.Println()
		color.HiBlack("  用法: /history switch <id> | rename <id> <title> | forget <id>")
		fmt.Println()
		return nil
	}

	parts := strings.SplitN(args, " ", 2)
	cmd := parts[0]
	rest := ""
	if len(parts) > 1 {
		rest = parts[1]
	}

	switch cmd {
	case "list", "ls":
		convs, err := ctx.ListSessions(context.Background())
		if err != nil {
			color.Red("  ✗ %v", err)
			return nil
		}
		fmt.Println()
		color.Cyan("  历史会话 (%d):", len(convs))
		for _, c := range convs {
			title := c.Title
			if title == "" {
				title = "(未命名)"
			}
			fmt.Printf("    %s · %s\n", c.ID, title)
		}
		fmt.Println()

	case "switch", "use":
		id := strings.TrimSpace(rest)
		if id == "" {
			color.Red("  用法: /history switch <id>")
			return nil
		}
		if err := ctx.SetCurrentSession(id); err != nil {
			color.Red("  ✗ %v", err)
			return nil
		}
		color.Green("  ✓ 已切换到: %s", id)

	case "rename":
		// /history rename <id> <title...>
		rest = strings.TrimSpace(rest)
		idx := strings.IndexAny(rest, " \t")
		if idx <= 0 {
			color.Red("  用法: /history rename <id> <新标题>")
			return nil
		}
		id := rest[:idx]
		title := strings.TrimSpace(rest[idx:])
		if err := ctx.RenameSession(context.Background(), id, title); err != nil {
			color.Red("  ✗ %v", err)
			return nil
		}
		color.Green("  ✓ 已重命名: %s → %s", id, title)

	case "forget", "delete", "rm":
		id := strings.TrimSpace(rest)
		if id == "" {
			color.Red("  用法: /history forget <id>")
			return nil
		}
		// Confirm before destructive delete — a typo'd id from
		// terminal history can permanently delete a session.
		// Use raw read since we may be in raw mode (watchEsc).
		fmt.Printf("  确认删除会话 %s? 输入 yes 确认: ", id)
		reader := bufio.NewReader(os.Stdin)
		ans, _ := reader.ReadString('\n')
		ans = strings.TrimSpace(strings.ToLower(ans))
		if ans != "yes" && ans != "y" {
			color.HiBlack("  已取消")
			return nil
		}
		if err := ctx.DeleteSession(context.Background(), id); err != nil {
			color.Red("  ✗ %v", err)
			return nil
		}
		color.Green("  ✓ 已删除: %s", id)

	default:
		color.HiBlack("  未知子命令: %s  (list | switch | rename | forget)", cmd)
	}
	return nil
}

func humanizeAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "刚刚"
	case d < time.Hour:
		return fmt.Sprintf("%d 分钟前", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d 小时前", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%d 天前", int(d.Hours()/24))
	default:
		return time.Now().Add(-d).Format("2006-01-02")
	}
}

func cmdRecall(ctx cliContext, args string) error {
	query := strings.TrimSpace(args)
	if query == "" {
		color.HiBlack("  用法: /recall <查询语句>")
		return nil
	}
	if err := ctx.Recall(context.Background(), query, 5); err != nil {
		if isUnsupported(err) {
			color.HiBlack("  (知识库未初始化)")
			return nil
		}
		color.Red("  ✗ %v", err)
	}
	return nil
}

func cmdKB(ctx cliContext, args string) error {
	args = strings.TrimSpace(args)
	parts := strings.SplitN(args, " ", 2)
	subcmd := parts[0]
	rest := ""
	if len(parts) > 1 {
		rest = parts[1]
	}
	switch subcmd {
	case "", "list", "ls":
		kbs, err := ctx.ListKBs()
		if err != nil {
			if isUnsupported(err) {
				color.HiBlack("  (知识库未初始化)")
				return nil
			}
			color.Red("  ✗ %v", err)
			return nil
		}
		if len(kbs) == 0 {
			color.HiBlack("  (未挂载任何知识库)")
			color.HiBlack("  用法: /kb add <目录路径>")
			return nil
		}
		fmt.Println()
		color.Cyan("  已挂载知识库 (%d):", len(kbs))
		for _, k := range kbs {
			fmt.Printf("    • %s\n", k.Path)
		}
		fmt.Println()
		color.HiBlack("  用法: /kb scan   重新索引全部")
		return nil
	case "add":
		if err := ctx.AddKB(rest); err != nil {
			if isUnsupported(err) {
				color.HiBlack("  (知识库未初始化)")
				return nil
			}
			color.Red("  ✗ %v", err)
		}
	case "remove", "rm":
		if err := ctx.RemoveKB(rest); err != nil {
			if isUnsupported(err) {
				color.HiBlack("  (知识库未初始化)")
				return nil
			}
			color.Red("  ✗ %v", err)
		}
	case "scan", "index":
		if _, _, err := ctx.ScanKBs(); err != nil {
			if isUnsupported(err) {
				color.HiBlack("  (知识库未初始化)")
				return nil
			}
			color.Red("  ✗ %v", err)
		}
	default:
		color.HiBlack("  未知子命令: %s  (list | add | remove | scan)", subcmd)
	}
	return nil
}

func cmdDebug(ctx cliContext, args string) error {
	topic := strings.TrimSpace(strings.ToLower(args))

	fmt.Println()
	c := color.New(color.FgCyan, color.Bold)
	c.Println("  调试信息")
	c.Println("  ─────────────────────────────────────")
	fmt.Println()

	switch topic {
	case "", "all":
		// Default: show everything that's available.
		printCacheStats(ctx)
		printMemoryStats(ctx)
	case "cache":
		printCacheStats(ctx)
	case "memory", "sessions":
		// "memory" is the legacy alias. The actual data is the
		// SQLite session/message store, not LLM long-term recall,
		// so the canonical name is now "sessions".
		printMemoryStats(ctx)
	default:
		color.HiBlack("    可用主题: cache | sessions | memory")
	}
	fmt.Println()
	return nil
}

func printCacheStats(ctx cliContext) {
	entries, hits, misses, stores, ratio, ok := ctx.SubAgentStats()
	if !ok {
		color.HiBlack("    (sub-agent 缓存未启用 / HTTP 模式)")
		fmt.Println()
		return
	}
	ratioColor := color.New(color.FgGreen)
	if ratio < 0.3 {
		ratioColor = color.New(color.FgYellow)
	}
	if ratio == 0 && misses == 0 {
		ratioColor = color.New(color.FgHiBlack)
	}

	color.Cyan("    Sub-agent 缓存")
	fmt.Println()
	fmt.Printf("      条目:      %d\n", entries)
	fmt.Printf("      命中:      %d\n", hits)
	fmt.Printf("      未命中:    %d\n", misses)
	fmt.Printf("      存储:      %d\n", stores)
	ratioColor.Printf("      命中率:    %.1f%%\n", ratio*100)
	fmt.Println()
}

func printMemoryStats(ctx cliContext) {
	convs, msgs, current, ok := ctx.MemoryStats()
	if !ok {
		color.HiBlack("    (会话存储统计仅在 CLI 本地模式可用)")
		fmt.Println()
		return
	}
	color.Cyan("    会话存储 (SQLite)")
	fmt.Println()
	fmt.Printf("      会话数:    %d\n", convs)
	if current != "" {
		fmt.Printf("      当前会话:  %s\n", current)
	}
	fmt.Printf("      消息数:    %d (当前会话)\n", msgs)
	fmt.Println()
}

func cmdQuit(ctx cliContext, args string) error {
	fmt.Println()
	color.Cyan("  再见！")
	// Return instead of os.Exit(0) so deferred flushes (DB, etc.)
	// in the caller have a chance to run.
	return errQuit
}

// newSandbox is a thin wrapper kept here so cmdUnsafe can rebuild the
// sandbox from the current config.
func newSandbox(cfg config.SandboxConfig) (*sandbox.Sandbox, error) {
	return sandbox.New(cfg)
}

// cmdExpand shows the full result of a previous tool call. Without an
// argument it lists all stored results. With a 1-based index or
// "last" it prints the full result.
func cmdExpand(ctx cliContext, args string) error {
	all := ctx.ExpandList()
	if len(all) == 0 {
		color.HiBlack("  (没有可展开的工具结果)")
		return nil
	}

	arg := strings.TrimSpace(args)
	if arg == "" {
		// List mode.
		fmt.Println()
		color.Cyan("  工具结果缓存 (%d):", len(all))
		fmt.Println()
		for _, t := range all {
			tr := toolResult{
				seq: t.Seq, tool: t.Tool, args: t.Args,
				result: t.Result, err: t.Err, duration: t.Duration, at: t.At,
			}
			fmt.Println("  " + describe(&tr))
		}
		fmt.Println()
		color.HiBlack("  用法: /expand <编号> | /expand last")
		return nil
	}

	var r2 *ToolResultView
	if arg == "last" {
		v, ok := ctx.ExpandLast()
		if !ok {
			color.Red("  ✗ 未找到 #%s", arg)
			return nil
		}
		r2 = &v
	} else {
		var idx int
		if _, err := fmt.Sscanf(arg, "%d", &idx); err != nil {
			color.Red("  ✗ 无效的编号: %s", arg)
			return nil
		}
		v, ok := ctx.ExpandByIndex(idx)
		if !ok {
			color.Red("  ✗ 未找到 #%s", arg)
			return nil
		}
		r2 = &v
	}
	if r2 == nil {
		color.Red("  ✗ 未找到 #%s", arg)
		return nil
	}

	// Print full result.
	fmt.Println()
	c := color.New(color.FgCyan, color.Bold)
	c.Printf("  #%d  %s", r2.Seq, r2.Tool)
	if r2.Args != "" {
		fmt.Printf("  %s\n", r2.Args)
	} else {
		fmt.Println()
	}
	fmt.Println("  ─────────────────────────────────────────────────")
	if r2.Err != "" {
		color.Red("  ERR: %s", r2.Err)
		fmt.Println()
	}
	if r2.Result != "" {
		// Print line by line to keep output clean. Don't strip newlines
		// so the user can see structure.
		for _, line := range strings.Split(r2.Result, "\n") {
			fmt.Printf("    %s\n", line)
		}
	}
	fmt.Println()
	return nil
}
