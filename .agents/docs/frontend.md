# Frontend 模块（Vue 3 前端）

> **位置**：`frontend/src/`  
> **技术栈**：Vue 3 + Vite + Pinia + Naive UI + marked  
> **后端通信**：HTTP REST (JSON) + SSE (Server-Sent Events)
>
> **样式规范**：见 [frontend-design.md](frontend-design.md) —— 设计 token、组件规则、强制约束都在那边

## 概述

P-Chat 的浏览器端 GUI，提供会话列表、聊天窗口、子代理卡片、问题模态框、设置面板等功能。与 pchat-server 通过同一域名的 `/api/v1/*` 端点通信，SSE 流通过 `fetch()` + `ReadableStream` 消费。

## 文件结构

| 路径 | 职责 | 关键导出/组件 |
|---|---|---|
| `api/client.ts` | HTTP+SSE 客户端、类型定义 | `StreamEvent`, `MessagePart`, `streamMessages()`, `sendMessage()` |
| `stores/chat.ts` | Pinia 状态管理（会话、消息、流） | `appendStreamEvent()`, `useChatStore()` |
| `main.ts` | 应用入口（Naive UI + Router） | `createApp()` |
| `App.vue` | 根布局（侧边栏 + 聊天区域） | |
| `components/ChatWindow.vue` | 聊天窗口（消息列表 + 输入区） | |
| `components/InputArea.vue` | 输入区域（文本、附件、计划模式） | |
| `components/MessageBubble.vue` | 单条消息渲染（parts[] 迭代） | |
| `components/TypedText.vue` | 流式文本渲染（blinking caret） | |
| `components/ThinkingBlock.vue` | 思考块（可折叠） | |
| `components/ToolCallCard.vue` | 工具调用卡片（name/args/status/result） | |
| `components/SubAgentCard.vue` | 子代理嵌套卡片（header + inner parts） | |
| `components/SessionSidebar.vue` | 会话列表 + 项目选择器 + 设置 | |
| `components/CommandPalette.vue` | `/` 斜杠命令内联自动补全 | |
| `components/TodoPanel.vue` | 待办事项面板（交互式） | |
| `components/QuestionModal.vue` | 多选问题对话框 | |
| `components/ImageLightbox.vue` | 全屏图片查看器 | |
| `components/LoadingDots.vue` | 子代理加载指示器 | |
| `components/AppSettingsModal.vue` | Provider/Model/Style/知识库管理 | 左右分栏 + KB 三层树视图 + NCollapse |

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
