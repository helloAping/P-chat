# Remove P2-5 Race Mode

## 目标

完整移除 v1.0.9 引入的 P2-5 多 LLM race mode（"单线 / 对比" 切换 + 3 pane 并发对比 UI）。
功能被评估为鸡肋，且存在 3 个真实 bug（session 切换不取消 race、provider < 3 时 picker 空槽、fork 失败报错）。

## 范围

**5 个文件** 改动 + **1 个文件** 删除。约 -650 行。

| 文件 | 改动 |
| --- | --- |
| `frontend/src/components/RaceView.vue` | 整文件删除（236 行） |
| `frontend/src/components/InputArea.vue` | 见 §1 |
| `frontend/src/components/ChatWindow.vue` | 见 §2 |
| `frontend/src/stores/chat.ts` | 见 §3 |
| `.agents/docs/frontend.md` | 删 P2-5 一节（line 231-257） |
| `.agents/docs/INDEX.md` | 删 P2-5 索引行（line 29） |

**不动**：`CHANGELOG.md`、round3 / round4 plan 文件（历史档案，保留做项目记录）。

---

## §1 InputArea.vue 改动

### 1.1 script 块（按行号）

**删除 line 19-27 import 中 `startRace`**：
```diff
 import {
   state, currentMeta, currentAttachments, addAttachment, removeAttachment, clearAttachments,
   isStreaming, startStream, stopStream, appendStreamEvent, endStream,
   switchSession, renameSession, createSession, deleteSessionById,
   currentMessages, appendSystemMessage, loadProviders,
   currentRollbackBanner, currentPendingInput, undoRollback, dismissRollback,
   recoverMissingParts,
-  startRace,
 } from '../stores/chat'
```

**删除 line 70-115 整块**（含 P2-5 注释 + `sendMode` ref + `isRaceActive` computed + `inputDisabled` computed + `RaceSlot` interface + `raceCandidates` ref + `syncRaceCandidates` 函数 + 3 个 watcher）：
- 删除后 `inputDisabled` 不再需要，`onKeyDown` 等地方也未引用它（已确认）
- 删除后 `sendMode` 不再需要，全文也未再引用

**删除 line 117-124 `availableModelsFor` helper** —— 全文件已无引用方。

**修改 line 10 import**：
```diff
-import { NInput, NButton, NSpace, NScrollbar, NPopover, NDropdown, NSelect, NRadioGroup, NRadioButton, useMessage } from 'naive-ui'
+import { NInput, NButton, NSpace, NScrollbar, NPopover, NDropdown, useMessage } from 'naive-ui'
```
（`NSelect` / `NRadioGroup` / `NRadioButton` 在 InputArea.vue 中唯一用处就是 race picker 那一段，已确认）

### 1.2 send() 函数

**删除 line 786-811 race short-circuit 整段**（含 P2-5 注释 + try/catch），恢复 `sending.value = true` 是 send 流程第一句。改后 send 入口直接：
```ts
sending.value = true
const ctrl = new AbortController()
...
```

### 1.3 模板

**删除 line 1062-1078** send-mode toggle row（含 P2-5 注释 + `<div class="send-mode">` + `NRadioGroup`）。

**删除 line 1080-1108** race picker row（含 P2-5 注释 + `<div v-if="sendMode === 'race'" class="race-picker">`）。

### 1.4 模板里散落的 race 引用

- **line 1154** `textarea :disabled`：从 `:disabled="inputDisabled"` 改回 `:disabled="!state.currentID"`（isStreaming 状态由 v-if/v-else 控制 send/stop 切换，按钮另有自己的 disabled，不影响；保持原单线逻辑：`!state.currentID` 或干脆删这个 prop，textarea 现在没显式 disabled 需求 —— 见 §1.4.1）
  - **§1.4.1 决定**：删掉 `:disabled="inputDisabled"` 整段属性
- **line 1155** `placeholder`：从 `isRaceActive ? 'race 进行中,等待选 winner' : (isSlashLine() ? '...' : '...')` 简化为 `isSlashLine() ? '输入 / 后跟命令 (例如 /help)' : '输入消息，Enter 发送，Shift+Enter 换行，Esc 停止，/ 前缀是命令'`
- **line 1215** 发送按钮 disabled：`:disabled="!inputText.trim() || isRaceActive"` 简化为 `:disabled="!inputText.trim()"`
- **line 1217** 发送按钮 title：`isRaceActive ? 'race 进行中' : (sendMode === 'race' ? '...' : '发送 (Enter)')` 简化为 `title="发送 (Enter)"`

### 1.5 style 块

**删除 line 1531-1578** 整段（含 P2-5 注释 + `.send-mode` / `.send-mode-hint` / `.race-picker` / `.race-slot` / `.race-slot-label` 5 个 class + 之间的注释行）。

### 1.6 InputArea.vue 改动后净行数

预计从 1955 → ~1820 行。

---

## §2 ChatWindow.vue 改动

### 2.1 import

**删除 line 7** `import RaceView from './RaceView.vue'`。

### 2.2 模板

**删除 line 226-231** race view 渲染块（含 P2-5 注释 + `<RaceView v-if="state.race" />`）。

**修改 line 249** `messages-scroll` 容器：从 `v-if="!state.race"` 改回无 v-if（race 移除后无条件渲染）。

**保留不动**：line 244 `no race with NScrollbar's` —— 这是英文 "race condition" 含义，不是 race mode 功能，**不要改**。

---

## §3 stores/chat.ts 改动

### 3.1 state 对象

**删除 line 137-174 整段**（含 P2-5 注释块 + `race` 字段定义），`recoveryBanner` 字段后直接接 `contextInspector`。

### 3.2 P2-5 race mode 全段

**删除 line 2501-2709 整段**（含开头的 `// --- P2-5 race mode ---` 长注释 + `RaceCandidate` interface + `startRace` 函数 + `cancelRace` 函数 + `pickWinner` 函数 + `lastUserMessageId` helper）。

### 3.3 散落 "race" 字样

**保留不动**（这是普通英文 "race condition" 含义，不是 race mode）：
- line 228 `// switchSession race before listMessages`
- line 446-448 dedup 注释
- line 2274-2276 question race 注释
- line 1875-1881 trace id 注释

### 3.4 chat.ts 改动后净行数

预计从 2709 → ~2450 行。

---

## §4 docs 改动

### 4.1 `.agents/docs/frontend.md`

**删除 line 231-257** P2-5 一节（标题 + 整段正文）。该节段后是 `## 修改指南`（line 259），无空白行问题。

### 4.2 `.agents/docs/INDEX.md`

**删除 line 29**：
```markdown
| **P2-5 多 LLM race mode** | [frontend.md](frontend.md) §P2-5 | [P2-5 设计](../../docs/plans/round4-trace-and-extensibility-plan.md) |
```

---

## §5 RaceView.vue 删除

直接 `git rm frontend/src/components/RaceView.vue`（或本地 `rm`，最终由 git 接管）。

---

## §6 验证步骤

按 CLAUDE.md §3.3 + §2.3 的 build gate 顺序：

```bash
# 1. TypeScript 类型检查（最严格，依赖分析最敏感）
cd frontend
npx vue-tsc -b

# 2. 前端构建
npm run build

# 3. Go 后端编译（chat.ts 是前端，但包完整性也跑一下）
cd ..
go build ./...
```

### 6.1 预期结果

- `vue-tsc -b` 通过：删除后 `state.race` / `startRace` / `pickWinner` / `RaceCandidate` 应当**完全无引用**（已通过 `grep -rE "startRace|cancelRace|pickWinner|RaceCandidate|RaceView" frontend/src/` 全量确认）。
- `npm run build` 通过：RaceView.vue 删后无悬空 import。
- `go build ./...` 通过：纯前端改动，不影响 Go。

### 6.2 手动 smoke test（可选）

启动 `pchat-server` + 前端 dev server：

1. 新建 session，正常发一条消息 —— 应能正常 stream 回复（单线模式走原路径）
2. 切到其他 session —— `MessageList` 应能立即渲染（验证 `v-if="!state.race"` 已移除）
3. 输入 / 命令 —— slash command 仍工作（不依赖 race）
4. 长对话 / 多 session 切换 —— 侧边栏正常

---

## §7 Commit

单 commit，按项目规范：

```bash
git add -A
git commit -m "feat(frontend): revert P2-5 race mode

移除 v1.0.9 引入的「单线 / 对比」多 LLM 并发对比模式
(P2-5, commit cd79717)。功能被评估为鸡肋，且存在 3 个
真实 bug：
  1. session 切换不取消 race，state.race 仍指向旧 baseSessionId
  2. provider 不足 3 个时 picker 必现空槽，stream 报错
  3. api.forkSession 失败时仅 toast，prompt 需用户手动重发

改动：
  - 删除 RaceView.vue（236 行）
  - InputArea.vue：移除 sendMode toggle + race picker +
    相关 state/computed/watcher，简化 send() 路径
  - ChatWindow.vue：移除 RaceView 渲染 + 简化 messages-scroll
  - stores/chat.ts：移除 state.race + startRace / cancelRace /
    pickWinner / RaceCandidate / lastUserMessageId
  - 文档：删除 .agents/docs/frontend.md §P2-5 + INDEX.md 索引行

CHANGELOG v1.0.9 / round 3-4 plan 文件保留为项目档案不动。
"
```

---

## §8 风险与回退

- **风险面**：纯 frontend 改动，~650 行净删除，无新增逻辑。Go 后端、API 协议、SQLite schema 均无触碰。
- **回退**：`git revert <commit>` 一键回退。RaceView.vue + chat.ts race 段在 git 历史可查。
- **数据兼容**：`state.race` 是纯前端运行时 state，不持久化（也未写 localStorage / SQLite），删除无 schema 影响。
