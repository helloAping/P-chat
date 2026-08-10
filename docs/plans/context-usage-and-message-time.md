# 上下文占用占比徽章 + 用户消息发送时间

> 状态：已实现（含一轮需求变更，见文末「变更记录」）
> 关联模块：[`frontend.md`](../../.agents/docs/frontend.md) / [`server.md`](../../.agents/docs/server.md) / [`frontend-design.md`](../../.agents/docs/frontend-design.md)
> 决策记录：两个关键取舍已与用户确认
> - **展示形式**：上下文占用信息以**常驻徽章**形式挂在顶部"上下文占用"按钮上（点击静默刷新，不弹窗）
> - **占比分母**：百分比分母用**完整上下文窗口 `context_window`**（如 1M），与 usable 口径并存

---

## 1. 背景与现状

### 1.1 顶部"上下文占用"按钮

- 按钮位于 `frontend/src/components/TopBar.vue:197-209`，点击调 `openContextInspector()` 打开 `ContextInspectorDrawer` 抽屉并请求 `GET /api/v1/sessions/:id/context`。
- 后端端点已完整实现（`internal/server/message_helpers.go:317` `ContextInspector`），返回：
  - `context_window`：模型完整上下文窗口（如 1M）
  - `estimated_tokens`：LLM 下次请求真正看到的 token 估算（含压缩摘要，与 agent 压缩决策同口径）
  - `usable_tokens`：`context_window - max_output(8k) - 20k 缓冲`（自动压缩阈值口径，`internal/llm/token_count.go`）
  - `utilization_pct`：`estimated / usable × 100`（当前口径）
  - `messages[]`：每条消息 token 明细 + `compressed_summary` 压缩摘要
- 抽屉已展示进度条 + 明细 + 压缩摘要。

**问题**：数据只在点开抽屉后可见；按钮本身是裸图标，切会话 / 发消息后不自动刷新，顶栏无常驻可见的占比。→ 本计划让按钮"活"起来：常驻徽章 + 自动刷新。

### 1.2 用户消息发送时间

- 数据已具备：后端 `buildMessageResponse`（`message_helpers.go:531`）已把 `created_at`（Unix 秒）写入每条消息；前端 `Message.created_at?: number`（`frontend/src/api/client.ts:222`）已定义。
- 两个缺口：
  1. 乐观插入的用户消息（`frontend/src/components/InputArea.vue:1010-1015`）**未写 `created_at`** → 刚发出的消息没有时间；
  2. `MessageBubble.vue` **未渲染**用户消息时间（仅 assistant 显示耗时 / 模型 / token）。

---

## 2. 方案 A：上下文占用占比常驻徽章

### A1 后端：新增 `context_window_pct` 字段

- `internal/server/message_helpers.go` `ContextInspectorResponse` 增加：

  ```go
  ContextWindowPct float64 `json:"context_window_pct"`
  ```

  计算：`cwPct = estimated_tokens / context_window × 100`，与 `utilPct` 同样 clamp 到 `999.9`。分母用完整窗口，与用户确认的口径一致。
- 保留 `utilization_pct`（usable 口径）不动——自动压缩仍按它触发，抽屉/徽章 tooltip 双口径展示。
- 测试：`internal/server/handler_context_test.go` 增加/扩展现有断言，验证 `context_window_pct` 存在且 `0..999.9`。

### A2 前端 store（`frontend/src/stores/chat.ts`）

- 新增 **`refreshContextUsage(sessionId)`**：静默刷新，**不改变 `contextInspector.open`**，只更新 `data`。复用 `loadContextInspector` 的请求内核；`contextInspector` 为 null 时以 `{ open:false, loading:true, error:null, data:null }` 初始化。失败静默（不置 error 弹窗，仅留空，徽章据此隐藏）。
- 自动刷新触发时机（现状 `refreshContextUsage` 是唯一入口）：
  1. `switchSession`（chat.ts:472）末尾 → `void refreshContextUsage(id)`（切会话后徽章跟着换会话）
  2. 流结束：`appendStreamEvent` 的 `done` 分支（chat.ts:1834）+ `endStream`（chat.ts:2373）兜底 → `void refreshContextUsage(id)`（回复完成后数字更新）
  3. 徽章点击 → `refreshContextUsage(id)` 静默刷新（无弹窗）

### A3 前端 TopBar（`frontend/src/components/TopBar.vue`）

- 在"上下文占用"按钮处渲染**常驻徽章**：
  - 细进度条：复用阈值色（<60% `--success-500`，60–80% `--warn-500`，>80% `--error-500`）
  - 文本：`20%` + `200K / 1M`（`estimated / context_window`，compact 格式）
  - hover tooltip：`model` + `estimated / usable tokens` + 对可用窗口的百分比（标注"估算"）+ "点击刷新"
  - 点击仅静默刷新（弹窗已移除）
- **隐藏条件**：无会话 / 无数据 / `context_window <= 0` / 请求失败 / 加载中 → 不渲染徽章（按钮裸图标照旧）。
- 新增 compact token 格式化工具 `formatCompactTokens(n)` → `200K` / `12.3K` / `1.2M`；放 `frontend/src/utils/format.ts`（与时间格式化同文件）。

> **变更**：TopBar 顶部原本的**模型名称徽章（model-badge）已移除**——它和每条 assistant 消息气泡里显示的模型名重复（`MessageBubble` 的 `.bubble-header` / `.msg-meta` 已展示当前模型）。模型选择入口保留在输入区（`InputArea.vue` 的模型选择器）。
> **变更**：`ContextInspectorDrawer` 抽屉组件已整体移除（含 `openContextInspector` / `closeContextInspector` 与 `state.contextInspector.open` 字段），`refreshContextUsage` 成为唯一入口。

---

## 3. 方案 B：用户消息发送时间

### B1 数据补全（`frontend/src/components/InputArea.vue`）

- 乐观插入的用户消息（:1010-1015）补 `created_at: Date.now() / 1000`。历史消息已有后端时间，无需改动。

### B2 渲染（`frontend/src/components/MessageBubble.vue`）

- **仅 `role === 'user'`** 且 `message.created_at` 存在时，在气泡下方渲染时间戳：
  - 用户气泡右对齐（`.msg.user .bubble-col` 底部，右对齐）
  - 格式 `formatMessageTime(sec)`：当天 `HH:MM`；本年 `MM-DD HH:MM`；跨年 `YYYY-MM-DD HH:MM`
  - 样式：`font-variant-numeric: tabular-nums` + `--text-quaternary`（弱化层级），新增 scoped `.msg-time`
- assistant / system / tool 消息**不显示**。

---

## 4. 任务划分

| # | 任务 | 涉及文件 |
| --- | --- | --- |
| 1 | 后端 `context_window_pct` 字段 + 测试 | `internal/server/message_helpers.go`, `internal/server/handler_context_test.go` |
| 2 | 前端 store：`refreshContextUsage` + 刷新时机挂载 | `frontend/src/stores/chat.ts` |
| 3 | 前端格式化工具 `format.ts`（compact token + 消息时间） | `frontend/src/utils/format.ts`（新增） |
| 4 | 前端 TopBar 常驻徽章 + tooltip | `frontend/src/components/TopBar.vue`, `frontend/src/api/client.ts`（类型） |
| 5 | InputArea 乐观用户消息补 `created_at` | `frontend/src/components/InputArea.vue` |
| 6 | MessageBubble 用户消息时间戳 | `frontend/src/components/MessageBubble.vue` |
| 7 | 构建验证：`vue-tsc -b` + `npm run build` + `go test` | — |
| 8 | （变更）移除抽屉：store 去 `open` 字段 + 删 open/close action | `frontend/src/stores/chat.ts` |
| 9 | （变更）TopBar：点击改静默刷新 + 移除 model-badge | `frontend/src/components/TopBar.vue` |
| 10 | （变更）移除 `ContextInspectorDrawer` 挂载与文件 | `frontend/src/components/ChatWindow.vue`（删除 `ContextInspectorDrawer.vue`） |

---

## 5. 验证

```powershell
cd frontend
npx vue-tsc -b
npm run build
cd ..
go test -count=1 ./internal/server/...
```

手动：
1. 切会话 → 徽章数字随之变化；无上下文数据的会话徽章隐藏
2. 发一条消息并等回复 → 结束后徽章估算值上涨
3. 用户消息气泡下方显示发送时间（当天 HH:MM，跨天 MM-DD HH:MM）
4. assistant 消息不显示时间；点徽章仅刷新（不弹窗）；顶栏不再显示重复的模型名

---

## 6. 非目标（本次不做）

- 接入真实 tokenizer（仍是估算，标签"估算"）
- 恢复上下文明细抽屉（per-message token 明细 / 压缩摘要不再有 UI；数据仍可经 `/context` 端点获取）
- 非用户消息显示时间

---

## 变更记录

- **2026-08-10 需求变更**：
  - 保留上下文进度徽章，但**点击不再弹出明细抽屉**——改为静默刷新数据；`ContextInspectorDrawer` 组件删除，store 相应清理 `open` 字段与 open/close action。
  - **移除 TopBar 顶部的模型名称徽章**（与每条 assistant 消息气泡里已显示的模型名重复）；模型选择入口保留在输入区。
