# 知识库系统

> **P-Chat 本地知识库** — 零依赖、零运维的 Wiki/FTS5 全文检索 + 三层索引树系统。
> 替代方案（被否决）：向量嵌入 → 见 [`knowledge-task.md`](knowledge-task.md)（已废弃的 TAPD 计划）。

---

## 1. 设计决策

| 决策 | 理由 |
|------|------|
| **FTS5 而非 ES/向量** | 零依赖、零运维；`unicode61` + CJK 单字分词 |
| **三层索引树** | L1(root)→L2(file)→L3(section)→Content 层级结构，LLM 按需展开 |
| **Markdown `##`/`###` 解析** | 零 API 消耗；结构化分段提升检索精度 |
| **SHA256 缓存** | 媒体描述 & LLM 文本总结均按内容哈希去重 |
| **mtime 增量扫描** | 仅处理变更文件，未变文件跳过 |
| **索引注入 prompt** | L1 级 overview 嵌入系统提示（≤2000 字符），LLM 有目标地检索而非盲目探索 |
| **双表共存** | 旧 `wiki_sections` + 新 `index_nodes` 并存，向后兼容，idempotent 迁移 |

---

## 2. 系统架构

```
┌─────────────────────────────────────────────────────────┐
│ 前端                                                        │
│ ┌──────────────────────┐  ┌────────────────────────────┐ │
│ │ AppSettingsModal      │  │ InputArea (KB 选择器)        │ │
│ │ · 知识库 Tab           │  │ · __off__ / __all__ / base   │ │
│ │ · 三层树视图            │  │ · 写入 sessionMeta.knowledge │ │
│ │ · 扫描/清除按钮         │  │   _base                     │ │
│ └──────┬───────────────┘  └────────────┬─────────────────┘ │
└────────┼───────────────────────────────┼───────────────────┘
         │ API 调用                        │ PATCH/POST session meta
         ▼                                 ▼
┌─────────────────────────────────────────────────────────┐
│ Server API 层                                              │
│ ┌────────────────────┐  ┌──────────────────────────────┐ │
│ │ knowledge_api.go    │  │ handler.go                    │ │
│ │ · CRUD bases        │  │ · sessionMeta.KnowledgeBase  │ │
│ │ · scan lifecycle    │  │ · ChatRequest.KBBase         │ │
│ │ · indexScan 管道    │  │ · SSE chunkToEvent           │ │
│ │ · ListNodes/nodes   │  │                              │ │
│ │ · ClearBase         │  │                              │ │
│ └────────┬───────────┘  └────────────┬─────────────────┘ │
└──────────┼───────────────────────────┼───────────────────┘
           │                            ▼
           │              ┌─────────────────────────┐
           │              │ Agent 层                 │
           │              │ · ChatWithTools          │
           │              │ · 4 层 KB off 守卫       │
           │              │ · buildKBIndex(L1 overview)│
           │              │ · resolveBases → tool     │
           │              └──────┬───────────────────┘
           │                     │ tool call
           ▼                     ▼
┌─────────────────────────────────────────────────────────┐
│ Tool 层                                                   │
│ ┌──────────────┐  ┌──────────────┐  ┌────────────────┐ │
│ │ wiki_lookup   │  │ wiki_list    │  │ grep           │ │
│ │ · query/base  │  │ · parent_id  │  │ · pattern/base │ │
│ │ · 5 策略排序  │  │ · 分页       │  │ · filepath.Walk│ │
│ └──────┬───────┘  └──────┬───────┘  └────────┬───────┘ │
└────────┼──────────────────┼──────────────────┼─────────┘
         │                  │                  │
         ▼                  ▼                  ▼
┌─────────────────────────────────────────────────────────┐
│ 存储层 (WikiStore)                                        │
│ ┌──────────────────────────────────────────────────────┐ │
│ │ wiki.db (SQLite) — 每知识库独立文件                   │ │
│ │ ├── wiki_sections   (旧表，向后兼容)                  │ │
│ │ ├── wiki_fts        (旧 FTS5，向后兼容)               │ │
│ │ ├── index_nodes     (★ 三层索引节点)                  │ │
│ │ ├── contents        (★ 节点内容块)                    │ │
│ │ ├── index_fts       (★ 新 FTS5，CJK 分词，触发器同步) │ │
│ │ ├── media_cache     (sha256 TEXT PK, content TEXT)    │ │
│ │ └── file_mtimes     (base, source, mtime)             │ │
│ └──────────────────────────────────────────────────────┘ │
└─────────────────────────────────────────────────────────┘
```

---

## 3. 数据库表结构

### 3.1 `wiki_sections` — 旧知识条目（向后兼容）

| 列 | 类型 | 说明 |
|------|------|------|
| `id` | INTEGER PK | 自增 |
| `title` | TEXT NOT NULL | `##` 标题 |
| `content` | TEXT NOT NULL | 段落正文 |
| `source` | TEXT NOT NULL | 来源文件相对路径 |
| `base` | TEXT NOT NULL | 所属知识库名 |
| `heading` | TEXT | 上级 `#` 标题 |

### 3.2 `wiki_fts` — 旧 FTS5 索引

```sql
CREATE VIRTUAL TABLE wiki_fts USING fts5(
    title, content, source, content='wiki_sections', content_rowid='id',
    tokenize='unicode61'
)
```

### 3.3 `index_nodes` — 三层索引节点（★ 新）

| 列 | 类型 | 说明 |
|------|------|------|
| `id` | INTEGER PK | 自增 |
| `parent_id` | INTEGER | 父节点 ID（L1=0） |
| `base` | TEXT NOT NULL | 所属知识库名 |
| `level` | INTEGER NOT NULL | 1=root, 2=file, 3=section |
| `source` | TEXT | 相对路径（L2/L3） |
| `kind` | TEXT | text/image/pdf/audio/video |
| `sort_order` | INTEGER | 排序权重 |
| `title` | TEXT | 节点标题 |
| `keywords` | TEXT | 逗号分隔关键词（AI 解析） |
| `overview` | TEXT | 1-3 句摘要（AI 解析） |

层级关系：`L1 (base 概览) → L2 (文件) → L3 (章节/标题) → Content (叶子)`

### 3.4 `contents` — 内容叶子节点（★ 新）

| 列 | 类型 | 说明 |
|------|------|------|
| `id` | INTEGER PK | 自增 |
| `node_id` | INTEGER | 父 L3 节点 ID |
| `content` | TEXT | 内容正文 |
| `content_type` | TEXT | text/image_description/audio_transcript |
| `sort_order` | INTEGER | 排序权重 |

### 3.5 `index_fts` — 新 FTS5 索引（★ 新）

```sql
CREATE VIRTUAL TABLE index_fts USING fts5(
    title, keywords, overview, content='index_nodes', content_rowid='id'
)
```

- **FTS 同步机制**：通过 INSERT/UPDATE/DELETE 触发器自动同步
- **只索引 level≥2 的节点**（L1 不在 FTS 范围内）
- **CJK 分词**：写入前通过 `tokenizeForFTS()` 将中文字符逐一分离（如 `"用户认证"` → `"用 户 认 证"`），实现前缀匹配

### 3.6 `media_cache` — 媒体描述缓存

| 列 | 类型 | 说明 |
|------|------|------|
| `sha256` | TEXT PK | 文件内容 SHA256 |
| `content` | TEXT | LLM 生成的媒体描述 |

### 3.7 `file_mtimes` — 增量扫描时间戳

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

### 4.2 旧表数据写入（wiki_sections）

| 方法 | 签名 | 用途 |
|------|------|------|
| `ReplaceBase` | `(ctx, base string, sections []WikiSection)` | 全量重建 |
| `ReplaceSource` | `(ctx, base, source string, sections []WikiSection)` | 增量更新文件 |
| `AppendSections` | `(ctx, sections []WikiSection)` | 追加条目 |
| `RemoveStaleSources` | `(ctx, base string, currentSources map[string]bool)` | 清理删除 |

### 4.3 旧表数据读取

| 方法 | 签名 | 用途 |
|------|------|------|
| `SearchFTS` | `(ctx, query string, topK int) ([]WikiSection, error)` | FTS5 全文搜索 |
| `ListBase` | `(ctx, base string) ([]WikiSection, error)` | 列出所有条目 |
| `Index` | `(ctx, maxSections int) (string, error)` | 构建索引文本 |

### 4.4 三层索引操作（★ 新）

| 方法 | 签名 | 用途 |
|------|------|------|
| `MigrateBaseToIndex` | `(ctx, base string) (bool, error)` | 创建 index_nodes/contents/index_fts 表，幂等 |
| `InsertNode` | `(ctx, *IndexNode) (int64, error)` | 写入节点 → 触发器自动同步 FTS |
| `InsertContent` | `(ctx, *ContentNode) (int64, error)` | 写入内容块 |
| `LookupSearch` | `(ctx, query, base string, expand bool, level, page, size int)` | 多策略排序搜索 |
| `ListChildren` | `(ctx, parentID, page, size int)` | 分页列出子节点 |
| `ListNodes` | `(ctx, base string) ([]NodeTreeItem, error)` | 返回整棵树的扁平列表（含 child_count/content_count） |
| `GetNodeContent` | `(ctx, nodeID int) ([]ContentNode, error)` | 读取节点的内容块 |
| `GetL1Overview` | `(ctx, base string) (string, error)` | 读取 L1 概览 |
| `ClearBase` | `(ctx, base string) error` | 清除知识库所有数据（wiki_sections + index 全表） |

### 4.5 增量扫描缓存

| 方法 | 签名 | 用途 |
|------|------|------|
| `GetFileMtime` | `(ctx, base, source string) (int64, error)` | 读取 mtime |
| `SetFileMtime` | `(ctx, base, source string, mtime int64) error` | 写入 mtime |

### 4.6 LookupSearch — 5 策略排序（★ 新）

```
权重排序（高→低）:
  1. title   (权重 1.0) — FTS5 标题精确匹配
  2. keywords(权重 0.8) — FTS5 关键词匹配
  3. overview(权重 0.6) — FTS5 摘要匹配
  4. L2      (权重 0.4) — 命中 L3 section → 回退到 L2 parent
  5. LIKE    (权重 0.2) — contents LIKE '%query%' 兜底
```

支持多 base 搜索：每个 base 查 page=1/size=200 → 合并 → 跨库 re-rank → 切片返回请求页。

---

## 5. 配置模型

### 5.1 KnowledgeBase

```go
type KnowledgeBase struct {
    Name            string   `json:"name"`
    Path            string   `json:"path"`
    Enabled         bool     `json:"enabled"`
    FileTypes       []string `json:"file_types"`
    ScanModel       string   `json:"scan_model"`        // "{provider}/{model}" 或 ""(text-only)
    ScanMediaTypes  []string `json:"scan_media_types"`   // ["image","video","audio","pdf"]
    AutoScan        bool     `json:"auto_scan"`
    ExcludePatterns []string `json:"exclude_patterns"`
    MaxFileSize     int      `json:"max_file_size"`     // 0 = 默认 5MB
}
```

### 5.2 KnowledgeConfig

```go
type KnowledgeConfig struct {
    Enabled   bool            `json:"enabled"`
    AutoIndex bool            `json:"auto_index"`
    Bases     []KnowledgeBase `json:"bases"`
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
wikiScan()  +  mediaScan()  +  indexScan()  (并行)
```

### 6.2 wikiScan — 文本文件扫描（旧管道）

```
walk directory
  ├── Phase 1: count indexable files
  └── Phase 2: process files
      ├── 读取文件
      ├── mtime 比对 → 跳过未变文件
      ├── ParseWikiFile → 按 ##/### 拆分
      ├── summarizeText (如配了 ScanModel → LLM 总结 + SHA256 缓存)
      ├── ReplaceSource → upsert wiki_sections + wiki_fts
      ├── SetFileMtime
      └── RemoveStaleSources
```

### 6.3 indexScan — 三层索引扫描（★ 新管道）

```
walk directory
  └── process files:
      ├── EnsureMigrated → 建表 (幂等)
      ├── 解析 Markdown ##/### → 拆分为段
      ├── AI 解析 (如配了 ScanModel):
      │   ├── prompt = "为以下段落生成 JSON: {title,keywords,overview}"
      │   ├── parseKWAndOverview → JSON parse → text pattern fallback
      │   └── TruncateText(overview, 800)
      ├── 构建三层结构:
      │   ├── L1 (root): 一个 per-base，Title=base name, Overview=base 摘要
      │   ├── L2 (file): 一个 per-文件，Title=文件名, Source=路径, Kind=text/image/...
      │   └── L3 (section): 一个 per-段落，Title=标题, Keywords/Overview=AI 解析结果
      ├── InsertNode(L2) + InsertNode(L3) + InsertContent(段落正文)
      ├── tokenizeForFTS → FTS5 触发器自动同步
      └── SetFileMtime
```

### 6.4 mediaScan — 媒体文件扫描

```
walk directory
  ├── 匹配 ScanMediaTypes
  └── describeMediaFile
      ├── SHA256 → 查 media_cache
      ├── 缓存命中 → AppendSections（旧表）
      └── 缓存未命中 → base64 → LLM 视觉 → 缓存 → AppendSections
```

### 6.5 媒体类型映射

```go
var MediaTypeExtensions = map[string][]string{
    "image": {".png", ".jpg", ".jpeg", ".gif", ".bmp", ".webp", ".svg", ".ico", ".tiff", ".tif"},
    "video": {".mp4", ".mov", ".avi", ".mkv", ".webm", ".wmv", ".flv", ".m4v"},
    "audio": {".mp3", ".wav", ".ogg", ".flac", ".aac", ".m4a", ".wma", ".opus"},
    "pdf":   {".pdf"},
}
```

### 6.6 扫描生命周期

| API 端点 | 用途 |
|----------|------|
| `POST /bases/:name/scan` | 启动扫描 |
| `GET /bases/:name/scan/status` | 轮询进度 |
| `DELETE /bases/:name/scan` | 取消扫描 |
| `DELETE /bases/:name/clear` | 清除所有扫描数据 |

- 每个知识库最多一个活跃 scan job
- 30 分钟超时自动取消
- 进度通过 `sync.Map` 共享，前端 800ms 轮询

---

## 7. 知识库 API 端点总览

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/api/v1/knowledge/config` | 获取 KnowledgeConfig |
| `PATCH` | `/api/v1/knowledge/config` | 部分更新 |
| `GET` | `/api/v1/knowledge/models` | 列出 scan 模型 |
| `GET` | `/api/v1/knowledge/bases` | 列出基座（含状态、文档数） |
| `POST` | `/api/v1/knowledge/bases` | 新增知识库 |
| `DELETE` | `/api/v1/knowledge/bases/:name` | 删除知识库 |
| `POST` | `/api/v1/knowledge/bases/:name/scan` | 启动扫描 |
| `GET` | `/api/v1/knowledge/bases/:name/scan/status` | 扫描进度 |
| `DELETE` | `/api/v1/knowledge/bases/:name/scan` | 取消扫描 |
| `DELETE` | `/api/v1/knowledge/bases/:name/clear` | ★ 清除扫描数据 |
| `GET` | `/api/v1/knowledge/bases/:name/sections` | 旧表条目列表 |
| `POST` | `/api/v1/knowledge/bases/:name/sections` | 新增条目 |
| `GET` | `/api/v1/knowledge/bases/:name/sections/:id` | 条目详情 |
| `DELETE` | `/api/v1/knowledge/bases/:name/sections/:id` | 删除条目 |
| `GET` | `/api/v1/knowledge/bases/:name/nodes` | ★ 三层索引节点列表 |
| `GET` | `/api/v1/knowledge/bases/:name/nodes/:id/content` | ★ 节点内容块 |
| `POST` | `/api/v1/knowledge/search` | 跨库 FTS5 + grep 联合搜索 |

---

## 8. 工具实现

### 8.1 `wiki_lookup`（★ 重写）

```
参数: query (required), base (optional), expand (bool), level (int), page (int), size (int)
流程: resolveBases → 遍历 bases → LookupSearch 5策略排序 → 合并 → re-rank → 分页返回
```

| 参数 | 默认值 | 最大 | 说明 |
|------|--------|------|------|
| `query` | (必填) | — | 搜索关键词 |
| `base` | "" | — | "__all__" = 全库, "" = 自动 |
| `level` | 0 | 3 | 0=所有层级, 2=文件, 3=章节 |
| `page` | 1 | — | 分页页码 |
| `size` | 20 | 50 | 每页条数 |
| `expand` | false | — | 展开 children + parent 引用 |

### 8.2 `wiki_list`（★ 替代 wiki_index）

```
参数: parent_id (optional), page (1), size (50, max 100)
流程: resolveBases → ListChildren(parent_id, page, size) → 返回子节点列表
```

- `parent_id=0` → 列出所有 L1（root）节点
- 用于树形浏览，LLM 按需展开子节点

### 8.3 `grep`

```
参数: pattern (required), top_k (10, max 20), base (optional)
流程: resolveBases → filepath.Walk → 逐行搜索 → 过滤 .git/node_modules/vendor
```

- 跳过 >5MB 文件
- 大小写不敏感
- **注意**: grep 不是 KB 专用工具，不受 KB off 开关影响

### 8.4 `resolveBases`

```go
func resolveBases(kc *KnowledgeConfig, name string) []KnowledgeBase
// name == ""       → 全部启用的 base
// name == "__all__" → 全部启用的 base
// 其他             → 按名匹配单个已启用 base
```

---

## 9. 对话级 KB 集成

### 9.1 数据流

```
InputArea KB 选择器
    ↓ sessionMeta.knowledge_base = "__off__" / "__all__" / "base_name"
POST /api/v1/sessions/:id/messages
    ↓ chatReq.KBBase = meta.KnowledgeBase
agent.ChatWithTools(req)
    ↓
    ├── KBBase == "" || "__off__"
    │   ├── 移除 wiki_lookup / wiki_list 工具
    │   ├── 历史消息中 KB 相关 tool_call 过滤
    │   ├── dispatch 守卫：拒绝 wiki 工具调用
    │   └── 不注入 buildKBIndex
    └── KBBase == "__all__" / "name"
        ├── 保留所有 wiki 工具
        └── buildKBIndex() → L1 overview → system prompt
```

### 9.2 KB off 4 层守卫（★ 新）

| 层 | 位置 | 机制 |
|----|------|------|
| 1. system prompt | `agent.go:buildKBIndex` | KB off → 不注入索引 |
| 2. 工具列表 | `agent.go:ChatWithTools` | `availableTools` 移除 wiki_lookup/wiki_list |
| 3. 历史消息 | `agent.go:ChatWithTools` | 过滤含 KB tool_call 的旧消息 |
| 4. 工具派发 | `agent.go` dispatch | 运行时拒绝透传的 wiki 工具调用 |

### 9.3 `buildKBIndex` — L1 overview 注入（★ 重写）

```go
func buildKBIndex(cfg *config.Config, kbBase string) string
```

1. 解析 `kbBase`，获取 L1 node overview（通过 `GetL1Overview`）
2. 格式化为 1-2 句简介，追加到 system prompt 末尾
3. 截断至 2000 字符
4. 缓存 30s（L1 overview 缓存），扫描/清除后 `Reload()` 刷新

```text
[Knowledge Base: my-docs] (path/to/docs)
包含 45 个文件，12 个章节的 API 文档、开发指南和架构说明。
```

### 9.4 工具过滤逻辑

```go
// agent.go:ChatWithTools
if req.KBBase == "" || req.KBBase == "__off__" {
    // 从 tools 中移除 "wiki_lookup", "wiki_list"
    // grep 保留 — 它是通用搜索工具
}
```

---

## 10. 工具错误处理

### 10.1 错误传递路径

```
工具返回 error → agent.go 派发器
    roundAnyToolErrored = true
    ChatMessage{ToolError: true}
    Content = "Tool xxx returned an error: ..."
    ↓
LLM 收到错误 → 自行决策
    ↓
下一轮继续（除非 stuck-loop）
```

### 10.2 stuck-loop 检测

| 条件 | 行为 |
|------|------|
| 单次报错 | 不终止，反馈 LLM 继续 |
| 同工具+同错误 x3 | stuck-loop → `Phase: "stuck"` → `Done: true` |
| LLM 自行停止 | 正常结束 |
| 达到 50 轮 | `Phase: "limit"` → 正常结束 |

---

## 11. 前端实现

### 11.1 知识库 Tab（`AppSettingsModal.vue`）

左右分栏布局：

```
┌─ Provider list (左) ──┬─ Detail pane (右) ───────────────┐
│ 知识库 (N)             │ ┌────────────────────────────────┐│
│ ┌────────────────────┐ │ │ 名称  启用标签   N 条索引      ││
│ │ ✓ my-docs          │ │ │ [扫描] [清除]                  ││
│ │ ✗ project (禁用)   │ │ └────────────────────────────────┘│
│ └────────────────────┘ │ ┌─ 扫描进度 ─────────────────────┐│
│                        │ │ 扫描中 57/120      [取消]      ││
│                        │ │ ████████░░░░░░░░ 47%          ││
│                        │ └────────────────────────────────┘│
│                        │ ┌─ 基本信息 ─────────────────────┐│
│                        │ │ 路径  /path/to/docs            ││
│                        │ │ 状态  [toggle]                 ││
│                        │ └────────────────────────────────┘│
│                        │ ┌─ AI 扫描设置 (NCollapse) ──────┐│
│                        │ │ 模型 / 媒体类型 / 自动扫描...  ││
│                        │ └────────────────────────────────┘│
│                        │ ┌─ 索引节点 (树视图) ────────────┐│
│                        │ │ L1: my-docs                    ││
│                        │ │   45 文件, 12 章节...          ││
│                        │ │                               ││
│                        │ │ ▸ api.md  text  5 章节        ││
│                        │ │   ├── 用户认证接口             ││
│                        │ │   │   2 块                     ││
│                        │ │   │   支持 JWT 和 OAuth...     ││
│                        │ │   └── SSE 流式响应             ││
│                        │ │       1 块                     ││
│                        │ │ ▸ guide.md  text  3 章节      ││
│                        │ └────────────────────────────────┘│
│                        │ 原始条目 (wiki_sections cards)    │
│                        │ ┌───┐ ┌───┐ ┌───┐             ││
│                        │ │ S1│ │ S2│ │ S3│             ││
│                        │ └───┘ └───┘ └───┘             ││
└────────────────────────┴────────────────────────────────────┘
```

**三层树视图交互**：
- 点击 L2 文件行 → 展开/折叠 → 箭头旋转动画
- 展开时按需加载 `contents` 内容块（API: `getNodeContent`）
- L3 节点显示 title + overview + content count
- L1 节点以卡片形式展示 overview 概览

### 11.2 KB 选择器（`InputArea.vue`）

```
[不使用 ▾]
  不使用   (__off__)
  全部     (__all__)
  my-docs  (45 files, 120 sections)
  project  (已扫描, 45 sections)
```

### 11.3 类型定义（`client.ts`）

```ts
interface KnowledgeBaseItem {
  name: string; path: string; enabled: boolean
  file_types?: string[]; scan_model?: string
  scan_media_types?: string[]; auto_scan?: boolean
  exclude_patterns?: string[]; max_file_size?: number
  status?: string; doc_count?: number
}

// ★ 三层索引节点
interface NodeTreeItem {
  id: number; parent_id: number; level: number
  title: string; keywords: string; overview: string
  source: string; kind: string
  child_count: number; content_count: number
}

interface NodeContentItem {
  id: number; node_id: number
  content: string; content_type: string
  sort_order: number
}
```

---

## 12. 配置持久化层

### 12.1 `UpdateKnowledgeConfig`

```go
func UpdateKnowledgeConfig(patch KnowledgeConfig) *config.Config
```

- Load → merge → SaveGlobal
- 支持部分更新

### 12.2 `AddKnowledgeBaseRecord` / `RemoveKnowledgeBaseRecord`

```go
func AddKnowledgeBaseRecord(base KnowledgeBase) *config.Config
func RemoveKnowledgeBaseRecord(name string) *config.Config
```

---

## 13. 清理功能（ClearBase）

```
DELETE /api/v1/knowledge/bases/:name/clear
    ↓
WikiStore.ClearBase(ctx, base)
    ├── DELETE wiki_fts (手动，因为是 content= 外部 FTS)
    ├── DELETE wiki_sections
    ├── DELETE contents (先删子表)
    ├── DELETE index_nodes (触发器自动清 index_fts)
    └── DELETE file_mtimes
    ↓
agent.Reload() — 刷新 L1 overview 缓存
```

前端：NPopconfirm "确定清除知识库「xxx」的所有扫描数据？此操作不可撤销。" → NButton "确定清除"

---

## 14. 迁移机制（EnsureMigrated）

```
pchat-server 启动
    └── EnsureMigrated(cfg.Knowledge.Bases)
        └── for each base:
            └── store.MigrateBaseToIndex(ctx, base)
                ├── CREATE TABLE IF NOT EXISTS index_nodes ...
                ├── CREATE TABLE IF NOT EXISTS contents ...
                ├── CREATE TABLE IF NOT EXISTS index_fts ...
                ├── CREATE TRIGGER IF NOT EXISTS fts_ins ...
                ├── CREATE TRIGGER IF NOT EXISTS fts_del ...
                └── CREATE TRIGGER IF NOT EXISTS fts_upd ...
```

- **幂等**：所有语句使用 `IF NOT EXISTS`
- **每库独立**：wiki.db 在知识库目录下独立管理
- **无需版本号**：表和触发器通过 SQL 自身保证幂等

---

## 15. 辅助函数

| 函数 | 位置 | 用途 |
|------|------|------|
| `tokenizeForFTS(s string)` | `wiki_store.go` | CJK 字符逐一分离 → FTS5 前缀匹配 |
| `TruncateText(s string, n int)` | `wiki_store.go` (导出) | 截断 UTF-8 安全文本 |
| `parseKWAndOverview(content)` | `knowledge_api.go` | AI 输出解析 → JSON → text pattern fallback |

---

## 16. 关键词 / 概览解析策略（parseKWAndOverview）

```
输入: LLM 返回的原始文本
    ↓
尝试 1: JSON 解析
    {"keywords": "a, b, c", "overview": "...", "summary": "..."}
    ↓ 失败
尝试 2: 文本模式匹配
    关键词/关键字: ...
    概览/摘要: ...
    ↓ 失败
返回空 keywords + TruncateText(原文, 800) 作为 overview
```

---

## 17. 关键文件索引

| 文件 | 内容 |
|------|------|
| `internal/knowledge/wiki_store.go` | WikiStore — SQLite 存储引擎（旧表 + 三层索引 + FTS5 + 触发器） |
| `internal/knowledge/wiki_parser.go` | Markdown `##`/`###` 解析器 |
| `internal/knowledge/media_types.go` | 媒体类型→扩展名映射 |
| `internal/tool/wiki.go` | `wiki_lookup`（5 策略排序）+ `wiki_list` 工具 |
| `internal/tool/grep.go` | `grep` 工具 |
| `internal/server/knowledge_api.go` | 知识库 CRUD + 扫描管道 + API 端点 + parseKWAndOverview |
| `internal/server/handler.go` | sessionMeta/KnowledgeBase 流、SSE 映射 |
| `internal/server/server.go` | 路由注册（含 /nodes /clear 新路由） |
| `internal/agent/agent.go` | ChatWithTools、buildKBIndex、4 层 KB off 守卫 |
| `internal/config/config.go` | KnowledgeBase / KnowledgeConfig 类型定义 |
| `internal/config/knowledge_config.go` | 配置持久化操作 |
| `internal/recall/stub.go` | recall 工具 stub |
| `frontend/src/components/AppSettingsModal.vue` | 知识库 Tab UI（左右分栏 + 三层树视图 + NCollapse） |
| `frontend/src/components/InputArea.vue` | KB 选择器 UI |
| `frontend/src/api/client.ts` | 前端类型 + API 调用（含 NodeTreeItem + NodeContentItem） |
| `frontend/src/stores/chat.ts` | sessionMeta 状态管理 |

---

## 18. 待办项

- [ ] `wiki_lookup` 多 base search 合并 re-rank 当前按权重排序，可考虑 BM25 归一化
- [ ] `buildKBIndex` 使用 `context.Background()` 而非传入 ctx — L1 cache 已用 30s TTL 缓解
