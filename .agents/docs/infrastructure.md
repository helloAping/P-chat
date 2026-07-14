# 基础设施模块

> 包含多个小型工具模块，为其他核心模块提供支撑功能。

## Style 风格管理

**位置**：`internal/style/`  
**文件**：`manager.go`

管理 LLM 人格风格。每个风格由三部分组成，存储为独立文件：

| 组件 | 文件路径 | 注入方式 |
|---|---|---|
| Identity | `prompts/identity/{id}.md` | staticPrompt 缓存 |
| Soul | `prompts/soul/{id}.md` | staticPrompt 缓存 |
| **Memory** | `prompts/memory/{id}.md` | **每轮动态追加** |

**风格即记忆**：不同人设 = 不同 Identity + Soul + Memory。Memory 修改即时生效，不破坏 LLM prefix-cache。

`styleMgr.GetMemory(s)` → 读取 → agent.go 动态注入 systemPrompt 末尾（`## 我的上下文`）。

## 沙箱 (Sandbox)

**位置**：`internal/sandbox/`  
**文件**：`sandbox.go`, `os_stub.go`

对 `exec_command` 和 `write_file` 工具进行安全检查：

- 维护危险命令模式列表（正则）
- 维护受保护路径列表
- 返回 Decision: Allow / Block / Confirm
- 通过 Toolkit `SandboxChecker` 接口与 tool 模块解耦

**修改指南**：
- 添加危险模式：修改 `sandbox.go` 中的模式列表
- 修改确认行为：`SandboxDecision` 枚举 + agent.go 中的 confirm 逻辑

## Skill 技能系统

**位置**：`internal/skill/`  
**文件**：`skill.go`

管理可安装的 Skill 定义：
- `LoadAllWithRoot(root)` — 加载全局 + `<root>/.p-chat/skills` 的 skills（**2026-07 项目根感知**）
- `LoadAll()` — 旧接口，等价于 `LoadAllWithRoot("")`（向后兼容 CLI 命令）
- `Install(repoURL)` — 从 GitHub 安装
- `Delete(name)` — 卸载
- Skill 内容被注入到系统提示词
- 合并策略：全局 + 项目都加载，项目同名覆盖全局

## Style 风格管理

**位置**：`internal/style/`  
**文件**：`manager.go`

管理 LLM 人格风格：
- "tech" — 技术专家风格（默认）
- 用户可定义自定义风格
- 风格定义加载自 `~/.p-chat/styles/`
- 风格通过 `Style` 字段嵌入系统提示词

## MCP 服务器集成

**位置**：`internal/mcp/`  
**文件**：`manager.go`, `client.go`, `transport.go`, `sse_transport.go`, `handler.go`, `types.go`

- 管理外部 MCP (Model Context Protocol) 服务器连接
- 支持 SSE 传输
- 通过配置文件定义服务器列表
- 前端可通过 API 管理 MCP 服务器

## 项目目录管理

**位置**：`internal/project/`  
**文件**：`project.go`

- `~/.p-chat/projects.json` 存储注册的项目目录
- 每个项目可有独立的 `.p-chat/config.json` 和 `AGENTS.md`
- API: `GET/POST/DELETE /api/v1/projects`

## AGENTS.md 加载器

**位置**：`internal/agents/`  
**文件**：`agents.go`

**2026-07 加载顺序（OR 策略，第一个命中的胜出）**：

1. `<root>/AGENTS.md` — 项目根（**主路径**，最高优先级）
2. `<root>/.p-chat/AGENTS.md` — 项目级 .p-chat（fallback，install 脚本同步位置）
3. `~/.p-chat/AGENTS.md` — 全局（项目级都没有时兜底）

段标题区分：
- 命中 #1 → `### Project (root)`
- 命中 #2 → `### Project (.p-chat)`
- 命中 #3 → `### Global`

API：
- `LoadAllWithRoot(root)` — 主入口
- `LoadAll()` — 等价于 `LoadAllWithRoot("")`（无 project 模式，CLI 启动用）
- `agentsSignatureWithRoot(root)` (agent.go) — 把 3 个路径的 mtime 都编入 sig，任何一个变化都失效 static-prompt cache

**与 Skill/Rule 的策略区别**：
- AGENTS.md：OR（项目级优先，单一来源，避免冲突指令）
- Skill / Rule：AND（项目级 + 全局级都加载，能力可叠加）

## Rules 规则监听

**位置**：`internal/rules/`  
**文件**：`rules.go`

- 加载 `~/.p-chat/rules/*.md` 和 `<root>/.p-chat/rules/*.md` 的规则文件
- 规则内容注入系统提示词
- **2026-07 项目根感知**：`LoadAllWithRoot(root)` / `LoadAll()`（向后兼容）
- `Watch(onChange, pollInterval, root)` — 监听 mtime 变化，**root 参数**指定项目级 dir
- 合并策略：全局 + 项目都加载，全局排前项目排后（IsGlobal 标记）

## Knowledge 知识检索

**位置**：`internal/knowledge/`  
**文件**：`index.go`, `embed_openai.go`, `embed_local.go`, `embedding.go`, `bits.go`

RAG (检索增强生成) 实现：
- 文本嵌入（OpenAI embedding 或本地模型）
- 向量索引和相似度搜索
- 知识分块存储

## Recall 记忆召回

**位置**：`internal/recall/`  
**文件**：`recall.go`

从历史对话中召回相关信息，增强 LLM 上下文。

## Paths 路径解析

**位置**：`internal/paths/`  
**文件**：`paths.go`、`devhome.go`

- 解析全局 P-Chat home 目录（`~/.p-chat/` 及其子目录）
- 跨平台路径处理（`filepath`）
- **dev/prod 隔离（`devhome.go`）**：`GlobalDir()` 解析顺序：
  1. `PCHAT_DATA_HOME` 环境变量（最高优先级，手动覆盖 **数据目录**）
  2. 二进制文件在 `bin/` 或 `dev-bin/` 子目录 → 用 `<parent>/.p-chat/`（隔离本地测试）
  3. 兜底：`~/.p-chat/`
- **`PCHAT_HOME` 不再影响数据目录**：它是 `install.ps1 -AddToPath` 写的安装根（在 PATH 里用作 `%PCHAT_HOME%`），以前和 `PCHAT_DATA_HOME` 混用导致"装在 `D:\develop\pchat` 的用户把记忆存到了 `D:\develop\pchat\memory\`"。详见 `internal/upgrade` 的 `stepV3toV4`（V3→V4 自动把安装目录下的 memory 迁到 `~/.p-chat/memory/`）
- `ResolveStrategy()` 返回当前选用的策略字符串，启动日志会打印它
- 每次调用重新读 env + `os.Executable()`（无缓存，测试用 `t.Setenv` 无需手动失效）
- 测试用 `SetExecutableForTest` / `SetHomeForTest` 注入
- `EnsureGlobal()` 在解析到的目录下创建所有子目录（skills / rules / prompts / memory / tools / knowledge / uploads）

## HTTP 客户端 (CLI)

**位置**：`internal/httpcli/`  
**文件**：`client.go`

CLI 使用的 HTTP + SSE 客户端，与 pchat-server 通信。

## Server 进程管理

**位置**：`internal/serverproc/`  
**文件**：`serverproc.go`, `detach_windows.go`, `detach_unix.go`

- `Start()` — 自动启动 pchat-server 子进程
- `Stop()` — 关闭子进程
- 跨平台进程分离（Windows `DETACHED_PROCESS` / Unix `setsid`）

## 路由约定

基础设施模块通常不需要独立修改，除非：
- 添加新系统功能时需要其支撑
- 配置格式变更需要相应调整
