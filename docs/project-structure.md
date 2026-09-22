# 项目结构总览

> 快照日期：2026-09-22。当前 `VERSION` 为 `1.0.13.beta`。
>
> 这份文档用于回答“这个仓库每个目录负责什么、改功能先看哪里、功能状态以哪里为准”。更细的模块维护入口见 `.agents/docs/INDEX.md`，用户入口见 `README.md`，完整图文流程见 `docs/p-chat-article-2026-09.md`。

## 1. 顶层目录

| 路径 | 类型 | 职责 | 维护提示 |
| --- | --- | --- | --- |
| `cmd/` | Go 入口 | CLI、HTTP server、Wails GUI、安装器、更新器等可执行入口 | 只放启动编排和平台壳逻辑，业务逻辑下沉到 `internal/` |
| `frontend/` | Vue SPA | GUI / 浏览器模式共用前端源码 | Wails 配置通过 `cmd/pchat-gui/wails.json` 指向这里 |
| `internal/` | Go 业务包 | Agent、LLM、server、工具、记忆、知识库、IM、浏览器控制等核心实现 | 新功能默认先在这里找归属模块 |
| `browser-extension/` | 浏览器扩展源码 | Chrome / Edge 扩展桥接真实浏览器 | 打包后复制到 `cmd/pchat-server/browser-extension.zip` 和 `bin/` |
| `configs/` | 默认配置模板 | 首次运行或迁移时参考的配置样例 | 用户运行时配置在数据目录，不在这里直接写密钥 |
| `scripts/` | 构建/打包脚本 | 前端同步、Wails 构建、安装包、更新包、冒烟测试 | Taskfile 多数任务调用这里的脚本 |
| `runtimeprofile/` | 共享 Go module | data home 派生运行 profile、进程 instance、server 启动握手 | 保持 Go 1.23 + 标准库，以便根模块和 Wails 子模块共同引用 |
| `docs/` | 用户/维护文档 | 功能指南、结构说明、历史计划、报告 | 当前功能状态以 `docs/feature-opportunities.md` 为准 |
| `.agents/` | Agent 协作规范 | canonical `AGENTS.md` 与模块维护文档 | `.opencode` / `.codex` / `.claude` 目录指向这里 |
| `.p-chat/` | 项目级运行时配置 | 本仓库作为项目运行时的本地配置、规则、技能 | 不提交密钥；全局用户数据默认在 `~/.p-chat/` |
| `web/` | 构建产物 | Vite 输出，供 `pchat-server` embed / 静态托管 | 由 `task build:frontend` 生成，不手写 |
| `bin/` | 构建产物 | release / 本地构建二进制和 zip | 由构建脚本生成，不作为源码入口 |
| `dev-bin/` | 开发运行产物 | `task build:dev` 输出和隔离数据目录 | 使用 `dev-bin/.p-chat/`，不污染正式 `~/.p-chat/` |
| `build/`、`tmp/`、`_tmp-*`、`.cache/` | 临时目录 | 构建缓存、测试缓存、临时输出 | 不作为功能入口 |
| `cmdpchat-server/`、`cmd/bin/` | 历史/生成残留 | 旧路径或构建输出残留 | 正式维护入口是 `cmd/pchat-server/` 和顶层 `bin/` |

## 2. 可执行入口

| 入口 | 输出 | 职责 |
| --- | --- | --- |
| `cmd/pchat/` | `pchat.exe` | CLI REPL 和只读命令入口；启动时可拉起同目录 server |
| `cmd/pchat-server/` | `pchat-server.exe` | Gin HTTP API、SSE、静态 web、browser WS、IM webhook |
| `cmd/pchat-gui/` | `pchat-gui.exe` | Wails 桌面壳；启动/托管 `pchat-server.exe` 子进程 |
| `cmd/pchat-installer/` | `pchat-setup-v*.exe` | Windows 单文件安装器，释放 GUI/server/CLI/updater/web |
| `cmd/pchat-updater/` | `pchat-updater.exe` | 自动更新时重启后替换文件 |
| `cmd/pchat-fix/` | 辅助工具 | 修复类临时/维护入口，使用前先读源码确认用途 |

## 3. 源码模块

### 3.1 核心对话链路

| 模块 | 位置 | 职责 | 必读文档 |
| --- | --- | --- | --- |
| Agent 循环 | `internal/agent/` | ReAct 轮次、工具派发、auto-continue、parts 累加、图片附件上下文 | `.agents/docs/agent.md` |
| LLM 适配 | `internal/llm/` | OpenAI 兼容 / Anthropic 协议、SSE parser、错误分类、token 估算 | `.agents/docs/llm.md` |
| HTTP Server | `internal/server/` | REST API、SSE 写循环、会话、上传、配置、browser/IM/MCP 路由 | `.agents/docs/server.md` |
| 持久化 | `internal/memory/` | SQLite 会话、消息、metadata、摘要、todo、subagent job、schema 迁移 | `.agents/docs/memory.md` |
| 工具系统 | `internal/tool/` | 内置工具、确认、问题、图片识别、PDF/DOCX、动态工具桥接 | `.agents/docs/tool.md` |
| 子代理 | `internal/subagent/` | `task` 工具、同步/异步子代理、工具隔离、后台 job | `.agents/docs/subagent.md` |

### 3.2 能力模块

| 模块 | 位置 | 职责 | 相关入口 |
| --- | --- | --- | --- |
| 知识库 | `internal/knowledge/` | Wiki/FTS5、三层索引、文件扫描、引用解释 | `.agents/docs/knowledge.md`, `docs/knowledge.md` |
| Recall | `internal/recall/` | 多库召回、查询分解、合并重排、注入 Agent 上下文 | `.agents/docs/knowledge.md` |
| 搜索 | `internal/search/` | `web_search` provider、配额、Tavily / OpenAI 兼容接口 | `.agents/docs/tool.md` |
| 浏览器控制 | `internal/browser/` + `browser-extension/` | 扩展连接、标签页、截图、页面操作、域名权限策略 | `README.md` 浏览器控制章节 |
| IM 桥接 | `internal/im/` | Gateway、Adapter、飞书、微信 QR/长轮询、出站切分 | `.agents/docs/im.md` |
| MCP | `internal/mcp/` | MCP server 管理、stdio/SSE transport、工具发现基础 | `.agents/docs/infrastructure.md` |
| 导出 | `internal/export/` | 会话导出 HTML/PDF/text 与清洗 | `internal/server/export.go` |
| 风格生成 | `internal/stylegen/` | 根据当前对话生成/优化人格风格 | `docs/plans/stylegen-plan.md` |
| 自动更新 | `internal/update/` | 更新检查、下载、manifest 解析 | `frontend/src/api/update.ts`, `internal/server/update_api.go` |
| 修复工具 | `internal/repair/` | 本地数据/安装修复逻辑 | `cmd/pchat-server/repair_cmd.go` |

### 3.3 支撑模块

| 模块 | 位置 | 职责 |
| --- | --- | --- |
| 配置 | `internal/config/` | `config.json` 加载、迁移、系统配置、IM/知识库/search 子配置 |
| CLI | `internal/cli/` | REPL、斜杠命令、HTTP+SSE 客户端交互 |
| HTTP CLI | `internal/httpcli/` | CLI 到 server 的 REST/SSE client |
| 项目 | `internal/project/` | 项目注册、项目级配置/AGENTS/rules/skills 根路径 |
| 路径 | `internal/paths/` | `PCHAT_DATA_HOME`、`dev-bin/.p-chat`、`~/.p-chat` 解析 |
| 沙箱 | `internal/sandbox/` | 命令和写文件风险决策 |
| Skill | `internal/skill/` | skill 安装、加载、项目级覆盖 |
| Rules | `internal/rules/` | `.p-chat/rules/*.md` 加载和监听 |
| AGENTS 加载 | `internal/agents/` | 项目/全局 `AGENTS.md` 优先级解析 |
| Style | `internal/style/` | 内置/自定义风格管理，当前风格数据在 SQLite |
| Trace | `internal/trace/` | 端到端 trace id 生成、注入和日志前缀 |
| ServerProc | `internal/serverproc/` | CLI 启停 server 子进程、原子端口绑定与身份校验；GUI 通过 `runtimeprofile/` 复用握手协议 |
| RotateLog | `internal/rotatelog/` | 日志按日期切割和保留策略 |
| Version | `internal/version/` | `VERSION` + ldflags + git hash 的统一版本字符串 |
| Upgrade | `internal/upgrade/` | 用户数据目录结构版本升级，当前 `AppVersion` 为 `V8` |

## 4. 前端结构

| 路径 | 职责 |
| --- | --- |
| `frontend/src/api/client.ts` | REST/SSE 类型和 API client |
| `frontend/src/api/sse.ts` | SSE frame parser |
| `frontend/src/api/update.ts` | 自动更新 API client |
| `frontend/src/stores/chat.ts` | Pinia 主 store：会话、消息、parts、流状态、todo、工具确认 |
| `frontend/src/components/` | 聊天窗口、输入区、设置、工具卡、子代理卡、知识库、IM、浏览器等组件 |
| `frontend/src/im/` | IM 设置页辅助逻辑，如 session source、微信 QR 状态 |
| `frontend/src/composables/` | Vue composable |
| `frontend/src/utils/` | Markdown cache、通知、格式化等通用工具 |
| `frontend/src/assets/` | 字体和静态资源 |

## 5. 关键运行流

### 5.1 聊天与工具流

```
GUI/CLI 输入
  -> POST /api/v1/sessions/:id/messages
  -> internal/server respondSSE
  -> internal/agent ChatWithTools
  -> internal/llm Stream
  -> tool dispatch / subagent / memory persist
  -> StreamEvent over SSE
  -> frontend/src/stores/chat.ts appendStreamEvent
  -> MessageBubble / ToolCallCard / SubAgentCard
```

### 5.2 知识库流

```
设置添加知识库目录
  -> /api/v1/knowledge/bases/:name/scan
  -> internal/knowledge 扫描文件并写入 Wiki/FTS5
  -> Agent 暴露 recall / wiki 工具
  -> internal/recall 查询分解 + 多库合并重排
  -> 结果带 citation/explanation 注入上下文
```

### 5.3 浏览器控制流

```
Chrome/Edge 扩展连接 /api/v1/browser/ws
  -> internal/browser Hub 管理连接和 tab
  -> browser_* 工具按域名策略确认
  -> 扩展执行导航/点击/截图
  -> 截图走 CallResultImage 双通道：给 LLM 看，也给 GUI 渲染
```

### 5.4 IM 桥接流

```
平台 webhook / adapter
  -> internal/im PlanInbound 做鉴权、mention、session、persona、工具白名单
  -> Gateway 调用 agent.ChatWithTools
  -> outbound renderer 按平台方言发送文本或流式更新
  -> GUI IMSettings 读取健康状态、配置和事件流
```

## 6. 功能状态口径

| 能力 | 当前状态 | 说明 |
| --- | --- | --- |
| 三端运行 | 已落地 | CLI、server、Wails GUI 共用后端主链路 |
| 结构化消息流 | 已落地 | text/thinking/tool/sub_agent parts、SSE seq、断线恢复 |
| Agent 长任务 | 已落地 | todo、auto-continue、回合超时续跑、LLM 阶梯式重试、熔断 |
| LLM 协议 | 已落地 | OpenAI 兼容 + Anthropic 原生，多模态边界由 Agent 控制 |
| 工具系统 | 已落地 / 持续增强 | 内置工具、动态 YAML 工具、dry-run、确认、工具结果缓存 |
| 子代理 | 已落地 / 持续增强 | 同步/异步 `task`、后台 job、父级授权隔离、部分结果返回 |
| 知识库 | 已落地 | 三层索引、混合检索、多库重排、引用解释、增量扫描 |
| 浏览器控制 | 已落地 | 扩展连接、15 个工具、多 tab、权限策略、E2E 测试 |
| IM 桥接 | 实验中 / 部分可用 | Gateway、飞书、微信 QR/长轮询、出站切分和 GUI 设置入口已有，跨平台完善仍在推进 |
| MCP | 基础可用 / 待增强 | 管理入口已有，统一工具确认和诊断体验仍需收口 |
| 导出与更新 | 已落地一部分 | 会话导出、版本检查、下载、安装器/更新器脚本已有 |
| 沙箱 | 基础可用 / 待增强 | 命令/写文件/浏览器域名策略已接入，Docker runner 和策略可视化待推进 |

更细 backlog 和“已落地/待做”判断见 `docs/feature-opportunities.md`。

## 7. 文档真源

| 文档 | 面向对象 | 维护内容 |
| --- | --- | --- |
| `README.md` | 用户 | 安装、启动、GUI 操作入口、常见问题、当前能力摘要 |
| `docs/p-chat-article-2026-09.md` | 新用户 | 从首次配置到项目、知识库、媒体、Skill/MCP、CLI 的完整图文流程 |
| `docs/project-structure.md` | 新维护者 | 目录结构、模块职责、关键运行流、功能状态口径 |
| `docs/feature-opportunities.md` | 排期/产品判断 | 已落地能力、可迭代任务、优先级 |
| `.agents/AGENTS.md` | Agent | 协作规范、启动检查、编码约束、关键文件速查 |
| `.agents/docs/*.md` | Agent / 维护者 | 每个模块的实现入口、修改指南和测试提示 |
| `docs/plans/*.md` | 设计追溯 | 历史方案和阶段性设计，不作为当前 backlog 真源 |
| `CHANGELOG.md` | 发布记录 | 已发布/待发布版本改动记录 |

## 8. 新增功能时的文档检查

1. 用户能直接操作的功能：更新 `README.md` 的入口、步骤、成功状态和排查方式。
2. 涉及模块边界或关键文件变化：更新 `.agents/docs/INDEX.md` 和对应模块文档。
3. 功能状态变化：更新 `docs/feature-opportunities.md`，已完成项移出 backlog 或标注已落地。
4. 目录、入口、运行流变化：更新本文件。
5. 版本、配置、数据目录、schema 变化：同时检查 `.agents/docs/versioning.md` 和 `.agents/docs/upgrade.md`。
