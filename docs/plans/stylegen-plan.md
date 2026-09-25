# P-Chat 从对话生成自定义风格（stylegen）实现计划

> **状态**：设计定稿（v4），待开工 · **作者**：Claude · **日期**：2026-08-10 · **目标版本**：feat_1.0.11（后续）
>
> **目标**：在对话界面右侧检查器的「会话操作」中提供「生成风格」入口 + CLI `/stylegen` 指令，基于**当前对话**自动总结生成 AI 风格（persona）：支持**新建**风格或**优化现有**风格；生成跑在**后台隔离任务**（只读快照当前对话，不写原会话），通过 **SSE 流**把阶段进度与生成内容实时推送到弹窗。

---

## 1. 背景与目标

### 1.1 背景

P-Chat 已有完整的风格系统（SQLite `styles` 表 + `style.Manager` CRUD + `POST /api/v1/styles` + 前端「设置→风格配置」编辑 UI），但**没有"从对话自动生成"能力**：用户想创建自定义风格只能手写整篇 6 段式 markdown（人设/性格/说话风格/表达模板/禁止项/示例），记忆（memory）也要手动总结。这是"每次创建都比较麻烦"的根因。

### 1.2 目标（本方案要交付的）

1. **GUI 主入口**：右侧检查器「会话操作」里的小按钮 → 弹窗，用户填表即可生成风格
2. **CLI 辅入口**：`/stylegen create|optimize` 指令（local / http 双模式）
3. **双模式**：新建风格 / 优化现有风格（优化时把原 prompt + memory 附上，结合当前对话最新内容补充完善）
4. **隔离执行**：后台任务**只读**当前对话快照，全程不写原会话（fork=只读快照方案）
5. **进度可见**：SSE 流推送阶段进度 + 流式生成内容到弹窗

### 1.3 已确认的设计决策

| 决策点 | 结论 | 理由 |
| --- | --- | --- |
| 触发方式 | GUI 右侧检查器按钮 + CLI 指令 | 用户自主触发，不依赖 LLM 主动提议 |
| 来源对话 | **不可选，恒为当前会话**（按钮所在会话） | 用户明确要求；简单可控 |
| 风格选择 | 弹窗内可选：新建 或 优化现有 | 双模式 |
| 隔离方式 | **A：只读快照**（不 fork 新会话） | 用户确认 A；零污染、零写入 |
| SKILL / LLM 工具 | **不做** | 用户确认不保留；改为 GUI+CLI |
| 进度推送 | 后台 Job + SSE 流（复用聊天 SSE 架构） | 弹窗显示阶段 + 内容实时生成 |
| 生成调用 | `llm.ChatStream` 流式 | 边生成边显示内容 |
| 内置风格优化 | 自动"复制另存新风格"（原内置不动） | 内置只读是现有约束，兼容处理 |
| 前端会话 id | 取 chat store `state.currentID` | 现成用法（ContextInspectorDrawer 同款） |

### 1.4 整体数据流

```
InspectorPanel 会话操作「生成风格」按钮
  → StyleGenModal 弹窗（新建/优化 + 名称 + 补充要求；来源对话=当前）
  → POST /api/v1/stylegen   (body: mode/style_id/label/requirement/conversation_id=当前会话)
  → 202 {job_id}，后台 goroutine 跑 stylegen.Generate
      · 只读快照读会话历史（GetChatMessagesWithMetaFor）
      · 清洗截断 → llm.ChatStream 流式生成 prompt+memory
      · 解析 JSON → styleMgr.Create / Update（内置→另存）
      · emit 阶段事件 → JobManager 广播
  → GET /api/v1/stylegen/:job/events  (SSE: stage/content/thinking/result/error)
  → 弹窗进度面板渲染；结果卡片「应用到当前会话」→ updateSessionMeta
CLI: /stylegen create|optimize ...（local 同步跑打印阶段行 / http 走 API 轮询）
```

---

## 2. 任务拆分总览

| # | 任务点 | 主要文件 | 依赖 |
| --- | --- | --- | --- |
| 1 | stylegen 核心生成引擎 | `internal/stylegen/generate.go` | 无（先做纯逻辑） |
| 2 | JobManager + HTTP/SSE 端点 | `internal/stylegen/jobs.go`、`internal/server/stylegen.go`、`internal/server/server.go` | 任务 1 |
| 3 | 前端：API 客户端 + 弹窗 + 检查器入口 | `frontend/src/api/client.ts`、`StyleGenModal.vue`（新）、`InspectorPanel.vue` | 任务 2 |
| 4 | CLI `/stylegen` 指令 | `internal/cli/commands.go`、`internal/cli/context.go`、`internal/httpcli/client.go` | 任务 1/2 |
| 5 | 接线 + 测试 + 构建验证 | `cmd/pchat-server/main.go`、各 `*_test.go` | 任务 1-4 |

> 依赖关系：1→2→3；1/2→4；全部→5。可并行：任务 1 与任务 4 的 CLI 骨架可先并行。

---

## 3. 任务 1：stylegen 核心生成引擎

### 3.1 改动内容

新建 `internal/stylegen/` 包，提供纯逻辑生成核心（不依赖 HTTP/CLI）：

```go
type Deps struct {
    Store    *memory.Store   // 读会话历史
    StyleMgr *style.Manager  // 新建/更新风格
    LLM      LLMClient       // 流式生成；接口便于单测 mock
    Provider string          // 默认 provider（main.go defaultProviderName(cfg)）
    Model    string          // 可为空（= provider 默认）
    MaxChars int             // 会话摘录截断上限，默认 ~40000
}
type LLMClient interface {
    ChatStream(ctx context.Context, provider, model string, msgs []llm.Message) <-chan llm.StreamChunk
}

type Params struct { Mode, StyleID, Label, Requirement, ConversationID string }
type Result struct { ID, Label, Prompt, Memory, Mode string; Updated bool }
type ProgressEvent struct { Stage, Label string; Content, Thinking string }

func Generate(ctx context.Context, deps Deps, p Params, emit func(ProgressEvent)) (*Result, error)
```

核心流程：
1. `deps.Store.GetChatMessagesWithMetaFor(p.ConversationID, 0)` 读**只读快照**（不写任何东西）
2. 清洗：只留 `role ∈ {user, assistant}`；丢弃 `Type ∈ {tool, thinking, tool_call, tool_result, command}` 与空 content；按 `MaxChars` 截断（最新优先，超长从最旧裁）
3. 组中文生成 prompt（见 §5），`emit` 各阶段；`deps.LLM.ChatStream` 流式生成，`content`/`thinking` delta 转 `emit`
4. 汇流完整文本 → 解析 JSON `{id?, prompt, memory}`（fenced 代码块 / 裸 JSON / 失败兜底整段当 prompt + memory 空）
5. 落库：
   - `create` → `StyleMgr.Create(id, label, prompt, memory)`（id 缺省由 LLM 从 label 派生）
   - `optimize`（目标自定义）→ `StyleMgr.Update(id, label, 新prompt, 新memory)` 原地更新
   - `optimize`（目标内置 cute/guofeng/tech）→ 内置只读，改为**复制另存新风格**（新 id、label 用用户给的或「原名·优化版」）→ `Create`
6. 返回 `Result`（含 `Updated` 标记是原地更新还是另存）

### 3.2 涉及位置

| 位置 | 说明 |
| --- | --- |
| `internal/stylegen/generate.go`（新） | 主逻辑；含 `buildTranscript`、`buildGenerationPrompt`、`parseGenOutput` 子函数 |
| `internal/style/manager.go` | **只读复用**：`Create`(L142) / `Update`(L170) / `GetSystemPrompt`(L63) / `GetMemory`(L73) / `ListAll`(L100) |
| `internal/memory/memory.go` | **只读复用**：`GetChatMessagesWithMetaFor`(L634) |
| `internal/llm/client.go` | **只读复用**：`ChatStream`(L319) 签名；`StreamChunk{Content, Thinking}` 结构 |
| `internal/memory/summarizer.go` | **参考范式**：`summarize`(L282) 的一次性 LLM 调用风格 |
| `internal/upgrade/prompts/cute.md` | **参考范本**：6 段式人格模板（人设/性格/说话风格/表达模板/禁止项/示例）嵌入生成 prompt |

### 3.3 改动范围

- **新增**：`internal/stylegen/` 一个包，约 200-300 行；**不改**任何现有文件
- 依赖方向：`stylegen → {memory, llm, style, config}`；**无循环依赖**（本方案不做 LLM 工具，不再涉及 `internal/llm`→`internal/tool` 的既有约束）
- `Generate` 为纯函数式核心，便于单测与将来复用（如未来加回 SKILL/HTTP 直调）

### 3.4 注意事项

- **只读原则**：全程不得对 `ConversationID` 写任何消息/元数据；「不影响原有上下文」是本任务硬性验收
- **内置只读**：`optimize` 目标为内置风格时必须走"另存"分支；`Update` 对内置会报错（manager.go L175-179），不要试图绕开
- **id 派生**：中文 label 经 `normaliseStyleID` 会变空，必须由生成 LLM 产出 ASCII id（拼音/意译）；`Create` 内部会再做规范化 + 保留名/重复检查，错误直接透传
- **JSON 解析兜底**：LLM 偶发非 JSON 输出，兜底为全文当 prompt、memory 空，并在结果中提示"可后编辑"
- **流式 vs 汇流**：`ChatStream` 逐 delta 上抛给 emit（供进度显示），同时本地累积完整文本用于 JSON 解析
- **超时/取消**：`ctx` 贯穿；任务取消时 `ChatStream` 自然终止，已 emit 的阶段保留

---

## 4. 任务 2：JobManager + HTTP/SSE 端点

### 4.1 改动内容

- `internal/stylegen/jobs.go`（新）：内存 `JobManager`
  - `Start(p Params) (jobID string)`：注册任务 → 后台 goroutine 跑 `Generate`，`emit` 转广播
  - `Subscribe(jobID) <-chan ProgressEvent`：SSE 订阅；`Status(jobID) *JobStatus`：轮询用
  - `JobStatus{ID, Status(running|done|error), Stages[], Result*, Error}`；TTL 清理（如 10 分钟）+ 容量上限
  - 并发安全：`sync.Mutex`/`RWMutex`；完成后关闭订阅 channel，防止 SSE 悬挂
- `internal/server/stylegen.go`（新）：HTTP handler
  - `POST /api/v1/stylegen`：body `{mode, style_id?, label, requirement, conversation_id}` → 校验（label 必填、conversation_id 非空、optimize 时 style_id 存在）→ 202 `{job_id}`；**立即返回，不阻塞**
  - `GET /api/v1/stylegen/:job/events`：SSE 流（`Content-Type: text/event-stream`），事件：
    | type | 字段 | 说明 |
    | --- | --- | --- |
    | `stage` | `stage,label` | 阶段切换（reading/analyzing/generating/saving/done） |
    | `content` | `content` | 生成中的 prompt/memory 文本 delta |
    | `thinking` | `thinking` | 推理 delta（可折叠展示） |
    | `result` | `result_json` | `{id,label,prompt,memory,mode,updated}` |
    | `error` | `error,error_kind` | `E_EMPTY / E_NOT_FOUND / E_LLM / E_PARSE / E_ARGS` |
  - `GET /api/v1/stylegen/:job`：JSON 状态（轮询/调试兜底）
  - `styleMgr == nil` 时 `POST` 返回 503（与 styles.go 既有约定一致）
- `internal/server/server.go`：注册路由（`stylegen` 相关 group，参考 styles 路由 L159-163）

### 4.2 涉及位置

| 位置 | 说明 |
| --- | --- |
| `internal/stylegen/jobs.go`（新） | JobManager |
| `internal/server/stylegen.go`（新） | handler + SSE |
| `internal/server/server.go` | 路由注册（~L159-163 styles 路由附近） |
| `internal/server/handler.go` / `messages.go` | **参考**：聊天 SSE 事件序列（`chunkToEvent` L1495-1613）的事件形状 |
| `frontend/src/api/sse.ts` | **只读参考**：前端侧已存在的 SSE 解析器（本任务只看后端形状对齐） |

### 4.3 改动范围

- **新增** 2 个 Go 文件 + server.go 加路由（约 10 行）；**不改**聊天流主链路
- 单进程内存任务表即可（pchat-server 单实例），无需持久化、无需消息队列
- 路由命名空间 `/api/v1/stylegen`（与 `/api/v1/styles` 平级，避免混淆）

### 4.4 注意事项

- **SSE 对齐聊天流**：事件 JSON 字段命名尽量与现有 `type/content/thinking/error` 一致，前端可复用解析逻辑
- **job 生命周期**：完成后关闭 `Subscribe` 的 channel；客户端断连（ctx cancel）时 goroutine 正常结束；TTL 清理避免泄漏
- **不阻塞请求**：`POST` 立即 202；所有 LLM 调用都在后台 goroutine
- **并发**：同一会话可多次提交（各自独立 job）；JobManager 需并发安全
- **错误语义**：错误码前缀 `E_` 与现有工具错误码风格统一，前端按 `error_kind` 渲染

---

## 5. 任务 3：前端——API 客户端 + 弹窗 + 检查器入口

### 5.1 改动内容

- `frontend/src/api/client.ts`：
  - 类型：`StyleGenRequest {mode, style_id?, label, requirement, conversation_id}`、`StyleGenResult {id,label,prompt,memory,mode,updated}`、`StyleGenStageEvent/ContentEvent/...`（或复用 sse 泛型）
  - `startStyleGen(req)` → `POST /api/v1/stylegen` → `{job_id}`
  - `streamStyleGen(jobID)` → 复用 `api/sse.ts` 的 ReadableStream SSE 解析，逐事件回调（stage/content/thinking/result/error）
  - 挂在现有 styles API 附近（~L735-780）
- `frontend/src/components/StyleGenModal.vue`（新）：
  - 模式切换（NTabs 或 NRadioGroup）：新建 / 优化现有
  - 新建：风格名称（NInput，必填）+ 补充内容/要求（NInput type=textarea，占位引导"语气/人设/要记住的事"）
  - 优化：现有风格（NSelect ← `api.getStyles()`）+ 补充要求（"想改哪里"）
  - **不显示来源对话选择**（固定当前会话，取 chat store `state.currentID`）
  - 提交 → `startStyleGen` → `streamStyleGen` → 进度面板：阶段清单（读取对话→生成人格→生成记忆→保存，勾选/进行中标记）+ 流式内容（thinking 可折叠 + prompt/memory 实时文本）+ 结果卡片（id/label/updated）
  - 「应用到当前会话」→ `updateSessionMeta(currentID, {style})` 立即生效；「去设置查看」/「关闭」
  - 错误态：按 `error_kind` 显示，可重试
- `frontend/src/components/InspectorPanel.vue`：在「会话操作」中加一个 Sparkles 图标小按钮「生成风格」，点击开弹窗；`!state.currentID` 时禁用

### 5.2 涉及位置

| 位置 | 说明 |
| --- | --- |
| `frontend/src/api/client.ts` | 新增 ~40 行 + 复用 SSE 模式（L1243/2010、L735-780） |
| `frontend/src/api/sse.ts` | **只读复用**现有 SSE 解析 |
| `frontend/src/components/StyleGenModal.vue`（新） | 弹窗 ~300 行 |
| `frontend/src/components/InspectorPanel.vue` | 会话操作区加按钮 ~15 行 |
| `frontend/src/stores/chat.ts` | **只读复用** `state.currentID`（ContextInspectorDrawer.vue L33 同款用法） |
| `frontend/src/components/AppSettingsModal.vue` | **参考**：现有风格编辑弹窗（L2830-2915）的样式/字段风格 |
| `frontend/src/components/AppModal.vue` | **参考/复用**：弹窗容器 |

### 5.3 改动范围

- 新增 1 个组件 + client.ts 增函数 + InspectorPanel 增按钮；**不改**聊天主链路、不改 chat store
- CSS 遵循 `frontend-design.md`：scoped + CSS variables（`--accent`/`--bg-2`），禁止硬编码颜色
- 严格 TS：`npx vue-tsc -b` 必须通过

### 5.4 注意事项

- **按钮禁用态**：无当前会话（`!state.currentID`）时禁用，避免空会话提交
- **弹窗生命周期**：提交后用户可关闭弹窗，但任务仍在后台跑；再次打开可通过 job 恢复或重新发起——本期**不做断点恢复**（可选项，标记为后续）
- **流式渲染性能**：`content` 高频 delta 用 `v-text`/`ref` 直接赋值，避免重渲染整个列表
- **复用而非新造**：`getStyles`/`updateSessionMeta`/`sse.ts` 全部现成，不得复制实现
- **「应用到当前会话」**：调现有 `PATCH /sessions/:id/meta` 的 style 字段（client.ts L1205），复用输入框风格选择器同一路径

---

## 6. 任务 4：CLI `/stylegen` 指令

### 6.1 改动内容

- `internal/cli/commands.go`：注册 `/stylegen` 命令（别名 `/sg`），用法与交互风格参照 `/fork`(cmdFork L1843) / `/compress`
  ```
  /stylegen create "温柔老师" [要求...]          # 缺省来源=当前会话
  /stylegen create "温柔老师" --要求 "语气温柔..."
  /stylegen optimize <style_id> [要求...]
  ```
  - 缺省参数交互式询问补全（名称、要求、来源=当前会话；optimize 时列出现有风格供选）
  - 执行中**逐阶段打印进度行**（reading→…→saving→done + 结果 id/label）
  - 内置优化 → 提示"内置只读，已另存新风格"
- `internal/cli/context.go`：`localContext` 直接调 `stylegen.Generate`（同步跑 + 阶段行）；`httpContext` 走 HTTP API（`POST /stylegen` + 轮询 `GET /:job`）
- `internal/httpcli/client.go`：新增 `StartStyleGen` / `GetStyleGenJob`（仿 `ForkSession` L684-689 的 `doJSON` 模式）

### 6.2 涉及位置

| 位置 | 说明 |
| --- | --- |
| `internal/cli/commands.go` | 命令表（~L455 `/fork` 附近）注册 + `cmdStyleGen` handler |
| `internal/cli/context.go` | `cliContext` 接口加 `StyleGen` 方法（L73-77 附近）+ local/http 两实现（L469/1071 附近） |
| `internal/httpcli/client.go` | `StartStyleGen`/`GetStyleGenJob`（仿 L684-689） |
| `internal/cli/commands_test.go` | **参考**：`cmdFork` 的 mock 测试写法（L666-685） |

### 6.3 改动范围

- 新增 1 个命令 + context 接口各 1 个方法 + httpcli 2 个方法；**不改** CLI 主循环/REPL
- CLI 的进度展示与 GUI 的 SSE 是两条独立渲染路径，共享同一个 `stylegen.Generate` 核心

### 6.4 注意事项

- **local 模式**：直连 `memory.Store` + `stylegen.Generate`，需在 `localContext` 里能拿到 `styleMgr`/`llm` 依赖——**若 CLI local 未持有 `styleMgr`，则 local 模式走"新建 style 对象"或复用 server 的 wiring；实现时优先复用 main 的构建方式，避免重复初始化**
- **http 模式**：CLI 到 server 是异步任务，用轮询 `GET /:job` 模拟进度（CLI 是终端，不需要 SSE 长连接）
- **交互补全**：缺省交互遵循 CLI 现有 `prompt` 风格；optimize 时列现有风格（`GET /api/v1/styles` 或 `styleMgr.ListAll`）
- **错误转述**：`E_` 错误码要输出可读中文提示（如 `E_NOT_FOUND`→"风格不存在，用 /stylegen list 查看"）

---

## 7. 任务 5：接线 + 测试 + 构建验证

### 7.1 改动内容

- `cmd/pchat-server/main.go`：
  - 在 `styleMgr` 创建后（~L129）构造 `stylegen.JobManager`：`Deps{Store: memStore, StyleMgr: styleMgr, LLM: llmClient, Provider: defaultProviderName(cfg), Model: "", MaxChars: 40000}`
  - 注入 `server.NewWithStaticFS(...)`（~L267）或 `srv.Handler().SetStyleGen(jm)`（仿 `SetSummarizer` L981）
- 测试（各任务点配套）：
  - `internal/stylegen/generate_test.go`：transcript 清洗（丢 tool/thinking/command）、截断；JSON 解析（fenced/裸/兜底）；create 落库后 `ListAll`/`GetSystemPrompt`/`GetMemory` 正确；optimize 自定义原地更新、**内置→另存且原内置未动**；空对话、重复 id、不存在 id、LLM 错误分支（用 mock LLMClient）
  - `internal/stylegen/jobs_test.go`：Subscribe 收到阶段/结果事件、done 后 channel 关闭、error 传播、TTL 清理
  - `internal/server/stylegen_test.go`：POST 校验（label 必填/无会话/优化不存在）→ 202+job_id；events SSE 事件序列；503 分支
  - `internal/cli/commands_test.go`：`cmdStyleGen` mock（create/optimize/交互补全/错误转述）
- 构建：`go build ./...`；前端 `npx vue-tsc -b` + `npm run build`

### 7.2 涉及位置

| 位置 | 说明 |
| --- | --- |
| `cmd/pchat-server/main.go` | 接线（~L129 之后、L267 附近） |
| `internal/server/handler.go` | `SetStyleGen`/字段（仿 `summarizer` 字段 L31 + `SetSummarizer` L981） |
| 各新增 `*_test.go` | 与实现同包 |

### 7.3 改动范围

- main.go 增 ~10 行接线；handler.go 增 1 个字段 + setter；测试文件 4-5 个
- **不新增 upgrade 步骤**：本功能不动 `~/.p-chat` 目录结构、不改 SQLite schema、不改 config 格式——`styles` 表已存在，无需走 `internal/upgrade/`

### 7.4 注意事项

- **不碰用户数据**：测试用临时/内存 DB；不得写 `~/.p-chat/`
- **回归**：`internal/llm/client.go` SSE parser 是核心，本方案不触碰；改动只落在新增包与新增文件
- **构建顺序**：`go test ./...` → `go build ./...` → 前端 `vue-tsc -b` → `npm run build`

---

## 8. 双模式生成逻辑（生成 prompt 策略）

两种模式共用同一段"对话摘录 + 用户要求"，差异在**是否附加原风格内容**：

| 模式 | 生成 prompt 附加内容 | 输出 JSON | 落库 |
| --- | --- | --- | --- |
| `create` | 6 段式模板骨架 + 用户 requirement + 当前对话摘录 | `{id, prompt, memory}` | `Create`（新风格） |
| `optimize` | 原风格 `prompt` + `memory`（原文附上）+ 用户 requirement + 当前对话最新摘录 | `{prompt, memory}` | 自定义→`Update` 原地；内置→复制 `Create` 另存 |

- **prompt**：完整 markdown 人格（首行 `# label`，人设/性格/说话风格/表达模板/禁止项/示例六段齐全，示例用「用户：…」对话示范）
- **memory**：从对话提取的用户稳定偏好/事实（语气偏好、称呼、常用术语、目标等），200-300 字要点式；optimize 时**合并原 memory + 对话新观察**
- **id**：create 时由 label 派生 ASCII（中文→拼音/意译），仅 `[a-z0-9-_.]`

---

## 9. 边界与错误处理

| 场景 | 处理 | 错误码 |
| --- | --- | --- |
| 当前对话为空 | create：无要求→报错请用户先聊几句；有要求→仅凭要求生成 | `E_EMPTY` |
| optimize 目标不存在 | 报错并提示 `list_styles`/`/stylegen list` | `E_NOT_FOUND` |
| 目标为内置风格 | 自动复制另存新风格，原内置不动 | —（`updated=true` 提示另存） |
| LLM 输出非 JSON | 兜底整段当 prompt、memory 空，提示可后编辑 | `E_PARSE`（可降级不报错） |
| id 撞内置/已存在（create） | `Create` 报错 → 换 id 重试 | `E_DUP` |
| label/会话缺失 | 校验失败 | `E_ARGS` |
| 默认 provider 不可用 | LLM 调用失败转译 | `E_LLM` |
| 安全性 | 纯 DB 读写 + 只读会话，无沙箱确认需求 | — |

---

## 10. 验收标准

### 10.1 自动测试
- `go test -count=1 ./internal/stylegen/... ./internal/server/... ./internal/cli/...` 全绿
- `go build ./...` 通过
- `cd frontend && npx vue-tsc -b && npm run build` 通过

### 10.2 手工验证
1. 起 `pchat-server`，打开对话 → 右侧检查器「会话操作」里的「生成风格」→ 新建：填"温柔老师"+要求 → 观察阶段进度 + 流式内容 → 结果卡片 → 「应用到当前会话」→ 下一轮回复带新风格
2. 新建后再跑一次同名/近义名 → 触发 id 冲突提示
3. 优化自定义风格：选现有 → 改要求 → 预览确认 → 原地更新生效
4. 优化内置（如 `tech`）→ 提示"内置只读，已另存新风格"，`GET /api/v1/styles` 多出一条新风格，原 `tech` 未变
5. **原会话未被改动**：生成前后会话消息数、标题、风格均不变（硬性验收）
6. CLI：`/stylegen create "x" "要求"`、`/stylegen optimize <id> "要求"` 各跑通，阶段行打印正确

---

## 11. 风险与汇总注意事项

1. **只读隔离**：任务 1 全程不写来源会话——最高优先级约束，验收 10.2.5 专门验证
2. **循环依赖**：本方案不做 LLM 工具，`internal/stylegen` 位于 memory/llm 之上，无循环；**不要**把逻辑塞回 `internal/tool`
3. **内置只读**：`Update` 对内置风格报错（manager.go L175-179），optimize 内置必须走"另存"分支
4. **id 派生**：中文 label 会规范化成空串，必须由生成 LLM 产出 ASCII id
5. **SSE 生命周期**：job 完成关闭订阅 channel，防悬挂；TTL 清理防泄漏
6. **不新增 upgrade 步骤**：无目录/schema/config 变更，勿在 `internal/upgrade/` 写 ad-hoc 迁移
7. **不碰用户数据**：测试用临时 DB，不改 `~/.p-chat/`（含 config）
8. **前端规范**：scoped + CSS variables，禁硬编码颜色；`vue-tsc -b` 必过
9. **CLI local 依赖**：实现时优先复用 server 的 wiring 方式构建 `styleMgr`，避免重复初始化
10. **后续可选**：断点恢复（弹窗重开恢复 job）、对话内自然语言触发（复用 `Generate` 再做一层）、前端"设置页从对话生成"按钮
