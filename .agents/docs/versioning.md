# 版本管理与 Schema 迁移

> **最后更新**：新版本 VERSION 文件 + one-shot 迁移约束生效

## 一、版本号体系

### 唯一真源

```
VERSION  ← 项目根目录，semver 格式，如 1.0.0
```

- **发布前改此文件** — 所有二进制（pchat / pchat-server / pchat-gui）共享同一版本
- **CI / task build 自动注入** — go-task 读取 VERSION 并通过 ldflags 编译进二进制
- **开发环境回退** — 未注入时自动读取 VERSION 文件或 git hash → `dev-abc1234`

### 可查询位置

| 方式 | 命令 / 端点 |
|---|---|
| CLI | `pchat version` |
| HTTP | `GET /api/v1/version` |
| 启动日志 | `pchat-server version=1.0.0` |

### 版本号格式

```
1.0.0           — 正式发布版本
1.0.0 (abc1234) — 含 git hash 的完整版本
dev-abc1234     — 开发构建
```

## 二、Schema 迁移规范

### 迁移引擎概览

```
internal/memory/
├── migrations.go    ← 迁移引擎 + 所有迁移定义
└── memory.go        ← Open() → Migrate() → 自动执行
```

- **升级**：`Open()` 启动时自动执行所有未应用的迁移，幂等（已应用自动跳过）
- **回滚**：`POST /api/v1/migrations/rollback { "target": N }` — 手动触发
- **旧数据库兼容**：检测到无 `schema_migrations` 表时，自动标记所有已知迁移为已应用

### 新增迁移的步骤

```go
// 在 internal/memory/migrations.go 的 allMigrations 末尾追加：

var allMigrations = append(allMigrations, Migration{
    Version: 3,
    Name:    "add_feature_x_table",
    Up:      `CREATE TABLE IF NOT EXISTS feature_x (...)`,
    Down:    `DROP TABLE IF EXISTS feature_x`,
})
```

**规则**：
1. **版本号递增**，禁止修改已有迁移的 `Up`/`Down`（历史不可变）
2. **Up 必须幂等** — 使用 `IF NOT EXISTS` / `IF EXISTS`
3. **Down 必须可逆** — 保证回滚到上一版本能恢复
4. **事务包裹** — 引擎自动在事务内执行 Up + 写 `schema_migrations` 记录
5. **添加新表** — `CREATE TABLE IF NOT EXISTS`
6. **修改现有表** — 优先 `ALTER TABLE ADD COLUMN ... DEFAULT ...`，避免 `DROP COLUMN`

### 破坏性变更约束

以下操作**需要在 Up 中使用 DROP/CREATE 时务必注意**：

| 操作 | 约束 |
|---|---|
| `DROP TABLE` | 只删除**临时/重建**的表。永久删除表应**先归档数据**到新表 |
| `DROP COLUMN` | SQLite 不支持直接删除列。**先建新表复制数据，再删旧表**。需在 Up 中显式处理数据迁移 |
| `ALTER TABLE RENAME COLUMN` | 仅在 SQLite 3.25+ 支持，**需同时更新所有引用该列的 Go 代码** |
| 列类型变更 | SQLite 无严格类型。**禁止**在 Up 中用 CAST 强制转换 |
| 删除索引后重建 | 确保新索引与旧索引等价，否则可能**影响查询性能** |

### DROP COLUMN 的标准流程

SQLite 不支持 `ALTER TABLE DROP COLUMN`（3.35.0+ 支持，但 Go 绑定的 `modernc.org/sqlite` 版本可能滞后）。标准做法：

```sql
-- 1. 创建新表（不含要删除的列）
CREATE TABLE messages_new (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    conversation_id TEXT NOT NULL,
    role            TEXT NOT NULL,
    content         TEXT NOT NULL,
    created_at      INTEGER NOT NULL
);

-- 2. 复制数据
INSERT INTO messages_new (id, conversation_id, role, content, created_at)
    SELECT id, conversation_id, role, content, created_at FROM messages;

-- 3. 删除旧表
DROP TABLE messages;

-- 4. 重命名
ALTER TABLE messages_new RENAME TO messages;

-- 5. 重建索引
CREATE INDEX IF NOT EXISTS idx_messages_conv ON messages(conversation_id, id);
```

此类迁移**必须**在 Up 注释中标注 `⚠️ 破坏性迁移 — 回滚将丢失数据`。

### 回滚约束

| 约束 | 说明 |
|---|---|
| **回滚非自动** | 需手动调用 API，防止误操作 |
| **回滚前建议备份** | `cp store.db store.db.bak` |
| **Down 可能丢数据** | `DROP COLUMN` / `DROP TABLE` 类 Down 不可逆（数据已消除），Down 注释必须标注 |
| **回滚版本范围** | 支持回滚到任意历史版本（`target: 0` = 完全清空） |

### 测试要求

```go
// 每条迁移必须覆盖：
func TestMigration_V3_Upgrade(t *testing.T) { ... }     // 空白 DB → V3，表正常创建
func TestMigration_V3_Rollback(t *testing.T) { ... }     // V3 → V2，表正常清除
func TestMigration_V3_Idempotent(t *testing.T) { ... }   // 重复执行无副作用
func TestMigration_V3_Bootstrap(t *testing.T) { ... }    // 已有数据的旧 DB 升级正常
```

## 三、发版检查清单

```
□ 1. 修改 VERSION 文件为新的 semver
□ 2. 如有 Schema 变更，新增迁移定义在 allMigrations 末尾
□ 3. 迁移测试通过：go test ./internal/memory/... -run TestMigration
□ 4. 全量测试通过：go test ./...
□ 5. 前端构建通过：cd cmd/pchat-gui/frontend && npm run build
□ 6. task build 成功，version 命令输出正确版本号
□ 7. git tag v{version} && git push --tags
```

## 四、相关文件索引

| 文件 | 说明 |
|---|---|
| `VERSION` | 版本号唯一真源 |
| `internal/version/version.go` | 版本号解析（String/FullString/git hash） |
| `internal/memory/migrations.go` | 迁移引擎 + 所有迁移定义（`allMigrations`） |
| `internal/memory/memory.go` | `Open()` → `Migrate()` 入口 |
| `internal/server/handler.go` | `MigrationStatus` / `MigrationRollback` / `VersionHandler` |
| `Taskfile.yml` | `build` / `build:gui` 任务中的 ldflags 注入 |
