# P-Chat 项目 Agent 指令

> **P-Chat** — Go + Wails 桌面端 LLM 聊天应用，配套 CLI / 独立 server。
>
> 本文档是 agent 协作的**唯一规范来源**。各 agent 工具（opencode / codex / claude 等）通过 `.agents/scripts/install.{ps1,sh}` 自动建立符号链接，统一读取此文件。

---

## 0. Agent 启动检查清单（每次激活时必须执行）

每个 agent 在开始工作前，**必须**按顺序执行以下检查：

### 步骤 1：验证安装脚本是否已执行

```powershell
# 检查符号链接是否已建立（任一文件存在即视为已安装）
Test-Path ".opencode/AGENTS.md"   # opencode 的入口
Test-Path ".codex/AGENTS.md"      # codex 的入口
Test-Path ".claude/CLAUDE.md"     # claude 的入口
```

如果**全部不存在** → 运行安装脚本：
```powershell
powershell -NoProfile -ExecutionPolicy Bypass -File .agents\scripts\install.ps1 -Force
```

### 步骤 2：确认自身 agent 的符号链接目录

根据当前 agent 类型，检查对应的符号链接是否指向本文件：

| 当前 Agent | 检查路径 | 期望目标 |
| --- | --- | --- |
| opencode | `.opencode/AGENTS.md` | `../.agents/AGENTS.md` |
| codex | `.codex/AGENTS.md` | `../.agents/AGENTS.md` |
| claude | `.claude/CLAUDE.md` | `../.agents/AGENTS.md` |

### 步骤 3：根据任务类型读取对应的模块文档

在执行任何修改之前，**必须先阅读本文件相关内容 + 对应的模块文档**：

| 要改动的功能 | 必读文档 |
| --- | --- |
| Agent 循环 / 工具派发 / parts | `AGENTS.md` §1.1-1.3 + [`.agents/docs/agent.md`](docs/agent.md) |
| LLM 调用 / 协议适配 | [`.agents/docs/llm.md`](docs/llm.md) |
| HTTP API / SSE 流 | [`.agents/docs/server.md`](docs/server.md) |
| 工具注册 / 实现 | [`.agents/docs/tool.md`](docs/tool.md) |
| 子代理系统 | `.agents/AGENTS.md` §1.3 + [`.agents/docs/subagent.md`](docs/subagent.md) |
| 数据库 / 持久化 | [`.agents/docs/memory.md`](docs/memory.md) |
| 配置管理 | [`.agents/docs/config.md`](docs/config.md) |
| CLI 终端 | [`.agents/docs/cli.md`](docs/cli.md) |
| Vue 前端 / Pinia | [`.agents/docs/frontend.md`](docs/frontend.md) |
| 沙箱 / Skill / MCP 等 | [`.agents/docs/infrastructure.md`](docs/infrastructure.md) |
| 全模块索引 | [`.agents/docs/INDEX.md`](docs/INDEX.md) |
| 用户询问 agent 功能 / GUI 操作流程 | [`README.md`](README.md)「GUI 操作入口速查 / 常见问题」+ 对应模块文档 |

**模块文档位置**：`.agents/docs/` 目录下，每个模块一个 `.md` 文件。

---

## 项目概述

P-Chat 是为本地使用设计的 AI 编程助手，特色是**完全本地化运行**（无云端依赖）+ **结构化的消息流**（text + thinking + tool calls + sub-agents）+ **多 LLM 协议兼容**（OpenAI / Anthropic / 自定义代理）。

### 技术栈

| 层 | 选型 | 版本 |
| --- | --- | --- |
| 后端 | Go | 1.21.13 |
| LLM 客户端 | 自定义 SSE reader（绕过 `go-openai` SDK 的 `ReasoningContent` 缺失） | — |
| LLM SDK | `github.com/sashabaranov/go-openai` v1.30.0 | — |
| 桌面端 | Wails v2 | 2.12.0 |
| 前端框架 | Vue 3 + `<script setup>` + Naive UI | — |
| 构建工具 | Vite 5 | — |
| 运行时 | Node | 24.11 |
| 构建编排 | Taskfile (go-task) | — |

### 目录结构

```
D:\develop\project\P-chat\
├── AGENTS.md                   # 根目录入口（符号链接 → .agents/AGENTS.md）
├── .agents/                    # canonical agent 规范来源
│   ├── AGENTS.md               # ★ 本文件 — 所有工具的统一规范
│   ├── README.md               # .agents/ 目录说明
│   ├── docs/                   # ★ 模块文档（每个模块一个文件）
│   │   ├── INDEX.md            #   总索引："想改 X → 读哪个文档"
│   │   ├── agent.md            #   Agent ReAct 循环
│   │   ├── llm.md              #   LLM 客户端 + 协议适配
│   │   ├── server.md           #   HTTP API + SSE
│   │   ├── tool.md             #   工具注册 + 内置工具
│   │   ├── subagent.md         #   子代理系统
│   │   ├── memory.md           #   SQLite 持久化
│   │   ├── config.md           #   配置管理
│   │   ├── cli.md              #   CLI REPL
│   │   ├── frontend.md         #   Vue 3 前端
│   │   └── infrastructure.md   #   基础设施模块
│   └── scripts/
│       ├── install.ps1         # Windows 安装脚本
│       └── install.sh          # Unix 安装脚本
├── cmd/
│   ├── pchat/                  # CLI 入口
│   ├── pchat-server/           # 独立 HTTP server（API + web/）
│   └── pchat-gui/              # Wails 桌面应用
│       └── frontend/           # Vue 3 + Naive UI SPA
├── internal/                   # 业务包
│   ├── agent/                  # ReAct 主循环，工具调度
│   ├── llm/                    # 自定义 SSE reader + 双协议 (OpenAI/Anthropic)
│   ├── server/                 # HTTP 路由 + SSE 端点
│   ├── subagent/               # 子 agent 系统
│   ├── tool/                   # 工具注册表
│   ├── style/                  # 人格风格（cute/guofeng/tech）
│   ├── memory/                 # 会话记忆 (SQLite)
│   ├── config/                 # YAML + 持久化 provider/model 配置
│   ├── cli/                    # 终端 REPL + ChatUI
│   ├── sandbox/                # 命令/文件安全检查
│   ├── skill/                  # Skill 技能系统
│   ├── mcp/                    # MCP 服务器集成
│   ├── project/                # 项目目录注册
│   ├── agents/                 # AGENTS.md 加载器
│   ├── rules/                  # .rules/ 规则监听
│   ├── knowledge/              # RAG 知识检索
│   ├── recall/                 # 记忆召回
│   ├── paths/                  # ~/.p-chat 路径解析
│   ├── httpcli/                # CLI HTTP+SSE 客户端
│   └── serverproc/             # 服务器进程生命周期管理
├── prompts/                    # 人格 prompt 源文件
│   ├── identity/{cute,guofeng,tech}.md
│   └── soul/{cute,guofeng,tech}.md
├── configs/config.yaml          # 默认配置模板（用户运行时覆盖在 ~/.p-chat/）
├── scripts/                    # 平台无关工具脚本
├── web/                        # Vite 输出（gitignored）
├── bin/                        # 构建产物（gitignored）
└── Taskfile.yml                # task build:* / test:* / package:*
```

### 三个二进制

| 二进制 | 入口 | 用途 | 大小 |
| --- | --- | --- | --- |
| `pchat.exe` | `cmd/pchat` | CLI 终端模式 | ~20MB |
| `pchat-server.exe` | `cmd/pchat-server` | 独立 HTTP server + web UI | ~27MB |
| `pchat-gui.exe` | `cmd/pchat-gui` | Wails 桌面应用（内嵌 webview + spawn pchat-server 子进程） | ~10MB |

`pchat-gui` 启动时会 `exec.Command` 拉起 `pchat-server.exe` 作为子进程，通过 PCHAT_PORT 环境变量传动态端口。

---

## 1. 核心架构：消息流

### 1.1 前端 Message 数据模型

```ts
type MessagePart =
  | { kind: 'text'; text: string }
  | { kind: 'thinking'; text: string; streaming?: boolean }
  | { kind: 'tool'; name: string; args?: string; status: 'start'|'ok'|'warn'|'error'; result?: string; error?: string; elapsed?: string }
  | { kind: 'sub_agent'; task: string; status: 'start'|'ok'|'err'; parts: MessagePart[]; elapsed?: string }
```

### 1.2 完整数据流

```
用户输入 (Vue/CLI)
  → POST /api/v1/sessions/:id/messages  (server/handler.go:SendMessage)
    → agent.ChatWithTools()              (agent/agent.go)
      → LLM Stream (OpenAI/Anthropic)    (llm/*)
        → ChatStreamChunk channel (cap 64)
          → 工具派发: goroutine + per-tool eventCh (cap 64)
              → forwarder → partsAcc.update() → main ch
              → tool handler → subagent           (subagent/*)
          → chunkToEvent(ev) → type mapping      (server/handler.go)
          → SSE: "data: {...}\n\n"               (server/handler.go)
      → 前端 appendStreamEvent()                  (frontend/stores/chat.ts)
        → 路由到 parts (text/thinking/tool/sub_agent)
        → Vue 响应式 → DOM 渲染
    → memory.Store 持久化                          (memory/*)
```

### 1.3 SSE 协议

Server-Sent Events 端点 `POST /api/v1/sessions/:id/messages`。Event 类型（`internal/server/handler.go:1495-1613` `chunkToEvent()`）：

| `type` | 触发 | 关键字段 |
| --- | --- | --- |
| `content` | LLM 文本 delta | `content` |
| `thinking` | reasoning delta | `thinking` |
| `tool` | 工具调用生命周期 | `tool_name`, `tool_status`, `tool_result`, `tool_args`, `tool_elapsed` |
| `phase` | sub-agent lifecycle / system status | `phase`, `sub_agent_status` |
| `error` | LLM/transport error | `error`, `error_kind` |
| `done` | 流结束 | `tokens_in`, `tokens_out`, `elapsed` |
| `question` | LLM 向用户提问 | `question_json` |
| `tool_confirm` | 沙箱确认请求 | `tool_confirm_json` |

### 1.4 子 agent 系统

详见 [`.agents/docs/subagent.md`](docs/subagent.md)。

核心要点：
- 子 agent 通过 `task` 工具触发
- 子 agent 跑独立的 LLM stream，事件通过 per-tool eventCh 转发到父级
- **关键设计**：子 agent 的 `Done=true` chunk **不转发**到父主通道（防止触发 SSE 过早关闭）
- 关闭事件 `sub_agent_ok` / `sub_agent_err` 是子 agent 完成的唯一外部信号

### 1.5 LLM 客户端的容错性

`internal/llm/client.go` 的 `openaiStream` 故意**绕过 SDK 的 `CreateChatCompletionStream`**，自己跑 HTTP + SSE reader。理由：

1. SDK 的 `ChatCompletionStreamChoiceDelta` 没有 `ReasoningContent` 字段
2. 代理用了非标准字段名（`delta.text` / chatml `message.content` 包装）
3. 代理的 error envelope 和正常 content 相似 — 需要 `extractProxyError` 先于 walker 检测

parser 三层 fallback：
1. **Standard struct decode**：`choices[].delta.content` / `delta.text` / `delta.reasoning_content`
2. **extractProxyError**：顶层 `error` 字段 → APIError 路径
3. **Recursive walker**：递归搜索 JSON 树中的 content/thinking 候选字段

---

## 2. 编码规范

### 2.1 通用

- **响应语言**：与用户对话用中文，code comment 双语（中文 → 英文）
- **Commit message**：英文标题 + 详细中文 body
- **每个 PR/commit 都要**：单点改动 + 测试覆盖 + build 验证
- **永远不要**：kill 用户的 GUI 进程 / 改用户的 `~/.p-chat/config.yaml` / 写密钥到任何文件

### 2.2 Go

- 1.21.13，启用 generics，避免 `interface{}` 滥用
- 所有公开函数带 godoc 注释
- 测试放在同包的 `*_test.go`
- 错误处理：`fmt.Errorf("...: %w", err)` 包装；`errors.Is` 判断
- `internal/llm/client.go` SSE parser 是核心，改之前先跑测试

### 2.3 TypeScript / Vue

- 严格 TypeScript（`vue-tsc -b` 必须在 build 前通过）
- Vue 3 `<script setup lang="ts">` 单文件组件
- 状态管理：`src/stores/chat.ts`（Pinia）
- CSS：scoped + CSS variables（`--accent` / `--bg-2`），**禁止硬编码颜色**
- 提交前：`cd frontend && npx vue-tsc -b && npm run build`

### 2.4 命名

- Go: `camelCase` 私有，`PascalCase` 导出
- TS: `camelCase` 变量/函数，`PascalCase` 类型/组件

---

## 3. 构建与运行

### 3.1 完整构建

```powershell
task build:all        # 编译 pchat.exe + pchat-server.exe + 前端
task build:gui        # 额外：编译 pchat-gui.exe (Wails)
task package:gui      # 额外：完整 bundle (含 web/ 资源)
```

### 3.2 关键脚本

- `scripts/clean-frontend-output.ps1` — 清理旧 Vite 产物
- `scripts/sync-web.ps1` — 同步 `web/` → `cmd/pchat-server/web/`
- `scripts/package-gui.ps1` — web/assets/ merge 到 Wails 产物

### 3.3 测试

```powershell
go test -count=1 ./...                          # 所有 Go 测试
cd frontend
npx vue-tsc -b                                  # TS 类型检查
npm run build                                   # 前端 bundle
```

### 3.4 调试

| 问题 | 怎么查 |
| --- | --- |
| LLM 回复不显示 | `~/.p-chat/server-debug.log` |
| 前端路由错 | `MessageBubble.vue` `parts` 数组 |
| Wails 启动失败 | `bin/pchat-server.log` |
| Anthropic 协议不工作 | `internal/llm/anthropic.go:130-260` |

---

## 4. 已知问题

### 4.1 api-convert.08ms.cn 代理

- `cs` provider 走 `http://api-convert.08ms.cn/v1`
- **2026-07-12 前**：月度 quota 已用完
- `doubao-seed-2.0-lite` 路由被屏蔽
- **这不是 P-Chat 的 bug**

### 4.2 LLM 合成错误

LLM 在工具失败时会合成 `ERROR: ... Inform the user.` 伪错误消息。已在 prompt 强化。
**不要**在源码里加字符串替换。

### 4.3 pchat-gui 不会自动重启 pchat-server

**改 pchat-server binary 后必须重启 pchat-gui**。

### 4.4 Windows 符号链接

`.agents/install.ps1` 需要 Windows 开发者模式或管理员权限。失败时 fallback 到 copy。

---

## 5. 关键文件位置速查

| 想改 | 看 |
| --- | --- |
| ReAct 主循环 | `internal/agent/agent.go:900-1510` `ChatWithTools()` |
| 工具派发 + forwarder | `internal/agent/agent.go:1150-1471` |
| parts 累加器 | `internal/agent/parts.go` |
| 流式事件分发 | `frontend/src/stores/chat.ts:828-1103` `appendStreamEvent()` |
| 后端 SSE 事件映射 | `internal/server/handler.go:1495-1613` `chunkToEvent()` |
| 子 agent runner | `internal/subagent/subagent.go:511-832` `Run()` |
| 子 agent 事件转发 | `internal/subagent/subagent.go:837-842` `tryForward()` |
| OpenAI SSE parser | `internal/llm/client.go:235-440` |
| Anthropic SSE parser | `internal/llm/anthropic.go:130-260` |
| 系统 prompt 拼装 | `internal/agent/agent.go:820-876` |
| 配置加载 | `internal/config/config.go` |
| 数据库 CRUD | `internal/memory/memory.go` |

---

## 6. `.agents/` 目录约定

为了**让多个 agent 工具（opencode / codex / claude 等）共享同一份规范**，本项目用 `.agents/` 目录作为**canonical source**。

### 目录级符号链接

安装脚本创建**目录级**符号链接（Windows Junction），而非逐个文件链接：

```
.opencode  ──junction──>  .agents/    (opencode 读 .opencode/AGENTS.md)
.codex     ──junction──>  .agents/    (codex 读 .codex/AGENTS.md)
.claude    ──junction──>  .agents/    (claude 读 .claude/CLAUDE.md)
```

目录级的好处：`.agents/` 下的所有子内容（`docs/`、`scripts/`）自动可见。

```powershell
# 验证：以下路径应解析到同一文件
.opencode/AGENTS.md   → .agents/AGENTS.md  ✓
.codex/AGENTS.md      → .agents/AGENTS.md  ✓
.claude/CLAUDE.md     → .agents/CLAUDE.md ──copy──> AGENTS.md  ✓
.opencode/docs/       → .agents/docs/     ✓
```

### Agent 启动时的符号链接检测

Agent 启动检查自身入口是否存在并指向正确目标：

1. 检查目录是否存在且为符号链接：
   ```powershell
   (Get-Item ".opencode" -Force).Attributes -band [IO.FileAttributes]::ReparsePoint
   ```
2. 若不存在或为普通目录 → 运行 install 脚本。

### 安装（一次性）

```powershell
# Windows（PowerShell 5.1+）
powershell -NoProfile -ExecutionPolicy Bypass -File .agents\scripts\install.ps1

# Unix
bash .agents/scripts/install.sh
```

### .p-chat 的特殊处理

`.p-chat/` 是项目级运行时配置目录（含 `config.yaml`、`memory/` 等），保留为普通目录。安装脚本仅将 `AGENTS.md` 同步到 `.p-chat/AGENTS.md`。

### Windows Junction 与权限

Windows Junction（`mklink /J`）不需要管理员权限即可创建，是本项目推荐的符号链接方式。脚本三级回退：
1. `mklink /J` — Junction（绝对路径，无需管理员）
2. `mklink /D` — 目录符号链接（需管理员或开发者模式）
3. `Copy-Item -Recurse` — 文件副本（最后兜底）

修改规范只需改 `.agents/AGENTS.md`，所有工具通过符号链接自动读取最新内容。
