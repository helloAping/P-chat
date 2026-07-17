# LLM 模块

> **位置**：`internal/llm/`  
> **依赖**：config（类型）  
> **被依赖**：agent, server, memory（Summarizer）

## 概述

LLM 模块封装与 LLM 提供商的 HTTP 通信，包括流式请求、协议适配、错误分类。支持 OpenAI 兼容 API 和 Anthropic Messages API 两种协议。

## 文件结构

| 文件 | 职责 | 关键函数/类型 |
|---|---|---|
| `client.go` | HTTP 客户端、流式请求、重试/退避 | `Client`, `Stream()`, `ChatOptions` |
| `adapter.go` | 协议适配器接口定义 | `ProtocolAdapter`, `ProtocolRequest`, `StreamChunk` |
| `openai_adapter.go` | OpenAI 兼容协议的 Build + ParseStream | `OpenAIAdapter` |
| `anthropic_adapter.go` | Anthropic Messages API 的 Build + ParseStream | `AnthropicAdapter` |
| `anthropic.go` | Anthropic 特有类型（工具、消息结构） | (Anthropic 工具调用解析) |
| `chat_message.go` | 协议无关的 ChatMessage 类型 | `ChatMessage`, `Role/Type` 常量 |
| `errors.go` | API 错误分类（auth/rate_limit/vision 等） | `APIError`, `ErrorKind`, `ClassifyAPIError()` |
| `openai_adapter_test.go` | OpenAI Build 单元测试 | 覆盖并行 tool_call 合并等 |
| `anthropic_adapter_test.go` | Anthropic Build 单元测试 | 覆盖并行 tool_use 合并等 |

## 核心概念

### 1. 协议适配器 (Protocol Adapter)

```
type ProtocolAdapter interface {
    Build([]ChatMessage, Model, []ToolDef, SystemPrompt) → ProtocolRequest
    ParseStream(io.Reader) → <-chan StreamChunk
}
```

**OpenAI Adapter** (`openai_adapter.go`):
- Build: 将 ChatMessage[] 转为 OpenAI `ChatCompletionRequest` JSON
  - **并行 `tool_call` 合并**（P2-3）：连续的 `TypeToolCall` 消息和 assistant 文本+tool_call 组合会被合并到**单条** assistant 消息的 `tool_calls` 数组里。OpenAI 协议要求并行 tool_call 必须在同一条消息里；拆成多条 assistant 会被 `api-convert.08ms.cn` 等严格代理以 `code=invalid_request_error / "Upstream request failed"` 拒收（2026-07-17 复现）。
- ParseStream: 逐行读取 SSE (`data: ...`)，解析为 `StreamChunk{Content, Thinking, ToolCall, Done, Error}`
- 支持 OpenAI 的 `reasoning_content` → Thinking

**Anthropic Adapter** (`anthropic_adapter.go`):
- Build: 将 ChatMessage[] 转为 Anthropic `MessagesRequest` JSON
  - **assistant 消息合并**（P2-3）：连续 assistant-role 的 text 块和 `tool_use` 块合并到同一条 assistant 消息的 content 数组；连续 `tool_result` 块也合并到同一条 user 消息的 content 数组。
- ParseStream: 逐行读取 SSE (`event: ...` → `data: ...`)，解析 content_block_start/delta/stop、thinking_delta 等事件
- 原生支持 `thinking` 块

### 2. ChatMessage（协议无关消息）

```go
type ChatMessage struct {
    Role    string     // "user" | "assistant" | "tool" | "system"
    Type    string     // "text" | "image" | "tool_call" | "tool_result" | "thinking"
    Content string
    // 工具调用特有字段
    ToolID    string
    ToolName  string
    ToolInput string  // JSON
    ToolError bool
    // 图片特有字段
    ImageURL  string  // 或 ImageData (base64)
    ImageType string
}
```

`Type` 枚举：
- `text` — 普通文本
- `image` — 图片（base64 或 URL）
- `tool_call` — LLM 发出的工具调用（OpenAI native）
- `tool_result` — 工具执行结果
- `thinking` — 代理内部思考（不发送给 LLM）

### 3. StreamChunk（流式增量）

```go
type StreamChunk struct {
    Content  string  // 文本增量
    Thinking string  // 思考/推理增量
    ToolCall *ToolCallDelta  // 工具调用增量（名称、参数片段）
    Done     bool    // 流结束
    Error    string  // 流错误
}
```

### 4. Client 与重试

`Client` 封装：
- 多 provider 端点（从 `config.Config` 读取）
- HTTP 重试（指数退避，最大 3 次）
- 自定义 HTTP 头（API key、organization 等）
- 流式连接超时

### 5. 错误分类

`ClassifyAPIError(err, statusCode)` 将 HTTP 错误映射为 `ErrorKind`：

| ErrorKind | 含义 | 前端处理 |
|---|---|---|
| `auth_error` | API key 无效 | 显示认证错误提示 |
| `rate_limit` | 速率限制 | 显示速率限制提示 |
| `vision_unsupported` | 模型不支持图片 | 用户消息上显示警告芯片 |
| `context_length` | Token 超限 | 建议 /compress |
| `server_error` | LLM 服务端故障 | 通用错误展示 |

## 修改指南

### 要添加新的 LLM 提供商协议
1. 创建 `xxx_adapter.go`，实现 `ProtocolAdapter` 接口
2. 在 `client.go` 注册新协议
3. 参考 `openai_adapter.go` 和 `anthropic_adapter.go`

### 要修改流式解析
- OpenAI: `openai_adapter.go` 的 `ParseStream()`
- Anthropic: `anthropic_adapter.go` 的 `ParseStream()`

### 要添加新的 ChatMessage 类型
- 修改 `chat_message.go` 中的类型常量和字段
- 修改各 adapter 的 Build 方法支持新类型

### 要修改错误分类
- 修改 `errors.go` 的 `ClassifyAPIError()`
- 前端 `chat.ts` 的 `error_kind` 处理

## 相关模块

- [agent.md](agent.md) — 使用 Client.Stream() 进行 LLM 调用
- [config.md](config.md) — Provider/Model 配置
- [server.md](server.md) — SSE 事件映射 chunkToEvent()
