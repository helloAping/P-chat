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
| **代码高亮** | 1.0.8 起：代码块带 syntax 颜色（14 个主流语言：ts/py/go/rs/java/json/yaml/bash 等） |
| **上下文检查器** | 1.0.8 起：TopBar 📊 按钮，右侧抽屉显示 context window 利用率 + per-message token 列表 + compressed summary |
| **工具 dry-run** | 1.0.8 起：`exec_command` / `read_file` / `write_file` 加 `dry_run: true` 参数；UI 用 "干跑 X" prompt 触发，ToolCallCard 显示 `dry-run` chip |
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
# 打开浏览器访问 http://127.0.0.1:15150/app/index.html
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
| 风格 | 选择说话风格；选「关闭」等同于 `style=off` |
| 工作模式 | 选择 `coding` / `daily`，决定当前会话优先处理编码任务还是日常文档任务 |
| 推理等级 | off / low / medium / high / max，控制 LLM 思考深度 |
| 计划/构建 | 切换 Plan/Build 模式。计划模式让 LLM 先出方案不执行 |
| 权限级别 | 🔒 始终询问 / 🔓 替我审批 / 🔑 完全访问，控制沙箱行为 |
| 知识库 | 选择当前对话使用的知识库（不使用 / 全部 / 指定知识库） |
| 附件 | 支持拖拽上传图片、音频、PDF、文本文件等 |

### 应用设置 (⚙)

点击右上角齿轮图标打开设置面板，包含以下 Tab：

| Tab | 功能 |
|-----|------|
| LLM 提供商 | 添加/编辑/删除 LLM 提供器，管理多模型，设置默认模型，标记模型能力 (vision/thinking) |
| 风格 | 管理说话风格、人格 prompt 和风格记忆 |
| 系统 | 设置全局工作模式、自动压缩、工具结果截断等系统偏好 |
| MCP 服务器 | 配置 Model Context Protocol 服务器 |
| 技能 | 安装/卸载/搜索技能包 |
| 归档 | 查看和恢复已归档会话，永久删除 |
| 知识库 | 配置知识库系统（详见下方 [知识库](#知识库-knowledge-base)） |
| 联网搜索 | 配置 web_search 使用的搜索供应商 |
| 浏览器 | 启用浏览器控制、下载扩展、查看已连接浏览器/标签页、选择控制目标、检查扩展协议兼容性、域名/风险权限策略 |

### GUI 操作入口速查

| 想做什么 | GUI 操作入口 |
|----------|--------------|
| 切换编码/日常工作模式 | 聊天输入区的「工作模式」选择器；全局默认值在「应用设置」→「系统」→「工作模式」 |
| 关闭或切换说话风格 | 聊天输入区的「风格」选择器；自定义风格在「应用设置」→「风格」 |
| 选择本轮对话知识库 | 聊天输入区的「知识库」选择器，可选「不使用」「全部」或指定知识库 |
| 配置知识库目录和扫描 | 「应用设置」→「知识库」→ 新增/选择知识库 → 扫描 |
| 查看可用工具 | 顶部栏点击「工具列表」按钮，右侧抽屉会列出内置工具、自定义工具和动态工具 YAML 加载诊断；自定义工具行内可填写参数、干跑或执行试运行 |
| 启用浏览器控制 | 「应用设置」→「浏览器」→ 打开「启用浏览器控制」→ 下载并安装扩展；同页可查看连接诊断、标签页列表、设置控制目标与扩展更新提示 |
| 复制 trace id | 顶部栏 `#` 按钮，或错误消息气泡下方的 trace id 按钮 |
| 重新生成回答 | assistant 消息底部点击「重答」，历史版本会显示在消息下方的版本切换条 |

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
  "server": { "host": "127.0.0.1", "port": 15150 },
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

## 常见问题

### 1. LLM 做到一半停了怎么办？

GUI：当前主要通过会话元数据生效，默认开启。多步任务建议先让 LLM 写 `todo_write`，状态栏会显示自动续提示。

CLI：查看或切换 `auto-continue`：

```bash
/auto-continue
/auto-continue on
/auto-continue off
```

HTTP：也可以 `PATCH /api/v1/sessions/:id` 设置 `{"auto_continue": false}`。

### 2. 我想让它按“编码”或“日常工作”做事，怎么切？

GUI：在输入框上方/附近的会话级选项里点击「工作模式」，选择「编码」或「日常工作」。这只影响当前会话。

全局默认：打开「应用设置」→「系统」→「工作模式」，选择默认值；新会话会使用这个默认模式。

CLI：

```bash
/mode coding
/mode daily
```

`coding` 偏向读写代码、测试、构建；`daily` 偏向文档、邮件、摘要、知识检索。

### 3. 我不想要说话风格了，怎么关？

GUI：在输入区的「风格」选择器里选「关闭」。关闭后只是停止注入风格 prompt 和记忆，不影响 `work_mode`。

CLI：

```bash
/style off
```

### 4. 怎么看当前能用哪些工具？

GUI：点击顶部栏「工具列表」按钮，右侧抽屉会显示内置工具、全局自定义工具和当前项目自定义工具。动态工具会显示作用域（全局 / 项目）和 YAML 来源路径。自定义工具下方会按 YAML 的 `parameters` 生成参数表单，可以先点「干跑」查看最终命令、URL 或返回文本，也可以点「执行」做一次真实调用；最近一次结果会留在该工具行里，方便复看错误。

如果某个 `~/.p-chat/tools/*.yaml` 或当前项目 `.p-chat/tools/*.yaml` 没有出现在自定义工具列表里，先看抽屉顶部的「加载诊断」。那里会列出加载失败的 YAML 路径、作用域、最近修改时间和具体错误，例如缺少 `name`、`description`、`template.type`，或 `sandbox.exec` 不是 `allow|deny|confirm`。

CLI：

```bash
/tools
```

动态工具文件可放在全局 `~/.p-chat/tools/*.yaml`，也可放在当前项目的 `.p-chat/tools/*.yaml`。项目工具只在绑定该项目的会话中生效；同名时项目工具覆盖全局工具。修复 YAML 后刷新工具列表，错误项会消失，对应工具会进入「全局自定义工具」或「项目自定义工具」区域。

### 5. 如何启用浏览器控制？

GUI 流程：

1. 打开右上角「应用设置」。
2. 进入「浏览器」Tab。
3. 打开「启用浏览器控制」。
4. 点击「下载扩展包」。
5. 解压下载的 zip。
6. Chrome / Edge 打开 `chrome://extensions`。
7. 开启「开发者模式」。
8. 点击「加载已解包扩展」，选择解压目录。
9. 在扩展弹窗的「服务器」输入框里粘贴 GUI 中显示的服务器地址。
10. 回到 P-Chat 的「浏览器」Tab，确认「已连接浏览器」数量大于 0。

连接成功后，LLM 才能调用 `browser_*` 工具，例如导航、点击、输入、截图、提取内容。

排查连接问题时，仍在「浏览器」Tab 看诊断区域：

- `HTTP 地址`：复制到扩展弹窗里的服务器地址。
- `WebSocket 地址`：扩展实际连接的桥接地址。
- `已连接数量`：大于 0 才会注册 `browser_*` 工具。
- `最近错误`：显示最近一次握手、断线或工具调用错误。
- 已连接浏览器行会显示 tab 数、扩展版本、协议版本和最后活跃时间；旧扩展未上报版本时会显示「未上报」。
- 如果浏览器行显示「需更新扩展」，点击同页「下载最新扩展」，解压后回到 `chrome://extensions`，对原来的 P-Chat 扩展选择「重新加载」或重新「加载已解包扩展」。

### 5.1 如何选择浏览器控制目标标签页？

当浏览器里打开了多个标签页时，可以指定 P-Chat 的 `browser_*` 工具默认作用到哪一个：

1. 打开「应用设置」→「浏览器」。
2. 确认已连接浏览器数量大于 0。
3. 在对应浏览器卡片下的「标签页」列表中，查看标题与 URL。
4. 点击目标行的「设为控制目标」。
5. 成功后该行显示「控制目标」标签；浏览器摘要也会更新「控制目标」。

说明：

- 控制目标是扩展侧记住的 preferred tab，不强制要求该标签页一直在浏览器前台。
- LLM 调用 `browser_*` 时默认使用这个目标；也可在工具参数里显式传 `tab_id` 覆盖。
- `browser_tabs` 工具的 `action=select` 也会切换控制目标。
- 如果目标标签页被关闭，扩展会自动回退到浏览器当前前台标签页。
- 截图会短暂激活目标标签页（Chrome API 限制），其他操作通常不需要切前台。


### 5.2 浏览器工具权限（BR-04）

`browser_*` 工具会在执行前按「动作风险 + 页面域名」决策：自动通过、弹确认框、或硬拦截。

默认策略（`browser.require_confirm: "dangerous"`）：

| 场景 | 行为 |
|---|---|
| 只读操作（snapshot / extract / find / screenshot / scroll 等） | 自动通过 |
| 普通导航 / 点击 | 自动通过 |
| 表单输入 / 上传 / `browser_evaluate` | 弹出确认（显示目标页面 URL） |
| 命中 `sensitive_hosts`（如登录页） | 即使只读也确认 |
| 命中 `blocked_hosts` | 直接拦截，不确认 |
| 命中 `allowed_hosts` | 自动通过（仍尊重 `blocked_hosts`） |

配置写在 `~/.p-chat/config.json` 的 `browser` 段：

```json
{
  "browser": {
    "enabled": true,
    "require_confirm": "dangerous",
    "allowed_hosts": ["localhost", "*.internal.example"],
    "blocked_hosts": ["evil.example"],
    "sensitive_hosts": ["accounts.google.com", "*.alipay.com"]
  }
}
```

- `require_confirm`：`never` / `dangerous`（默认） / `always`
- 域名匹配大小写不敏感；`*.example.com` 同时匹配 `example.com` 与子域
- 会话权限 `full` 或 `/unsafe once` 会跳过确认（`blocked_hosts` 仍拦截）
- 确认弹窗与沙箱共用 `ToolConfirmModal`，标签为「浏览器」，「目标页面」展示 URL

### 5.3 浏览器控制回归测试（BR-05）

开发者可在仓库根目录运行：

```bash
go test ./internal/browser -run E2E -count=1
```

该套件用**模拟扩展**（不启动真实 Chrome）覆盖：连接握手、导航/点击/输入/截图、断线重连、`blocked_hosts` 拦截、高风险确认路径、以及 Manager 连接时动态注册工具。

### 6. 报错时怎么把 trace id 给我？

GUI：看错误气泡上的 `trace id` 按钮，点一下就能复制。顶部栏也会显示最近一次请求的 trace，点击同样可复制。

这个 id 也会出现在日志里，方便你直接定位同一条请求。

### 7. 重答以后还能看上一版吗？

GUI：在 assistant 消息底部点击「重答」，确认后会重新生成。旧回答会被归档为历史版本，消息气泡下方会出现版本切换条，可以左右切换。

### 8. 如何使用知识库？

GUI 流程：

1. 打开「应用设置」→「知识库」。
2. 打开「启用知识库」。
3. 添加或选择知识库目录。
4. 点击「扫描」，等待索引完成。
5. 回到聊天输入区，点击「知识库」选择器，选择「不使用」「全部」或某个知识库。

之后 LLM 会按需调用 `recall` 工具检索。



### 8.1 多知识库搜索如何排序？

当配置了多个知识库（或会话选择「全部」）时，P-Chat 会：

1. 在每个启用的知识库内分别检索
2. 将各库分数归一化后合并排序
3. 去掉重复的同一文件/片段
4. 在结果中标注来源知识库（`base`）

这样不会因为第一个库填满 topK 就丢掉后面库里更相关的结果。

### 9. 我想让它自己拆任务、问我问题，怎么让它配合？

直接把目标和约束说清楚就行。它会在需要时使用 `todo_write` 记录步骤，遇到缺信息时会弹出问题框让你确认或补充。

GUI 中会看到：

- 待办面板显示任务列表。
- 工具卡片显示执行过程。
- 如果 LLM 需要你确认，会弹出问题框或工具确认框。

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
