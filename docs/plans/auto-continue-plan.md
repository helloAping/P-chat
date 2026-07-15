# 对话连续性 + 工具调用优化计划

> **状态**: 实施中
> **创建日期**: 2026-07-15
> **优先级**: P0（修复用户报的 bug）+ 周边优化
> **关联**: `.claude/CLAUDE.md` §1（消息流）、`.agents/docs/agent.md`（ReAct 循环）、`.agents/docs/tool.md`（todo_write）

---

## 1. 需求说明

### 1.1 背景

用户在 2026-07-15 报了一个对话连续性 bug：

> 任务未完成的情况下，LLM 会中断对话不会继续触发执行。

复现路径：

```
LLM 收到用户任务
  → 调用 todo_write([{status: "in_progress", ...}, {status: "pending", ...}])
  → 执行第一项工具（比如 read_file）
  → 调用 todo_write([第一项 done, 第二项 in_progress])
  → 发一段文本："好的，第一步完成，下面我来..."
  → 这段文本不是工具调用
  → internal/agent/agent.go:1696 命中 `if len(toolCalls) == 0`
  → Done: true, 主循环退出
  → TodoPanel 显示 2/5 未完成
  → 用户必须手动打字"继续"才能让 LLM 接着做
```

### 1.2 根因

`internal/agent/agent.go:1427-1701` 的 `for round := 1; ...` 主循环有 6 个出口，其中 #1 "LLM 自然结束"（`len(toolCalls) == 0`）完全由 LLM 自觉决定是否继续：

```go
// agent.go:1696-1701
if len(toolCalls) == 0 {
    persistAssistant(...)
    sendOrDrop(ctx, ch, ChatStreamChunk{Phase: "done", ...})
    sendOrDrop(ctx, ch, ChatStreamChunk{Done: true})
    return
}
```

而 `internal/tool/todo.go:46-56` 的 todo 系统**只用于 UI 展示**，agent 循环从不读 todo：

```bash
$ grep "GetSessionTodos" internal/agent/agent.go
# 0 命中
```

### 1.3 设计决策

| 问题 | 决策 | 理由 |
|------|------|------|
| 怎么知道任务未完成？ | 检查 todo 列表的 `in_progress` / `pending` 数量 | LLM 唯一可靠的"任务进度"信号源 |
| 在哪检查？ | 出口 #1（`len(toolCalls) == 0`）前 | 唯一 LLM 自觉的出口，最该加守卫 |
| 注入什么消息？ | `user` 角色的"任务未完成"提示 | `system` 易被忽略，`assistant` 污染历史，`user` 最像自然对话 |
| 最多续多少次？ | `MaxAutoContinue = 3` | 多了 LLM 学会偷懒，少了不够用 |
| 跟 `maxRounds` 关系？ | 独立计数 | auto-continue 是兜底，不该跟硬上限冲突 |
| 用户能禁用吗？ | per-session `AutoContinue bool`，CLI `/auto-continue` 切换，默认 true | 用户保持控制权 |
| 怎么防 LLM 虚假 done？ | 配套 P1-1（plan B）：prompt 强制要求 todo_write 收尾 | 单一方案不够，双保险 |
| 怎么防死循环？ | MaxAutoContinue 上限 + UI 显示"已自动续 N 次" | 用户能感知并可中止 |

### 1.4 不在范围内

- **P1-3** `MessageBubble.vue`（1270 行）拆子组件 — 太大，独立排期
- **P1-4** `appendStreamEvent` 加 vitest — 前端测试基础设施未就绪，独立排期
- **P3-2** `buildStaticSystemPrompt` 已经在上一轮拆完
- **`interface{}` → `any`** 已经在上一轮清完

---

## 2. 任务点列表

> 7 个任务点，估时合计 ~6h。**P0-3 是核心**，其他都是支撑。

### 🔴 P0-3 — Plan A: todo 未完成时自动续 LLM

**改动文件**：
- `internal/agent/agent.go` — 新增 `injectAutoContinue()` helper，修改主循环出口
- `internal/agent/agent.go` — 新增 `countSessionPending()` helper
- `internal/agent/agent.go` — `ChatRequest` 加 `AutoContinue bool` 字段
- `internal/server/handler.go` — SSE 事件 schema 加 `auto_continue` phase 支持（自动，已存在 schema 通过 `Phase` 字段传字符串）
- `internal/agent/agent_test.go`（新文件）— 单元测试
- `frontend/src/stores/chat.ts` — 监听 `Phase: "auto-continue"` 事件，更新 UI 文案
- `internal/cli/commands.go` — `/auto-continue` slash command 切换

**改动细节**：

```go
// internal/agent/agent.go 新增常量
const MaxAutoContinue = 3

// ChatRequest 加字段
type ChatRequest struct {
    // ... 现有字段 ...
    AutoContinue bool  // default true; per-session 开关
}

// 主循环出口 #1 之前（约 agent.go:1696）
if len(toolCalls) == 0 {
    if a.shouldAutoContinue(req, roundNum) {
        // 1. 注入 user 消息
        msgs = append(msgs, llm.ChatMessage{
            Role:    llm.RoleUser,
            Type:    llm.TypeText,
            Content: buildAutoContinuePrompt(req.SessionID),
        })
        // 2. 累计次数
        autoContinueCount++
        // 3. 发 SSE 事件
        sendOrDrop(ctx, ch, ChatStreamChunk{
            Phase:    "auto-continue",
            Step:     "todo-incomplete",
            Message:  fmt.Sprintf("检测到未完成 todo，自动续 LLM (第 %d/%d 次)", autoContinueCount, MaxAutoContinue),
            Round:    roundNum, MaxRound: maxRounds,
        })
        // 4. 续
        continue
    }
    // 原有退出逻辑
    persistAssistant(...)
    sendOrDrop(ctx, ch, ChatStreamChunk{Phase: "done", ...})
    sendOrDrop(ctx, ch, ChatStreamChunk{Done: true})
    return
}

// 新增 helpers
func (a *Agent) shouldAutoContinue(req ChatRequest, roundNum int) bool {
    if !req.AutoContinue { return false }
    if autoContinueCount >= MaxAutoContinue { return false }
    pending := countSessionPending(req.SessionID)
    return pending > 0
}

func countSessionPending(sessionID string) int {
    todos := tool.GetSessionTodos(sessionID)
    n := 0
    for _, t := range todos {
        if t.Status == "pending" || t.Status == "in_progress" {
            n++
        }
    }
    return n
}

func buildAutoContinuePrompt(sessionID string) string {
    todos := tool.GetSessionTodos(sessionID)
    var pending, inProgress []tool.TodoItem
    for _, t := range todos {
        switch t.Status {
        case "pending":     pending = append(pending, t)
        case "in_progress": inProgress = append(inProgress, t)
        }
    }
    var sb strings.Builder
    sb.WriteString("⚠ 系统检测：todo 列表还有未完成项，")
    sb.WriteString("但你刚才的回复没有任何工具调用。\n\n")
    if len(inProgress) > 0 {
        sb.WriteString("**进行中**:\n")
        for _, t := range inProgress {
            sb.WriteString(fmt.Sprintf("- [%s] %s\n", t.ID, t.Content))
        }
    }
    if len(pending) > 0 {
        sb.WriteString("\n**待开始**:\n")
        for _, t := range pending {
            sb.WriteString(fmt.Sprintf("- [%s] %s\n", t.ID, t.Content))
        }
    }
    sb.WriteString("\n请继续执行剩余任务，完成后调用 `todo_write` 标记 done 或 cancelled。")
    return sb.String()
}
```

**注意点**：
- ⚠ **`tool.GetSessionTodos` 是 `sync.RWMutex` 保护的**（todo.go:46-56），并发安全
- ⚠ **必须在 persistAssistant 之前注入消息**，否则会先 persist 一个"无工具调用"的回合，再续就乱套
- ⚠ **续的时候 roundNum 不增加**（用 `continue` 而不是 `round++`），UI 上看是同一轮
- ⚠ **autoContinueCount 要 reset**——同一会话下一次 user message 来时应该从 0 开始（新 `for round` 循环外层就是新一轮 user message，count 是 for 外面的变量，自动归零）
- ⚠ **`msgs = append(msgs, ...)` 会触发 auto-compact 重新评估**——如果太长了会触发 tryAutoCompact（这是好事）
- ⚠ **需要小心：如果 LLM 在 auto-continue 续的回合又发空工具调用**，shouldAutoContinue 仍然会 true → 进死循环。**用 `autoContinueCount >= MaxAutoContinue` 兜底**

**测试**：
```go
// internal/agent/agent_test.go 新增
func TestChatWithTools_AutoContinue_OnIncompleteTodos(t *testing.T)
func TestChatWithTools_AutoContinue_RespectsMaxAutoContinue(t *testing.T)
func TestChatWithTools_AutoContinue_DisabledViaConfig(t *testing.T)
func TestBuildAutoContinuePrompt_FormatsCorrectly(t *testing.T)
func TestCountSessionPending_CountsBothStates(t *testing.T)
```

---

### 🟠 P1-1 — Plan B: prompt 强制 todo_write 收尾

**改动文件**：
- `internal/agent/agent.go` — `buildStaticSystemPrompt` 的 todo_write 提示段（line 666-676）加强

**改动细节**：

在 `buildToolSpecificHints` 现有的 todo_write 段（agent.go:666-676）末尾追加：

```go
// 新增规则
sb.WriteString("- **重要**：在你结束当前回合前（停止调用工具、只发文本），" +
    "**必须**再次调用 `todo_write` 把列表更新到最终状态。" +
    "如果你声明任务完成但 todo 列表还有 pending/in_progress 项，系统会自动续你。\n")
```

**注意点**：
- ⚠ **改的是 system prompt 缓存前缀**——会触发所有活跃会话的 prompt 重建。预期行为（缓存是按内容 hash 算的）。
- ⚠ **效果取决于 LLM 是否遵守**——弱模型可能不读这段。这是 Plan A 的补强，不能单独依赖
- ⚠ **不改 `MaxStepsPrompt`**——那是截断场景，跟"自然结束"是两回事

**测试**：
- 这是 prompt 改动，单元测试覆盖不到。靠手动跑 + e2e 验证。
- 至少加一个 `TestBuildStaticSystemPrompt_TodoContractPromptPresent` 验证字符串包含新规则。

---

### 🟠 P1-2 — 修 `tryAutoCompact` 调用顺序

**改动文件**：
- `internal/agent/agent.go` — 移动 `tryAutoCompact` 调用（line 1708-1730）

**问题**：
当前顺序（`agent.go:1675-1730`）：

```
1. toolCalls append 到 msgs  (line 1675-1694) ← 已经 append
2. tryAutoCompact(...)        (line 1708)     ← 可能把刚 append 的吞掉
3. tool 执行                  (line 1732+)
```

注释里写了 "Re-append tool_call messages"（line 1713-1723）就是因为这个 bug——compact 把刚 append 的 tool_call 吞了，又得手动补回。

**改动细节**：
把 `tryAutoCompact` 调用移到 tool_call append **之前**：

```go
// 修改 agent.go:1674 之前
if a.tryAutoCompact(ctx, &msgs, req, ch, roundNum, maxRounds) {
    // 续
}

// 然后再 append tool_call 消息
for _, tc := range toolCalls { ... }

// 删掉原来 line 1708-1730 的 tryAutoCompact 调用 + re-append
```

**注意点**：
- ⚠ **要保留原 line 1708-1730 的兜底逻辑**——如果不命中，compact 已经做完了就 OK，删掉 re-append 块
- ⚠ **删掉的"Re-append tool_call messages" 块**（line 1713-1723）是因为新顺序下不再需要
- ⚠ **不要 `continue`**——LLM 已经决定要调用这些工具，用户期望它们执行；跳过执行会留下孤立的 tool_call 在历史里，让下轮 ReAct 卡住。`tryAutoCompact` 返回 true 只代表"我做了压缩"，不代表"你该 continue"

---

### 🟠 P2-1 — `sameToolErrCount` 不重置

**改动文件**：
- `internal/agent/agent.go` — 抽出 `injectSystemMessage()` helper

**问题**（`agent.go:2253-2270`）：
```go
if sameToolErrCount >= sameToolErrMax {
    msgs = append(msgs, llm.ChatMessage{...})  // 注入系统消息
    sendOrDrop(ctx, ch, ...)
    sameToolErrCount = 0  // ← 这里 reset 了，但
}
```

但 `stuckStreak` 没有重置——如果 LLM 在注入"改用其他方式"后还继续同工具同错误调用，`stuckStreak` 还在累计，会先撞 stuck 守卫退出。

**改动细节**：
```go
// 抽出公共 helper
func injectSystemMessage(msgs *[]llm.ChatMessage, ch chan<- ChatStreamChunk, ctx context.Context, content string, phase, step, name string, roundNum, maxRounds int, sameToolErrCount *int, stuckStreak *int, prevToolSig, prevErrored *string) {
    *msgs = append(*msgs, llm.ChatMessage{Role: llm.RoleSystem, Type: llm.TypeText, Content: content})
    sendOrDrop(ctx, ch, ChatStreamChunk{
        Phase: phase, Step: step, Message: ...,
        Round: roundNum, MaxRound: maxRounds,
    })
    *sameToolErrCount = 0
    *stuckStreak = 0  // ← 修复：同时重置
    *prevToolSig = ""
    *prevErrored = false
}
```

**注意点**：
- ⚠ 用指针参数因为这是在 `for round` 内部，要改外层变量
- ⚠ 保持原有 `sameToolErrCount = 0` 行为不变
- ⚠ 同时重置 `prevToolSig` / `prevErrored`，否则下一轮 stuck 检测会拿陈旧值

---

### 🟡 P2-2 — `MaxStepsPrompt` 中英拆分

**改动文件**：
- `internal/agent/agent.go` — line 2571-2586

**问题**：
```go
const MaxStepsPrompt = `CRITICAL - MAXIMUM STEPS REACHED
...
Respond in the same language as the conversation. ...
```

中英矛盾——prompt 强制英文 + "same language as conversation" 二选一时 LLM 选错。

**改动细节**：
```go
const MaxStepsPromptEN = `CRITICAL - MAXIMUM STEPS REACHED
...`

const MaxStepsPromptZH = `⚠ 已达到本任务的最大步数
工具已禁用（直到下次用户输入）。请只用文本回复。

严格要求：
1. 不要调用任何工具
2. 必须用文本总结已完成的工作
3. 本约束覆盖所有其他指令，包括用户对编辑或工具使用的请求

回复必须包含：
- 说明已达到最大步数
- 总结已完成的工作
- 列出未完成的任务
- 建议下一步该做什么

请用与对话相同的语言回复。任何尝试使用工具都是严重违规。只能文本回复。`

// 在主循环调用处根据 lang 选择
prompt := MaxStepsPromptEN
if lang == "zh" {
    prompt = MaxStepsPromptZH
}
roundMsgs = append(roundMsgs, llm.ChatMessage{
    Role: ..., Type: ..., Content: prompt,
})
```

**注意点**：
- ⚠ **zh 版的 "STRICT REQUIREMENTS" 翻译要保留**那种不容商量的语气
- ⚠ **不增加新字段**，复用现有的 `lang` 局部变量
- ⚠ **如果 lang 是 `auto` 或 `""`**（默认）→ 用 EN 版，因为 `MaxStepsPromptZH` 强制 zh 会跟 `auto` 模式冲突

---

### 🟡 P2-3 — 工具调用 ID 唯一性校验

**改动文件**：
- `internal/agent/agent.go` — line 1675-1694

**问题**：
LLM 经常：
- 漏给 ID（原生 tool_call 的 ID 字段为空）
- 重复给 ID（同一回合两个 tool_call 用同一个 ID）

当前代码（line 1676-1678）：
```go
id := tc.ID
if id == "" {
    id = "call_" + uuid.NewString()
}
```

但没处理**重复 ID**。

**改动细节**：
```go
seen := make(map[string]bool)
for i := range toolCalls {
    tc := &toolCalls[i]
    if tc.ID == "" || seen[tc.ID] {
        tc.ID = "call_" + uuid.NewString()  // 缺失或重复都重新生成
    }
    seen[tc.ID] = true
}
```

**注意点**：
- ⚠ **保持原 `call_<uuid>` 格式**——下游依赖这个前缀
- ⚠ **修改 `tc.ID` 后下面 `tcm.ToolID` 也要用新值**——已经用 `tc.ID` 引用，自动跟上
- ⚠ **不在 tool_call 消息持久化**层做（`AddChatMessageTo`）——那是 memory 层，不该负责去重

---

### 🟢 P3-1 — 延迟 `SessionStatus: busy`

**改动文件**：
- `internal/agent/agent.go` — 移动 line 1234

**问题**：
```go
// agent.go:1234 — 在工具加载之前发 busy
sendOrDrop(ctx, ch, ChatStreamChunk{SessionStatus: "busy"})

// 然后才开始
sendOrDrop(ctx, ch, ChatStreamChunk{Phase: "system", Step: "load-tools", ...})
availableTools := a.tools.List()  // ← 可能 100ms+
// 然后
sendOrDrop(ctx, ch, ChatStreamChunk{Phase: "system", Step: "load-system", ...})
systemPrompt, _, err := a.buildStaticSystemPrompt(...)
// 然后
sendOrDrop(ctx, ch, ChatStreamChunk{Phase: "system", Step: "ok", ...})
```

UI 在 100ms+ 里显示 "busy" 但其实没活干。

**改动细节**：
把 `SessionStatus: "busy"` 移到 `system-prompt ready` 之后（约 line 1296）：

```go
// 删掉 line 1234 的 sendOrDrop(SessionStatus: "busy")
// 删掉 line 1211-1217 的 idle defer 保持不变

// 在 line 1296 之后追加
sendOrDrop(ctx, ch, ChatStreamChunk{SessionStatus: "busy"})
```

**注意点**：
- ⚠ **defer idle 不能动**——必须保证退出时发 idle，否则前端永远 busy
- ⚠ **busy/idle 间隔可能极短**（如果 fast-fail）——没问题，UI 状态机接受
- ⚠ **Panic 路径上 busy 没发，但 defer 还是会发 idle**——但 UI 看到 idle 后才看到 panic error，合理

---

## 3. 修改文件清单

```
internal/agent/agent.go              # 主战场：~150 行新增/修改
internal/agent/agent_test.go         # 新文件：~200 行
internal/cli/commands.go             # +/auto-continue command
internal/server/handler.go           # ChatRequest schema 透传（自动）
frontend/src/stores/chat.ts          # 监听 auto-continue phase
```

预估净变化：+350 / −50 行

---

## 4. 验收清单

### 功能验收

- [ ] todo 列表有 2 项 in_progress，LLM 发空工具调用 → 自动注入续的消息，循环继续
- [ ] 续到第 3 次仍不收敛 → 不再续，正常退出
- [ ] `/auto-continue off` → 不再注入续消息
- [ ] todo 列表全 done → 不触发自动续
- [ ] 续消息里能看到 in_progress + pending 的具体内容
- [ ] 前端 UI 显示 "🔄 自动续 LLM (第 N/M 次)"

### 代码质量

- [ ] 7 个新增单元测试全过
- [ ] `go test ./...` 全 22 包通过
- [ ] `go vet ./...` 干净
- [ ] `go build ./...` 干净
- [ ] prompt 改动不破坏现有 `TestBuildStaticSystemPrompt_*` 系列测试

### 回归保护

- [ ] LLM 自然完成（todo 全 done）行为不变
- [ ] LLM 撞 maxRounds 行为不变
- [ ] LLM 撞 stuck-loop 行为不变
- [ ] LLM 撞 sameToolErr 行为不变

### 文档

- [ ] `.agents/docs/agent.md` 第 6 节（卡死循环）补一段"auto-continue 守卫"
- [ ] `CLAUDE.md` §1.2 数据流图加 "auto-continue" 阶段

---

## 5. 实施顺序

| 序号 | 任务 | 估时 | 依赖 |
|------|------|------|------|
| 1 | P0-3 主体（auto-continue） | 2h | — |
| 2 | P2-1 抽出 injectSystemMessage | 30min | — |
| 3 | P2-2 MaxStepsPrompt zh/en | 30min | — |
| 4 | P2-3 工具调用 ID 唯一性 | 30min | — |
| 5 | P3-1 busy 延迟 | 15min | — |
| 6 | P1-1 prompt 强化 todo_write 契约 | 30min | — |
| 7 | P1-2 tryAutoCompact 顺序 | 30min | — |
| 8 | CLI `/auto-continue` 命令 | 20min | P0-3 |
| 9 | 前端 `auto-continue` phase 文案 | 20min | P0-3 |
| 10 | 文档更新 | 20min | 全部 |

合计 ~6.5h

---

**文档结束。按此计划执行。**
