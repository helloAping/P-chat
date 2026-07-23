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
| `dynamic/` | P3-2 用户自定义工具（YAML hot-reload） | `Watch()`, `BuildDynamicHandler()`, `ParseSpec()` |

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

### 8. 动态工具 (P3-2, 2026-07-16)

用户在 `~/.p-chat/tools/*.yaml` 写工具定义，5s polling watcher
自动 register，无需重启 server。详见 [P3-2 设计](../../docs/plans/round4-trace-and-extensibility-plan.md)。

**YAML v1 格式**：

```yaml
name: greet                    # 唯一名
description: "向用户问好"      # LLM 看到的描述
parameters:                   # JSON Schema 对象（可选）
  type: object
  properties:
    name: { type: string }
  required: [name]
template:
  type: exec | http | echo     # 三种 template
  # exec:
  command: "echo Hello, {{.args.name}}!"
  timeout: 5s
  # http:
  method: POST
  url: "https://api.example.com/{{.args.endpoint}}"
  headers: { X-Key: "{{.config.api_key}}" }
  body: '{"q": "{{.args.q}}"}'
  # echo (smoke test):
  text: "called greet with name={{.args.name}}"
sandbox:
  exec: allow | deny | confirm # 默认 confirm (fail-safe)
  read: allow | deny | confirm
  write: allow | deny | confirm
```

**模板渲染**：`text/template`，支持 `{{.args.foo}}` 和 `{{.config.bar}}`。
`.config` 从 `~/.p-chat/config.json` 的 `dynamic.<name>.config` 读。
`missingkey=zero` 让缺字段渲染 `<no value>` 不报错。

**Sandbox 决策**：`internal/agent/confirm_target.go` default 分支
走 `decisionFromSandbox(spec.Sandbox.Exec, toolName)` 决定
`SandboxDecision`：

- `allow` → `SandboxAllow`（直接执行）
- `deny` → `SandboxBlock`（返回 E_SANDBOX 错误）
- `confirm` → `SandboxConfirm`（弹确认 modal）

**Watcher / 项目级目录行为**：
- 全局 `~/.p-chat/tools/*.yaml` 仍由 5s polling watcher 热加载。
- 当前会话绑定项目时，`<project>/.p-chat/tools/*.yaml` 会按需扫描并合成到该会话工具视图。
- 项目工具同名覆盖全局动态工具；离开项目或切换到其他项目时不会泄漏。
- 文件删除以 scope snapshot 替换实现，不用裸 `Unregister(name)` 删除同名全局工具。
- 单个 YAML 解析失败 → log warn + 跳过（不影响其他工具），同时写入 dynamic diagnostics 快照供 GUI 展示。

**Process-global spec table**：
`dynamic.SetSpecsForRoot(scope, projectRoot, all)` 按作用域发布 spec 快照；
`dynamic.LookupSpecForRoot(name, projectRoot)` 供 `confirm_target.go` 读 spec.Sandbox.Exec，解析顺序为项目 → 全局。
兼容 wrapper `SetSpecs` / `LookupSpec` 表示全局视图。

**API 暴露**：
- `GET /api/v1/tools` 返回全局视图；`GET /api/v1/tools?session_id=<id>` 返回该会话项目合成视图。
- `tools[]` 含 `dynamic` flag、`scope` (`builtin|global|project`)、`source` YAML 路径和可选 `project_root`，前端 `ToolListDrawer` 渲染为内置 / 全局自定义 / 项目自定义。
- `diagnostics[]` 含每个动态 YAML 的 `source`、`scope`、`project_root`、`status`、`error`、`mod_at`，解析失败时在工具列表抽屉顶部展示。
- `POST /api/v1/tools/:name/trial` 支持同样的 `?session_id=<id>`，确保试运行的是 GUI 当前看到的项目覆盖版本。body 为 `{ arguments, dry_run }`；`dry_run=true` 调 `dynamic.Preview` 渲染命令 / URL / echo 文本但不执行；`dry_run=false` 调当前 registry handler，返回 `{ status, result, elapsed }` 给 GUI 最近结果面板。

**限制（v1）**：
- 无 trust-level / signature 校验（用户自管 `~/.p-chat/`）
- 项目级工具目录只在会话绑定项目时按需扫描，暂不做独立项目 watcher。
- 当前校验覆盖必填字段、template 类型、template 必要字段、sandbox 枚举、parameters 顶层对象；GUI 参数表单覆盖 string / number / boolean / enum 的基础输入，尚未做完整 JSON Schema 语义校验或复杂对象参数表单生成。

## 相关模块

- [agent.md](agent.md) — 工具由 ReAct 循环调用
- [subagent.md](subagent.md) — task 工具由 subagent 模块提供
- [infrastructure.md](infrastructure.md) — sandbox 模块
