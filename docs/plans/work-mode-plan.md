# P-Chat 工作模式 (work_mode) 方案

> **状态**：实现方案 + 落地记录（v2） · **作者**：Claude / Codex · **日期**：2026-07-20 · **目标版本**：feat_1.0.7
>
> **目标**：在系统配置里新增「工作模式」维度，可选 `daily`（日常工作）或 `coding`（编码）。与现有 `style`（人格/说话方式）**正交**，可两两组合。
>
> 本文档记录 `work_mode` 与 `style=off` 的产品语义、实现拆分、验收标准、风险与回退路径。

---

## 1. 方案概述

### 1.1 背景

P-Chat 当前只有 `style`（说话风格）一个维度（`cute` / `guofeng` / `tech` + 用户自建），通过 `~/.p-chat/styles` 表 + CLI `/style` + GUI 对话输入区 picker 三处切换。它的语义是"**怎么说**"，并且携带该风格对应的记忆内容。

用户的诉求：加一个"**做什么**"的全局维度——

- 选「编码」→ agent 偏向读代码 / 写代码 / 调试 / 跑命令
- 选「日常工作」→ agent 偏向写文档 / 邮件 / 会议纪要 / 知识检索

补充诉求：风格需要支持在对话输入区的弹窗 picker 中选择「关闭」。关闭后，不使用任何说话风格，不注入风格 prompt，也不注入该风格的记忆内容；但 `work_mode` 仍然正常注入，因为它代表任务侧重点。

### 1.2 设计决策

| 选项 | 是否采用 | 理由 |
| --- | --- | --- |
| A. 与 `style` 正交，新增 `work_mode` 维度 | ✅ **采用** | 两个概念语义不同（"怎么说" vs "做什么"），正交最清晰；`style` 子系统已成型（DB 表 + CLI + 顶栏 picker + 大量测试），合并会污染 |
| B. 合并到 `style`（如 `coding-cute` / `daily-tech`） | ❌ | 笛卡尔积爆炸，CLI `/style` 参数变长 |
| C. work_mode 替代 style | ❌ | 抹杀"说话风格"这个独立价值 |
| D. 工具集过滤（work_mode 隐藏某些工具） | ❌ **Phase 1 不做** | 风险高、影响现有测试矩阵；先只改 prompt 引导 LLM 偏好 |
| E. per-mode 模板文件（`prompts/mode/coding.md`） | ❌ **Phase 1 不做** | YAGNI，先在 Go 代码里写常量；后续要扩展再迁 |
| F. 走「配置驱动可扩展」（config.json 配 mode 列表） | ❌ **Phase 1 不做** | 过度设计 |
| G. 给 `style` 增加 `off` 内置关闭值 | ✅ **采用** | 满足用户显式关闭风格的需求；`off` 不是 DB 中的人格，只是跳过风格 prompt/记忆的控制值 |

**最终选择**：正交 + 仅改 prompt + 枚举写死 + 全局默认 + per-session 覆盖 + V5 升级步骤。

### 1.3 取值与默认值

```text
work_mode ∈ {"daily", "coding"}

默认 = "coding"
  └─ 理由：保持现有 P-Chat"编程助手"定位，旧用户升级后行为不变。
```

`Normalize()` 规则：空字符串 / 未知值 / 大小写不一致 → 全部回落 `coding`。

### 1.4 与现有 style 系统的对比

| 维度 | 现 `style` | 新 `work_mode` |
| --- | --- | --- |
| 答的问题 | "怎么说？" | "做什么？" |
| 取值 | `off` / `cute` / `guofeng` / `tech` (+ 用户自建) | `daily` / `coding`（内置枚举） |
| 默认值 | `Config.Style.Default = "tech"` | `Config.WorkMode.Default = "coding"` |
| 配置位置 | `Config.Style` + `sessionMeta.Style` | `Config.WorkMode` + `sessionMeta.WorkMode` |
| 切换体验 | 高频：CLI `/style`、GUI 顶栏 picker | 低频：系统设置里改全局默认 + 单会话 PATCH |
| 持久化 | DB `styles` 表 + `conversations.metadata` | `config.json` + `conversations.metadata` |
| 用户自定义 | 允许（CRUD） | 不允许（内置枚举） |
| 关闭行为 | `style=off` 时不注入风格 prompt 和记忆 | 不支持关闭；空值回落到 `coding` |

### 1.5 正交组合示例

| style \ work_mode | coding | daily |
| --- | --- | --- |
| off | 无说话风格 + 编码侧重点 | 无说话风格 + 日常工作侧重点 |
| cute (小P) | 软萌编程小P | 软萌办公小P |
| guofeng (墨言) | 古风编程幕僚 | 古风办公幕僚 |
| tech (NEXUS) | 冷峻编程助手（**当前默认**） | 冷峻办公助手 |

---

## 2. 数据流（改完后）

```
用户切换 workMode
  ├─ 系统设置 (全局默认)   → PUT  /api/v1/config        → Config.WorkMode.Default
  └─ 单会话 (per-session)   → PATCH /api/v1/sessions/:id  → sessionMeta.WorkMode
                                                          ↓
                                              写 conversations.metadata
                                                          ↓
用户发消息
  → POST /api/v1/sessions/:id/messages
    → server/messages.go:SendMessage
      → 读 sessionMeta.WorkMode
      → 构造 agent.ChatRequest{ WorkMode: ... }
        → agent.ChatWithTools()
          → buildStaticSystemPrompt(style, wm, ...)
            → 拼装顺序:
              1. buildStyleBlock(style)         ← 现有，不动
              2. buildWorkModeBlock(wm)         ← 新增
              3. agents.LoadAllWithRoot(root)
              4. rules.BuildRulesContext
              5. skill.BuildSkillContext
              6. buildToolHintBlock
              7. buildWorkingDirBlock
              8. buildLanguageBlock
            → sig 多 string(wm) 一项
              ↓
            → LLM 看 system prompt = "我是 NEXUS + Working Mode: 编码"
            → tool 列表不变，prompt 引导 LLM 优先用 exec_command
```

风格关闭路径：

```
用户在风格 picker 选择「关闭」
  → PATCH /api/v1/sessions/:id {style:"off"}
  → POST /api/v1/sessions/:id/messages {style:"off", work_mode:"coding|daily"}
    → style.ParseStyle("off") = style.Off
    → buildStyleBlock(style.Off) = ""
    → getStyleMemory(style.Off) = ""
    → buildWorkModeBlock(work_mode) 仍然注入
```

**关键不变量**：
- `buildStyleBlock` / `buildToolHintBlock` / `buildToolSpecificHints` 行为 0 改动
- `style=off` 只影响 style prompt 和风格记忆，不影响 work_mode / AGENTS / rules / skills / tools
- `availableTools` 列表 0 改动
- 旧用户升级：`Config.WorkMode.Default = ""` → `Normalize()` → `coding` → 行为不变

---

## 3. 任务点清单（按 commit 顺序）

### Task 1 — 后端 config + agent 类型 + buildWorkModeBlock

**目标**：`work_mode` 类型 / 配置项 / 提示词拼装段全部就位，但不接线（前端/CLI/server 仍走旧路径）。

**改动文件**：
- `internal/config/config.go`
  - 新增 `type WorkMode string` + 常量 `WorkModeCoding` / `WorkModeDaily`
  - 新增 `type WorkModeConfig struct { Default WorkMode }` 块
  - `Config` 加 `WorkMode WorkModeConfig \`json:"work_mode"\`` 字段
  - `Default()` 工厂加 `WorkMode: WorkModeConfig{Default: WorkModeCoding}`
  - 新增方法 `(w WorkMode) Normalize() WorkMode`（空/未知回落 coding）
  - 新增方法 `(w WorkMode) IsValid() bool`（"daily" / "coding" 之外返回 false）
- `internal/agent/agent.go`
  - `ChatRequest` 加 `WorkMode config.WorkMode \`json:"work_mode,omitempty"\``
  - `ChatWithTools` 在 `buildStaticSystemPrompt` 调用前做 fallback：`req.WorkMode == ""` → `a.cfg.WorkMode.Default` → `Normalize()`
  - 把 `buildStaticSystemPrompt` 调用点从 1 处改成传 `req.WorkMode`
- `internal/agent/prompt.go`
  - `buildStaticSystemPrompt` 形参加 `wm config.WorkMode`，内部 `wm = wm.Normalize()`
  - `sig` 多 `string(wm)` 一项
  - 拼装顺序：`buildStyleBlock` 之后插入 `buildWorkModeBlock(wm)`
  - 新增 helper `buildWorkModeBlock(wm config.WorkMode) string`（两个 case + fallback）

**buildWorkModeBlock 内容模板**（写到代码里，不是文件）：

```go
// buildWorkModeBlock returns the "## Working Mode" section that
// primes the LLM with the user's chosen domain focus.
//
//   - "daily": 文档 / 邮件 / 报告 / 会议纪要 / 待办 / 翻译 / 摘要 / 信息检索
//   - "coding": 读 / 写 / 改代码 / 调试 / 跑命令 / git / code review
//
// Empty / unknown values fall back to "coding" so existing
// sessions that pre-date this field keep behaving the same.
func buildWorkModeBlock(wm config.WorkMode) string {
    mode := wm.Normalize()
    switch mode {
    case config.WorkModeDaily:
        return "\n\n---\n\n## Working Mode: 日常工作 (daily)\n\n" +
            "你当前是「日常工作」模式。该模式下，你的主要职责是帮用户处理「非编码」类的日常任务：\n\n" +
            "- **核心场景**：撰写/润色文档、邮件、报告、会议纪要、待办清单；翻译/摘要/改写；信息检索与知识问答；\n" +
            "  数据整理、表格/CSV 分析、计划/排期辅助。\n" +
            "- **首选工具**：\n" +
            "  - `read_file` / `write_file` — 读/写本地文档（用户工作目录内）。\n" +
            "  - `wiki_lookup` / `wiki_list` — **优先**用知识库回答项目/团队/产品/流程相关问题，避免凭空编造。\n" +
            "  - `recall` / `grep` — 跨会话历史与本地文件搜索。\n" +
            "  - `web_search` / `web_fetch` — 联网检索 + 抓正文。\n" +
            "  - `todo_write` — 多步骤任务时维护待办。\n" +
            "- **慎用工具**：`exec_command` 在非必要时不调用；执行前需用户确认。\n" +
            "- **回复习惯**：\n" +
            "  - 结构化（标题/列表/表格），关键结论放最前；\n" +
            "  - 文档类输出给 markdown 草稿，必要时提示「需不需要导出为 .md / .docx？」；\n" +
            "  - 信息不确定时主动标注「待确认」并指出需要哪些输入；\n" +
            "  - 不主动改代码文件，除非用户明确要求。\n"
    case config.WorkModeCoding, "":
        return "\n\n---\n\n## Working Mode: 编码 (coding)\n\n" +
            "你当前是「编码」模式。该模式下，你的主要职责是帮用户完成「编程」类任务：\n\n" +
            "- **核心场景**：阅读/理解代码、定位 bug、实现新功能、重构、写测试、跑命令验证、git 操作、code review。\n" +
            "- **首选工具**：\n" +
            "  - `read_file` / `write_file` / `edit_file` — 读写源码；\n" +
            "  - `exec_command` — 跑测试、构建、git、grep、ls 等 shell 命令；\n" +
            "  - `list_files` — 探索目录结构；\n" +
            "  - `grep` / `recall` — 在代码库中精确定位；\n" +
            "  - `wiki_lookup` — 当需要理解既有架构/约定时使用。\n" +
            "- **慎用工具**：`web_search` 仅在用户明确要求联网或本地查不到时使用。\n" +
            "- **回复习惯**：\n" +
            "  - 先给结论/方案再给解释，避免无意义寒暄；\n" +
            "  - 修改前先 `read_file` 看上下文，不要凭空补全；\n" +
            "  - 改完提示用户跑测试 / `go build` 验证；\n" +
            "  - 涉及破坏性操作（rm -rf、git push -f 等）必须二次确认。\n"
    }
    return "" // 不可达：Normalize 已保证落到合法值
}
```

**验收**：
- `go test -count=1 ./internal/config ./internal/agent` 全绿
- `internal/agent/agent_static_test.go` 新增 `TestBuildStaticSystemPrompt_Daily` 验证含 "日常工作" / "wiki_lookup"，不含 "exec_command" 描述段
- `internal/agent/agent_static_test.go` 新增 `TestBuildStaticSystemPrompt_Coding` 验证含 "exec_command" / 不含 "日常工作"
- `internal/agent/agent_static_test.go` 新增 `TestBuildStaticSystemPrompt_SigDiffersByMode` 验证 `(style, projectRoot, …)` 相同但 workMode 不同时 sig 不同 → 缓存命中失效
- `internal/agent/prompt_test.go` 新增 `TestBuildWorkModeBlock` 三个 case（空、daily、coding、bogus）
- `internal/config/config_test.go`（如不存在则新建）新增 `TestWorkModeNormalize` 边界
- `internal/agent/agent_static_test.go` 新增 `TestBuildStaticSystemPrompt_StyleOff`：验证 `style=off` 不含内置人格 prompt，但仍含 `Working Mode`
- `internal/style/manager_test.go` 覆盖 `off` / `关闭` / `无` 的解析

**注意**：此 Task 不改 `internal/server` / `internal/cli` / `frontend`，所以 wire 不到用户；仅作为库函数可独立测试。

---

### Task 2 — server session meta + messages handler 接线

**目标**：让 `work_mode` 真正从 session meta 流到 `agent.ChatRequest`，并通过 HTTP API 暴露给前端。

**改动文件**：
- `internal/server/handler.go`
  - `sessionMeta` 加 `WorkMode string` 字段
  - `sessionMetaBlob` 加 `WorkMode string \`json:"work_mode,omitempty"\``
  - `CreateSessionRequest` 加 `WorkMode string \`json:"work_mode,omitempty"\``
  - `UpdateSessionMetaRequest` 加 `WorkMode *string \`json:"work_mode,omitempty"\``
  - `SessionResponse` 加 `WorkMode string \`json:"work_mode,omitempty"\``
  - `sessionToResponse()` 复制 `WorkMode`
  - 新增 `setSessionMetaWorkMode(id, wm string)`（仿 `setSessionMetaProjectPath`）
- `internal/server/sessions.go`
  - `CreateSession` 把 `req.WorkMode` 写入 meta（通过 `setSessionMetaWorkMode`）
  - `UpdateSessionMeta` 在 `UpdateSessionMetaRequest.WorkMode != nil` 时调用 `setSessionMetaWorkMode`
- `internal/server/messages.go`
  - `SendMessage` 构造 `ChatRequest` 时把 `sessionMeta.WorkMode` 转成 `config.WorkMode(wm)` 传过去（与 `style.ParseStyle(req.Style)` 平级）
- `internal/server/handler_test.go` / `internal/server/sessions_test.go`（如有）
  - 增 `TestSessionMeta_WorkMode`：create / get / patch / get cycle

**验收**：
- `go test -count=1 ./internal/server` 全绿
- 手测 curl：建会话带 `work_mode: "daily"` → `GetSession` 返回 `work_mode: "daily"` → 发消息，LLM prompt 含 "Working Mode: 日常工作"

**落地状态**：CLI / httpcli / REPL 已接入 `work_mode`，发送消息时会透传当前模式；未显式设置的旧会话仍回落到 `Config.WorkMode.Default`。

---

### Task 3 — upgrade V5 步骤

**目标**：按 `AGENTS.md §2.5` 强约束，注册 V4→V5 升级步骤，把 `~/.p-chat/version` 从 `4` 升到 `5`。

**改动文件**：
- `internal/upgrade/version.go`
  - 新增 `V5 AppVersion = 5` 常量
  - 注释说明：V5 加 work_mode，JSON 字段 backward compatible，无需数据迁移
  - `Current = V5`
- `internal/upgrade/steps.go`
  - `steps` map 加 `V4: stepV4toV5`
  - 新增 `func stepV4toV5(_ *sql.DB) error { log.Print(...); return nil }`
- `internal/upgrade/upgrade_test.go`（如有）
  - 增 case 覆盖 V4→V5 路径

**验收**：
- `go test -count=1 ./internal/upgrade` 全绿
- 手测：旧用户 `~/.p-chat/version` 写 `4` → 启动后被改成 `5`，无报错
- 手测：全新用户 `~/.p-chat/version` 直接是 `5`

---

### Task 4 — CLI `/mode` 命令

**目标**：CLI 用户能 `/mode daily` 切换当前 REPL 的 work_mode，与 `/style` 平级。

**改动文件**：
- `internal/cli/commands.go`
  - `commandsList` 加 `Name: "/mode"` + `Aliases: ["/m"]` + `Description: "切换工作模式 [daily|coding]"`
  - 新增 `cmdMode(ctx cliContext, args string) error`（仿 `cmdStyle`）
- `internal/cli/context.go`
  - `cliContext` 接口加 `ModeName() string` / `SetMode(string) error` / `ListModes() []string`（与 `StyleName/SetStyle/ListStyles` 平行）
  - `localContext` 实现：从 `r.mode` 读，`SetMode` 写 `r.mode`（仿 `localContext.SetStyle`）
  - `httpContext` 实现：`c.mode` 字段（仿 `c.style`）
- `internal/cli/repl.go`
  - REPL 结构加 `mode config.WorkMode`
  - `NewREPL` 形参加 `mode config.WorkMode`
  - `Repl.Style` / `Repl.Mode` 显式区分
  - `chatReq` 构造时 `Mode: r.mode`
- `internal/cli/init.go`
  - 启动时读 `cfg.WorkMode.Default` 当初始 mode
- `internal/cli/input.go`
  - `printPrompt(s style.Style, provider, mode config.WorkMode)` 末尾追加 `(mode:coding)` 标识
  - `printPromptRaw` 同上
- `internal/cli/commands_test.go`
  - `mockCtx` 加 `modeArg` / `ModeName` / `SetMode` / `ListModes`
  - `TestCmdMode_List` / `TestCmdMode_Set`（仿 `TestCmdStyle_*`）
- `internal/cli/repl_test.go`
  - `NewREPL(... mode, ...)` 测试 mode 透传

**验收**：
- `go test -count=1 ./internal/cli` 全绿
- 手测：`/mode` 列两个选项；`/mode daily` 切换后 prompt 末尾变 `(mode:daily)`；发消息 LLM prompt 含 "Working Mode: 日常工作"

---

### Task 5 — 前端 API 类型 + store

**目标**：前端 TS 类型对齐后端，Pinia store 透传 `work_mode` 状态。

**改动文件**：
- `frontend/src/api/client.ts`
  - `Session` 加 `work_mode?: string`
  - `SessionMeta` 加 `work_mode: string`
  - `UpdateSessionMetaResponse` 加 `work_mode?: string`
  - `SessionResponse` 加 `work_mode?: string`
  - `SendMessageRequest` 加 `work_mode?: string`
  - `streamMessages` opts 加 `workMode?: string`（与 `style?` 平行）
  - `updateSessionMeta` 参数类型加 `work_mode: string`
  - `createSession` 参数类型加 `work_mode: string`
- `frontend/src/stores/chat.ts`
  - `Session` 状态加 `workMode`
  - `currentSessionMeta` 加 `workMode` getter
  - `createSession({ workMode })` 透传
  - `streamMessages` 传 `workMode`
  - `loadSessions` / `getSessionSnapshot` 读 `workMode`
- `frontend/src/components/SessionSidebar.vue`（如有读 session meta）
  - 不动（不显示 work_mode）

**验收**：
- `cd frontend && npx vue-tsc -b` 通过
- `cd frontend && npm run build` 通过

---

### Task 6 — 前端 AppSettingsModal + InputArea UI

**目标**：在系统设置加全局默认 picker，在会话顶栏加 per-session picker。
同时在风格 picker 的弹窗选项中增加「关闭」，选择后对当前会话 PATCH `style:"off"`。

**改动文件**：
- `frontend/src/components/AppSettingsModal.vue`
  - `system` tab 已有（`settingsTabs` 第 3 个）
  - 在该 tab 的限额/子代理配置之上加：
    ```vue
    <n-form-item label="工作模式（默认）">
      <n-radio-group v-model:value="form.workMode">
        <n-radio-button value="coding">编码（coding）</n-radio-button>
        <n-radio-button value="daily">日常工作（daily）</n-radio-button>
      </n-radio-group>
      <div class="hint">新建会话默认采用此模式；单会话可在顶栏单独切换。</div>
    </n-form-item>
    ```
  - `form` 状态加 `workMode`
  - 加载全局 config 时读 `config.work_mode.default`
  - 保存触发 `PUT /api/v1/config`（或 `PATCH`）
- `frontend/src/api/client.ts`
  - 新增 `getAppConfig()` / `updateAppConfig()`（如尚无），读 `ConfigResponse{ work_mode: { default: string } }`
- `frontend/src/components/InputArea.vue`
  - style picker 的 options 第一项加 `{ label: "关闭", value: "off" }`
  - 现有 `style-pick` 旁加 `workMode-pick`：
    ```vue
    <n-popselect
      v-model:value="currentWorkMode"
      :options="workModeOptions"
      @update:value="onWorkModeChange"
    >
      <n-button text>
        <component :is="workModeIcon" /> {{ workModeLabel }}
      </n-button>
    </n-popselect>
    ```
  - 图标：`Terminal` (coding) / `FileText` (daily)（用现有 `icons.ts` 里的 lucide）
  - `workModeOptions = [{ label: '编码 (coding)', value: 'coding' }, { label: '日常工作 (daily)', value: 'daily' }]`
  - `onWorkModeChange` 调 `api.updateSessionMeta(currentID, { work_mode: v })`
  - `currentWorkMode` 从 `currentMeta.workMode || globalConfig.workMode.default || 'coding'`

**验收**：
- `vue-tsc -b` + `npm run build` 通过
- 手测：改全局默认 → 新建会话默认采用新值；改单会话 → 不影响其他会话
- 手测：风格 picker 选择「关闭」→ 下一轮请求 `style=off` → 后端不注入风格 prompt 和记忆

---

### Task 7 — 文档更新

**目标**：把方案落地到 agent 规范文档里，让未来 agent 知道 work_mode 的存在。

**改动文件**：
- `.agents/AGENTS.md` §5 「关键文件位置速查」加：
  - `工作模式 work_mode 配置` → `internal/config/config.go` `WorkModeConfig`
  - `work_mode 提示词段` → `internal/agent/prompt.go` `buildWorkModeBlock`
  - `work_mode per-session 覆盖` → `internal/server/handler.go` `sessionMeta.WorkMode`
  - `CLI /mode 命令` → `internal/cli/commands.go` `cmdMode`
- `.agents/docs/config.md` 加 `### WorkModeConfig` 段落，描述字段 + 默认值 + `Normalize` 规则
- `.agents/docs/agent.md` §1 拼装顺序表加 `buildWorkModeBlock` 行
- `.agents/docs/INDEX.md` 「想改 X → 应读的文档」加一行 `修改工作模式 (work_mode) | config.md, agent.md`
- `.agents/docs/frontend.md` 说明 `sessionMeta.style/workMode`、`globalWorkMode`，以及 `style=off` 的关闭语义

**验收**：文档 grep `work_mode` / `workMode` / `WorkMode` 至少 5 处被引用。

---

## 4. 详细代码位置（grep anchor）

方便 reviewer 跳转：

| 内容 | 文件 : 行 |
| --- | --- |
| `Config` 结构体定义 | `internal/config/config.go:21-42` |
| `Default()` 工厂 | `internal/config/config.go:741-802` |
| `StyleConfig` 现有（参考格式） | `internal/config/config.go:315-317` |
| `ChatRequest` 定义 | `internal/agent/agent.go:340-442` |
| `buildStaticSystemPrompt` 调用点 | `internal/agent/agent.go:980` |
| `buildStaticSystemPrompt` 实现 | `internal/agent/prompt.go:30-91` |
| `buildStyleBlock`（参考） | `internal/agent/prompt.go:93-115` |
| `buildToolSpecificHints`（参考） | `internal/agent/prompt.go:137-219` |
| `sessionMeta` / `sessionMetaBlob` | `internal/server/handler.go:53-86` |
| `setSessionMeta` | `internal/server/handler.go:120-151` |
| `CreateSessionRequest` | `internal/server/handler.go:315-322` |
| `UpdateSessionMetaRequest` | `internal/server/handler.go:330-354` |
| `SessionResponse` | `internal/server/handler.go:360-385` |
| `SendMessage` handler 入口 | `internal/server/messages.go:70` (style 解析处) |
| `CreateSession` handler | `internal/server/sessions.go:120-175` |
| `UpdateSessionMeta` handler | `internal/server/sessions.go:656` |
| `cmdStyle` 参考 | `internal/cli/commands.go:545-568` |
| `cliContext` 接口 | `internal/cli/context.go:118-122` |
| `localContext.StyleName/SetStyle` | `internal/cli/context.go:675-680` |
| `httpContext.StyleName/SetStyle` | `internal/cli/context.go:1047-1052` |
| `REPL` 结构 | `internal/cli/repl.go:40-60` |
| `chatReq` 构造 | `internal/cli/context.go:1027` / `internal/cli/repl.go:197` |
| `printPrompt` | `internal/cli/input.go:291` |
| `AppVersion` | `internal/upgrade/version.go:17-42` |
| `steps` map | `internal/upgrade/steps.go:27-32` |
| `Session` TS 类型 | `frontend/src/api/client.ts:82-100` |
| `updateSessionMeta` | `frontend/src/api/client.ts:393` |
| `streamMessages` opts | `frontend/src/api/client.ts:920-948` |
| `style-pick` 参考 | `frontend/src/components/InputArea.vue:918-1098` |
| `system` tab | `frontend/src/components/AppSettingsModal.vue:170, 187-197` |

---

## 5. 测试矩阵

| 维度 | case | 期望 |
| --- | --- | --- |
| `WorkMode.Normalize()` | `""` | `coding` |
| `WorkMode.Normalize()` | `"coding"` | `coding` |
| `WorkMode.Normalize()` | `"daily"` | `daily` |
| `WorkMode.Normalize()` | `"bogus"` | `coding` |
| `WorkMode.Normalize()` | `"DAILY"` (大写) | `coding`（大小写敏感，避免 `Daily` 之类拼写） |
| `WorkMode.IsValid()` | `""` | `false` |
| `WorkMode.IsValid()` | `"coding"` | `true` |
| `WorkMode.IsValid()` | `"daily"` | `true` |
| `WorkMode.IsValid()` | `"bogus"` | `false` |
| `Config.Default()` | `WorkMode.Default` | `coding` |
| `buildWorkModeBlock` | `""` | 与 `coding` 输出一致 |
| `buildWorkModeBlock` | `"coding"` | 含 "exec_command" / 不含 "日常工作" |
| `buildWorkModeBlock` | `"daily"` | 含 "日常工作" / "wiki_lookup" / 不含 "exec_command" |
| `buildWorkModeBlock` | `"bogus"` | 与 `coding` 输出一致 |
| `buildStaticSystemPrompt` sig | `(style=tech, wm=coding, ...)` vs `(style=tech, wm=daily, ...)` | sig 不同 |
| `buildStaticSystemPrompt` | tech + daily | prompt 同时含 "NEXUS" 和 "Working Mode: 日常工作" |
| `buildStaticSystemPrompt` | cute + coding | prompt 同时含 "小P" 和 "Working Mode: 编码" |
| `buildStaticSystemPrompt` | off + coding | 不含 "NEXUS"/"小P"/"墨言"，仍含 "Working Mode: 编码" |
| `getStyleMemory` | off | 返回空字符串 |
| HTTP API | POST /sessions `{work_mode:"daily"}` | GetSession 返回 `work_mode: "daily"` |
| HTTP API | PATCH /sessions/:id `{work_mode:"coding"}` | GetSession 返回 `work_mode: "coding"` |
| HTTP API | 旧会话无 work_mode | SendMessage 时 fallback 到 Config 默认 |
| CLI | `/mode` | 列出 daily / coding |
| CLI | `/mode daily` | 切到 daily，prompt 末尾 `(mode:daily)` |
| CLI | `/mode bogus` | 报错 |
| CLI | `/style off` | 当前会话风格切到 off，后续不注入风格 prompt 和记忆 |
| Upgrade | `~/.p-chat/version=4` 启动 | 升到 5，无报错 |
| Upgrade | 全新安装 | version=5 |
| Frontend | 全局默认 radio 切换 | PUT /api/v1/config 触发 |
| Frontend | 会话级 picker 切换 | PATCH /api/v1/sessions/:id 触发 |
| Frontend | 风格 picker 选择「关闭」 | PATCH `style:"off"`；下一轮请求不带人格 prompt |
| Frontend | 加载会话 | picker 反映 session meta 的 work_mode |
| LLM 行为 | work_mode=daily + 问"帮写个邮件" | 优先用 wiki_lookup（如有）/ write_file，不用 exec_command |
| LLM 行为 | work_mode=coding + 问"go test 怎么跑" | 主动调 exec_command 跑 go test |

---

## 6. 不做什么（明确边界）

| 不做项 | 理由 | 何时做 |
| --- | --- | --- |
| ❌ work_mode 过滤 `availableTools` | 影响现有测试矩阵；prompt 引导足够 | Phase 2：观察 LLM 是否遵循偏好，若不遵循再加 tool 过滤 |
| ❌ per-mode prompt 模板文件（`prompts/mode/coding.md`） | YAGNI；2 个 mode 在代码里写常量已够 | 模式 ≥ 4 或支持用户自定义 mode 时 |
| ❌ 用户自定义 mode（CRUD） | 与 `style` 的可扩展性不同；work_mode 是"做什么"的产品定位 | 永远不做（除非业务上要"财务/营销/教育"等垂直 mode） |
| ✅ `style` 增加 `off` 控制值 | 用户明确要求关闭风格 | 已纳入 Phase 1；它不是新人格，不进入 DB CRUD |
| ❌ 数据库新表 / 改 `conversations` schema | work_mode 走 `config.json` + `metadata` JSON 列，足够 | 真要走 SQL 索引时（如要按 mode 统计） |
| ❌ 改 `buildToolSpecificHints` | 现有 per-tool 提示已成熟 | 永远不做 |
| ❌ 删除现有 `/style` 命令 | 兼容 | 永远不做 |
| ❌ 改 prefix cache 命中策略 | 接受切 mode 时 miss 一次 | 永远不做（除非频繁切换） |

---

## 7. commit 拆分与依赖关系

```
Task 1 (config + agent 类型)
   ↓
Task 2 (server 接线)
   ↓
Task 3 (upgrade V5)  ─── 可与 Task 2 并行 commit
   ↓
Task 4 (CLI)         ─── 可与 Task 5 并行
   ↓
Task 5 (前端 API + store)
   ↓
Task 6 (前端 UI)
   ↓
Task 7 (文档)
```

每个 commit 必须：
- 单点改动
- `go test -count=1 ./...` 全绿（Task 5/6 加 `vue-tsc -b` + `npm run build`）
- commit message：英文标题 + 详细中文 body（CLAUDE.md §2.1）
- 不 kill 用户 GUI 进程 / 不改 `~/.p-chat/config.yaml` / 不写密钥

commit message 模板（建议）：

```
feat(work-mode): add WorkMode type and Normalize helper

- internal/config: WorkMode + WorkModeConfig + Default=coding
- internal/agent/agent.go: ChatRequest.WorkMode + ChatWithTools fallback
- internal/agent/prompt.go: buildStaticSystemPrompt takes wm; new buildWorkModeBlock
- tests: agent_static_test, prompt_test, config_test

No server/CLI/frontend wiring yet. The new field defaults to
"coding" via Normalize(), so existing sessions behave unchanged.
```

---

## 8. 风险与回退

| 风险 | 等级 | 缓解 | 回退路径 |
| --- | --- | --- | --- |
| 旧用户升级后 `Default = ""` 落到 `coding` 改变行为 | 低 | `coding` 恰好是当前"编程助手"定位 | `Normalize` 改成占位字符串 `"auto"` 走空段 |
| prefix cache 命中率下降 | 低 | 切 mode 是低频操作 | 不回退；接受 |
| 升级 V5 失败 | 低 | noop step 不写文件 | `stepV4toV5` 改返回 nil，升级系统已有 retry |
| 前端 picker 与 style picker 视觉冲突 | 低 | 复用 `.style-pick` 样式 token + 独立 class | CSS revert 到仅 style-pick |
| LLM 不遵守 work_mode 偏好 | 中 | prompt 措辞强引导；plan 监控 | 加 tool 过滤（Phase 2 决策） |
| 破坏现有 `agent_static_test` 的 byte-exact 断言 | 中 | 提前 audit 测试是否依赖具体 prompt 字符串 | 改 test 用 substring match 而非 exact match |
| `sessionMeta.WorkMode` 与 `Style` 字段顺序错位导致 `conversations.metadata` 兼容问题 | 低 | JSON unmarshal 容错，缺字段即空 | 写一次性 SQL `UPDATE conversations SET metadata=json_set(metadata,'$.work_mode','coding')` |
| CLI 切换 mode 后未重发 system prompt 缓存 | 中 | `req.WorkMode` 在 `ChatWithTools` 入口，sig 自带 work_mode，cache miss 一次 | 无 |

**回退脚本**（如全局回退）：

```sql
-- 1. 清空 per-session work_mode
UPDATE conversations
SET metadata = json_replace(metadata, '$.work_mode', NULL)
WHERE json_extract(metadata, '$.work_mode') IS NOT NULL;

-- 2. 全局 config 改回 coding（手工编辑 ~/.p-chat/config.json）
-- { "work_mode": { "default": "coding" } }
```

---

## 9. 验收清单（DoD）

- [ ] `go test -count=1 ./...` 全绿
- [ ] `cd frontend && npx vue-tsc -b` 通过
- [ ] `cd frontend && npm run build` 通过
- [ ] `go build -o bin/pchat-server.exe ./cmd/pchat-server` 成功
- [ ] 手测 1：CLI `/mode daily` → 发消息 → LLM 用日常模式回复
- [ ] 手测 2：GUI 改全局默认 → 新建会话 → picker 显示新默认
- [ ] 手测 3：GUI 改单会话 picker → 不影响其他会话
- [ ] 手测 4：旧用户升级 → `~/.p-chat/version` 从 4 升 5，无报错
- [ ] 手测 5：style 与 work_mode 正交（cute + daily 组合正常）
- [ ] 手测 6：style=off + coding/daily 都能正常回复，且不注入风格 prompt 和记忆
- [ ] grep `.agents/` 文档含 `work_mode` / `workMode` / `WorkMode` 至少 5 处
- [ ] 每个 commit 都有英文标题 + 中文 body
- [ ] `style` 仅增加 `off` 关闭逻辑，没有把工作模式合并进风格系统
- [ ] 没改 `buildToolSpecificHints` / `buildToolHintBlock` 一行

---

## 10. 评审待确认

以下决策是**默认选择**，如有反对请在评审时提出：

1. ✅ 正交（work_mode 与 style 并存）
2. ✅ 仅改 prompt，工具全保留（Phase 1）
3. ✅ daily + coding 两种 + 默认 coding
4. ✅ 全局默认 + per-session 覆盖
5. ✅ 升级走 V5 noop step
6. ✅ 不做用户自定义 mode
7. ✅ 静态 prompt cache 接受切 mode 时的 miss

如果评审通过，按 §7 的 commit 顺序依次落地。
