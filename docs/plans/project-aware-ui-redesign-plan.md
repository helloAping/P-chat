# Project-Aware UI Redesign Plan

> 状态：**产品视觉已锁定为 Project-Aware Calm Workbench**（规范真源：[`.agents/docs/frontend-design.md` §0.5 / §3.7–3.10 / §10](../../.agents/docs/frontend-design.md)）。主壳、thinking 独立块与执行时间线已成为后续 UI 约束；本计划作为历史背景、补充样式图与验收参考。
> 关联模块：[`frontend.md`](../../.agents/docs/frontend.md) / [`frontend-design.md`](../../.agents/docs/frontend-design.md)
> 设计图汇总：[`gui-design-reference.md`](gui-design-reference.md)
> 任务拆分：[`gui-implementation-task-breakdown.md`](gui-implementation-task-breakdown.md)
> 参考样例图：`C:\Users\admin\.codex\generated_images\01a09621-4be8-7343-8f14-aa85c77f5ec3\call_7lvhmzHXBAXo619BAr8tPxIC.png`
> 子代理补充图：`C:\Users\admin\.codex\generated_images\01a09621-4be8-7343-8f14-aa85c77f5ec3\call_wyrt6snK8obG8VrcBP3nRqGQ.png`
> 设置页补充图：`C:\Users\admin\.codex\generated_images\01a09621-4be8-7343-8f14-aa85c77f5ec3\call_CiTheEW9FZ595Jz6AS7wwBvM.png`

## 1. 背景

当前 UI 已具备聊天、项目、会话、模型、工具调用、设置等能力，但视觉层级偏散：

- 初版聊天工作台方案弱化了“项目”维度，无法充分表达新增项目、切换项目、查看项目内会话等核心工作流。
- 四列项目方案虽然补齐了项目能力，但栏位过多、蓝色过重、信息块拥挤，整体像管理后台，不适合日常聊天与开发协作。
- 最终确认方向是：保留完整项目能力，但用更轻的“单侧栏 + 项目切换器弹层”承载项目切换，不让项目列表长期挤占主聊天空间。

## 2. 目标风格

P-Chat 的主界面统一为 **Project-Aware Calm Workbench**：

- 气质：安静、克制、精致的桌面 AI 编程工作台，接近 Raycast / 原生生产力工具 / 轻量 IDE，而不是管理后台或营销页。
- 默认重心：用户每天最常看的聊天区和输入区，项目能力清楚可达但不喧宾夺主。
- 色彩：减少大面积高饱和蓝紫，只保留细 accent、状态点、发送按钮等关键操作色。
- 密度：信息完整但不堆叠，用分组、弹层、inspector 和弱分割线组织信息。
- 一致性：所有颜色、间距、圆角、阴影、动效继续走 `frontend/src/style.css` design tokens；不要写裸值。

## 3. 信息架构

核心规则：

1. 项目是一级上下文。
2. 会话是当前项目下的二级对象。
3. 全局会话是特殊空间，不属于任何项目，但必须始终可进入。
4. 新建项目、切换项目、项目内新建对话、搜索项目内会话都必须在默认界面中可发现。
5. 不使用长期可见的“四重竖栏”。项目列表通过侧栏顶部的 project switcher popover 展开。
6. 新建项目入口保留 AppRail 左下角快捷按钮，并在 ProjectSwitcher popover 内提供行入口；不在项目触发器旁重复放 `+` 挤压项目名。

## 4. 主布局

### 4.1 App Shell

- 保留 frameless titlebar / Wails 约束，不改变窗口关闭流程。
- 主体布局建议：
  - 左侧 app rail：约 52px。
  - 统一侧栏：使用 `--sidebar-width`，默认约占工作区 1/5，并支持拖拽收缩/展开；承载项目切换和当前项目会话。不恢复项目列表常驻独立列。
  - 中央聊天区：flex 自适应，默认约占工作区 3/5，消息阅读列居中。
  - 右侧 inspector：使用 `--inspector-width`，默认约占工作区 1/5；可折叠，默认可以打开但视觉必须轻。

### 4.2 左侧 App Rail

显示固定入口：

- P-Chat logo
- 全局
- 项目
- 设置
- 底部新建项目入口
- 底部关于入口（信息图标）

文件夹、终端这类当前项目动作不放在 AppRail 或 TopBar 中重复出现，统一放在 Inspector 的项目区域。

样式要求：

- active 状态用细线或轻背景表达，不用大面积蓝色块。
- 图标走 `frontend/src/components/icons/index.ts` barrel。
- UI chrome 不使用 emoji。

### 4.3 统一侧栏

侧栏顶部是项目切换器：

- 显示当前项目名，例如 `P-Chat`。
- 显示项目路径 / 分支 / 工作区状态等一行弱 metadata。
- 右侧有 chevron 表示可切换。
- 不在触发器旁放独立 `新建项目` 图标按钮，避免挤压项目名；左下 AppRail 快捷入口保留。

项目切换器展开后展示 popover：

- 搜索项目输入框：`搜索项目`
- `新建项目` 行
- `全局会话` 行
- 项目列表：项目名、路径；若项目数据提供分支 / 工作区状态，则显示分支文本和状态点
- 当前项目用 check / accent 标记

侧栏下方是当前项目的对话列表：

- 标题：`P-Chat 对话` 或 `{项目名} 对话`
- 紧凑 `新建对话` 按钮
- 搜索当前项目对话
- 分组：今天 / 本周 / 更早
- active 会话用轻背景、文字权重和状态点表达；不要使用左侧 brand 竖条或厚重整块高亮

## 5. 聊天区

### 5.1 顶部 Header

顶部 header 显示：

- 项目 breadcrumb：`P-Chat / 当前会话标题`
- 项目路径；若项目数据提供 git branch / 工作区状态，则显示对应 metadata
- 上下文占用 chip，例如 `4.5K / 64K`
- inspector / 工具 / 设置等轻量图标按钮

注意：

- 顶部不重复显示过多模型信息，模型入口仍以底部 composer 为主。
- header 高度要克制，避免压缩消息区。

### 5.2 消息流

- 中央消息列保持舒适宽度，不铺满全屏。
- Assistant 消息以头像 + 名称 + 时间 + 正文呈现，正文默认不套大卡片。
- User 消息右对齐，使用柔和浅蓝气泡，宽度受限。
- 附件保持中性卡片，不和用户文字气泡混在一起。

### 5.3 Thinking 独立块 + Tool / Skill / SubAgent 执行时间线

把当前重复的横条式展示拆成两类独立视觉对象：

- `thinking`：**独立块**，默认折叠，流式时可展开；用中性 surface tint，不进入工具时间线。
- `tool / skill / sub_agent`：统一进入执行时间线；小 spinner / 状态点 + 名称 + 简短状态。
- `tool ok`：绿色状态点，结果按现有规则折叠。
- `tool error`：细红色左边框 + 简短错误摘要，避免整块红色警告铺满。
- `skill`：与 tool 视觉同族，但明确显示 skill name / status。
- `sub_agent`：保留嵌套能力，作为执行时间线内的可展开任务行或独立子代理卡片；必须把后台子代理类型 `sub_agent_type` 与运行模式 `sub_agent_run_mode` 合并成紧凑 meta（例如 `frontend_agent · 后台`），展开后要像一个新的嵌套对话列表，而不是普通详情面板；运行态不能使用大面积 shimmer 或持续背景动画。

遵守现有性能约束：流式动画只用小 dot / spinner / caret，且只动画 `opacity` / `transform`。

## 6. 右侧 Inspector

右侧 inspector 是轻量信息面板，不要做成一堆厚卡片。

建议 tab：

- `会话`
- `项目`

项目 tab 至少包含：

- 当前项目名、路径；若项目数据提供分支 / 工作区状态，则显示对应 metadata
- `AGENTS.md` / 项目指令加载状态
- 模型与上下文摘要
- 工具调用成功 / 失败 / 运行中统计
- 项目路径复制等轻量信息动作；打开文件夹/终端等当前项目动作由 Inspector 项目区域承载，避免入口重复。

样式要求：

- 使用弱 section divider，而不是一层套一层的卡片。
- inspector 可以折叠；折叠后主聊天区自然扩展。

## 7. 底部 Composer

底部输入区保持现有功能，但视觉更轻：

- 第一行：附件按钮、输入框、发送按钮。
- 第二行：模型选择、项目模式、会话设置、构建、终端、权限/静音等现有控制。
- 快捷键提示放右侧，用弱色显示。
- 不破坏 `.agents/docs/frontend-design.md` 中的 Composer Dock Stack 顺序：
  - QuestionModal
  - TodoPanel
  - SubAgentJobsPanel
  - TurnQueue
  - InputArea

## 8. 实现拆分

### 阶段 1：盘点现有功能与数据流

目标：确认现有项目、会话、全局会话、新建项目、新建对话、设置入口的真实组件和 store 字段。

重点文件：

- `frontend/src/App.vue`
- `frontend/src/components/SessionSidebar.vue`
- `frontend/src/components/ChatWindow.vue`
- `frontend/src/components/InputArea.vue`
- `frontend/src/components/TopBar.vue`
- `frontend/src/components/MessageBubble.vue`
- `frontend/src/components/ThinkingBlock.vue`
- `frontend/src/components/ToolCallCard.vue`
- `frontend/src/components/SkillCallCard.vue`
- `frontend/src/components/SubAgentCard.vue`
- `frontend/src/stores/chat.ts`
- `frontend/src/style.css`

输出：列出“必须保留的现有功能清单”和对应组件入口。

### 阶段 2：建立统一侧栏与项目切换器

目标：把项目切换、新建项目、全局会话、项目内会话收进一个统一侧栏。

建议：

- 在 `SessionSidebar.vue` 内重构，或拆出 `ProjectSwitcher.vue` / `ProjectMenu.vue` / `ConversationList.vue`。
- 默认侧栏只显示当前项目和该项目会话。
- 项目列表只在 switcher popover 中展开。
- 全局会话作为 switcher popover 内的固定特殊入口，同时在 rail 的 `全局` 入口可达。

验收：

- 用户能在默认界面看到当前项目。
- 用户能明显找到 `新建项目`。
- 用户能切换项目。
- 切换项目后，会话列表只展示对应项目下的对话。
- 用户能进入全局会话。

### 阶段 3：聊天区视觉重排

目标：让主聊天区从“普通消息流”升级为精致工作台阅读区。

建议：

- 调整 `ChatWindow.vue` 的消息列宽、padding、顶部 header 关系。
- 保持 `.chat-main` / `.messages-scroll` / `InputArea` flex 约束不变。
- 调整 `MessageBubble.vue` 的 assistant / user 信息层级。
- 附件卡片和消息操作栏保持稳定底部基线。

验收：

- 消息区不会横向过宽。
- 输入框不会漂移或被挤压。
- 用户气泡、assistant 文本、附件卡片互不重叠。
- 亮色和暗色主题都可读。

### 阶段 4：Thinking 独立块与执行时间线

目标：让思考过程从工具调用中拆分出来，同时统一 tool / skill / sub-agent 的执行记录视觉语言。

建议：

- 在 `MessageBubble.vue` 内按 segment 渲染：`ThinkingBlock` 独立显示；`ToolCallCard` / `SkillCallCard` / `SubAgentCard` 进入 `.event-timeline`。
- 先改样式和布局，不改变 SSE parts 数据结构。
- 保留长 tool result 折叠、复制、dry-run、媒体结果展示等现有能力。
- `SubAgentCard` 折叠态合并显示子代理类型与后台/同步模式，并展示任务标题、状态和耗时；不要额外展示首字母图标、`类型` badge、`后台子代理` badge。展开态显示子代理自己的消息列表、独立 thinking、工具/技能记录和结果摘要。

验收：

- thinking、tool、skill、sub-agent 都能正确显示。
- thinking 不与 tool / skill / sub-agent 组合，不进入 `.event-timeline`。
- 子代理卡片能展示后台子代理类型；展开后像嵌套对话列表，不是单个详情卡片。
- tool error 是克制错误态，不是大面积警告块。
- 流式时 GPU 友好，无大面积持续动画。

### 阶段 5：右侧 Inspector

目标：把会话和项目 metadata 放进可折叠 inspector，减少主聊天区负担。

建议：

- 优先复用已有 context / project / tool state，避免新增后端字段。
- 如果当前项目数据不足，先做只读摘要和快捷入口。
- 使用轻分隔线和紧凑 section，不做多层卡片。

验收：

- inspector 折叠后主区布局正常。
- 会话 tab 与项目 tab 都有明确内容。
- 项目指令、上下文、工具统计、项目路径复制可见。

### 阶段 6：供应商与模型设置语义修正

目标：让设置页文案准确表达真实配置含义，避免把 LLM 端点协议误写成网络协议。

建议：

- 供应商表单里的「协议」改为「请求协议」或「LLM 协议」。
- 选项展示 `OpenAI Chat` / `Anthropic Messages`；配置值继续沿用后端约束 `openai` / `anthropic`。
- `Base URL` 才展示 `http://` / `https://`；端点路径可以用只读提示展示 `/chat/completions` 或 `/messages`。
- 模型添加/编辑弹窗移除「功能标签」字段；保留模型 ID、显示名、上下文、输出上限、启用状态等必要字段。

验收：

- 设置页不会出现「访问协议 HTTPS」这类误导文案。
- 用户能清楚区分 Base URL 的网络地址和 LLM 请求协议。
- 模型弹窗没有「功能标签」区域。

### 阶段 7：视觉收敛与验证

目标：把主界面统一到最终风格，不留下旧样式断层。

必须检查：

- 没有硬编码颜色、间距、圆角、阴影、动效。
- 新增 token 同时覆盖 light / dark。
- 图标从 barrel 导入。
- UI chrome 没有 emoji。
- 没有 NScrollbar 替换聊天滚动容器。
- 没有破坏 Composer Dock Stack。

验证命令：

```powershell
cd frontend
npx vue-tsc -b
npm run build
```

手动验证：

1. 新建项目。
2. 切换项目。
3. 切到不同项目后只看到该项目下会话。
4. 进入全局会话。
5. 新建项目内对话。
6. 发送普通消息。
7. 发送触发 thinking / tool / error / skill / sub-agent 的消息，确认 thinking 独立显示。
8. 触发后台子代理，确认紧凑类型 meta 与嵌套对话展开态。
9. 检查供应商设置页：请求协议为 OpenAI Chat / Anthropic Messages，Base URL 独立展示，模型弹窗无功能标签。
10. 展开/折叠右侧 inspector。
11. 验证亮色与暗色主题。
12. 验证窄窗口下侧栏和 inspector 不遮挡输入区。

## 9. 非目标

本轮不做：

- 后端项目模型重写。
- SQLite schema 迁移。
- LLM / SSE 协议变更。
- 修改用户 `~/.p-chat/config.json`。
- 删除现有功能或隐藏无法替代的入口。
- 用全新 UI 库替换 Naive UI。

## 10. 给其他 Agent 的执行提示词

```text
你要在 P-Chat 项目中实现一次项目感知的主界面 UI 重构。请先严格执行项目根目录 AGENTS.md 的启动检查，并阅读：

- .agents/AGENTS.md
- .agents/docs/frontend.md
- .agents/docs/frontend-design.md
- docs/plans/project-aware-ui-redesign-plan.md
- docs/plans/gui-design-reference.md
- docs/plans/gui-implementation-task-breakdown.md

目标风格是 Project-Aware Calm Workbench：安静、克制、精致的桌面 AI 编程工作台，接近 Raycast / 原生生产力工具 / 轻量 IDE。不要做成管理后台，不要复刻四列拥挤布局，不要使用大面积高饱和蓝紫。

核心信息架构：

1. 项目是一级上下文。
2. 会话是当前项目下的二级对象。
3. 全局会话是特殊空间，必须始终可进入。
4. 新建项目、切换项目、项目内新建对话、搜索项目内会话都必须在默认界面中可发现。
5. 使用“52px app rail + 可拖拽统一侧栏 + 中央聊天区 + 可折叠右侧 inspector”的布局；默认展开比例约为 `SessionSidebar : ChatWindow : InspectorPanel = 1 : 3 : 1`。
6. 项目列表不要长期占一整列；放到统一侧栏顶部的 project switcher popover 中。

必须保留并展示的现有能力：

- 新建项目
- 切换项目
- 全局会话
- 当前项目及其路径/分支/状态
- 当前项目内会话列表
- 新建对话
- 搜索会话
- 聊天消息流
- thinking/tool/skill/sub-agent parts
- tool error / tool result / media result 展示
- 子代理类型 `sub_agent_type` / `subagent_type` 与后台运行模式展示
- 模型选择
- 上下文占用
- 会话设置
- 供应商请求协议设置（OpenAI Chat / Anthropic Messages，不是 HTTP/HTTPS）
- 构建 / 终端 / 权限等底部控制
- 项目指令或 AGENTS.md 加载状态

实现建议：

1. 先盘点现有组件和 store 字段，不要凭空改数据模型。
2. 优先在 SessionSidebar.vue 中重构统一侧栏；必要时拆 ProjectSwitcher.vue、ProjectMenu.vue、ConversationList.vue。
3. 保持 ChatWindow.vue 的 flex 布局铁律：chat-main 不加 max-height，messages-scroll 保持 min-height: 0 和 overflow-y: auto，InputArea 保持 flex-shrink: 0，不使用 NScrollbar。
4. 顶部 header 显示项目 breadcrumb、路径、上下文 chip 和 inspector toggle；branch / 工作区状态只有在项目数据提供时展示。
5. MessageBubble.vue 中 assistant 文本默认不套厚重卡片；user 消息右对齐柔和气泡。
6. ThinkingBlock 必须独立显示；ToolCallCard / SkillCallCard / SubAgentCard 统一为紧凑执行时间线视觉；SubAgentCard 必须合并展示子代理类型和后台/同步运行模式，展开态像嵌套对话列表；不要改变 SSE parts 数据结构。
7. 右侧 inspector 用轻量 section 展示会话/项目信息；项目动作放在项目区域内，不要多层卡片嵌套，也不要和 AppRail / TopBar 重复堆叠入口。
8. 底部 InputArea 保留现有 composer dock stack 和控件，只做视觉收敛。
9. 设置页供应商「协议」表示 LLM 请求协议，选项显示 OpenAI Chat / Anthropic Messages；Base URL 独立展示网络地址，模型弹窗移除「功能标签」。

强约束：

- 所有颜色、间距、圆角、阴影、动效必须走 frontend/src/style.css tokens。
- 如需新增 token，必须同时写入 :root[data-theme="dark"] 和 :root[data-theme="light"]。
- 图标必须从 frontend/src/components/icons/index.ts barrel 导入，不要直接从 lucide-vue-next 导入。
- CSS 写在组件 scoped style 中，除非是全局 token 或共享样式。
- UI chrome 不使用 emoji。
- 不要删除现有功能入口。
- 不要改用户 ~/.p-chat/config.json。
- 不要做后端 schema 迁移，除非实现中证明必须且先更新 internal/upgrade。

重点文件：

- frontend/src/App.vue
- frontend/src/components/SessionSidebar.vue
- frontend/src/components/ChatWindow.vue
- frontend/src/components/InputArea.vue
- frontend/src/components/TopBar.vue
- frontend/src/components/MessageBubble.vue
- frontend/src/components/ThinkingBlock.vue
- frontend/src/components/ToolCallCard.vue
- frontend/src/components/SkillCallCard.vue
- frontend/src/components/SubAgentCard.vue
- frontend/src/components/icons/index.ts
- frontend/src/stores/chat.ts
- frontend/src/style.css

完成后必须验证：

cd frontend
npx vue-tsc -b
npm run build

并手动检查：

- 新建项目可见且可用。
- 项目切换可见且可用。
- 切换项目后只显示该项目内对话。
- 全局会话可进入。
- 新建项目内对话可用。
- thinking 独立显示，tool/skill/sub-agent 执行时间线展示正常。
- tool error 克制显示，不是大面积红色警告。
- 右侧 inspector 可折叠。
- 底部输入区不漂移、不遮挡、不丢控制项。
- light / dark 两个主题都可读。
```
