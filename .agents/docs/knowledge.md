# 知识库系统

> **P-Chat 本地知识库** — 零依赖、零运维的 Wiki/FTS5 全文检索系统。
> 替代方案（被否决）：向量嵌入 → 见 [`knowledge-task.md`](knowledge-task.md)（已废弃的 TAPD 计划）。

---

## 1. 设计决策

| 决策 | 理由 |
|------|------|
| **FTS5 而非 ES/向量** | 零依赖、零运维；`unicode61` tokenizer 天然支持中文 |
| **Markdown `##`/`###` 解析** | 零 API 消耗；结构化分段提升检索精度 |
| **SHA256 缓存** | 媒体描述 & LLM 文本总结均按内容哈希去重 |
| **mtime 增量扫描** | 仅处理变更文件，未变文件跳过 |
| **索引注入 prompt** | section 标题树嵌入系统提示，LLM 有目标地检索而非盲目探索 |

---

## 2. 系统架构

```
┌─────────────────────────────────────────────────────────┐
│ 前端                                                        │
│ ┌──────────────────┐  ┌──────────────────────────────────┐ │
│ │ AppSettingsModal  │  │ InputArea (KB 选择器)              │ │
│ │ · 知识库 Tab       │  │ · __off__ / __all__ / base 名      │ │
│ │ · 单卡片布局       │  │ · 写入 sessionMeta.knowledge_base  │ │
│ └──────┬───────────┘  └────────────┬─────────────────────┘ │
└────────┼───────────────────────────┼───────────────────────┘
         │ API 调用                    │ PATCH/POST session meta
         ▼                             ▼
┌─────────────────────────────────────────────────────────┐
│ Server API 层                                              │
│ ┌────────────────────┐  ┌──────────────────────────────┐ │
│ │ knowledge_api.go    │  │ handler.go                    │ │
│ │ · CRUD bases        │  │ · sessionMeta.KnowledgeBase  │ │
│ │ · scan lifecycle    │  │ · ChatRequest.KBBase         │ │
│ │ · media describe    │  │ · SSE chunkToEvent           │ │
│ └────────┬───────────┘  └────────────┬─────────────────┘ │
└──────────┼───────────────────────────┼───────────────────┘
           │                            ▼
           │              ┌─────────────────────────┐
           │              │ Agent 层                 │
           │              │ · ChatWithTools          │
           │              │ · 工具过滤 (KBBase=off)   │
           │              │ · buildKBIndex → prompt   │
           │              │ · resolveBases → tool     │
           │              └──────┬───────────────────┘
           │                     │ tool call
           ▼                     ▼
┌─────────────────────────────────────────────────────────┐
│ Tool 层                                                   │
│ ┌──────────────┐  ┌──────────────┐  ┌──────────────────┐ │
│ │ wiki_lookup   │  │ wiki_index   │  │ grep             │ │
│ │ · base 参数   │  │ · base 参数  │  │ · base 参数       │ │
│ │ · SearchFTS   │  │ · ListBase   │  │ · filepath.Walk  │ │
│ └──────┬───────┘  └──────┬───────┘  └────────┬─────────┘ │
└────────┼──────────────────┼──────────────────┼───────────┘
         │                  │                  │
         ▼                  ▼                  ▼
┌─────────────────────────────────────────────────────────┐
│ 存储层 (WikiStore)                                        │
│ ┌──────────────────────────────────────────────────────┐ │
│ │ wiki.db (SQLite)                                     │ │
│ │ ├── wiki_sections  (id, title, content, source,      │ │
│ │ │                    base, heading)                   │ │
│ │ ├── wiki_fts       (FTS5 VIRTUAL, tokenize=unicode61)│ │
│ │ ├── media_cache    (sha256 TEXT PK, content TEXT)    │ │
│ │ └── file_mtimes    (base, source, mtime)             │ │
│ └──────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────┘
```

---

## 3. 数据库表结构

### 3.1 `wiki_sections` — 知识条目

| 列 | 类型 | 说明 |
|------|------|------|
| `id` | INTEGER PK | 自增 |
| `title` | TEXT NOT NULL | `##` 标题 |
| `content` | TEXT NOT NULL | 段落正文（截断 800 字符返回） |
| `source` | TEXT NOT NULL | 来源文件相对路径 |
| `base` | TEXT NOT NULL | 所属知识库名 |
| `heading` | TEXT | 上级 `#` 标题（有则填充） |

### 3.2 `wiki_fts` — 全文搜索索引

```sql
CREATE VIRTUAL TABLE wiki_fts USING fts5(
    title, content, source, content='wiki_sections', content_rowid='id',
    tokenize='unicode61'
)
```

- FTS5 prefix match → phrase match → LIKE fallback（三阶段搜索策略）

### 3.3 `media_cache` — 媒体描述缓存

| 列 | 类型 | 说明 |
|------|------|------|
| `sha256` | TEXT PK | 文件内容 SHA256 |
| `content` | TEXT | LLM 生成的媒体描述 |

### 3.4 `file_mtimes` — 增量扫描时间戳

| 列 | 类型 | 说明 |
|------|------|------|
| `base` | TEXT | 知识库名 |
| `source` | TEXT | 文件相对路径 |
| `mtime` | INTEGER | Unix 时间戳 |

---

## 4. WikiStore API

### 4.1 生命周期

```go
func NewWikiStore(name, dir string) (*WikiStore, error)   // 打开/创建 wiki.db
func GetOrOpenWikiStore(cfg *config.Config, name string)   // 单例缓存
func (ws *WikiStore) Close() error                         // 关闭连接
```

### 4.2 数据写入

| 方法 | 签名 | 用途 |
|------|------|------|
| `ReplaceBase` | `(ctx, base string, sections []WikiSection)` | 全量重建一个知识库 |
| `ReplaceSource` | `(ctx, base, source string, sections []WikiSection)` | 增量更新单个文件 |
| `AppendSections` | `(ctx, sections []WikiSection)` | 追加条目（媒体扫描用） |
| `RemoveStaleSources` | `(ctx, base string, currentSources map[string]bool)` | 清理已删除文件的条目 |

### 4.3 数据读取

| 方法 | 签名 | 用途 |
|------|------|------|
| `SearchFTS` | `(ctx, query string, topK int) ([]WikiSection, error)` | FTS5 全文搜索 |
| `ListBase` | `(ctx, base string) ([]WikiSection, error)` | 列出知识库所有条目 |
| `Index` | `(ctx, maxSections int) (string, error)` | 构建索引文本（供 system prompt） |

### 4.4 增量扫描缓存

| 方法 | 签名 | 用途 |
|------|------|------|
| `GetFileMtime` | `(ctx, base, source string) (int64, error)` | 读取文件 mtime |
| `SetFileMtime` | `(ctx, base, source string, mtime int64) error` | 写入/更新 mtime |
| `CacheMediaDescription` | `(ctx, sha256, content string) error` | 缓存媒体 AI 描述 |
| `GetCachedMediaDescription` | `(ctx, sha256 string) (string, error)` | 读取缓存的描述 |

### 4.5 SearchFTS 三阶段策略

```
1. FTS5 prefix match:   SELECT ... WHERE wiki_fts MATCH 'title:q*' LIMIT topK
2. FTS5 phrase match:   SELECT ... WHERE wiki_fts MATCH '"q"'    LIMIT topK
3. LIKE fallback:       SELECT ... WHERE title LIKE '%q%'        LIMIT topK
```

---

## 5. 配置模型

### 5.1 KnowledgeBase

```go
type KnowledgeBase struct {
    Name           string   `json:"name"`             // 唯一标识
    Path           string   `json:"path"`             // 扫描目录绝对路径
    Enabled        bool     `json:"enabled"`          // 是否参与检索
    FileTypes      []string `json:"file_types"`       // 文本文件后缀
    ScanModel      string   `json:"scan_model"`       // "{provider}/{model}" 或 ""(text-only)
    ScanMediaTypes []string `json:"scan_media_types"` // ["image","video","audio","pdf"]
    AutoScan       bool     `json:"auto_scan"`        // 启动时自动扫描
    ExcludePatterns []string `json:"exclude_patterns"` // glob 排除模式
    MaxFileSize    int      `json:"max_file_size"`    // 0 = 默认 5MB
}
```

### 5.2 KnowledgeConfig

```go
type KnowledgeConfig struct {
    Enabled   bool            `json:"enabled"`    // 知识库系统总开关
    AutoIndex bool            `json:"auto_index"` // 启动时自动索引
    Bases     []KnowledgeBase `json:"bases"`      // 知识库列表
}
```

---

## 6. 扫描管道

### 6.1 入口

```
POST /api/v1/knowledge/bases/:name/scan
    ↓
startScanJob(name)  → goroutine
    ↓
wikiScan()  +  mediaScan()  (并行)
```

### 6.2 wikiScan — 文本文件扫描

```
walk directory
  ├── Phase 1: count indexable files (IndexableExtensions, skip .git/node_modules/vendor)
  └── Phase 2: process files
      ├── 读取文件
      ├── mtime 比对 → 未变则跳过
      ├── ParseWikiFile → 拆分为 sections
      │   ├── 有 ##/### 标题 → 按标题分段
      │   └── 无标题 → 整个文件作为单个 section
      ├── summarizeText (如果配置了 ScanModel)
      │   ├── 内容 SHA256 → 查 media_cache
      │   ├── 缓存命中 → 直接返回
      │   └── 缓存未命中 → 调用 LLM → 缓存结果 → 返回
      ├── ReplaceSource → upsert 到 wiki_sections + wiki_fts
      ├── SetFileMtime → 记录 mtime
      └── RemoveStaleSources → 清理已不存在的文件条目
```

### 6.3 mediaScan — 媒体文件扫描

```
walk directory
  ├── 匹配 ScanMediaTypes (image/video/audio/pdf)
  └── describeMediaFile
      ├── 读取文件 → SHA256
      ├── 查 media_cache
      ├── 缓存命中 → 返回描述 → AppendSections
      └── 缓存未命中 → base64 编码 → LLM 视觉模型 → 缓存 → 返回描述 → AppendSections
```

### 6.4 媒体类型映射

```go
var MediaTypeExtensions = map[string][]string{
    "image": {".png", ".jpg", ".jpeg", ".gif", ".bmp", ".webp", ".svg", ".ico", ".tiff", ".tif"},
    "video": {".mp4", ".mov", ".avi", ".mkv", ".webm", ".wmv", ".flv", ".m4v"},
    "audio": {".mp3", ".wav", ".ogg", ".flac", ".aac", ".m4a", ".wma", ".opus"},
    "pdf":   {".pdf"},
}
```

### 6.5 扫描生命周期

| API 端点 | 用途 |
|----------|------|
| `POST /bases/:name/scan` | 启动扫描（自动启用 base） |
| `GET /bases/:name/scan/status` | 轮询进度 (800ms 前端轮询) |
| `DELETE /bases/:name/scan` | 取消扫描 |

- 每个知识库最多一个活跃扫描 job
- 30 分钟超时自动取消
- 进度通过 `sync.Map` 共享

---

## 7. 知识库 API 端点总览

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/api/v1/knowledge/config` | 获取完整 KnowledgeConfig |
| `PATCH` | `/api/v1/knowledge/config` | 部分更新配置 |
| `GET` | `/api/v1/knowledge/models` | 列出可用的 scan 模型（含 `supports_vision`） |
| `GET` | `/api/v1/knowledge/bases` | 列出所有基座（含扫描状态、文档数） |
| `POST` | `/api/v1/knowledge/bases` | 新增知识库（需 name + path） |
| `DELETE` | `/api/v1/knowledge/bases/:name` | 删除知识库 |
| `POST` | `/api/v1/knowledge/bases/:name/scan` | 启动扫描 |
| `GET` | `/api/v1/knowledge/bases/:name/scan/status` | 查询扫描进度 |
| `DELETE` | `/api/v1/knowledge/bases/:name/scan` | 取消扫描 |
| `POST` | `/api/v1/knowledge/search` | 跨库 FTS5 + grep 联合搜索 |

---

## 8. 工具实现

### 8.1 `wiki_lookup`

```
参数: title (required), top_k (default 5, max 10), base (optional)
流程: resolveBases(kc, base) → 遍历 bases → SearchFTS → 合并结果 → 截断返回
```

| 返回值 | IsError | LLM 行为 |
|--------|---------|----------|
| 检索到条目 | 无 | LLM 总结回答 |
| 未找到匹配 | 无 | LLM 告知"没找到" |
| title 为空 | `true` | LLM 调整参数重试 |
| base 找不到 | `true` | LLM 告知用户 |
| DB 错误 | `true` | LLM 可能重试 |

### 8.2 `wiki_index`

```
参数: source (optional 按文件筛选), base (optional)
流程: resolveBases → ListBase → 按 source 分组 → 返回标题列表
```

> **注意**: `"知识库为空，尚未扫描"` 当前标记为 `IsError: true`，这属于设计不一致——空索引是正常状态，应改为 `IsError: false`。

### 8.3 `grep`

```
参数: pattern (required), top_k (default 10, max 20), base (optional)
流程: resolveBases → 遍历目录 → filepath.Walk → 逐行搜索 → 过滤 .git/node_modules/vendor
```

- 跳过 >5MB 文件
- 大小写不敏感
- 返回 `knowledge.SearchResult{Souce: "path:line", Content, Similarity: 1.0}`

### 8.4 `resolveBases`

```go
func resolveBases(kc *KnowledgeConfig, name string) []KnowledgeBase
// name == ""     → 全部启用的 base
// name == "__all__" → 全部启用的 base
// 其他         → 按名匹配单个已启用 base
```

---

## 9. 对话级 KB 集成

### 9.1 数据流

```
InputArea KB 选择器
    ↓ sessionMeta.knowledge_base = "__off__" / "__all__" / "base_name"
    ↓ PATCH /api/v1/sessions/:id
handler.UpdateSessionMeta
    ↓ 写入 h.meta[id].KnowledgeBase
    ↓ 持久化到 DB (sessionMetaBlob)
POST /api/v1/sessions/:id/messages
    ↓ chatReq.KBBase = meta.KnowledgeBase
agent.ChatWithTools(req)
    ↓
    ├── KBBase == "" || "__off__"
    │   ├── 移除 wiki_lookup / wiki_index / grep 工具
    │   └── 不注入 buildKBIndex 到 system prompt
    └── KBBase == "__all__" / "name"
        ├── 保留全部 wiki 工具
        └── buildKBIndex() → 注入索引到 system prompt
```

### 9.2 `buildKBIndex` — 索引注入

```go
func buildKBIndex(cfg *config.Config, kbBase string) string
```

1. 解析 `kbBase`，打开对应 `WikiStore`
2. 调用 `ListBase` 获取所有 sections
3. 格式化为 `[Knowledge Base: xxx]` 前缀 + 按 source 分组的标题树
4. 截断至 3000 字符
5. 追加到 system prompt 末尾

```text
[Knowledge Base: my-docs] (path/to/docs, 120 sections)

source: api.md
  - 用户认证接口
    - JWT 令牌验证
    - OAuth 2.0 集成
  - SSE 流式响应
    ...

source: guide.md
  - 快速开始
  - 配置说明
    ...
```

### 9.3 工具过滤逻辑

```go
// agent.go:ChatWithTools
if req.KBBase == "" || req.KBBase == "__off__" {
    // 从 tools 中移除 "wiki_lookup", "wiki_index", "grep"
}
```

---

## 10. 工具错误处理

### 10.1 错误传递路径

```
工具返回 error
    ↓
agent.go 工具派发器
    roundAnyToolErrored = true
    ChatMessage{ToolError: true}
    Content = "Tool xxx returned an error: ..."
    ↓
LLM 收到错误 → 自行决定停止/调整重试
    ↓
下一轮继续（除非 stuck-loop）
```

### 10.2 stuck-loop 检测

| 条件 | 行为 |
|------|------|
| 单次工具报错 | 不终止，结果反馈给 LLM 继续 |
| 同一工具 + 同一错误，连续 3 轮 | 触发 stuck-loop → `Phase: "stuck"` → `Done: true` |
| LLM 自己停止 | 正常结束 |
| 达到 50 轮上限 | `Phase: "limit"` → 正常结束 |

### 10.3 结论

- 工具报错不会终止对话
- 不存在 NPE / panic
- stuck-loop 有 3 轮宽容期
- LLM 总能得到错误详情并决策

---

## 11. 前端实现

### 11.1 知识库 Tab (`AppSettingsModal.vue`)

单卡片布局，所有设置合并到 `.kb-detail-scroll`：

```
┌─────────────────────────────────────────────┐
│ 基本信息                                     │
│  名称 [________]  路径 [________]            │
│  启用 [toggle]                               │
├─────────────────────────────────────────────┤
│ AI 设置                                      │
│  Scan Model [dropdown]                       │
│  Media Types [multi-select]                  │
├─────────────────────────────────────────────┤
│ 扫描                                        │
│  [Scan Button]  [progress bar]  [Cancel]     │
│  已索引: 120 sections                        │
├─────────────────────────────────────────────┤
│ 高级                                         │
│  Auto Scan [toggle]                         │
│  Exclude Patterns [comma-sep input]          │
│  Max File Size [number input] MB             │
└─────────────────────────────────────────────┘
```

- 左侧 bases 列表：名称、状态 (ready/scanning/error)、section 计数
- 点击扫描按钮自动启用 base
- 扫描期间 800ms 轮询 `GET /scan/status`

### 11.2 KB 选择器 (`InputArea.vue`)

```
[不使用 ▾]
  不使用   (__off__)
  全部     (__all__)
  my-docs  (已扫描, 120 sections)
  project  (已扫描, 45 sections)
```

- `kbBases` 从 `api.getKnowledgeBases()` 加载
- 选择结果写入 `sessionMeta.knowledge_base`
- 每对话独立选择

### 11.3 类型定义 (`client.ts`)

```ts
interface KnowledgeBaseItem {
  name: string
  path: string
  enabled: boolean
  file_types?: string[]
  scan_model?: string
  scan_media_types?: string[]
  auto_scan?: boolean
  exclude_patterns?: string[]
  max_file_size?: number
  status?: string        // "ready" | "scanning" | "error"
  doc_count?: number      // sections 计数
}

interface Session {
  // ...
  knowledge_base?: string
}
```

---

## 12. 配置持久化层

### 12.1 `UpdateKnowledgeConfig`

```go
func UpdateKnowledgeConfig(patch KnowledgeConfig) *config.Config
```

- Load → merge → SaveGlobal
- 支持部分更新（只传需修改的字段）

### 12.2 `AddKnowledgeBaseRecord` / `RemoveKnowledgeBaseRecord`

```go
func AddKnowledgeBaseRecord(base KnowledgeBase) *config.Config
func RemoveKnowledgeBaseRecord(name string) *config.Config
```

- 新增 base：名称唯一性校验
- 删除 base：按名查找并移除

---

## 13. 关键文件索引

| 文件 | 内容 |
|------|------|
| `internal/knowledge/wiki_store.go` | WikiStore — SQLite FTS5 存储引擎 |
| `internal/knowledge/wiki_parser.go` | Markdown `##`/`###` 解析器 |
| `internal/knowledge/media_types.go` | 媒体类型→扩展名映射 |
| `internal/tool/wiki.go` | `wiki_lookup` + `wiki_index` 工具实现 |
| `internal/tool/grep.go` | `grep` 工具实现 |
| `internal/server/knowledge_api.go` | 知识库 CRUD + 扫描管道 + API 端点 |
| `internal/server/handler.go` | sessionMeta/KnowledgeBase 流、SSE 映射 |
| `internal/agent/agent.go` | ChatWithTools、buildKBIndex、工具过滤、错误处理 |
| `internal/config/config.go` | KnowledgeBase / KnowledgeConfig 类型定义 |
| `internal/config/knowledge_config.go` | 配置持久化操作 |
| `internal/recall/stub.go` | recall 工具 stub（CLI 编译通过用） |
| `frontend/src/components/AppSettingsModal.vue` | 知识库 Tab 单卡片 UI |
| `frontend/src/components/InputArea.vue` | KB 选择器 UI |
| `frontend/src/api/client.ts` | 前端类型定义 + API 调用 |
| `frontend/src/stores/chat.ts` | sessionMeta 状态管理 |

---

## 14. 待办项

- [ ] `wiki_index` 空索引时 `IsError: true` → 应改为 `IsError: false`
- [ ] `resolveBases("")` 返回全部启用 base（与 `"__all__"` 相同），可能导致 LLM 未传 base 时意外搜全部
- [ ] `buildKBIndex` 使用 `context.Background()` 而非传入 ctx
- [ ] `knowledge_api.go` 未跟踪（git `??`）
