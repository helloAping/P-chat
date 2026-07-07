# 知识库使用指南

P-Chat 知识库是一个**本地优先的文档语义检索系统**。它通过向量嵌入（embedding）技术将你的代码、文档、配置文件转化为可语义搜索的知识片段，LLM 可以在对话中直接检索和引用。

---

## 快速上手

### 1. 开启知识库

打开「应用设置」→「知识库」Tab → 打开「启用知识库」开关。

### 2. 配置嵌入模型

在「嵌入模型」区域选择一个供应商和模型。推荐使用以下模型：

| 供应商 | 模型 | 维度 | 适用场景 |
|--------|------|------|---------|
| OpenAI | `text-embedding-3-small` | 1536 | 通用，性价比高 |
| OpenAI | `text-embedding-3-large` | 3072 | 高精度需求的代码/技术文档 |
| 本地 Ollama | `nomic-embed-text` | 768 | 完全离线，无需 API Key |

> 提示：供应商下拉会从你已配置的 LLM 提供商中自动筛选包含 `embedding` 关键字的模型。

### 3. 添加向量库

向量库是存储嵌入向量的后端。支持两种类型：

**本地向量库**（推荐首次使用）：
- 驱动：`local`
- 存储路径：`~/.p-chat/vectors/<名称>/`
- 零依赖，开箱即用，适合个人使用

**远程向量库**（团队共享场景）：
- Qdrant、Milvus、Chroma、Weaviate、Pinecone
- 配置 URL / Host:Port / Collection 名称即可

### 4. 添加知识库并扫描

点击「知识库」区域的「+ 添加」按钮，填入：
- **名称**：给知识库起个名字（如 "项目文档"）
- **路径**：要索引的目录绝对路径
- **向量库**：选择上一步创建的向量库

点击「确认添加」后，点「扫描」开始索引。扫描完成后会显示索引到的文档片段数量。

### 5. 在对话中使用

在聊天输入框底部，点击「向量库」下拉选择器选择一个向量库。之后 LLM 在需要时会自动调用 `recall` 工具检索知识库。

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

**自动跳过的内容**：二进制文件、图片、音视频、压缩包、`node_modules/`、`vendor/`、点开头的隐藏目录。

---

## 检索参数说明

| 参数 | 默认值 | 说明 |
|------|--------|------|
| 检索结果数 (Top-K) | 5 | 每次检索返回的最相关片段数，范围 1-20 |
| 最低相似度 | 0.5 | 低于此分数的结果被过滤，范围 0-1 |
| 自动索引 | 关闭 | 启动时自动扫描并重新索引所有已启用的知识库 |

---

## 工作流程

```
添加知识库路径
  → 扫描（分块 → 嵌入 → 存储到向量库）
    → LLM 对话中调用 recall 工具
      → 查询嵌入 → 向量相似度搜索 → Top-K 结果
        → 结果注入 LLM 上下文 → 辅助回答
```

**分块规则**：按 800 字符分块，相邻块 100 字符重叠，保证语义连续性。切分时按段落/句子边界，不是机械截断。

---

## API 端点

所有知识库 API 在 `/api/v1/knowledge/` 下：

| 方法 | 路径 | 说明 |
|------|------|------|
| `GET` | `/knowledge/config` | 获取全局配置 |
| `PATCH` | `/knowledge/config` | 更新全局配置 |
| `GET` | `/knowledge/stores` | 列出向量库 |
| `POST` | `/knowledge/stores` | 添加向量库 |
| `DELETE` | `/knowledge/stores/:name` | 删除向量库 |
| `POST` | `/knowledge/stores/:name/test` | 测试连接 |
| `GET` | `/knowledge/bases` | 列出知识库 |
| `POST` | `/knowledge/bases` | 添加知识库 |
| `DELETE` | `/knowledge/bases/:name` | 删除知识库 |
| `POST` | `/knowledge/bases/:name/scan` | 扫描索引 |
| `POST` | `/knowledge/search` | 语义搜索 |
| `GET` | `/knowledge/embedders` | 列出可用嵌入模型 |

### config.json 配置参考

```json
{
  "knowledge": {
    "enabled": false,
    "default_store": "local",
    "auto_index": false,
    "embedder": {
      "provider": "openai",
      "model": "text-embedding-3-small"
    },
    "search": {
      "top_k": 5,
      "min_score": 0.5
    },
    "vector_stores": [
      {
        "name": "local",
        "type": "local",
        "driver": "local",
        "path": ""
      }
    ],
    "bases": [
      {
        "name": "项目文档",
        "path": "D:/docs/",
        "store": "local",
        "chunk_size": 800,
        "chunk_overlap": 100,
        "enabled": true
      }
    ]
  }
}
```

### 会话级绑定

每个对话可以独立绑定不同的向量库，通过会话元数据 API：

```bash
# 绑定到指定向量库
PATCH /api/v1/sessions/:id
{ "vector_store": "qdrant-prod" }

# 使用全局默认
{ "vector_store": "" }

# 禁用本对话知识检索
{ "vector_store": "__off__" }
```

---

## 常见问题

### Q: 知识库和 LLM 的上下文有什么区别？

知识库是**外部持久化存储**，LLM 上下文是**当前对话窗口**。知识库的内容不会随对话结束而消失，LLM 通过 `recall` 工具按需检索，不占用上下文窗口。

### Q: 扫描后为什么没有结果？

检查以下几项：
1. 目录路径是否存在且包含支持格式的文件
2. 嵌入模型是否配置正确（供应商有效、API Key 可用）
3. 向量库连接是否正常（点「测试」按钮）
4. 检索时相似度阈值是否过高（尝试调低 min_score）

### Q: 嵌入 API 调用会花钱吗？

是的，如果你使用 OpenAI 等云服务的嵌入模型。参考价格（2024）：
- `text-embedding-3-small`：$0.02 / 1M tokens
- `text-embedding-3-large`：$0.13 / 1M tokens

使用本地模型（Ollama nomic-embed-text 等）完全免费。嵌入只在**扫描索引时**产生 API 调用，后续检索是在向量库上进行，不再调用嵌入 API（仅对用户查询进行一次嵌入）。

### Q: 如何让 LLM 更频繁地使用知识库？

LLM 在以下情况下会自动调用 recall：
- 不确定某条信息
- 需要查找代码或文档
- 想引用历史或项目知识

如果你想**强制**检索，可以直接在消息中说"查一下知识库中关于 XXX 的内容"，LLM 会主动调用 recall。

### Q: 多个向量库可以同时使用吗？

当前设计是**每个对话绑定一个向量库**。如果你有多个知识域（如"技术文档"和"公司规章"），可以：
1. 将它们扫描到同一个向量库（统一检索）
2. 或分别建不同的向量库，切换对话时更换绑定

### Q: 本地向量库的 .vec 文件有多大？

估算公式：`文件大小 ≈ 文档片段数 × 向量维度 × 4 字节`

例如：1000 个片段 × 1536 维 × 4B = 约 6 MB。实际会比这个略大（含元数据和索引开销），但增长是线性的。

### Q: 如何备份知识库？

- **本地向量库**：备份 `~/.p-chat/vectors/` 目录
- **远程向量库**：参考对应服务的备份文档（Qdrant snapshots、Milvus backup 等）
- **配置**：备份 `~/.p-chat/config.json` 中的 `knowledge` 部分

### Q: 支持增量索引吗？

支持。重新扫描同一个目录时，已扫描且内容未变的文件会跳过，只处理新增和修改过的文件。分块是基于文件内容哈希的幂等操作。

### Q: 知识库和 Rules/AGENTS.md 的区别？

| | 知识库 | Rules | AGENTS.md |
|--|--------|-------|-----------|
| 存储方式 | 向量嵌入 | 原始 Markdown | 原始 Markdown |
| 检索方式 | 语义相似度搜索 | 全部注入 System Prompt | 全部注入 System Prompt |
| 适用场景 | 大量文档/代码，按需查 | 行为约束，全局生效 | Agent 指令，全局生效 |
| 消耗 | 嵌入 API + 少量 token | 占用上下文 token | 占用上下文 token |

### Q: 目前支持哪些 LLM 供应商的嵌入模型？

任何提供 OpenAI 兼容 embedding 端点的供应商都可以使用，包括 OpenAI、DeepSeek、智谱、百川、Ollama 等。配置时只需确保：
1. 供应商的 `base_url` 指向正确的端点
2. 模型名在 `/v1/embeddings` 端点可用
3. API Key 有权限调用 embedding API
