# 项目现状与可做事项

> 这份文档用于梳理“已落地能力 + 后续可迭代/可优化点”。
>
> 原则：
> - 已经在 GUI / CLI / server 中实现的能力，只放在“已落地”里，不再重复放入 backlog。
> - 仍可继续做的点按模块拆开，尽量写成能进入排期的方向。
> - 历史设计保留在 `docs/plans/*.md`，本文档只维护当前判断。
> - 本文档只做功能与逻辑梳理，不包含代码实现。

## 0. 当前快照

> 快照日期：2026-10-19（SEC-02 Provider 密钥脱敏已落地；其余 backlog 仍以 2026-09-26 梳理为主）。

| 文档 | 角色 | 使用方式 |
|---|---|---|
| `README.md` | 用户入口 + 快速状态摘要 | 面向安装、配置、GUI 操作、常见问题和“当前进度快照”。 |
| `docs/p-chat-article-2026-09.md` | 完整图文指南 | 面向新用户，串联首次配置、项目、附件、知识库、媒体、Skill/MCP 与 CLI。 |
| `docs/project-structure.md` | 项目结构与模块职责总览 | 面向维护者，回答目录归属、运行流、功能状态口径和新增功能文档检查。 |
| `docs/feature-opportunities.md` | 当前项目进度与 backlog 真源 | 判断功能是否已落地、下一步排期看这里。 |
| `CHANGELOG.md` | 历史开发记录 | 记录各轮交付和改动统计；其中旧阶段计划不自动等于当前 backlog。 |
| `docs/plans/*.md` | 历史设计方案 | 做实现背景和设计追溯，不作为默认待办清单。 |
| `.agents/docs/*.md` | agent 维护入口 | 改代码前按模块阅读，里面记录实现细节、关键文件和约束。 |

当前代码侧的主线能力已经覆盖：三端运行形态、dev/prod 运行隔离与动态端口、结构化 SSE 消息流、ReAct + 子 agent、auto-continue、会话分叉/编辑重发、并行工具调用、项目级上下文、知识库三层索引、浏览器控制、动态工具、trace id、上下文检查器、会话导出、自动更新基础能力，以及 MCP 集成。Runtime Trust 原型（instance capability + 扩展配对）代码与测试已齐但未接线生产。后续优先级应集中在“安全边界收口（§2.9）、可靠性修复（§2.14 REL 系列）、可解释性、外部集成完整度、发布/文档体验”，而不是重复排期已落地功能。

版本口径：仓库 `VERSION` 当前为 `1.0.14.beta`；正式分发版本以 `VERSION` 和 release tag 为准。

## 1. 已落地

### 1.1 Agent 与对话连续性
- `auto-continue`：todo 未完成时可自动续接 LLM，避免多步任务半路停止。
- `work_mode`：`coding` / `daily` 工作模式已接入，支持会话级与全局默认配置。
- `style=off`：GUI / CLI 均可关闭说话风格注入。
- `question` / `tool_confirm`：LLM 提问与沙箱确认可通过 GUI 弹窗承接。
- 重答历史：重答不会直接抹掉旧回答，支持历史版本保留与切换。
- 会话分叉 / 编辑重发：`forkSession` / `regenerate` / `rollbackTo` 已落地，支持从任意回合分叉或回滚后重发。
- 并行工具调用：`agent.go` 工具派发按 `ToolPolicy.CanRunInParallel` 决策，只读工具可并行，`question` / `write` / `exec` 为 exclusive。

### 1.2 流式消息与可观测性
- 断线恢复：SSE 流意外中断后可从服务端快照补齐 trailing assistant bubble。
- 工具结果折叠：长工具结果默认折叠，折叠状态持久化，支持复制。
- 上下文检查器：GUI 可查看 context window 利用率、per-message token 与压缩摘要。
- `trace id`：请求、SSE、工具调用链路已贯通，可在前端复制并用于日志定位。
- 代码高亮：常见语言代码块已支持 syntax highlight。
- LLM 重试累积器重置（REL-01）：attempt 级重试会重置 content/thinking/tool_args 累积器并回滚 usage 计数，同时向下游发 `stream_reset` 事件清除前端部分输出，修复中途失败重试导致的叠字/叠参数；同修复 stall watchdog 的 select-break 缺陷（此前 stall 重试会被 cancelStream 引发的取消错误吞掉，重试从未真正发生）。
- LLM raw chunk 日志门控（REL-03）：debug 日志默认只记 chunk 统计与截断后的解析错误，消息正文/思维链全文需 `PC_LLM_DEBUG=1` 才落盘。
- 搜索结果跳转高亮（SM-01）：侧栏全文搜索点击结果后切换会话、按需翻页加载目标消息、滚动居中并闪烁高亮（`[data-msg-id]` 锚点）。

### 1.3 工具、浏览器与扩展
- 浏览器控制：浏览器连接、扩展下载、工具注册、设置面板已实现。
- 浏览器连接诊断：浏览器设置 Tab 可查看 HTTP/WS 地址、连接数量、最近错误、tab 数、扩展版本、协议版本和最后活跃时间。
- 浏览器扩展更新提示：扩展握手会上报扩展版本和协议版本，GUI 可提示旧扩展重新下载/加载。
- 浏览器工具权限（BR-04）：`browser.require_confirm` + `allowed_hosts` / `blocked_hosts` / `sensitive_hosts`；高风险操作与敏感域走 `ToolConfirmModal`（显示目标页面 URL）。
- 浏览器控制 E2E（BR-05）：`internal/browser/e2e_test.go` 用模拟扩展覆盖连接、导航/点击/输入/截图、断线重连、策略拦截与 Manager 动态注册。
- 动态工具：`~/.p-chat/tools/*.yaml` 全局热加载、当前项目 `.p-chat/tools/*.yaml` 按会话加载，项目工具可覆盖全局同名工具。
- 动态工具加载诊断：YAML 解析/校验失败会暴露到工具列表抽屉，显示作用域、来源路径、错误原因和修改时间。
- 工具列表抽屉：GUI 可查看当前可用工具，并区分内置工具 / 全局自定义工具 / 项目自定义工具 / 加载失败项。
- 工具 dry-run：`exec_command` / `read_file` / `write_file` 支持 `dry_run: true`，GUI 可显示 dry-run 标识。
- `allow_eval_write` fail-closed（SEC-04）：`browser_evaluate` 的表达式会经写操作模式检测（fetch/XHR/postMessage/eval/DOM 变更/cookie/storage 等），`AllowEvalWrite=false` 时命中即 Block，配置语义与执行行为一致。

### 1.4 知识、项目与配置
- 知识库：本地/远程向量库、语义检索、目录扫描、对话级选择已实现。
- 知识库三层树：GUI 可查看知识库索引节点与内容块。
- 多知识库合并重排（KB-01）：跨库搜索会先分库召回，再归一化、去重、全局排序，结果携带来源库。
- 混合检索（KB-02）：FTS5 + 路径/文件名/标题/正文 LIKE 通过 RRF 融合，结果携带 `match_type` 命中原因。
- 查询改写与分解（KB-03）：复杂 query 会派生 2-5 个路径/符号/关键词子查询，结果保留命中的 `query`。
- 引用可解释性（KB-04）：搜索结果携带结构化 `citation` 与 `explanation`，说明来源库、路径、标题、query、命中类型与 score。
- 增量扫描优化（KB-05）：按文件 mtime 跳过未变文件、逐文件替换变更文件、清理删除文件，并在扫描状态暴露 changed/skipped/deleted/failed。
- 项目系统：支持多项目目录注册、项目级 AGENTS.md / rules / skills 注入。
- Provider / Model / Style 管理：GUI 设置面板已覆盖常见配置入口。
- CLI `/kb` 命令（CLI-01）：local REPL 启动时已挂载 `KBManager`，`/kb list|add|remove|scan` 全部可用；`/kb scan` 真实重建全部启用知识库的三层索引（此前只统计节点数，从未扫描）。HTTP 模式 `/kb` 仍走 server API，暂不支持 scan。

### 1.5 发布、导出与外部入口
- 会话导出：`internal/export` 支持 HTML/PDF/text 导出，server 暴露 `/api/v1/sessions/:id/export`。
- 自动更新：`internal/update`、`cmd/pchat-updater`、`frontend/src/api/update.ts` 和 `scripts/package-update-zip.ps1` 已形成检查/下载/替换的基础链路；SEC-05 客户端侧已强制 HTTPS、SHA-256 与 ed25519 发布者签名（发布站需上传带 `signature` 的 latest.json）。
- 浏览器扩展分发：`browser-extension/` 源码和 `scripts/package-browser-ext.ps1` 已接入 build 流程。
- 诊断接口：`/api/v1/diagnostics/*` 提供内存、配置、heap/goroutine 快照等排查入口。
- pprof 默认关闭（SEC-03）：`/debug/pprof*` 改为 opt-in，仅 `PC_PPROF=1` 时挂载，避免无鉴权堆快照默认暴露。

### 1.6 多环境运行隔离

- runtime profile：由规范化 data home 派生稳定身份，隔离 GUI 单实例锁、托盘窗口、非默认 WebView2 数据、配置、SQLite、日志和运行文件；dev 与正式版可同时打开。
- 动态端口：正式 GUI 依次尝试 `15150-15159`，全部占用时回退到系统临时端口；dev/test/CLI 直接使用原子分配的临时端口；独立 server 保持配置端口严格绑定。
- 启动身份校验：server 持有 listener 后原子发布启动公告，父进程同时校验 profile ID、每次启动重新生成的 instance ID 和 PID，再对 `/api/v1/health` 做同样的二次校验。
- 安全重建：开发脚本仅停止当前仓库 `dev-bin/` 下的进程，安装/卸载脚本仅处理目标安装目录中的进程，不会按进程名误结束其他环境。

### 1.7 安全与信任原型（未接线生产）

- Runtime Trust 原型：`internal/auth`（instance capability）+ `internal/browser/pairing.go`（bootstrap token / 扩展配对状态机）代码与约 29 个测试已齐，但 `internal/server` 尚未引用 `RequireCapability` / `NewPairer`，浏览器 WS 仍是连接即注册。生产接线见 SEC-01。
- Secret 脱敏契约（SEC-02）：Provider `GET/PATCH` 读侧只返回 `has_api_key`，`custom_headers` 仅保留键名；写侧 `clear_api_key` / 非空 `api_key` / 省略三态；设置页不再回显明文密钥。
- 动态工具权限字段（DT-04 部分）：YAML 已支持 `sandbox.exec/read/write`，`confirmTargetFor` 已读 `Sandbox.Exec`；GUI 风险展示与 read/write 按参数分流仍未做。

### 1.8 本地生命感

- 风格记忆：`remember` 不单独建事实表。用户在确认框里选择已有风格（含内置人格）后，事实追加到该风格的 `styles.memory`；也可以填一个新名字，据此新建风格并把事实作为它的记忆。拒绝或未选择都不写。当前会话仍通过原来的风格记忆注入读到它。
- 风格对照：输入区「换种说法」对当前句子用另外两种人格各做一次无工具补全，结果在检查器里。点「采用」才写成一条普通助手消息。
- 回合回放：文件和命令摘要写入 `turn_changes`，重启后气泡仍能展开。字节快照仍只留在内存日记的最近 5 轮里，撤销按钮仍只作用于最近一轮。
- 晨报和全局记忆卡片已移除。曾经生成过的 `user_memories` / `morning_digests` 表在升级到 V25 时删除。

## 2. 可迭代任务明细

### 2.1 知识库检索质量

| 编号 | 任务点 | 功能描述 | 涉及点 | 修改点 | 逻辑梳理 | 验收点 |
|---|---|---|---|---|---|---|
| KB-01 | 多知识库结果合并再排序（已落地） | 用户选择“全部”或多个知识库时，统一比较所有召回结果，减少单库排序造成的噪音。 | `internal/recall`、`internal/knowledge`、`internal/server/knowledge_api.go`、`frontend/src/api/client.ts`、知识库选择器。 | 搜索返回结构需携带 base/store/source 信息；召回层增加跨库归一化评分；GUI 展示来源库。 | 先按每个库召回候选，再统一归一化 score，按相关性、来源权重、去重结果重排，最后注入 agent 上下文。 | 选择“全部”时结果按全局相关性排序；同一文件/片段不会重复出现；GUI 能看到结果来自哪个知识库。 |
| KB-02 | 混合检索（已落地） | 在向量检索外补关键词、文件路径、标题匹配，提高代码仓库与精确术语场景命中率。 | `internal/knowledge/indexer.go`、`wiki_store.go`、`internal/recall/engine.go`、SQLite 索引、`docs/knowledge.md`。 | 增加 lexical index 或查询 SQL；召回结果增加命中类型；融合向量分与关键词分。 | 对用户 query 同时跑向量召回和关键词召回，按 RRF 或加权策略融合，并保留命中原因。 | 搜索函数名、配置 key、文件名时能命中精确结果；语义问题仍能保留向量召回优势。 |
| KB-03 | 查询改写与分解（已落地） | 对复杂问题自动拆成多个检索 query，提升长问题、跨文件问题的召回覆盖。 | `internal/agent` prompt 拼装、`internal/recall`、LLM 调用层、搜索 API。 | 在 recall 前增加 query planner；结果合并去重；记录原始 query 与派生 query。 | LLM 或轻量规则把用户问题拆成 2-5 个 query，分别检索后合并，最终只把高质量片段注入上下文。 | 复杂问题能召回多个相关文件；GUI/日志能追踪派生 query；不会明显增加无关结果。 |
| KB-04 | 引用可解释性（已落地） | GUI 展示“为什么召回这段内容”，包括 query、相似度、路径、片段位置。 | `internal/knowledge` result model、`internal/server/knowledge_api.go`、`frontend/src/components/AppSettingsModal.vue`、`ToolCallCard.vue`。 | 扩展搜索结果字段；工具结果卡片增加来源和命中解释；README/FAQ 补说明。 | 召回结果从“纯文本片段”升级为“片段 + 元信息 + 命中原因”，前端按信息密度折叠展示。 | 用户能从工具卡片或知识库搜索结果里定位文件、章节、命中原因。 |
| KB-05 | 增量扫描优化（已落地） | 文件变更后只重建受影响节点，并在 GUI 显示失败文件、跳过原因和重试入口。 | `internal/knowledge/indexer.go`、`wiki_store.go`、`internal/server/knowledge_api.go`、`frontend/src/components/AppSettingsModal.vue`。 | 记录文件 hash/mtime；扫描任务增加 changed/failed/skipped 统计；GUI 扫描状态细化。 | 扫描前比对文件状态，只处理新增/变更/删除文件，失败项入库或暂存，供 GUI 展示和重试。 | 二次扫描明显更快；失败文件可见；单个文件修复后可重试，不必全量重建。 |

### 2.2 浏览器控制强化

| 编号 | 任务点 | 功能描述 | 涉及点 | 修改点 | 逻辑梳理 | 验收点 |
|---|---|---|---|---|---|---|
| BR-01 | 连接诊断页（已落地） | GUI 显示扩展版本、server 地址、最后心跳、最近错误和修复建议。 | `internal/browser/hub.go`、`manager.go`、`internal/server/browser_handler.go`、浏览器设置 Tab。 | 浏览器状态 API 增加 version/heartbeat/error；GUI 增加诊断区域。 | 扩展连接后定期上报状态，server 保存最近状态，GUI 轮询或刷新展示，并将错误映射为可读建议。 | 用户能判断未连接原因是地址错误、扩展未启用、版本不匹配还是心跳超时。 |
| BR-02 | 扩展更新流程（已落地） | 协议或工具能力变化时，提示重新下载/升级浏览器扩展。 | `internal/browser/protocol.go`、扩展包生成脚本、浏览器设置 Tab、README FAQ。 | 增加 protocol_version；server 比对扩展版本；GUI 显示升级提示与下载入口。 | 扩展连接时带版本，server 返回兼容性状态；不兼容时仍显示连接，但提示升级。 | 旧扩展连接后能看到明确升级提示；升级后提示消失。 |
| BR-03 | 多标签页管理（已落地） | GUI 显示已连接 tab，允许指定当前控制目标，避免工具调用落到错误页面。 | `internal/browser/hub.go`、`tools.go`、`browser_handler.go`、前端浏览器 Tab、`browser_*` 工具参数。 | 状态 API 增加 tab 列表；会话或全局保存 active tab；工具调用读取目标 tab。 | 扩展上报 tabs，用户在 GUI 选择目标，agent 工具默认使用 active tab，也允许工具参数显式覆盖。 | 多个页面打开时可切换控制目标；导航、点击、截图作用于选中的 tab。 |
| BR-04 | 浏览器工具权限收口（已落地） | 按域名或会话控制 `browser_*` 权限，敏感页面操作前确认。 | `internal/browser/policy.go`、`tools.go`、`internal/tool/confirm.go`、`ToolConfirmModal.vue`、`BrowserConfig`。 | 为浏览器工具接入 policy/confirm；增加域名规则；确认弹窗显示页面 URL 和操作。 | 浏览器工具执行前提取目标 URL、动作类型和风险等级，交给策略决策，必要时走确认事件。 | 敏感域名或表单输入会触发确认；普通导航可按策略自动通过；确认信息足够用户判断。 |
| BR-05 | 浏览器控制 E2E 回归（已落地） | 覆盖连接、导航、点击、输入、截图、断线重连。 | `internal/browser/*_test.go`、前端测试或 Playwright、扩展测试夹具、CI 脚本。 | 增加可控测试页面；模拟扩展连接；记录稳定性测试步骤。 | 用本地测试页面驱动 browser hub 和工具调用，验证协议、状态和工具结果一致。 | 核心浏览器工具有自动化覆盖；断线重连有回归用例；失败时能定位到协议/工具/GUI 层。 |

### 2.3 动态工具安全与可维护性

| 编号 | 任务点 | 功能描述 | 涉及点 | 修改点 | 逻辑梳理 | 验收点 |
|---|---|---|---|---|---|---|
| DT-01 | YAML schema 校验（已落地） | 保存动态工具后给出字段错误、缺少参数、命令模板风险提示。 | `internal/tool/dynamic/parse.go`、`dynamic.go`、`watcher.go`、工具列表抽屉、CLI `/tools`。 | 定义 schema 与错误码；加载失败结果暴露给 API；GUI 显示具体行/字段。 | watcher 发现变更后先 parse/validate，再注册；失败不污染当前可用工具，并保存诊断信息。 | 写错 YAML 后旧工具仍可用；GUI 能看到失败原因；修复保存后自动恢复。 |
| DT-02 | 工具试运行面板（已落地） | 自定义工具提供参数表单、dry-run、最近一次结果，降低调试成本。 | `internal/tool/dynamic`、`internal/server` tools API、`ToolListDrawer.vue`、`ToolCallCard.vue`。 | API 支持 tool metadata/参数 schema；GUI 增加试运行区域；dry-run 结果统一渲染。 | 从 YAML 参数定义生成表单，用户填参后先 dry-run，再可选择真实执行，结果复用工具卡片展示。 | 用户不进入聊天也能验证自定义工具；dry-run 明确标识；最近一次错误可复看。 |
| DT-03 | 项目级工具目录（已落地） | 除 `~/.p-chat/tools` 外支持 `.p-chat/tools`，并标记来源与覆盖关系。 | `internal/project`、`internal/paths`、`internal/tool/dynamic/watcher.go`、工具注册表、GUI 工具列表。 | watcher 支持多目录；工具 metadata 增加 scope/source；处理同名覆盖策略。 | 启动时加载全局工具和当前项目工具；项目工具可覆盖或补充全局工具，GUI 明确显示作用域。 | 切换项目后工具列表随项目变化；同名工具规则可预测；离开项目后不会泄漏项目工具。 |
| DT-04 | 动态工具权限分级（部分落地） | 为每个动态工具增加 sandbox/confirm 策略，避免共享同一审批强度。 | `internal/tool/dynamic`、`internal/sandbox`、`internal/tool`、`ToolConfirmModal.vue`。 | YAML 增加 permission/sandbox 字段；工具执行前进入统一决策表；GUI 显示风险等级。 | 动态工具声明读/写/执行/网络等能力，运行前结合会话权限和工具策略决定自动通过或确认。 | 高风险动态工具会触发确认；低风险查询工具可按策略自动执行；工具卡片显示权限来源。**现状**：YAML `sandbox.exec/read/write` 与 exec 确认已接线（`parse.go`、`confirm_target.go:160`），剩余为 GUI 风险展示与 read/write 按参数分流。 |
| DT-05 | 版本与禁用机制 | 支持禁用某个动态工具、查看加载失败原因、回退到上一版 YAML。 | `internal/tool/dynamic/watcher.go`、工具元数据存储、`ToolListDrawer.vue`、配置文件。 | 记录 enabled 状态、版本 hash、最近成功版本；GUI 增加禁用/恢复入口。 | 每次成功加载保存快照元信息，失败时保留上一版；用户可临时禁用工具，不删除源文件。 | 禁用后工具不再进入 LLM 可用列表；加载失败可回退；恢复后 watcher 正常接管。 |

### 2.4 Agent 任务执行体验

| 编号 | 任务点 | 功能描述 | 涉及点 | 修改点 | 逻辑梳理 | 验收点 |
|---|---|---|---|---|---|---|
| AG-01 | Todo 状态透明化 | GUI 展示 auto-continue 次数、下一次续接原因和最终停止原因。 | `internal/agent`、SSE `session_status/phase`、`frontend/src/stores/chat.ts`、`TodoPanel.vue`。 | 后端事件增加 continuation metadata；前端状态栏/TodoPanel 展示。 | 每次自动续接前后发出原因、次数、剩余上限，完成或停止时写清停止条件。 | 用户能知道 LLM 为什么继续、继续了几次、为什么最终停下。 |
| AG-02 | 子 agent 汇总增强 | 父消息中增加子 agent 结论摘要、耗时、失败原因和可重试入口。 | `internal/subagent`、`internal/agent`、`SubAgentCard.vue`、`MessageBubble.vue`。 | 子 agent 完成事件携带 summary/error detail；前端卡片增加 summary 区和 retry 动作入口。 | 子 agent 内部完成后产出简短摘要，父级只接收收口事件，GUI 把摘要和原始 parts 分层展示。 | 多个子 agent 并行时能快速看结论；失败卡片有原因；可针对失败任务重新发起。 |
| AG-03 | 问题弹窗历史 | 保留 LLM 向用户提问的历史，便于回看确认依据。 | `internal/server` question event、`memory`、`QuestionModal.vue`、`QuestionTable.vue`、会话消息模型。 | question/answer 入库或绑定消息 metadata；GUI 增加历史入口。 | LLM 提问时生成 question id，用户回答后将问答与会话消息关联，后续可在消息或侧栏查看。 | 刷新后问题历史不丢；能看到问题、选项、用户回答和时间。 |
| AG-04 | 工作模式错配提示 | 当前模式与用户任务类型明显不一致时，给出轻量提示或建议切换。 | `internal/agent` prompt、`work_mode` 配置、`InputArea.vue`、`frontend/stores/chat.ts`。 | 增加任务类型判断规则；GUI 提示不阻塞发送；可一键切换本会话模式。 | 发送前或 LLM 回合初期判断任务意图，与当前 `coding/daily` 比对，只在高置信错配时提示。 | 日常写作不被 coding 模式过度工具化；代码任务在 daily 模式下有明确切换提示。 |

### 2.5 前端信息架构与操作流

| 编号 | 任务点 | 功能描述 | 涉及点 | 修改点 | 逻辑梳理 | 验收点 |
|---|---|---|---|---|---|---|
| UX-01 | 错误恢复中心 | 集中展示最近错误、trace id、重试按钮、日志位置和用户可执行操作。 | `frontend/src/stores/chat.ts`、`TopBar.vue`、`MessageBubble.vue`、`internal/trace`、server error event。 | 增加 error center drawer；错误事件归档；README FAQ 补报错流程。 | 所有 error event 标准化进入错误中心，按 trace/session/time 聚合，给出复制、重试、查看日志指引。 | 用户不用翻消息也能找到最近错误；trace id 一键复制；可看到下一步处理建议。 |
| UX-02 | 流式恢复细化 | 补齐恢复过程中的进度、去重说明和失败 fallback。 | `frontend/src/api/client.ts`、`stores/chat.ts`、RecoveryBanner、server snapshot API。 | 恢复状态从单一 banner 扩展为 recovering/recovered/failed；增加去重统计。 | SSE drop 后进入恢复状态，拉取 snapshot，按 fingerprint 合并，成功/失败都清晰反馈。 | 网络中断后用户能看到恢复中；恢复失败不会留下误导性的半截状态。 |
| UX-03 | 工具卡片信息密度优化 | 给长参数、长结果、二进制/附件结果提供更合适的折叠和预览。 | `ToolCallCard.vue`、`frontend/style.css`、SSE tool event、工具 result metadata。 | 增加参数折叠、结果类型识别、复制/下载入口；统一 tool card spacing。 | 按内容类型选择渲染策略：文本可折叠，JSON 格式化，文件/图片/二进制显示摘要和操作按钮。 | 长工具调用不会撑爆页面；用户能快速复制关键结果；短结果保持直接可读。 |
| UX-04 | 上下文检查器增强 | 增加“可删除/可压缩的高 token 消息”建议，辅助用户管理上下文。 | `ContextInspectorDrawer.vue`、`internal/server` context API、`memory` 压缩逻辑。 | API 返回 message risk/suggestion；GUI 增加建议区；可能需要消息操作入口。 | 根据 token 占比、消息角色、是否已有摘要判断可优化项，给用户明确建议但不自动删除。 | 用户能看到哪些消息占用最多上下文；建议不会破坏历史；压缩前后利用率变化可见。 |
| UX-05 | GUI 操作引导覆盖 | 为浏览器控制、知识库、动态工具、MCP、沙箱权限补完整 README FAQ 流程。 | `README.md`、`.agents/docs/INDEX.md`、`.agents/docs/frontend.md`、各设置 Tab。 | 每个 GUI 功能写“入口、操作步骤、成功状态、失败排查”；索引指向 README。 | 新增功能完成时同步更新 README FAQ，agent 回答用户操作问题时只引用同一入口。 | 用户能按 README 完成 GUI 操作；agent 文档不再散落重复步骤。 |

### 2.6 MCP 与外部集成

| 编号 | 任务点 | 功能描述 | 涉及点 | 修改点 | 逻辑梳理 | 验收点 |
|---|---|---|---|---|---|---|
| MCP-01 | MCP 工具协议完整集成 | 补齐连接管理、工具发现、调用确认、错误展示，让 MCP 工具进入统一工具体系。 | `internal/mcp`、`internal/server/mcp.go`、`internal/tool`、`internal/agent`、MCP 设置 Tab。 | MCP tool 转换为统一 tool schema；工具调用走相同事件和确认链路；错误标准化。 | server 管理 MCP 连接，发现 tools 后注册到 tool registry，agent 调用时由 MCP client 执行并返回标准 tool event。 | MCP 工具能在工具列表出现；调用过程有 tool card；失败能显示 server/tool/error detail。 |
| MCP-02 | MCP GUI 配置体验 | 提供 server 健康检查、stdio/http 类型提示、环境变量缺失提示和连接日志。 | `frontend/src/api/client.ts` MCP API、设置面板 MCP Tab、`internal/mcp/manager.go`。 | GUI 增加测试连接、重启、日志摘要；后端返回状态与诊断。 | 用户配置 MCP server 后先健康检查，server 记录启动/连接/握手/工具发现日志，GUI 以可读方式展示。 | 配错命令或环境变量时能看到明确原因；重启 server 后状态刷新。 |
| MCP-03 | MCP 工具权限 | 将 MCP 工具纳入 sandbox / confirm 体系，避免外部工具绕过审批模型。 | `internal/mcp`、`internal/tool`、`internal/sandbox`、`ToolConfirmModal.vue`。 | MCP 工具 metadata 增加风险能力；执行前统一走 sandbox decision；确认弹窗显示 MCP 来源。 | MCP 工具注册时声明读写/网络/命令风险，调用前与当前会话权限合并判断。 | 高风险 MCP 工具触发确认；用户能看到工具来自哪个 MCP server。 |

### 2.7 沙箱与运行环境

| 编号 | 任务点 | 功能描述 | 涉及点 | 修改点 | 逻辑梳理 | 验收点 |
|---|---|---|---|---|---|---|
| SB-01 | Docker 代码沙箱 | 为高风险命令执行提供隔离环境，降低本机文件和环境变量暴露风险。 | `internal/sandbox`、`internal/tool` exec 工具、配置管理、Taskfile、README。 | 增加 sandbox backend 配置；exec 工具支持 docker runner；路径挂载和网络策略可配置。 | 用户选择 Docker 沙箱后，命令在容器内执行，只挂载允许目录，并按策略控制网络和环境变量。 | 高风险命令不直接跑在宿主机；输出仍回到 tool card；无 Docker 时有明确降级提示。 |
| SB-02 | 沙箱策略可视化 | GUI 解释当前权限等级会允许/拦截哪些操作。 | `internal/sandbox` decision table、设置系统 Tab、`ToolConfirmModal.vue`、README FAQ。 | API 暴露当前策略摘要；GUI 增加权限说明和示例；确认框展示命中规则。 | 用户切换权限等级时看到影响范围，工具被拦截时看到具体规则。 | 用户能理解 ask/auto/full 差异；误拦截或误放行更容易排查。 |
| SB-03 | 危险命令解释 | 被拦截时返回可读原因、命中的规则和建议替代操作。 | `internal/sandbox/sandbox.go`、`internal/tool`、SSE error/tool event、`ToolCallCard.vue`。 | sandbox decision 返回 rule id/reason/suggestion；前端显示结构化拦截原因。 | 工具执行前命中危险规则时，后端不只返回 forbidden，还返回为什么危险和如何改写。 | 用户能看到被拦截命令、命中规则和替代建议；日志可追踪 rule id。 |
| SB-04 | 跨平台一致性测试 | 补 Windows / Linux / macOS 文件路径、shell、权限差异测试。 | `internal/sandbox/*_test.go`、`internal/tool` tests、CI、AGENTS 构建说明。 | 增加路径分类、shell 命令、权限规则测试矩阵；文档记录平台差异。 | 用表格用例覆盖不同 OS 的路径、环境变量、shell 内置命令和危险模式。 | Windows/Linux/macOS 下同类风险结果一致；平台特殊行为有文档说明。 |

### 2.8 文档与发布体验

| 编号 | 任务点 | 功能描述 | 涉及点 | 修改点 | 逻辑梳理 | 验收点 |
|---|---|---|---|---|---|---|
| DOC-01 | README FAQ 持续补齐 | 新增 GUI 功能必须写清入口、步骤、成功状态、失败排查。 | `README.md`、`.agents/docs/INDEX.md`、`.agents/docs/frontend.md`。 | README 增加 FAQ 模板；agent 文档要求新增 GUI 功能同步补文档。 | 每个面向用户的 GUI 功能，都在 README 保留唯一操作说明，模块文档只放维护入口。 | 用户问“怎么用”时能直接从 README 找到答案；agent 不需要猜操作路径。 |
| DOC-02 | 功能状态表 | 标记 `已实现 / 实验中 / 计划中 / 暂缓`，减少历史规划造成的误解。 | `README.md`、本文档、`docs/plans/*.md`。 | 增加状态字段；已完成计划从 backlog 移到已落地；暂缓项写原因。 | 当前文档维护最新状态，历史计划只作参考，不再作为默认待办来源。 | README 路线图和本文档状态一致；已实现功能不会重复排期。 |
| DOC-03 | 版本升级说明 | 整理“需要重启 GUI / server”的场景，形成发布检查清单。 | `README.md`、`.agents/docs/versioning.md`、`.agents/docs/upgrade.md`、打包脚本。 | 发布文档增加升级步骤、重启条件、兼容性注意事项。 | 每次版本发布前检查配置迁移、server/gui 重启、扩展升级、数据库 schema。 | 用户升级后知道是否需要重启；发布遗漏减少。 |
| DOC-04 | 用户问题模板 | 为报错反馈提供 trace id、日志位置、复现步骤模板。 | `README.md` FAQ、错误恢复中心、`internal/trace`、日志文档。 | README 增加反馈模板；GUI 错误中心一键复制诊断信息。 | 报错时统一收集 trace id、时间、会话、操作步骤、日志位置，减少来回追问。 | 用户能复制一段完整反馈信息；开发者能按 trace id 定位日志。 |
| DOC-05 | 项目结构总览（已落地） | 用单页文档梳理顶层目录、源码模块、可执行入口、运行流和功能状态口径。 | `docs/project-structure.md`、`README.md`、`.agents/docs/INDEX.md`、`docs/modules/INDEX.md`。 | 新增结构文档；README 和索引指向该文档。 | 让新维护者先按“目录 → 模块 → 运行流 → 文档真源”建立项目地图，减少从历史 plans 里反推现状。 | 新增或调整目录/功能时有明确同步位置；README 和 agent 索引都能跳到结构总览。 |
| UPD-01（客户端已落地；发布站待接入） | 可选预览版本更新 | 在「关于 P-Chat」勾选“接收预览版本（Beta）”后，可检查、下载并安装版本号含 `.beta` 的更新；默认仅接收稳定版。 | `SessionSidebar.vue`、`frontend/src/api/update.ts`、`internal/server/update_api.go`、`internal/update/update.go`、发布站 `latest.json`。 | 勾选状态本地持久化；检查与下载传递同一渠道选择；客户端校验版本后缀与 `channel`，切换渠道时清除旧检查/下载状态。发布站仍需按渠道返回候选版本。 | 未勾选时只比较稳定版本；勾选后由发布站从稳定版与 beta 中选择版本号最高的可用版本；下载请求携带界面展示的 `expected_version`，在同一渠道重新检查；版本变化时拒绝下载并刷新提示，产物仍需校验哈希。单靠客户端无法发现未返回的 beta。 | 客户端默认不提示 beta；勾选后接受并下载更新的 `.beta`；取消勾选后 beta 不再提示；检查和下载结果版本一致。发布站接入 `include_prerelease=1` 后验证真实 Beta 可被发现。 |

### 2.9 安全与信任收口

> 来源：2026-09-26 全量代码梳理。Runtime Trust 原型与测试已齐（见 §1.7），本节聚焦“接线 + 收口”。

| 编号 | 任务点 | 功能描述 | 涉及点 | 修改点 | 逻辑梳理 | 验收点 |
|---|---|---|---|---|---|---|
| SEC-01 | Runtime Trust 生产接线 | 把 instance capability / 扩展配对接入生产路由，关闭“连接即信任”。 | `internal/auth/capability.go`、`internal/browser/pairing.go`、`internal/server/browser_handler.go:105-123`、`docs/plans/runtime-trust-decision-map.md` #7。 | spawn 下发 `PCHAT_INSTANCE_CAPABILITY`；路由挂 `RequireCapability`/`HostAllowlist`；Pairer 接入 `BrowserWebSocket` + GUI 批准 + 扩展 JS。 | 按 decision-map #7 七片落地；配对前 WS 不注册工具；capability 用常数时间比较。 | 无 capability 的 HTTP/WS 请求被拒；未配对扩展不出现在工具列表；GUI 可批准/撤销配对。 |
| SEC-02 | Secret 脱敏契约（已落地） | Provider 配置读侧不再回传明文密钥。 | `internal/server/provider_api.go`、`config_api.go`；模板参照 `search_api.go` 的 `has_key` + `clear_api_key`。 | 读侧统一 `has_*` 布尔；写侧三态 patch（不改/清空/设置）；前端设置面板改为四态 UX。 | 任意能访问本地端口的客户端不应读到完整密钥；存储仍明文（OS credential store 另开迁移）。 | 列表/详情接口无密钥明文；前端编辑不回显旧 key；清空与修改路径可用。 |
| SEC-03 | pprof 默认关闭（已落地） | 诊断端点改为 opt-in。 | `internal/server/monitor.go:41-49`、`server.go:465-468`。 | 默认关，`PC_PPROF=1` 才挂 `/debug/pprof*`；SEC-01 后归入管理级凭据保护。 | 当前逻辑仅 `PC_PPROF=0` 才关，与路线图相反；无鉴权堆快照可能残留密钥。 | 默认访问 pprof 404；显式开启后可用。 |
| SEC-04 | `allow_eval_write` fail-closed（已落地） | 配置开关真正拦截 `browser_evaluate`。 | `internal/browser/tools.go:364-367`（当前仅 `_ = cfg.AllowEvalWrite` 软注释）。 | `AllowEvalWrite=false` 时 Block 或强制 Confirm；补 policy 单测。 | 配置语义与执行行为对齐，避免“用户以为关了实际没关”。 | 关闭后 evaluate 被拦或触发确认；单测覆盖两种模式。 |
| SEC-05 | 自动更新防伪（客户端已落地；发布站需签包） | 元数据/产物强制 HTTPS + 发布者签名。 | `internal/update`、`cmd/pchat-updater`、`cmd/pchat-updatesign`、`scripts/package-update-zip.ps1`。 | 默认 latest URL 改 HTTPS；非 loopback 拒绝 http；artifact 需 `signature`（ed25519）；updater 拒绝空 `--expected-sha256`（仅 `--allow-insecure`）。 | SHA-256 防内容损坏，签名防假冒发布者；发布站上传带 `signature` 的 latest.json。 | 篡改的 latest.json/zip 被拒绝；打包脚本在设置 `PCHAT_UPDATE_SIGNING_KEY` 时写入签名。 |
| SEC-06 | Windows 危险命令规则 | 默认危险模式覆盖 Windows shell。 | `internal/config/config.go:1490-1510`（`defaultDangerousPatterns` 几乎全 Unix）、`defaultWriteProtectedPaths`。 | 按 shell（cmd/pwsh）分层追加 `Remove-Item -Recurse`、`Format-Volume`、`del /s /q`、`iex (iwr ...)`、`reg delete` 等规则。 | 默认 shell 是 Windows，Unix 规则对 PowerShell/cmd 破坏面覆盖弱；与 SB-04 测试矩阵一起验收。 | Windows 高危命令触发拦截/确认；规则命中原因可读。 |
| SEC-07 | 配对凭据与配置文件硬化 | pairing token 存哈希 + 常数时间比较；`config.json` 0600。 | `internal/browser/pairing.go:145-149`（`rec.Token == token`）、`internal/config/config.go:1323-1354`（原子写但无 Chmod）。 | token 存 SHA-256，校验改 `subtle.ConstantTimeCompare`；`writeConfigJSON` 落盘 0600 并启动时纠权限。 | 明文 token + 0644 配置在多用户机/备份泄露时可读；OS credential store 迁移另列。 | pairing.json/config.json 非所有者可读；错误 token 不泄漏时序。 |

### 2.10 Agent / LLM 能力增强

| 编号 | 任务点 | 功能描述 | 涉及点 | 修改点 | 逻辑梳理 | 验收点 |
|---|---|---|---|---|---|---|
| AE-01 | 流式工具参数上屏 | 工具参数生成期间实时展示进度。 | `internal/agent/agent.go:2947-2960`（只累加 delta）、`:3281-3287`（开始时才发 200 字截断）、`internal/llm/client.go:46-56`（LLM 层已支持增量）。 | 转发 `tool_args_delta`（按 tool_id/index）；前端 ToolCard 实时拼参数；完整参数仍在 start/ok 落盘。 | 长 `write_file`/`edit_file` 参数生成期间 UI 无反馈像卡住；LLM 层增量能力已具备，只差转发。 | 参数流式可见；终态参数与持久化一致；多工具并行不串台。 |
| AE-02 | 供应商原生 Prompt Cache 断点 | Anthropic `cache_control` 等显式缓存标记。 | `internal/llm/anthropic_adapter.go`（无 cache_control）、`internal/agent/prompt.go:7-8`（本地前缀稳定已做）、`cache_usage.go`（仅解析）。 | 静态 system / 工具 schema / 稳定历史前缀打断点；与现有 signature 失效规则对齐。 | 当前依赖供应商隐式前缀缓存，长会话成本/延迟不稳定。 | Claude 长会话 cache 命中率可见提升；失效场景不错缓存。 |
| AE-03 | Token / Cache 用量上 UI | Done 事件与 Inspector 展示 cache 命中率与轮次明细。 | `internal/agent/agent.go:2933-2935`（仅 log）、`usage.go:4-18`、`frontend/src/api/client.ts`、上下文检查器。 | Done/phase 事件带 `cache_hit/miss`、按 round 累计；Inspector 展示 `hit/(hit+miss)`。 | 解析与日志已有，用户看不到；区别于 AG-01（续接原因）。 | 每轮结束可见 tokens + cache 命中；历史消息持久化该字段。 |
| AE-04 | `apply_patch` 多 hunk 编辑 | 支持 unified-diff / 多 hunk / 多文件结构化编辑。 | `internal/tool/edit.go:19-67`（仅单次 old/new）、`write_file`（整文件覆盖）。 | 新增 patch 工具；失败返回精确冲突上下文（行号/期望内容）。 | 大文件多处改动现在逼模型整文件重写，token 浪费且易失败。 | 多 hunk 一次成功；冲突时模型能按上下文自修复。 |
| AE-05 | 工具参数 JSON 校验闭环 | 参数非法时不执行，短反馈让模型自修。 | `internal/agent/agent.go:3139`（normalizeToolCallArgs）、工具 schema。 | schema 校验失败发结构化 `tool_args_invalid`，限 1 次重试再执行。 | 畸形 JSON 现在直接变工具错误再烧一轮。 | 非法参数不落地执行；自修复成功率可统计。 |
| AE-06 | 渐进式工具暴露 | 核心工具常驻 + 其余按需加载。 | `internal/agent/agent.go:2225/2779`（每轮全量 toolDefs）、`token_count.go:24-28`（每轮扫 schema）。 | 常驻核心集 + `tool_search` 式加载；或按 `work_mode` 裁剪默认集。 | 工具集含 browser/MCP/动态工具后前缀膨胀，吃 token 与 cache。 | 默认前缀 token 下降；非常用工具仍可被发现调用。 |
| AE-07 | 文件变更可回滚 | 会话级 file change journal，支持撤销本轮改动。 | `internal/tool/edit.go`、`write_file`、会话 fork/rollback（已有消息级）。 | 记录 path/before/after/tool_id；GUI「撤销本轮文件改动」。 | Agent 改坏代码目前只能靠 git 或手改。 | 撤销后文件字节级还原；跨多工具连续撤销顺序正确。 |

### 2.11 会话管理与前端操作

| 编号 | 任务点 | 功能描述 | 涉及点 | 修改点 | 逻辑梳理 | 验收点 |
|---|---|---|---|---|---|---|
| SM-01 | 搜索结果跳转高亮（已落地） | 点击搜索结果定位到具体消息。 | `frontend/src/components/SessionSidebar.vue:314-317`（只 switchSession）、`internal/server/sessions.go:69-108`、`internal/memory/memory.go:3449-3477`（已返回 message_id）。 | switchSession 后按 message_id/seq 拉页定位，`ChatWindow` 滚动到 `[data-msg-id]` 并短暂高亮。 | API 已具备定位信息，前端只差滚动与高亮。 | 点击结果后目标消息可见且高亮；跨分页会话也能定位。 |
| SM-02 | 会话标签 / 分组 | 用户自定义标签组织会话。 | `SessionSidebar.vue:128-158`（仅时间分组）、`internal/memory/migrations.go`（无 tags 列）。 | `conversations.metadata` 或新表存 tags；侧栏筛选 + PATCH meta；走 `internal/upgrade/`。 | 组织维度只有时间 + 本机置顶。 | 标签增删改查；筛选即时生效；升级迁移幂等。 |
| SM-03 | 全局快捷键 | Ctrl+K 搜索 / Ctrl+N 新建 / Ctrl+B 侧栏等。 | `App.vue`（无全局绑定）、`InputArea.vue:261-268`（仅 slash 命令）。 | App 级统一 keymap + 快捷键帮助面板。 | 桌面聊天应用标配，当前全靠鼠标。 | 主要操作可纯键盘完成；帮助可发现。 |
| SM-04 | 消息赞 / 踩反馈 | 标记回复质量，沉淀本地信号。 | `MessageBubble.vue` 操作区、`messages` schema（无 feedback 列）。 | 消息 meta 或旁表存 rating；气泡按钮；可选汇总到诊断。 | 无法标记好/坏回复，提示优化无数据。 | 反馈持久化；可按会话导出。 |
| SM-05 | 置顶服务端持久化 | 置顶状态跨设备/重装保留。 | `SessionSidebar.vue:95-125`（localStorage，注释承认跨浏览器不同步）。 | `conversations.pinned` 或 metadata；与现有菜单对齐。 | 清 WebView 数据/换机后置顶丢失。 | 重装后顺序保留；多客户端一致。 |
| SM-06 | 消息书签 + 范围导出 | 钉住关键回合；按消息范围导出/复制 Markdown。 | `internal/server/export.go:27-29`（仅整会话）、`MessageBubble.vue`。 | `message_bookmarks(session_id, message_id, note)`；导出支持 `from_seq/to_seq` 或前端多选。 | 长会话回找与片段分享成本高。 | 书签可跳转；范围导出内容正确。 |

### 2.12 CLI 能力对齐

> CLI 与 GUI 共用 server 能力，但多个命令未挂载或只做了半截。

| 编号 | 任务点 | 功能描述 | 涉及点 | 修改点 | 逻辑梳理 | 验收点 |
|---|---|---|---|---|---|---|
| CLI-01 | `/kb` 挂载 + scan 真实现（已落地） | CLI 知识库命令真正可用。 | `cmd/pchat/main.go:199-213`（无 `SetKBManager`）、`internal/cli/context.go:1079-1104`（nil → Unsupported）、`internal/cli/kb.go:95-115`（`ScanStats` 只计数，真 `Scan()` 从未被调）。 | main 里 `SetKBManager(NewKBManager(cfg))`；`scan` 改调 `KBManager.Scan` 或转发 server scan API。 | 当前 `/kb` 恒“未初始化”；即便挂上 scan 也不重建索引。 | CLI 可列出/扫描知识库；scan 后索引真实更新。 |
| CLI-02 | Skill 管理命令 | CLI 支持 install/search/remove。 | `internal/cli/commands.go:405-414/2190-2212`（仅 list）、`frontend/src/api/client.ts:670-730`（GUI 五 API）、`internal/skill/manager.go:603-620`。 | 补 `/skills install\|search\|remove\|doctor`，复用 `/api/v1/skills*`。 | Skill 生命周期在 Manager + GUI + agent 工具侧已齐，CLI 只能看不能管。 | CLI 完成 skill 安装到可用全流程。 |
| CLI-03 | 导出 HTML/PDF | `/export` 支持 GUI 已有格式。 | `internal/cli/export.go:21-24/43-59`（仅 md/json）、`internal/export`（HTML/PDF 已实现）。 | `/export` 增加 `html\|pdf`，直接调 `export.ToHTML`/`ToPDF`。 | 能力在包里，CLI 未暴露。 | CLI 导出与 GUI 产物一致。 |
| CLI-04 | `/recall` 多 base + 文案更新 | 召回覆盖全部启用库。 | `cmd/pchat/main.go:205-212`（`break` 在第一个 enabled base）、`internal/cli/commands.go:320-332`（帮助仍是向量/余弦时代，实际 FTS5）。 | engine 支持 `__all__`/多 base；改写 help。 | 多库时 CLI 召回面窄，文案误导排查。 | 多库结果合并返回；help 描述与实现一致。 |

### 2.13 知识库与媒体资产补齐

| 编号 | 任务点 | 功能描述 | 涉及点 | 修改点 | 逻辑梳理 | 验收点 |
|---|---|---|---|---|---|---|
| KB-06 | 媒体扫描接线或下线 | `ScanMediaTypes` 配置真正生效，或移除死代码。 | `internal/server/knowledge_api.go:813-877`（`describeMediaFile` 死代码）、`:1033-1087`（indexScan 只走 ReadFileText，kind 固定 text）、`internal/knowledge/index.go:7-29`。 | `describeMediaFile` 接回 `indexScan` 按 `ScanMediaTypes` 分支；或删死代码改文档。 | 配置/文档宣称支持图片/音视频索引，实际管线只索引文本/PDF/Office。 | 开启后图片进三层索引并可召回；或配置项不存在不产生误解。 |
| KB-07 | 索引失败重试与计数 | LLM 索引瞬时失败不永久降级。 | `internal/server/knowledge_api.go:910-919`（Err → return nil）、`:1080-1083`（失败仍写截断正文当 overview）。 | 有限次重试；失败记入 stats.Failed / 节点标记，下次强制重扫。 | mtime 增量命中后失败节点永不重试，召回质量永久偏弱。 | 瞬时失败可恢复；失败文件在 GUI 可见（衔接 KB-05）。 |
| KB-08 | `media_cache` TTL / 清理 | 视觉摘要缓存可失效。 | `internal/knowledge/wiki_store.go:140/252-261`（只有 INSERT/SELECT）。 | 按 created_at TTL，或 ClearBase 时清空。 | 换模型/提示词后旧摘要仍命中；库长期膨胀。 | 过期缓存不再命中；清理有日志。 |
| KB-09 | indexScan 有界并发 | LLM 索引不再完全串行。 | `internal/server/knowledge_api.go:1006-1108`（Walk 内同步 buildIndexEntry）。 | worker pool（2-4）+ 保留 per-base 单 job 互斥。 | 大库 + ScanModel 时扫描耗时线性叠加。 | 大库扫描明显提速；无并发写冲突。 |
| GEN-01 | 生成媒体资产孤儿 GC | `~/.p-chat/generated/` 可清理。 | `internal/generation/executor.go:31/75-130`（有 20GB 配额，无 Prune/sweep）；参照 `internal/server/upload.go` `sweepOrphanUploads`。 | 会话删除时按 session_id 元数据 prune + 启动 grace sweep。 | 资产只增不扫，删会话后残留直到配额顶满。 | 删会话后资产可回收；sweep 不误删在引用文件。 |

### 2.14 可靠性与性能优化

> 非新功能，针对已落地能力的正确性/性能修复。REL 前缀为可靠性，PERF 前缀为性能。

| 编号 | 任务点 | 功能描述 | 涉及点 | 修改点 | 逻辑梳理 | 验收点 |
|---|---|---|---|---|---|---|
| REL-01 | LLM 重试重置流式累积器（已落地） | 修复重试叠字/叠 tool_args。 | `internal/agent/agent.go:2580-2591`（builders 在 retry 循环外初始化）、`:2937-2960`（失败后继续 WriteString/+=）。 | 每 attempt 开始重置 builders/argsAccum/streamBytes；发 `content_rewrite`/`stream_reset` 清前端；usage 按 attempt 隔离。 | 中途 rate_limit/stall 重试会在已上屏内容后继续追加，UI 与工具参数错乱。 | 构造中途失败重试用例，最终文本/参数无重复；usage 不重复累计。 |
| REL-02 | 混合工具批次并行度 | safe 工具不被 exclusive 拖累。 | `internal/agent/agent.go:3320-3331/3589-3596`（任一不可并行则整批 serialGate）、`policy.go:159-178`。 | 仅 exclusive 互斥；safe 仍并行；或按目标路径细粒度锁。 | `read×N + write×1` 时所有 read 被串行，wall-clock 拉长。 | 混合批次中只读工具并行完成；写/执行语义不变。 |
| REL-03 | LLM raw chunk 日志门控（已落地） | 默认不把用户内容写进 debug 日志。 | `internal/llm/client.go:924-929`（无条件 log 前 3 个 raw chunk）、`openai_responses_adapter.go:264-266`。 | 默认只记长度/字段名；全文需 `PC_LLM_DEBUG=1`；redact `api_key`/`Authorization`。 | 用户回复/思维链/工具参数默认落盘 `~/.p-chat/logs/`。 | 默认日志无消息正文；开启调试才有全文。 |
| REL-04 | timer 生命周期修复 | sendOrDrop / stallTimer 不再堆 timer。 | `internal/agent/agent.go:1955-1959`（每次 `time.After(30s)`）、`:2797-2798`（attempt 循环内 defer Stop）。 | `time.NewTimer` + Stop/Reset；attempt 结束显式 Stop。 | 高频 content 下大量未回收 timer 增 GC 压力；多次重试堆叠 defer。 | 长会话 goroutine/timer 数稳定（可配 diagnostics 验证）。 |
| PERF-01 | SSE content 帧合并（已落地） | 降低 marshal + Flush 频率。 | `internal/server/messages.go:551-575`、`stream_adapter.go:199-206`、`frontend/src/stores/chat.ts:2235+`。 | 服务端对纯 content/thinking 做 16-33ms 合并；tool/phase/done 立即刷；前端可选 rAF 合并。 | token 级 delta 每帧 JSON + 强制 flush，放大调度与前端响应式成本。 | 长输出 CPU/事件数下降；首字延迟不明显增加。 |
| PERF-02 | 消息搜索 FTS5（已落地） | 替换 LIKE 全表扫描。 | `internal/memory/memory.go`、`messages_fts.go`、migration 14、`internal/upgrade` V20。 | `messages_fts` FTS5；项目过滤进 SQL（`json_extract(metadata,'$.project_path')`）；保留 escape 语义；走 `internal/upgrade/`。 | 知识库已有 FTS5，消息搜索仍 O(N)。 | 大库搜索毫秒级；项目过滤结果一致。 |
| PERF-03 | `Flush()` 去重 MAX(seq) 查询 | 批量写不再每行查一次。 | `internal/memory/memory.go:1409-1440`（注释写每会话一次，实现每条 pending 都查）。 | `if _, ok := convMax[id]; !ok` 再查；或先 GROUP BY。 | 同会话批量写入 N 次相同查询，放大锁持有。 | 批量写耗时下降；seq 分配正确无空洞冲突。 |
| PERF-04 | 滚动可见性改 IntersectionObserver | 长会话滚动不再全量 DOM 查询。 | `frontend/src/components/ChatWindow.vue:99-120`（每次 scroll querySelectorAll）、`:75-92`（hasArchivedInView O(N)）。 | IntersectionObserver 维护 visibleMsgIds；archived id 用 Set 缓存。 | 已有 content-visibility，但 JS 侧仍每次扫全部节点。 | 长会话滚动帧率提升；归档提示逻辑不变。 |
| PERF-05 | upload 清理 / 子代理 store 优化 | 消除反复 ReadDir 与重复建库。 | `internal/server/upload.go:69-91`（按 id ReadDir）、`internal/memory/memory.go:3172-3181`（按 id COUNT）、`internal/subagent/subagent.go:1580-1601`（每次新建 ephemeral SQLite）。 | 一次 ReadDir 建 id→paths map；批量 COUNT/IN；短任务子代理用内存 store 或池化。 | 删多附件会话 O(ids × dir_entries)；并行子代理启动 IO 叠加。 | 大量附件删除耗时下降；并行 task 启动开销下降。 |

### 2.15 结构债（长期可维护性）

| 编号 | 任务点 | 功能描述 | 涉及点 | 修改点 | 逻辑梳理 | 验收点 |
|---|---|---|---|---|---|---|
| REF-01 | `ChatWithTools` 分阶段拆分 | 上帝函数按阶段拆解。 | `internal/agent/agent.go:2012-4370`（约 2300 行）。 | 拆 `runLLMRound`/`dispatchTools`/`applyBreakers`/`finalizeRound`，主循环只编排。 | 回归成本高，新功能易踩旧逻辑；建议随功能迭代渐进拆。 | 行为不变（现有测试全绿）；各阶段可单测。 |
| REF-02 | `chat.ts` store 拆分 | 上帝 store 按域拆分 + 流式去响应式。 | `frontend/src/stores/chat.ts`（约 3667 行，无 shallowRef/批处理）。 | 拆 session/stream/queue store；流式消息 shallowRef + 显式 trigger；delta 走 rAF queue。 | 会话/流式/队列/恢复/分叉/todo 挤一个文件，响应式粒度粗。 | 流式渲染 CPU 下降；store 单测可行。 |
| REF-03 | 巨石组件拆分 | MessageBubble / AppSettingsModal 解耦。 | `MessageBubble.vue`（约 2930 行）、`AppSettingsModal.vue`（>6200 行）。 | 气泡拆 User/Assistant/Actions/Attachments；设置按 Tab 懒加载子页。 | 难测难并行改，热更新慢。 | 拆分后类型检查与构建通过；视觉回归一致。 |
| REF-04 | 图片识别单轨化 | 消除双轨实现。 | `internal/agent/agent.go:1140-1170`（recognizeImageWith*）vs `:875-905`（recognizeMediaWith*）、派发处 `:3379-3417` 两套注入。 | 以 `media_recognize` 为唯一路径，image API 作薄封装。 | 双轨行为易漂移，测试面翻倍。 | 行为一致；旧 API 测试通过薄封装复用。 |
| REF-05 | MCP 测试补齐 | transport/错误/超时路径回归网。 | `internal/mcp/`（6 源文件仅 manager_test.go 3 个 Test；client/transport/sse_transport/handler 无测试）。 | 补 transport 契约与失败路径 characterization 测试。 | MCP-01 推进前无回归网，集成风险高。 | stdio/SSE/超时/工具路由碰撞有用例。 |
| REF-06 | 扩展 content script 健壮性 | 修复上传硬失败与 SPA 脆弱性。 | `browser-extension/content.js:188-195`（file_upload 硬失败）、`:57-68`（直接 el.value=）、`manifest.json:13-16`（`<all_urls>`）。 | 原生 value setter + input 事件；snapshot 刷新/短生命周期 ref；收窄 host_permissions；工具描述标明不支持上传。 | React 受控输入/SPA 重绘后 ref 失效；权限面偏大。 | 主流 SPA 站点输入/点击稳定；权限按域名白名单。 |

## 3. 已移除或不再作为当前迭代

- `P2-5 race mode` 已移除，不再作为当前 backlog。
- 下面这些虽然曾经在规划里出现，但现在已经属于已落地能力：
  - `trace id`
  - `auto-continue`
  - `work_mode`
  - 重答历史
  - 断线恢复
  - 工具结果折叠
  - 上下文检查器
  - 工具 dry-run
  - 浏览器控制
  - 动态工具 hot-reload
  - 工具列表抽屉
  - 会话分叉 / 编辑重发（`forkSession` / `rollbackTo` / `regenerate`）
  - 并行工具调用（`ToolPolicy.CanRunInParallel`）
  - Runtime Trust 原型（`internal/auth` + `internal/browser/pairing.go`，代码与测试已齐，生产接线见 SEC-01）

## 4. 建议优先级

1. ~~**低成本高价值修复先行**~~：第一批小步（REL-01 重试叠字、REL-03 日志门控、SEC-03 pprof 默认关、SEC-04 eval_write 拦截、CLI-01 /kb 挂载、SM-01 搜索跳转）已于 2026-09-26 全部落地。
2. **安全主线**：~~SEC-02~~ / ~~SEC-05 客户端~~ 已落地 → 发布站签包联调 → SEC-01（Runtime Trust 接线）→ SEC-06/07。
3. **更新渠道**：UPD-01 客户端已接入；发布站需支持渠道筛选并做真实 Beta 联调。更新防伪仍由 SEC-05 跟进。
4. **体验主线**：AE-01（流式工具参数）+ AE-03（cache 用量）→ PERF-01（SSE 帧合并）→ PERF-02（消息搜索 FTS5）→ SM-02/04/05（会话组织）。
5. **成本主线**：AE-02（prompt cache 断点）+ AE-04（apply_patch），对 coding 场景 token 节省最直接。
6. 知识库 KB-01 到 KB-05 已落地；KB-06~09 处理媒体接线、失败重试、缓存 TTL 与并发。
7. 浏览器控制稳定性（BR-01～BR-05）已落地；REF-06 处理扩展 content script 健壮性。
8. `DT-04` 剩余 GUI 风险展示与 `DT-05` 版本/禁用机制。
9. `AG-01` 到 `AG-04` Agent 任务执行体验：提高长任务和多 agent 协作的可解释性，适合与 GUI 优化穿插推进。
10. `MCP-01` 到 `MCP-03` MCP 完整集成：建议先补 REF-05 测试回归网，再在动态工具安全模型稳定后推进。
11. `SB-01` 到 `SB-04` 沙箱与运行环境：价值高但改动面大，适合独立设计与分阶段交付。
12. 结构债（REF-01~04）穿插在功能迭代中渐进拆分，不宜单独开大重构。

## 5. 任务拆分建议

- 第一批小步（一天级）：`REL-01`、`REL-03`、`SEC-03`、`SEC-04`、`CLI-01`、`SM-01` —— ✅ 已于 2026-09-26 全部落地（含 stall watchdog 重试被吞的隐藏 bug 修复）。
- 安全连续增强：`SEC-02` ✅ → `SEC-05` → `SEC-01`（七片接线按 decision-map）→ `SEC-06`/`SEC-07`。
- 更新渠道：`UPD-01` 客户端勾选、检查、下载和版本锁定已接通；待发布站按 `include_prerelease=1` 返回稳定版/Beta 中的最高可用版本，并完成真实发布联调。
- 文档结构：`DOC-05` 已落地，后续目录或模块职责变化时同步维护 `docs/project-structure.md`。
- 第二批能力：`AE-01`、`AE-03`、`PERF-01`、`PERF-02`、`SM-02`，优先提升 agent 实际可用性与前端性能。
- 第三批增强：`AE-02`、`AE-04`、`MCP-01`（先 REF-05）、`SB-01`，这些涉及跨模块协议或运行环境，建议单独写设计文档后再做。

## 6. 说明

- 本文档是“现状 + 机会”清单，不是详细设计。
- 详细实现请回到对应模块文档或 `docs/plans/*.md` 历史档案。
- 新增功能完成后，应同步更新本文档、`README.md` 的功能特性 / GUI 操作入口 / 常见问题，以及 `.agents/docs/INDEX.md` 的模块入口。
