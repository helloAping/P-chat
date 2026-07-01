# 版本升级系统

> **强约束**：任何涉及 `~/.p-chat/` 目录结构、SQLite schema、配置文件格式的结构性变更，**必须**编写升级步骤。

## 概念

- **AppVersion**：`~/.p-chat/version` 文件中存储的整数，表示用户数据目录的当前结构版本。
- **Current**：二进制内嵌的最新版本号，定义在 `internal/upgrade/version.go`。
- **升级步骤**：一个 `func(*sql.DB) error` 函数，将数据从 `V(N)` 升级到 `V(N+1)`。

## 启动流程

```
pchat-server 启动
  └─ memory.Open()
  └─ upgrade.Run(db)          ← 读 ~/.p-chat/version
       ├─ from == Current → 跳过
       └─ from < Current → 顺序执行 V(from)→V(from+1)→...→V(Current)
           每步成功后写 version 文件（支持断点续升）
  └─ style.NewManager(db)     ← 此时 styles 表已存在且已 seed
```

## 如何新增一个版本

例：当前 `Current = V3`，需要做一次 schema 变更升级到 V4。

### 1. 更新版本常量

`internal/upgrade/version.go`：

```go
const (
    V0 AppVersion = 0
    V1 AppVersion = 1
    V2 AppVersion = 2
    V3 AppVersion = 3
    V4 AppVersion = 4           // ← 新增

    Current AppVersion = V4     // ← 更新
)
```

### 2. 注册升级步骤

`internal/upgrade/steps.go`：

```go
var steps = map[AppVersion]func(*sql.DB) error{
    V0: stepV0toV1,
    V1: stepV1toV2,
    V2: stepV2toV3,
    V3: stepV3toV4,             // ← 新增
}

func stepV3toV4(db *sql.DB) error {
    // SQL 变更
    _, err := db.Exec(`ALTER TABLE conversations ADD COLUMN tags TEXT`)
    if err != nil {
        return fmt.Errorf("add tags column: %w", err)
    }
    // 文件迁移
    // ...
    return nil
}
```

### 3. 测试

`internal/upgrade/upgrade_test.go` 中新增测试用例，模拟 V3→V4 升级路径。

## 升级步骤可包含的操作

| 操作 | 示例 |
|------|------|
| SQL schema 变更 | `db.Exec("ALTER TABLE ...")` |
| 文件迁移 | `os.Rename()`, `os.WriteFile()` |
| 配置更新 | 读写 `~/.p-chat/config.json` |
| 数据导入 | 从旧格式文件读取，写入新表 |
| 目录清理 | `os.RemoveAll()` |

## 注意事项

1. **幂等性**：步骤可能因崩溃而重复执行。使用 `CREATE TABLE IF NOT EXISTS`、`INSERT OR IGNORE` 等。
2. **顺序**：步骤按版本号严格顺序执行，不可跳步。
3. **断点续升**：每步成功后立即写 version 文件。崩溃后重启从断点继续。
4. **回滚**：当前不支持自动回滚。如需回滚，手动降级 version 文件 + 手动修复数据。
5. **不要绕过**：任何结构性变更都必须通过此系统。不要在其他地方写 ad-hoc 迁移代码。
