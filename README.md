# P-Chat

**对话式 AI Agent · 三种人格 · 四端同源（CLI / HTTP / 桌面端 / 移动端）**

## 快速开始

### 1. 配置

首次运行会自动创建 `~/.p-chat/` 目录结构：

```bash
# 编辑配置文件（新版 JSON，旧版 YAML 仍兼容）
~/.p-chat/config.json

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

### 4. 运行桌面端 (Wails v2)

**Windows:**

```powershell
# 一次性打包出所有产物
task package:gui

# 装到 %LOCALAPPDATA%\Programs\P-Chat\（带开始菜单快捷方式 + 卸载项）
task install:gui

# 启动
"%LOCALAPPDATA%\Programs\P-Chat\pchat-gui.exe"

# 卸载
task uninstall:gui
```

**Linux:**

```bash
# 打包（Wails GUI 必须在 Linux 上编译）
task package:gui:linux
# 或者：make package-linux

# 安装到 ~/.local/bin/（含 .desktop 快捷方式）
cd build/bin && ./install.sh

# 系统级安装
cd build/bin && sudo ./install.sh --prefix /usr/local

# 便携模式（不安到系统目录）
cd build/bin && ./install.sh --portable

# 卸载
~/.local/share/pchat/uninstall.sh
# 同时删除 ~/.p-chat/ 数据
~/.local/share/pchat/uninstall.sh --remove-data
```

桌面端架构：`pchat-gui` 启动时拉起 `pchat-server`（同目录子进程，端口随机），等后端就绪后 webview 自动跳转到 `http://127.0.0.1:<port>/app/index.html` —— 跟浏览器打开 `pchat web` 是同一份 `web/index.html`，所以功能 1:1 一致。关窗自动杀子进程。

### 5. 编译

```bash
task build          # CLI + Server
task build:gui      # 桌面端 (Wails v2)
task package:gui    # 全部打包到 bin\
```

## 目录结构

### 全局配置 (`~/.p-chat/`)

```
~/.p-chat/
├── config.json         # 主配置（LLM、Server、Memory 等；旧版 config.yaml 仍兼容）
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
├── config.json         # 项目配置（覆盖全局）
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
│   ├── pchat-server/    # HTTP Server 入口
│   └── pchat-gui/       # 桌面端 (Wails v2)
│       ├── main.go      # 拉起 pchat-server 子进程 + WebView2 窗口
│       ├── install.ps1  # 安装脚本（开始菜单 + 注册表卸载项）
│       ├── uninstall.ps1
│       └── build/       # Wails 产物
├── frontend/            # Vue 3 + Vite + Naive UI SPA
│   └── src/
│       ├── api/client.ts     # HTTP/SSE 客户端
│       ├── stores/chat.ts    # Pinia 会话状态
│       └── components/       # 18 个 Vue 组件
├── internal/
│   ├── agent/           # Agent 核心
│   ├── agents/          # AGENTS.md 加载
│   ├── config/          # 配置加载
│   ├── llm/             # LLM 客户端
│   ├── memory/          # 记忆模块
│   ├── paths/           # 路径解析
│   ├── rules/           # 规则加载
│   ├── server/          # HTTP Server
│   ├── serverproc/      # 子进程拉起 + 健康等待
│   ├── skill/           # 技能加载
│   ├── style/           # 风格管理
│   └── tool/            # 内置工具
├── prompts/             # 本地风格提示词
├── configs/             # 示例配置
├── scripts/             # 打包 + 冒烟测试脚本
├── web/                 # Web UI 入口（桌面端和 pchat web 共用）
├── go.mod
├── Taskfile.yml
└── README.md
```

## 配置优先级

配置加载顺序（后加载覆盖先加载）：

1. 默认值（代码内置）
2. `~/.p-chat/config.json`（全局配置；旧版 `config.yaml` 仍兼容，自动迁移）
3. `.p-chat/config.json`（项目配置）
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

所有端点都在 `/api/v1/` 下。

### 核心
| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/health` | 健康检查 |
| GET/POST/PATCH/DELETE | `/styles` / `/:id` | 风格 CRUD（identity + soul） |
| GET/POST/PATCH/DELETE | `/providers` / `/:name` | 供应商管理 |
| POST/PUT/DELETE | `/providers/:name/models` / `/:model` | 模型 CRUD |
| POST | `/providers/:name/models/:model/default` | 设默认模型 |
| PATCH | `/providers/:name/models/:model/capabilities` | 模型能力标记（vision/thinking） |

### 会话
| 方法 | 路径 | 说明 |
|------|------|------|
| GET/POST | `/sessions` | 列出/创建（支持 `?project_path=` 过滤） |
| GET/PATCH/DELETE | `/sessions/:id` | 获取/更新元数据/归档 |
| GET | `/sessions/:id/messages` | 历史消息 |
| POST | `/sessions/:id/messages` | **发送消息（SSE 流式响应）** |
| POST | `/sessions/:id/compress` | 压缩对话 |
| PATCH | `/sessions/:id/reasoning-effort` | 设置思考深度 |
| POST | `/sessions/:id/system-message` | 自定义 system prompt |
| DELETE | `/sessions/:id/messages` | 清空会话 |

### 归档
| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/sessions/:id/archive` | 软删除 |
| POST | `/sessions/:id/unarchive` | 恢复 |
| GET | `/sessions/archived` | 列出已归档 |
| DELETE | `/sessions/:id/permanent` | 永久删除 |

### 项目
| 方法 | 路径 | 说明 |
|------|------|------|
| GET/POST/DELETE | `/projects` | 列出/添加/移除项目目录 |
| POST | `/dialog/folder` | 打开原生文件夹选择对话框 |

### 技能
| 方法 | 路径 | 说明 |
|------|------|------|
| GET | `/skills` | 列出已安装技能 |
| POST | `/skills/install` | 安装技能 |
| DELETE | `/skills/:name` | 卸载技能 |
| GET | `/skills/search?q=` | 搜索技能仓库 |

### 其他
| 方法 | 路径 | 说明 |
|------|------|------|
| POST | `/uploads` | 文件上传（multipart） |
| GET | `/uploads/:id` | 获取已上传文件 |
| GET | `/commands` | 列出斜杠命令 |
| POST | `/commands/:name` | 执行斜杠命令 |

### POST /api/v1/sessions/:id/messages (SSE)

```json
{
  "message": "你好",
  "style": "cute",
  "provider": "openai",
  "model": "gpt-4o",
  "attachments": []
}
```

响应 (SSE):

```
data: {"type":"thinking","thinking":"我需要分析用户的问题..."}
data: {"type":"phase","step":"analyzing","message":"分析问题..."}
data: {"type":"content","content":"你好呀"}
data: {"type":"tool","tool_name":"read_file","tool_args":"{}","tool_status":"start"}
data: {"type":"tool","tool_name":"read_file","tool_status":"ok","tool_result":"..."}
data: {"type":"content","content":"！"}
data: {"type":"done","tokens_in":123,"tokens_out":456,"elapsed":"2.3s"}
```

## 开发任务

- [x] 项目脚手架
- [x] 三种风格提示词
- [x] CLI 交互
- [x] HTTP Server
- [x] 多模型支持 (OpenAI 兼容协议)
- [x] 会话记忆 (SQLite)
- [x] 内置工具 (文件/Shell/子代理)
- [x] AGENTS.md 支持
- [x] Skills 技能系统
- [x] Rules 规则系统
- [x] Wails v2 桌面端
- [x] Vue 3 + Vite + Naive UI 前端
- [x] SSE 流式渲染（逐事件 flush、打字机效果）
- [x] 思考过程展示（可折叠面板）
- [x] 项目系统（多项目目录注册）
- [x] 会话归档/恢复
- [x] 技能安装/搜索（GUI）
- [x] Plan/Build 模式切换
- [x] 供应商多模型（per-provider models）
- [ ] MCP 工具协议集成
- [ ] 本地 LLM (Ollama) 深度集成
- [ ] 代码沙箱 (Docker)
