# 基础设施模块

> 包含多个小型工具模块，为其他核心模块提供支撑功能。

## Style 风格管理

**位置**：`internal/style/`  
**文件**：`manager.go`

管理 LLM 人格风格。当前风格数据存储在 SQLite `styles` 表中：

| 字段 | 含义 | 注入方式 |
|---|---|---|
| `id` / `label` | 风格 ID 与显示名 | GUI/CLI 展示 |
| `prompt` | 完整人格系统提示词 | staticPrompt 缓存 |
| `memory` | 用户维护的风格记忆 | 每轮动态追加 |
| `is_builtin` | 内置风格标记 | 内置风格只读 |

内置风格 `cute` / `guofeng` / `tech` 由 `internal/upgrade/prompts/*.md` embed 进二进制，并在升级步骤 V2→V3 seed 到 SQLite。旧版 `prompts/identity`、`prompts/soul`、`prompts/style` 只作为迁移导入来源，不再是当前运行时真源。

`styleMgr.GetSystemPrompt(s)` 读取 prompt 进入静态系统提示词；`styleMgr.GetMemory(s)` 读取 memory，在 `agent.go` 中动态注入 systemPrompt 末尾。`style=off` 不注入 prompt，也不注入 memory。

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
**文件**：`manager.go`（主入口）, `skill.go`（旧接口兼容）

Skill 是包含 `SKILL.md` 及可选 `scripts/`、`references/`、`assets/` 的完整目录包。
`Manager` 是 Agent、HTTP、CLI 和工具共用的唯一边界：

```go
type Manager interface {
    Catalog(context.Context, CatalogQuery) (Catalog, error)
    Load(context.Context, LoadRequest) (LoadedSkill, error)
    Apply(context.Context, ChangeRequest) (ChangeResult, error)
}
```

发现优先级从高到低：

1. `<project>/.p-chat/skills/<name>/`（项目托管）
2. `<project>/.agents/skills/<name>/`（Agent Skills 标准项目目录）
3. `~/.p-chat/skills/<name>/`，实际根由 `PCHAT_HOME` / `PCHAT_DATA_HOME` 解析（P-Chat 全局托管）
4. `~/.agents/skills/<name>/`（Agent Skills 标准用户目录）

同名 Skill 只选最高优先级版本，较低版本记录在 `shadowed[]`；目录名与
frontmatter `name` 不一致会产生诊断，但逻辑名称始终以 frontmatter 为准。

`SKILL.md` frontmatter 支持 `requires` 或 `metadata.requires`：

```yaml
---
name: docs-tool
description: 管理在线文档
metadata:
  requires:
    skills: [shared-auth]
    bins: [vendor-cli]
---
```

系统提示词只注入 `Catalog` 的名称、描述和依赖摘要；正文在显式激活或模型调用
`skill(action=load)` 时按需加载。加载按依赖拓扑顺序合成上下文，并校验依赖 Skill、
外部可执行文件和资源路径。每次 `Catalog/Load` 都重新扫描，所以外部 CLI 写入标准目录后，
新会话无需重启即可发现。

安装/导入由 `Apply` 完成：远程安装仅接受 HTTPS GitHub 仓库或目录 URL；本地导入接受
单个完整 Skill 包目录，或任意外部工具导出的 Skill 集合目录（每个直接子目录都是一个包）。
集合导入省略 `name` 时导入全部包；提供 `name` 时导入目标及其在该集合内声明的依赖。
P-Chat 核心不执行外部 CLI，也不为具体供应商维护专用适配器。

安装过程不执行包内脚本；发布使用 staging + rename，并限制整次导入的包数、文件数、总大小
和单文件大小，拒绝符号链接。单包和集合都先完整 staging，再按事务发布：任一包发布或验证
失败都会恢复全部旧包；发布/校验窗口由进程级读写锁隔离，`Catalog` / `Load` 不会看到半发布
状态。完成后必须再次 `Load` 验证；返回 `rolled_back=true` 表示已回滚，不能向用户声称安装
成功。

外部 CLI 或安装器应优先把完整包写入 `.agents/skills` 标准目录；若只能导出到私有目录，
则调用 `skill_manage(action=import, source_path=...)`。最后用 `skill(action=doctor)` 或
`skill(action=list)` 验证。host runtime prompt 会明确告知数据根、四类目录和通用导入契约。

兼容接口 `LoadAllWithRoot()` / `LoadAll()` 仍保留，但同样通过 Manager 发现；不得再新增
另一套安装、删除或合并逻辑。

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
**文件**：`index.go`, `indexer.go`, `wiki_store.go`, `hybrid.go`, `merge.go`, `query_plan.go`, `bits.go`

RAG (检索增强生成) 实现：
- Wiki/FTS5 三层索引
- 路径、标题、正文、关键词混合检索
- 多知识库合并重排与引用解释
- 增量扫描和删除文件清理

知识库细节以 [knowledge.md](knowledge.md) 为准，本文件只保留基础设施入口。

## Recall 记忆召回

**位置**：`internal/recall/`  
**文件**：`engine.go`

把知识库搜索结果转换为 Agent 可用上下文，负责查询分解、多库召回、去重和重排。

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
**文件**：`serverproc.go`, `ports.go`, `detach_windows.go`, `detach_unix.go`

**共享身份模块**：`runtimeprofile/`（独立 Go 1.23 module，供根模块和 Wails GUI 子模块共同引用）

- `Start()` — 自动启动 pchat-server 子进程；传递 profile/instance 身份并等待握手文件
- `Stop()` — 关闭子进程
- `Listen()` — server 直接绑定显式端口、端口范围或 `:0`，不经过 probe-close-rebind
- profile ID 由规范化 data home 派生；不同 data home 可共存，同目录 GUI 共享单实例身份
- `/api/v1/health` 与启动 announcement 必须同时匹配 profile ID 和 instance ID
- 跨平台进程分离（Windows `DETACHED_PROCESS` / Unix `setsid`）

详细需求和端口矩阵见 [`docs/plans/runtime-profile-coexistence.md`](../../docs/plans/runtime-profile-coexistence.md)。

## 路由约定

基础设施模块通常不需要独立修改，除非：
- 添加新系统功能时需要其支撑
- 配置格式变更需要相应调整
