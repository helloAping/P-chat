# Server 模块

> **位置**：`internal/server/`  
> **依赖**：agent, llm, memory, config, tool, subagent, mcp, project, style  
> **被依赖**：cmd/pchat-server, cmd/pchat（通过 serverproc）

## 概述

Server 模块是 P-Chat 的 HTTP API 层，基于 Gin 框架。负责：REST API 路由、SSE 流式推送、会话管理、消息持久化、配置管理、上传、项目/技能管理。

## 文件结构

| 文件 | 职责 | 关键函数/类型 |
|---|---|---|
| `server.go` | Gin 引擎构建、路由注册、CORS 中间件 | `New()`, `NewWithStaticFS()`, `corsMiddleware()` |
| `handler.go` | 所有 API handler 实现（~2130行） | `SendMessage()`, `ListMessages()`, `chunkToEvent()` 等 |
| `messages.go` | 消息发送 / 重答 / SSE 写循环 / 自动续跑控制 | `SendMessage()`, `respondSSE()` |
| `message_helpers.go` | 历史消息响应整形、parts 解码、内部行过滤、附件合并 | `ListMessages()`, `buildMessageResponse()` |
| `handler_test.go` | Handler 单元测试 | |
| `provider_api.go` | Provider/Model CRUD + 上游模型查询 | |
| `config_api.go` | 全局配置接口 | |
| `skill_api.go` | Skill 安装/卸载/搜索 REST | |
| `command_api.go` | 斜杠命令执行 | |
| `upload.go` | 文件上传/下载 | |
| `dialog.go` | 本地文件选择对话框 | |
| `helpers.go` | 辅助函数 | |

## 核心 API 路由

详见 `server.go:86-167`，所有路由以 `/api/v1` 为前缀。

## 核心概念

### 1. POST /sessions/:id/messages — 流式消息处理

**完整的请求处理流程**（handler.go `SendMessage` 约 1150 行起）：

```
1. 解析请求体 (SendMessageRequest)
2. 验证 session 存在，读取 per-session meta (provider/model/style)
3. 加载历史消息（分页，含压缩摘要）
4. 构建 ChatRequest
   - 合并项目级 config/AGENTS.md（若 session 有 project_path）
   - 设置 SubagentRegistry
   - 展开附件 (AttachmentResolver)
5. 调用 agent.ChatWithTools(ctx, req) → <-chan ChatStreamChunk
6. 启动 SSE 流: c.Stream(func(w) { ... })
   6a. 读取 chunk ← stream channel
   6b. chunkToEvent(chunk, provider, model) → StreamEvent
   6c. json.Marshal(ev) → fmt.Fprintf(w, "data: %s\n\n")
   6d. Flush()
   6e. return !chunk.Done  // Done=true 时终止 SSE
7. 清理: 结束流、更新会话时间
```

### 2. chunkToEvent — 事件映射

`chunkToEvent()` (handler.go:1495-1613) 是服务端到前端的翻译器：

```
Chunk 字段检查顺序（优先级从高到低）:
  1. QuestionJSON 非空 → type: "question"
  2. ToolConfirmJSON 非空 → type: "tool_confirm"
  3. Error 非空 → type: "error"
  4. Done == true → type: "done"
  5. ToolName 非空 → type: "tool" (status 由 Step 字符串推导)
  6. Thinking 非空 → type: "thinking"
  7. Content 非空 → type: "content"
  8. ContentRewrite 非空 → type: "content_rewrite"
  9. ThinkingRewrite 非空 → type: "thinking_rewrite"
  10. Phase 非空 → type: "phase"
  11. 其他 → type: "phase" (心跳)
```

**关键设计**：sub_agent 字段（SubAgent/SubAgentTask/SubAgentStatus 等）在所有分支中***无条件拷贝***，使子代理的 content/thinking/tool/phase 事件都能正确路由到嵌套卡片。

### 3. 会话管理

- `ListSessions` — 列出会话（支持 `?project_path=` 过滤）
- `CreateSession` — 创建会话
- `GetSession` — 获取单个会话元数据
- `UpdateSessionMeta` — PATCH 更新 provider/model/style
- `DeleteSession` — 软删除（标记 archived）
- `ArchiveSession / UnarchiveSession` — 归档/恢复
- `PermanentDeleteSession` — 物理删除
- `ClearSessionMessages` — 清空会话消息

### 4. 消息管理

- `ListMessages` (GET) — 分页返回历史消息，含 `parts` 解码
- `SendMessage` (POST) — 发送 + SSE 流
- `CompressConversation` — LLM 压缩历史
- `SetReasoningEffort` — 设置 DeepSeek/OpenAI 思考深度
- `SaveSystemMessage` — 保存自定义系统提示词
- `GetTodos` — 获取待办列表

### 5. 消息持久化

- 消息通过 `memory.Store.AddChatMessageTo()` 持久化
- Assistant 消息的 parts 以 JSON 存储在 metadata 列
- `decodePartsFromMeta()` (handler.go:1280) 在 GET /messages 时还原 parts
- `buildMessageResponse()` 会过滤不应渲染的内部行：tool_call/tool_result、媒体附件独立行、`metadata.ui_hidden=true` 的消息，以及旧库中以 `⏱ 上一回合因` / `⚠ 系统检测：你刚才的回复没有调用任何工具` 开头的自动续跑 user-style nudge。内部续跑提示只能影响下一次 LLM 输入，不应在 GUI 里表现成用户重复发送消息。

### 6. 回合超时自动重试

`limits.max_turn_retries` 控制超时后的服务端自动续跑。每次续跑会重新加载已持久化历史，
追加一条给 LLM 的 "继续" user-style nudge，并在同一个 HTTP 请求 / SSE 流内重新运行 agent。
这条 nudge 入库时必须带 `Meta{"origin":"auto_resume","ui_hidden":true}`。
`ListMessages` / `SnapshotRecovery` 通过 `buildMessageResponse()` 过滤该行；用户只看到
`phase` / `session_status` 表示系统自动接续，不会看到一条伪造的用户气泡。

## 修改指南

### 要添加新的 API 端点
1. 在 `handler.go` 添加 handler 方法
2. 在 `server.go` 注册路由
3. 前端 `client.ts` 添加调用函数

### 要修改 SSE 流式推送
- `SendMessage()` 中的 `c.Stream()` 回调 (handler.go 约 1450 行)
- `chunkToEvent()` 映射逻辑 (handler.go:1495)
- 前端 `chat.ts` `appendStreamEvent()` 处理

### 要修改消息持久化格式
- `decodePartsFromMeta()` (handler.go:1280)
- `parts.go` 中的 `snapshotStructural()`

## 相关模块

- [agent.md](agent.md) — ChatWithTools 提供数据
- [frontend.md](frontend.md) — SSE 事件消费端
- [memory.md](memory.md) — 消息存储
- [config.md](config.md) — Provider/Model 配置
