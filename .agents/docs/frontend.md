# Frontend 模块（Vue 3 前端）

> **位置**：`frontend/src/`  
> **技术栈**：Vue 3 + Vite + Pinia + Naive UI + marked  
> **后端通信**：HTTP REST (JSON) + SSE (Server-Sent Events)
>
> **样式规范**：见 [frontend-design.md](frontend-design.md) —— 设计 token、组件规则、强制约束都在那边

## 概述

P-Chat 的浏览器端 GUI，提供会话列表、聊天窗口、子代理卡片、问题模态框、设置面板等功能。与 pchat-server 通过同一域名的 `/api/v1/*` 端点通信，SSE 流通过 `fetch()` + `ReadableStream` 消费。

用户向 GUI 操作流程统一写在项目根目录 `README.md` 的「GUI 操作入口速查」和「常见问题」里，例如浏览器控制安装与连接诊断、知识库选择、工作模式切换、风格关闭、工具列表、动态工具 YAML 加载诊断和 trace id 复制。本文件只记录前端实现结构和维护入口。

## 文件结构

| 路径 | 职责 | 关键导出/组件 |
|---|---|---|
| `api/client.ts` | HTTP+SSE 客户端、类型定义 | `StreamEvent`, `MessagePart`, `streamMessages()`, `sendMessage()` |
| `stores/chat.ts` | Pinia 状态管理（会话、消息、流） | `appendStreamEvent()`, `useChatStore()` |
| `utils/markdownCache.ts` | 共享 markdown 渲染 LRU 缓存（MessageBubble / SubAgentCard 共用） | `renderMarkdown()` |
| `utils/mediaPreview.ts` | data URL 转短生命周期 blob URL，统一保留正确媒体 MIME | `dataURLToBlobURL()` |
| `main.ts` | 应用入口（Naive UI + Router） | `createApp()` |
| `App.vue` | 根布局（侧边栏 + 聊天区域） | |
| `components/ChatWindow.vue` | 聊天窗口（消息列表 + 输入区） | |
| `components/InputArea.vue` | 输入区域（文本、附件、计划模式、会话设置） | 风格/知识库下拉选择器 + 回合队列 |
| `components/MessageBubble.vue` | 单条消息渲染（parts[] 迭代） | |
| `components/TypedText.vue` | 流式文本渲染（blinking caret） | |
| `components/ThinkingBlock.vue` | 思考块（可折叠） | |
| `components/ToolCallCard.vue` | 工具调用卡片（name/args/status/result） | |
| `components/SubAgentCard.vue` | 子代理嵌套卡片（header + inner parts） | |
| `components/SessionSidebar.vue` | 会话列表 + 项目选择器 + 设置 | |
| `components/CommandPalette.vue` | `/` 斜杠命令内联自动补全 | |
| `components/TodoPanel.vue` | 待办事项面板（交互式） | |
| `components/QuestionModal.vue` | 多选问题对话框 | |
| `components/StreamingBar.vue` | 对话页顶部"对话进行中"加载条（transform 滚动渐变，compositor-only） | 绑定 `isStreaming`/`currentSessionWorking` |
| `components/ImageLightbox.vue` | 全屏图片查看器 | |
| `components/LoadingDots.vue` | 子代理加载指示器 | |
| `components/AppSettingsModal.vue` | Provider/Model/Style/知识库管理 | 左右分栏 + provider/model 测试 + KB 三层树视图 + NCollapse |

## 核心概念

### 1. Parts 模型（MessagePart）

Assistant 消息使用 `parts[]` 数组，每项一个逻辑单元：

| Kind | 组件 | 说明 |
|---|---|---|
| `text` | TypedText / static md | 流式期间用 TypedText（blinking caret）；完成后用 marked.parse |
| `thinking` | ThinkingBlock | 可折叠面板，流式期间默认展开 |
| `tool` | ToolCallCard | name, args, status(start/ok/error), result, elapsed |
| `sub_agent` | SubAgentCard | 嵌套卡片，含自身 parts[] |

User/System 消息使用旧版 `content` 字符串 → `marked.parse()`。

### 2. 流式数据流

```
pchat-server SSE → fetch() ReadableStream reader
  → 逐行解析 "data: {...}\n\n"
  → JSON.parse → StreamEvent
  → await setTimeout(0)  ← 关键！强制 Vue 在两帧之间 flush
  → appendStreamEvent(id, ev)
    → 路由到匹配的 part (text/thinking/tool/sub_agent)
    → Vue 响应式 → DOM 更新
```

**`setTimeout(0)` 的重要性**：防止同一 TCP 包内的多事件被 Vue 批量合并为一帧渲染（导致文本一次性出现，失去打字机效果）。

### 3. appendStreamEvent — 事件路由 (chat.ts:828-1103)

```typescript
switch (ev.type) {
    case 'content':        // 追加文本增量 (含 sub_agent 路由)
    case 'content_rewrite': // 服务端后处理重写（phantom error 清洗）
    case 'thinking':        // 追加思考增量
    case 'thinking_rewrite': // 思考块重写
    case 'tool':            // tool_status: start/ok/error → ToolCallCard
    case 'phase':           // sub_agent_status → SubAgentCard 状态
    case 'session_status':  // busy/idle/retry → 全局流状态
    case 'done':            // token 计数 + 安全网 (walkParts → force err)
    case 'question':        // JSON → QuestionModal
    case 'tool_confirm':    // 沙箱确认对话框
    case 'error':           // 错误文本 + vision_unsupported 标记
}
```

### 4. SubAgent 事件路由

子代理事件通过 `ev.sub_agent` + `ev.sub_agent_task` 路由：

```typescript
let sub = null
if (ev.sub_agent && ev.sub_agent_task) {
    sub = findOrCreateSubAgent(m, ev.sub_agent_task, ev)
}
// content/thinking/tool 事件 → appendToSubAgent(sub, m, mutator)
// phase 事件 → sub.status = ev.sub_agent_status
```

### 5. Done 事件安全网

父级 Done 事件触发时，遍历所有 sub_agent parts，将仍处于 "start" 状态的强制改为 "err"。这是最后的防御——如果子代理关闭事件丢失，卡片不会永久卡在"运行中"。

### 6. Pinia Store (chat.ts)

核心状态：
- `currentID` — 当前活动会话 ID
- `sessionMessages[id]` — 消息列表
- `sessionMeta[id].style` / `sessionMeta[id].workMode` — 单会话说话风格与工作侧重点；`style=off` 表示关闭风格 prompt 和风格记忆注入
- `sessionMeta[id].enabled_recognition_capabilities` — 会话启用的媒体能力工具（`image` / `video` / `audio`）；新会话只继承仍有可用系统路由的选项
- `recognitionCapabilitiesAvailable` — `/api/v1/config` 返回的可用媒体识别路由缓存，用于过滤会话多选项
- `globalWorkMode` — `/api/v1/config.work_mode.default` 的前端缓存，新建/旧会话无覆盖时回落使用
- `streaming[id]` — 是否正在流式传输
- `sessionWorking[id]` — 是否忙碌（TodoPanel 控制）
- `sessionTodos[id]` — 待办列表
- `pendingQuestion[id]` — 待回答问题（QuestionModal）
- `pendingConfirm[id]` — 沙箱确认

### 7. Phantom Error 过滤

客户端防幻影错误（"Cannot read ... Inform the user"）：
- `PHANTOM_RE` 正则匹配
- 在文本增量追加时实时过滤
- 在 Done 事件时全量扫描

### 8. 知识库三层树视图

`AppSettingsModal.vue` 知识库 Tab 新增三层索引树视图：

```
API: GET /api/v1/knowledge/bases/:name/nodes → NodeTreeItem[]
      GET /api/v1/knowledge/bases/:name/nodes/:id/content → NodeContentItem[]

渲染层次:
  L1 概览卡片 → kb-config-card (base overview)
  L2 文件节点 → 可点击行，箭头旋转动画
    ├── 展开 → 加载 children (getChildren(pid)) + 内容块 (getNodeContent)
    └── L3 章节节点 → title + overview + content_count
          └── 内容块 → <pre> 代码段

状态管理:
  kbNodes: NodeTreeItem[]       — 扁平节点列表
  kbExpandedNodes: Set<number>  — 已展开的节点 ID
  kbNodeContents: Map<number, NodeContentItem[]> — 节点内容缓存
```

原始条目卡片 (`wiki_sections`) 保留在树视图下方作为向后兼容层。

### 8.1 Provider / Model 测试

LLM 提供商页复用 `api.testProvider(provider, model?)`：顶部「测试默认模型」省略
`model`，每个模型行的「测试」传入对应模型 ID。设置页用单一
`testingTarget` 控制局部 loading、防止重复请求；成功 toast 展示实际模型、耗时和
回复摘要，失败 toast 展示后端返回的标准化错误。

### 8.2 会话设置下拉选择

`InputArea.vue` 的会话设置中，风格与知识库复用 `NDropdown + .opt-pick`
模式，只显示当前值和右侧箭头；展开后再列出全部选项。这样风格或知识库数量
增加时不会撑高设置面板。知识库没有启用项时，选择器仍保留“不使用/全部”，并
在下方显示配置引导。

风格在创建新会话时继承当前活动会话；没有活动会话时默认为 `off`。能力工具使用多选下拉，仅展示系统设置中已启用、供应商与模型完整且协议受支持的图片/视频/音频路由。

模型编辑器的上下文窗口提供 32K、64K、128K、200K、256K、512K、1M、2M 预设，同时保留自定义 token 输入；原“支持视觉输入”开关改为图片/视频/音频多选能力配置。系统设置“媒体识别”为三条独立路由，每条可单独关闭或指定 provider/model；最大文件限制可用 B、KB、MB、GB 编辑，前端换算后仍以 `max_bytes` 整数字节提交。

### 8.3 附件预览与发送时序

`addAttachment()` 在选择文件时立即插入本地预览占位，并用同一个 `_ready`
任务完成文件读取和 `/uploads` 上传。普通消息发送时，`InputArea.send()` 先用独立的
本地 blob URL 插入用户气泡，再等待 `_ready`，随后复用已有 `upload_id` 组装请求；
发送阶段不得再次调用上传接口。图片、视频、音频、PDF 和 Office 文档都携带同一
`upload_id` 引用；只有上传失败时才回退到内联数据。非媒体附件不再把 data URL
误放进 `text` 字段，服务端会把引用持久化并按需交给 `read_attachment`。

所有 data URL 预览统一通过 `utils/mediaPreview.ts` 转换；Blob MIME 只取 data URL
元数据中第一个分号前的媒体类型，不能把 `;base64` 当作 MIME 的一部分。会话换页、
消息淘汰或重新加载时必须释放消息附件持有的 blob URL。

撤回用户消息时，GUI 以被点击的用户消息作为草稿来源，同时从撤回响应中合并该回合
拆开的附件行，将文字和附件原子回填到 `InputArea`。历史 `/uploads/:id` 地址转换为
已有上传引用，不重新上传；回填附件带撤回来源标记。点击“撤销”时只移除仍属于该
来源的附件，并且仅当输入文字未被用户修改时清空文字。关闭撤回提示只放弃撤销能力，
不清空已经回填的草稿。撤销后消息列表使用撤回前已归一化的 GUI 消息恢复，避免附件
行重新拆成多个气泡。

消息气泡中的附件下载/复制按钮只在附件悬浮或键盘聚焦时显示，并使用统一的浮层、
边框、阴影与焦点样式。消息级操作保留复制和重答为直接按钮，创建分支、撤回等次级
操作合并到三点菜单，菜单复用全局 `app-action-menu`。图片使用独立的中性附件卡片，
随消息发送的说明文字单独显示为用户色气泡；多附件采用自适应网格。用户消息操作与
时间位于同一静态底部基线，只有附件而没有文字时也使用附件组下方的同一兜底工具栏，
避免操作栏覆盖小文本气泡或图片内容。

用户气泡的出现不依赖 LLM API。启用媒体能力后，服务端仍会先执行对应能力模型的
非流式识别，再启动主模型 SSE；前端会实时显示识别 phase，但主回复文本要到主模型
产生首个 delta 后才出现。这是服务端串行预处理，不是前端等待完整回复后再播放。

### 9. Round 2 增强 (2026-07-15)

#### P1-1 工具结果折叠

`ToolCallCard.vue` 自动折叠 `result.length >= 200` OR `>= 4 个换行`
的结果（shell / wiki_search / 长 file 读）。短 result 保持展开。
用户点击 header 切换状态，状态写到 `localStorage` 持久化
（key = `pchat.toolFold.<name>.<tool_id[:12]>`）。折叠态下
header 的 📋 复制按钮可点（stopPropagation 防 toggle）。

#### P1-2 子 agent 实时进度

`SubAgentCard.vue` header 加 `part.parts.length` 计数 chip
（pill 样式），running 时切到 sub-accent 色。后端 `tryForward`
本身就是逐 chunk 转发，前端只是把"看不到进度"的问题修了。

#### P0-1 流式中断恢复

`streamMessagesViaFetch` 和 Wails 路径的 stream catch
边界都触发 `opts.onStreamDrop({ lastSeq, reason })`。
`recoverMissingParts` action：
1. 调 `getSessionSnapshot(sessionId, lastSeq)` 拿增量
2. 用 fingerprint (tool_id / text 前 40 字符) 去重 merge 到 trailing bubble
3. 触发 3s 的 `RecoveryBanner`（"已恢复 N 条消息"）

不在用户主动 abort 时触发（`signal.aborted` 短路）。

#### P1-3 重新生成按钮

`MessageBubble.vue` trailing assistant 消息加"重答"按钮。
`regenerateMessage` action：弹掉本地 trailing bubble → 调
`api.streamRegenerate` → 走正常 stream 路径。auto-continue
从 0 重新计数（算新 stream）。

### 10. Round 3 增强 (2026-07-15)

#### P2-2 代码高亮

`main.ts` 注册 14 个常用语言（ts/js/py/go/rs/java/json/yaml/bash/sql/xml/css/md），
用 `marked-highlight` 扩展把 highlight.js 接到 `Marked` instance。
MessageBubble 的 `marked.parse(...)` 通过全局 mirror 自动获得高亮。
`style.css` 末尾 `@import 'highlight.js/styles/github-dark.css'` +
自定义 `.hljs { background: transparent }`（保留 P-Chat 表面色）。

`vite.config.ts` 加 `marked` / `marked-highlight` alias + dedupe 避免 npm
hoisting 让 marked-highlight 解析到根 node_modules。

#### P2-3 上下文检查器

`ContextInspectorDrawer.vue`：NDrawer 右侧滑出，顶部 NProgress 显示
context window 利用率（> 60% 黄，> 80% 红，与 tryAutoCompact 阈值一致），
中部 per-message 列表（role badge + token 数 + preview），底部 NCollapse
显示 compressed summary。

`chat.ts`：`state.contextInspector` 字段 (open/loading/error/data) +
`openContextInspector` / `closeContextInspector` / `loadContextInspector` actions。
重开刷新（in-flight 跳过 spinner）。

`TopBar.vue` 加 `BarChart3` 按钮触发 `openContextInspector(state.currentID)`。
`ChatWindow.vue` 挂载 Drawer 组件。

#### P2-4 工具 dry-run

`ToolCallCard.vue` 检测 `args.dry_run === true` 显示 `dry-run` pill chip
(brand-50 背景)。折叠态可见，告知用户"这个工具是预览未实际执行"。

UX 决策：`ToolConfirmModal` 不加"先干跑一次"按钮（避免新增 server 端点 /
复杂状态机）。改用 prompt 触发：用户打"干跑 shell_command X" → LLM 自加
`dry_run: true` → ToolCallCard 显示 chip。

#### P3-3 trace id 复制按钮

`MessageBubble.vue` 检测 `message.traceId`（chat store 在 error
事件触发时塞到 trailing assistant message）显示 `.trace-id-chip`
按钮。点击调 `copyText(id)` 复制到剪贴板 + toast 提示"已复制"。

`client.ts` 增 `mintTraceId()` 8 字符 hex (T-xxxxxxxx)，fetch
路径设 `X-Trace-Id` header，Wails 路径把 `trace_id` 塞 body 由
Go binding `extractTraceID` 提取。详见 [P3-3 设计](../../docs/plans/round4-trace-and-extensibility-plan.md)。

#### P3-2 工具列表抽屉

`ToolListDrawer.vue`：NDrawer 右侧滑出，分内置 / 自定义两段。
自定义工具带 "自定义" badge + "查看源码" 展开显示 YAML 路径
（提示用户在编辑器中编辑）。TopBar 加 `Wrench` 按钮触发。

`api/client.ts` 增 `listTools()` / `listToolsDetailed()` + `Tool` 类型（`dynamic`
flag + `source` 路径）。`GET /api/v1/tools` 同时返回 `diagnostics[]`，
`ToolListDrawer.vue` 会把动态工具 YAML 解析失败项显示在"加载诊断"区域。
`chat.ts` 无状态（按需 fetch）—— 工具列表是 per-server 不 per-session。

#### BR-01 浏览器连接诊断

`AppSettingsModal.vue` 的浏览器 Tab 读取 `getBrowserStatus()` 和
`listBrowsers()`：显示 HTTP/WS 地址、连接数量、最近错误、tab 数、扩展版本、
协议版本和最后活跃时间。旧扩展如果未上报版本字段，GUI 显示"未上报"。

#### BR-02 浏览器扩展更新提示

`browser-extension/background.js` 在 hello 握手里上报 `extension_version`
和 `protocol_version`。服务端通过 `browser.ProtocolVersion` 判断兼容性，
`listBrowsers()` 返回 `update_required` / `update_message`。浏览器设置 Tab
在有旧扩展连接时显示"需更新扩展"标签和"下载最新扩展"入口。


#### BR-03 多标签页管理

浏览器设置 Tab 通过 `listBrowserTabs()` / `setBrowserActiveTab()` 展示已连接
浏览器的标签页列表，并允许用户把某一页设为「控制目标」。扩展侧维护
`preferredTabId`；`browser_*` 工具默认作用到该目标，也可在参数里显式传
`tab_id` 覆盖。协议版本现为 `3`。

### 11. GPU 渲染优化 (2026-08-06)

修复 WebView2 **GPU 进程**（合成器/光栅线程）流式期间持续高 CPU。核心原则：
**流式指示只允许小组件（dot / spinner / caret，只动画 `opacity`/`transform`）；
大图层禁止持续动画；流式文本禁止每 delta 重解析 markdown。**

- **F1** — 移除整气泡流式 opacity 脉冲（`MessageBubble.vue` 曾
  `.msg.streaming .bubble { animation: pulse 1.5s infinite }`）。整层 opacity
  动画 = 合成器全程 60fps，流式几分钟就烧几分钟。流式信号改由 6px stream-dot
  + TypedText caret + thinking spinner 承担。
- **F2** — 移除子代理 header 的 `background-position` shimmer（每帧重绘 +
  GPU raster）。运行态改为静态 surface tint + 左 accent 边框。
- **F3** — 子代理**流式中的文本**改走 `TypedText`（textContent 直写，O(1)），
  不再每次 delta 对全文 `marked.parse`（O(n²)）。静态 part 统一走共享 LRU 缓存
  `utils/markdownCache.ts`（MessageBubble / SubAgentCard 共用）。

验证：`scripts/gpu-probe/` A/B 测量（Chromium GPU 进程，见其 README），
流式 renderer 20.5%→10.3%、GPU 11.7%→8.1%。卡死流（`done` 丢失）由传输层
150s idle watchdog（`idleTimeoutMs`）兜底，无需额外前端看门狗。

### 12. 会话回合队列

- 会话忙碌或已有待处理项时，`InputArea.send()` 把完整发送参数持久化到
  `/turn-queue`，不提前渲染 user bubble。
- 队列条默认显示一行摘要；展开后支持编辑 queued 消息、删除、失败重试和清空。
- 编辑调用 `PATCH /turn-queue/:queue_id`；保存时保留队列 ID、FIFO 位置、附件和
  模型/风格等 payload，只改消息文本。
- 进入编辑态会暂停客户端自动出队；保存或取消后恢复。服务端仍以
  `status=queued` 条件更新，兜底处理多客户端领取竞态。
- failed 项不可直接编辑，需先重试回到 queued；running 项不会出现在可编辑列表。

对话页顶部的 `StreamingBar.vue`（"对话进行中"加载条）同样遵循
compositor-only 原则：`transform` 平移超宽渐变条 + `background-size`
重复模式实现无缝滚动，无 `background-position` 动画（见 §8.4 例外）。

## 修改指南

### 要添加新的 SSE 事件类型
1. `client.ts` — 在 `StreamEvent` 接口添加字段
2. `chat.ts` — 在 `appendStreamEvent` switch 添加 case
3. `handler.go` — 在 `chunkToEvent` 添加事件组映射

### 要修改消息渲染
- `MessageBubble.vue` — 消息容器
- `TypedText.vue` — 文本渲染（marked.parse）
- `ThinkingBlock.vue` — 思考面板

### 要修改 SubAgentCard
- `SubAgentCard.vue` — 子代理卡片 UI
- `chat.ts` `findOrCreateSubAgent()` — parts 管理
- `chat.ts` `sub_agent` 路由 — 事件分发

### 要修改流式传输
- `client.ts` `streamMessages()` — SSE 消费循环
- `chat.ts` `appendStreamEvent()` — 事件路由
- `chat.ts` `endStream()` — 清理

## 相关模块

- [server.md](server.md) — SSE 事件生产者
- [agent.md](agent.md) — ChatStreamChunk 到 StreamEvent 的映射
