package agent

// prompt.go — system-prompt construction. Every helper here is
// pure: it takes an (optional) agent state slice and a set of
// available tools, and returns the corresponding prompt block as
// a string. Used by buildStaticSystemPrompt to assemble the
// "static" half of the system prompt (the part that benefits from
// LLM-side prompt caching).
//
// Split from agent.go in T05. Behaviour unchanged.

import (
	"context"
	"fmt"
	"log"
	"runtime"
	"strings"
	"time"

	"github.com/p-chat/pchat/internal/agents"
	"github.com/p-chat/pchat/internal/config"
	"github.com/p-chat/pchat/internal/knowledge"
	"github.com/p-chat/pchat/internal/llm"
	"github.com/p-chat/pchat/internal/rules"
	"github.com/p-chat/pchat/internal/skill"
	"github.com/p-chat/pchat/internal/style"
	"github.com/p-chat/pchat/internal/tool"
)

func (a *Agent) buildStaticSystemPrompt(s style.Style, toolDefs []llm.ToolDef, availableTools []tool.Tool, projectRoot string, kbEnabled bool) (string, string, error) {
	// 2026-07: if the session's projectRoot has changed
	// since the last call (user switched projects
	// mid-session, or this is the first turn of a new
	// session), reload skills and rules from the new
	// root. The static-prompt cache is invalidated by
	// ReloadWithRootIfChanged when the root differs, so the
	// sig-comparison below always finds a miss and rebuilds
	// the prompt with the new project's skills + rules.
	a.ReloadWithRootIfChanged(projectRoot)
	toolNames := make([]string, 0, len(toolDefs))
	for _, t := range toolDefs {
		toolNames = append(toolNames, t.Name)
	}
	lang := ""
	if a.cfg != nil {
		lang = a.cfg.LLM.Output.Language
	}
	// Include kbEnabled in the signature so cached prompts that
	// include wiki/grep instructions are not reused when KB is
	// toggled off mid-conversation.
	sigKB := "kb:0"
	if kbEnabled {
		sigKB = "kb:1"
	}
	sig := strings.Join([]string{
		string(s),
		agentsSignatureWithRoot(projectRoot),
		rulesSignature(a.rules),
		skillSignature(a.skills),
		strings.Join(toolNames, ","),
		lang,
		projectRoot,
		sigKB,
	}, "|")
	if sig == a.staticPromptID && a.staticPrompt != "" {
		return a.staticPrompt, sig, nil
	}

	// Each helper below returns a fully-prefixed section
	// (header + trailing "\n---\n\n" or "\n\n---\n\n" or empty
	// when the section doesn't apply). The orchestrator stays
	// a flat list so the order and the byte-exact output are
	// easy to verify.
	var sb strings.Builder
	styleBlock, err := a.buildStyleBlock(s)
	if err != nil {
		return "", sig, err
	}
	sb.WriteString(styleBlock)
	sb.WriteString(agents.LoadAllWithRoot(projectRoot) + "\n---\n\n")
	sb.WriteString(rules.BuildRulesContext(a.rules) + "\n---\n\n")
	sb.WriteString(skill.BuildSkillContext(a.skills) + "\n---\n\n")
	sb.WriteString(buildToolHintBlock(availableTools, kbEnabled))
	sb.WriteString(buildWorkingDirBlock(projectRoot))
	sb.WriteString(buildLanguageBlock(lang))

	prompt := sb.String()
	a.staticPrompt = prompt
	a.staticPromptID = sig
	return prompt, sig, nil
}

// buildStyleBlock returns section 1 (style identity + soul) plus
// the trailing separator. Graceful fallback: if the requested
// style isn't registered (legacy "default" string, deleted
// user-defined style, etc.) we fall back to "tech" rather than
// failing the turn. The misconfiguration is logged so it stays
// visible in the server log.
//
// If even the tech fallback fails, the style manager is
// broken — we propagate the error so the caller fails the
// turn rather than emitting a degraded prompt. (Earlier
// versions emitted an empty section; that was misleading
// because the LLM would proceed with no identity at all.)
func (a *Agent) buildStyleBlock(s style.Style) (string, error) {
	stylePrompt, err := a.styleMgr.GetSystemPrompt(s)
	if err != nil {
		log.Printf("[agent] style %q not found (%v) — falling back to %q", s, err, style.Tech)
		stylePrompt, err = a.styleMgr.GetSystemPrompt(style.Tech)
		if err != nil {
			return "", fmt.Errorf("style fallback failed: %w", err)
		}
	}
	return stylePrompt + "\n\n---\n\n", nil
}

// buildToolHintBlock returns section 5 (the big one). It is a
// composite of 5 sub-blocks: tool-specific hints (recall/grep/
// wiki/question/todo_write), available-tools table, platform
// section, conversation continuity, and uploaded attachments.
// Returns "" when no tools are available, matching the original
// `if len(toolDefs) > 0` guard.
func buildToolHintBlock(availableTools []tool.Tool, kbEnabled bool) string {
	if len(availableTools) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(buildToolHint(availableTools))
	sb.WriteString(buildToolSpecificHints(availableTools, kbEnabled))
	sb.WriteString(buildAvailableToolsSection(availableTools))
	sb.WriteString(buildPlatformSection())
	sb.WriteString(buildConversationContinuitySection())
	sb.WriteString(buildAttachmentsSection())
	return sb.String()
}

// buildToolSpecificHints emits per-tool guidance (recall, grep,
// wiki, question, todo_write). Only sections for tools the LLM
// actually has access to are emitted.
func buildToolSpecificHints(availableTools []tool.Tool, kbEnabled bool) string {
	hasRecall, hasGrep, hasWiki, hasQuestion, hasTodoWrite := false, false, false, false, false
	for _, t := range availableTools {
		switch t.Name {
		case "recall":
			hasRecall = true
		case "grep":
			hasGrep = true
		case "wiki_lookup":
			hasWiki = true
		case "question":
			hasQuestion = true
		case "todo_write":
			hasTodoWrite = true
		}
	}
	var sb strings.Builder
	if hasWiki && kbEnabled {
		sb.WriteString("\n\n---\n\n## Using Knowledge Base (wiki_lookup / wiki_list)\n\n" +
			"**何时必须查询知识库：**\n" +
			"- 用户询问项目相关概念、设计、架构、配置、API、流程等任何专业问题时，**优先查询知识库**，而非仅凭训练数据回答。\n" +
			"- 系统提示中已包含知识库索引概览（一级索引），根据概览定位相关文件后再检索。\n" +
			"\n**工具使用规则：**\n" +
			"- `wiki_lookup(query=\"\")` — 查询为空时，返回知识库中所有文件目录（L2 列表），按关联度排序。默认每页 20 条，可用 page 翻页。\n" +
			"- `wiki_lookup(query=\"关键词\")` — 按关键词、标题或概览搜索条目，返回匹配的 L3 章节节点及其所属文件（L2 父节点）。\n" +
			"- `wiki_lookup(query=\"...\", expand=true)` — 同时返回匹配条目的完整正文内容。\n" +
			"- `wiki_list(parent_id=N)` — 列出父节点 N 下的所有子节点。L1（id=1）列出所有文件；L2 节点列出该文件所有章节。\n" +
			"\n**标准流程：**\n" +
			"1. 先看系统提示中的一级索引概览，找到可能相关的文件（L2）。\n" +
			"2. 用 wiki_lookup 搜索关键词或浏览目录定位目标文件/章节。\n" +
			"3. 用 wiki_lookup(query=\"...\", expand=true) 获取完整内容。\n" +
			"4. 信息足够后直接回答 → 不需要再调用任何 wiki 工具。\n")
	}
	if hasRecall {
		sb.WriteString("\n\n---\n\n## Using recall\n\n" +
			"当你不确定某条信息、需要查代码/文档、或想引用历史对话时，\n" +
			"先用 `recall(query=\"...\")` 工具查一下知识库/历史。\n" +
			"不要凭印象编造 API 名称、文件路径、函数签名。\n")
	}
	if hasGrep {
		sb.WriteString("\n\n---\n\n## Using grep\n\n" +
			"使用 `grep(pattern=\"...\")` 在知识库文件中精确搜索关键词。\n" +
			"适用场景：找特定函数名、变量名、类名、配置项、或任何精确文本。\n" +
			"recall 适合语义概念搜索，grep 适合精确字符串定位。\n" +
			"两者可结合使用：先用 recall 理解上下文，再用 grep 精确定位。\n")
	}
	if hasTodoWrite {
		sb.WriteString("\n\n---\n\n## Task Planning with todo_write\n\n" +
			"使用 `todo_write` 工具创建和管理结构化任务列表。\n" +
			"何时使用：复杂多步骤任务（3+ 步）、用户明确要求、收到新指令后、开始或完成工作时。\n" +
			"规则：\n" +
			"- 始终包含完整列表（替换式，非追加式）\n" +
			"- 同时只能有一个任务处于 in_progress\n" +
			"- 完成任务后立即标记为 done（不要批量标记）\n" +
			"- 如果测试失败、实现不完整或错误未解决，不要标记为 done\n" +
			"- 状态: pending（待处理）、in_progress（进行中）、done（已完成）、cancelled（已取消）\n" +
			// P1-1 (Plan B): the LLM often exits the ReAct loop
			// with a "ready to continue" text block instead of
			// the next tool_call. Plan A (P0-3) auto-re-prompts
			// when the todo list still has unfinished items, but
			// the cleanest fix is for the LLM to update the
			// todo list BEFORE it tries to exit. This rule makes
			// that contract explicit. Per-session opt-out via
			// ChatRequest.AutoContinue (P0-3) — the auto-prompt
			// is a backstop, not a substitute.
			"- **完成契约**：在你结束当前回合前（停止调用工具、只发文本总结），**必须**先调用 `todo_write` 把列表更新到最终状态——所有已完成项标 `done`，无法完成的项标 `cancelled`。如果列表里还有 `pending` 或 `in_progress`，说明任务没做完，你应该继续调用工具而不是发总结。\n")
	}
	if hasQuestion {
		sb.WriteString("\n\n---\n\n## Asking the User (question tool)\n\n" +
			"当你需要用户决策、明确需求或在执行前确认计划时，使用 `question` 工具。\n" +
			"何时使用：\n" +
			"- 需求不明确，有多个可行的技术方案\n" +
			"- 需要用户选择工具、框架或架构\n" +
			"- 在执行前需要用户确认关键决策\n" +
			"- 用户指令模糊，需要澄清\n" +
			"最多一次提 4 个问题，每个问题 2-4 个选项。\n" +
			"不要在简单/琐碎的事务上使用（如「我能开始吗？」）。\n")
	}
	return sb.String()
}

// buildAvailableToolsSection emits the "Available Tools" markdown
// table and the opcode-style operation→tool mapping. Listing
// every available tool explicitly prevents the LLM from
// hallucinating non-existent tools (e.g. grep, bash, find).
func buildAvailableToolsSection(availableTools []tool.Tool) string {
	var sb strings.Builder
	sb.WriteString("\n\n---\n\n## Available Tools\n\n")
	sb.WriteString("You have access to these tools. Call ONLY the tools in this list.\n\n")
	sb.WriteString("| Tool | What it does |\n")
	sb.WriteString("|------|-------------|\n")
	for _, t := range availableTools {
		desc := t.Description
		if idx := strings.Index(desc, "."); idx > 0 {
			desc = desc[:idx]
		}
		sb.WriteString(fmt.Sprintf("| `%s` | %s |\n", t.Name, desc))
	}
	sb.WriteString("\nOperation → correct tool mapping:\n")
	sb.WriteString("- Read a file → `read_file` (NOT `cat` / `type` / `head` / `tail`)\n")
	sb.WriteString("- Write a file → `write_file` (NOT `echo >` / `cat >`)\n")
	sb.WriteString("- List directory → `list_files` (NOT `ls` / `dir`)\n")
	sb.WriteString("- System commands → `exec_command` (NOT `bash` / `sh` / `powershell`)\n")
	sb.WriteString("- Search file contents → `exec_command` with shell search commands\n")
	sb.WriteString("- Search the web → `web_search` (returns title+url+snippet; chain with `web_fetch` for full content)\n")
	sb.WriteString("- Fetch a URL → `web_fetch` (NOT `curl` / `Invoke-WebRequest`)\n")
	sb.WriteString("- Manage tasks → `todo_write`\n")
	sb.WriteString("- Ask user → `question`\n")
	return sb.String()
}

// buildPlatformSection emits OS-specific command-availability
// guidance. See opencode's shell/prompt.ts for the original
// design; the Windows branch is the more thorough one because
// cmd.exe's command set differs most from POSIX expectations.
func buildPlatformSection() string {
	var sb strings.Builder
	sb.WriteString("\n\n---\n\n## Platform\n\n")
	sb.WriteString(fmt.Sprintf("Platform: %s\n", runtime.GOOS))
	if runtime.GOOS == "windows" {
		sb.WriteString("Shell for exec_command: cmd.exe /C <command>\n")
		sb.WriteString("Chain commands: `&` (always run next) or `&&` (only if previous succeeded).\n\n")
		sb.WriteString("Available built-in commands:\n")
		sb.WriteString("  dir       — list directory contents (NOT ls)\n")
		sb.WriteString("  type      — print file content (NOT cat)\n")
		sb.WriteString("  findstr   — search text in files (NOT grep / rg)\n")
		sb.WriteString("  echo      — print text\n")
		sb.WriteString("  copy      — copy files (NOT cp)\n")
		sb.WriteString("  move      — move / rename files (NOT mv)\n")
		sb.WriteString("  del / rd  — delete files / dirs (NOT rm)\n")
		sb.WriteString("  mkdir     — create directory\n")
		sb.WriteString("  cd        — change directory (prefer using work_dir parameter)\n")
		sb.WriteString("  set       — set / show environment variables (NOT export)\n")
		sb.WriteString("\nCommands that do NOT exist on Windows (never call these):\n")
		sb.WriteString("  grep, rg, ls, bash, sh, find, cp, mv, rm, cat, chmod, sudo\n")
		sb.WriteString("\nPowerShell is available via: pwsh -NoProfile -Command \"...\"\n")
	} else {
		sb.WriteString("Shell for exec_command: /bin/sh -c <command>\n")
		sb.WriteString("Standard Unix tools are available (grep, ls, cat, find, etc.).\n")
	}
	return sb.String()
}

// buildConversationContinuitySection emits style-agnostic
// persistence / anti-repetition / tool-failure-fallback guidance.
// opencode's "beast mode" prompt drives persistence; this is a
// condensed version applied at the system level so it works
// across all personality styles.
func buildConversationContinuitySection() string {
	var sb strings.Builder
	sb.WriteString("\n\n---\n\n## Conversation Continuity\n\n")
	sb.WriteString("You are in a continuous conversation loop with tool access. ")
	sb.WriteString("Your goal is to complete the task, not merely report status.\n\n")
	sb.WriteString("When a tool call fails:\n")
	sb.WriteString("1. Read the error message carefully — identify the root cause\n")
	sb.WriteString("2. Try an alternative approach using different tools or parameters\n")
	sb.WriteString("3. After 3 consecutive failures on the same task, use `question` to ask the user for guidance\n")
	sb.WriteString("4. A tool failure does NOT mean the task is impossible — keep going\n")
	sb.WriteString("5. NEVER end a turn with only a status summary after a tool error — always propose or attempt the next step\n\n")
	sb.WriteString("Operation → fallback mapping (when the primary approach fails):\n")
	sb.WriteString("- `read_file` path not found → try `list_files` to discover the correct path\n")
	sb.WriteString("- Command not found in `exec_command` → check the Platform section for available commands\n")
	sb.WriteString("- File too large for `read_file` → use `exec_command` with `type` or `findstr` to grep specific lines\n")
	sb.WriteString("- Tool not found error → re-read the Available Tools table; use only listed tools\n")
	sb.WriteString("- `browser_*` connection error (\"connection closed\", \"browser extension has disconnected\") → ")
	sb.WriteString("the extension may reconnect. Retry once; if it fails again, tell the user the browser extension disconnected ")
	sb.WriteString("and ask whether to wait, re-establish the connection, or continue without browser tools\n")
	sb.WriteString("- `browser_screenshot` captures the viewport and the picture is automatically delivered as a " +
		"follow-up image message so you can see it directly (requires vision). If the picture doesn't appear or the " +
		"model doesn't support vision, fall back to `browser_snapshot` (text-based, no image payload)\n")
	sb.WriteString("- `browser_snapshot` returns too few elements (e.g. SPA page where content is dynamic divs, not interactive elements) → ")
	sb.WriteString("use `browser_extract` to get all visible rendered text content\n")
	sb.WriteString("- Reading page content on a SPA / JavaScript-heavy site → ")
	sb.WriteString("`browser_extract` is preferred over `browser_snapshot` + `browser_screenshot` because it extracts the full ")
	sb.WriteString("JavaScript-rendered text without requiring vision capabilities\n")
	sb.WriteString("- `browser_*` returns \"not found\" listing it as available → the tool was unregistered mid-turn due to browser disconnect. ")
	sb.WriteString("Do NOT retry browser_* tools; use `web_fetch` for HTTP content or text-based tools for other data\n\n")
	sb.WriteString("## Anti-Repetition\n\n")
	sb.WriteString("When the user repeats a question or instruction, always reason fresh: ")
	sb.WriteString("do NOT echo or copy your previous response. The available tool set may have changed since your last turn ")
	sb.WriteString("(tools can be dynamically registered or unregistered mid-conversation). ")
	sb.WriteString("Always check the Available Tools table first, then re-evaluate your options. ")
	sb.WriteString("Repeating an identical reply is a bug — if the same question is asked twice, the user wants a DIFFERENT answer, ")
	sb.WriteString("not the same one.\n\n")
	sb.WriteString("Only stop when:\n")
	sb.WriteString("- The task is truly complete and you have delivered the final result to the user\n")
	sb.WriteString("- The user explicitly says to stop\n")
	sb.WriteString("- All reasonable approaches have been exhausted (and you explain why to the user)\n\n")
	sb.WriteString("IMPORTANT: A tool error — especially a transient one like a connection error — ")
	sb.WriteString("is NOT a valid reason to end the turn. If the original task cannot proceed, ")
	sb.WriteString("explicitly tell the user what happened and propose concrete next steps. ")
	sb.WriteString("Do NOT silently output a summary and stop without a text response.\n")
	return sb.String()
}

// buildAttachmentsSection reminds the LLM that uploaded images
// arrive as vision input in the user message (image_url content
// parts), NOT as files on disk. The phrasing is deliberately
// positive and language-neutral — earlier versions literally
// primed the LLM with "ERROR: ... Inform the user" by name and
// the model echoed that exact phrasing back to users.
func buildAttachmentsSection() string {
	return "\n\n---\n\n## Uploaded Attachments\n\n" +
		"User-uploaded images and files are sent directly inside the user message — " +
		"images as image_url content parts (data URLs), text files as inline blocks. " +
		"You can see them. Just answer based on their content.\n\n" +
		"Do not call read_file on an uploaded image: it lives on disk as a temporary " +
		"file, and read_file only handles text. If read_file returns a binary error, " +
		"that is read_file's limitation, not a problem with the attachment — the image " +
		"was already delivered to you through the user message. " +
		"Respond in the same language as the conversation.\n"
}

// buildWorkingDirBlock returns section 6 (project root) or ""
// when no root is configured. Tells the LLM which directory to
// use as CWD for exec_command and file operations.
func buildWorkingDirBlock(projectRoot string) string {
	if projectRoot == "" {
		return ""
	}
	return fmt.Sprintf("\n\n---\n\n## Working Directory\n\n"+
		"Your working directory is fixed at `%s`. exec_command runs here automatically "+
		"(the work_dir argument is ignored). read_file and write_file resolve relative "+
		"paths against this directory.\n", projectRoot)
}

// buildLanguageBlock returns section 7 (output language hint)
// or "" if `lang` is unrecognized. The default ("auto" or "")
// follows the opencode rule of "Respond in the same language as
// the conversation" — the LLM already follows the user's
// language, so we don't hardcode one.
func buildLanguageBlock(lang string) string {
	switch lang {
	case "zh":
		return "\n---\n\n## 输出语言\n\n请用简体中文回答用户的问题。\n"
	case "en":
		return "\n---\n\n## Output Language\n\nPlease answer in English.\n"
	case "auto", "":
		return "\n\n---\n\n## Output Language\n\nRespond in the same language as the conversation.\n"
	default:
		// Unknown language code: treat as auto rather than
		// emitting a wrong-locked prompt. Returning the
		// conversation-language default is the least-
		// surprising behaviour.
		return "\n\n---\n\n## Output Language\n\nRespond in the same language as the conversation.\n"
	}
}

// appendWorkingDirectoryBlock returns the "## Working Directory"
// section text, formatted exactly as buildStaticSystemPrompt emits
// it. Exposed as a function so the sub-agent prompt re-append path
// (which fires AFTER buildStaticSystemPrompt has been overridden by
// PromptOv) stays in lock-step with the main-agent wording — any
// drift between the two will confuse the LLM.
func appendWorkingDirectoryBlock(projectRoot string) string {
	return fmt.Sprintf("\n\n---\n\n## Working Directory\n\n"+
		"Your working directory is fixed at `%s`. exec_command runs here automatically "+
		"(the work_dir argument is ignored). read_file and write_file resolve relative "+
		"paths against this directory.\n", projectRoot)
}

// buildKBIndex builds the Knowledge Base section of the system prompt.
// When KBBase is "__all__", all enabled bases are listed. When it's a
// specific name, only that base's index is shown. If the base has no
// sections, a placeholder is returned. The output is truncated at 3000
// characters to avoid prompt explosion. Results are cached for 60s to
// avoid repeated full-DB scans per message turn.
// Uses the L1 overview from the three-level index tree.
func (a *Agent) buildKBIndex(kbBase string) string {
	nowUnix := time.Now().Unix()
	if a.kbIndexCache != "" && a.kbIndexCacheKey == kbBase && (nowUnix-a.kbIndexCacheTime) < 30 {
		return a.kbIndexCache
	}

	kc := a.cfg.Knowledge
	var bases []config.KnowledgeBase
	if kbBase == "__all__" {
		for _, b := range kc.Bases {
			if b.Enabled {
				bases = append(bases, b)
			}
		}
	} else {
		for _, b := range kc.Bases {
			if b.Name == kbBase && b.Enabled {
				bases = append(bases, b)
				break
			}
		}
	}
	if len(bases) == 0 {
		result := "\n[Knowledge Base]\n(no enabled bases configured)\n"
		a.kbIndexCache = result
		a.kbIndexCacheKey = kbBase
		a.kbIndexCacheTime = nowUnix
		return result
	}

	var sb strings.Builder
	for _, base := range bases {
		store, err := knowledge.GetOrOpenWikiStore(base.Name, base.Path)
		if err != nil {
			continue
		}
		overview, err := store.GetL1Overview(context.Background(), base.Name)
		if err != nil || overview == "" {
			continue
		}
		// overview is pre-formatted by the scan pipeline as the L1 prompt content.
		sb.WriteString(overview)
	}

	if sb.Len() == 0 {
		result := "\n[Knowledge Base]\n(index empty — run a scan first)\n"
		a.kbIndexCache = result
		a.kbIndexCacheKey = kbBase
		a.kbIndexCacheTime = nowUnix
		return result
	}

	// Append tool usage footer.
	sb.WriteString("\n\n使用 wiki_lookup(query, page, size) 检索，默认 20 条/页。")
	sb.WriteString("query=空 浏览目录；query=关键词 搜索匹配；expand=true 获取全文。")
	result := sb.String()
	a.kbIndexCache = result
	a.kbIndexCacheKey = kbBase
	a.kbIndexCacheTime = nowUnix
	return result
}

// Reload forces the next call to rebuild the static system prompt
