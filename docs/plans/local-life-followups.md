# 本地生命感后续计划

> **状态**: 待实现
> **创建日期**: 2026-11-17
> **整理日期**: 2026-11-19
> **分支**: `feat_1.0.14`
> **关联**: [`docs/feature-opportunities.md`](../feature-opportunities.md) §1.8、[`.agents/docs/frontend-design.md`](../../.agents/docs/frontend-design.md) §0.5、[`internal/server/style_compare.go`](../../internal/server/style_compare.go)、[`internal/style/manager.go`](../../internal/style/manager.go)、[`internal/memory/bookmarks.go`](../../internal/memory/bookmarks.go)

---

## 1. 需求说明

### 1.1 背景

风格对照、风格记忆、书签、回合摘要、导轨和命令面板已经能用。缺的是把这些入口补完整：对照可以自选人格、记忆可以删单条、书签可以写备注并按范围导出、旧回合标明不能撤字节、再补两条直接快捷键。

每批都能单独合入。不新开记忆体系，不改主壳布局。界面继续走 Project-Aware Calm Workbench：人格选择是输入区「换种说法」上的弹出层，对照结果仍在检查器现有区块。不新增竖栏，不新开设置页，颜色和间距走现有 CSS 变量。

### 1.2 现状

| 能力 | 现在停在哪 |
| --- | --- |
| 换种说法 | `CompareStyleIDs` 取当前风格以外的前两个，串行各等最多 45 秒。结果进检查器。点「采用」写成 `origin=style_compare` 的助手消息，会话风格不变。 |
| 风格记忆 | `remember` 确认后向 `styles.memory` 追加 `- ` 行。单条 200 字，整段 4000 字，重复行跳过。设置里是一整段文本。 |
| 书签 | 可钉、可跳、设置页按项目 / 对话 / 内容浏览。`message_bookmarks.note` 已存在，写入时固定为空。导出仍是整段会话。 |
| 回合摘要 | `turn_changes` 重启后仍可展开。字节撤销只在内存日记的最近 5 轮。最近一轮的文件列表和撤销已经在对话末尾。 |
| 导轨 | `/user-turns` 覆盖未加载历史，点击走 `scrollToMark`。刻度只有用户回合。 |
| 命令 | `Ctrl+K` 打开命令面板，内有新建对话、折叠侧栏、检查器、主题。没有 `Ctrl+N` / `Ctrl+B`。面板底栏在 `GlobalCommandPalette.vue`。 |

流式工具参数已经由 `ToolCallStatus: "args"` 转到工具卡片，卡片在 JSON 未闭合时显示「生成中…」。`docs/feature-opportunities.md` 里 AE-01、SM-03 的旧描述不要再排期。

### 1.3 设计决策

| 问题 | 决策 | 理由 |
| --- | --- | --- |
| 对照哪些人格 | 用户从已有风格里选 1 或 2 个。不选时保持现在的「另外前两个」 | 现有按钮不能失效 |
| 改写还没发出的草稿 | 用一个选定风格重写输入框文字，结果回填输入框。不写会话，不调用工具 | 草稿还不是一条消息 |
| 采用后是否换人格 | 默认只写入那条助手消息。旁边可勾选「本会话改用这个风格」，走已有 `PATCH /sessions/:id` 的 `style` | 采用和换人格是两件事 |
| 记忆存在哪 | 继续整段存在 `styles.memory`。只把以 `- ` 开头的行当成可删条目。其余段落保留 | 不新增表，不增加 upgrade 版本 |
| 书签备注放哪 | 气泡仍是钉上 / 取消。备注在设置页和检查器的书签列表里改，最长 200 字 | 气泡不加第二套编辑器 |
| 范围导出 | 只加 Markdown。同一会话的两条书签决定 `from_seq` / `to_seq`，含两端。HTML / PDF 忽略这两个参数 | 避免半截 PDF |
| 旧回合能不能撤字节 | 不能。摘要继续持久化。历史回合写明只能看。不把文件字节写入 SQLite 或磁盘 | 字节日记只留在内存 |
| 快捷键 | 保留 `Ctrl+K` 面板。补 `Ctrl+N` 新建对话、`Ctrl+B` 折叠侧栏。面板底部写出这两条，不另做帮助页 | 面板已经能做这两件事，缺的是直接键 |

### 1.4 不在范围内

- 晨报、全局记忆卡、独立事实表
- 把文件字节日记落到磁盘
- 重做流式工具参数（AE-01 已落地）
- 重做命令面板，或把 `Ctrl+K` 改成搜索
- Runtime Trust 与 Windows 危险命令规则

---

## 2. 任务点列表

P1 → P2 → P3 → P4。P5、P6 可随时插入，互不依赖。

P1 到 P3 是这一轮的趣味主体。P4 是书签的分享能力。P5 只改文案和条件。P6 是操作补齐。

### P1 — 换种说法：自选人格、改写草稿、可选换人格

**改动文件**：

- `internal/server/style_compare.go` — `POST /api/v1/sessions/:id/style-compare` 接受可选 `styles`
- `frontend/src/api/client.ts` — 请求体带上选中的风格 id
- `frontend/src/components/InputArea.vue` — 「换种说法」弹出层：对照 / 改写草稿
- `frontend/src/components/InspectorPanel.vue` — 「采用」旁的「本会话改用这个风格」
- `frontend/src/stores/chat.ts` — 勾选后走已有 `updateSessionMeta(id, { style })`

**改动细节**：

- `styles` 缺省或空数组：沿用 `CompareStyleIDs`。
- 1 或 2 个：必须是已存在、且不是 `off` 的风格。允许选中当前会话风格，这样「用现在的人格重写」成立。
- 超过 2 个，或含未知 id：400。
- 两次补全改为并行。单次超时仍是 45 秒。无工具、`MaxTokens` 700 不变。
- 对照：沿用检查器。选中的风格名出现在卡片上。
- 改写草稿：只在输入框非空时可用。选一个风格，请求同一接口，把返回文本填回输入框。失败时保留原文。不产生新消息。
- 采用且勾选后，下一条用户消息走新风格。不勾选时行为与现在相同。
- 流式回复进行中，按钮保持禁用。

**验收**：

- 不选风格时，结果仍是另外两个风格。
- 指定一个风格时，只返回那一个。
- 改写草稿不产生新消息。
- 采用且勾选后，下一条用户消息走新风格。
- 流式回复进行中，按钮保持禁用。

### P2 — 风格记忆单条删除

**改动文件**：

- `internal/style/manager.go` — 按原文删除一条 `- ` 记忆
- 设置里的风格编辑器 — 条目与其余文本分开
- `internal/tool/remember.go` — 确认框和追加规则保持不变

**改动细节**：

- 找不到该行时幂等成功。
- 4000 字上限只约束追加，删除只会变短。
- 设置里把记忆拆成「条目」和「其余文本」。条目一行一个删除按钮。其余文本继续用现在的文本框。
- 保存时先写其余文本，再按原顺序拼回条目。

**验收**：

- 删一条后，该风格提示词不变，下次注入看不到这一条。
- 重复删除不报错。
- 非 `- ` 开头的手写段落还在。
- 不新增表，不增加 upgrade 版本。

### P3 — 书签备注和导轨刻度

**改动文件**：

- `internal/memory/bookmarks.go` — 读写已有 `note` 列，无需迁移
- `internal/server` 书签路由 — 新增 `PATCH /api/v1/sessions/:id/bookmarks/:message_id`
- `frontend/src/components/BookmarkSettings.vue` — 每条书签可改备注
- 检查器里的书签列表 — 同一句备注
- `frontend/src/utils/sessionMinimap.ts`、`frontend/src/components/ChatWindow.vue` — 书签刻度，点击复用 `scrollToMark`

**改动细节**：

- body `{ "note": "..." }`。空字符串清空。超过 200 字返回 400。未钉住的消息返回 404。
- 列表响应带上 `note`。
- 气泡不加第二套编辑器。
- 导轨在用户回合之外，为当前会话已加载的书签再画一种刻度。点击时在 DOM 里就滚动，不在就走搜索跳转。

**验收**：

- 备注重启后还在。
- 取消书签再钉上，备注清空。
- 导轨能点到未加载页里的书签。
- 没有书签时导轨外观与现在一致。

### P4 — 按书签范围导出 Markdown

**改动文件**：

- `internal/server/export.go` — `GET /api/v1/sessions/:id/export` 增加可选 `from_seq`、`to_seq`
- `internal/export` 的 Markdown 路径 — 只输出范围内的消息
- `frontend/src/components/BookmarkSettings.vue` — 同一对话下「从这条到那条」

**改动细节**：

- 两个参数只在 `format=md|markdown` 时生效。缺一个、非整数、或 `from_seq > to_seq` 时返回 400。
- 两端书签所在消息都包含。中间消息按原顺序。
- HTML / PDF 忽略这两个参数。
- 复制或下载的是 Markdown 正文，不是整段会话。

**验收**：

- 两端书签所在消息都在结果里，中间消息按原顺序在。
- 不带参数时，导出与现在字节一致。
- HTML / PDF 不因带了 seq 而改内容。

### P5 — 旧回合标明不能撤字节

**已落地**：最近一轮的文件列表和撤销在对话末尾，不在输入框上方。

**改动文件**：

- `frontend/src/components/MessageBubble.vue` — 历史「本回合」展开时的说明

**改动细节**：

- 不持久化字节。`turn_changes` 的路径和预览继续留下。
- 若该消息不是内存日记里可撤销的最近一轮，显示一行说明：摘要会留下，字节撤销只作用于最近一轮，重启后不可用。

**验收**：

- 重启后旧回合仍能看到路径和预览，且能看出不能撤销。
- 撤销最近一轮的现有测试保持通过。

### P6 — Ctrl+N / Ctrl+B

**改动文件**：

- `frontend/src/App.vue` — `onGlobalKeydown`
- `frontend/src/utils/globalCommands.ts` — 复用已有的新建对话、折叠侧栏
- `frontend/src/components/GlobalCommandPalette.vue` — 底栏文案

**改动细节**：

- `Ctrl+N` 或 `Meta+N`：新建当前项目对话。
- `Ctrl+B` 或 `Meta+B`：折叠或展开侧栏。
- 输入法组合中不触发。命令面板打开时不抢走方向键和 Enter。
- `Ctrl+K` 的开关行为不变。

**验收**：

- 焦点在输入框时这两条仍然生效，并且不会把 `N` / `B` 打进草稿。
- 面板过滤和 `Esc` 关闭不变。

---

## 3. 完成时要改的文档

每批落地后同步：

- `README.md` 的「GUI 操作速查」
- `docs/feature-opportunities.md` §1.8，并把对应的 SM-03 / SM-06 改成部分落地或已落地
- `.agents/docs/INDEX.md` 里风格记忆那一行

本计划留在 `docs/plans/`。实现细节不写回 feature-opportunities 的任务大表。
