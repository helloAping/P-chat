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
| 修改媒体生成（图片/视频/音频、厂商适配、资产） | [config.md](config.md) §7 | [tool.md](tool.md) §6.8, [agent.md](agent.md) §3.6, [实现说明](../../docs/plans/media-generation-implementation.md) |
| 修改知识库系统（扫描/搜索/三层索引） | [knowledge.md](knowledge.md) | server.md, tool.md, frontend.md |
| 修改浏览器控制（扩展/标签页/截图/权限策略） | [server.md](server.md) §10-12 | tool.md, frontend.md, [结构总览](../../docs/project-structure.md) |
| 修改导出、自动更新、诊断接口 | [server.md](server.md) | [结构总览](../../docs/project-structure.md), frontend.md |
| 修改版本升级流程 | [upgrade.md](upgrade.md) | knowledge.md |
| 修改 CLI 终端界面 (REPL) | [cli.md](cli.md) | server.md, agent.md |
| 修改沙箱/安全检查 | [infrastructure.md](infrastructure.md) | tool.md |
| 修改 Skill/Agent 定义系统 | [infrastructure.md](infrastructure.md) | subagent.md |
| 修改项目目录管理 | [infrastructure.md](infrastructure.md) | config.md |
| **修改 IM 桥接（飞书 / TG / 企微 / QQ / 微信 Gateway）** | [**im.md**](im.md) | [server.md](server.md), [config.md](config.md), [agent.md](agent.md), [实现计划](../../docs/plans/im-bridge-plan.md) |
| 回答用户“agent 功能怎么用 / GUI 怎么操作” | [../../README.md](../../README.md)「GUI 操作入口速查 / 常见问题」 | agent.md, tool.md, frontend.md |
| 梳理项目目录、模块职责、运行流 | [../../docs/project-structure.md](../../docs/project-structure.md) | 本索引 + 对应模块文档 |
| 查看当前项目现状与可做事项 | [../../docs/feature-opportunities.md](../../docs/feature-opportunities.md) | — |
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
│   ├── pchat-installer/
│   └── pchat-updater/ → 更新器入口，先读 [server.md](server.md)
│
├── internal/                         # 核心业务逻辑
│   ├── agent/        → 请读 [agent.md](agent.md)
│   ├── llm/          → 请读 [llm.md](llm.md)
│   ├── memory/       → 请读 [memory.md](memory.md)
│   ├── server/       → 请读 [server.md](server.md)
│   ├── tool/         → 请读 [tool.md](tool.md)
│   ├── subagent/     → 请读 [subagent.md](subagent.md)
│   ├── config/       → 请读 [config.md](config.md)
│   ├── generation/   → 媒体生成执行器与资产存储，先读 [实现说明](../../docs/plans/media-generation-implementation.md)
│   ├── cli/          → 请读 [cli.md](cli.md)
│   ├── im/           → 请读 [im.md](im.md)   # IM 桥接 Gateway（飞书 / TG / 企微 / QQ / 微信）
│   ├── browser/      → 浏览器控制，先读 [server.md](server.md) §10-12
│   ├── export/       → 会话导出，先读 [server.md](server.md)
│   ├── update/       → 自动更新，先读 [server.md](server.md)
│   ├── repair/       → 修复命令，先读对应 cmd 入口和源码
│   ├── version/      → VERSION/ldflags 版本字符串，先读 [versioning.md](versioning.md)
│   ├── stylegen/     → 风格生成，先读 [frontend.md](frontend.md) 和 stylegen plan
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
├── browser-extension/                # Chrome/Edge 扩展源码，配合 internal/browser
├── docs/project-structure.md          # 项目结构、模块职责、运行流总览
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
