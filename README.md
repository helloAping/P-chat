# P-Chat

**对话式 AI Agent · 三种人格 · 四端同源**

## 快速开始

### 1. 配置

首次运行会自动创建 `~/.p-chat/` 目录结构：

```bash
# 编辑配置文件
~/.p-chat/config.yaml

# 填入你的 API Key
```

### 2. 运行 CLI

```bash
# 默认科技风
pchat.exe

# 指定风格
pchat.exe --style cute
pchat.exe --style guofeng

# 指定 LLM 提供商
pchat.exe --provider ollama
```

### 3. 运行 HTTP Server

```bash
pchat-server.exe
# → http://127.0.0.1:8960/api/v1/health
```

### 4. 编译

```bash
task build
# 或
go build -o bin/pchat.exe ./cmd/pchat
go build -o bin/pchat-server.exe ./cmd/pchat-server
```

## 目录结构

### 全局配置 (`~/.p-chat/`)

```
~/.p-chat/
├── config.yaml         # 主配置（LLM、Server、Memory 等）
├── AGENTS.md           # 全局 Agent 指令
├── skills/             # 全局技能
│   └── code-review/
│       └── SKILL.md
├── rules/              # 全局规则
│   └── code-style.md
├── prompts/            # 风格提示词
│   ├── cute.md
│   ├── guofeng.md
│   └── tech.md
├── memory/             # 记忆存储
│   └── conversations.json
└── tools/              # 自定义工具
```

### 项目配置 (`.p-chat/`)

```
.p-chat/
├── config.yaml         # 项目配置（覆盖全局）
├── AGENTS.md           # 项目级 Agent 指令
├── skills/             # 项目级技能
│   └── my-skill/
│       └── SKILL.md
└── rules/              # 项目级规则
    └── project-rules.md
```

### 项目根目录

```
P-chat/
├── cmd/
│   ├── pchat/           # CLI 入口
│   └── pchat-server/    # HTTP Server 入口
├── internal/
│   ├── agent/           # Agent 核心
│   ├── agents/          # AGENTS.md 加载
│   ├── config/          # 配置加载
│   ├── llm/             # LLM 客户端
│   ├── memory/          # 记忆模块
│   ├── paths/           # 路径解析
│   ├── rules/           # 规则加载
│   ├── server/          # HTTP Server
│   ├── skill/           # 技能加载
│   ├── style/           # 风格管理
│   └── tool/            # 内置工具
├── prompts/             # 本地风格提示词
├── configs/             # 示例配置
├── web/                 # 桌面端前端
├── go.mod
├── Taskfile.yml
└── README.md
```

## 配置优先级

配置加载顺序（后加载覆盖先加载）：

1. 默认值（代码内置）
2. `~/.p-chat/config.yaml`（全局配置）
3. `.p-chat/config.yaml`（项目配置）
4. `--config` 参数指定的文件（最高优先级）

## 风格人格

| 风格 | 名称 | 关键词 | 启动参数 |
|------|------|--------|---------|
| 可爱风 | 小P (PiPi) | 软萌、颜文字、治愈 | `--style cute` |
| 古风 | 墨言 (MoYan) | 雅致、引经据典、谦辞 | `--style guofeng` |
| 科技风 | NEXUS (零号) | 冷静、结构化、高效 | `--style tech` |

## AGENTS.md

AGENTS.md 是 Agent 的行为指令文件，支持两层：

- `~/.p-chat/AGENTS.md`：全局指令，对所有项目生效
- `./AGENTS.md`：项目级指令，仅对当前项目生效

两者会合并注入到 System Prompt 中。

## Skills 技能系统

每个技能是一个目录，包含 `SKILL.md` 文件：

```
~/.p-chat/skills/code-review/
└── SKILL.md
```

SKILL.md 的第一行非空非标题文本会被提取为技能描述。

## Rules 规则系统

规则是 `.md` 文件，存放在 `rules/` 目录：

```
~/.p-chat/rules/
├── code-style.md
└── security.md
```

所有规则文件内容会拼接后注入到 System Prompt 中。

## CLI 命令

| 命令 | 说明 |
|------|------|
| `/help` | 显示帮助信息 |
| `/style [name]` | 切换/查看风格 |
| `/model [provider]` | 切换/查看模型 |
| `/clear` | 清屏 |
| `/skills` | 列出已安装技能 |
| `/rules` | 列出已加载规则 |
| `/agents` | 查看 AGENTS.md |
| `/tools` | 列出可用工具 |
| `/init` | 初始化 .p-chat/ |
| `/quit` | 退出 |

## LLM 协议支持

支持两种 API 协议：

| 协议 | 说明 | 适用提供商 |
|------|------|-----------|
| `openai` | OpenAI 兼容协议 | OpenAI, DeepSeek, Ollama, 通义千问, 智谱等 |
| `anthropic` | Anthropic 原生协议 | Claude (Anthropic) |

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

## HTTP API

| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/api/v1/health` | 健康检查 |
| GET | `/api/v1/styles` | 获取可用风格列表 |
| GET | `/api/v1/providers` | 获取可用 LLM 提供商 |
| POST | `/api/v1/chat` | 流式对话 (SSE) |

### POST /api/v1/chat

```json
{
  "message": "你好",
  "style": "cute",
  "provider": "openai"
}
```

响应 (SSE):

```
data: {"content":"你好呀","done":false}
data: {"content":"！","done":false}
data: {"content":"","done":true}
```

## 开发任务

- [x] 项目脚手架
- [x] 三种风格提示词
- [x] CLI 交互
- [x] HTTP Server
- [x] 多模型支持 (OpenAI 兼容协议)
- [x] 会话记忆
- [x] 内置工具 (文件/Shell)
- [x] AGENTS.md 支持
- [x] Skills 技能系统
- [x] Rules 规则系统
- [ ] MCP 工具协议集成
- [ ] Tauri/Wails 桌面端
- [ ] iOS SwiftUI 客户端
- [ ] 本地 LLM (Ollama) 深度集成
- [ ] 代码沙箱 (Docker)
