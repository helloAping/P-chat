# SubAgent 模块（子代理系统）

> **位置**：`internal/subagent/`  
> **依赖**：agent, llm, memory, tool, config, style  
> **被依赖**：agent（通过 SubagentRegistry 接口）, server

## 概述

子代理系统实现 `task` 工具，让父 LLM 可以派生一个独立子代理，带独立的工具集、模型和系统提示，并行或独立执行子任务。详见根级别文档 [`docs/SUBAGENT.md`](../SUBAGENT.md)。

## 文件结构

| 文件 | 职责 | 关键函数/类型 |
|---|---|---|
| `subagent.go` | Runner 实现 + task 工具入口 | `Tool()`, `Default.Run()`, `Result` |
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
3. 构建子工具注册表 (过滤: 移除 task/recall, 应用全局 allow/deny, 应用 agent 白名单)
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

### 3. 关闭事件的重要性

`sub_agent_ok/err` 事件是子代理的**唯一外部结束信号**：
- 前端 SubAgentCard status 从 "start" → "ok"/"err"
- partsAcc 更新 Status 和 Elapsed
- 持久化时 status 正确写入

### 4. 三层工具隔离

1. **硬排除**：task, recall 强制移除
2. **全局配置过滤**：`subagent.allowed_tools` / `denied_tools`
3. **Per-agent 白名单**：`agentInfo.Tools` 非空时只暴露列表中的

### 5. 缓存

`agentCache` 按 `(description, subagent_type, model)` 缓存结果。通过 `task_id` 参数可恢复缓存（不重新执行）。

## 修改指南

### 要修改子代理创建流程
- `Default.Run()` (subagent.go:511-832)
- 过滤逻辑 (subagent.go:573-611)

### 要修改子代理事件转发
- `tryForward()` (subagent.go:837-842)
- `OnEvent` 回调构造 (subagent.go 的 Tool handler)

### 要添加新的内置子代理
1. 在 `builtins.go` 定义 Prompt 常量
2. 在 `NewRegistry()` (registry.go) 中注册

### 要修改缓存机制
- `agentCache` 定义和 `Cache.Get/Set` (subagent.go)

### 要修改子代理超时
- `timeout` 变量 (subagent.go:566-568)
- config 中的 `subagent.timeout` 字段

## 相关模块

- [agent.md](agent.md) — 父 ReAct 循环、工具派发、forwarder
- [tool.md](tool.md) — 工具注册表机制
- [frontend.md](frontend.md) — SubAgentCard 渲染
- [docs/SUBAGENT.md](../SUBAGENT.md) — 完整架构文档（含 roadmap）
