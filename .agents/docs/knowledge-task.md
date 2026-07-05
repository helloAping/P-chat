# 知识库体系完善任务文档

> TAPD 风格任务分解，每阶段独立可验证

---

## S1: VectorStore 接口 + Registry + StoreConfig（0.5d）

**目录**：`internal/knowledge/`

### 任务
- [ ] `store.go`：定义 `VectorStore` 接口（Connect / Close / Health / Upsert / Delete / Search / Stats）
- [ ] `store.go`：定义 `VectorChunk`、`SearchResult`、`StoreStats`、`KBCapabilities` 数据结构
- [ ] `store_config.go`：定义 `StoreConfig`（name / type / driver / host / port / url / collection / auth）
- [ ] `registry.go`：`drivers map` + `OpenStore(cfg) (VectorStore, error)` 工厂方法
- [ ] `pool.go`：`GetStore(name, configs)` 带 `sync.Map` 连接池缓存
- [ ] 单元测试：接口类型断言 + registry 注册/覆盖 + 连接池缓存命中

### 验收
- `go test -count=1 ./internal/knowledge/...` 通过
- `go build ./...` 通过

---

## S2: 本地 LocalStore（SQLite 元数据 + mmap 向量文件）（1d）

**目录**：`internal/knowledge/`

### 任务
- [ ] `store_local.go`：实现 `LocalStore`（实现 `VectorStore` 接口）
  - `Connect`：打开/创建 `chunks.db` + `.vec` 文件 + `meta.json`
  - `Close`：关闭 mmap + db
  - `Upsert`：批量写入 SQLite chunks 表 + `WriteAt` 追加向量文件
  - `Delete`：tombstone 标记（写入 delete 位图文件）
  - `Search`：mmap 映射 → `[]float32` → 全量余弦相似度 → top-K 排序
  - `Stats`：读取 meta.json + chunks 计数
- [ ] `store_local_test.go`：单元测试（upsert/search/delete/stats 完整流程）
- [ ] 迁移现有 `Indexer` 使用 `VectorStore` 接口（__不删除旧代码__，先适配）

### 数据库变更
- `chunks.db` 新建于 `~/.p-chat/vectors/<store_name>/chunks.db`
- 表结构复用 `internal/memory/migrations.go` 中 chunks 定义
- 向量文件 `vec_000.vec`：`[chunk_id:4B][dim*4B]` 顺序排列

### 验收
- `go test -count=1 -v -run TestLocalStore ./internal/knowledge/` 通过
- `go test -count=1 ./...` 全量通过

---

## S3: Config 扩展（0.5d）

**目录**：`internal/config/`

### 任务
- [ ] `config.go`：新增 `KnowledgeConfig` struct
  ```
  KnowledgeConfig {
      Enabled       bool             `json:"enabled"`
      AutoIndex     bool             `json:"auto_index"`
      DefaultStore  string           `json:"default_store"`
      Embedder      EmbedderConfig   `json:"embedder"`
      Search        SearchConfig     `json:"search"`
      VectorStores  []StoreConfig    `json:"vector_stores,omitempty"`
      Bases         []KnowledgeBase  `json:"bases,omitempty"`
  }
  ```
- [ ] `config.go`：`EmbedderConfig`（provider / model / dimensions）
- [ ] `config.go`：`SearchConfig`（top_k / min_score）
- [ ] `config.go`：`KnowledgeBase`（name / path / store / file_types / chunk_size / chunk_overlap / enabled）
- [ ] `config.go`：`Config.Knowledge` 字段加到主 Config struct
- [ ] `config.go`：`Default()` 中设置 `Knowledge.Enabled = false`
- [ ] `config.go`：`Default()` 中设置默认 local store

### 升级脚本
- [ ] `internal/config/migrate_config_v2.go`：自动检测旧 JSON 无 `knowledge` 字段 → 注入默认值
- 检测方式：`json.Unmarshal` 后检查 `cfg.Knowledge` 所有字段是否为零值

### 验收
- 无 knowledge 字段的旧 config.json 加载不报错，自动补全默认值
- 有 knowledge 字段的 config.json 正确解析

---

## S4: Qdrant 远程适配器（1d）

**目录**：`internal/knowledge/`

### 任务
- [ ] `store_qdrant.go`：实现 `QdrantStore`（纯 HTTP client，零额外 Go 依赖）
  - `Connect`：调用 `GET /collections/{name}` 验证，不存在则 `PUT /collections/{name}` 创建
  - `Upsert`：`PUT /collections/{name}/points`（batch upsert）
  - `Search`：`POST /collections/{name}/points/search`
  - `Delete`：`POST /collections/{name}/points/delete`
  - `Stats`：`GET /collections/{name}` 获取 points_count
- [ ] `store_qdrant_test.go`：集成测试（需 Qdrant 容器，CI 可跳过）
- [ ] registry 注册 `"qdrant" → NewQdrantStore`

### Qdrant REST API 参考
| 操作 | 方法 | 路径 |
|------|------|------|
| 创建集合 | PUT | `/collections/{name}` + body `{vectors: {size: N, distance: "Cosine"}}` |
| Upsert | PUT | `/collections/{name}/points?wait=true` + body `{points: [{id, vector, payload}]}` |
| Search | POST | `/collections/{name}/points/search` + body `{vector, limit, with_payload: true}` |
| Delete | POST | `/collections/{name}/points/delete?wait=true` + body `{points: [id]}` |

### 验收
- `go test -count=1 -v -run TestQdrantStore ./internal/knowledge/` 通过
- `go test -count=1 ./...` 全量通过（qdrant 测试在无容器时 skip）

---

## S5: Session vector_store 绑定 + recall 路由（0.5d）

**目录**：`internal/server/`、`internal/agent/`、`internal/memory/`、`internal/recall/`

### 任务
- [ ] `internal/memory/migrations.go`：`ALTER TABLE sessions ADD COLUMN vector_store TEXT DEFAULT ''`
- [ ] `internal/memory/memory.go`：`Conversation.VectorStore` 字段 + 读写
- [ ] `internal/server/handler.go`：SendMessage 中读取 session.vector_store → 为空则用 config.Knowledge.DefaultStore
- [ ] `internal/agent/agent.go`：
  - 注册 `recall` 工具（受 `knowledge.enabled` 开关控制）
  - recall 工具 handler：`GetStore(name)` → `Embedder.Embed(query)` → `store.Search()` → `FormatForPrompt()`
  - recall 与 task 工具一样从子代理排除
- [ ] `internal/recall/recall.go`：重构 `Engine` 接受 `VectorStore` 而非直接操作 db
- [ ] 前端：session meta 加入 `vector_store` 字段透传

### Session API 变更
- `PATCH /api/v1/sessions/:id` 新增可选字段 `vector_store`

### 验收
- 新建 session 默认 vector_store 为 default_store
- 修改 session vector_store 后检索路由到正确库
- `knowledge.enabled=false` 时 recall 工具不存在
- `knowledge.enabled=true` 时 LLM 可调用 recall 工具

---

## S6: Milvus 远程适配器（1d）

**目录**：`internal/knowledge/`

### 任务
- [ ] `store_milvus.go`：实现 `MilvusStore`（封装 `milvus-sdk-go`）
  - `Connect`：gRPC 连接 + 验证 collection 存在
  - `Upsert`：`client.Insert()` 批量写入
  - `Search`：`client.Search()` 带 ANN 参数
  - `Delete`：`client.DeleteByPks()`
  - `Stats`：`client.GetCollectionStatistics()`
- [ ] `store_milvus_test.go`：集成测试（需 Milvus 容器，CI 可跳过）
- [ ] registry 注册 `"milvus" → NewMilvusStore`
- [ ] `go.mod`：添加 `milvus-sdk-go` 依赖

### 验收
- `go test -count=1 -v -run TestMilvusStore ./internal/knowledge/` 通过

---

## S7: 前端（1.5d）

**目录**：`cmd/pchat-gui/frontend/src/`

### 任务
- [ ] `AppSettingsModal.vue`：
  - Tab 类型扩展：`ref<'providers' | ... | 'knowledge'>('providers')`
  - 新增 `NTabPane name="knowledge"` Tab
  - 全局设置区：enabled toggle / auto_index toggle / topK slider / minScore slider
  - Embedder 选择区：provider 下拉 + model 下拉
  - 向量库面板：列表（名称/类型/地址/状态/默认选中）+ 添加/删除/测试连接
  - 知识库面板：列表（名称/路径/向量库/索引状态/开关）+ 添加/删除/扫描
  - `watch(tab)` 中添加 `case 'knowledge'`
- [ ] `api/client.ts`：新增 knowledge CRUD 函数
- [ ] `components/ChatInput.vue`（或等效组件）：新增向量库选择器

### 验收
- 知识库 Tab 可开关功能
- 向量库可添加/删除/测试连接/设为默认
- 知识库可添加/删除/手动扫描
- 对话输入区可选择向量库

---

## S8: Chroma / Weaviate / Pinecone 适配器（1d）

**目录**：`internal/knowledge/`

### 任务
- [ ] `store_chroma.go`：REST 适配器
- [ ] `store_weaviate.go`：GraphQL + REST 适配器
- [ ] `store_pinecone.go`：REST 适配器
- [ ] 各自 `_test.go` 集成测试
- [ ] registry 注册所有驱动

### 验收
- `go test -count=1 -v -run "TestChroma|TestWeaviate|TestPinecone" ./internal/knowledge/` 通过

---

## 升级脚本

### `internal/config/migrate_config_v2.go`
- 在 `Load()` 中调用：若加载的 JSON 无 `knowledge` 字段 → 原地注入默认 `KnowledgeConfig`
- 修改 `Load()` 或 `Default()` 完毕后检测，确保向后兼容

### 旧向量数据迁移（`chunks` / `embeddings` 表）
- 当前数据在 `~/.p-chat/memory/store.db` 的 `chunks` + `embeddings` 表
- 迁移策略：__不自动迁移__，旧数据保持不变。用户升级后需重新扫描知识库
- 新架构下数据写入 `~/.p-chat/vectors/<store_name>/chunks.db` + `vec_*.vec`

---

## 不在范围内的任务（后续迭代）

- [ ] 文件监听（fsnotify）自动增量索引
- [ ] 定时巡检索引
- [ ] 对话历史自动索引
- [ ] BM25 混合检索
- [ ] RAGAS 评估体系
