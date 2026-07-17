# Server 模块

> **位置**：`internal/server/`  
> **依赖**：agent, llm, memory, config, tool, subagent, mcp, project, style  
> **被依赖**：cmd/pchat-server, cmd/pchat（通过 serverproc）

## 概述

Server 模块是 P-Chat 的 HTTP API 层，基于 Gin 框架。负责：REST API 路由、SSE 流式推送、会话管理、消息持久化、配置管理、上传、项目/技能管理。

## 文件结构

| 文件 | 职责 | 关键函数/类型 |
|---|---|---|
| `server.go` | Gin 引擎构建、路由注册、CORS 中间件 + trace id middleware | `New()`, `NewWithStaticFS()`, `corsMiddleware()`, `traceIDMiddleware()` |
| `handler.go` | 所有 API handler 实现（~2130行） | `SendMessage()`, `ListMessages()`, `chunkToEvent()` 等 |
| `handler_test.go` | Handler 单元测试 | |
| `knowledge_api.go` | 知识库 CRUD + 扫描管道 + 三层索引 | `ListSections`, `ListNodes`, `GetNodeContent`, `ClearKnowledgeBase`, `indexScan` |
| `provider_api.go` | Provider/Model CRUD + 上游模型查询 | |
| `config_api.go` | 全局配置接口 | |
| `skill_api.go` | Skill 安装/卸载/搜索 REST | |
| `command_api.go` | 斜杠命令执行 | |
| `tool_api.go` | P3-2 工具列表端点 | `ListTools()` |
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
- `SnapshotRecovery` (GET) — **P0-1**：断线恢复用的增量快照
- `Regenerate` (POST) — **P1-3**：物理截断 user 消息之后的行后重跑
- `CompressConversation` — LLM 压缩历史
- `SetReasoningEffort` — 设置 DeepSeek/OpenAI 思考深度
- `SaveSystemMessage` — 保存自定义系统提示词
- `GetTodos` — 获取待办列表

### 5. 消息持久化

- 消息通过 `memory.Store.AddChatMessageTo()` 持久化
- Assistant 消息的 parts 以 JSON 存储在 metadata 列
- `decodePartsFromMeta()` (handler.go:1280) 在 GET /messages 时还原 parts

### 6. P0-1 / P1-3 增量端点 (round 2, 2026-07-15)

#### SnapshotRecovery (P0-1)

`GET /api/v1/sessions/:id/snapshot?after_seq=N` — 返回所有
`seq > N` 的 assistant 消息 (oldest first)，含完整 metadata
blob (持久化 parts[])。响应 `{ messages, next_seq }`。

- 前端在 SSE 流意外断开时调此端点补齐 trailing assistant bubble
- 不重发 LLM 调用，不增加费用，只补"已入库但没传到前端"的内容
- 客户端用 fingerprint (tool_id / text 前 40 字符) 去重

#### Regenerate (P1-3)

`POST /api/v1/sessions/:id/regenerate` body `{ user_message_id }` —
物理截断 `id > user_message_id` 的行，复用 `respondSSE` 重新
跑 agent loop。`user_message_id` 必须是 user role (`ValidateUserMessageID` 严格校验)。

- 不放 undo buffer（regen 是正常流，不是破坏性操作）
- 不接受 style/model 覆盖（保持与上轮一致，避免意外切换）
- 共享 `sessionLocks` 防并发 regen

#### respondSSE helper

SendMessage 和 Regenerate 共享的 SSE 写循环：
设 header + 写 `data: <json>\nid: <N>\n\n`（P3-1 顺序）+ 强制 Flush。
新增 SSE 端点应复用此函数，避免重复实现。

#### ContextInspector (P2-3)

`GET /api/v1/sessions/:id/context` — 返回 `{session_id, provider, model,
context_window, estimated_tokens, usable_tokens, utilization_pct,
compressed_summary, messages:[{role, tokens, preview, is_tool_result}]}`。

- 复用 `buildLLMMessages` + `llm.EstimateTokensMessages` 拿 LLM-bound token 估算
- compSummary 加成 system 消息（与 prompt 拼接方式一致）
- 利用率 = estimated / usable * 100，颜色阈值 60%/80% 跟 tryAutoCompact 一致
- 响应小（~10KB for 200 messages），不带 parts[]

### 7. 知识库 API

`knowledge_api.go` 提供完整的知识库生命周期管理：

| 端点 | 方法 | 说明 |
|------|------|------|
| `/knowledge/config` | GET/PATCH | 配置读写 |
| `/knowledge/bases` | GET/POST/DELETE | 知识库 CRUD |
| `/knowledge/bases/:name/scan` | POST/DELETE | 启动/取消扫描 |
| `/knowledge/bases/:name/scan/status` | GET | 扫描进度轮询 |
| `/knowledge/bases/:name/clear` | DELETE | 清除所有扫描数据 |
| `/knowledge/bases/:name/sections` | GET/POST | 旧表条目管理 |
| `/knowledge/bases/:name/sections/:id` | GET/DELETE | 单个条目 |
| `/knowledge/bases/:name/nodes` | GET | 三层索引节点列表（树视图数据源） |
| `/knowledge/bases/:name/nodes/:id/content` | GET | 节点内容块 |
| `/knowledge/search` | POST | 跨库搜索 |

`clear` 端点执行后自动调用 `agent.Reload()` 刷新 L1 overview 缓存。

`nodes` 端点返回扁平 `NodeTreeItem[]`（含 `parent_id` / `child_count` / `content_count`），前端据此构建三层树视图。

### 8. 端到端 trace id (P3-3, 2026-07-16)

每次 SSE 会话自动生成 8 字符 hex trace id (`T-xxxxxxxx`)，
贯穿整个请求生命周期。详见 [P3-3 设计](../../docs/plans/round4-trace-and-extensibility-plan.md)。

**流程**：
```
HTTP request (X-Trace-Id 头可选)
  → traceIDMiddleware: 读 header 或 mint
    → trace.WithID(ctx, id) + c.Header("X-Trace-Id", id)
      → agent.ChatWithTools 读 req.TraceID → ctx 注入
        → sendOrDrop 从 ctx 读 → chunk.TraceID
          → chunkToEvent 复制 → ev.TraceID → SSE JSON event
            → 前端错误气泡 "复制 trace id" 按钮
```

**Package**：`internal/trace` 提供 `NewID()` / `WithID(ctx, id)` /
`FromContext(ctx)` / `LogPrefix(ctx)` 四个核心函数。

**Wails 路径兼容**：`cmd/pchat-gui/main.go` `extractTraceID` 从
请求 body 抽 `trace_id` 字段并设 `X-Trace-Id` header（绕过 Wails
binding 不能加任意 header 的限制）。前端 `client.ts` 在 Wails
路径把 `trace_id` 塞 body。

**CORS**：`Access-Control-Allow-Headers` 含 `X-Trace-Id`（浏览器
预检通过）。

**Log 前缀**：`trace.LogPrefix(ctx)` 返回 `[T-xxxxxxxx] ` 或 `""`。
主要 log 行（LLM client / forwarder / tool handler）已加。

**测试**：`handler_trace_test.go` 4 个 case：
header 透传 / 缺失生成 / 互不重复 / CORS 允许。

### 9. 工具列表 API (P3-2, 2026-07-16)

`GET /api/v1/tools` 返回 `[]ToolInfo`：
```json
{
  "tools": [
    {
      "name": "exec_command",
      "description": "执行 shell 命令",
      "parameters": {...},
      "dynamic": false
    },
    {
      "name": "greet",
      "description": "向用户问好",
      "parameters": {...},
      "dynamic": true,
      "source": "/home/u/.p-chat/tools/greet.yaml"
    }
  ]
}
```

- `dynamic=true` 的工具由 P3-2 watcher 从 `~/.p-chat/tools/*.yaml` 加载
- `source` 是 YAML 绝对路径（前端 `ToolListDrawer` 显示"查看源码"）
- 顺序：built-in (alphabetical) → dynamic (alphabetical)
- 前端缓存路径：直接读 `state.tools` 或调 `api.listTools()`

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
