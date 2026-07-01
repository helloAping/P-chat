# Agent 模块

> **位置**：`internal/agent/`  
> **依赖**：llm, memory, tool, config, subagent（接口）, style, skill, rules, agents  
> **被依赖**：server, subagent, cli

## 概述

Agent 模块是 P-Chat 的核心业务逻辑层，实现 **ReAct 式工具调用循环**。当一个用户消息到达时，Agent 调用 LLM 获取回复 → 解析工具调用 → 执行工具 → 将结果反馈给 LLM → 继续循环，直到 LLM 决定结束或达到轮次上限。

## 文件结构

| 文件 | 职责 | 关键函数/类型 |
|---|---|---|
| `agent.go` | ReAct 循环、LLM 调用编排、工具派发、事件流控制 | `ChatWithTools()`, `Agent` |
| `parts.go` | 助手消息的结构化 parts 累加器（thinking/tool/sub_agent） | `partsAccumulator`, `snapshotStructural()` |
| `attachment.go` | 用户附件（图片/文件）扩展为 ChatMessage | `AttachmentResolver` |

## 核心概念

### 1. ReAct 循环 (`agent.go:ChatWithTools`)

```
for round := 1; maxRounds==0 || round<=maxRounds; round++ {
    1. 构建系统提示词 (style + AGENTS + rules + skills)
    2. 规范化消息 (normalizeToolResults — DeepSeek 兼容)
    3. 调用 LLM Stream → 获取内容/思考/工具调用
    4. 解析工具调用 (原生 tool_calls 或 markdown ```tool_call 块)
    5. 清理 markdown tool_call 块中的文本内容
    6. 若无工具调用 → 完成，退出
    7. 并行执行工具 (goroutine + eventCh 64)
    8. 将工具结果追加到消息列表
    9. DeepSeek 兼容：工具结果角色 → User
    10. persistAssistant() — 持久化带 parts 的助手消息
    11. 下一个循环轮次
}
```

### 退出条件

| 条件 | 阶段 | 行为 |
|---|---|---|
| `len(toolCalls) == 0` | `done` | LLM 自然完成 |
| `meaningful > 80` | `context_warn` | 仅警告 |
| `meaningful > 120` | `context_warn` | 自动停止，建议 /compress |
| `ctx.Err() != nil` | (错误路径) | 用户取消 |
| `maxRounds` 达到 | `limit` | 强制停止 |
| `stuckStreak >= 3` | `stuck` | 连续 3 轮相同失败工具调用 |

### 2. 工具执行的并行派发 (`agent.go:1185-1471`)

每个工具调用在独立 goroutine 中执行，通过 **per-tool eventCh** (cap 64) 与父级通信：

```
工具 goroutine:
  → handler(toolCtx, argsRaw)
  → 通过 eventCh 发送子事件 (content/thinking/tool/sub_agent)
  → defer close(eventCh)

forwarder goroutine:
  → 读取 eventCh → partsAcc.update(ev) → ch (主通道) ← ev
  → defer close(fwd.done)

父循环:
  → wg.Wait() 等所有工具 goroutine 结束
  → <-f.done  等所有 forwarder 排空 (drain)
  → 写入工具完成事件到主通道
  → persistAssistant()
  → 下一轮
```

### 部分状态累加器 (partsAccumulator)

`partsAccumulator` (`parts.go`) 维护当前轮次助手消息的结构化 parts 列表，支持：

- **text** — 文本增量追加
- **thinking** — 思考增量追加（带 streaming flag）
- **tool** — 工具调用卡片（start/ok/err 状态）
- **sub_agent** — 嵌套子代理卡片（start/ok/err 状态，含内嵌 parts）
- **Done** — 清除所有 thinking streaming flag

`persistAssistant()` 调用 `snapshotStructural()` 将 part 持久化到 SQLite（工具和子代理 part → meta["parts"] JSON）。

### 4. 计划模式 (Plan Mode)

当 `ChatRequest.PlanMode == true`：
- 仅暴露 `todo_write` 和 `question` 工具（执行类工具禁用）
- `maxRounds = 1`（单轮）
- LLM 生成分步计划 + 待办 + 可选澄清问题

### 5. DeepSeek 兼容性

`normalizeToolResults()` (`agent.go:1531`) 将 ToolCall 类型消息移除，ToolResult 角色改为 User。这是 DeepSeek 模型接受工具结果的必要条件。

### 6. 卡死循环保护

`toolCallSignature()` 计算每轮工具调用的稳定签名。若连续 3 轮相同的工具调用都失败 → 判定卡死 → 自动停止。

## 修改指南

### 要改 LLM 调用流程
- 修改 `ChatWithTools()` 中的 LLM Stream 调用部分 (agent.go 约 1000-1130 行)

### 要改工具派发流程
- 工具 goroutine 定义在 agent.go:1259-1366
- eventCh 容量和 forwarder 逻辑在 agent.go:1189-1257
- 工具结果处理在 agent.go:1376-1471

### 要改 parts 结构
- 结构化 parts 类型定义在 parts.go
- `snapshotStructural()` 决定哪些 part 被持久化
- 前端对应的 MessagePart 类型在 `frontend/src/api/client.ts`

### 要改系统提示词构建
- `buildStaticSystemPrompt()` (agent.go)
- `buildToolHint()` 生成 markdown 回退格式的工具说明

## 相关模块

- [llm.md](llm.md) — LLM 客户端、协议适配器
- [tool.md](tool.md) — 工具注册与实现
- [subagent.md](subagent.md) — 子代理系统（task 工具）
- [memory.md](memory.md) — 消息持久化
- [server.md](server.md) — HTTP API + SSE
