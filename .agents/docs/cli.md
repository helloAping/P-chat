# CLI 模块

> **位置**：`internal/cli/`  
> **依赖**：agent, llm, server（通过 httpcli）, config, memory, tool  
> **被依赖**：cmd/pchat

## 概述

CLI 模块实现 P-Chat 的终端交互界面：REPL 循环、命令解析、终端渲染（ChatUI）、SSE 事件适配、多行输入、文件编辑器、进度条。

## 文件结构

| 文件 | 职责 | 关键函数/类型 |
|---|---|---|
| `repl.go` | REPL 主循环 | `NewREPL()`, `(*REPL).Run()` |
| `commands.go` | 斜杠命令处理 | `/help`, `/history`, `/tools`, `/config`, `/compress` 等 |
| `input.go` | 终端逐行输入与斜杠补全 | `InputLine()` |
| `kb.go` | CLI 知识库配置与本地扫描 | `KBManager` |
| `context.go` | local / HTTP 命令上下文与 SSE 事件适配 | `cliContext`, `httpEventToChunk()` |
| `progress.go` | 终端进度/工具调用渲染 | |
| `spinner.go` | 加载指示器 | |
| `selector.go` | 选项选择器 | |
| `editor.go` | 多行编辑与计划确认 | `multilineEditor()`, `readPlanDecision()` |
| `export.go` | 对话导出 | |
| `init.go` | CLI 初始化 | |
| `reload.go` | 配置热重载 | |
| `templates.go` | 终端模板 | |
| `toolcache.go` | 工具调用缓存 | |

## 核心概念

### 1. REPL 循环

CLI 使用 RuneReader + 终端 raw mode 实现实时输入处理：
- 多行输入（Shift+Enter 换行）
- 斜杠命令候选弹窗：输入 `/` 展示命令，继续输入按主命令或别名过滤，↑↓ 选择，Tab/→ 补全，Enter 执行；候选区按 stdout 实际尺寸截断，并用相对光标移动原位重绘以兼容 Windows ConPTY
- 文件引用的可视化

TTY 输入正文已保留在终端时，响应渲染只增加分隔，不再次打印同一正文。队列自动发送、
计划自动执行等没有现场输入行的回合，才由 `ChatUI.BeginTurn()` 补充一次用户消息标题。

### 2. SSE 事件适配

CLI 通过 `httpcli.Client` 连接 pchat-server，消费 SSE 事件流。事件被映射为终端渲染：
- content → 流式文本打印
- thinking → 折叠思考块
- tool → 工具调用进度
- skill → 固定输出 `当前调用 Skill：<name>`，随后显示 ready/error
- sub_agent → 缩进度视图
- phase → 状态消息
- stream_reset → 标记上一次失败 attempt 的部分输出作废，并从新区块展示重试结果
- question / tool_confirm → 内联交互式问答与工具确认

终端无法像 GUI 一样可靠删除已经打印的任意多行文本。收到 `stream_reset` 时，
`ChatUI` 会结束当前文本块、明确提示上方片段已作废，然后从下方重新展示本次重试；
HTTP 模式由 `httpEventToChunk()` 将 SSE `type=stream_reset` 映射到
`ChatStreamChunk.StreamReset`。

### 3. 服务器自动启动

`serverproc.Start()` 自动启动 pchat-server 子进程，通过环境变量 `PCHAT_PORT` 通信。

### 4. 会话回合队列

CLI 用 `/queue` 管理与 GUI 相同的持久化 FIFO：

```text
/queue                         # 列表
/queue add <消息>              # 入队
/queue edit <id> <新消息>      # 编辑 queued 项
/queue run                     # 按 FIFO 连续发送
/queue retry <id>              # 重试 failed 项
/queue delete <id>             # 删除 queued/failed 项
/queue clear                   # 清空 queued/failed 项
```

`/queue run` 通过 claim/complete/fail 生命周期执行，沿用入队时保存的 provider、
model、style、work_mode、附件、`active_skills` 等发送参数。遇到 failed/running 队首
会停止，不越过队首；显式 `false` 的图像识别和子代理开关也按三态值原样转发。
local / HTTP 两种 `cliContext` adapter 使用同一组命令处理器。local CLI 列表时会恢复
超过安全窗口的陈旧 running 项，避免上次进程中断后永久卡住队首。

### 5. 项目会话范围

生产 HTTP CLI 启动时会将当前工作目录作为 `project_path`，只恢复该项目中最近更新的会话；
新建会话也继承这个范围，避免误接入其他项目或全局历史。使用 `/project <目录>` 显式切换并
新建该项目会话，`/project off` 切回全局会话。恢复旧会话时同步其风格、工作模式、
提供商和模型；显式 `--style`、`--provider` 参数优先。

### 6. HTTP 会话与命令能力

生产 CLI 的聊天、会话、知识库与召回都通过 `httpContext` 访问
`pchat-server`。`/kb list|add|remove|scan|status` 调用服务端知识库 API，`/kb add` 也会启用知识库总开关；`/recall`
调用服务端 canonical 搜索 API；因此服务端以 `--config <path>` 启动时，CLI 不会
误写默认全局配置。`/kb scan` 只启动服务端异步扫描，并明确提示索引在后台进行；不会创建轮询 goroutine
或在用户输入期间写入终端。使用 `/kb status [name]` 主动读取服务端状态：无参数依次显示
所有已挂载库，`name` 只查询一个库。服务端返回 `done=false` 且没有 `message` 时表示当前没有
活动扫描任务，CLI 只展示已有节点数；`message` 表示进行中，`done=true` 表示完成，`error` 表示
完成但发生错误。

`/rules` 和 `/agents` 同样按当前会话 ID 调用服务端。规则使用该会话的项目根加载全局与
项目 `.p-chat/rules/`；AGENTS.md 返回项目根、项目 `.p-chat`、全局三个候选及首个非空可读
的 OR 胜出项，并展示实际注入 agent 的提示词，不把候选内容错误称为合并结果。

### 7. Skill 命令

```text
/skills                              # 列出当前项目视图可发现的 Skill
/skill lark-doc 创建一份周报         # 显式调用
/lark-doc 创建一份周报                # 已安装 Skill 的动态快捷形式
```

local 与 HTTP CLI 都通过 catalog 验证动态快捷名称；实际聊天请求只发送
`ActiveSkills: ["lark-doc"]`。可见 start 事件必须发生在 Skill 正文进入 LLM 上下文之前。

### 8. 模型切换

`/model <provider>` 会选择该 provider 标记为默认的模型；未标记默认模型时选择第一个可用模型。
编号、`provider/model` 和 `provider model` 形式都会先校验目标模型，校验失败时保持当前 provider/model 不变。

### 9. 会话导出

`/export [markdown|html|pdf|json] [id|last] [-o <file>]` 在 local 和生产 HTTP CLI 中均可使用。
HTTP 模式下载服务端的 canonical export，并优先使用 `Content-Disposition` 中的建议文件名；
该名称会去除路径分隔符后再落盘。`last` 只查询当前项目范围内最近更新的会话，默认格式为 markdown。

### 10. 工具结果展开

`/expand` 保存最近 20 个工具终态结果。普通输出直接保存在 CLI 环形缓存中；SSE 标记为
截断的输出在首次 `/expand <编号>` 或 `/expand last` 时，按工具所属会话向服务端请求完整内容，
并回填缓存。服务端 30 分钟缓存失效、条目淘汰或会话不匹配时会显示明确的过期提示。

### 11. Provider / Model 配置

生产 HTTP CLI 的 `/setup`、`/provider`、`/config` 与 `/config model` 通过
`pchat-server` 的 Provider/Model API 读写服务端当前生效的配置层。启动 server 时传入的
`--config <path>` overlay 因此会保留；CLI 不直接加载或写入 `~/.p-chat/config.json`。
每次写入后都会刷新 provider/model 缓存，删除当前会话选择的模型后不会继续发送已删除的
模型。`/provider` 只显示 API Key 是否已设置，不保留或输出密钥明文。

## 修改指南

### 要修改 REPL 输入处理
- `input.go` 和 `kb.go`

### 要添加新斜杠命令
- `commands.go` 中的命令映射表
- 需要跨 local / HTTP 模式时，同步扩展 `context.go` 与 `internal/httpcli/client.go`

### 要修改终端渲染
- `progress.go` (工具调用渲染)
- `templates.go` (终端模板)

### 要修改 SSE 事件处理
- `context.go` 的 `httpEventToChunk()`（HTTP SSE → `ChatStreamChunk`）
- `progress.go` 的 `ChatUI.Handle()`（终端渲染）

## 相关模块

- [server.md](server.md) — CLI 通过 HTTP 连接服务器
- [httpcli.md](infrastructure.md#httpcli) — SSE 客户端
- [serverproc.md](infrastructure.md#serverproc) — 服务器进程管理
