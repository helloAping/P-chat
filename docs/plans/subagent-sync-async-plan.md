# Subagent 同步/异步执行重构计划

## 背景

实际使用中，长时间运行的 subagent 容易失败。当前 `task` 工具虽然在父 agent 的工具派发层以 goroutine 并行执行，但语义仍是同步：父对话必须等所有 `task` 工具返回结果，才能把工具结果回填给父 LLM 并继续下一轮推理。

现状里存在两层 wall-clock 截断：

1. `task` 工具的 per-tool timeout：旧行为继承普通工具默认 5 分钟。
2. `subagent.timeout`：子代理专属 wall-clock cap。旧默认是 30 分钟。
3. `limits.max_turn_seconds`：整个主对话 turn 的 wall-clock cap。默认 900 秒。

因此，仅调大或取消 `subagent.timeout` 并不能完整解决问题。只要 `task` 仍同步挂在父 turn 上，父 turn 超时仍会取消 subagent。

## 当前实现结论

- `task` 工具描述明确为同步执行，返回子代理最终文本结果。
- 父 agent 支持同一轮多个工具并行执行，包括多个 `task` 并行执行。
- 父 agent 会等待所有工具 goroutine 完成，并 drain per-tool eventCh 后，才进入下一轮 LLM。
- 子代理内部是独立 `agent.Agent`，使用独立 in-memory store、过滤后的工具集、独立 `ChatWithTools`。
- 子代理进度通过 `OnEvent -> eventCh -> parent forwarder -> SSE` 流式转发给 UI。
- 子代理结果只存在于内存 cache；没有持久化 job/session 状态，不能跨请求可靠查询或恢复。

## 目标语义

### 同步 subagent

同步模式用于父 LLM 必须立即读取子代理结果后继续推理的场景。

要求：

- 父对话等待同步 subagent 完成。
- 子代理不再有独立 wall-clock timeout。
- 取消由明确 hook 触发：用户取消、父 turn 取消、LLM stream stall、工具 timeout、轮次上限、失败熔断。
- 子代理若被取消且已有部分输出，继续返回 PARTIAL 结果给父 LLM。

### 异步 subagent

异步模式用于长时间探索、审计、后台调研。

要求：

- 父对话发起后立即继续执行。
- `task` 立即返回 `async_launched` 结果，满足工具调用配对。
- 后台 job 独立运行，不依赖当前 HTTP/SSE turn 生命周期。
- 子代理完成后 hook 结果回主对话，或由父对话主动调用状态工具查看。
- 用户可查询进度、取消任务、查看最终结果。

## 任务拆分

### P0：文档与现状校准

- 新增本计划文档。
- 修正根 `docs/SUBAGENT.md` 中过期描述。
- 明确当前“并行工具执行”不等于“异步 subagent”。

### P1：同步 subagent 取消独立 wall-clock timeout

- 修改 `SubAgentConfig.TimeoutDuration()`：空值、`0`、非法值都返回 `0`，表示不设置子代理专属 deadline。
- 修改 `Default.Run()`：仅当 timeout > 0 时创建 `context.WithTimeout`，否则继承父 ctx 并只安装 no-op cancel。
- 修改 `task` 工具默认策略：不再继承普通工具 5 分钟 per-tool timeout；仍继承父 ctx 以响应用户取消和父 turn 取消。
- 修改父 agent 工具派发：当工具策略 timeout 为 0 时使用 `context.WithCancel`，不安装 deadline。
- 更新 `configs/config.yaml`：默认不再写 `subagent.timeout: "30m"`。
- 更新 `.agents/docs/subagent.md` 与 `docs/modules/subagent.md`。
- 更新测试：覆盖空值/0/非法值禁用，正数值仍启用。

### P2：同步模式下的父 turn 超时策略

问题：即使 P1 完成，父 `limits.max_turn_seconds` 仍会取消同步 subagent。

已落地策略：

- 当本轮工具调用包含同步 `task` 时，父 agent 将后续工具执行、subagent 事件转发、工具结果回填后的 continuation LLM 调用切换到 deadline-free abort context。
- 这个 abort context 仍会被用户取消、`cancel-stream`、客户端断开取消。
- 普通工具仍保留自己的 per-tool timeout；普通非 task turn 仍受 `limits.max_turn_seconds` 保护。

剩余限制：

- 同步 `task` 一旦触发，当前 turn 的后续 continuation 也脱离 `max_turn_seconds`，依靠 LLM stall、工具 timeout、失败熔断、轮次上限和用户取消收敛。
- 真正后台执行、跨请求查询和完成后 hook 回主对话仍需要 P3 的 job 状态层。

### P3：异步 subagent job 状态层

P3a 已落地：新增持久化状态层和 Store CRUD。

新增持久化状态：

- `subagent_jobs`
- 字段：`id/task_id/session_id/status/subagent_type/model/description/result/error/progress_json/created_at/started_at/finished_at/cancelled_at`

新增 Store API：

- `CreateSubagentJob`
- `GetSubagentJob` / `GetSubagentJobByID`
- `ListSubagentJobs`
- `MarkSubagentJobRunning`
- `UpdateSubagentJobProgress`
- `CompleteSubagentJob` / `FailSubagentJob` / `CancelSubagentJob`

已接入服务端能力：

- 创建后台 job。
- 查询 job 列表和详情。
- 取消 job。
- job 完成后写入主会话 synthetic message。

schema 已走 `internal/memory/migrations.go` migration v11，并覆盖升级 / 回滚 / 幂等 / CRUD 测试。

### P4：工具协议扩展

扩展 `task` 参数：

```json
{
  "mode": "sync | async",
  "description": "...",
  "subagent_type": "explore",
  "task_id": "..."
}
```

同步返回现有最终结果。

异步立即返回：

```text
sub-agent launched in background: task_id=<id>, status=running
Use task_status with the same task_id to check progress.
```

新增工具已落地：

- `task_status`
- `task_cancel`

剩余限制：

- `async` 完成后目前通过 synthetic assistant message 回写主会话；没有独立 SSE 推送给已结束的前端流。
- 进程重启后可以查询历史 job，但只能取消当前 server 进程内仍在运行的 job。
- 前端尚未提供后台 job 专用卡片、刷新按钮或取消按钮。

### P5：前端/CLI 体验

- SubAgentCard 增加后台运行状态。
- 展示 task_id、状态、最近进度、完成结果。
- 支持点击取消或刷新。
- CLI 显示后台 job launched/completed/cancelled。

## 当前执行顺序

已完成 P0、P1、P2、P3a、P4 后端最小闭环。下一步进入 P5：前端/CLI 展示后台 job 状态、刷新/取消入口，以及完成通知体验。
