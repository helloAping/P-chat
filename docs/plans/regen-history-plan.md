# P1-4 重答历史 + 二次确认 规划

> **状态**: 评审中（未开始实施）
> **创建日期**: 2026-07-16
> **分支**: `feat_1.0.6`（基于 P1-3 重答按钮 + P0-3 auto-continue 之后）
> **关联**: `.claude/CLAUDE.md` §1 / §5、[`internal/server/handler.go:2780 Regenerate`](../../internal/server/handler.go)、[`frontend/src/stores/chat.ts:2164 regenerateMessage`](../../frontend/src/stores/chat.ts)、[`frontend/src/components/MessageBubble.vue:875 P1-3 重答按钮`](../../frontend/src/components/MessageBubble.vue)、[`docs/plans/auto-continue-plan.md`](auto-continue-plan.md)

---

## 0. 现状速览

| 项 | 状态 | 备注 |
| --- | --- | --- |
| **P1-3 重答按钮** | 已做 | trailing assistant 消息 hover 出现 brand 色 pill；点击立即调 `regenerateMessage` → 服务端物理删除 + 重新跑 agent loop |
| **二次确认** | 无 | 现在的"重答"零确认，鼠标误碰就清空历史 — 对比 rollback 有 `dialog.warning` 保护，重答是更破坏性的操作却没有 |
| **历史回复保留** | 无 | `Regenerate` handler (`handler.go:2818`) 直接 `DeleteMessagesFrom(user_message_id+1)`；老回复从 SQLite 里抹掉，用户无回退 |
| **左右切换** | 无 | 列表里始终是最新一次回复 |
| **历史回复 ↔ 用户消息 关联** | **需新增** | 用户提出：翻历史时不知道"这条在回哪个用户消息"；需在数据 / API / UI 三层都把关联显式化 |

**为什么 P1-3 当时没做历史保留**：核心是降低单次点击成本（"我看新回复"），但实际用户体验上看完觉得不对想看上一版，只能 fork 出去对比；这个代价过高。

**目标**：重答保留所有历史版本 + 默认展示最新；用户在消息下方通过分页条左右切换；首次点击需二次确认；**所有历史回复必须明确关联到触发的用户消息**（数据可查 / API 返回 / UI 可视）。

---

## 1. 设计决策

### 1.1 数据模型：原地复用 `messages` 表 + regen group 字段

**选项对比**：

| 选项 | 优点 | 缺点 | 选？ |
| --- | --- | --- | --- |
| A. 原地复用 `messages`，加 `regen_group_id` + `is_archived` | 零迁移成本查主列表；part / content / metadata 存储原样；rollback / fork 语义不变 | 老消息需要 backfill group_id | ✅ |
| B. 拆 `message_replies` 子表 | 主表保持极简；replies 独立生命周期 | 双写一致性；ListMessages 要 join；历史 API 路径分裂 | ❌ |
| C. 软删除 + 同表 `parent_id` 链 | 最少 schema 改动 | 主表查询要 `WHERE parent_id IS NULL` 兜底，跟 regen 语义不符 | ❌ |

**字段定义**（追加到 `internal/memory/memory.go:38 Message`）：

```go
type Message struct {
    // ... 现有字段 ...
    RegenGroupID  *string `json:"regen_group_id,omitempty"`  // NULL = 单次回复
    IsArchived    bool    `json:"is_archived"`                // 0 = 当前展示
}
```

- `regen_group_id` 是 **用户消息**的 SQLite row id（稳定，不会因为 regen 自增而变）
  - **它本身就是"回复 ↔ 用户消息"关联的载体**：group 内每条回复都能通过 `regen_group_id` 反查到对应的 user message
  - 配合 `user_message_id`（已有字段）做双向索引
- `is_archived=0` 是默认状态，对应"展示在主时间线"
- 主查询 `ListMessages` 加 `WHERE is_archived = 0`；新接口 `GET .../replies` 不过滤
- API 返回时**显式**带上 `user_message` 摘要（id / content 前 80 字 / created_at），让前端在翻归档时无需再次查询就能定位上下文

### 1.2 二次确认的两种风格

| 风格 | 体现 | 优点 | 缺点 | 选？ |
| --- | --- | --- | --- | --- |
| A. 模态对话框 | 仿 rollback `dialog.warning` | 跟项目一致；信息量大 | 弹层打断流畅 | ✅ 主推 |
| B. 内联双击 + 倒计时 | "重答? 3s" 文案 | 仿 rollback 注释里提的"pending 状态" | 易误触 | 备选 |

**主推方案 A**：在 `onRegenerate` 里用 Naive UI `useDialog`（`MessageBubble.vue:83` 已引入）弹一个小确认框 ——
> "重新生成回答？当前回答将被归档，可通过下方的分页条查看。"

提供"确认重答 / 取消"两个按钮。

### 1.3 分页 UI 位置

放在 bubble **内部底部**（紧贴 msg-meta 那行下面），**不是** toolbar 内部。理由：
- toolbar 是 hover 出现的，hover 消失分页条就没了，不可见性差
- 分页是这条回复的元数据，跟随消息稳定显示更合理
- 跟项目里 `.msg-meta`（token 数 / elapsed / model 那一行）同级

样式：小尺寸 pill，brand-50 背景，单行 `◀  2/3  ▶`：

```
┌─────────────────────────────────┐
│  Assistant · Claude 3.5 · 2.3s  │
│                                 │
│  [助手正文 / parts...]          │
│                                 │
│  1.2k↓ / 380↑ · 2.3s · claude   │
│  ┌──────────┐                   │
│  │ ◀ 2/3 ▶  │  ← 仅当 count > 1 │
│  └──────────┘                   │
└─────────────────────────────────┘
```

默认 index = 最大值（最新）；左箭头 ≤ 0 时 disabled；右箭头 ≥ count-1 时 disabled。

### 1.4 "激活"某条历史回复的语义

点 ◀ / ▶ 切换展示 = **改激活态**。所有兄弟标 `is_archived=1`，选中的标 `is_archived=0`。DB 物理行不动，seq 不变。

服务端只需一行 UPDATE：
```sql
UPDATE messages SET is_archived = CASE WHEN id = ? THEN 0 ELSE 1 END
WHERE regen_group_id = ? AND conversation_id = ?
```

前端立即更新本地 store 的对应字段（reactive 触发重渲染），无需重新拉取。

### 1.5 默认激活 = 最新

新增的回复就是新的激活态（`is_archived=0`），老的 `is_archived=1`。所以**默认展示最新**天然成立，**不需要**客户端传 "active" 参数。

### 1.6 Rollback 交互

Rollback 的 `DeleteMessagesFrom` 是物理删除 — regen group 里所有兄弟（active + archived）一并删除。这跟"撤回"语义一致（用户想回到该 user message 之前的状态），不需要单独处理。**重新生成时**会开新的 group_id（用 user_message_id 自身）。

### 1.7 存储上限（**本期实现**）

单 group 保留 N 条（**N = 20**，用户确认）。超出 FIFO 删 archived 最老的；全部 archived 时也保留最近 20。删除走 `memory.go` 的 hard delete（不走 rollback 撤销栈），仅删除**archived** 行，**不动** active 那条。

### 1.8 历史回复 ↔ 用户消息的可视关联

用户提出的核心痛点：翻归档时"这条在回谁？"。

**三层保证**：

| 层 | 机制 |
| --- | --- |
| **数据** | `regen_group_id` 本身就是 user_message_id；group 内任意一行 → 反查该用户消息 |
| **API** | `GET .../replies` 响应里**显式**带 `user_message` 字段（id + content 前 80 字 + created_at） |
| **UI** | ① 归档（is_archived=true）回复的 bubble 顶部加 `↳ 上一版回答` chip（极简，不带预览）；② 分页条带用户消息前 12 字预览 `◀ 2/3 · "请帮我..." ▶`；③ **锚定浮动按钮**（FAB）— 仿 Jump to Bottom 模式，distanceFromBottom > 50px **且** 视口内有归档回复时显示，点击 scrollIntoView 到用户消息；④ 切归档时若用户消息已滚出视口自动 smooth scroll |

**为什么默认 active 不显示 chip**：当前 active 回复上方就是用户消息本身（chat 流自然布局），上下文已可见；再加 chip 反而冗余。只有切到归档版本后才需要"找回用户消息"。

**chip 视觉**（占位设计，最终在 §3.6 详细化）：

```
┌─────────────────────────────────┐
│ ↳ 上一版回答                      │  ← 仅 is_archived=true 时
├─────────────────────────────────┤
│ [归档的旧回复正文]               │
│ 1.2k↓ / 380↑                    │
│ ◀ 1/3 · "请帮我..." ▶            │
└─────────────────────────────────┘
```

- chip 文案用"**上一版回答**"（用户确认）— 简洁、动作感强；不重复"回复"（chip 下方就是回复本身）
- 用户消息预览移到**分页条**里（`◀ 1/3 · "请帮我..." ▶`），让 chip 保持极简

**锚定按钮的可见性规则**（用户确认）：`distanceFromBottom > 50px` **且** 视口内有归档回复。
- 50px 是"冗余阈值"（用户原话）— 距离底部 ≤ 50px 时用户可以自己滚回去，按钮冗余
- 不用现有的 200px（`SCROLL_BOTTOM_THRESHOLD`）— 那是给"跟随新消息"用的，200px 太宽

**为什么 §1.8 而不是合并进 §1.4**：用户消息关联是横切关注点（数据 / API / UI 各占一块），独立成节方便评审时单独确认这一项的方案。

---

## 2. 任务点列表

> 估时合计 ~4.5d。**P1-4.1 → P1-4.2 → P1-4.3 → P1-4.4 → P1-4.5 → P1-4.6** 顺序。

| # | 任务 | 估时 | 改动文件 | 类型 |
| --- | --- | --- | --- | --- |
| **.1** | DB schema 迁移 + backfill | 0.5d | `internal/memory/memory.go`, `internal/memory/migrate.go` | DB |
| **.2** | Regen handler 改造（不删 + 标 archived）+ 20 条上限 | 1.0d | `internal/server/handler.go`, `internal/memory/memory.go` | API |
| **.3** | 列表 / 激活 API（含 `user_message` 摘要） | 0.5d | `internal/server/handler.go`, `frontend/src/api/client.ts` | API + client |
| **.4** | 前端 store（replies + userMsg 缓存） | 0.5d | `frontend/src/stores/chat.ts` | Frontend |
| **.5** | MessageBubble UI：分页条 + 历史 chip + 锚定按钮 + auto-scroll | 1.0d | `MessageBubble.vue` | Frontend |
| **.6** | 二次确认 dialog + 视觉打磨 | 0.5d | `MessageBubble.vue` | Frontend |
| **.7** | 关联反查 + 边界测试 | 0.5d | `internal/memory/memory_test.go`, `internal/server/handler_test.go` | Test |

合计 ~4.5d，分 4 个 commit：
1. **commit 1**: .1 + .2（DB + handler 改造，向后兼容）
2. **commit 2**: .3（新 API + 客户端类型）
3. **commit 3**: .4 + .5（前端 store + UI 主体）
4. **commit 4**: .6 + .7（确认 + 测试）

---

## 3. 详细设计

### 3.1 P1-4.1 — DB schema 迁移

**改动文件**：
- `internal/memory/memory.go`：`Message` struct 加 `RegenGroupID *string` + `IsArchived bool`；INSERT 路径加这两个字段
- `internal/memory/migrate.go`（新文件或扩展现有）：`schema_version` 升至 N+1
- `internal/memory/memory.go`：`ListMessages` 主查询加 `WHERE is_archived = 0`

**迁移 SQL**（在 `migrate.go` 的 step 链里追加）：

```sql
-- 1. 加列（可空 + 默认 0）
ALTER TABLE messages ADD COLUMN regen_group_id TEXT;
ALTER TABLE messages ADD COLUMN is_archived INTEGER NOT NULL DEFAULT 0;

-- 2. 索引：按 group 查兄弟 + 按 group + seq 排序
CREATE INDEX IF NOT EXISTS idx_messages_conv_group
  ON messages(conversation_id, regen_group_id, seq);

-- 3. backfill：把已经存在的 user 消息的下一条 assistant 消息 group_id 设为 user.id
--    只 backfill regen_group_id IS NULL 且 role='assistant' 且 紧跟 user 消息的行
UPDATE messages
SET regen_group_id = (
  SELECT prev.id FROM messages prev
  WHERE prev.conversation_id = messages.conversation_id
    AND prev.role = 'user'
    AND prev.id < messages.id
    AND NOT EXISTS (
      SELECT 1 FROM messages mid
      WHERE mid.conversation_id = messages.conversation_id
        AND mid.role = 'user'
        AND mid.id > prev.id AND mid.id < messages.id
    )
)
WHERE role = 'assistant' AND regen_group_id IS NULL;
```

**向后兼容**：所有 `INSERT messages` 的旧代码路径加 `is_archived = 0` 默认值。`regen_group_id` NULL 时等同于"单次回复"，主查询照常返回。

### 3.2 P1-4.2 — Regen handler 改造

**关键变化**：`handler.go:2818` 的物理删除改为**标 archived**。

```go
// 旧（P1-3）：
if _, err := h.store.DeleteMessagesFrom(id, req.UserMessageID+1); err != nil { ... }

// 新（P1-4）：
// 1. 找该 user message 后所有 assistant 兄弟，标 archived
// 2. 用 user_message_id 作为 group_id
groupID := strconv.FormatInt(req.UserMessageID, 10)
if err := h.store.ArchiveSiblings(id, groupID); err != nil { ... }
// 3. 不删除；让 agent loop 走完后 INSERT 一条新 assistant（is_archived=0）
```

**agent loop INSERT 路径**（`memory.go:241 INSERT`）加两列：
```go
`INSERT INTO messages(conversation_id, role, content, regen_group_id, is_archived, created_at) VALUES (?, ?, ?, ?, ?, ?)`
```

参数来源：`ChatRequest` 加 `RegenGroupID *string` 字段，regen handler 注入；`send` handler 传 nil。`agent.ChatStream` → `persistAssistant` → INSERT 全部透传。

**新增 store 方法**（`memory.go`）：

```go
// ArchiveSiblings 标 group 内除指定 id 外的所有行为 archived。
// 用于 regen：把当前展示的"老回复"归档。
func (s *Store) ArchiveSiblings(convID, groupID string, keepActiveID int64) error

// ListSiblings 返回 group 内全部消息（active + archived），按 seq 升序。
// 用于分页端点。
func (s *Store) ListSiblings(convID, groupID string) ([]Message, error)

// ActivateSibling 把 group 内指定 id 设为 active，其余 archived。
// 用于分页 ◀/▶ 切换。
func (s *Store) ActivateSibling(convID, groupID string, activeID int64) error
```

### 3.3 P1-4.3 — API 端点

**新增 2 个端点**（`internal/server/handler.go` 注册路由）：

| Method | Path | 用途 | 响应 |
| --- | --- | --- | --- |
| GET | `/api/v1/sessions/:id/messages/:user_msg_id/replies` | 列出一个 group 的所有回复（含用户消息摘要） | 见下方 JSON |
| POST | `/api/v1/sessions/:id/messages/:reply_id/activate` | 切换激活态 | `{ active_reply_id, replies, user_message }` |

**`GET .../replies` 响应**（关键：显式带 `user_message`，无需前端二次查询）：

```json
{
  "user_message": {
    "id": 42,
    "role": "user",
    "content": "请帮我写一个 todo list 工具",
    "content_preview": "请帮我写一个 todo list 工具",
    "created_at": 1721241600
  },
  "replies": [
    { "id": 43, "seq": 12, "is_archived": true,  "content": "第一个回复...", "model": "gpt-4o",  "created_at": ... },
    { "id": 47, "seq": 18, "is_archived": true,  "content": "第二个回复...", "model": "claude", "created_at": ... },
    { "id": 51, "seq": 22, "is_archived": false, "content": "最新回复...",   "model": "claude", "created_at": ... }
  ],
  "active_reply_id": 51
}
```

- `user_message.content_preview` = content 前 80 字符（API 层 truncate，节省 payload；前端拿来做 chip / pager 文案）
- `replies` 按 seq 升序（旧的在前）
- `active_reply_id` 冗余但显式，让前端无需遍历 find

**`POST .../activate` request body**：
```json
{ "user_message_id": 42, "reply_id": 43 }   // reply_id 必须是该 group 内的兄弟
```

**校验**：activate handler 必须验证 `reply.regen_group_id == request.user_message_id`，防止恶意客户端把任意消息 activate。

**type 扩展**（`api/client.ts:Message`）：

```typescript
interface Message {
  // ... 现有字段 ...
  regen_group_id?: string  // string form of user message id
  is_archived?: boolean
}

// 新增 replies 响应类型
interface RepliesResponse {
  user_message: {
    id: number
    role: 'user' | 'system'
    content: string
    content_preview: string
    created_at: number
  }
  replies: Message[]
  active_reply_id: number
}
```

**replies 端点的 SSE 不需要**：replies 是非流式查询，纯 JSON。**activate 也不需要 SSE** —— 单次 UPDATE 即返回完整新状态。

### 3.4 P1-4.4 — 前端 store + UI

**store 改动**（`chat.ts`）：

```typescript
// state.sessionMessages[id] 不变；分页通过 message.regen_group_id 派生
// 新增：per-session, per-group 的 replies 缓存（首次 hover pager 时按需拉）
state.sessionReplies: Record<string, Record<string, Message[]>>
//                       ↑session      ↑group_id  ↑所有兄弟（含当前 active）

// 新增：per-group 的 user message 摘要缓存（同上按需拉，避免 listMessages 全量带）
state.sessionUserMsgs: Record<string, Record<string, UserMsgSummary>>
//                      ↑session       ↑user_msg_id  ↑摘要

// 新 action
async function fetchReplies(sessionId: string, userMsgId: number): Promise<RepliesResponse>
async function activateReply(sessionId: string, userMsgId: number, replyId: number): Promise<void>
```

**`regenerateMessage` 改造**（`chat.ts:2164`）：
- **不再 pop** trailing assistant 消息
- 改为：保留原消息（标 `is_archived=true`），让 stream 创建新消息
- 关键：调 `streamRegenerate` 前，**预先**把当前 trailing assistant 的 `is_archived` 设 true（optimistic）；stream 完成后 server 落库
- stream 创建的新消息：`is_archived=false`，`regen_group_id=user_msg_id`
- 不再重置 `auto-continue` 计数（沿用 P0-3）

**MessageBubble 改造**（`MessageBubble.vue`）：

新增三块 UI，按从上到下顺序渲染：

1. **历史版本 chip**（`is_archived=true` 时显示，文案"上一版回答"）：
   ```vue
   <div v-if="message.is_archived" class="bubble-archived-chip">
     <CornerDownLeft :size="12" />
     <span class="bubble-archived-label">上一版回答</span>
   </div>
   ```
   - 极简：仅 icon + label，**不带**用户消息预览
   - 用户消息预览在第 2 块分页条里

2. **分页条**（`replySiblings.length > 1` 时显示，含用户消息前 12 字预览）：
   ```vue
   <div v-if="replySiblings.length > 1" class="bubble-reply-pager">
     <button :disabled="activeIdx === 0" @click="onPrevReply" title="更早的回复">◀</button>
     <span class="bubble-reply-pager-pos">{{ activeIdx + 1 }}/{{ replySiblings.length }}</span>
     <span v-if="userMsgPreview" class="bubble-reply-pager-context">· "{{ truncatedPreview }}"</span>
     <button :disabled="activeIdx === replySiblings.length - 1" @click="onNextReply" title="更新的回复">▶</button>
   </div>
   ```

3. **锚定 FAB**（`ChatWindow.vue` 全局浮动按钮，**不**在 MessageBubble 里）：
   - 仿 `Jump to Bottom` 模式，fixed bottom-right
   - 显示条件：`distanceFromBottom > 50px` **且** 视口内有归档回复（详见 §3.6.3）
   - 点击 `scrollIntoView` 到当前正在浏览的归档回复的 user message

**`userMsgPreview` 派生**：
- 优先从 `state.sessionUserMsgs[sid][userMsgId].content_preview` 读
- 缺失时从当前消息列表里找（`sessionMessages[sid].find(m => m.id === userMsgId)`）
- 都没有时（老消息 / 翻页未加载）显示 `''`，仅渲染 `· N/M` 数字

**`onPrevReply / onNextReply` 逻辑**：
```typescript
async function onNextReply() {
  if (activeIdx.value >= replySiblings.value.length - 1) return
  const target = replySiblings.value[activeIdx.value + 1]
  await activateReply(state.currentID, userMsgId, target.id)
  await ensureUserMsgInView(target)  // §3.6
}
```

**ChatWindow 改动**（`ChatWindow.vue`）：
- `currentMessages` 的派生逻辑**保持不变**（仍渲染 `is_archived=false` 的所有消息）
- 切换 active 是 store 直接改字段，reactive 触发重渲染
- **不**自动 scroll（避免打断用户阅读）；由 MessageBubble 内部 `ensureUserMsgInView` 决定是否需要

**`onRegenerate` 改造**（`MessageBubble.vue:575`）：

```typescript
async function onRegenerate() {
  if (regenerating.value) return
  const userMsgId = findPrecedingUserMessageId()
  if (!userMsgId) return

  dialog.warning({
    title: '重新生成回答？',
    content: '当前回答将被归档为历史版本，可通过消息下方的分页条查看。',
    positiveText: '确认重答',
    negativeText: '取消',
    onPositiveClick: async () => {
      regenerating.value = true
      try {
        await regenerateMessage(sid, userMsgId)
      } finally {
        regenerating.value = false
      }
    },
  })
}
```

### 3.5 P1-4.6 — 二次确认 + 视觉打磨

**二次确认的二次防误触**（细节）：
- 确认对话框 `positiveText: '确认重答'`（不写"重答"避免视觉混淆）
- 第一次 hover 出现 pill 按钮时**不**预热状态机；点击才进入
- 确认后服务端 stream 期间按钮维持 busy 态（沿用现有 `.bubble-action-regenerate--busy`）

**分页条视觉**（`MessageBubble.vue` style section 追加）：

```css
.bubble-reply-pager {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  margin-top: 4px;
  padding: 2px 6px;
  background: var(--brand-50);
  border: 1px solid var(--brand-100);
  border-radius: 10px;
  font-size: 11px;
  font-variant-numeric: tabular-nums;
  color: var(--brand-700);
  user-select: none;
}
.bubble-reply-pager button {
  border: none;
  background: transparent;
  color: inherit;
  width: 16px;
  height: 16px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border-radius: 50%;
  cursor: pointer;
  padding: 0;
  transition: background var(--dur-fast) var(--ease-out);
}
.bubble-reply-pager button:hover:not(:disabled) {
  background: var(--brand-100);
}
.bubble-reply-pager button:disabled {
  opacity: 0.35;
  cursor: not-allowed;
}
.bubble-reply-pager-pos {
  min-width: 28px;
  text-align: center;
  font-weight: 500;
}
```

**重答按钮的"等待二次确认"态**（可选 — 二期再做，本期不做）：
- 当前是直接弹 dialog；如果未来想换成"内联 pending 3s 倒计时"，参考 `MessageBubble.vue:1241-1262` 的 `.bubble-action-countdown` 注释（已写但未启用）

### 3.6 用户消息关联的 UI 细节

#### 3.6.1 历史版本 chip（"上一版回答"）

```vue
<div v-if="message.is_archived" class="bubble-archived-chip">
  <CornerDownLeft :size="12" />
  <span class="bubble-archived-label">上一版回答</span>
</div>
```

**极简设计**（用户确认文案）：仅一个 label + 一个回车箭头 icon，**不带**用户消息预览。用户消息预览放在 §3.6.3 的分页条里，chip 保持视觉极简。

CSS：
```css
.bubble-archived-chip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  margin-bottom: 4px;
  padding: 2px 8px;
  background: var(--surface-2);
  border: 1px solid var(--border-subtle);
  border-radius: 4px;
  font-size: 11px;
  color: var(--text-tertiary);
  user-select: none;
}
.bubble-archived-label {
  color: var(--text-secondary);
  font-weight: 500;
  letter-spacing: 0.2px;
}
```

**关键**：chip 位置在 bubble-body 内部最顶部（紧贴 assistant header 下面），不是替换 header。**active 回复不显示** chip（header 下方直接是正文）。

#### 3.6.2 用户消息可视性保证

**问题**：用户从归档回复往前/往后翻页时，对应的用户消息可能已经滚出视口（用户可能已往下阅读了 50 条新消息）。

**解决方案**（**只**在切归档版本时触发；切回最新时**不**触发）：

```typescript
async function ensureUserMsgInView(reply: Message) {
  // 只在切到归档版本时滚动（最新版本上方就是用户消息，不需要）
  if (!reply.is_archived) return
  const userMsgId = Number(reply.regen_group_id)
  if (!userMsgId) return

  // DOM 查用户消息元素（bubble 渲染时给 user 消息加 data-msg-id）
  const userEl = document.querySelector(
    `[data-msg-id="${userMsgId}"]`
  ) as HTMLElement | null
  if (!userEl) {
    // 用户消息在历史分页里（loadMoreMessages 还没拉到）；不强行滚动
    return
  }
  // 检查是否已可见
  const rect = userEl.getBoundingClientRect()
  const visible = rect.top >= 0 && rect.bottom <= window.innerHeight
  if (visible) return

  userEl.scrollIntoView({ behavior: 'smooth', block: 'center' })
}
```

**不强制**始终滚动的原因：用户可能正在做别的事（编辑输入框、阅读后续消息），强滚动会打断；只在**确认不可见**时滚动。

#### 3.6.3 分页条 + 锚定按钮

**分页条**（始终显示，`replySiblings.length > 1` 时）：

```vue
<div v-if="replySiblings.length > 1" class="bubble-reply-pager">
  <button :disabled="activeIdx === 0"
          @click="onPrevReply"
          title="更早的回复">◀</button>
  <span class="bubble-reply-pager-pos">{{ activeIdx + 1 }}/{{ replySiblings.length }}</span>
  <span v-if="userMsgPreview" class="bubble-reply-pager-context">
    · "{{ truncatedPreview }}"
  </span>
  <button :disabled="activeIdx === replySiblings.length - 1"
          @click="onNextReply"
          title="更新的回复">▶</button>
</div>
```

- `truncatedPreview` = `userMsgPreview.length > 12 ? userMsgPreview.slice(0, 12) + '…' : userMsgPreview`

**锚定按钮**（**独立于分页条**，**全局**浮动显示 — 仿 `Jump to Bottom` 模式）：

```vue
<button v-if="showAnchor"
        class="reply-anchor-fab"
        @click="scrollToUserMsg"
        title="跳到用户消息">
  <ArrowUp :size="14" />
</button>
```

**位置**：跟 `Jump to Bottom` 同位（fixed bottom-right），**叠在它上面**（`bottom: 64px`），共用同一个浮动按钮区域。

**可见性规则**（用户确认）：

```typescript
// ChatWindow.vue 提供全局滚动状态
const ANCHOR_THRESHOLD = 50   // 距离底部 > 50px 才显示（用户给的冗余阈值）

const distanceFromBottom = computed(() => /* 复用 ChatWindow 的 updateScrollPosition 派生 */)
const showAnchor = computed(() =>
  distanceFromBottom.value > ANCHOR_THRESHOLD
  && hasActiveArchivedReplyInView   // 当前视口里有正在浏览的归档回复
)
```

**`hasActiveArchivedReplyInView` 派生**（IntersectionObserver / scroll 回调维护）：

```typescript
// ChatWindow 维护一个 Set<number> = 视口里可见的 message.id
// 每次滚动时更新；O(visible messages) 成本
const visibleMsgIds = ref(new Set<number>())

function updateVisibleMsgIds() {
  const newSet = new Set<number>()
  for (const el of messagesEl.value?.querySelectorAll('[data-msg-id]') || []) {
    const rect = el.getBoundingClientRect()
    if (rect.top < window.innerHeight && rect.bottom > 0) {
      newSet.add(Number(el.getAttribute('data-msg-id')))
    }
  }
  visibleMsgIds.value = newSet
}

const hasActiveArchivedReplyInView = computed(() => {
  for (const id of visibleMsgIds.value) {
    const m = state.sessionMessages[state.currentID]?.find(x => x.id === id)
    if (m?.is_archived) return true
  }
  return false
})
```

**为什么用 50px 而不是 ChatWindow 已有的 200px（`SCROLL_BOTTOM_THRESHOLD`）**：
- 200px 用于"跟随新消息"（用户已经明显不跟随了）
- 50px 用于"主动回到上下文"（用户只是稍微偏一点，手动滚一下就到了，**不**需要按钮）

#### 3.6.4 三层保证总览

| 层 | 改动 | 文件 |
| --- | --- | --- |
| 数据 | `regen_group_id = user_message_id` | `memory.go:38` |
| API | `GET .../replies` 响应带 `user_message.content_preview` | `handler.go` 新端点 |
| Store | `state.sessionUserMsgs[sessionId][userMsgId]` 缓存摘要 | `chat.ts` |
| ChatWindow | `visibleMsgIds` 集合 + `distanceFromBottom` 派生 | `ChatWindow.vue` |
| UI | ① chip "上一版回答" ② 分页条带预览 ③ 锚定浮动按钮（FAB）④ auto-scroll | `MessageBubble.vue` + `ChatWindow.vue` |

### 3.7 存储上限 — 本期实现

**配置**：`const MaxRegenPerGroup = 20`（在 `internal/memory/memory.go`）

**触发点**：
- `ArchiveSiblings` 完成后，**在同事务内**检查 group 内 archived 行数 + active 行数 = 当前值 N
- 若 N > 20：按 `seq` 升序删 archived 最老的（保留 active 不动；保留最近 20 条）

**实现**（伪 SQL）：
```sql
-- ArchiveSiblings 事务内追加
DELETE FROM messages
WHERE id IN (
  SELECT id FROM messages
  WHERE conversation_id = ? AND regen_group_id = ?
    AND is_archived = 1
  ORDER BY seq ASC
  LIMIT MAX(0, (SELECT COUNT(*) FROM messages
                WHERE conversation_id = ? AND regen_group_id = ?) - 20)
);
```

**UI 影响**：用户看到的 `N/20` 上限不会变（active 不删）；老的 archived 自动被截断。**无需前端改动**。

**测试**：`TestRegenGroupCap` — 连续 regen 25 次，验证 archived 数量 ≤ 20、active 1 条、总数 21。

---

## 4. 边界与回归

### 4.1 行为不变的场景

- **没点过重答**：`regen_group_id = NULL`，主查询照常，无任何 UI 变化
- **点了重答但没切过分页**：UI 多了一个分页条（1/2, 2/2），点击右侧▶ 默认就是 2/2
- **rollback 后再重答**：物理删除所有兄弟，新 group_id = 当前 user_message_id
- **stream 中途断开**（P0-1 恢复路径）：恢复的 part 落到 trailing 新消息（`is_archived=0`），老兄弟不动
- **auto-continue**（P0-3）：是同一条消息内的多次 agent 循环，**不**触发 regen group；parts 都在同一 Message 里
- **同一个用户消息 20+ 次重答**：archived 老的被 hard delete（前端无感知，因为不展示）

### 4.2 需更新的回归测试

- `internal/memory/memory_test.go`：
  - `TestArchiveSiblings` / `TestListSiblings` / `TestActivateSibling` 三个基础
  - `TestRegenGroupCap` — 25 次 regen 后 archived ≤ 20
  - `TestUserMessageAssociation` — 任意 archived 行能反查到 user message
- `internal/server/handler_test.go`：
  - `TestRegeneratePreservesHistory` — 调 regen 两次，验证 ListMessages 仍只 1 条，ListSiblings 返回 2 条
  - `TestRepliesEndpointIncludesUserMessage` — 验证 `user_message.content_preview` 字段
  - `TestActivateValidatesGroupMembership` — 跨 group activate 应 400
- 手动：
  - regen 3 次 → 验证分页 1/3 → 2/3 → 3/3
  - 切到 1/3 → 验证 chip + 分页文案带用户消息预览
  - 滚到很远后切归档 → 验证 auto-scroll 把用户消息带回视口
  - rollback 后再重答 → 验证新 group 干净

### 4.3 性能影响

- `ListMessages` 主查询加 `WHERE is_archived = 0` — 走 `idx_messages_conv_seq` 过滤 row 几乎无开销（archived 行的 seq 不连续但不会太多）
- 每次 regen 后激活态切换 = 1 个 UPDATE + 1 个 SELECT（ListSiblings），< 5ms
- `GET .../replies`：单 group 通常 < 20 行，全量返回；极端情况 20+ parts × token 体积 < 100KB，可接受
- `replies` 缓存（store 内存）：每 group ~20 × 几 KB = 几百 KB；100 个 group 上限约 50MB，可接受

### 4.4 未来扩展（不在本期）

- **N 选 1 投票 / 打分**：LLM-as-judge；UI 加星标
- **diff 视图**：并排比较两条回复的 parts
- **导出历史**：把某个 group 的所有回复导出成 markdown
- **CLI 同步**：cli REPL 也加 `.regen` / `.regen --list` 子命令
- **archived 行 GC 后端 cron**：避免 SQLite 文件无限增长（当前 hard delete 已经有上限，不需要）

---

## 5. 文件清单

**新增**：
- `internal/memory/migrate.go`（如果不存在则新建；否则追加 step）
- `frontend/src/components/ReplyPager.vue`（可选 — 把 §3.4 + §3.6 的分页条 UI 抽成子组件；chip / 锚定 FAB 留在 MessageBubble / ChatWindow）

**修改**：
- `internal/memory/memory.go` — Message struct、INSERT 路径、ListMessages 查询、3 个新方法（`ArchiveSiblings` / `ListSiblings` / `ActivateSibling`）、`MaxRegenPerGroup` 上限 enforcement
- `internal/agent/agent.go` — `ChatRequest.RegenGroupID`、`persistAssistant` 透传到 INSERT
- `internal/server/handler.go` — `Regenerate` 改造（不删 + 标 archived）、2 个新端点（`GET .../replies` / `POST .../activate`）、路由注册
- `frontend/src/api/client.ts` — Message 类型加 2 字段、`RepliesResponse` 类型、3 个新 API 函数（`listReplies` / `activateReply` / 改 `streamRegenerate` 不再依赖 client pop）
- `frontend/src/stores/chat.ts` — `state.sessionReplies` + `state.sessionUserMsgs`、3 个新 action、`regenerateMessage` 改不 pop
- `frontend/src/components/MessageBubble.vue` — 二次确认 dialog、分页条 UI、"上一版回答" chip、auto-scroll；user message bubble 加 `data-msg-id` 属性供 §3.6.2 / §3.6.3 查询
- `frontend/src/components/ChatWindow.vue` — `visibleMsgIds` 集合、`distanceFromBottom` 派生、锚定 FAB（仿 Jump to Bottom）

**测试**：
- `internal/memory/memory_test.go` — 5 个单测（3 基础 + 1 上限 + 1 关联反查）
- `internal/server/handler_test.go` — 3 个集成测试

---

## 6. 不在范围内

- ~~重答时切换 provider/model~~ — RegenRequest body 不暴露 override，沿用 per-session 配置；用户确认"与主模型一致即可"
- ~~跨会话同步 regen group~~ — group 是 per-conversation 的；fork 出去的新会话一切从 0 开始（不继承 regen group）；用户确认
- ~~撤销分页切换~~ — 切换是激活态切换，没有破坏性，不需要 undo
- ~~archived 行的后端 GC cron~~ — 已有 §3.7 的 20 条上限硬删，SQLite 文件不会无限增长
- ~~regen 历史的导入/导出~~ — 见 §4.4
