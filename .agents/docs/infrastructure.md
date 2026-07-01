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
- `LoadAll()` — 加载安装的 skills
- `Install(repoURL)` — 从 GitHub 安装
- `Delete(name)` — 卸载
- Skill 内容被注入到系统提示词

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

- 加载全局 `AGENTS.md` 和项目级 `<project>/AGENTS.md`
- 内容合并到系统提示词
- `LoadAllWithRoot(root)` — 从指定根目录加载

## Rules 规则监听

**位置**：`internal/rules/`  
**文件**：`rules.go`

- 监听 `.rules/` 目录的规则文件
- 规则内容注入系统提示词
- 支持项目级和全局规则

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
**文件**：`paths.go`

- `~/.p-chat/` 路径解析
- 跨平台路径处理
- 全局目录、数据库、上传目录等

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
