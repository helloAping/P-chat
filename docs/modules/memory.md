# Memory 模块

> **位置**：`internal/memory/`  
> **依赖**：llm, paths  
> **被依赖**：agent, server, subagent

## 概述

Memory 模块管理 P-Chat 的持久化存储——SQLite 数据库存储对话历史、消息、元数据、摘要和待办事项。

## 文件结构

| 文件 | 职责 | 关键函数/类型 |
|---|---|---|
| `memory.go` | SQLite 数据库操作、CRUD、迁移、待办 | `Store`, `Open()`, `OpenAt()`, `AddChatMessageTo()` |
| `summarizer.go` | LLM 驱动的对话压缩 | `Summarizer`, `Compress()` |
| `fileio.go` | 旧版 JSON 文件导入 | `migrateFromLegacyJSON()` |

## 核心概念

### 1. Store — 核心持久化接口

```go
type Store struct {
    db         *sql.DB
    mu         sync.Mutex
    currentID  string
    maxHistory int
    pendingWrites []Message  // 批量写入缓冲区
    pendingMu     sync.Mutex
    maxPending    int
    flushInterval time.Duration
}
```

关键方法：
- `Open(maxHistory)` — 打开/创建数据库
- `CreateConversation(title)` → ID
- `AddChatMessageTo(convID, msg)` — 追加消息（异步批量写入）
- `AddChatMessageWithMetaTo(convID, msg, meta)` — 带元数据写入
- `GetChatMessagesFor(convID, limit)` — 获取消息历史
- `GetChatMessagesWithMetaPage(convID, beforeID, limit)` — 分页查询
- `GetChatMessagesAfterIDFor(convID, limit, afterID)` — 从某 ID 之后查询
- `ListConversations()` — 列出所有会话
- `SetConversationMeta(convID, meta)` → 写入会话元数据

### 2. 数据库 Schema

```sql
conversations: id, title, created_at, updated_at, metadata, archived
messages: id, conversation_id, role, content, tokens, created_at, metadata
summaries: conversation_id, range_start, range_end, summary, created_at
todo_items: id, session_id, content, status, created_at, updated_at
knowledge_chunks: id, conversation_id, content, embedding, source, created_at
compression_snapshots: conversation_id, summary, message_count, created_at
```

### 3. 批量写入

为减少 fsync 开销，消息写入是异步的：
- `AddChatMessageTo()` 将消息加入 `pendingWrites` 缓冲区
- 定时器（`flushInterval`）或缓冲满（`maxPending`）触发 flush
- flush 使用 `BEGIN → INSERT → COMMIT` 事务

### 4. 元数据格式

`messages.metadata` 列存储 JSON `map[string]string`，常用键：
- `role` — 消息角色
- `reason` — 来源 ("user" / "assistant" / "compression")
- `thinking` — 完整思考文本（非 parts 路径）
- `parts` — 结构化 parts JSON（tool + sub_agent）

### 5. Summarizer（对话压缩）

`Summarizer` 使用 LLM 压缩旧消息为摘要：
- `Compress(convID, maxMessages)` — 保留最近 N 条，其余压缩为摘要
- 摘要存储在 `compression_snapshots` 表
- 后续 `ChatRequest` 会携带 `CompressedSummary`

### 6. 遗留数据迁移

`Open()` 首次打开时自动迁移旧的 JSON 文件格式到 SQLite。

## 修改指南

### 要修改数据库 Schema
- `memory.go` 中的 `initTables()` / `runMigrations()`
- 使用 `migration_*` 命名模式添加新迁移

### 要修改消息持久化格式
- `AddChatMessageWithMetaTo()` (memory.go)
- `decodePartsFromMeta()` (handler.go:1280) — 服务端还原

### 要修改批量写入策略
- `pendingWrites`, `maxPending`, `flushInterval` (memory.go)
- `startFlusher()` / `flushNow()`

### 要修改 Summary 压缩
- `summarizer.go` 中的 `Compress()` 方法

## 相关模块

- [agent.md](agent.md) — 通过 `persistAssistant()` 持久化
- [server.md](server.md) — 通过 API 读取/写入
- [config.md](config.md) — 数据库路径配置
