# Project Agent Instructions

> **P-Chat** — Go + Wails 桌面端 LLM 聊天应用，配套 CLI / 独立 server。
>
> 本文档是 agent 协作的唯一规范来源。各 agent 工具（opencode / codex / claude 等）通过 `.agents/install` 脚本自动建立符号链接，统一读此文件。

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
├── AGENTS.md                   # 本文件（opencode 默认读这里）
├── .agents/                    # 各 agent 工具的规范来源（见下面 §7）
│   ├── AGENTS.md               # canonical，符号链接指向
│   ├── README.md
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
│   ├── memory/                 # 会话记忆
│   ├── config/                 # YAML + 持久化 provider/model 配置
│   └── ...
├── prompts/                    # 人格 prompt 源文件
│   ├── identity/{cute,guofeng,tech}.md
│   └── soul/{cute,guofeng,tech}.md
├── configs/config.yaml          # 默认配置模板（用户运行时覆盖在 ~/.p-chat/）
├── scripts/                    # 平台无关工具脚本
├── web/                        # Vite 输出（gitignored；pchat-server 通过 //go:embed 同步到 cmd/pchat-server/web/）
├── bin/                        # 构建产物（gitignored）
└── Taskfile.yml                # task build:* / test:* / package:*
```

### 三个二进制

| 二进制 | 入口 | 用途 | 大小 |
| --- | --- | --- | --- |
| `pchat.exe` | `cmd/pchat` | CLI 终端模式 | ~20MB |
| `pchat-server.exe` | `cmd/pchat-server` | 独立 HTTP server + web UI | ~27MB |
| `pchat-gui.exe` | `cmd/pchat-gui` | Wails 桌面应用（内嵌 webview + spawn pchat-server 子进程） | ~10MB |

`pchat-gui` 启动时会 `exec.Command` 拉起 `pchat-server.exe` 作为子进程，通过 PCHAT_PORT 环境变量传动态端口；父进程通过 `/api/v1/health` 轮询待 server 就绪后才显示窗口。

---

## 1. 核心架构：消息流

### 1.1 前端 Message 数据模型

`Message` 不再是单一 `content` 字符串，而是**结构化 parts 数组**：

```ts
type MessagePart =
  | { kind: 'text'; text: string }
  | { kind: 'thinking'; text: string; streaming?: boolean }
  | { kind: 'tool'; name: string; args?: string; status: 'start'|'ok'|'warn'|'error'; result?: string; error?: string; elapsed?: string }
  | { kind: 'sub_agent'; task: string; status: 'start'|'ok'|'err'; parts: MessagePart[]; elapsed?: string }
```

用户/系统消息仍然是单一 `content` 字符串（不含 parts）。Assistant 消息 `parts` 数组按到达顺序组装：text / thinking / tool calls / sub-agents 可交错。

### 1.2 SSE 协议

Server-Sent Events 端点 `POST /api/v1/sessions/:id/messages`。Event 类型（`internal/server/handler.go:1048-1115`）：

| `type` | 触发 | 关键字段 |
| --- | --- | --- |
| `content` | LLM 文本 delta | `content` |
| `thinking` | reasoning delta（DeepSeek `reasoning_content` / OAI `reasoning` / Anthropic `thinking_delta`） | `thinking` |
| `tool` | 工具调用生命周期 | `tool_name`, `tool_status`, `tool_result`, `tool_args`, `tool_elapsed` |
| `phase` | heartbeat / system / memory / plan / sub-agent lifecycle | `phase`, `step`, `message` |
| `error` | LLM/transport error | `error` |
| `done` | 流结束 | `tokens_in`, `tokens_out`, `elapsed`, `provider`, `model` |

每个 event 都带 `provider` + `model`，UI 不需要会话级缓存。

### 1.3 子 agent

`internal/subagent/subagent.go` 通过 `task` 工具触发。子 agent 跑独立的 LLM stream，**所有 chunk 通过父的 `agent.GetToolEventChan(ctx)` 转发**（标 `SubAgent=true` + `SubAgentTask=<description>`）。UI 用 task 描述作为唯一 key 路由到嵌套卡片。

子 agent lifecycle：
- `sub_agent_start` 事件 → 父 UI 打开嵌套卡片
- 期间子 agent 自己的 content/thinking/tool events 流转到嵌套卡片的 `parts`
- `sub_agent_ok` / `sub_agent_err` → 父 UI 关闭卡片 + 标记状态

### 1.4 LLM 客户端的容错性

`internal/llm/client.go` 的 `openaiStream` 故意**绕过 SDK 的 `CreateChatCompletionStream`**，自己跑 HTTP + SSE reader。理由：

1. SDK 的 `ChatCompletionStreamChoiceDelta` 没有 `ReasoningContent` 字段 — DeepSeek/O1 的 reasoning 会被静默丢弃
2. 用户的代理（`api-convert.08ms.cn`）用了非标准字段名（早期测试时还用过 `delta.text` / chatml `message.content` 包装）
3. 代理的 error envelope（`{"error": {"message": "..."}}`）和正常 content 看起来很像 — 需要 `extractProxyError` 先于 walker 检测

parser 三层 fallback（关键！）：
1. **Standard struct decode**：`choices[].delta.content` / `delta.text` / `delta.reasoning_content` / `delta.reasoning`
2. **extractProxyError**：顶层 `error` 字段 → 走 APIError 路径（不要走 walker）
3. **Recursive walker**：当 1+2 都空时，在原始 JSON 树里搜 `content` / `text` / `output_text` / `reasoning_content` / `reasoning` / `thoughts` 等候选字段名

walker **故意排除 `message` 字段名**，因为 `error.message` 容易冲突。

---

## 2. 编码规范

### 2.1 通用

- **响应语言**：与用户对话用中文，code comment 用英文（项目内已有大量英文注释，保持一致）
- **Commit message**：英文标题 + 详细中文 body（参考 `git log --oneline -10` 风格）
- **每个 PR/commit 都要**：单点改动 + 测试覆盖 + build 验证
- **永远不要**：kill 用户的 GUI 进程 / 改用户的 `~/.p-chat/config.yaml` / 写密钥到任何文件

### 2.2 Go

- 1.21.13，启用 generics，避免 `interface{}` 滥用
- 所有公开函数带 godoc 注释（中文或英文均可）
- 测试放在同包的 `*_test.go`，用标准 `testing` 包
- 错误处理：`fmt.Errorf("...: %w", err)` 包装；用 `errors.Is` 判断
- `internal/llm/client.go:374-450` 是 SSE parser 的核心，改之前先跑 `go test -count=1 -v -run TestOpenAIStream ./internal/llm/...`

### 2.3 TypeScript / Vue

- 严格 TypeScript（`vue-tsc -b` 必须在 build 前通过）
- Vue 3 `<script setup lang="ts">` 单文件组件
- 状态管理：单文件 `src/stores/chat.ts`（无 Pinia，避免额外依赖）
- 组件拆分原则：
  - `MessageBubble.vue` 只负责"按 part 类型分发渲染"
  - 复杂 part 各自一个组件（`ThinkingBlock.vue` / `ToolCallCard.vue` / `SubAgentCard.vue` / `LoadingDots.vue`）
- CSS：scoped + CSS variables（`--accent` / `--bg-2` / 等），**禁止硬编码颜色**。新组件要切 light/dark 主题时，用 var(--xxx) 而不是 #fff/#333
- 提交前：`cd cmd/pchat-gui/frontend && npx vue-tsc -b && npm run build`

### 2.4 命名

- Go: `camelCase` 私有，`PascalCase` 导出
- TS: `camelCase` 变量/函数，`PascalCase` 类型/组件
- 事件 / 字段名遵循 OpenAI 兼容标准（`content` / `reasoning_content`），不要用 `text` 之类

---

## 3. 构建与运行

### 3.1 完整构建

```powershell
task build:all        # 编译 pchat.exe + pchat-server.exe + 前端
task build:gui        # 额外：编译 pchat-gui.exe (Wails)
task package:gui      # 额外：完整 bundle (含 web/ 资源)
```

子任务：
- `task build:frontend` — 单独构建 Vite SPA
  - 第一步：`clean-frontend-output.ps1` 清理 `web/index.html` + `web/assets/`
  - 第二步：`npm install`
  - 第三步：`npm run build`（Vite 输出到 `web/`）
  - **第四步**（重要）：`sync-web.ps1` 把 `web/` 同步到 `cmd/pchat-server/web/`，让 `//go:embed all:web` 能找到文件
- `task build:all` 仅依赖 `build:frontend`（不并发跑 `sync:web`，否则 race）

### 3.2 关键脚本

- `scripts/clean-frontend-output.ps1` — 清理旧 Vite 产物
- `scripts/sync-web.ps1` — 同步 `web/` → `cmd/pchat-server/web/`
- `scripts/package-gui.ps1` — 把 `web/assets/` clean-merge 到 Wails 产物里

### 3.3 测试

```powershell
go test -count=1 ./...                          # 所有 Go 测试
go test -count=1 -v -run TestOpenAIStream \
  ./internal/llm/...                            # SSE parser 单元测试
cd cmd/pchat-gui/frontend
npx vue-tsc -b                                  # TS 类型检查
npm run build                                   # 前端 bundle
```

### 3.4 调试

| 问题 | 怎么查 |
| --- | --- |
| LLM 回复不显示 | `~/.p-chat/server-debug.log` 看原始 SSE chunks + walker 命中日志 |
| 前端路由错 | `MessageBubble.vue` `parts` 数组是否填充 |
| Wails 启动失败 | `bin/pchat-server.log`（pchat-gui 内部启动 server 时的输出） |
| Anthropic 协议不工作 | `internal/llm/anthropic.go:130-260` SSE event handler |

---

## 4. 已知问题 & 外部依赖

### 4.1 api-convert.08ms.cn 代理

- 用户的 `cs` provider 走 `http://api-convert.08ms.cn/v1` 这个反向代理
- **2026-07-12 前**：月度 quota 已用完，会返回 `AccountQuotaExceeded` 错误
- 同时 `doubao-seed-2.0-lite` 路由被屏蔽：`No available route for model because all candidates are temporarily blocked`
- **这不是 P-Chat 的 bug**，是上游代理的状态

修复路径：等 quota 重置 / 联系代理 / 切换到其他 provider（`ollama` 是本地备选）

### 4.2 LLM "ERROR: Cannot read image.png..." 合成

LLM 在 `read_file` 工具失败于图片时，会**合成**一个 `ERROR: ... Inform the user.` 的伪错误消息（来源：`prompts/soul/tech.md` 的 `E_FS_NOT_FOUND` 格式）。这不是源码里的字符串。

**已在 prompt 强化**（`internal/agent/agent.go`）：
- 明确禁止对上传图片调 `read_file`
- 明确禁止向用户转述 `read_file` 错误
- 明确禁止伪造 `ERROR: ... Inform the user.` 字串

如果 LLM 仍输出，说明 prompt 还不够明确。**不要**在源码里加字符串替换 — 那会污染 UX。

### 4.3 pchat-gui 不会自动重启 pchat-server

`pchat-gui` spawn `pchat-server` 子进程但不会 respawn（`cmd/pchat-gui/main.go:432` 只 log exit）。**改 pchat-server binary 后必须重启 pchat-gui**。`task build:gui` 不会自动重启 GUI。

### 4.4 Windows 符号链接

`.agents/install.ps1` 创建的符号链接需要 Windows 开发者模式或管理员权限。如果失败，脚本 fallback 到 copy。

---

## 5. 当前进度（最近 commit）

```
0993b7d fix(llm): don't surface proxy error.message as content
6593f23 fix(llm): recursive walker for unknown proxy field names
7661068 fix(llm): tolerant SSE parser + raw-chunk debug log
d0d59de fix(chat): default model picker + new-session meta hydration
d000bb4 feat(chat): loading dots, tool calls, thinking, sub-agent cards
abcbab2 fix(ui): flat model dropdown, full provider echo via rich endpoint
2b33168 fix(ui): model dropdown stacked, provider/model edit echo, read_file prompt
37b1d63 fix(build): inline sync-web into build:frontend to avoid parallel-race
3fb63e8 feat(gui): Vue 3 + Vite + Nails UI SPA
```

| Commit | 关键 |
| --- | --- |
| `3fb63e8` | Vue 3 + Vite + Naive UI 替换原生 webview（结构性升级） |
| `37b1d63` | `sync-web` 移入 `build:frontend` 避免 race |
| `2b33168` | 模型选择回显 / 折叠块错误 / `read_file` prompt 强化 |
| `abcbab2` | 模型下拉框改为单行 + provider 编辑回显 |
| `d000bb4` | **重大功能**：loading dots / 工具调用卡片 / thinking 折叠块 / sub-agent 嵌套卡片 |
| `d0d59de` | 默认模型 picker + 新会话 meta hydration |
| `7661068` | SSE parser 兼容 `delta.text` + 原始 chunk log |
| `6593f23` | **Walker 终极 fallback**：递归搜 JSON 树找 content |
| `0993b7d` | **关键修复**：walker 不会把 `error.message` 当成 LLM 回复 |

---

## 6. 关键文件位置速查

| 想改 | 看 |
| --- | --- |
| 助手消息渲染 | `cmd/pchat-gui/frontend/src/components/MessageBubble.vue` |
| 工具调用卡片 | `ToolCallCard.vue` |
| 思考折叠块 | `ThinkingBlock.vue` |
| 子 agent 卡片 | `SubAgentCard.vue` |
| 流式事件分发 | `cmd/pchat-gui/frontend/src/stores/chat.ts` `appendStreamEvent()` |
| 后端 SSE 事件类型 | `internal/server/handler.go:1048-1115` `chunkToEvent()` |
| 流式 ChatStreamChunk 字段 | `internal/agent/agent.go:173-198` |
| OpenAI SSE parser | `internal/llm/client.go:235-440` |
| Anthropic SSE parser | `internal/llm/anthropic.go:130-260` |
| 子 agent 转发逻辑 | `internal/subagent/subagent.go:240-450` |
| 默认 provider / model 解析 | `internal/server/handler.go:1117-1147` `sessionToResponse()` |
| ReAct 主循环 | `internal/agent/agent.go:399-816` |
| 系统 prompt 拼装 | `internal/agent/agent.go:209-280` `buildStaticSystemPrompt()` |
| 设计 tokens (light/dark) | `cmd/pchat-gui/frontend/src/style.css` |
| Taskfile | `Taskfile.yml` |

---

## 7. `.agents/` 目录约定

为了**让多个 agent 工具（opencode / codex / claude 等）共享同一份规范**，本项目用 `.agents/` 目录作为**canonical source**：

```
.agents/
├── AGENTS.md           # canonical agent 规范（与根 AGENTS.md 同源）
├── README.md           # 本约定说明
└── scripts/
    ├── install.ps1     # Windows：建立符号链接
    └── install.sh      # Unix：建立符号链接
```

### 安装（一次性）

```powershell
# Windows（PowerShell 5.1+）
powershell -NoProfile -ExecutionPolicy Bypass -File .agents\scripts\install.ps1

# Unix
bash .agents/scripts/install.sh
```

执行后会自动建立：

| 符号链接 | 目标 |
| --- | --- |
| `.opencode/AGENTS.md` | `./.agents/AGENTS.md` |
| `.codex/AGENTS.md` | `./.agents/AGENTS.md` |
| `.claude/CLAUDE.md` | `./.agents/AGENTS.md` |
| 根 `AGENTS.md` | `./.agents/AGENTS.md`（如果当前根文件是个 stub） |

之后无论用哪个 agent 工具，都会读到同一份规范。**修改规范只需改 `.agents/AGENTS.md`**。

### Windows 符号链接权限

Windows 创建符号链接需要：
- 管理员权限，**或**
- 开发者模式（Settings → Privacy & security → For developers → Developer Mode）

脚本会先尝试 `New-Item -ItemType SymbolicLink`；失败时 fallback 到 `Copy-Item`（不再是符号链接，修改时需要重新跑 install）。
