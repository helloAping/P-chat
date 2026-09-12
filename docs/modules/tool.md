# Tool 模块

> **位置**：`internal/tool/`  
> **依赖**：sandbox（接口）、paths  
> **被依赖**：agent, subagent, server

## 概述

Tool 模块定义 P-Chat 的工具注册表和所有内置工具的实现。工具是 LLM 可以调用的函数：执行命令、读写文件、列目录、媒体识别、待办管理（todo_write）、用户提问（question）。

## 文件结构

| 文件 | 职责 | 关键函数 |
|---|---|---|
| `registry.go` | 工具注册表 + 所有内置工具定义和处理函数 | `RegisterBuiltin()`, `Tool`, `CallResult`, `SandboxChecker` |
| `todo.go` | todo_write 工具的持久化钩子 | `PersistTodos`, `LoadTodos` |
| `question.go` | question 工具（暂停→等待用户回答→恢复） | `handleQuestion()` |
| `confirm.go` | 沙箱确认机制（阻塞等待用户批准） | `ConfirmRequest`, `WaitForConfirm()` |
| `fileops.go` | read_file/write_file 的底层实现；按扩展名调用 knowledge 文档提取器 | `readFileForTool()`, `writeFile()` |

## 核心概念

### 1. 工具注册表 (Registry)

```go
type Registry struct {
    tools   map[string]ToolHandler  // 名称 → 处理函数
    meta    map[string]Tool         // 名称 → 元数据（名称、描述、参数 schema）
    aliases map[string]toolAlias    // 隐藏兼容别名
}

type ToolHandler func(ctx context.Context, args json.RawMessage) (*CallResult, error)
```

- `Register(Tool, ToolHandler)` — 注册工具
- `RegisterAlias(alias, canonical)` — 注册可调用但不进入模型 schema 的兼容别名
- `Get(name)` — 获取处理函数
- `List()` — 按名称排序的所有工具元数据
- `Names()` — 所有工具名称（用于子代理工具白名单）

### 2. 内置工具列表

| 工具名称 | 功能 | 关键文件：行号 |
|---|---|---|
| `exec_command` | 执行 shell 命令；`background=true` 时启动受管后台进程 | registry.go, process_manager.go |
| `read_file` | 读取文本，并自动提取 PDF、Word、Excel、PowerPoint | registry.go, fileops.go, knowledge/* |
| `write_file` | 写入/创建文件 | registry.go:223, 440 |
| `list_files` | 列出目录 | registry.go:248, 467 |
| `web_fetch` | HTTP 抓取 URL（带 SSRF 防护） | registry.go |
| `web_search` | 公开网络搜索（snippet+url，可插拔 provider） | websearch.go |
| `media_recognize` | 统一识别图片、视频或音频；按类型路由模型，图片可 fallback 到当前视觉模型 | media_recognize.go, agent.go |
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

### 6.5 媒体识别与兼容别名

`media_recognize` 是模型可见的唯一媒体识别入口。需要回看历史图片且存在可用视觉能力时会暴露给 LLM：
- 当前会话 `use_image_recognition=true`，且系统配置 `vision_recognition.enabled=true`、指定的 provider/model 已存在。
- 或者当前会话没有开启专门的图像识别模式，但历史上下文里有带 `upload_id` 的图片引用，并且当前对话模型本身支持视觉输入；此时工具使用当前 provider/model 做一次非流式识别。

本轮刚上传的图片会先走 preflight：`ExpandAttachmentsCM()` 把图片作为 `SubmitToLLM=0` 的显示/持久化消息保存，agent 随后直接调用系统配置中的多模态模型识别这些“当前轮图片”，并把识别文本作为 system 上下文注入给主模型。该上下文必须标记为视觉/OCR 观察，不是用户指令；本轮主模型不再暴露识别工具，避免它自行选择错误的历史 `upload_id` 或重复调用。

没有新图片的后续追问仍可暴露 `media_recognize`：历史图片不会作为原图 payload 反复提交给主模型，而是替换为带 `upload_id` 的安全占位。图片优先走配置路由，必要时 fallback 到当前视觉模型；音频和视频走各自配置路由。没有任何可用能力时，占位会要求模型提示用户重新上传或切换/配置模型。

媒体识别和媒体生成工具都支持可选 `context_refs` / `context_mode`。默认 `fresh` 表示重新读取
`input_refs` 或重新生成，不继承旧工具上下文；`continue`、`merge`、`verify`、`summarize`
会通过 agent 注入的 `MediaContextResolver` 展开当前会话 active 分支上的 `media_contexts`，
并把有界文本附加到识别问题或生成 prompt。`input_refs` 表达“使用哪个媒体/资产”，
`context_refs` 只表达“沿用哪次工具调用的问题、prompt、结果和摘要”，两者不能互相替代。

`read_file` 自动提取 PDF 和支持的 Office 文档；`exec_command(background=true)` 统一启动后台进程。`image_recognize`、`read_docx`、`read_pdf`、`start_process` 仅作为隐藏兼容别名保留，不会和 canonical 工具同时暴露给模型。

识图模式优先级高于主模型视觉能力：即使当前主模型支持多模态，只要会话开启 `use_image_recognition` 且系统识图配置可用，当前轮图片二进制都不会发给主模型，避免主模型收到 `image_url` / image block。重答目标消息中的图片按“当前轮图片”处理，会重新识别或重新提交；更早的历史图片仍走占位 + 工具引用。

## 修改指南

### 要添加新工具
1. 在 `RegisterBuiltin()` (registry.go) 中注册 Tool 元数据 + 处理函数
2. 实现处理函数（同文件或新文件）
3. 更新 AGENTS.md 中的工具说明

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
