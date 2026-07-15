# 第二轮优化计划 — 流式 / 渲染 / 工具调用增强

> **状态**: 待评审
> **创建日期**: 2026-07-15
> **分支**: `feat_1.0.6`（基于上一轮 P0-3 auto-continue 之后）
> **关联**: `.claude/CLAUDE.md` §1.2-1.3, `.agents/docs/agent.md` §2-3, `.agents/docs/subagent.md` §1-3, `.agents/docs/frontend.md` §1-9

---

## 0. TL;DR

5 个独立可交付的子任务，按依赖排序：

| # | 任务 | 优先级 | 估时 | 后端 | 前端 | 测试 |
| --- | --- | --- | --- | --- | --- | --- |
| 0 | **P3-1 事件 sequence_id**（其它 4 项的诊断基础） | P3 | 0.5d | ✅ | ✅ | ✅ |
| 1 | **P1-1 工具结果折叠**（默认收起长 result） | P1 | 0.5d | ❌ | ✅ | ✅ |
| 2 | **P1-2 子 agent 实时进度**（取消 batch 转发） | P1 | 1d | ✅ | ✅ | ✅ |
| 3 | **P0-1 流式中断恢复** | P0 | 1.5d | ✅ | ✅ | ✅ |
| 4 | **P1-3 重新生成按钮** | P1 | 1d | ✅ | ✅ | ✅ |

合计 ~4.5d（一个人），按 0→1→2→3→4 顺序，每完成一个独立提交。

---

## 1. 设计决策一览

| 问题 | 决策 | 理由 |
|------|------|------|
| 0.1 seq 起点？ | 每个 SendMessage 调用从 0 重新计数（per-stream） | 简单，前端用 1 个 stream 内的 seq 足够；持久化用 `messages.seq` 是另一回事 |
| 0.2 seq 给前端的意义？ | 调试用 — 写入 `data:` envelope header `id: <seq>`，前端在 dev console 高亮 | 真实业务不用 seq，但**少了它 P0-1 的补齐逻辑没法定位断点** |
| 1.1 默认折叠阈值？ | result ≥ 200 字符 OR 含 ≥ 4 个换行 | shell_command / wiki_search 命中；read_file 不命中（通常短） |
| 1.2 折叠态记忆？ | 写到 `localStorage.sessionToolFolded[sessionId] = Set<toolId-or-name>` | 刷新会话后状态保留 |
| 2.1 batch 间隔？ | 100ms 累积 OR ≥ 8 个事件强制 flush | 实测打字机视觉效果保留 |
| 2.2 batch 不影响哪些事件？ | `sub_agent_start` / `sub_agent_ok` / `sub_agent_err` / `error` / `done` / `tool_confirm` / `question` — 立即转发 | 生命周期边界不能 delay |
| 3.1 恢复时怎么对齐？ | SSE 末尾 `done` 事件带 `last_message_id` + `messages.snapshot_seq`；客户端在 `streamMessagesViaFetch` 报错后 `GET /sessions/:id/messages?after_seq=N` | 简单且幂等 |
| 3.2 断点后 LLM 是否重发？ | **不重发** — 已写入的 `messages.seq > N` 视为已完成 | 避免重复内容、避免重新计费 |
| 3.3 重连窗口？ | 同会话 30s 内最多 2 次自动重连 | 防止死循环 |
| 4.1 regenerate 怎么工作？ | 服务端从 `user_message_id` 之后截断 → 重新跑 agent loop（**不重新发用户消息**） | 用户消息已存在 DB |
| 4.2 同一问题多版本？ | v1 只存最新一版（覆盖式），加 `regenerate_count` 字段 | 后续 P1-4 branching 再做多版本 |
| 4.3 regen 跟 auto-continue 关系？ | 独立计数 — regen 算新 stream，autoContinueCount 从 0 重算 | 互不干扰 |

---

## 2. 任务点列表

---

### 🔧 P3-1 — 全局事件 sequence_id（基础设施）

**目标**：所有 SSE 事件带递增 `seq`，方便调试和后续 P0-1 补齐逻辑定位断点。

**改动文件**：

| 文件 | 改动 |
| --- | --- |
| `internal/agent/agent.go` | `ChatStreamChunk` 加 `Seq uint64` 字段（json tag `seq,omitempty`） |
| `internal/agent/agent.go` `ChatStream` | 包装内部 channel，分配 `atomic.Uint64` 计数器，每个 chunk emit 前自增并赋值 |
| `internal/server/handler.go` `chunkToEvent` | `Seq` 透传到 `StreamEvent.Seq` |
| `internal/server/handler.go` `SendMessage` SSE loop | `c.Stream` 写完后追加 `\nid: <seq>\n`（标准 SSE 事件 ID 格式） |
| `internal/subagent/subagent.go` `tryForward` | 不变（seq 在 ChatStream 这一层就打好） |
| `frontend/src/api/client.ts` `StreamEvent` | 加 `seq?: number` 字段 |
| `frontend/src/stores/chat.ts` | debug 用：拿到 seq 时 `console.debug('[stream]', ev.seq, ev.type)`，**生产构建去掉**（用 `import.meta.env.DEV` 守护） |
| `frontend/src/api/client.ts` `streamMessagesViaFetch` | 解析 SSE `id:` 行写入 `ev.seq`（如果有） |

**关键点**：
- `atomic.Uint64` 必须在 `ChatStream` 入口 new，传给内部生成器；保证 per-stream 单调
- chunkToEvent 的 `Seq` 字段放在 `ev` 顶层，复用现有 JSON 序列化
- SSE `id:` 字段在浏览器原生 EventSource 中会自动断线重连时作为 `Last-Event-ID` 头；fetch 路径需要手动解析（`streamMessagesViaFetch` 中扫描以 `id:` 开头的行）

**测试**：
- `internal/agent/agent_seq_test.go` 新文件：mock LLM，订阅 stream，验证 seq 单调递增 0,1,2,3…
- 前端手动验证：dev tools network → 看 SSE 帧 `id: 0\nid: 1\n...`

**回滚**：所有改动是新增字段，删掉即可。

---

### 📦 P1-1 — 工具结果默认折叠

**目标**：长 result 默认折叠，提升长任务对话可读性。

**改动文件**：

| 文件 | 改动 |
| --- | --- |
| `frontend/src/components/ToolCallCard.vue` | 加 `shouldFold` 计算属性：`part.result` 字符数 ≥ 200 OR 换行数 ≥ 4 → `true` |
| 同上 | 加 `userFolded` 状态（默认 `null` — 跟随 `shouldFold`），点击 header 切换 `userFolded` |
| 同上 | 当 `shouldFold && userFolded !== false` → 默认收起；否则展开 |
| 同上 | header 加"📋 复制结果"图标按钮（不触发 toggle，stopPropagation） |
| `frontend/src/stores/chat.ts` | 加 `loadToolFoldState(sessionId)` / `saveToolFoldState(sessionId, state)` 工具；key 用 `tool:<name>:<first-40-chars-of-result>` 哈希 |
| `frontend/src/stores/chat.ts` `appendStreamEvent` `case 'tool'` ok 分支 | result 落地时记录 fold 状态到本地 |

**关键点**：
- 折叠态在**前端的 tool_id** 上记忆（不是 server 端）；重置在 session 切换时
- "复制结果"按钮在折叠态下也可点（防止用户想复制还得先展开）
- 不改 server 端 — result 仍是 300 字符 preview；折叠是为了 UI 不喧宾夺主

**测试**：
- `ToolCallCard.spec.ts` 新文件（Vitest，**先要确认项目有 Vitest 配置**；没有就加最小集 `vitest.config.ts` + `package.json` test script）
  - 长 result 默认折叠
  - 短 result 默认展开
  - 点击 header 切换状态
  - localStorage 持久化

**回滚**：删 `shouldFold` 逻辑即可恢复全部默认展开。

**前置**：可能需要初始化 Vitest（前端测试基础设施）。先验证。

---

### 📡 P1-2 — 子 agent 实时进度（取消 batch 转发）

**目标**：子 agent 跑期间，前端能逐步看到内部文本/工具调用/思考过程，而不是等结束后一次性 flush。

**现状**：`tryForward()` (subagent.go:145) 逐个 chunk 立即转发，**实际已经是实时**。那为什么用户看到 batch？看 SubAgentCard 渲染逻辑 — card 默认 `open=false`（line 33），且展开后 sub-body 内部 `part.parts` 不会自动增量 push（需要验证 chat.ts 的 `appendToSubAgent`）。

让我先实际验证 — 但更可能的真实问题是：
- `tryForward` 是同步回调，每次 emit 后 `partsAcc.update()` 是同步的 → **已经是实时**
- 但 sub-agent card 默认 `open=false`，且没有 streaming 指示器 → **用户体感是"等很久"**
- 关闭时才有 `<LoadingDots>` 提示 → 实际显示的就是 `...`

**重新定性为前端体验问题**：
- 真正要做的是：**让 sub-agent 卡片在跑期间默认展开**，并显示"📡 已发出 N 个工具调用"实时计数

**改动文件**：

| 文件 | 改动 |
| --- | --- |
| `frontend/src/components/SubAgentCard.vue` | `open` 默认值改成"running 时 true，结束后保持当前用户选择" |
| 同上 | header 加实时计数 chip：`{{ part.parts.length }} events` |
| `frontend/src/components/LoadingDots.vue` | 不变；只在 `part.status === 'start' && part.parts.length === 0` 时显示（已是） |

**关键点**：
- 改默认开合**会改变用户既有习惯** — 提供 `userToggled` 标志位（已有），用户点过就尊重
- 计数 chip 实时更新（Vue 响应式天然支持）
- 不动后端 — 后端逻辑已经是实时

**测试**：
- 手动：启动一次调 `task` 工具的对话，看 sub-agent 卡片是否逐步显示内部动作
- 已有 vitest 的话加 SubAgentCard 单元测试

**回滚**：把 `open = ref(...)` 改回原来的逻辑。

---

### 🔄 P0-1 — 流式中断恢复 / 重连

**目标**：SSE 中断后，前端自动 `GET /sessions/:id/messages?after_seq=N` 补齐缺失的 message parts，UI 顶部显示"已恢复 X 字符"。

**改动文件**：

| 文件 | 改动 |
| --- | --- |
| `internal/server/handler.go` `SendMessage` | done 事件多加 `StreamEndSeq int64` 字段（`messages.seq` 的最大值） |
| `internal/server/handler.go` `chunkToEvent` | `StreamEndSeq` 透传 |
| `internal/server/handler.go` `ListMessages` | 支持 `?after_seq=N` 参数（> N 的全部），加 `next_seq` 响应字段 |
| `internal/server/handler.go` 新增 `SnapshotMessageParts` 端点 | `GET /api/v1/sessions/:id/snapshot?after_seq=N` → 返回 `[{seq, parts[]}]` 增量快照 |
| `frontend/src/api/client.ts` | `StreamEvent` 加 `stream_end_seq`；加 `getSessionSnapshot(sessionId, afterSeq)` 函数 |
| `frontend/src/api/client.ts` `streamMessagesViaFetch` | catch 错误时 → 调 `getSessionSnapshot` → 把 `parts[]` 追加到 trailing assistant message |
| `frontend/src/stores/chat.ts` `appendStreamEvent` `case 'done'` | 存 `m._streamEndSeq = ev.stream_end_seq` 到 message 标记 |
| `frontend/src/stores/chat.ts` | 加 `recoverMissingParts(id)` action：找到 trailing assistant message，调 snapshot API，merge parts |
| `frontend/src/components/ChatWindow.vue` | 顶部加 `<RecoveryBanner>`，显示"📥 已恢复 N 条消息"，3s 后 fade |

**关键点**：
- snapshot 端点**只补 assistant message 的 parts**（user/tool messages 不会丢，因为是 POST body 直接写库）
- seq 边界检查：客户端发 `after_seq=N`，服务端返回 `seq > N` 的所有 assistant message 的最新 parts
- 幂等：merge 时按 `part.tool_id` / `part.text` 内容去重
- **不做自动重发**（让 LLM 重跑一遍太贵）— 只补"已写库但没传到前端"的部分

**测试**：
- `handler_snapshot_test.go` 新文件：mock 5 条 message，删一条前端的 parts，调 snapshot，验证补齐
- 前端手动：dev tools 切断网络 2s，看 banner + parts 是否补齐

**回滚**：前端 try/catch 拿掉，自动恢复分支就不执行；服务端新端点不影响其它逻辑。

---

### 🔁 P1-3 — 重新生成按钮

**目标**：assistant 消息底部加"↻ 重新生成"按钮，点击后从该用户消息后截断，重跑 agent loop。

**改动文件**：

| 文件 | 改动 |
| --- | --- |
| `internal/memory/memory.go` | 加 `TruncateMessagesAfter(sessionID, userMessageID int64) error` — 物理删 `id > userMessageID` 的行（同时回滚 `conversations.last_message_id`） |
| `internal/server/handler.go` 新增 `Regenerate` 端点 | `POST /api/v1/sessions/:id/regenerate` body `{user_message_id}` → truncate → 复用 SendMessage 的核心循环（**不重新 POST 用户消息**） |
| `internal/server/handler.go` 新增 helper `runChatStream` | 抽出 SendMessage 的 stream loop 到独立函数，SendMessage 和 Regenerate 都调 |
| `internal/agent/agent.go` `ChatRequest` | 加 `Regenerate bool` 标志，agent loop 据此跳过 `append(user message)` 那一步 |
| `frontend/src/api/client.ts` | `regenerate(sessionId, userMessageId)` 函数 |
| `frontend/src/components/MessageBubble.vue` | assistant message 底部加"↻ 重新生成"按钮（hover 时显示），点击调 regenerate API |
| `frontend/src/stores/chat.ts` | 加 `regenerateMessage(id, userMessageId)` action：调 API → 走正常 streamMessages 路径 |

**关键点**：
- **物理截断**（不是软删）：避免旧 assistant 内容残留在 DB
- 截断前先检查 user message 是否仍是 trailing user message（防止两个用户消息之间穿插 regenerate）
- 按钮只在 "user_msg_id 之后到当前最后一条 message" 都是 assistant 时显示
- auto-continue 在 regen 流程里重新计数（这是新 stream）

**测试**：
- `memory_truncate_test.go`：插 3 条 message，truncate 第 2 条后，验证第 3 条不存在、last_message_id 已更新
- `handler_regenerate_test.go`：mock 完整 regen 流程
- 前端手动：发问 → 答 → 改 prompt → 重新生成

**回滚**：删端点 + 删前端按钮。

---

## 3. 改动文件汇总

### 后端（Go）
- `internal/agent/agent.go` — P3-1, P1-3
- `internal/agent/agent_seq_test.go` (新) — P3-1
- `internal/subagent/subagent.go` — 不动
- `internal/server/handler.go` — P3-1, P0-1, P1-3
- `internal/server/handler_snapshot_test.go` (新) — P0-1
- `internal/server/handler_regenerate_test.go` (新) — P1-3
- `internal/memory/memory.go` — P1-3
- `internal/memory/memory_truncate_test.go` (新) — P1-3

### 前端（Vue 3）
- `frontend/src/api/client.ts` — P3-1, P0-1, P1-3
- `frontend/src/stores/chat.ts` — P3-1, P1-1, P0-1, P1-3
- `frontend/src/components/ToolCallCard.vue` — P1-1
- `frontend/src/components/SubAgentCard.vue` — P1-2
- `frontend/src/components/MessageBubble.vue` — P1-3
- `frontend/src/components/ChatWindow.vue` — P0-1
- `frontend/src/components/RecoveryBanner.vue` (新) — P0-1
- `frontend/vitest.config.ts` (新, P1-1 需要时) — P1-1
- `frontend/src/components/ToolCallCard.spec.ts` (新) — P1-1

### 文档
- `.agents/docs/agent.md` — P3-1 seq 字段说明
- `.agents/docs/frontend.md` — P1-1 折叠规则 + P1-2 默认展开
- `.agents/docs/server.md` — P0-1 snapshot 端点 + P1-3 regen 端点
- `.agents/docs/memory.md` — P1-3 TruncateMessagesAfter
- `CHANGELOG.md` — v1.0.7 汇总
- `README.md` — "工具结果折叠" "流式断线恢复" "重新生成" 三行

---

## 4. 实施顺序

```
Day 1 上午: P3-1 事件 seq（基础设施，0.5d）
Day 1 下午: P1-1 工具结果折叠（前端 0.5d）  → 提交
Day 2:      P1-2 子 agent 实时（前端 0.5d + 验收 0.5d）  → 提交
Day 3:      P0-1 流式中断恢复（后端 0.5d + 前端 0.5d + 验收 0.5d）  → 提交
Day 4:      P1-3 重新生成（后端 0.5d + 前端 0.5d）  → 提交
Day 4 下午: 文档 + CHANGELOG + README  → 提交
```

每完成 1 个任务点 → 立即：
1. 跑 `go test -count=1 ./internal/agent/...` 和相关包
2. 跑 `cd frontend && npx vue-tsc -b && npm run build`
3. 写 commit message：`feat(area): <task-id> <short desc>` + 详细中文 body
4. 推送到 `feat_1.0.6` 分支

---

## 5. 验收标准（每项独立）

### P3-1
- [ ] `go test ./internal/agent/...` 全绿
- [ ] dev tools 看 SSE 帧，**每帧**都有 `id: <n>` 行
- [ ] `n` 单调递增
- [ ] `n` 在 done 事件上 = 最后一个 content chunk 的 `n+1`（边界检查）

### P1-1
- [ ] `read_file` 短 result（< 200 字符）默认展开
- [ ] `shell_command` 长 result 默认折叠
- [ ] 点击 header 切换状态，刷新页面状态保留
- [ ] 折叠态下"📋 复制"按钮可点

### P1-2
- [ ] sub-agent running 时卡片**默认展开**
- [ ] header 显示实时计数
- [ ] 用户点过 collapse 后，**尊重用户选择**（不强制展开）
- [ ] running 期间 parts 数组实时增长（dev tools 验证）

### P0-1
- [ ] 主动切断网络 2s 再恢复 → banner 显示 + parts 补齐
- [ ] 多次断开重连（最多 2 次）→ 不死循环
- [ ] 服务端 `?after_seq=0` 返回所有 assistant 消息最新 parts
- [ ] 重连后 trailing assistant message 完整可读

### P1-3
- [ ] assistant 消息 hover 显示"↻ 重新生成"按钮
- [ ] 点击 → DB 中 user message 之后的 assistant 消息被物理删除
- [ ] 流重新走通，新 assistant 内容落库
- [ ] auto-continue 计数从 0 重新开始

---

## 6. 不在范围内（明确排除）

- **P1-4** 对话分支（branching）— 需要 parent_msg_id + 树状 UI，规模大，独立排期
- **P2-2** shiki 高亮 — 依赖外部库引入，独立排期
- **P2-3** context window inspector — 需要新增 inspector 端点 + 抽屉 UI，独立排期
- **P2-4** 工具 dry-run — 需要改 sandbox 系统，独立排期
- **P2-5** 多 LLM 并行对比 — 大特性，独立排期
- **P3-2** 工具 hot-reload — 需要新增 dynamic registry 整套，独立排期
- **P3-3** 端到端 trace ID — 横切所有层，独立排期

---

## 7. 风险与回滚

| 风险 | 等级 | 缓解 |
| --- | --- | --- |
| P3-1 seq 在 sub-agent 嵌套时错乱 | 中 | seq 仍是 per-stream 线性；嵌套不引入二级 seq |
| P1-1 折叠态污染 localStorage | 低 | 用 sessionId 隔离；session 删时 GC |
| P1-2 用户不习惯默认展开 | 低 | `userToggled` 标志位；点过就尊重 |
| P0-1 补齐逻辑把已显示的 parts 重复 | 中 | 严格按 `part.tool_id` 去重；dev 模式打印 diff |
| P1-3 truncate 误删 user message 之间的 assistant | 中 | 服务端先校验"待删范围全是 assistant"再动手 |

每个任务的回滚路径见各任务点的"回滚"小节。

---

## 8. 与上轮工作的衔接

- P3-1 seq 字段是**新增**，不动上一轮的 `autoContinueCount` / `pending_stream` 任何字段
- P0-1 与 P0-3 auto-continue 互补：auto-continue 防"任务未完成就退出"，snapshot 恢复防"网络断了 UI 空白"
- P1-1 / P1-2 / P1-3 都是 UI 增强，不触及 ReAct 主循环
