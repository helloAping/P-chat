# LLM 模块

> **位置**：`internal/llm/`  
> **依赖**：config（类型）, requestheader, trace
> **被依赖**：agent, server, memory（Summarizer）

## 概述

LLM 模块封装与 LLM 提供商的 HTTP 通信，包括流式请求、协议适配、错误分类。支持 OpenAI Chat Completions、OpenAI Responses 和 Anthropic Messages 三种协议；旧配置值 `openai` / `anthropic` 会分别按 OpenAI Chat / Anthropic Messages 兼容处理。

## 文件结构

| 文件 | 职责 | 关键函数/类型 |
|---|---|---|
| `client.go` | HTTP 客户端、流式请求、重试/退避 | `Client`, `Stream()`, `ChatOptions` |
| `adapter.go` | 协议适配器接口定义 | `ProtocolAdapter`, `ProtocolRequest`, `StreamChunk` |
| `openai_adapter.go` | OpenAI 兼容协议的 Build + ParseStream | `OpenAIAdapter` |
| `openai_responses_adapter.go` | OpenAI Responses 协议的 Build + ParseStream | `OpenAIResponsesAdapter` |
| `anthropic_adapter.go` | Anthropic Messages API 的 Build + ParseStream | `AnthropicAdapter` |
| `anthropic.go` | Anthropic 特有类型（工具、消息结构） | (Anthropic 工具调用解析) |
| `chat_message.go` | 协议无关的 ChatMessage 类型 | `ChatMessage`, `Role/Type` 常量 |
| `runtime_context.go` | 动态上下文消息识别与协议包装 | `IsRuntimeContext()` |
| `reasoning_replay.go` | DeepSeek 推理内容回传 | `deepSeekReasoningReplay()`, `messageReasoning()` |
| `cache_usage.go` | 统一缓存命中/未命中 token | `CacheUsage` |
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
- 对 DeepSeek（模型名前缀或官方 endpoint host）且携带 tools 的请求，从助手 `Meta["thinking"]` 回传 `reasoning_content`，覆盖纯思考工具轮和历史文本轮。
- `pchat_context_*` 系统快照在消息原位置包装为 `application_context`，不会提升到首条系统提示词。

**OpenAI Responses Adapter** (`openai_responses_adapter.go`):
- Build: 复用 OpenAI Chat 的协议无关消息归一逻辑，再转换为 Responses `input`、`tools`、`max_output_tokens` 请求形状。
- ParseStream: 解析 `response.output_text.delta`、`response.refusal.delta`、reasoning summary / reasoning text、function call arguments、`response.completed` / `response.failed` 等事件。
- 工具调用按 Responses 的 `call_id` / `output_item.done` 归一成 `ToolCallDelta`；usage 解析 `input_tokens` / `output_tokens` 与 cached tokens。
- `reasoning_effort` 在 Responses 下会转换为顶层 `reasoning.effort`。

**Anthropic Adapter** (`anthropic_adapter.go`):
- Build: 将 ChatMessage[] 转为 Anthropic `MessagesRequest` JSON
  - **assistant 消息合并**（P2-3）：连续 assistant-role 的 text 块和 `tool_use` 块合并到同一条 assistant 消息的 content 数组；连续 `tool_result` 块也合并到同一条 user 消息的 content 数组。
- ParseStream: 逐行读取 SSE (`event: ...` → `data: ...`)，解析 content_block_start/delta/stop、thinking_delta 等事件
- 原生支持 `thinking` 块
- DeepSeek 带 tools 请求回传已有 thinking；其他模型不注入缺少签名的 DeepSeek thinking 块。
- `pchat_context_*` 动态快照包装为原位置的 user 内容，不合并到顶层 system。

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
- `image` — 图片（base64 或 URL）；OpenAI-compatible 请求会在 `image_url.url` 放 data URL，同时补 `image_url.data` raw base64 兼容需要 data 字段的代理。
- `tool_call` — LLM 发出的工具调用（OpenAI native）
- `tool_result` — 工具执行结果
- `thinking` — 展示用思考行（不直接发送）；DeepSeek 需要的回传内容取自助手文本消息的 `Meta["thinking"]`

图片消息的提交边界由 Agent 决定，而不是 LLM adapter 决定：
- 当前轮图片和重答目标图片可以保留为 `TypeImage`，adapter 才会把它转为 OpenAI `image_url` 或 Anthropic `image` block。
- 历史图片在进入 adapter 前会被 Agent 替换成文本占位，不会反复提交原图 payload。
- 如果历史图片带 `upload_id` 且存在可用视觉能力，LLM 可通过 `media_recognize` 工具按需重新读取；工具返回文本结果后再进入后续 LLM 轮次。

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

OpenAI 流式 usage 解析保留 `prompt_cache_hit_tokens` / `prompt_cache_miss_tokens`；兼容
`prompt_tokens_details.cached_tokens`。字段缺失时 `StreamChunk.CacheUsage=nil`，不会把
“未报告”误判成零命中。Agent 输出 `[llm/cache]` 日志，含 provider/model/round/attempt
以及 input、cache_hit、cache_miss token，不记录完整提示词。

输入/输出总量在单次请求内按累计 usage 去重，跨轮次和重试按增量求和。聚合命中率用
`sum(hit) / sum(hit + miss)`，不要平均各请求的百分比。估算上下文长度时也计入已存推理文本。

`Client` 封装：
- 多 provider Base URL 与按模型解析的 `api_endpoint`（请求前统一拼接成完整地址）
- HTTP 重试（指数退避，最大 3 次）
- 自定义 HTTP 头（API key、organization 等）
- Provider 自定义请求头模板；协议默认头设置完成后展开并覆盖同名字段
- 流式连接超时
- `ChatCM()` 非流式调用也用于 provider/model 的无状态 `sayhi` 连接测试

Provider 选择 `openai_chat`、`openai_responses` 或 `anthropic_messages` 协议；旧 `openai`
与 `anthropic` 值会继续兼容。Provider 保存公共 `base_url`，每个 LLM 模型保存可编辑
`api_endpoint`；OpenAI Chat 新模型默认 `/chat/completions`，OpenAI Responses 默认
`/responses`，Anthropic Messages 默认 `/messages`。`providerEntry` 根据本次请求的
模型解析完整 URL，避免同一 Provider 下的模型互相覆盖端点。旧完整 `api_url` 仅由兼容
读取与 V12 升级步骤拆分。

`internal/requestheader` 从 Agent 注入的请求上下文读取 `SessionID` / `ClientMsgID`，并结合
trace、UUID、雪花 ID 与时间戳展开 `ProviderConfig.CustomHeaders`。`ChatStreamCM()`、
`ChatCM()`、旧 OpenAI/Anthropic 流式与非流式入口都在真正发送 HTTP 请求前调用同一套
`Apply()`，因此主对话、识别、摘要与连接测试行为一致。自定义请求头最后应用，允许兼容
网关覆盖 `Authorization`、`x-api-key`、`Content-Type` 等协议字段；传输层字段在保存时拒绝。

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
