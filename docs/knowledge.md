# 知识库使用指南

P-Chat 知识库是一个**本地优先的文档检索系统**。当前检索基于 SQLite FTS5 + 路径/文件名/标题/正文混合召回，LLM 可以在对话中直接检索和引用。

---

## 快速上手

### 1. 开启知识库

打开「应用设置」→「知识库」Tab → 打开「启用知识库」开关。

### 2. 添加知识库并扫描

点击「知识库」区域的「+ 添加」按钮，填入：
- **名称**：给知识库起个名字（如 `项目文档`）
- **路径**：要索引的目录绝对路径

点击「确认添加」后，点「扫描」开始索引。扫描完成后会显示索引到的文档/章节数量。

### 3. 在对话中使用

在聊天输入框底部打开「会话设置」→「知识库」，选择“不使用 / 全部 / 指定知识库”。之后 LLM 在需要时会自动调用 `recall` 或 `wiki_lookup` 检索知识库。

> 选择「不使用」可禁用当前对话的知识检索。

---

## 支持的文件格式（53 种）

### 文档
`.md` `.txt` `.markdown` `.rst` `.org`

### 代码
`.go` `.ts` `.tsx` `.js` `.jsx` `.py` `.java` `.rs` `.cpp` `.c` `.h` `.hpp`
`.vue` `.svelte` `.astro` `.cs` `.rb` `.php` `.swift` `.kt` `.scala`
`.sh` `.bash` `.ps1` `.bat` `.sql` `.r` `.dart` `.lua` `.zig` `.nim`
`.ex` `.exs` `.elm` `.clj` `.groovy` `.fs` `.fsx` `.erl` `.hrl`

### 配置/数据
`.json` `.yaml` `.yml` `.toml` `.xml` `.ini` `.cfg` `.conf` `.env`
`.properties` `.editorconfig`

### Web
`.html` `.htm` `.css` `.scss` `.less`

### 其他
`.csv` `.tsv` `.log` `.diff` `.patch` `.proto` `.graphql` `.gql` `.tf`

**自动跳过的内容**：二进制文件、压缩包、`node_modules/`、`vendor/`、点开头的隐藏目录。图片、音视频、PDF 等媒体文件默认不扫描；如果知识库配置了 `scan_model` 和 `scan_media_types`，扫描任务可以让模型先提取媒体内容再写入索引。

---

## 检索参数说明

| 参数 | 默认值 | 说明 |
|------|--------|------|
| 检索结果数 (Top-K) | 5 | 每次检索返回的最相关片段数，API 上限 50 |
| 知识库范围 | 全部启用库 | 会话可选“不使用 / 全部 / 指定知识库”，搜索 API 也可传 `bases` |
| 自动索引 | 关闭 | 启动时自动扫描并重新索引所有已启用的知识库 |

---

## 工作流程

```
添加知识库路径
  → 扫描（解析文件 → 构建 L1/L2/L3 Wiki 节点 → 写入 SQLite FTS5）
    → LLM 对话中调用 recall / wiki_lookup 工具
      → 查询分解 → 路径/文件名/标题/正文混合召回
        → 多库合并重排 + citation/explanation
          → 结果注入 LLM 上下文 → 辅助回答
```

**索引规则**：文本文件零成本解析并写入 Wiki/FTS5 索引；每个知识库会形成 L1 概览、L2 文件节点、L3 章节节点和内容块。重新扫描时按文件状态增量处理，未变文件跳过，删除文件会清理索引。

---

## API 端点

所有知识库 API 在 `/api/v1/knowledge/` 下：

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/knowledge/config` | 获取全局配置 |
| `PATCH` | `/knowledge/config` | 更新全局配置 |
| `GET` | `/knowledge/models` | 列出可用于媒体扫描的模型 |
| `GET` | `/knowledge/bases` | 列出知识库 |
| `POST` | `/knowledge/bases` | 添加知识库 |
| `DELETE` | `/knowledge/bases/:name` | 删除知识库 |
| `POST` | `/knowledge/bases/:name/scan` | 扫描索引 |
| `DELETE` | `/knowledge/bases/:name/scan` | 取消扫描 |
| `GET` | `/knowledge/bases/:name/scan/status` | 查询扫描进度 |
| `DELETE` | `/knowledge/bases/:name/clear` | 清空该知识库索引 |
| `GET` | `/knowledge/bases/:name/nodes` | 获取三层索引树节点 |
| `GET` | `/knowledge/bases/:name/nodes/:id/content` | 获取节点内容块 |
| `DELETE` | `/knowledge/bases/:name/nodes/:id` | 删除节点 |
| `POST` | `/knowledge/search` | 混合搜索 |

### config.json 配置参考

```json
{
  "knowledge": {
    "enabled": false,
    "auto_index": false,
    "bases": [
      {
        "name": "项目文档",
        "path": "D:/docs/",
        "enabled": true,
        "file_types": [".md", ".go", ".ts", ".vue"],
        "scan_model": "",
        "scan_media_types": [],
        "auto_scan": false,
        "exclude_patterns": ["node_modules/**", "vendor/**"],
        "max_file_size": 5242880
      }
    ]
  }
}
```

### 会话级绑定

每个对话可以独立选择知识库范围，通过会话元数据 API：

```bash
# 使用全部启用知识库
PATCH /api/v1/sessions/:id
{ "knowledge_base": "__all__" }

# 绑定到指定知识库
{ "knowledge_base": "项目文档" }

# 禁用本对话知识检索
{ "knowledge_base": "__off__" }
```

旧版 `vector_store` 字段仍可能出现在类型定义里用于兼容历史数据；新功能和 GUI 入口优先使用 `knowledge_base`。

---

## 常见问题

### Q: 知识库和 LLM 的上下文有什么区别？

知识库是**外部持久化存储**，LLM 上下文是**当前对话窗口**。知识库的内容不会随对话结束而消失，LLM 通过 `recall` 工具按需检索，不占用上下文窗口。

### Q: 扫描后为什么没有结果？

检查以下几项：
1. 目录路径是否存在且包含支持格式的文件
2. 知识库是否启用，当前会话是否选择了“不使用”
3. 扫描状态里是否有 failed，失败文件通常来自编码、权限、文件过大或媒体模型缺失
4. 查询是否过窄；可以试试文件名、函数名、配置 key 或更短关键词

### Q: 扫描知识库会调用 LLM 或产生费用吗？

纯文本和代码文件走本地解析 + SQLite FTS5，不需要嵌入模型，也不会调用云端 LLM。只有当知识库配置了 `scan_model` 并启用图片、音频、视频、PDF 等媒体扫描时，扫描任务才会调用对应模型提取内容。

### Q: 如何让 LLM 更频繁地使用知识库？

LLM 在以下情况下会自动调用 recall：
- 不确定某条信息
- 需要查找代码或文档
- 想引用历史或项目知识

如果你想**强制**检索，可以直接在消息中说"查一下知识库中关于 XXX 的内容"，LLM 会主动调用 recall。

### Q: 多个知识库可以同时使用吗？

可以。会话选择“全部”时，系统会对所有启用知识库分别召回，再归一化、去重、全局重排。也可以在会话设置里绑定某一个知识库，降低跨领域噪音。

### Q: 本地索引文件有多大？

当前主要是 SQLite Wiki/FTS5 索引，大小取决于文件数量、文本长度和内容块数量。纯文本索引通常比原始文本大一些；启用媒体扫描时，模型提取出的描述文本也会写入索引。

### Q: 如何备份知识库？

- **本地索引**：备份 `~/.p-chat/knowledge/` 目录
- **配置**：备份 `~/.p-chat/config.json` 中的 `knowledge` 部分

### Q: 支持增量索引吗？

支持。重新扫描同一个目录时，已扫描且内容未变的文件会跳过，只处理新增和修改过的文件。分块是基于文件内容哈希的幂等操作。

### Q: 知识库和 Rules/AGENTS.md 的区别？

| | 知识库 | Rules | AGENTS.md |
|--|--------|-------|-----------|
| 存储方式 | SQLite Wiki/FTS5 索引 | 原始 Markdown | 原始 Markdown |
| 检索方式 | 按需混合检索 | 全部注入 System Prompt | 全部注入 System Prompt |
| 适用场景 | 大量文档/代码，按需查 | 行为约束，全局生效 | Agent 指令，全局生效 |
| 消耗 | 纯文本零模型费用；媒体扫描可能调用模型 | 占用上下文 token | 占用上下文 token |


## 多知识库合并重排 (KB-01)

`POST /api/v1/knowledge/search` 与 `wiki_lookup` 在多库场景下：

1. 对每个启用的知识库分别 `LookupSearch`（不再 early-stop）
2. 为每条结果打上 `base` 标签
3. 按库内 max 归一化 score，再全局排序
4. 按 `source+title` 去重（路径大小写/分隔符不敏感）
5. 截断到 topK / 分页窗口

实现：`internal/knowledge/merge.go` 的 `MergeAndRerank` / `TagBase`。
响应字段新增 `base`、`title`。


## 混合检索 (KB-02)

`LookupSearch` 会并行收集多类候选，然后用 RRF（Reciprocal Rank Fusion）融合：

1. 路径 / 文件名 / 标题 substring 命中（适合 `config.go`、函数名、配置 key）
2. FTS5 title / keywords / overview / L2 文件节点前缀匹配
3. 正文 `LIKE` 命中（补精确术语）

返回结果新增：

- `match_type`: `path | filename | title | keywords | overview | l2 | content`
- `base`: 来源知识库（KB-01）
- `rank`: 融合后的相关性分数

`wiki_lookup` 会在工具结果中显示 `来源库` 与 `命中`；`POST /api/v1/knowledge/search` 也会返回 `base` / `title` / `match_type`。


## 查询改写与分解 (KB-03)

搜索入口会先调用 `knowledge.PlanQueries(query)`，规则型派生 2-5 个 query：

- 原始 query
- 反引号中的术语（如 `work_mode`）
- 路径型 token（如 `internal/config/config.go`）
- 函数/配置 key/文件名等 symbol token（如 `LoadConfig`）
- 较长中文/英文关键词

每个派生 query 都会独立 `LookupSearch`，再复用 KB-01 `MergeAndRerank` 去重、归一化、全局排序。结果新增 `query` 字段，表示这条命中来自哪个原始或派生查询。


## 引用可解释性 (KB-04)

知识库搜索结果会附带结构化 `citation` 与短文本 `explanation`：

- `base` / `source` / `title` / `parent_title`: 来源库与定位
- `query`: 命中该结果的原始或派生查询
- `match_type`: `path | filename | title | keywords | overview | l2 | content`
- `score`: 融合后的相关性分数
- `explanation`: 可直接展示给用户的命中说明

`wiki_lookup` 工具结果会显示来源库、命中类型、匹配查询和解释；`POST /api/v1/knowledge/search` 返回同样的结构化字段，前端类型为 `KnowledgeCitation`。


## 增量扫描优化 (KB-05)

扫描知识库时会按文件 `mtime` 进行增量处理：

- 未变文件：跳过，计入 `skipped`
- 新增/变更文件：只替换该 source 的 L2/L3/contents，计入 `changed`
- 已删除文件：从索引和 `file_mtimes` 清理，计入 `deleted`
- 读取失败文件：计入 `failed`，扫描继续

`GET /api/v1/knowledge/bases/:name/scan/status` 新增 `changed`、`skipped`、`deleted`、`failed` 字段，GUI 可展示二次扫描是否真正跳过未变文件。
