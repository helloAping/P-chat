# 项目现状与可做事项

> 这份文档用于梳理“已落地能力 + 后续可迭代/可优化点”。
>
> 原则：
> - 已经在 GUI / CLI / server 中实现的能力，只放在“已落地”里，不再重复放入 backlog。
> - 仍可继续做的点按模块拆开，尽量写成能进入排期的方向。
> - 历史设计保留在 `docs/plans/*.md`，本文档只维护当前判断。
> - 本文档只做功能与逻辑梳理，不包含代码实现。

## 1. 已落地

### 1.1 Agent 与对话连续性
- `auto-continue`：todo 未完成时可自动续接 LLM，避免多步任务半路停止。
- `work_mode`：`coding` / `daily` 工作模式已接入，支持会话级与全局默认配置。
- `style=off`：GUI / CLI 均可关闭说话风格注入。
- `question` / `tool_confirm`：LLM 提问与沙箱确认可通过 GUI 弹窗承接。
- 重答历史：重答不会直接抹掉旧回答，支持历史版本保留与切换。

### 1.2 流式消息与可观测性
- 断线恢复：SSE 流意外中断后可从服务端快照补齐 trailing assistant bubble。
- 工具结果折叠：长工具结果默认折叠，折叠状态持久化，支持复制。
- 上下文检查器：GUI 可查看 context window 利用率、per-message token 与压缩摘要。
- `trace id`：请求、SSE、工具调用链路已贯通，可在前端复制并用于日志定位。
- 代码高亮：常见语言代码块已支持 syntax highlight。

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
| DT-04 | 动态工具权限分级 | 为每个动态工具增加 sandbox/confirm 策略，避免共享同一审批强度。 | `internal/tool/dynamic`、`internal/sandbox`、`internal/tool`、`ToolConfirmModal.vue`。 | YAML 增加 permission/sandbox 字段；工具执行前进入统一决策表；GUI 显示风险等级。 | 动态工具声明读/写/执行/网络等能力，运行前结合会话权限和工具策略决定自动通过或确认。 | 高风险动态工具会触发确认；低风险查询工具可按策略自动执行；工具卡片显示权限来源。 |
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

## 4. 建议优先级

1. 知识库 KB-01 到 KB-05 已落地；后续可转向动态工具安全、Agent 执行体验或 MCP 集成。
2. 浏览器控制稳定性（BR-01～BR-05）已落地：连接诊断、扩展更新、多 tab、权限收口与 E2E 回归均完成。
3. `DT-04` 到 `DT-05` 动态工具安全收口：加载诊断、试运行和项目级目录已落地，下一步应补权限分级和版本/禁用机制。
4. `AG-01` 到 `AG-04` Agent 任务执行体验：提高长任务和多 agent 协作的可解释性，适合与 GUI 优化穿插推进。
5. `MCP-01` 到 `MCP-03` MCP 完整集成：README 仍标记为未完成，建议在动态工具安全模型稳定后推进。
6. `SB-01` 到 `SB-04` 沙箱与运行环境：价值高但改动面大，适合独立设计与分阶段交付。

## 5. 任务拆分建议

- 第一批小步：`DOC-01`、`UX-05`、`BR-01`、`DT-01` 已开始落地，当前已完成 README 操作说明、浏览器连接诊断、动态工具加载诊断。
- 浏览器连续增强：BR-02 / BR-03 / BR-04 / BR-05 已落地（协议版本、多 tab、权限策略、模拟扩展 E2E）。
- 第二批能力：`KB-01`、`KB-02`、`DT-04`、`AG-01`，优先提升 agent 实际可用性和安全边界。
- 第三批增强：`MCP-01`、`SB-01`，这些涉及跨模块协议或运行环境，建议单独写设计文档后再做。

## 6. 说明

- 本文档是“现状 + 机会”清单，不是详细设计。
- 详细实现请回到对应模块文档或 `docs/plans/*.md` 历史档案。
- 新增功能完成后，应同步更新本文档、`README.md` 的功能特性 / GUI 操作入口 / 常见问题，以及 `.agents/docs/INDEX.md` 的模块入口。
