# CLI 模块

> **位置**：`internal/cli/`  
> **依赖**：agent, llm, server（通过 httpcli）, config, memory, tool  
> **被依赖**：cmd/pchat

## 概述

CLI 模块实现 P-Chat 的终端交互界面：REPL 循环、命令解析、终端渲染（ChatUI）、SSE 事件适配、多行输入、文件编辑器、进度条。

## 文件结构

| 文件 | 职责 | 关键函数/类型 |
|---|---|---|
| `repl.go` | REPL 主循环 | `RunREPL()` |
| `commands.go` | 斜杠命令处理 | `/help`, `/sessions`, `/tools`, `/config`, `/compress` 等 |
| `input.go` | 多行输入、文件引用 | `ReadInput()` |
| `kb.go` | 键盘输入处理、热键 | `handleKey()` |
| `context.go` | 上下文命令（/add, /clear 等）| |
| `progress.go` | 终端进度/工具调用渲染 | |
| `spinner.go` | 加载指示器 | |
| `selector.go` | 选项选择器 | |
| `editor.go` | 外部编辑器集成 | `LaunchEditor()` |
| `export.go` | 对话导出 | |
| `init.go` | CLI 初始化 | |
| `reload.go` | 配置热重载 | |
| `templates.go` | 终端模板 | |
| `toolcache.go` | 工具调用缓存 | |

## 核心概念

### 1. REPL 循环

CLI 使用 RuneReader + 终端 raw mode 实现实时输入处理：
- 多行输入（Shift+Enter 换行）
- 斜杠命令前缀解析
- 文件引用的可视化

### 2. SSE 事件适配

CLI 通过 `httpcli.Client` 连接 pchat-server，消费 SSE 事件流。事件被映射为终端渲染：
- content → 流式文本打印
- thinking → 折叠思考块
- tool → 工具调用进度
- sub_agent → 缩进度视图
- phase → 状态消息
- question → 内联交互式问答（待实现）

### 3. 服务器自动启动

`serverproc.Start()` 自动启动 pchat-server 子进程，通过环境变量 `PCHAT_PORT` 通信。

## 修改指南

### 要修改 REPL 输入处理
- `input.go` 和 `kb.go`

### 要添加新斜杠命令
- `commands.go` 中的命令映射表

### 要修改终端渲染
- `progress.go` (工具调用渲染)
- `templates.go` (终端模板)

### 要修改 SSE 事件处理
- `repl.go` 中的事件适配器

## 相关模块

- [server.md](server.md) — CLI 通过 HTTP 连接服务器
- [httpcli.md](infrastructure.md#httpcli) — SSE 客户端
- [serverproc.md](infrastructure.md#serverproc) — 服务器进程管理
