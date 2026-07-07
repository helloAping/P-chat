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
| `handler_test.go` | Handler 单元测试 | |
| `knowledge_api.go` | 知识库 CRUD + 扫描管道 + 三层索引 | `ListSections`, `ListNodes`, `GetNodeContent`, `ClearKnowledgeBase`, `indexScan` |
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
