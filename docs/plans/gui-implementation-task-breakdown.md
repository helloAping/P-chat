# P-Chat GUI Implementation Task Breakdown

> 目标：基于已锁定设计图，把 GUI 完善拆成可并行、可验收的实现任务。  
> 设计索引：[`gui-design-reference.md`](gui-design-reference.md)  
> 视觉规范：[`../../.agents/docs/frontend-design.md`](../../.agents/docs/frontend-design.md)  
> 主计划：[`project-aware-ui-redesign-plan.md`](project-aware-ui-redesign-plan.md)

## 0. 当前执行进度

| 任务 | 状态 | 备注 |
|---|---|---|
| T0 文档与范围锁定 | 已完成 | 设计图引用已统一到最终版本 |
| T1 主壳与项目工作流验收 | 已完成 | 本地审计完成；补齐侧栏搜索按 `project_path` 过滤，并增加前后端测试 |
| T2 消息 parts 分组修正 | 已完成 | `thinking` 已独立渲染，不进入 `.event-timeline` |
| T3 子代理卡片与后台任务 UI | 已完成 | 子代理类型与后台/同步模式已合并为紧凑 meta，后台任务列表保留类型文本，展开态收敛为嵌套对话列表 |
| T4 Provider / Model 设置语义 | 已完成 | Provider 请求协议显示为 `OpenAI Chat` / `Anthropic Messages`，保存值不变 |
| T5 弹窗、抽屉、菜单、空状态统一 | 已完成 | `ToolConfirmModal` 接入 `AppModal`；`ToolListDrawer` 改用 icon barrel 和 token 化错误态；新增静态规范测试 |
| T6 响应式与视觉 QA | 返工中 | 真实渲染截图发现侧栏比例、重复快捷入口、工具/媒体卡片与设计稿仍有差距；项目列表必须只在 `ProjectSwitcher` 下拉面板中展示，不允许作为常驻侧栏区块占位 |
| T7 最终整合与 build | 待复验 | 需在视觉返工后重新运行 `npm run build`、`npm run test`、相关 Go 测试与截图验收 |

> Cursor Grok 4.6（非 Fast）只读审计已按用户授权尝试启动 T1/T5/T6。T1/T5/T6 进程均未返回可用审计正文（连接重试或静默退出），本轮以本地审计和测试结果收口。

## 1. 文档审计结论

### 1.1 已描述清楚的部分

- 产品方向已明确为 **Project-Aware Calm Workbench**。
- 主壳结构已明确：`AppRail` + 统一 `SessionSidebar` + `TopBar` + `ChatWindow` + 可折叠 `InspectorPanel`。
- 项目能力边界已明确：新增项目、切换项目、全局会话、项目内会话、新建对话、搜索会话都不能遗漏。
- 消息 parts 的核心规则已明确：`thinking` 独立，`tool` / `skill` / `sub_agent` 进入 `.event-timeline`。
- Provider 协议语义已明确：是 LLM 请求端点协议，不是 HTTP/HTTPS 网络协议。
- 子代理补充要求已明确：显示子代理类型和运行模式，展开态像嵌套对话列表。

### 1.2 已修正的文档矛盾

- `project-aware-ui-redesign-plan.md` 曾写统一侧栏约 300-320px，后续又与实现 token 产生 220px / 360px 口径不一致。已统一为默认 `SessionSidebar : ChatWindow : InspectorPanel = 1 : 3 : 1`，侧栏允许用户拖拽并持久化。
- `project-aware-ui-redesign-plan.md` 曾写 active 会话使用左侧细 accent 条，但 `frontend-design.md` 禁止列表 active 左侧 brand 竖条。已改为轻背景、文字权重、状态点。

### 1.3 仍需实现时特别澄清的点

- `OpenAI Chat` 是 UI 显示语义；当前后端配置值仍是 `openai`，不是把 schema 改成 `openaichat`。除非单独做后端兼容任务，否则不要改后端枚举。
- `Base URL` 是网络地址字段，可以出现 `https://`；Provider 的“请求协议”下拉不能出现 HTTP/HTTPS。
- `thinking` 不仅在文档上独立，代码里也要从 `MessageBubble.vue` 的 process row 判断和 `.event-timeline` CSS 中移出。
- `SubAgentCard` 目前已有部分 metadata 字段，但展开态仍更像带竖向 rail 的内部过程流，需要收敛成设计图里的嵌套对话列表。
- `SubAgentJobsPanel` 显示 `subagent_type` 文本即可，不再额外增加「类型」标签或强 badge。
- 弹窗、抽屉、菜单、toast、空状态已有多个组件，任务里必须逐一核对，不要只改设置页。

## 2. 并行策略

主线任务建议由一个主 agent 串行整合，避免多个 agent 同时改同一批 Vue 文件。可交给 Cursor Grok 4.6（不要 Fast）的任务以“相对独立、偏审计或局部 UI”为主。

| 任务 | 建议执行者 | 是否可异步 | 原因 |
|---|---|---:|---|
| T0 文档与范围锁定 | 主 agent | 否 | 需要统一口径，避免多 agent 分叉 |
| T1 主壳与项目工作流验收 | Cursor Grok 4.6 | 是 | 偏读代码和列问题，可异步输出差距清单 |
| T2 消息 parts 分组修正 | 主 agent | 否 | 会碰 `MessageBubble.vue`、`ThinkingBlock.vue`，和子代理任务有交叉 |
| T3 子代理卡片与后台任务 UI | Cursor Grok 4.6 或主 agent | 可异步但需小心 | 可独立做，但要等 T2 的分组规则稳定 |
| T4 Provider / Model 设置语义 | Cursor Grok 4.6 | 是 | 范围集中在 `AppSettingsModal.vue` |
| T5 弹窗、抽屉、菜单、空状态统一 | Cursor Grok 4.6 | 是 | 偏视觉审计和局部样式，和主消息流冲突较少 |
| T6 响应式与视觉 QA | Cursor Grok 4.6 | 是 | 适合独立验证并提交问题清单 |
| T7 最终整合与 build | 主 agent | 否 | 需要解决冲突、跑最终验证 |

## 3. 任务卡片

### T0 文档与范围锁定

执行者：主 agent  
目标：确保后续实现只按最终设计图执行，不再回到旧图或扩展图。

输入：

- `docs/plans/gui-design-reference.md`
- `docs/plans/project-aware-ui-redesign-plan.md`
- `.agents/docs/frontend-design.md`

实施方案：

1. 确认最终采用设计图只包含 `call_7lv...`、`call_wyrt...`、`call_CiThe...`、`call_rX...`、`call_QHz...`、`call_zi3...`。
2. 确认旧图 `call_Evg...` / `call_TBx...` 仅作背景参考，扩展过多的 `call_kh...` / `call_sW...` 不作为最终依据。
3. 确认 sidebar / inspector 宽度以 `style.css` token 为准。
4. 确认所有任务都不要求后端 schema 迁移。

交付：

- 文档差距清单。
- 若有冲突，先改文档，不进入代码实现。

### T1 主壳与项目工作流验收

建议执行者：Cursor Grok 4.6（不要 Fast）  
目标：确认主壳和项目/会话工作流没有漏功能。

重点文件：

- `frontend/src/App.vue`
- `frontend/src/components/AppRail.vue`
- `frontend/src/components/SessionSidebar.vue`
- `frontend/src/components/ProjectSwitcher.vue`
- `frontend/src/components/TopBar.vue`
- `frontend/src/components/InspectorPanel.vue`
- `frontend/src/stores/chat.ts`
- `frontend/src/style.css`

实施方案：

1. 对照主界面基准图检查 App shell 层级：`TitleBar`、`AppRail`、`SessionSidebar`、`TopBar`、`ChatWindow`、`InspectorPanel`。
2. 核对 `ProjectSwitcher` 是否包含搜索项目、新建项目、全局会话、项目列表、当前项目 check。
3. 核对 `SessionSidebar` 是否只展示当前项目会话，切换项目后是否调用 `setActiveProject()` 并刷新对应会话。
4. 核对 `ProjectSwitcher` 的项目列表只在 popover/dropdown 中出现，侧栏常驻区域只保留当前项目卡、搜索、新建对话和当前项目会话列表。
5. 核对 `createSession()` 是否使用当前 `activeProjectPath`。
6. 核对 AppRail 保留全局、项目、设置、左下新建项目、关于入口；ProjectSwitcher 触发器旁不再额外放 `+`。
7. 核对 sidebar collapsed 与 inspector open 的持久化是否互不影响。

验收：

- 新建项目入口在 AppRail 左下角和 ProjectSwitcher popover 内可见。
- 切换项目入口可见。
- 全局会话可进入。
- 当前项目会话列表可见。
- 项目选项不在侧栏页面中直接列举；展开项目切换器后才显示项目列表。
- 切换项目后不会混入其它项目会话。
- Inspector 折叠后聊天区正常扩展。

Cursor Grok 4.6 提示词：

```text
请使用 Cursor Grok 4.6，不要 Fast。只审计并必要时修正 P-Chat GUI 的主壳与项目工作流。

先阅读：
- AGENTS.md
- .agents/docs/frontend.md
- .agents/docs/frontend-design.md
- docs/plans/gui-design-reference.md
- docs/plans/project-aware-ui-redesign-plan.md

重点文件：
- frontend/src/App.vue
- frontend/src/components/AppRail.vue
- frontend/src/components/SessionSidebar.vue
- frontend/src/components/ProjectSwitcher.vue
- frontend/src/components/TopBar.vue
- frontend/src/components/InspectorPanel.vue
- frontend/src/stores/chat.ts
- frontend/src/style.css

目标：
确认 AppRail + 统一侧栏 + ProjectSwitcher popover + ChatWindow + InspectorPanel 的结构符合设计图，并保证新增项目、切换项目、全局会话、项目内会话列表、新建对话、搜索会话都可见可用。

不要改后端，不要改 ~/.p-chat/config.json，不要删除入口，不要恢复项目列表常驻独立列。发现问题先给差距清单；若修改代码，改动保持最小，并运行 cd frontend && npx vue-tsc -b && npm run build。
```

### T2 消息 parts 分组修正

执行者：主 agent  
目标：把 `thinking` 从 `.event-timeline` 中彻底拆出来。

重点文件：

- `frontend/src/components/MessageBubble.vue`
- `frontend/src/components/ThinkingBlock.vue`
- `frontend/src/components/ToolCallCard.vue`
- `frontend/src/components/SkillCallCard.vue`
- `frontend/src/components/ToolCallGroup.vue`
- `frontend/src/style.css`

实施方案：

1. 修改 `MessageBubble.vue` 的 `isProcessRowEntry()`，使 `thinking` 不再进入 events buffer。
2. 模板中让 `thinking` 在 content 分支渲染，和 text/question 一样独立排布。
3. 清理 `.event-timeline` CSS 中对 `.thinking-block` 的选择器和相邻分割规则。
4. 修改 `ThinkingBlock.vue` 注释和样式，让它拥有独立浅底、边框和间距，不依赖 `.event-timeline` 父容器。
5. 保持流式默认展开逻辑不变。
6. 不改变 SSE / parts 数据结构。

验收：

- assistant 消息中 `thinking` 独立出现。
- 连续 `tool` / `skill` / `sub_agent` 仍合并到 `.event-timeline`。
- `thinking` 与 tool 相邻时不会被包进同一个浅底面板。
- 流式 spinner 不引发大面积动画。

### T3 子代理卡片与后台任务 UI

建议执行者：Cursor Grok 4.6（不要 Fast），或主 agent 在 T2 后接手  
目标：按子代理卡片修正版完善 `SubAgentCard` 和 `SubAgentJobsPanel`。

重点文件：

- `frontend/src/components/SubAgentCard.vue`
- `frontend/src/components/SubAgentJobsPanel.vue`
- `frontend/src/stores/chat.ts`
- `frontend/src/api/client.ts`
- `frontend/src/components/ThinkingBlock.vue`
- `frontend/src/components/ToolCallCard.vue`
- `frontend/src/components/ToolCallGroup.vue`

实施方案：

1. 确认 `api.StreamEvent` 和 `SubAgentPart` 字段包含 `sub_agent_type`、`sub_agent_run_mode`、`sub_agent_model`、`sub_agent_task_id`。
2. 确认 `stores/chat.ts` 的 `backfillSubAgentMetadata()` 正确写入 `agentType`、`runMode`、`agentModel`、`taskId`。
3. `SubAgentCard` 折叠态把类型与运行模式合并为紧凑 meta：例如 `frontend_agent · 后台`。
4. `SubAgentCard` 折叠态不要再额外展示首字母图标、`类型` badge、`后台子代理` badge。
5. 展开态改为嵌套对话列表视觉：用户行、子代理行、独立 thinking 行、内部工具执行行、结果摘要行。
6. 去掉展开态中过强的竖向 rail；使用轻分割、缩进和头像/小图标表达层级。
7. `SubAgentJobsPanel` 将 `job.subagent_type` 保留为弱文本 meta，不重复加「类型」标签。
8. 保留取消任务、刷新、折叠、错误提示等现有交互。

验收：

- 折叠态可看到子代理类型。
- 后台任务列表可看到 `subagent_type`，且没有重复的「类型」标签。
- 展开态像嵌套对话列表，不像普通详情面板。
- 子代理内部 thinking 仍独立，不混入工具时间线。
- 长子代理输出不会造成明显性能退化。

Cursor Grok 4.6 提示词：

```text
请使用 Cursor Grok 4.6，不要 Fast。只处理 P-Chat 子代理 UI，不要改其它区域。

先阅读：
- AGENTS.md
- .agents/docs/frontend.md
- .agents/docs/frontend-design.md
- docs/plans/gui-design-reference.md
- docs/plans/gui-implementation-task-breakdown.md

参考设计图：
C:\Users\admin\.codex\generated_images\01a09621-4be8-7343-8f14-aa85c77f5ec3\call_wyrt6snK8obG8VrcBP3nRqGQ.png

重点文件：
- frontend/src/components/SubAgentCard.vue
- frontend/src/components/SubAgentJobsPanel.vue
- frontend/src/stores/chat.ts
- frontend/src/api/client.ts

目标：
SubAgentCard 折叠态必须把 sub_agent_type 与 sub_agent_run_mode 合并为紧凑 meta，并展示任务标题、状态、耗时、模型/任务 ID；不要额外展示首字母图标、`类型` badge、`后台子代理` badge。展开态必须像嵌套对话列表，包含子代理自己的消息、独立 thinking、工具/技能执行记录和结果摘要。SubAgentJobsPanel 每条后台任务必须显示 subagent_type 文本。

不要改变 SSE / parts 数据结构，不要改后端，不要重做整页布局。完成后运行 cd frontend && npx vue-tsc -b && npm run build。
```

### T4 Provider / Model 设置语义

建议执行者：Cursor Grok 4.6（不要 Fast）  
目标：按设置页修正版修正协议字段和模型弹窗字段。

重点文件：

- `frontend/src/components/AppSettingsModal.vue`
- `frontend/src/api/client.ts`
- `internal/config/manager.go`（只读确认）
- `internal/config/config.go`（只读确认）

实施方案：

1. 将 `protocolOptions` 文案改为 `OpenAI Chat` / `Anthropic Messages`，value 保持 `openai` / `anthropic`。
2. Provider 编辑表单 label 从“协议”改为“请求协议”或“LLM 协议”。
3. 新增 Provider 弹窗同样改为“请求协议”或“LLM 协议”。
4. helper text 明确“请求端点协议，不是 HTTP/HTTPS”。
5. `Base URL` helper text 明确它是网络地址，实际请求为 Base URL + 模型 API 端点后缀。
6. 模型添加/编辑弹窗移除“功能标签”相关 UI；保留模型能力、接口信息和必要字段。
7. 不把后端 `openai` 枚举改成 `openaichat`。

验收：

- Provider 协议下拉不出现 HTTP/HTTPS。
- UI 可见 `OpenAI Chat` / `Anthropic Messages`。
- Base URL 仍可以填 `https://...`。
- 模型弹窗没有“功能标签”区域。
- 新增 Provider 校验提示不再只说“协议”，而是“请求协议”。

Cursor Grok 4.6 提示词：

```text
请使用 Cursor Grok 4.6，不要 Fast。只处理 P-Chat 设置页 Provider / Model 的 UI 语义，不要改其它页面。

先阅读：
- AGENTS.md
- .agents/docs/frontend.md
- .agents/docs/frontend-design.md
- docs/plans/gui-design-reference.md
- docs/plans/gui-implementation-task-breakdown.md

参考设计图：
C:\Users\admin\.codex\generated_images\01a09621-4be8-7343-8f14-aa85c77f5ec3\call_CiTheEW9FZ595Jz6AS7wwBvM.png

重点文件：
- frontend/src/components/AppSettingsModal.vue
- frontend/src/api/client.ts
- internal/config/manager.go 只读确认
- internal/config/config.go 只读确认

要求：
Provider 的协议字段表示 LLM 请求端点协议，不是 HTTP/HTTPS。界面显示 OpenAI Chat / Anthropic Messages，保存值仍为 openai / anthropic。Base URL 单独作为网络地址字段，可以显示 https://。模型添加/编辑弹窗移除“功能标签”区域。

不要改后端枚举，不要做 schema 迁移，不要改 ~/.p-chat/config.json。完成后运行 cd frontend && npx vue-tsc -b && npm run build。
```

### T5 弹窗、抽屉、菜单、空状态统一

建议执行者：Cursor Grok 4.6（不要 Fast）  
目标：把补充设计图中的弹窗、抽屉、菜单、toast、空状态收敛到同一视觉语言。

重点文件：

- `frontend/src/components/AppModal.vue`
- `frontend/src/components/SessionSidebar.vue`
- `frontend/src/components/ToolConfirmModal.vue`
- `frontend/src/components/QuestionModal.vue`
- `frontend/src/components/ToolListDrawer.vue`
- `frontend/src/components/ImageLightbox.vue`
- `frontend/src/components/DownloadDock.vue`
- `frontend/src/style.css`

实施方案：

1. 盘点所有 modal / drawer / popover / toast / empty state 的现有样式。
2. 优先复用 `AppModal.vue`、`.settings-section`、`.app-action-menu`、全局 token。
3. 统一标题、说明、按钮区、危险操作、取消操作的层级。
4. 确认新建项目、删除项目、删除会话、导出、关于、工具确认、问题回答等弹窗都没有视觉断层。
5. 不新增业务功能，只做样式和布局收敛。

验收：

- 弹窗标题和说明层级一致。
- 危险操作使用一致的 error 语义。
- popover / menu 不使用大面积 brand 色。
- 空状态有明确主操作，但不喧宾夺主。
- 暗色主题可读。

Cursor Grok 4.6 提示词：

```text
请使用 Cursor Grok 4.6，不要 Fast。只做 P-Chat 弹窗、抽屉、菜单、toast、空状态的视觉审计和最小样式收敛。

参考：
- docs/plans/gui-design-reference.md
- C:\Users\admin\.codex\generated_images\01a09621-4be8-7343-8f14-aa85c77f5ec3\call_QHzFLdPv5c2KQ6d6Q1LPYPU2.png
- C:\Users\admin\.codex\generated_images\01a09621-4be8-7343-8f14-aa85c77f5ec3\call_zi3QYpr9kEtEO6WehyJOCAm0.png

重点文件：
- frontend/src/components/AppModal.vue
- frontend/src/components/SessionSidebar.vue
- frontend/src/components/ToolConfirmModal.vue
- frontend/src/components/QuestionModal.vue
- frontend/src/components/ToolListDrawer.vue
- frontend/src/components/ImageLightbox.vue
- frontend/src/components/DownloadDock.vue
- frontend/src/style.css

只统一样式和层级，不新增业务功能，不删除入口。所有颜色、间距、圆角、阴影、动效走 token。完成后运行 cd frontend && npx vue-tsc -b && npm run build。
```

### T6 响应式与视觉 QA

建议执行者：Cursor Grok 4.6（不要 Fast）  
目标：验证宽屏、窄屏、亮色、暗色、流式、非流式状态是否符合设计图。

重点文件：

- `frontend/src/App.vue`
- `frontend/src/components/ChatWindow.vue`
- `frontend/src/components/InputArea.vue`
- `frontend/src/components/SessionSidebar.vue`
- `frontend/src/components/InspectorPanel.vue`
- `frontend/src/style.css`

实施方案：

1. 跑前端 typecheck 和 build。
2. 使用本地 dev server 或 Wails GUI 观察 1600x1000、1366x768、1100x720、900x700。
3. 验证 sidebar collapsed、inspector open/closed、输入区 dock stack、消息滚动区域。
4. 验证长标题、长路径、长工具名、长模型名不会挤爆布局。
5. 验证 light / dark 主题颜色不冲突。
6. 输出截图和问题清单。

验收：

- 输入区不漂移。
- 消息区可滚动。
- 侧栏折叠不遮挡主区。
- Inspector 折叠不影响输入区。
- 文本不明显溢出或重叠。

Cursor Grok 4.6 提示词：

```text
请使用 Cursor Grok 4.6，不要 Fast。只做 P-Chat GUI 响应式与视觉 QA，优先输出问题清单，只有小问题才直接修。

先阅读 docs/plans/gui-design-reference.md 和 docs/plans/gui-implementation-task-breakdown.md。

验证视口：
- 1600x1000
- 1366x768
- 1100x720
- 900x700

重点看：
- sidebar collapsed / expanded
- inspector open / closed
- ChatWindow 消息滚动
- InputArea dock stack
- long title/path/model/tool name
- light / dark 主题
- thinking 独立与 event-timeline 分离
- 子代理展开态
- Provider 请求协议文案

完成后给出问题清单、影响范围、建议修复文件。若修改代码，保持最小改动，并运行 cd frontend && npx vue-tsc -b && npm run build。
```

### T7 最终整合与 build

执行者：主 agent  
目标：整合所有异步任务结果，解决冲突，并完成最终验证。

重点文件：

- 所有被 T1-T6 修改的文件
- `docs/plans/gui-design-reference.md`
- `docs/plans/gui-implementation-task-breakdown.md`

实施方案：

1. 拉取或合并异步任务改动前，先查看 `git status` 和每个文件 diff。
2. 保留用户/其他 agent 的有效改动，不做无关 revert。
3. 按 `frontend-design.md` 强约束逐项检查。
4. 跑 `npx vue-tsc -b`。
5. 跑 `npm run build`。
6. 如果有视觉风险，启动 dev server 做手动检查。
7. 更新任务文档中的完成状态。

验收：

- 构建通过。
- 没有明显视觉断层。
- 没有遗漏现有功能入口。
- 文档与实现一致。

## 4. 建议并发安排

第一批可并发：

- T1 主壳与项目工作流验收 - Cursor Grok 4.6
- T4 Provider / Model 设置语义 - Cursor Grok 4.6
- T5 弹窗、抽屉、菜单、空状态统一 - Cursor Grok 4.6

第二批：

- T2 消息 parts 分组修正 - 主 agent
- T3 子代理卡片与后台任务 UI - 等 T2 完成后执行，或由 Cursor Grok 4.6 在只改子代理文件的前提下异步做

最后：

- T6 响应式与视觉 QA - Cursor Grok 4.6
- T7 最终整合与 build - 主 agent

## 5. 总提示词

```text
请基于 docs/plans/gui-design-reference.md 和 docs/plans/gui-implementation-task-breakdown.md 完善 P-Chat GUI。

必须使用最终采用设计图，不要使用旧图作为最终依据。不要另起视觉风格，不要删除现有功能入口，不要擅自增加业务模块。

主目标：
- 保持 Project-Aware Calm Workbench 风格。
- 保留项目、全局会话、项目内会话、新建项目、新建对话、搜索、模型、上下文、会话设置、构建、终端、权限等入口。
- thinking 独立，不进入 event-timeline。
- tool / skill / sub_agent 进入执行时间线。
- SubAgentCard 合并展示子代理类型和运行模式，展开态像嵌套对话列表。
- SubAgentJobsPanel 展示 subagent_type 文本，不重复加「类型」标签。
- Provider 请求协议显示 OpenAI Chat / Anthropic Messages，不显示 HTTP/HTTPS；保存值仍为 openai / anthropic。
- Base URL 独立展示网络地址。
- 模型编辑移除“功能标签”。

不要改后端 schema，不要改用户 ~/.p-chat/config.json，不要改变 SSE / parts 数据结构。所有颜色、间距、圆角、阴影、动效走 frontend/src/style.css tokens。

完成后运行：
cd frontend
npx vue-tsc -b
npm run build
```
