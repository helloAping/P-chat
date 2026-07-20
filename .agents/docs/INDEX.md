# 模块索引

> **用途**：Agent 可根据要修改的功能快速定位对应的模块文档。

## 索引："我想改动 X" → 应读的文档

| 想改动的功能 | 先读 | 然后按需读 |
|---|---|---|
| 修改 Agent 循环逻辑（工具调用、轮次、流控） | [agent.md](agent.md) | llm.md, tool.md |
| 修改 LLM 调用、协议适配（OpenAI/Anthropic） | [llm.md](llm.md) | agent.md |
| 修改 SSE 流式传输、Web API 路由 | [server.md](server.md) | agent.md, frontend.md |
| 修改工具（新增/删除/修改行为） | [tool.md](tool.md) | agent.md, sandbox.md |
| 修改子代理系统 | [subagent.md](subagent.md) | agent.md, tool.md |
| 修改前端界面、Vue 组件、pinia store | [frontend.md](frontend.md) | server.md |
| **修改样式/设计 token/视觉规范** | [**frontend-design.md**](frontend-design.md) | frontend.md |
| 修改工作模式 `work_mode`（coding/daily 侧重点） | [config.md](config.md) | agent.md, server.md, frontend.md, cli.md |
| 修改说话风格 `style` 或风格记忆 | [agent.md](agent.md) | frontend.md, config.md |
| 修改数据库/消息持久化 | [memory.md](memory.md) | config.md |
| **修改数据库 Schema** | [**versioning.md**](versioning.md) | memory.md |
| **发布新版本** | [**versioning.md**](versioning.md) | — |
| 修改配置管理（providers/models） | [config.md](config.md) | llm.md |
| 修改知识库系统（扫描/搜索/三层索引） | [knowledge.md](knowledge.md) | server.md, tool.md, frontend.md |
| 修改版本升级流程 | [upgrade.md](upgrade.md) | knowledge.md |
| 修改 CLI 终端界面 (REPL) | [cli.md](cli.md) | server.md, agent.md |
| 修改沙箱/安全检查 | [infrastructure.md](infrastructure.md) | tool.md |
| 修改 Skill/Agent 定义系统 | [infrastructure.md](infrastructure.md) | subagent.md |
| 修改项目目录管理 | [infrastructure.md](infrastructure.md) | config.md |
| **P0-3 自动续 LLM / todo 守卫** | [agent.md](agent.md) §1 退出条件 | [实现计划](../../docs/plans/auto-continue-plan.md), [用户指南](../../docs/auto-continue.md) |
| **P3-3 端到端 trace id** | [trace 包](../../internal/trace/trace.go) | [server.md](server.md) §8, [P3-3 设计](../../docs/plans/round4-trace-and-extensibility-plan.md) |
| **P3-2 工具 hot-reload** | [tool.md](tool.md) §8 | [P3-2 设计](../../docs/plans/round4-trace-and-extensibility-plan.md) |

## 模块总览

```
P-Chat 项目
├── cmd/                              # 可执行入口
│   ├── pchat/        → 请读 [cli.md](cli.md)
│   ├── pchat-server/ → 请读 [server.md](server.md)
│   ├── pchat-gui/    → 请读 [frontend.md](frontend.md)
│   └── pchat-installer/
│
├── internal/                         # 核心业务逻辑
│   ├── agent/        → 请读 [agent.md](agent.md)
│   ├── llm/          → 请读 [llm.md](llm.md)
│   ├── memory/       → 请读 [memory.md](memory.md)
│   ├── server/       → 请读 [server.md](server.md)
│   ├── tool/         → 请读 [tool.md](tool.md)
│   ├── subagent/     → 请读 [subagent.md](subagent.md)
│   ├── config/       → 请读 [config.md](config.md)
│   ├── cli/          → 请读 [cli.md](cli.md)
│   └── 其他工具模块   → 请读 [infrastructure.md](infrastructure.md)
│       ├── sandbox/  → 命令/文件写入安全检查
│       ├── skill/    → Skill 定义与安装
│       ├── style/    → 人格风格管理
│       ├── mcp/      → MCP 服务器集成
│       ├── project/  → 项目目录注册
│       ├── agents/   → AGENTS.md 加载器
│       ├── rules/    → .rules/ 目录监听器
│       ├── knowledge/→ 知识检索 (RAG)
│       ├── recall/   → 记忆召回增强
│       ├── paths/    → ~/.p-chat 路径解析
│       ├── httpcli/  → CLI SSE 客户端
│       ├── serverproc/ → 服务器进程生命周期
│       ├── trace/    → P3-3 端到端 trace id
│       └── tool/dynamic/ → P3-2 动态工具 hot-reload
│
└── frontend/src/       → 请读 [frontend.md](frontend.md)
    │                       设计 token / 组件样式规则 → [frontend-design.md](frontend-design.md)
    ├── api/client.ts     → HTTP + SSE 客户端
    ├── stores/chat.ts    → Pinia 状态管理
    └── components/       → Vue 组件
```

## 关键数据流

```
用户输入 (Vue/CLI)
  → POST /api/v1/sessions/:id/messages  (server/handler.go:SendMessage)
    → agent.ChatWithTools()              (agent/agent.go)
      → LLM Stream (OpenAI/Anthropic)    (llm/*)
        → ChatStreamChunk channel
          → tool dispatch → subagent     (tool/*, subagent/*)
          → SSE event → 前端渲染         (server/handler.go, frontend/stores/chat.ts)
    → memory.Store 持久化                (memory/*)
```

## 架构图

参见项目根目录 [`AGENTS.md`](../../AGENTS.md) 中的架构图和模块概述。
