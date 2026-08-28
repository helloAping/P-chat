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

### P6：子代理工具执行压缩与重复控制

实际测试补充了第三类问题：子代理在一个工具组内连续发起大量语义相近的 `exec_command`，例如多次 `Get-Content -Encoding UTF8 internal\...`、多次 `Select-String -Path internal\...`、多次读取/搜索 `web\admin\...`。这些调用有成功也有失败，不是单纯失败重试，也不是前端重复渲染。

当前防线的缺口：

- `toolCallSignature()` 按完整 `ArgsJSON` 精确匹配；路径、行号、搜索词、输出过滤稍变就不算重复。
- `normalizeToolFailureSig()` 只进入失败分支；成功工具不会参与失败归一化。
- 同一轮内存在多条工具调用时，`sameToolErrCount` 不累计。
- `noProgressGuard` 按“轮”观察重复读取，不能压缩同一轮内一次性发出的二十多条相近命令。
- 混合成功/失败会被视为仍有进展，cross-turn 失败 streak 也可能被成功工具 reset。

目标不是一刀切禁止工具，而是把“多次低信息量工具调用”压缩成“少数高信息量工具调用”。子代理仍然可以充分使用工具，但需要优先使用覆盖面更大的查询，避免反复用窄查询撞同一目标。

建议执行语义：

1. **先压缩，不先熔断**：检测到同一轮内多个相近只读命令时，优先合并/覆盖，而不是直接停止子代理。
2. **按意图归一化**：给 `exec_command` 增加 operation fingerprint，例如 `read-file:<path>`、`search-file:<path>`、`search-tree:<root>`、`verify:go-test:<scope>`。归一化时剥离 PowerShell 包装、`2>&1`、`Select-Object -Last N`、`Write-Host exit`、纯展示管道等噪声。
3. **同轮压缩**：在工具派发前扫描本轮 tool calls。对相同 fingerprint 的只读命令，执行覆盖范围最大/信息量最高的代表调用；其余 tool_call 返回合成 tool_result，说明“已由同组代表调用覆盖”，以保持 LLM 工具协议配对完整。
4. **成功结果缓存**：同一子代理内，若已成功获得某 fingerprint 的结果，后续相近调用优先返回缓存摘要或提示使用已有结果，而不是再次执行。
5. **失败结果不盲目重试**：同 fingerprint 失败后，允许一次不同策略重试；继续失败则引导切换工具或基于已有证据总结。
6. **分级引导**：第一次发现低效重复时注入软提示，要求扩大查询或汇总；第二次仍重复时压缩执行；第三次仍重复时才强制进入总结轮。
7. **只读范围优先**：P6 首期只处理子代理里的只读工具和只读 `exec_command`，不碰写入、编辑、删除、构建产物生成等有副作用命令。

实施拆分：

- P6a：新增 `operationFingerprint` 纯函数与测试，覆盖 `Get-Content`、`Select-String`、`go test`、输出过滤变体。
- P6b：新增“同轮工具压缩器”，只对只读 `exec_command` 生效；被压缩的 tool_call 返回合成结果，确保每个 tool_call id 都有 tool_result。
- P6c：新增子代理内 fingerprint 成功/失败缓存，避免跨轮重复执行同一意图。
- P6d：扩展子代理 prompt，明确“优先用少量高覆盖工具完成调查；不要用多个窄查询反复读取同一目标；工具输出被截断时先改用更聚焦的单次查询，而不是连续发起多条近似查询”。
- P6e：前端工具组展示增加“已压缩/已覆盖”状态，便于用户区分真实执行与协议占位结果。

## 当前执行顺序

已完成 P0、P1、P2、P3a、P4 后端最小闭环。下一步进入 P5：前端/CLI 展示后台 job 状态、刷新/取消入口，以及完成通知体验。P6 作为新增质量改进线并行推进，优先级应高于纯展示优化，因为它直接影响子代理执行成本、上下文膨胀和长任务稳定性。
