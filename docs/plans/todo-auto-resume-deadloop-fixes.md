# 任务规划：todo 自动续跑死循环修复

> **状态**: 规划中（未实施，仅文档）
> **创建日期**: 2026-08-05
> **优先级**: P0（止血）+ P1 + P2
> **来源**: my-blog 会话 conv_1785593685713979400_2 死循环分析
> **关联**: `.agents/docs/agent.md`（ReAct 循环）、`.agents/docs/tool.md`（todo_write）、`.agents/docs/llm.md`（上下文窗口）、`internal/server/messages.go`（auto-resume）、`internal/agent/auto_continue.go`（auto-continue）、`internal/agent/agent.go`（ChatWithTools / tryAutoCompact）

---

## 1. 背景与根因

### 1.1 现象

my-blog 最新会话（`conv_1785593685713979400_2`）累积 **5041 条消息**，出现：

- 触发"继续处理任务" **51 次**（其中 seq=4852 / 4943 是 2026-08-05 新增的"todo 未完成自动续跑"功能触发）
- "中断/超时" 34 次、"重试" 50 次、工具失败 **810 次**
- 同一 turn（`T-32964d67`）内 `estimated_tokens` 从 **19.9 万单调涨到 80 万**（context_window 仅 6.4 万，**超窗 12.6 倍**），`messages=1` 但请求体 body 高达 3.5MB

### 1.2 根因链（4 环）

```
① auto-compact 压缩失效
   → 压缩后 messages=1 但 estimated_tokens 20万起（窗口仅 6.4万）
② LLM 收到超长混乱上下文（20~80万 token）
   → 无法理解任务，输出浅层内容 + 反复调 go test
   → 工具结果（go test 输出 / findstr 错误）不断累加 → 上下文更大
③ turn 无法正常完成：LLM 不标记 todo #2 done
   → 每次 turn 结束（maxRounds / 15min 超时）
   → todo 未完成 → 自动续跑（2026-08-05 新增功能）
   → 新 turn 又发起超窗请求 → 循环
④ 熔断未拦住
   → same-tool（4 次相同调用）要求"相同失败"，命令参数略变即不熔断
   → CumToolErrMax=8（不同失败累计）被 turn 边界重置
```

### 1.3 关键证据

| 证据 | 来源 |
| --- | --- |
| `messages=1 estimated_tokens=199375 → 806523`，`body_bytes=878KB → 3.5MB` | `dev-bin/.p-chat/logs/server-debug-2026-08-05.log`（T-32964d67，23:23-23:38） |
| 每次超窗请求上游都正常返回（LLM 确实收到超长上下文），但输出仅"让我回顾/梳理/理解" | 同日志 `[llm/ollama] raw` 片段 |
| seq=4852/4943 两条"上一回合因任务尚未完成被中断，请继续完成…" | DB messages 表 |
| 工具失败 810 次（findstr 参数错、误判 `[no test files]` 为错误） | DB messages 表 pattern 统计 |
| DB 单条消息最大仅 43KB → `messages=1` 的 20万 token 必是运行时拼接（非 DB 单条） | DB `length(content)` 查询 |

### 1.4 参考方案（Codex 机制）与 P-Chat 现状

用户参考 Codex（Rust 实现的 CLI）的上下文管理机制，主张**超窗时应让对话继续，而不是中断**：

| Codex 机制 | 说明 | P-Chat 现状 |
| --- | --- | --- |
| 自动压缩（Auto-compaction） | token 接近上下文窗口上限时自动触发，把较早的消息总结成摘要并**替换**原消息腾出空间；长任务靠它最多连续跑 ~7 小时 | 已有 `Summarizer.Compress`（总结历史），但 my-blog 场景压缩后仍超窗（T2 要修） |
| 头部裁剪（Head trimming） | 压缩后空间仍不够时，从最早的消息开始**直接砍掉**（丢弃而非摘要），确保对话能继续 | 有 `truncateToFit`（丢最老），但对单条超大消息无效 |
| 手动 `/compact` | 随时手动总结历史、保留关键上下文（项目结构、未完成工作、决定等） | 已有（CLI `/compact`） |
| `/resume` 会话恢复 | 会话自动保存（线程 ID 标识），`codex resume` 恢复完整历史 | 已有会话持久化 + 自动续跑 |
| API 层 `context_management` | OpenAI 提供 `context_management=[{"type":"compaction","compact_threshold":200000}]` 在应用层配置压缩阈值 | 未用（P-Chat 走自定义 SSE reader） |

**设计原则（据此修订 T1）**：超窗时按"总结替换 → 头部裁剪"逐级收敛，**始终保证对话能继续**；只有极端情况（裁剪后单条仍超窗）才做更激进的"截断超大内容 / 裁剪工具"，但**不直接中断对话、不拒绝发送**。

---

## 2. 任务总览

| # | 优先级 | 任务 | 目标 | 依赖 |
| --- | --- | --- | --- | --- |
| T1 | **P0** | 请求前强制上下文收敛（压缩→裁剪） | 超窗自动收敛，对话不中断（止血） | 无 |
| T2 | **P0** | 定位并修复 auto-compact 后仍超窗的机制 | 根治"messages=1 却 20万 token" | T1 之后便于复现 |
| T3 | **P1** | 自动续跑"无进展熔断" | 防止续跑功能放大死循环 | 无 |
| T4 | **P1** | 熔断跨 turn 累计（same-tool / CumToolErrMax） | 拦住 LLM 工具失败循环 | 无 |
| T5 | **P2** | LLM 工具失败 prompt 强化 | 减少无效工具重试 | 无 |

---

## 3. 任务详情

### 任务 T1（P0）：请求前强制上下文收敛（压缩→裁剪，对话不中断）

#### 背景
当前 LLM 请求在 `estimated_tokens` 严重超窗（80万 vs 6.4万）时**仍然发出**，上游硬扛返回超长上下文，LLM 无法正常处理。这是整个死循环的入口。必须先止血：任何请求发出前必须保证 token 在窗口内。

**方案约束（对齐 Codex 机制，见 §1.4）**：超窗时不中断对话、不拒绝发送，而是按"总结替换 → 头部裁剪 → 截断超大内容/裁剪工具"逐级收敛，**始终保证对话能继续**。原方案"拒绝发送 + error+done 提示开新对话"已废弃——它会令长任务在接近窗口时直接断掉，违背 Codex 式"总结并替换、对话继续"的原则。

#### 涉及点
- `internal/agent/agent.go` — `ChatWithTools` 主循环：
  - 约 1519 行 `tryAutoCompact` 调用（压缩判定）
  - 约 1626 行 `a.llm.ChatStreamCM(...)`（实际发请求处，即 `att:` 重试循环内）
- `internal/llm/token_count.go` — `EstimatePromptTokens` / `UsableContextWithBuf` / `ShouldCompactWithBuf`
- `internal/agent/util.go` — `truncateToFit`（头部裁剪工具，2026-08-05 已改 O(n)）
- `internal/agent/tool_result_cache.go` — 超大 tool result 截断/预览（供第三级"截断超大内容"复用）
- `internal/agent/agent.go` — `toolDefsForPhase`（供"裁剪工具列表"复用）

#### 改动点
在 `a.llm.ChatStreamCM(...)` 调用前强制执行一个**分级收敛函数** `EnsureWithinWindow(msgs, tools, ctxWindow)`，保证 `estimated_tokens ≤ usableWindow` 且对话不中断：

1. **第一级 · 总结替换（Auto-compaction）**：调用现有 `tryAutoCompact` / `Summarizer.Compress`，把最早的未压缩消息总结成摘要并**替换**原消息。压缩后达标则结束。
   ```go
   if llm.EstimatePromptTokens(roundMsgsForLLM, roundTools) > usableWindow {
       // 1) 总结替换（复用 tryAutoCompact / Summarizer）
   }
   ```
2. **第二级 · 头部裁剪（Head trimming）**：压缩后仍超窗时，从最早的非 system 消息开始**直接丢弃**（不摘要）直到 ≤ usableWindow。复用/强化 `truncateToFit`（丢弃最老、保留最新）。
3. **第三级 · 强制最小集（极端兜底，不拒绝）**：若裁剪到只剩 system + 最新一条仍超窗（单条超大 tool result 或工具定义超窗）：
   - **截断超大 tool result**：保留头部摘要（复用 `tool_result_cache` / `MaxToolResultFullBytes` 逻辑），丢弃超长正文；
   - **裁剪工具列表**：按 `toolDefsForPhase` 只保留当前阶段必需工具，缩小 `EstimateTokensTools`；
   - **兜底最小集**：即使全部截断后仍超窗，也保留"system prompt + 最新用户意图"的最小上下文**发出请求**，绝不拒绝/中断。
4. 全程通过 phase 事件向前端告知"上下文超限，已自动压缩/裁剪历史"，用户可感知但对话持续。

#### 注意点
- **不破坏正常压缩路径**：`tryAutoCompact` 正常触发逻辑不变，`EnsureWithinWindow` 作为它之后的兜底（压缩 → 裁剪逐级收敛，只有确实还有空间时才发）。
- **单条超大消息**：`truncateToFit` 对单条超大消息无效（`kept=[最新一条]` 仍超窗）——此时走第三级：截断该消息内容 / 裁剪工具，**而不是拒绝发送**。
- **工具定义也可能超窗**：`EstimateTokensTools(tools)` 若接近/超过窗口，压缩消息无效——需要裁剪工具列表（见 T2）。
- **保留最小上下文**：任何裁剪都不能丢掉 system prompt 和最新用户意图，否则 LLM"失忆"、对话失焦。
- **测试要求**：
  - 构造超窗 msgs（含单条超大消息），断言请求**仍被发出**且 `estimated_tokens ≤ usableWindow`（通过压缩/裁剪/截断实现），而非拒绝。
  - 断言裁剪后 system prompt + 最新用户消息仍保留。
  - 断言发送了"上下文超限，已自动压缩/裁剪"的 phase 事件。

---

### 任务 T2（P0）：定位并修复 auto-compact 后仍超窗的机制

#### 背景
压缩后 `messages=1` 但 `estimated_tokens` 仍有 20万（窗口 6.4万）。DB 中单条消息最大仅 43KB，因此这条"1 条消息"的 20万 token 是**运行时拼接的巨型内容**。机制未明，需先定位再修（盲改风险高）。已排除的候选：`CompressedSummaryFor` 拼接 263 条 summaries 仅 54KB（≈1.5万 token），不是主因。

#### 涉及点（需逐一排查）
- `internal/agent/agent.go` — `tryAutoCompact` 成功分支（约 3072-3113 行）：
  - `LastCompressedIDFor` + `GetChatMessagesAfterIDFor` 重新加载 `hist`
  - `CompressedSummaryFor` 拼入 system
  - `newMsgs = [system(含summary), ...hist]`
  - 若 `hist` 含巨型 tool result，或 summary 累积，都会超窗
- `internal/server/messages.go` — `loadHistoryForSend` / `buildLLMMessages`（resume 时加载整个历史）
- `internal/llm/token_count.go` — `EstimateTokensTools`（31 个工具 schema 是否本身巨大）
- 怀疑方向：
  1. **工具 schema 超窗**：`tools=31` 的 `EstimateTokensTools` 是否单独就接近/超过 6.4万？若是，auto-compact 只压消息永远无效。
  2. **运行时拼接**：某处把大量 tool result / 历史拼进单条消息（system 或 user），DB 不落库所以查不到。
  3. **summary 累积异常**：compSum 拼接是否因 range 覆盖异常（首个 summary range span 1019 亿）导致重复/膨胀。

#### 改动点（定位后实施）
1. **先加诊断**：在 `ChatStreamCM` 前打印 `EstimateTokensMessages(msgs)` 与 `EstimateTokensTools(tools)` 的分解（当前日志只打 total），区分是消息还是工具占大头。
2. 根据诊断结果修复：
   - 若工具超窗 → 按 phase 裁剪工具列表（`toolDefsForPhase`）或压缩工具 schema。
   - 若消息拼接 → 修正拼接逻辑（限制单条消息 / 截断 tool result / 正确压缩）。
   - 若 summary 异常 → 限制 summary 总量或重置异常 range。

#### 注意点
- **必须带诊断数据定位后再改**，不臆测。
- 涉及 LLM 请求体构造，改动需配套回归测试（正常会话压缩行为不变）。
- 与 T1 配合：T1 负责"任何请求发出前都收敛到窗口内"（压缩→裁剪→截断逐级兜底），T2 负责"为什么压缩/裁剪后仍超窗"的根治（工具定义超窗 / 运行时拼接 / summary 异常）。

---

### 任务 T3（P1）：自动续跑"无进展熔断"

#### 背景
2026-08-05 新增的"todo 未完成自动续跑"功能在 my-blog 会话被死循环放大：LLM 无法完成任务 → 每轮 turn 结束 → todo 未完成 → 自动续跑 → 新 turn 又失败 → 再续跑（seq 4852/4943）。**续跑后 todo 状态无任何变化**（无 done / 无新 todo_write），属于无效续跑，应熔断。

#### 涉及点
- `internal/server/messages.go` — `SendMessage` 的 turn 循环（resume 分支，约 286-311 行）+ `respondSSE`：
  - error+done 自动续跑分支（约 613-626 行）
  - 正常 done + todo 未完成自动续跑分支（约 628-648 行）
- `internal/agent/auto_continue.go` — `HasPendingTodos` / `BuildAutoResumePrompt`
- `internal/tool/todo.go` — `GetSessionTodos`（todo 状态来源）

#### 改动点
1. 记录每次**自动**续跑触发时的 todo 快照（未完成项 ID 集合，或"未完成项数"）。
2. 每次自动续跑前对比：若与上次快照**无变化**（未完成项数未减少 / 无新 done），累计"无效续跑计数"；达到 N（建议 2~3）后**停止自动续跑**，转为正常 error+done，提示用户手动介入。
3. 状态存储选择：
   - **内存 map**（按 sessionID）：实现简单，但 server 重启丢失、多实例不共享。
   - **memory store**（需新增字段/表）：持久，但要 schema 变更（走 upgrade 流程）。
   - 初始建议内存 map + session 隔离 + 清理（会话结束/turn 完成后清除）。
4. **手动"继续"不受熔断影响**：用户主动发送新消息应重置计数。

#### 注意点
- 熔断计数与 `MaxTurnRetries` 预算的关系：建议"无效续跑计数"独立于 MaxTurnRetries，专门防"todo 无进展死循环"。
- 状态并发安全：Agent/Handler 可能多 session 并发，用 `sync.Map` 或按 session 加锁。
- 区分自动 vs 手动：自动续跑是 server 注入的 resume（`ClientMsgID=0` + `TodoMode=Resume`），手动是用户真实消息（有 `ClientMsgID`）——据此区分并重置计数。
- **测试要求**：
  - mock LLM 永不更新 todo，断言自动续跑 N 次后停止并给出 error+done。
  - 手动发消息后计数重置。
  - 正常任务（续跑后 todo 有进展）不熔断。

---

### 任务 T4（P1）：熔断跨 turn 累计（same-tool / CumToolErrMax）

#### 背景
LLM 反复工具失败（810 次）未被熔断拦住。`same-tool`（同命令连错 4 次）和 `CumToolErrMax`（不同失败累计 8 次）的计数是 `ChatWithTools` 的**局部变量**，每次 turn 新建 → 被 turn 边界重置。LLM 每轮新 turn 又重新失败循环。

#### 涉及点
- `internal/agent/agent.go` — `ChatWithTools`：
  - 约 1410 行 `sameToolErrCount` 局部变量
  - 约 2869 行 `cumToolErrCount` 局部变量
  - `sameToolErrMax = 4`（约 1448 行）、`CumToolErrMax = 8`（auto_continue.go:42）
- `internal/agent/agent.go` — Agent 结构（约 47 行），需加按 session 的熔断状态

#### 改动点
1. 把熔断计数从"局部变量"提升为 **Agent 级按 session 的累计状态**（如 `sync.Map[sessionID]*breakerState`）。
2. same-tool 熔断：跨 turn 累计同一"失败命令签名"（命令名 + 归一化参数），达到阈值注入"不要重试，换方式"。
3. CumToolErrMax：跨 turn 累计不同失败，达到阈值强制转总结。
4. 提供清理机制：turn 正常完成 / 会话切换 / 超时后重置（避免永久误伤）。

#### 注意点
- **会话隔离 + 并发安全**：多 session 并发时不能互相污染，用 sessionID 隔离 + `sync.Map` / 锁。
- **命令签名归一化**：LLM 每次命令参数略变（`test_all.txt` → `test_out.txt`）会绕过 same-tool——归一化参数（去文件名/时间戳差异）才能命中。
- **避免误伤**：正常的多轮任务不应被跨 turn 熔断误杀；只在"同命令反复失败"或"累计失败异常多"时触发。
- 熔断后要能恢复（用户明确换任务 / 手动继续时重置）。
- **测试要求**：mock 同命令跨两个 turn 失败，断言第二个 turn 触发熔断。

---

### 任务 T5（P2）：LLM 工具失败 prompt 强化

#### 背景
LLM 在工具失败后反复重试同一命令（810 次失败），且误把 `[no test files]`（正常输出）当错误、findstr 参数写错。系统 prompt 对"工具失败如何应对"的约束不足。

#### 涉及点
- `internal/agent/prompt.go` — 系统 prompt 的工具使用约束段（`buildStaticSystemPrompt` 相关）
- `internal/agent/agent.go` — 工具失败时的注入提示（约 2839 行 same-tool 熔断消息）

#### 改动点
1. 在系统 prompt 工具约束段强化：
   - "工具执行失败时，立即换思路（换工具 / 换参数 / 拆解为更小步骤），**不要反复重试同一命令**；连续失败 2 次必须换方式。"
   - "`go test ./...` 输出 `[no test files]` 是正常信息，不是错误。"
   - "Windows 下优先用 `type` / `findstr`（注意参数），避免 shell 兼容问题。"
2. 保持双语（中文 → 英文），与其他 prompt 段风格一致。

#### 注意点
- **prompt 改动影响所有会话**：要评估对正常任务的影响（不引入过度约束导致 LLM 不敢重试）。
- 需要回归：正常任务中"重试一次合理失败命令"仍被允许（只禁止**反复**重试）。
- 文案要精炼，避免 prompt 膨胀。
- 效果验证依赖 T1/T4 的熔断（prompt 是减少触发，熔断是兜底）。

---

## 4. 依赖关系与实施顺序

```
T1（强制窗口检查，止血）    ← 最高优先，可独立先做
  └─ 复现环境：修复后能干净地暴露 T2
T2（根治超窗机制）          ← 依赖 T1 的复现环境
T3（续跑无进展熔断）        ← 独立，但需要 T1/T2 之后观察真实触发频率
T4（熔断跨 turn）           ← 独立
T5（prompt 强化）           ← 依赖 T4（熔断兜底存在后 prompt 才安全）
```

建议实施顺序：**T1 → T2 → T4 → T3 → T5**（T1 止血，T2 根治，T4 拦住失败循环，T3 防续跑放大，T5 减少触发）。

---

## 5. 验收标准

- [ ] T1：构造超窗请求（含单条超大消息），断言请求**仍发出**且 `estimated_tokens ≤ usableWindow`（经总结替换→头部裁剪→截断超大内容/裁剪工具逐级收敛）；system prompt + 最新用户消息保留；对话不中断、不发"拒绝"错误。
- [ ] T2：`estimated_tokens` 分解日志显示消息 vs 工具占比；修复后任何请求 `estimated_tokens ≤ context_window`。
- [ ] T3：mock LLM 永不更新 todo，断言自动续跑 N 次后停止并提示用户；手动消息重置计数。
- [ ] T4：同命令跨两个 turn 失败，第二个 turn 触发熔断；正常多轮任务不误伤。
- [ ] T5：工具失败后 LLM 换思路（不再反复同一命令）；正常重试一次仍允许。
- [ ] 回归：`go test ./...` 全绿；my-blog 会话复跑不再出现"token 单调暴涨 + 频繁续跑"。

---

## 6. 关联记录

- 2026-08-05 CPU 飙升根因（auto-compact 死循环 + truncateToFit O(n²)）：`memory/project-pchat-cpu-spike.md`（P0 已修复，本计划是其后续）
- 2026-08-05 中断自动继续功能（本计划 T3 的背景来源）：`memory/project-pchat-auto-resume.md`
- 现有规划：`docs/plans/auto-continue-plan.md`（P0-3 auto-continue guard）
