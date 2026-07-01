# Memory 模块

> **位置**：`internal/memory/`  
> **依赖**：llm, paths  
> **被依赖**：agent, server, subagent

## 概述

Memory 模块管理 P-Chat 的持久化存储——SQLite 数据库存储对话历史、消息、元数据、摘要和待办事项。

## 文件结构

| 文件 | 职责 | 关键函数/类型 |
|---|---|---|
| `memory.go` | SQLite 数据库操作、CRUD、迁移入口 | `Store`, `Open()`, `OpenAt()`, `AddChatMessageTo()` |
| `migrations.go` | **Schema 迁移引擎** — 版本表 + 所有迁移定义 | `Migrate()`, `Rollback()`, `allMigrations` |
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

当前版本表（由 `migrations.go` 中 `allMigrations[].Up` 定义）：

```sql
-- 元表
schema_migrations: version, name, applied_at

-- 业务表
conversations: id, title, created_at, updated_at, metadata, archived
messages: id, conversation_id, role, content, tokens, created_at, metadata
summaries: conversation_id, range_start, range_end, summary, created_at
todo_items: session_id, item_id, content, status, sort_order
chunks: id, source, content, metadata, created_at
embeddings: chunk_id, model, vector, dim, created_at
```

**Schema 变更规则** → 详见 [versioning.md §二](versioning.md#二schema-迁移规范)。

### 3. Schema 迁移（重要）

- 启动时自动执行 `Migrate()` — 已应用的迁移幂等跳过
- 回滚需手动调用 `POST /api/v1/migrations/rollback`
- 新增迁移只需在 `migrations.go` 的 `allMigrations` 末尾追加
- **破坏性变更**（DROP TABLE / DROP COLUMN）必须在 Up/Down 注释中标注 ⚠️

详见 [versioning.md](versioning.md) 完整规范。

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
- `migrations.go` 中的 `allMigrations` 列表 — **追加新 Migration，不修改已有**
- 遵循破坏性变更约束 → [versioning.md §二](versioning.md#二schema-迁移规范)
- 必须添加对应测试（升级 / 回滚 / 幂等）

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
