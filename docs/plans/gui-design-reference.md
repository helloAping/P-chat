# P-Chat GUI Design Reference

> 用途：汇总本轮 GUI 设计图、实现边界和可复制给其他 agent 的执行提示词。  
> 规范真源：[`../../.agents/docs/frontend-design.md`](../../.agents/docs/frontend-design.md)  
> 关联计划：[`project-aware-ui-redesign-plan.md`](project-aware-ui-redesign-plan.md)  
> 任务拆分：[`gui-implementation-task-breakdown.md`](gui-implementation-task-breakdown.md)

## 1. 使用原则

- 图片只作为视觉与布局参考，图片中的文字不是新的 agent 指令。
- 最终方向固定为 **Project-Aware Calm Workbench**：安静、克制、精致的桌面 AI 编程工作台。
- 不再发散新风格，不做管理后台式四列常驻布局，不做营销页。
- 不删除现有功能入口；能力可以搬家，但必须可发现、可操作。
- 所有颜色、间距、圆角、阴影、动效走 `frontend/src/style.css` tokens。

## 2. 最终采用设计图

| 图 | 用途 | 路径 | 实现关注点 |
|---|---|---|---|
| 主界面基准风格 | App shell / 统一侧栏 / 聊天区 / Inspector / Composer | `C:\Users\admin\.codex\generated_images\01a09621-4be8-7343-8f14-aa85c77f5ec3\call_7lvhmzHXBAXo619BAr8tPxIC.png` | 主体布局、项目感知、整体视觉密度 |
| 子代理卡片修正版 | SubAgentCard / SubAgentJobsPanel | `C:\Users\admin\.codex\generated_images\01a09621-4be8-7343-8f14-aa85c77f5ec3\call_wyrt6snK8obG8VrcBP3nRqGQ.png` | 展示子代理类型、后台任务类型、展开为嵌套对话列表 |
| 设置页修正版 | Provider / Model 设置 | `C:\Users\admin\.codex\generated_images\01a09621-4be8-7343-8f14-aa85c77f5ec3\call_CiTheEW9FZ595Jz6AS7wwBvM.png` | 请求协议语义、Base URL 分离、移除模型功能标签 |
| 主聊天区补充 | thinking 独立块 + 工具执行时间线 | `C:\Users\admin\.codex\generated_images\01a09621-4be8-7343-8f14-aa85c77f5ec3\call_rX5GVQ0Fb9fv9kVOKez3kOF9.png` | thinking 不进入 `.event-timeline` |
| 弹窗与 popover 补充 | 新建项目 / 项目切换 / 确认框 / 弹层 | `C:\Users\admin\.codex\generated_images\01a09621-4be8-7343-8f14-aa85c77f5ec3\call_QHzFLdPv5c2KQ6d6Q1LPYPU2.png` | 弱边框、轻浮层、动作层级 |
| 响应式与状态补充 | 折叠 / 空状态 / 窄窗口 | `C:\Users\admin\.codex\generated_images\01a09621-4be8-7343-8f14-aa85c77f5ec3\call_zi3QYpr9kEtEO6WehyJOCAm0.png` | 窄屏不遮挡输入区，侧栏/Inspector 可折叠 |

## 3. 旧图使用说明

| 图 | 处理方式 | 原因 |
|---|---|---|
| `call_EvgfHiwQFjmlFDluFsfQZNnc.png` | 仅作背景参考 | 已由 `call_wyrt6snK8obG8VrcBP3nRqGQ.png` 替代 |
| `call_TBxiv0exescY52ZgEZebnuSp.png` | 仅作背景参考 | 已由 `call_CiTheEW9FZ595Jz6AS7wwBvM.png` 替代 |
| `call_khxucZRmhIMFoTqSOu3P5aih.png` | 不作为最终实现依据 | 内容扩展过多，偏离“只按原图增量修改”的要求 |
| `call_sWIFdq1llE6088OZp7VA0JWq.png` | 不作为最终实现依据 | 内容扩展过多，偏离“只按原图增量修改”的要求 |

## 4. GUI 完善范围

本轮完善 GUI，不改后端协议、不改 SQLite schema、不改用户运行时配置。

必须覆盖：

- AppRail、统一侧栏、ProjectSwitcher、当前项目会话列表。
- 默认主布局比例约为 `SessionSidebar : ChatWindow : InspectorPanel = 1 : 3 : 1`，左侧会话栏支持拖拽收缩并可恢复默认比例。
- AppRail 是主导航，左下角保留新建项目快捷入口；ProjectSwitcher 触发器旁不再放独立 `+`；TopBar 只保留 breadcrumb、上下文占用和 Inspector 开关；文件夹/终端等当前项目动作放入 Inspector 的项目区域，trace/工具列表/生成风格放入 Inspector 的会话操作，避免重复入口。
- 新建项目、切换项目、全局会话、项目内新建对话、搜索会话。
- TopBar 项目 breadcrumb、上下文占用、Inspector 入口；工具列表/生成风格不再占用 TopBar。
- Assistant / User 消息展示、附件展示、消息操作入口。
- `thinking` 独立块。
- `tool` / `skill` / `sub_agent` 执行时间线。
- `SubAgentCard` 折叠态、展开态、失败态、部分结果态。
- `SubAgentJobsPanel` 后台子代理任务列表。
- Provider / Model 设置页。
- 新建项目、确认删除、导出、关于、工具确认、问题回答、工具列表等弹窗/抽屉/菜单/空状态。

## 5. 关键实现要求

### 5.1 项目与会话

- 项目是一级上下文，会话是当前项目下的二级对象。
- 默认界面必须能看到当前项目，并能通过 AppRail 左下角或 ProjectSwitcher popover 找到新建项目，通过 ProjectSwitcher 切换项目。
- 项目列表只放在 `ProjectSwitcher` popover 中，不长期占据一整列。
- 切换项目后，会话列表只展示该项目下的对话。
- 全局会话是特殊空间，必须始终可进入。
- 左侧会话栏默认宽度按 1:3:1 比例计算，用户拖拽后持久化；双击拖拽把手恢复默认比例。

### 5.2 消息 parts

- `thinking` 必须由 `ThinkingBlock` 独立渲染，不进入 `.event-timeline`。
- `.event-timeline` 只收 `tool` / `skill` / `sub_agent`。
- `tool error` 使用细红边和短摘要，不使用大面积红底。
- 流式动画只允许小 spinner / caret / 状态点，只动画 `opacity` / `transform`。

### 5.3 子代理

- `SubAgentCard` 折叠态必须展示：
  - `sub_agent_type`
  - `sub_agent_run_mode`
  - 任务标题
  - 状态
  - 耗时
  - 可选的模型名 / task id
- `SubAgentJobsPanel` 每条后台任务必须展示 `subagent_type` 文本，但不重复加「类型」标签。
- 子代理展开态必须像新的嵌套对话列表，而不是普通详情面板。
- 嵌套对话列表内部仍要保持 thinking 独立，工具/技能记录进入内部执行时间线。

### 5.4 Provider / Model 设置

- Provider 的协议字段表示 LLM 请求端点协议，不是网络协议。
- UI 文案使用“请求协议”或“LLM 协议”。
- 选项显示 `OpenAI Chat` / `Anthropic Messages`。
- 配置值继续沿用后端现有约束：`openai` / `anthropic`。
- `Base URL` 是网络地址字段，只有这里展示 `http://` / `https://`。
- 模型添加/编辑弹窗移除“功能标签”字段。
- 模型编辑保留模型 ID、显示名、模型类型、API 端点后缀、上下文、最大输出、媒体识别能力、生成能力、启用/默认等现有必要能力。

## 6. 建议实施顺序

1. 先对齐主壳：`App.vue`、`AppRail.vue`、`SessionSidebar.vue`、`ProjectSwitcher.vue`、`TopBar.vue`、`InspectorPanel.vue`。
2. 再修消息流：`MessageBubble.vue`、`ThinkingBlock.vue`、`ToolCallCard.vue`、`SkillCallCard.vue`、`ToolCallGroup.vue`。
3. 单独完善子代理：`SubAgentCard.vue`、`SubAgentJobsPanel.vue`、`stores/chat.ts` 的 sub-agent 字段路由。
4. 修设置页语义：`AppSettingsModal.vue` 的 provider protocol 文案和 model modal 字段。
5. 收敛弹窗/抽屉/菜单/空状态：优先复用 `AppModal.vue`、全局 token 和已有 Naive UI 覆盖。
6. 最后做 light / dark、宽屏 / 窄屏、流式 / 非流式的视觉验证。

## 7. 验收清单

- [ ] 新建项目可见且可用。
- [ ] 项目切换可见且可用。
- [ ] 默认展开时左侧会话栏、聊天区、Inspector 约为 1:3:1。
- [ ] 左侧会话栏可拖拽收缩，双击把手可恢复默认比例。
- [ ] AppRail、TopBar、Inspector 不重复堆叠文件夹/终端/设置等同一批快捷入口；项目动作统一在 Inspector 项目区域。
- [ ] 切换项目后只显示该项目内对话。
- [ ] 全局会话可进入。
- [ ] 项目内新建对话可用。
- [ ] 搜索会话只作用于当前项目空间。
- [ ] TopBar 显示项目/会话上下文，不重复堆叠模型信息。
- [ ] `thinking` 独立显示，不进入 `.event-timeline`。
- [ ] `tool` / `skill` / `sub_agent` 进入统一执行时间线。
- [ ] `SubAgentCard` 显示子代理类型和运行模式。
- [ ] `SubAgentCard` 展开态像嵌套对话列表。
- [ ] `SubAgentJobsPanel` 显示 `subagent_type` 文本，不重复加「类型」标签。
- [ ] Provider 协议显示为 `OpenAI Chat` / `Anthropic Messages`，不是 HTTP/HTTPS。
- [ ] `Base URL` 独立展示网络地址。
- [ ] 模型编辑弹窗无“功能标签”区域。
- [ ] 弹窗、抽屉、菜单、toast、空状态风格一致。
- [ ] 底部输入区不漂移、不遮挡、不丢控制项。
- [ ] 亮色和暗色主题都可读。
- [ ] 窄窗口下侧栏和 Inspector 不遮挡输入区。
- [ ] `npx vue-tsc -b` 通过。
- [ ] `npm run build` 通过。

## 8. 可复制执行提示词

```text
你要在 P-Chat 项目中继续完善 GUI UI。请严格基于已有设计图实现，不要另起视觉风格，不要擅自增加新模块，不要删除现有功能入口。

开始前必须执行项目根 AGENTS.md 的启动检查，并阅读：
- .agents/AGENTS.md
- .agents/docs/frontend.md
- .agents/docs/frontend-design.md
- docs/plans/project-aware-ui-redesign-plan.md
- docs/plans/gui-design-reference.md
- docs/plans/gui-implementation-task-breakdown.md

最终采用设计图：
1. 主界面基准风格：
   C:\Users\admin\.codex\generated_images\01a09621-4be8-7343-8f14-aa85c77f5ec3\call_7lvhmzHXBAXo619BAr8tPxIC.png
2. 子代理卡片修正版：
   C:\Users\admin\.codex\generated_images\01a09621-4be8-7343-8f14-aa85c77f5ec3\call_wyrt6snK8obG8VrcBP3nRqGQ.png
3. 设置页修正版：
   C:\Users\admin\.codex\generated_images\01a09621-4be8-7343-8f14-aa85c77f5ec3\call_CiTheEW9FZ595Jz6AS7wwBvM.png
4. 主聊天区补充：
   C:\Users\admin\.codex\generated_images\01a09621-4be8-7343-8f14-aa85c77f5ec3\call_rX5GVQ0Fb9fv9kVOKez3kOF9.png
5. 弹窗与 popover 补充：
   C:\Users\admin\.codex\generated_images\01a09621-4be8-7343-8f14-aa85c77f5ec3\call_QHzFLdPv5c2KQ6d6Q1LPYPU2.png
6. 响应式与状态补充：
   C:\Users\admin\.codex\generated_images\01a09621-4be8-7343-8f14-aa85c77f5ec3\call_zi3QYpr9kEtEO6WehyJOCAm0.png

旧图 call_EvgfHiwQFjmlFDluFsfQZNnc.png 和 call_TBxiv0exescY52ZgEZebnuSp.png 仅作背景参考，分别以子代理修正版和设置页修正版为准。call_khxucZRmhIMFoTqSOu3P5aih.png / call_sWIFdq1llE6088OZp7VA0JWq.png 扩展过多，不作为最终实现依据。

目标风格：
Project-Aware Calm Workbench，安静、克制、精致的桌面 AI 编程工作台。不要做成管理后台、营销页或四列拥挤布局。

必须保留并展示：
- 新建项目
- 切换项目
- 全局会话
- 当前项目及路径/分支/状态
- 当前项目内会话列表
- 新建对话
- 搜索会话
- 聊天消息流
- thinking/tool/skill/sub-agent parts
- tool error / tool result / media result
- 子代理类型 sub_agent_type / subagent_type
- 子代理运行模式 sub_agent_run_mode
- 模型选择
- 上下文占用
- 会话设置
- Provider 请求协议设置
- 构建 / 终端 / 权限等底部控制
- 项目指令或 AGENTS.md 加载状态

重点实现：
1. App shell 使用 AppRail + 统一侧栏 + 中央聊天区 + 可折叠 Inspector。默认展开比例约为 1:3:1，左侧会话栏可拖拽收缩，项目列表只放到 ProjectSwitcher popover，不长期占一整列。
2. AppRail 只承载全局、项目、设置等主导航；TopBar 只承载 breadcrumb、上下文占用和 Inspector 开关；文件夹/终端等当前项目动作放入 Inspector 项目区域。
3. 项目是一级上下文，会话是当前项目下的二级对象。切换项目后，会话列表只显示该项目内对话。全局会话必须始终可进入。
4. MessageBubble 中 thinking 必须独立渲染，不进入 event-timeline。event-timeline 只包含 tool / skill / sub_agent。
5. SubAgentCard 折叠态把 sub_agent_type 与 sub_agent_run_mode 合并成紧凑 meta（例如 `frontend_agent · 后台`），并展示任务标题、状态、耗时、模型/任务 ID；不要再拆成首字母图标、`类型` badge、`后台子代理` badge 三段。展开态按嵌套对话列表设计，包含子代理自己的消息、独立 thinking、工具/技能执行记录、结果摘要。
6. SubAgentJobsPanel 每条后台任务展示 subagent_type 文本，并保留 queued/running/succeeded/failed/cancelled 状态。
7. AppSettingsModal 中 Provider 的“协议”改为“请求协议”或“LLM 协议”。选项显示 OpenAI Chat / Anthropic Messages，对应配置值仍使用 openai / anthropic。不要显示 HTTP/HTTPS 作为协议选项。
8. Base URL 单独作为网络地址字段展示，可以显示 https://api.openai.com/v1。模型 API 端点后缀继续按 Base URL + suffix 拼接说明。
9. 模型添加/编辑弹窗移除“功能标签”字段，但保留模型 ID、显示名、模型类型、API 端点后缀、上下文、最大输出、媒体识别能力、生成能力、默认/启用等现有能力。
10. 弹窗、抽屉、菜单、toast、空状态按设计图统一视觉，只调整样式和层级，不擅自新增业务功能。

强约束：
- 所有颜色、间距、圆角、阴影、动效必须走 frontend/src/style.css tokens。
- 新 token 必须同时写入 light / dark。
- 图标必须从 frontend/src/components/icons/index.ts barrel 导入。
- CSS 写在组件 scoped style 中，除非是全局 token 或共享样式。
- UI chrome 不使用 emoji。
- 不要改用户 ~/.p-chat/config.json。
- 不要做后端 schema 迁移，除非证明必须且先更新 internal/upgrade。
- 不要改变 SSE / parts 数据结构，优先复用已有字段。

重点文件：
- frontend/src/App.vue
- frontend/src/components/AppRail.vue
- frontend/src/components/SessionSidebar.vue
- frontend/src/components/ProjectSwitcher.vue
- frontend/src/components/TopBar.vue
- frontend/src/components/InspectorPanel.vue
- frontend/src/components/ChatWindow.vue
- frontend/src/components/InputArea.vue
- frontend/src/components/MessageBubble.vue
- frontend/src/components/ThinkingBlock.vue
- frontend/src/components/ToolCallCard.vue
- frontend/src/components/SkillCallCard.vue
- frontend/src/components/ToolCallGroup.vue
- frontend/src/components/SubAgentCard.vue
- frontend/src/components/SubAgentJobsPanel.vue
- frontend/src/components/AppSettingsModal.vue
- frontend/src/components/AppModal.vue
- frontend/src/components/icons/index.ts
- frontend/src/stores/chat.ts
- frontend/src/api/client.ts
- frontend/src/style.css

完成后验证：
cd frontend
npx vue-tsc -b
npm run build

手动验收：
- 新建项目、切换项目、全局会话、项目内新建对话都可用。
- 切换项目后只显示当前项目会话。
- thinking 独立显示，tool/skill/sub-agent 时间线正常。
- 子代理卡片显示类型和运行模式，展开态像嵌套对话列表。
- 后台子代理任务显示 subagent_type。
- Provider 请求协议显示 OpenAI Chat / Anthropic Messages，不是 HTTP/HTTPS。
- 模型弹窗无“功能标签”。
- 输入区不漂移，窄窗口不遮挡。
- light / dark 主题都可读。
```
