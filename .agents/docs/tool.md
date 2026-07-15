# Tool 模块

> **位置**：`internal/tool/`  
> **依赖**：sandbox（接口）、paths  
> **被依赖**：agent, subagent, server

## 概述

Tool 模块定义 P-Chat 的工具注册表和所有内置工具的实现。工具是 LLM 可以调用的函数：执行命令、读写文件、列目录、阅读文档（PDF/DOCX）、待办管理（todo_write）、用户提问（question）。

## 文件结构

| 文件 | 职责 | 关键函数 |
|---|---|---|
| `registry.go` | 工具注册表 + 所有内置工具定义和处理函数 | `RegisterBuiltin()`, `Tool`, `CallResult`, `SandboxChecker` |
| `websearch.go` | `web_search` 工具注册 + handler | `RegisterWebSearch()`, `handleWebSearch()` |
| `todo.go` | todo_write 工具的持久化钩子 | `PersistTodos`, `LoadTodos` |
| `question.go` | question 工具（暂停→等待用户回答→恢复） | `handleQuestion()` |
| `confirm.go` | 沙箱确认机制（阻塞等待用户批准） | `ConfirmRequest`, `WaitForConfirm()` |
| `fileops.go` | read_file/write_file 的底层实现 | `readFileForTool()`, `writeFile()` |
| `docx.go` | .docx 读取实现 | `readDocx()` |
| `pdf.go` | .pdf 读取实现 | `readPdf()` |

## 核心概念

### 1. 工具注册表 (Registry)

```go
type Registry struct {
    tools map[string]ToolHandler  // 名称 → 处理函数
    meta  map[string]Tool         // 名称 → 元数据（名称、描述、参数 schema）
}

type ToolHandler func(ctx context.Context, args json.RawMessage) (*CallResult, error)
```

- `Register(Tool, ToolHandler)` — 注册工具
- `Get(name)` — 获取处理函数
- `List()` — 按名称排序的所有工具元数据
- `Names()` — 所有工具名称（用于子代理工具白名单）

### 2. 内置工具列表

| 工具名称 | 功能 | 关键文件：行号 |
|---|---|---|
| `exec_command` | 执行 shell 命令 | registry.go:201, 303 |
| `read_file` | 读取文本文件 | registry.go:211, 390 |
| `write_file` | 写入/创建文件 | registry.go:223, 440 |
| `list_files` | 列出目录 | registry.go:248, 467 |
| `read_docx` | 读取 .docx | registry.go:232, 540 |
| `read_pdf` | 读取 .pdf | registry.go:240, 555 |
| `web_fetch` | HTTP 抓取 URL（带 SSRF 防护） | registry.go:275, 749 |
| `web_search` | 公开网络搜索（snippet+url，可插拔 provider） | websearch.go |
| `todo_write` | 管理待办列表 | registry.go:256, todo.go |
| `question` | 向用户提问并等待 | registry.go:275, question.go |

### 3. 沙箱集成

工具通过 context 接收 `SandboxChecker` 接口：

```go
type SandboxChecker interface {
    CheckExecBool(command string) bool
    CheckWriteBool(path string) bool
    CheckExecDecision(command string) SandboxDecision
    CheckWriteDecision(path string) SandboxDecision
}
```

- `exec_command` 在执行前检查命令是否允许（CheckExecBool）
- `write_file` 在写入前检查路径是否允许（CheckWriteBool）
- 可通过 `/unsafe once` 或设置 `permission_level: "full"` 绕过

### 4. 确认机制 (Confirm)

`WaitForConfirm()` (`confirm.go`) 用于沙箱"需要确认"的场景：
1. 将 ConfirmRequest 通过 eventCh 发送给前端
2. 阻塞等待前端 POST /confirm-response
3. 返回批准/拒绝

### 5. Question 工具 (暂停-恢复)

`handleQuestion()` (`question.go`) 实现异步提问流程：
1. 将问题 JSON 通过 eventCh 发送给前端
2. 前端弹出 QuestionModal
3. 用户回答 → POST /question-response
4. 服务端将答案 `borrow` 到阻塞的 handler → 工具返回结果 → LLM 继续

### 6. Todo 持久化

`todo_write` 的工具结果通过 `PersistTodos` 持久化到 SQLite。`GET /sessions/:id/todos` 可在服务器重启后重新加载。

### 7. dry_run 模式 (P2-4, 2026-07-15)

`exec_command` / `read_file` / `write_file` 三个工具的 schema 加了
`dry_run: bool` 字段（默认 false）。当设为 true：

- `exec_command`：跳过 `exec.CommandContext`，返回
  `[dry-run] would execute: <cmd>` 预览（仍走 sandbox + upload-dir 检查）
- `read_file`：`os.Stat` 拿 size / mode / modified，附 200 字节 head
  预览（文件 > 1MB 跳过 head），不调用 `readFileForTool`
- `write_file`：报 content 大小 + 现有文件 (if any) 状态 + 200 字节
  head，不调用 `writeFile`

**安全**：dry_run 走相同 sandbox / upload-dir 校验，无法 bypass。
LLM 调 tool 必然经过 handler，前端直接调也受 sandbox 保护。

**触发方式**（v1）：
- LLM 自加 `dry_run: true`（用户 prompt "干跑 X"）
- 不在 `ToolConfirmModal` 加"先干跑"按钮（避免新增 server 端点 / 状态机）

**前端展示**：`ToolCallCard` header 检测 `args.dry_run === true` 显示
`dry-run` pill chip（brand-50 背景），折叠态可见。

## 修改指南

### 要添加新工具
1. 在 `RegisterBuiltin()` (registry.go) 中注册 Tool 元数据 + 处理函数
2. 实现处理函数（同文件或新文件）
3. 更新 AGENTS.md 中的工具说明

### `web_search` 的特殊注册模式
`web_search` **不**在 `RegisterBuiltin()` 中无条件注册 — 它依赖外部 API key。
调用方 (server bootstrap) 在 `RegisterBuiltin()` 之后单独调 `RegisterWebSearch(r, cfg.Search)`。
当 `cfg.Search.Enabled == false` 或缺 key/base_url 时该函数**不注册任何东西**，
LLM 看不到这个工具（避免「call → fail」浪费 turn）。
backend 由 `internal/search` 提供：可插拔 Provider 接口，默认 Tavily，支持任意 OpenAI 兼容 endpoint。

### 要修改现有工具行为
- 找到对应的处理函数（如 `handleReadFile`）
- 工具元数据（名称、描述、参数）在 `RegisterBuiltin()` 中

### 要修改沙箱检查
- [infrastructure.md](infrastructure.md#沙箱-sandbox) 中的 sandbox 模块

### 要修改 Question 流程
- `question.go` 中的 `handleQuestion()` — 服务端阻塞逻辑
- 前端 `chat.ts` 中的 `question` case — 前端处理
- `QuestionModal.vue` — 前端 UI

## 相关模块

- [agent.md](agent.md) — 工具由 ReAct 循环调用
- [subagent.md](subagent.md) — task 工具由 subagent 模块提供
- [infrastructure.md](infrastructure.md) — sandbox 模块
