# P-Chat

**对话式 AI Agent · 三种人格 · 四端同源（CLI / HTTP / 桌面端 / 移动端）**

P-Chat 是一个**本地优先的 AI 编程助手**。核心特色：完全本地化运行（无云端依赖）+ 结构化消息流（text + thinking + tool calls + sub-agents）+ 多 LLM 协议兼容（OpenAI / Anthropic）。

---

## 功能特性

| 特性 | 说明 |
|------|------|
| **四端同源** | CLI、HTTP Server、桌面端 (Wails v2)、Web 浏览器共享同一套后端和前端代码 |
| **三种人格** | 可爱风 (PiPi)、古风 (MoYan)、科技风 (NEXUS)，支持自定义扩展 |
| **多 LLM 协议** | OpenAI 兼容协议 + Anthropic 原生协议，支持 OpenAI / DeepSeek / Claude / Ollama / 通义千问 / 智谱等 |
| **会话记忆** | SQLite 持久化对话历史，支持上下文压缩、归档/恢复、消息回滚 |
| **任务连续性** | 1.0.6 起：todo 未完成时 LLM 不会半路停下，自动续接（最多 3 次，per-session 可关）— 详见 [`docs/auto-continue.md`](docs/auto-continue.md) |
| **断线恢复** | 1.0.7 起：SSE 流意外断开时自动从服务端拉取已入库的 assistant parts 补齐 trailing bubble，3s 弹"已恢复"banner |
| **工具结果折叠** | 1.0.7 起：长 result 默认折叠提升可读性；状态 localStorage 持久化；header 📋 复制按钮在折叠态下可点 |
| **重新生成回复** | 1.0.7 起：assistant 消息底部"重答"按钮，物理截断 user 消息之后的行后重跑 agent loop |
| **知识库** | 本地/远程向量库，语义检索，53 种文件格式索引，对话级绑定 |
| **工具系统** | 内置 9 个工具（Shell 执行、文件读写、Web 获取、PDF/DOCX 解析、子代理等） |
| **知识检索** | `recall` 工具按需查询知识库，LLM 自主决定何时需要查文档/代码 |
| **子代理系统** | `task` 工具启动独立子代理，并行执行复杂子任务 (explore / general / plan) |
| **安全沙箱** | 命令/文件访问控制，三级审批 (ask / auto / full)，支持对话级权限 |
| **项目系统** | 多项目目录注册，对话按项目隔离，项目级 AGENTS.md + rules + skills |
| **Plan/Build 模式** | 计划和构建双模式，计划模式下 LLM 先出方案再执行 |
| **思考过程展示** | 可折叠的 reasoning/thinking 面板，DeepSeek / Claude 推理过程可视化 |
| **MCP 协议** | Model Context Protocol 服务器集成（配置中） |
| **技能系统** | 可安装的 SKILL.md 技能包，全局 + 项目双层级加载 |
| **规则系统** | Markdown 规则文件，注入 System Prompt 控制 LLM 行为 |
| **应用打包** | 一键打包 Windows 安装包（开始菜单 + 卸载项）+ Linux 安装脚本 |

---

## 快速开始

### 前置准备

1. 下载或编译对应二进制
2. 配置 API Key（参考下方 [LLM 配置](#llm-协议支持)）

### 方式一：桌面端 (推荐新用户)

```powershell
# Windows — 编译并安装
task package:gui; task install:gui
# 从开始菜单启动「P-Chat」

# Linux
task package:gui:linux
cd build/bin && ./install.sh
```

桌面端启动时自动拉起后台 server，关窗自动杀子进程，无需手动管理。

### 方式二：浏览器访问

```bash
pchat-server.exe
# 打开浏览器访问 http://127.0.0.1:8960/app/index.html
```

### 方式三：CLI 终端

```bash
pchat.exe                  # 默认科技风
pchat.exe --style cute     # 可爱风
pchat.exe --style guofeng  # 古风
pchat.exe --provider ollama # 指定提供器
```

---

## GUI 使用指南

### 整体布局

```
┌─────────┬──────────────────────────────────────────┐
│ 侧边栏   │                                          │
│          │          消息列表 (MessageBubble)         │
│ ┌──────┐ │                                          │
│ │ 项目1 │ │  LLM: 你好，有什么可以帮你？             │
│ │ 项目2 │ │  ┌─ read_file ✓ 完成 0.3s ─┐           │
│ │       │ │  │  参数: {"path":"..."}   │           │
│ ├──────┤ │  │  结果: package main...   │           │
│ │ 会话1 │ │  └─────────────────────────┘           │
│ │ 会话2 │ │  已读取文件内容。                       │
│ │ 会话3 │ │                                          │
│ │ + 新  │ │                                          │
│ └──────┘ │                                          │
│          ├──────────────────────────────────────────┤
│          │ [模型] [推理] [计划/构建] [权限] [向量库] [🔊] │
│          │ [___________________________________]  ➤ │
│          │ Enter 发送  Shift+Enter 换行  Esc 停止     │
└─────────┴──────────────────────────────────────────┘
```

### 项目与会话

- **侧边栏项目**：点击「添加项目」注册本地目录，对话自动绑定项目上下文
- **侧边栏会话**：每个项目下有独立的会话列表，支持重命名、归档、恢复
- **搜索 (Ctrl+P)**：跨会话全文搜索历史消息
- **消息回滚**：点击某条 AI 消息可撤回之后的所有消息并恢复对话

### 输入区域

| 控件 | 说明 |
|------|------|
| 模型选择器 | 按提供商分组列出所有模型，⭐ 标记默认模型 |
| 推理等级 | off / low / medium / high / max，控制 LLM 思考深度 |
| 计划/构建 | 切换 Plan/Build 模式。计划模式让 LLM 先出方案不执行 |
| 权限级别 | 🔒 始终询问 / 🔓 替我审批 / 🔑 完全访问，控制沙箱行为 |
| 向量库 | 选择当前对话使用的知识库向量库（不使用 / 默认 / 指定） |
| 附件 | 支持拖拽上传图片、音频、PDF、文本文件等 |

### 应用设置 (⚙)

点击右上角齿轮图标打开设置面板，包含以下 Tab：

| Tab | 功能 |
|-----|------|
| LLM 提供商 | 添加/编辑/删除 LLM 提供器，管理多模型，设置默认模型，标记模型能力 (vision/thinking) |
| MCP 服务器 | 配置 Model Context Protocol 服务器 |
| 技能 | 安装/卸载/搜索技能包 |
| 归档 | 查看和恢复已归档会话，永久删除 |
| 知识库 | 配置知识库系统（详见下方 [知识库](#知识库-knowledge-base)） |

### 斜杠命令

输入框以 `/` 开头可触发命令：

| 命令 | 说明 |
|------|------|
| `/help` | 显示帮助 |
| `/skills` | 列出技能 |
| `/rules` | 列出规则 |
| `/agents` | 查看 AGENTS.md |
| `/tools` | 列出工具 |
| `/recall <query>` | 手动知识检索 |
| `/clear` | 清屏 |

---

## 配置与定制

### 配置优先级

后加载覆盖先加载：

1. 代码内置默认值
2. `~/.p-chat/config.json`（全局；旧版 `config.yaml` 自动迁移）
3. `.p-chat/config.json`（项目级）
4. `--config` 参数指定（最高）

### config.json 结构

```json
{
  "llm": { "default": "openai", "providers": [...] },
  "server": { "host": "127.0.0.1", "port": 8960 },
  "style": { "default": "tech" },
  "memory": { "max_history": -1 },
  "sandbox": { "exec_dangerous_patterns": "...", "write_protected_paths": "..." },
  "knowledge": { "enabled": false, "embedder": {...}, "vector_stores": [...], "bases": [...] }
}
```

### AGENTS.md

Agent 行为指令文件，支持两级注入 System Prompt：

```bash
~/.p-chat/AGENTS.md    # 全局，对所有项目生效
./AGENTS.md             # 项目级，仅对当前项目生效
```

### Skills 技能系统

每个技能是一个目录，包含 `SKILL.md` 和可选的资源文件：

```
~/.p-chat/skills/code-review/
├── SKILL.md
└── checklist.md
```

在 GUI 的技能 Tab 中可安装/搜索/卸载技能包。

### Rules 规则系统

Markdown 规则文件，存放在 `rules/` 目录，全部拼接注入 System Prompt：

```
~/.p-chat/rules/
├── code-style.md
└── security.md
```

### 风格人格

| 风格 | 名称 | 特性 | CLI 参数 |
|------|------|------|---------|
| 可爱风 | 小P (PiPi) | 软萌、颜文字、治愈 | `--style cute` |
| 古风 | 墨言 (MoYan) | 雅致、引经据典、谦辞 | `--style guofeng` |
| 科技风 | NEXUS (零号) | 冷静、结构化、高效 | `--style tech` |

---

## 知识库 (Knowledge Base)

本地优先的文档语义检索系统。通过向量嵌入将代码/文档/配置文件转化为可检索的知识片段。

> 完整使用指南：[docs/knowledge.md](docs/knowledge.md)

### 配置流程

1. 应用设置 → 知识库 Tab → 打开「启用知识库」
2. 配置嵌入模型（选择提供 embedding 模型的供应商）
3. 添加向量库（本地 `local` 开箱即用，也支持 Qdrant/Chroma/Pinecone 等远程库）
4. 添加知识库目录 → 点「扫描」后台索引
5. 在对话输入区底部选择要用的向量库

### LLM 如何使用

LLM 通过 `recall` 工具按需检索。当 LLM 不确定某条信息、需要查代码/文档、或想引用历史时，自动调用 recall。你也可以直接在消息中说"查一下知识库中关于 XXX 的内容"引导 LLM 检索。

### 支持格式

文档 (.md .txt .rst)、代码 (.go .ts .py .java .rs 等 40+ 种)、配置 (.json .yaml .toml .xml)、Web (.html .css .scss) 等共 53 种。详见 [docs/knowledge.md](docs/knowledge.md#支持的文件格式53-种)。

---

## CLI 命令

| 命令 | 说明 |
|------|------|
| `/help` | 显示帮助信息 |
| `/style [name]` | 切换/查看风格 |
| `/model [provider]` | 切换/查看模型 |
| `/recall <query>` | 手动知识检索 |
| `/clear` | 清屏 |
| `/skills` | 列出已安装技能 |
| `/rules` | 列出已加载规则 |
| `/agents` | 查看 AGENTS.md |
| `/tools` | 列出可用工具 |
| `/projects` | 列出项目目录 |
| `/init` | 初始化当前目录的 .p-chat/ |
| `/quit` | 退出 |

---

## LLM 协议支持

| 协议 | 适用 |
|------|------|
| `openai` (OpenAI 兼容) | OpenAI、DeepSeek、Ollama、通义千问、智谱、百川 等 |
| `anthropic` (原生) | Claude (Anthropic) |

### 配置示例

```yaml
llm:
  default: "openai"
  providers:
    - name: "openai"
      protocol: "openai"
      base_url: "https://api.openai.com/v1"
      api_key: "sk-xxx"
      model: "gpt-4o"

    - name: "claude"
      protocol: "anthropic"
      base_url: "https://api.anthropic.com"
      api_key: "sk-ant-xxx"
      model: "claude-3-5-sonnet-20241022"

    - name: "deepseek"
      protocol: "openai"
      base_url: "https://api.deepseek.com/v1"
      api_key: "sk-xxx"
      model: "deepseek-chat"

    - name: "ollama"
      protocol: "openai"
      base_url: "http://localhost:11434/v1"
      api_key: "ollama"
      model: "llama3"
```

---

## HTTP API

所有接口在 `/api/v1/` 下。

### 会话
| 方法 | 路径 | 说明 |
|------|------|------|
| GET/POST | `/sessions` | 列出/创建会话 |
| GET/PATCH/DELETE | `/sessions/:id` | 获取/更新/归档 |
| GET | `/sessions/:id/messages` | 历史消息 |
| POST | `/sessions/:id/messages` | **发送消息 (SSE 流式)** |
| POST | `/sessions/:id/compress` | 压缩历史 |
| PATCH | `/sessions/:id/reasoning-effort` | 调整推理深度 |
| DELETE | `/sessions/:id/messages` | 清空会话 |
| POST | `/sessions/:id/rollback` | 回滚消息 |

### 知识库
| 方法 | 路径 | 说明 |
|------|------|------|
| GET/PATCH | `/knowledge/config` | 配置读写 |
| GET/POST | `/knowledge/stores` | 向量库管理 |
| DELETE | `/knowledge/stores/:name` | 删除向量库 |
| POST | `/knowledge/stores/:name/test` | 测试连接 |
| GET/POST | `/knowledge/bases` | 知识库管理 |
| DELETE | `/knowledge/bases/:name` | 删除知识库 |
| POST | `/knowledge/bases/:name/scan` | 扫描索引（异步） |
| GET | `/knowledge/bases/:name/scan/status` | 查询扫描进度 |
| POST | `/knowledge/search` | 语义搜索 |
| GET | `/knowledge/embedders` | 可用嵌入模型 |

### 其他
| 方法 | 路径 | 说明 |
|------|------|------|
| GET/POST/PATCH/DELETE | `/styles` | 风格 CRUD |
| GET/POST/PATCH/DELETE | `/providers` | 提供器管理 |
| GET/POST/DELETE | `/projects` | 项目目录管理 |
| GET/POST/DELETE | `/skills` | 技能管理 |
| GET | `/commands` | 斜杠命令列表 |
| POST | `/uploads` | 文件上传 |
| POST | `/mcp/servers` | MCP 服务器管理 |

### SSE 消息发送

```bash
POST /api/v1/sessions/:id/messages
Content-Type: application/json

{"message": "你好", "provider": "openai", "model": "gpt-4o"}
```

响应 (SSE 事件流)：

```
data: {"type":"content","content":"你好"}
data: {"type":"thinking","thinking":"我需要分析用户的问题..."}
data: {"type":"tool","tool_name":"read_file","tool_status":"start"}
data: {"type":"tool","tool_name":"read_file","tool_status":"ok","tool_result":"..."}
data: {"type":"phase","step":"sub_agent","message":"正在启动子代理..."}
data: {"type":"done","tokens_in":123,"tokens_out":456,"elapsed":"2.3s"}
```

SSE 事件类型：`content` | `thinking` | `tool` | `phase` | `error` | `done` | `question` | `tool_confirm`

---

## 项目结构

```
P-chat/
├── cmd/pchat/          # CLI 入口
├── cmd/pchat-server/   # HTTP Server
├── cmd/pchat-gui/      # Wails 桌面端
├── frontend/src/       # Vue 3 SPA
│   ├── api/client.ts        # HTTP/SSE 客户端
│   ├── stores/chat.ts       # Pinia 状态管理
│   └── components/           # Vue 组件
├── internal/
│   ├── agent/          # ReAct 主循环
│   ├── knowledge/      # 向量库、嵌入、索引
│   ├── recall/         # 知识召回引擎
│   ├── llm/            # LLM 客户端 (SSE parser)
│   ├── memory/         # SQLite 持久化
│   ├── config/         # 配置管理
│   ├── server/         # HTTP 路由 + SSE
│   ├── tool/           # 工具注册 + 内置工具
│   ├── subagent/       # 子代理系统
│   ├── skill/          # 技能加载
│   ├── rules/          # 规则加载
│   ├── style/          # 人格风格
│   ├── sandbox/        # 命令/文件安全
│   ├── mcp/            # MCP 协议
│   └── project/        # 项目管理
├── docs/knowledge.md   # 知识库详细文档
├── prompts/            # 风格提示词
├── configs/            # 配置模板
└── scripts/            # 打包脚本
```

---

## 开发

### 编译

```bash
task build:all       # CLI + Server + 前端
task build:gui       # 桌面端 (Wails v2)
task package:gui     # 完整打包
```

### 测试

```bash
go test -count=1 ./...   # Go 测试
cd frontend && npx vue-tsc -b  # TS 类型检查
```

### 调试日志

`~/.p-chat/server-debug.log` 记录了所有 LLM 请求和 SSE 事件。

---

## 路线图

- [x] 项目脚手架
- [x] 三种风格提示词
- [x] CLI 交互
- [x] HTTP Server
- [x] 会话记忆 (SQLite)
- [x] 内置工具 (9 个)
- [x] AGENTS.md / Skills / Rules
- [x] Wails v2 桌面端
- [x] Vue 3 前端 + SSE 流式渲染
- [x] 思考过程可折叠面板
- [x] 项目系统 + 会话归档
- [x] Plan/Build 模式
- [x] 多模型 + 模型能力标记
- [x] 知识库系统（向量库 + 语义检索 + 对话级绑定）
- [x] 消息回滚
- [ ] MCP 工具协议完整集成
- [ ] Docker 代码沙箱
