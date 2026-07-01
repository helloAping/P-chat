# `.agents/` 目录

本目录是 **canonical agent 规范来源**，供多种 agent 工具（opencode / codex / claude 等）共享。

## 文件结构

```
.agents/
├── AGENTS.md           # ★ 主规范文档（所有工具的权威来源）
├── README.md           # 本文件
├── CLAUDE.md           # → AGENTS.md 的副本（claude 的工具约定文件名）
├── docs/               # ★ 按模块拆分的详细文档
│   ├── INDEX.md        #   总索引："想改 X → 读哪个文档"
│   ├── agent.md        #   Agent ReAct 循环
│   ├── llm.md          #   LLM 客户端 + 协议适配
│   ├── server.md       #   HTTP API + SSE
│   ├── tool.md         #   工具注册 + 内置工具
│   ├── subagent.md     #   子代理系统
│   ├── memory.md       #   SQLite 持久化
│   ├── config.md       #   配置管理
│   ├── cli.md          #   CLI REPL
│   ├── frontend.md     #   Vue 3 前端
│   └── infrastructure.md # 基础设施模块
└── scripts/
    ├── install.ps1     # Windows 安装脚本
    └── install.sh      # Unix 安装脚本
```

## 设计理念

不同 agent 工具的指令文件路径不同：

| 工具 | 默认读 | 约定 |
| --- | --- | --- |
| opencode | `<repo-root>/AGENTS.md` | 同时检查 `.opencode/AGENTS.md` |
| codex | `<repo-root>/.codex/AGENTS.md` | |
| claude | `<repo-root>/CLAUDE.md` 或 `.claude/CLAUDE.md` | |

为解决多工具维护问题，**安装脚本创建目录级符号链接**：

```
.opencode  ──junction──>  .agents/   （所有子文件自动路由）
.codex     ──junction──>  .agents/
.claude    ──junction──>  .agents/
```

之后无论用哪个工具，`<工具目录>/AGENTS.md` 都指向 `.agents/AGENTS.md`，
`<工具目录>/docs/` 都指向 `.agents/docs/`，单一来源、修改即同步。

## 安装

```powershell
# Windows（PowerShell 5.1+）
powershell -NoProfile -ExecutionPolicy Bypass -File .agents\scripts\install.ps1

# Unix
bash .agents/scripts/install.sh
```

执行后自动建立目录级符号链接。若符号链接创建失败（权限不足），回退到文件副本。

## Agent 启动检查

每个 agent 在开始工作前执行：
1. 检查 `.opencode/` 是否存在且为符号链接
2. 不存在 → 运行 install 脚本重建
3. 存在 → 读取 `AGENTS.md` 获取规范

## 修改规范

修改 `.agents/AGENTS.md` 或 `.agents/docs/` 下的文档。所有工具通过目录级符号链接自动读取最新内容。
