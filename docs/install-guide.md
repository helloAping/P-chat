# P-Chat 安装与使用说明

> P-Chat 是为本地使用设计的 AI 编程助手：**完全本地化运行**（无云端依赖）、**多 LLM 协议兼容**（OpenAI / Anthropic / 自定义代理）、支持**结构化消息流**（text + thinking + tool calls + sub-agents）。
>
> 本文为**安装 + 使用合并文档**，按「**API 厂商接入 → CLI / GUI 安装使用**」的顺序组织。截图位置以 `[xxx图片]` 占位，后续逐步补充。

---

## 目录

1. [架构概览](#1-架构概览)
2. [API 厂商接入（配置 LLM Provider）](#2-api-厂商接入配置-llm-provider)
   - 2.1 配置文件位置
   - 2.2 支持的协议
   - 2.3 方式一：GUI 可视化配置（推荐）
   - 2.4 方式二：直接编辑 config.yaml
   - 2.5 各厂商接入指南（配置 + 申请路径 + GUI 配置方法）
   - 2.6 校验配置
3. [CLI 安装与使用](#3-cli-安装与使用)
   - 3.1 获取 CLI
   - 3.2 启动参数与子命令
   - 3.3 首次启动与配置
   - 3.4 REPL 常用命令
4. [GUI 安装与使用](#4-gui-安装与使用)
   - 4.1 Wails 桌面版（pchat-gui）
   - 4.2 独立 Web 版（pchat-server）
   - 4.3 界面操作
5. [常见问题（FAQ）](#5-常见问题faq)
6. [附录：配置与数据目录](#6-附录配置与数据目录)

---

## 1. 架构概览

P-Chat 提供三种使用形态，**共用同一套后端逻辑**（Agent 循环、LLM 客户端、工具系统、记忆存储）：

| 形态 | 入口 | 用途 |
| --- | --- | --- |
| CLI 终端 | `pchat.exe` | 命令行 REPL，启动时自动拉起 `pchat-server` 子进程 |
| 独立 Web | `pchat-server.exe` | 纯 HTTP server + 浏览器访问的 web UI |
| 桌面应用 | `pchat-gui.exe` | Wails 桌面端，内嵌 webview，同样 spawn `pchat-server` 子进程 |

```
pchat-gui / pchat (CLI)
        │  exec.Command 拉起
        ▼
  pchat-server ── HTTP + SSE ──► 前端 (Vue 3) / CLI REPL
        │
        └─► LLM API (OpenAI / Anthropic / 自定义代理)
```

> 关键点：`pchat` 与 `pchat-gui` 只是不同的**前端入口**，聊天全部经由 `pchat-server` 的 REST / SSE 端点，因此三者的模型配置、会话历史、知识库完全互通。**在 GUI 中配置的 LLM 提供商，CLI 与 Web 版直接复用，无需重复配置。**

---

## 2. API 厂商接入（配置 LLM Provider）

### 2.1 配置文件位置

| 文件 | 作用 |
| --- | --- |
| `configs/config.yaml` | 项目内置**默认配置模板**（含各厂商示例） |
| `~/.p-chat/config.yaml` | **用户运行时配置**（实际生效；不存在时从默认模板复制） |

> `~` 在 Windows 下即用户主目录（如 `C:\Users\<你的用户名>`）。P-Chat 首次运行会自动创建 `~/.p-chat/` 目录。

配置文件顶层结构：

```yaml
server:
  host: "127.0.0.1"
  port: 15150

llm:
  default: "openai"        # 默认使用的 provider 名
  output:
    language: "auto"       # "zh" | "en" | "auto"（跟随用户输入语言）
  providers:
    - # 一个或多个 provider，见下文
```

### 2.2 支持的协议

`protocol` 字段决定走哪套 API 兼容层：

| `protocol` | 适用 | 说明 |
| --- | --- | --- |
| `openai` | OpenAI 官方、DeepSeek、豆包（火山方舟）、智谱、Kimi、各类 OpenAI 兼容代理 | 标准 `chat/completions` 协议；支持流式 reasoning（`delta.reasoning_content`） |
| `anthropic` | Anthropic 官方 Claude | `messages` 流式协议；支持 thinking 字段 |

> 大多数国内厂商（DeepSeek / 豆包 / 智谱等）都提供 **OpenAI 兼容端点**，填它们的 `base_url` 即可，无需特殊处理。

### 2.3 方式一：GUI 可视化配置（推荐）

> 桌面 GUI（`pchat-gui.exe`）与 Web 版（`pchat-server.exe`）共用同一个设置面板。以下步骤在两者中一致。
> **CLI 用户也可以照此操作**——配置写入 `~/.p-chat/config.yaml`，CLI 下次启动自动生效。

**第 1 步：打开设置面板**

- 主界面**左侧边栏底部**有「P-Chat 版本号」字样，其右侧的 **齿轮图标（⚙ 设置）** 即设置入口，点击打开；
- 或点击侧边栏顶部的 ⋯ 菜单，选择 **「设置」**。

[xxx图片：主界面左侧边栏底部，标注齿轮设置按钮]

**第 2 步：进入「LLM 提供商」页**

设置面板左侧导航共 11 个分类，点击第一个 **「LLM 提供商」**（图标 CPU，说明「API key 与模型管理」）。
该页为**左右分栏**：左侧是提供商列表，右侧是选中项的编辑表单。

[xxx图片：设置面板全貌 —— 左侧 11 个 tab 导航 + 右侧「LLM 提供商」页，标注「LLM 提供商」tab]

**第 3 步：添加提供商**

1. 点击左侧列表上方的 **「+」按钮**，弹出添加表单；
2. 填写字段（`名称 / 协议 / 模型` 为**必填**）：

| 字段 | 说明 | 示例 |
| --- | --- | --- |
| 名称 | 提供商在列表中的显示名，自定义即可 | `openai`、`deepseek`、`my-proxy` |
| 协议 | 接口协议，二选一：`openai`（OpenAI 兼容）或 `anthropic`（Anthropic） | `openai` |
| Base URL | 服务商 API 地址；官方 OpenAI / Anthropic 可留空使用默认地址，中转/代理必填 | `https://api.deepseek.com/v1` |
| API Key | 在厂商官网申请的密钥 | `sk-xxxxxxxx` |
| 模型 | 默认模型 ID（后续可在底部模型表格中增删） | `deepseek-chat` |

3. 点击 **「保存」**，提供商即出现在左侧列表中。

[xxx图片：添加提供商弹窗，标注各输入框（名称 / 协议 / Base URL / API Key / 模型）]

**第 4 步：编辑 / 删除 / 设默认**

- 在左侧列表点击某个提供商，右侧显示完整表单（含已保存的 Base URL 与 API Key）；
- 可修改 **名称 / 协议 / Base URL / API Key**，勾选 **「设为默认」** 可将该提供商设为全局默认；
- 修改后点击「保存」；如需删除，使用列表上的删除操作。

[xxx图片：LLM 提供商 tab 完整界面 —— 左侧提供商列表（含默认标记）+ 右侧编辑表单（含「设为默认」开关）]

**第 5 步：管理模型**

右侧表单底部是**模型表格**（每行含显示名 / 上下文 / max_tokens 等设置），支持：

- 每行右侧的 **编辑 / 设为默认 / 删除** 操作；
- 表格底部 **「+ 添加模型」** 按钮：按提供商补充更多模型 ID。

[xxx图片：设置面板「模型」表格 —— 按提供商分组的模型列表 + 添加/编辑/删除操作]

**第 6 步：验证**

关闭设置面板，在输入框左下方的**模型徽章**（⭐ 图标 + 模型名）上确认显示了目标模型，
发送一条测试消息即可验证（详见 [2.6 校验配置](#26-校验配置)）。

> 各厂商的具体填写值（Base URL / 协议 / 模型 ID / 申请路径）见 [2.5 各厂商接入指南](#25-各厂商接入指南配置--申请路径--gui-配置方法)。

### 2.4 方式二：直接编辑 config.yaml

> 与 GUI 配置**二选一**，两者写入的是同一个 `~/.p-chat/config.yaml`。

**单模型 Provider**（一个 provider 只挂一个模型，用 `model` 字段）：

```yaml
llm:
  providers:
    - name: "claude"                        # 唯一标识，/model /provider 里显示
      protocol: "anthropic"
      base_url: "https://api.anthropic.com"
      api_key: "sk-ant-xxxxxxxx"            # 填入你的密钥
      model: "claude-3-5-sonnet-20241022"
```

**多模型 Provider**（同一个 `base_url` + `api_key` 下挂多个模型，用 `models` 列表；`default: true` 标记默认模型，否则取第一个）：

```yaml
llm:
  providers:
    - name: "openai"
      protocol: "openai"
      base_url: "https://api.openai.com/v1"
      api_key: "sk-xxxxxxxx"
      models:
        - name: "gpt-4o"
          default: true
          display_name: "GPT-4o"           # 可选，界面展示名
          description: "Most capable, multimodal"
        - name: "gpt-4o-mini"
          display_name: "GPT-4o mini"
          description: "Fast and cheap"
        - name: "o1-preview"
          display_name: "o1 Preview"
          description: "Reasoning model"
```

编辑保存后**重启**程序生效（CLI 支持配置热重载）。

### 2.5 各厂商接入指南（配置 + 申请路径 + GUI 配置方法）

> 每个厂商小节均包含：**config 配置** → **API Key 申请路径** → **GUI 配置方法**。
> 通用 GUI 操作步骤（打开设置 → LLM 提供商 → + 添加 → 保存）见 [2.3 节](#23-方式一gui-可视化配置推荐)，各小节只列出该厂商需要填写的具体值。

---

#### 2.5.1 OpenAI（官方）

**config 配置**

```yaml
- name: "openai"
  protocol: "openai"
  base_url: "https://api.openai.com/v1"
  api_key: "sk-xxxxxxxx"
  models:
    - name: "gpt-4o"
      default: true
    - name: "gpt-4o-mini"
```

**API Key 申请路径**

1. 访问 <https://platform.openai.com> 注册 / 登录 OpenAI 账号；
2. 进入 **API Keys** 页面：<https://platform.openai.com/api-keys>；
3. 点击 **Create new secret key**，为密钥命名后生成，**复制保存**（密钥只显示一次）；
4. 官方 API 需先在 **Billing** 页面绑定支付方式并预充值，否则调用会返回 402 / insufficient_quota。

[xxx图片：OpenAI API Keys 页面，标注「Create new secret key」按钮与密钥复制位置]

**GUI 配置方法**

设置 → LLM 提供商 → 「+」添加：

| 字段 | 值 |
| --- | --- |
| 名称 | `openai` |
| 协议 | `openai` |
| Base URL | 留空（自动使用官方默认地址）或填 `https://api.openai.com/v1` |
| API Key | 上面申请的 `sk-...` |
| 模型 | `gpt-4o`（默认），可再添加 `gpt-4o-mini` 等 |

---

#### 2.5.2 Anthropic Claude（官方）

**config 配置**

```yaml
- name: "claude"
  protocol: "anthropic"
  base_url: "https://api.anthropic.com"     # 注意：不带 /v1 后缀
  api_key: "sk-ant-xxxxxxxx"
  model: "claude-3-5-sonnet-20241022"
```

**API Key 申请路径**

1. 访问 <https://console.anthropic.com> 注册 / 登录；
2. 进入 **API Keys** 设置页：<https://console.anthropic.com/settings/keys>；
3. 点击 **Create Key**，命名后生成，**复制保存**（前缀通常为 `sk-ant-`）；
4. 官方 API 为预付费模式，需先在 **Billing / Credits** 充值后才可调用。

[xxx图片：Anthropic Console API Keys 页面，标注「Create Key」按钮与密钥复制位置]

**GUI 配置方法**

设置 → LLM 提供商 → 「+」添加：

| 字段 | 值 |
| --- | --- |
| 名称 | `claude` |
| 协议 | `anthropic` |
| Base URL | 留空（自动使用官方默认地址）或填 `https://api.anthropic.com`（**不要带 /v1**） |
| API Key | 上面申请的 `sk-ant-...` |
| 模型 | `claude-3-5-sonnet-20241022`（或你账号可用模型） |

---

#### 2.5.3 DeepSeek（OpenAI 兼容）

**config 配置**

```yaml
- name: "deepseek"
  protocol: "openai"
  base_url: "https://api.deepseek.com/v1"
  api_key: "sk-xxxxxxxx"
  models:
    - name: "deepseek-chat"
      default: true
    - name: "deepseek-reasoner"
```

**API Key 申请路径**

1. 访问 <https://platform.deepseek.com> 注册 / 登录；
2. 进入 **API Keys** 页面：<https://platform.deepseek.com/api_keys>；
3. 点击 **创建 API Key**，命名后生成，**复制保存**；
4. 新账号需先在 **充值** 页面为账户充值，之后即可按量计费调用。

[xxx图片：DeepSeek 开放平台 API Keys 页面，标注「创建 API Key」按钮]

**GUI 配置方法**

设置 → LLM 提供商 → 「+」添加：

| 字段 | 值 |
| --- | --- |
| 名称 | `deepseek` |
| 协议 | `openai` |
| Base URL | `https://api.deepseek.com/v1` |
| API Key | 上面申请的 `sk-...` |
| 模型 | `deepseek-chat`（默认），可再添加 `deepseek-reasoner` |

---

#### 2.5.4 豆包 / 火山方舟（OpenAI 兼容）

**config 配置**

```yaml
- name: "doubao"
  protocol: "openai"
  base_url: "https://ark.cn-beijing.volces.com/api/v3"   # 火山方舟官方兼容端点
  api_key: "xxxxxxxx"                                    # 火山引擎 API Key
  models:
    - name: "doubao-seed-1-6-250615"    # 模型 ID 需在方舟控制台开通后获取
      default: true
    - name: "ep-xxxxxxxx"               # 或使用推理接入点 ID（ep- 开头）
```

> 若你走的是第三方中转网关（如示例中的 `http://api-convert.08ms.cn/v1`），则将 `base_url` 换成网关地址、`api_key` 换成网关密钥，模型 ID 以网关文档为准：
>
> ```yaml
> - name: "cs"                          # provider 名可自定义
>   protocol: "openai"
>   base_url: "http://api-convert.08ms.cn/v1"   # 你的代理网关地址
>   api_key: ""
>   models:
>     - name: "doubao-seed-2.0-lite"
>       default: true
>     - name: "doubao-pro"
>     - name: "doubao-lite-32k"
> ```

**API Key 申请路径（火山方舟官方）**

1. 访问火山引擎控制台：<https://console.volcengine.com>，注册 / 登录并完成实名认证；
2. 开通**方舟（Ark）**服务：<https://console.volcengine.com/ark>，按指引完成开通；
3. 在方舟控制台 **API Key 管理** 页创建 API Key（形如 UUID 字符串），**复制保存**；
4. 在 **开通管理** 中开通所需的豆包模型（如 `doubao-seed-1-6-250615`），并获取准确的模型 ID。

> 若使用第三方中转网关（`api-convert.08ms.cn` 等），申请入口在**中转服务商自己的控制台**（注册 → 充值 → 创建 API Key），与火山方舟无关；部分网关有**月度配额**限制，用尽后需等待重置或续费。

[xxx图片：火山方舟控制台 —— API Key 管理 / 开通管理页面]

**GUI 配置方法**

设置 → LLM 提供商 → 「+」添加：

| 字段 | 值 |
| --- | --- |
| 名称 | `doubao`（或自定义） |
| 协议 | `openai` |
| Base URL | 官方：`https://ark.cn-beijing.volces.com/api/v3`；中转：网关地址 |
| API Key | 上面申请的 Key |
| 模型 | 方舟控制台中的模型 ID（如 `doubao-seed-1-6-250615`）或中转网关的模型名 |

---

#### 2.5.5 自定义代理 / 中转（通用，可选）

**config 配置**

```yaml
- name: "my-proxy"
  protocol: "openai"                     # 或 "anthropic"，取决于中转协议
  base_url: "https://api.example.com/v1" # 你的代理地址
  api_key: "sk-xxxxxxxx"
  models:
    - name: "your-model-id"
      default: true
```

**API Key 申请路径**

1. 在代理 / 中转服务商的官网注册账号（如各类聚合网关、自建 one-api / new-api 面板）；
2. 在服务商控制台充值并创建 API Key，记录其 **Base URL** 与密钥；
3. 确认中转支持的**协议**（OpenAI 兼容或 Anthropic 兼容）与**模型 ID 列表**，以服务商文档为准。

[xxx图片：第三方中转服务商控制台的 API Key 申请页面]

**GUI 配置方法**

设置 → LLM 提供商 → 「+」添加：名称任意，协议按中转支持的选，Base URL 填网关地址，API Key 填网关密钥，模型填网关提供的模型 ID。

---

### 2.6 校验配置

配置写完后，用 CLI 快速确认 provider 是否被正确加载：

```powershell
# 显示当前合并后的配置（server、LLM 默认、provider 列表）
.\bin\pchat.exe config
```

输出中会列出所有 provider 的 `name / base_url / model / protocol`，核对无误即可。

GUI 中验证：关闭设置面板 → 输入框左下角**模型徽章**（⭐ 图标 + 模型名）确认显示目标模型 →
发送一条测试消息（如「你好，请介绍一下你自己」），正常收到流式回复即接入成功。

> `[config图片]` — `pchat config` 输出截图，待补充。
>
> `[configyaml图片]` — `~/.p-chat/config.yaml` 编辑示例截图，待补充。
>
> `[test-chat图片]` — 发送测试消息并收到回复的主界面截图，待补充。

---

## 3. CLI 安装与使用

### 3.1 获取 CLI

| 方式 | 命令 / 操作 |
| --- | --- |
| 源码构建 | 见项目 `Taskfile.yml`：`task build:all` 产出 `bin/pchat.exe` + `bin/pchat-server.exe` |
| 安装包 | 运行 `pchat-installer.exe`（根目录 / 发布页） |
| 手动放置 | `pchat.exe` 与 `pchat-server.exe` 放在**同一目录**（CLI 启动时会按「同目录 → PATH」顺序寻找 server） |

> `pchat.exe` 本身不包含 agent 逻辑——聊天全部转发给同目录的 `pchat-server.exe`。两者必须配套放置。

### 3.2 启动参数与子命令

```
pchat [flags] [command]

Flags:
  --config <path>   配置文件路径（默认 ~/.p-chat/config.yaml）
  -s, --style <s>   默认风格: off | cute | guofeng | tech
  -p, --provider <p>  LLM 提供商名（默认取 llm.default）

Commands:
  init        在当前目录初始化 .p-chat/ 项目结构
  skills      列出已安装的技能
  rules       列出已加载的规则
  config      显示当前合并后的配置
  web         启动 Web 服务（等价于运行 pchat-server）
  version     显示版本信息
```

示例：

```powershell
# 直接进入 REPL（默认 provider）
.\bin\pchat.exe

# 指定 provider 与风格
.\bin\pchat.exe --provider deepseek --style tech

# 查看版本
.\bin\pchat.exe version
```

### 3.3 首次启动与配置

1. 首次运行会自动创建 `~/.p-chat/` 目录与默认配置；
2. 在 `~/.p-chat/config.yaml` 中按 [第 2 节](#2-api-厂商接入配置-llm-provider) 填入至少一个 provider 的 `api_key`（或在 GUI 中配置，CLI 直接复用）；
3. 重新启动 `pchat.exe`，即可开始对话。

也可以在 REPL 内交互式配置：

```
/setup        # 交互式配置提供商（添加 / 删除 / 设置 Key / 测试）
/config       # 快速查看当前配置
/provider     # 查看当前提供商详情
/model        # 切换或查看当前模型
```

> `[cli-start图片]` — CLI 启动界面截图，待补充。
> `[cli-setup图片]` — `/setup` 交互配置截图，待补充。

### 3.4 REPL 常用命令

> 输入操作：直接输入文字后回车发送；`Shift + Enter` 换行（多行输入）。

| 命令 | 别名 | 说明 |
| --- | --- | --- |
| `/help` | `/h` `/?` | 显示帮助信息 |
| `/style` | `/s` | 切换说话风格 `[off\|cute\|guofeng\|tech]` |
| `/mode` | | 切换工作模式（coding / daily） |
| `/provider` | | 查看 / 切换当前提供商 |
| `/model` | | 查看 / 切换当前模型 |
| `/tools` | | 查看可用工具 |
| `/sessions` | | 会话列表与切换 |
| `/add` | | 添加上下文（引用文件） |
| `/clear` | | 清空当前上下文 |
| `/compress` | | 压缩上下文 |
| `/export` | | 导出对话 |
| `/config` | | 查看当前配置 |
| `/quit` | `/exit` `/q` | 退出 REPL |

> 完整命令列表以终端内 `/help` 输出为准。

---

## 4. GUI 安装与使用

### 4.1 Wails 桌面版（pchat-gui）

**安装**

| 方式 | 说明 |
| --- | --- |
| 安装包 | 运行 `pchat-installer.exe`（含 GUI 与 server 组件） |
| 源码构建 | `task build:gui` 产出 `bin/pchat-gui.exe`，`task package:gui` 产出完整 bundle |

**启动**

双击 `pchat-gui.exe` 即可。它会在启动时自动拉起 `pchat-server.exe` 作为子进程（动态端口），无需手动开服务。

> `[gui-main图片]` — 桌面端主界面截图，待补充。

**配置 API 厂商**

桌面端读取 `~/.p-chat/config.yaml`，推荐直接在界面内配置（见 [2.3 节](#23-方式一gui-可视化配置推荐)），
也可以在模型徽章处快速切换模型/提供商（见 4.3）。

### 4.2 独立 Web 版（pchat-server）

`pchat-server.exe` 是纯 HTTP 服务，浏览器访问即得 web UI：

```powershell
# 直接运行（默认 127.0.0.1:15150，可用 --port 覆盖）
.\bin\pchat-server.exe

# 浏览器打开
start http://127.0.0.1:15150
```

> `[web-main图片]` — 浏览器访问的 Web UI 截图，待补充。

### 4.3 界面操作

- **发送消息**：底部输入框输入，回车发送（`[gui-input图片]` 待补充）；
- **切换模型 / 提供商**：点击输入框左下角**模型徽章**（⭐ 图标 + 模型名），弹出命令面板式模型选择器，按提供商分组、支持搜索，选中即切换（`[gui-model图片]` 待补充）；
- **会话设置**：点击输入框下方「会话设置」按钮，可查看 / 调整当前会话的工作模式、思考强度等；
- **切换风格**：`cute`（可爱）/ `guofeng`（国风）/ `tech`（极客）人格（`[gui-style图片]` 待补充）；
- **工作模式**：`coding` / `daily` 侧重点切换；
- **工具调用可视化**：回复中以结构化卡片展示 thinking / tool call / sub-agent 过程（`[gui-tool图片]` 待补充）；
- **会话管理**：新建 / 历史列表 / 重命名 / 删除（`[gui-history图片]` 待补充）。

---

## 5. 常见问题（FAQ）

| 现象 | 排查建议 |
| --- | --- |
| **Q1：CLI 启动报 "pchat-server not found"** | `pchat.exe` 必须与 `pchat-server.exe` 同目录，或 server 已在 PATH 中。将两者放同一目录即可。 |
| **Q2：改了 `pchat-server` 二进制后 GUI 不生效** | `pchat-gui` 不会自动重启 `pchat-server` 子进程——**改完 server 必须重启 pchat-gui**。 |
| **Q3：回复不显示 / 一直"思考中"** | 查看调试日志：`~/.p-chat/server-debug.log`（LLM 流式 JSON 原始日志）、`bin/pchat-server.log`（server 启动日志）；确认提供商 Base URL / API Key 填写正确、模型 ID 存在。 |
| **Q4：Anthropic 协议不工作** | 确认 `base_url` 为 `https://api.anthropic.com`（不带 `/v1` 后缀）、`protocol: "anthropic"`、模型名正确（如 `claude-3-5-sonnet-20241022`）。 |
| **Q5：提示 API quota 用完** | 部分第三方代理（如 `api-convert.08ms.cn`）有月度配额限制，与 P-Chat 本身无关，请检查厂商控制台余额 / 额度。 |
| **Q6：Web 版端口被占用** | 用 `--port` 指定其他端口启动 `pchat-server`。 |
| **Q7：提示 API Key 无效 / 401** | 重新在厂商控制台生成 Key，并在设置「LLM 提供商」中更新保存。 |
| **Q8：切换了模型但没生效** | 检查该模型的协议是否与提供商一致（openai / anthropic 不可混用）。 |
| **Q9：CLI 无法连接服务端** | 确认没有残留的 pchat-server 进程占用端口，重启 `pchat.exe`。 |

---

## 6. 附录：配置与数据目录

所有本地数据保存在用户目录下的 `.p-chat/` 文件夹：

```
~/.p-chat/
├── config.yaml        # 配置（提供商 / 模型 / 风格 / 服务器等，GUI 设置会写入这里）
├── server-debug.log   # 后端运行日志
└── memory/            # 会话记忆数据库 (SQLite)
```

---

*文档版本：2.0（安装 + 使用合并版；已补充各厂商 API Key 申请路径与 GUI 配置方法；截图占位待补充）*
