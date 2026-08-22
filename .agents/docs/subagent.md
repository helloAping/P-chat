# SubAgent 模块（子代理系统）

> **位置**：`internal/subagent/`  
> **依赖**：agent, llm, memory, tool, config, style  
> **被依赖**：agent（通过 SubagentRegistry 接口）, server

## 概述

子代理系统实现 `task` 工具，让父 LLM 可以派生一个独立子代理，带独立的工具集、模型和系统提示，并行或独立执行子任务。详见根级别文档 [`docs/SUBAGENT.md`](../SUBAGENT.md)。

## 文件结构

| 文件 | 职责 | 关键函数/类型 |
|---|---|---|
| `subagent.go` | Runner 实现 + task 工具入口 + async job 工具 | `Tool()`, `TaskStatusTool()`, `TaskCancelTool()`, `Default.Run()`, `Result` |
| `registry.go` | 子代理目录（合并内置 + 用户自定义） | `Registry`, `AgentInfo`, `NewRegistry()` |
| `builtins.go` | 内置子代理定义（general-purpose/explore/plan） | Built-in prompts |
| `markdown.go` | 用户自定义代理加载（.p-chat/agent/*.md） | `loadFromDir()` |
| `adapter.go` | 适配 agent.SubagentRegistry 接口 | |
| `default_test.go` | 子代理 runner 单元测试 | |

## 核心概念

### 1. 子代理生命周期 (`subagent.go:Default.Run`)

```
1. 缓存检查 (task_id 命中 → 直接返回缓存)
2. 发出 sub_agent_start 事件 (Phase="sub_agent_start", SubAgentStatus="start")
3. 构建子工具注册表 (过滤: 移除 task/task_status/task_cancel/recall, 应用全局 allow/deny, 应用 agent 白名单)
4. 创建独立 agent 实例 (独立 memory.Store, 独立事件系统)
5. 调用 subAgent.ChatWithTools(runCtx, chatReq) → stream channel
6. 转发每个 chunk 到父级 (tryForward → OnEvent → eventCh)
   ★ Done=true chunk 不转发 — 它是本地信号，不应触发父 SSE 关闭
7. 发出关闭事件 (sub_agent_ok 或 sub_agent_err)
8. 缓存结果 (key: description + subagent_type + model)
9. 返回 Result (content + config)
```

### 2. 子代理与父级的通信

子代理的每个流事件通过 `tryForward()` 发送到 `OnEvent` 回调：
- **sub_agent_start** — 前端创建 SubAgentCard，status="start"
- **content/thinking/tool 增量** — 追加到 SubAgentCard 内部 parts
- **★ Done=true** — 本地消费，不转发（防止触发父 SSE 关闭）
- **sub_agent_ok/err** — 关闭 SubAgentCard，status="ok"/"err"

隔离约束：子代理内部工具事件只能更新嵌套 SubAgentCard。即使子代理暴露并调用 `todo_write`，它也只能维护子代理自己的私有 todo，不得驱动父会话 TodoPanel、父会话持久化 todo 或任何主对话控制状态。

### 3. 关闭事件的重要性

`sub_agent_ok/err` 事件是子代理的**唯一外部结束信号**：
- 前端 SubAgentCard status 从 "start" → "ok"/"err"
- partsAcc 更新 Status 和 Elapsed
- 持久化时 status 正确写入

### 4. 工具隔离与父级授权

子代理隔离分成两层：工具可见性只决定模型能看到什么；执行授权在 `agent.ChatWithTools` 的工具派发前再次判断，不能只依赖白名单。

工具可见性：
1. **硬排除**：`task`, `task_status`, `task_cancel`, `recall` 强制移除
2. **全局配置过滤**：`subagent.allowed_tools` / `denied_tools`
3. **Per-agent 白名单**：`agentInfo.Tools` 非空时只暴露列表中的

执行授权：
1. 子代理强制使用隔离的 `permission_level=ask`，不继承父会话的 `auto` / `full` / live session override
2. 子代理不能消费 `/unsafe once`
3. 子代理不能打开自己的 confirm flow；`tool.RequireConfirm` 在子代理上下文中 fail-closed
4. 子代理默认只允许项目内、只读、低风险工具执行
5. 写文件、编辑文件、启动进程、执行 shell、外部访问、交互提问、项目外读取、动态高风险工具调用返回 `SUBAGENT_PARENT_APPROVAL_REQUIRED`，由父对话决定后续动作

内置 `explore` / `plan` 只暴露 `read_file` 和 `list_files`。如果确实需要 shell search、git inspection、测试或其他进程执行，子代理应在最终结果中向父对话提出请求，而不是自行执行。

### 5. 缓存

`agentCache` 按 `(description, subagent_type, model)` 缓存结果。通过 `task_id` 参数可恢复缓存（不重新执行）。

### 6. 异步 job（2026-08-21 起）

`task` 参数支持 `mode: "sync" | "async"`：

- `sync` 是默认行为：父对话等待子代理完成，并把最终结果作为当前 tool result 回填给父 LLM。
- `async` 会立即创建 `subagent_jobs` 记录并返回 `task_id`，后台 goroutine 使用独立 context 执行，不依赖当前 HTTP/SSE turn 生命周期。
- `task_status` 查询当前 session 的 job 列表或单个 `task_id` 详情。
- `task_cancel` 取消当前 server 进程内仍在运行的后台 job，并把 durable 状态标记为 `cancelled`。
- job 完成后会向主会话追加一条 synthetic assistant message，使用与常规子代理相同的 `sub_agent` part 卡片结构；卡片用 `run_mode=async` 标记为后台子代理，并复用后台执行期间保存的内部 parts。stats 只保留在结构化 Result / job 元数据中，不作为聊天可见文本展示。

### 7. 超时与部分结果（2026-08-21 起）

**超时来源**：默认不再给 `task` 工具或子代理安装独立 wall-clock deadline。`task` 工具不再继承普通工具 5 分钟 per-tool timeout；`subagent.timeout` 为空、`"0"` 或非法时表示禁用。只有配置为正数 Go duration（如 `"30m"`）时，才作为显式部署策略生效。

同步 `task` 检测到后，父 agent 会将工具执行、子代理事件转发、工具结果回填后的 continuation LLM 调用切换到 deadline-free abort context。它不再被 `limits.max_turn_seconds` 截断，但仍响应用户取消、`cancel-stream`、客户端断开，以及内部 LLM/tool/failure/round 守卫。

**防卡死依赖的细粒度守卫**：
- LLM stream idle timeout **120s**（上游 120s 无字节 → cancel）
- 工具超时：exec_command 5m、read_file 60s、question 10m
- 累计失败熔断 `CumToolErrMax=8`（打地鼠式不同命令失败）
- 轮次策略继承主对话配置：子代理 `ChatRequest.MaxRounds=0`，由 `limits.max_rounds` / `todo_long_run_mode` 统一解析
- 子代理公共守则与长轮次软提醒：仅引导聚焦、总结进度、停止重复路径，不作为硬停止条件

**为什么取消默认 wall-clock**：正常长任务（explore 读 50 文件 / 慢速本地模型多轮）可能合法运行很久。固定 wall-clock 会把“正常但慢”的子代理误判为失败；真实卡死应由上面的细粒度守卫、用户取消、父 turn 取消或异步 job 取消 hook 处理。

**部分结果策略**：超时/中断时，若子代理已产出部分内容（如 explore 已完成一半调研），不再整段丢弃——`Default.Run` 把部分内容作为 `Result.Content` 返回（带 `Interrupted` 标记），`task` 工具 handler 把它包装成带 "PARTIAL" 前缀的 tool result 传给父 LLM。父 LLM 据此**总结子代理已完成的工作并继续剩余部分**，而不是收到 `(sub-agent returned no content)` 后无从下手。

**判定规则**（`Default.Run` 的 silent-close 检测）：
- 静默关闭 + 有部分输出 → `sub_agent_err`（卡片标"失败（部分内容）"）+ 部分内容返回给父级（`Interrupted` 非空）
- 静默关闭 + 无输出 → 硬失败，父级收到明确超时错误
- Error chunk → 硬失败（子代理自报错误，尾部内容不可信）
- 正常 Done / 软失败 → 不变

## 修改指南

### 要修改子代理创建流程
- `Default.Run()` (subagent.go:511-832)
- 过滤逻辑 (subagent.go:573-611)

### 要修改子代理事件转发
- `tryForward()` (subagent.go:837-842)
- `OnEvent` 回调构造 (subagent.go 的 Tool handler)

### 要修改异步子代理
- `TaskStatusTool()` / `TaskCancelTool()` / `launchAsyncTask()` (subagent.go)
- `SubagentJob` CRUD (memory/subagent_jobs.go)
- `subagent_jobs` schema (memory/migrations.go migration v11)

### 要添加新的内置子代理
1. 在 `builtins.go` 定义 Prompt 常量
2. 在 `NewRegistry()` (registry.go) 中注册

### 要修改缓存机制
- `agentCache` 定义和 `Cache.Get/Set` (subagent.go)

### 要修改子代理超时
- `timeout` / `runCtx` 构造 (subagent.go `Default.Run`)
- config 中的 `subagent.timeout` 字段

### 要修改子代理空转熔断
- `CumToolErrMax` (agent/auto_continue.go) — 累计工具失败熔断阈值（默认 8，跨不同命令）
- `buildSubAgentChatRequest` 保持 `MaxRounds=0` — 子代理继承主对话 / 全局轮次策略
- `subagent_guard.go` — 子代理公共守则与第 50/100/150… 轮软提醒
- `sameToolErrMax` / stuck-loop / no-progress 守卫 (agent/agent.go) — 同工具、同签名失败或只读无进展熔断

## 相关模块

- [agent.md](agent.md) — 父 ReAct 循环、工具派发、forwarder
- [tool.md](tool.md) — 工具注册表机制
- [frontend.md](frontend.md) — SubAgentCard 渲染
- [docs/SUBAGENT.md](../SUBAGENT.md) — 完整架构文档（含 roadmap）
