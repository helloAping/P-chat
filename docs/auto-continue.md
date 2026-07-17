# P0-3 自动续 LLM (auto-continue) 使用指南

> **版本**: 1.0.6 起
> **目标读者**: CLI / Web / 桌面端用户
> **关联**: [实现计划](plans/auto-continue-plan.md)、[agent 模块文档](../.agents/docs/agent.md)

## 这是什么？

`auto-continue` 是 P-Chat 在 1.0.6 引入的**任务连续性守卫**。当 LLM 在 ReAct 循环中"自然结束"（停止调用工具、只发文本），但 **todo 列表里还有未完成项**时，系统会自动注入一条 user 风格的"未完成"提示并继续调用 LLM，最多续 3 次。

### 触发场景

典型场景：用户给了一个多步任务（比如"先读 README，再写一个测试，最后跑一下"），LLM 用 `todo_write` 记下 3 步并开始执行第 1 步。完成后 LLM 经常**忘记更新 todo 列表**就直接发一段"好的，下面我来..."的文本 —— 旧版本就此结束，用户必须手动打字"继续"。

新版本会自动注入：

> ⚠ 系统检测：你刚才的回复没有调用任何工具，但 todo 列表还有未完成项。
> **进行中**:
> - [b] 写一个测试
> **待开始**:
> - [c] 跑一下
> 请继续执行剩余任务：调用所需工具，完成后用 `todo_write` 标记 `done` 或 `cancelled`。

LLM 看到这条提示就会接着做 todo 列表里剩下的事。

### 用户看到的反馈

- **状态栏（stream-status）**：自动续时会出现 `⚠ 检测到 N 项未完成 todo，自动续 LLM (第 N/3 次)`，用 `--accent` 颜色 + 加粗显示（比普通状态行更醒目）
- **轮次计数**：每次续都会增加轮次，所以总耗时 / token 统计会更新
- **最多续 3 次**：如果 LLM 续 3 次仍不收敛（todo 仍没完成或继续发空工具调用），守卫放弃，正常结束

## 怎么用？

### CLI

```bash
/auto-continue       # 显示当前状态（启用 / 关闭）
/auto-continue on    # 启用（默认）
/auto-continue off   # 关闭
/ac                  # 别名
```

启用/关闭是 **per-session** 的，每个会话独立记忆。重启 CLI 不影响其他会话。

### HTTP / Web / 桌面端

调用 `PATCH /api/v1/sessions/:id` 切换：

```bash
curl -X PATCH http://localhost:PORT/api/v1/sessions/SESSION_ID \
  -H "Content-Type: application/json" \
  -d '{"auto_continue": false}'
```

Web 端的 UI 控制项在「会话设置」面板（待实现；当前只能通过 PATCH API 设置）。

## 什么时候该关？

默认是开的（`auto_continue: true`），大部分情况下不要关。以下场景考虑关闭：

| 场景 | 建议 |
| --- | --- |
| LLM 频繁在 todo 没做完时就发总结 | **保持开启** —— 这正是该功能要解决的 |
| LLM 用了 todo 但写得不准（标了 done 但实际没做） | **保持开启** —— 守卫会让 LLM 多看一次 todo，给它改正机会 |
| 你想让 LLM 在某个回合**只发文本总结**（不开新工具） | **关闭** —— 关闭后 LLM 自然结束就立刻 Done |
| 调试 LLM 行为，怀疑 auto-continue 干扰了实验 | **关闭** —— 排除该变量 |
| 你看到对话结尾频繁出现"自动续 (第 3/3 次)" | **可考虑关闭** —— 说明 LLM 学会了依赖该机制而不是主动完成 todo |

**没有"频繁触发就需要关"的情况**。3 次上限本身就是兜底，不会无限续。

## 怎么验证它在工作？

最直观的方式：故意给一个多步任务，观察 todo 列表 + 状态栏。

1. 启用 KB / 项目根感知（让 LLM 更可能用 todo_write）
2. 发一个多步任务，例如："1. 读 main.go; 2. 写一个单元测试; 3. 跑测试"
3. 观察：
   - 状态栏前几行：`[第 1 轮] 调用 LLM` → `加载工具列表...` → `系统提示已就绪` 等
   - 状态栏中间：会陆续出现 todo 列表更新
   - 如果 LLM 一次完成所有 3 步 → 直接 Done（没触发 auto-continue，正常路径）
   - 如果 LLM 完成第 1 步就发文本 → 状态栏出现 `⚠ 检测到 N 项未完成 todo，自动续 LLM (第 1/3 次)` → LLM 接着做第 2、3 步

## 技术细节

### 注入消息的角色

用 `user` 而不是 `system`。原因：`system` 消息经常被 LLM 改写或忽略（"the LLM is told this, but the user is asking..."），`user` 更像"自然的对话驱动"，LLM 会把它当成"用户要求继续"的信号。

### 跟 maxRounds 关系

`maxRounds` 是硬上限（默认 300）；`auto-continue` 是软上限（3 次）。两者独立计数，auto-continue 续的轮次**会**计入 `maxRounds`（因为 `continue` 触发 `round++`）。这是合理的：每次续都是一次真实的 LLM 调用，消耗 token / 时间。

### 跟 stuck-loop / same-tool-err 关系

如果 LLM 卡在同工具+同错误上，`stuck-loop` 守卫（连续 3 轮）和 `same-tool-err-limit` 守卫（连续 4 次）会先于 auto-continue 触发。这两个守卫会注入"改用其他方式"提示，**同时清零 stuck-streak**（P2-1）以免打架。auto-continue 看到 todo 未完成时只在 LLM 没发工具调用时触发，所以两个守卫不会同时跑。

### 配置项

`internal/config/config.go` 没加新字段 —— 全部走 per-session 持久化（`conversations.metadata.auto_continue` 字段）。如果未来需要全局默认（比如某个团队统一关掉），可以加 `LLM.AutoContinueDefault bool`，但目前没必要。

### 数据库 schema

不需要迁移。`auto_continue` 复用了 `conversations.metadata` JSON blob，跟其他 per-session 字段（`style`、`provider`、`model` 等）共存。`auto_continue` 字段是 `*bool`（指针）以便区分"用户没设过"（nil → 默认 true）和"用户明确设为 false"。

## 反馈

如果 auto-continue 触发了你**不期望**的续（LLM 真的该结束但被强续了），请记下：
- 对应的会话 ID
- 续之前 LLM 发的最后一段文本
- 续之前 todo 列表的状态

提交 issue 并附上以上信息。这是优化 MaxStepsPrompt / 退出判断的输入。

如果 auto-continue **没**触发但你觉得它该触发（任务明显没做完 LLM 就停了），同样的三件信息 —— 这说明退出判断有 bug。

## 关联文档

- [实现计划](plans/auto-continue-plan.md) — 任务点列表、改动细节、注意点
- [agent 模块文档](../.agents/docs/agent.md) — 退出条件表 + P0-3 详细说明
- [CLAUDE.md §1.2 数据流图](../.claude/CLAUDE.md) — auto-continue 在数据流中的位置
