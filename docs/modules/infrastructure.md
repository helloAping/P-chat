# 基础设施模块

> 包含多个小型工具模块，为其他核心模块提供支撑功能。

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
- `LoadAllWithRoot(root)` — 加载全局 + `<root>/.p-chat/skills` 的 skills
- `LoadAll()` — 旧接口，等价于 `LoadAllWithRoot("")`
- `Install(repoURL)` — 从 GitHub 安装
- `Delete(name)` — 卸载
- Skill 内容被注入到系统提示词
- 合并策略：全局 + 项目都加载，项目同名覆盖全局

## Style 风格管理

**位置**：`internal/style/`  
**文件**：`manager.go`

管理 LLM 人格风格。当前风格数据存储在 SQLite `styles` 表中：
- 内置风格：`cute` / `guofeng` / `tech`
- `style=off`：关闭风格 prompt 和风格记忆注入
- 用户自定义风格：通过 `styles` 表保存 `prompt` / `memory`
- 旧版 `prompts/identity`、`prompts/soul`、`prompts/style` 只作为升级导入来源

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

- `LoadAllWithRoot(root)` — 从指定项目根解析
- 加载顺序是 OR 策略：`<root>/AGENTS.md` → `<root>/.p-chat/AGENTS.md` → `~/.p-chat/AGENTS.md`
- 只取第一个命中的 AGENTS，避免多份指令互相冲突

## Rules 规则监听

**位置**：`internal/rules/`  
**文件**：`rules.go`

- 加载 `~/.p-chat/rules/*.md` 和 `<root>/.p-chat/rules/*.md`
- 规则内容注入系统提示词
- 支持项目级和全局规则叠加，项目规则排在全局规则之后

## Knowledge 知识检索

**位置**：`internal/knowledge/`  
**文件**：`index.go`, `indexer.go`, `wiki_store.go`, `hybrid.go`, `merge.go`, `query_plan.go`, `bits.go`

RAG (检索增强生成) 实现：
- Wiki/FTS5 三层索引
- 路径、标题、正文、关键词混合检索
- 多知识库合并重排与引用解释
- 增量扫描和删除文件清理

## Recall 记忆召回

**位置**：`internal/recall/`  
**文件**：`engine.go`

把知识库搜索结果转换为 Agent 可用上下文，负责查询分解、多库召回、去重和重排。

## Paths 路径解析

**位置**：`internal/paths/`  
**文件**：`paths.go`、`devhome.go`

- 数据目录解析顺序：`PCHAT_DATA_HOME` → `dev-bin/.p-chat/` → `~/.p-chat/`
- `PCHAT_HOME` 只表示安装根目录，用于 PATH，不作为数据目录
- 跨平台路径处理，全局目录、数据库、上传目录等

## Trace 端到端追踪

**位置**：`internal/trace/`
**文件**：`trace.go`

- 生成 `T-xxxxxxxx` trace id
- 通过 HTTP header、context、SSE event、工具调用和日志贯通
- GUI 错误气泡和顶栏可复制 trace id

## Version 与 Upgrade

**位置**：`internal/version/`、`internal/upgrade/`
**文件**：`version.go`, `steps.go`

- `VERSION` 是应用版本唯一真源
- `internal/version` 负责 ldflags / git hash / VERSION 文件回退
- `internal/upgrade` 负责用户数据目录结构升级，当前 `AppVersion` 为 `V8`

## Update / Repair / Export

**位置**：`internal/update/`、`internal/repair/`、`internal/export/`

- `update`：更新检查、下载和 release manifest 解析
- `repair`：本地数据或安装状态修复逻辑
- `export`：会话导出 HTML/PDF/text

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
