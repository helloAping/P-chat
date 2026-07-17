# 第三轮优化计划 — 代码高亮 + 上下文检查器 + 工具增强

> **状态**: 待评审
> **创建日期**: 2026-07-15
> **分支**: `feat_1.0.6`（基于第二轮 P3-1/P1-1/P1-2/P0-1/P1-3 之后）
> **关联**: `.claude/CLAUDE.md` §1, `.agents/docs/frontend.md`, `.agents/docs/tool.md`, `.agents/docs/llm.md`

---

## 0. 现状速览

| 项 | 状态 | 备注 |
| --- | --- | --- |
| P1-4 对话分支 | **已完成**（forkSession + ForkSession 端点 + GitBranch 按钮） | 无单测；功能完整 |
| P2-2 代码高亮 | **未做** | highlight.js 已装但 `marked` 未传 `highlight` 选项 |
| P2-3 上下文检查器 | 未做 | 需新增 inspector 端点 + 抽屉 UI |
| P2-4 工具 dry-run | 未做 | 需改 sandbox 系统 |
| P2-5 多 LLM 并行对比 | 未做 | 大特性 |
| P3-2 工具 hot-reload | 未做 | 需新增 dynamic registry 整套 |
| P3-3 端到端 trace ID | 未做 | 横切所有层 |

按 P1-4 → P2-2 → P2-3 → P2-4 → P2-5 → P3-2 → P3-3 顺序。

---

## 1. 任务点列表

---

### 🟢 P2-2 — 激活 highlight.js 代码高亮

**现状**：`frontend/package.json` 已经有 `highlight.js@^11.9.0`，但 `MessageBubble.vue:61` 调 `marked.parse(text, { async: false, breaks: true })` 没传 `highlight` 函数。代码块是**纯文本**显示（仅 CSS 字体），没颜色。

**目标**：code block 内的代码根据 `language-xxx` 类自动高亮。

**改动**：

| 文件 | 改动 |
| --- | --- |
| `frontend/src/main.ts` | import highlight.js + 注册 `marked` 的 `highlight` 选项 + import 常用语言子集 |
| `frontend/src/components/MessageBubble.vue` | （已支持 toolbars + langExt） |
| `frontend/src/style.css` | 加 highlight.js GitHub Dark 主题 CSS（`github-dark.css`） |

**关键点**：
- **不引入 shiki**：highlight.js 已装且更轻（~40KB vs shiki 500KB+），效果对绝大多数代码足够
- marked v12+ 推荐用 `marked-highlight` 扩展，但 `marked` 自身仍支持旧 `highlight` 选项；用旧 API 避免多一个 dep
- 注册 10+ 主流语言：ts / js / py / go / rs / java / json / yaml / bash / sql
- 不在 streaming 期调 highlight（性能 + 复杂度）；等 message 完成（`streaming = false`）后跑一次
- marked 渲染流程：text → marked.parse → HTML → highlight（post-process via walk on `<pre><code>`）

**测试**：手动验证（前端暂无 vitest 基础设施）

**回滚**：删 main.ts 的 import 即可。

---

### 🟢 P2-3 — 上下文窗口检查器

**目标**：UI 抽屉显示当前 session 喂给 LLM 的所有 message + estimated tokens，让用户能：
1. 知道当前对话占多少 context
2. 看到哪些 message 是工具结果（往往占大头）
3. 知道哪些 message 被 tryAutoCompact 压缩过

**改动**：

| 文件 | 改动 |
| --- | --- |
| `internal/server/handler.go` | 新增 `ContextInspector` 端点 `GET /api/v1/sessions/:id/context` |
| `internal/llm/chat_message.go` (新方法) | `EstimateTokensSession(msgs)` 复用 `EstimateTokensMessages` |
| `frontend/src/api/client.ts` | `getSessionContext(sessionId)` 函数 |
| `frontend/src/stores/chat.ts` | `state.contextInspector` 字段 + `loadContextInspector` action |
| `frontend/src/components/ContextInspectorDrawer.vue` (新) | Naive UI Drawer，显示 messages + token 估算 + 高亮工具结果 |
| `frontend/src/components/TopBar.vue` 或新按钮 | 触发抽屉 |

**响应格式**：
```json
{
  "session_id": "...",
  "model": "gpt-4o",
  "context_window": 128000,
  "estimated_tokens": 45230,
  "utilization_pct": 35.3,
  "compressed_summary": "...",  // 可选，tryAutoCompact 写入的
  "messages": [
    {"role": "user", "tokens": 12, "preview": "你好", "is_tool_result": false},
    {"role": "assistant", "tokens": 450, "preview": "...", "is_tool_result": false, "parts_count": 3},
    {"role": "tool", "tokens": 4200, "preview": "...file content...", "is_tool_result": true}
  ]
}
```

**关键点**：
- 不重算实际 token（无法，依赖 LLM tokenizer）；用 `llm.EstimateTokensMessages` 启发式
- 跟 `tryAutoCompact` 阈值 80% 联动：超 80% 红色，超 60% 黄色
- `is_tool_result: true` 的 message 用不同颜色
- 复用 Naive UI `NDrawer`，不动现有 layout

**测试**：
- `internal/server/handler_context_test.go`：mock 5 条 message，验证 estimated_tokens 在合理范围

**回滚**：删除端点 + 删除组件 + 移除 import。

---

### 🟢 P2-4 — 工具 dry-run / 预览

**目标**：`shell_command` / `write_file` / `read_file` 在真正执行前可"先干跑"，返回将要做的事（不是真做）。

**改动**：

| 文件 | 改动 |
| --- | --- |
| `internal/tool/shell_command.go` | 加 `dry_run bool` 参数；dry_run 模式只检查命令（通过 sandbox）不 exec |
| `internal/tool/write_file.go` | dry_run 只算 diff（与已有对比）不真写 |
| `internal/tool/read_file.go` | dry_run 模式返回前 50 行 + 字节数 |
| `internal/server/handler.go` `chunkToEvent` | tool 事件加 `tool_dry_run: true` 字段 |
| `frontend/src/api/client.ts` | `ToolPart` 加 `dryRun: boolean` |
| `frontend/src/components/ToolCallCard.vue` | 工具卡加 "干跑" 按钮（stopPropagation） |
| `frontend/src/components/ToolConfirmModal.vue` | 加 "先干跑一次" 按钮 |

**关键点**：
- sandbox 系统不用改（`dry_run` 走正常 sandbox 检查路径）
- 干跑结果以 tool_result 形式回给 LLM（跟正常 tool call 一样），LLM 看到"将执行 cat file, file 是 30 行"决定是否继续
- 工具 schema 加 dry_run 后 LLM 可能自动用；前端按钮是给"想先看"的用户的快速入口

**测试**：tool/shell_command_test.go + write_file_test.go 加 dry_run case

**回滚**：删 dry_run 参数处理即可（默认 false → 行为不变）。

---

### 🟢 P2-5 — 多 LLM 并行对比（race mode）

**目标**：用户发问时选多个 LLM 同步跑，UI 分屏显示 3 个回复，结束后让用户选一个 fork 为主线。

**改动（巨）**：
- 后端：`/sessions/:id/race` 端点 + 多 stream 并发合流
- 前端：分屏 UI + 选 winner
- 工作量大，独立排期 — **本轮只做"骨架"，完整 UI 留后续**

---

### 🟢 P3-2 — 工具 hot-reload

**目标**：从 `~/.p-chat/tools/*.yaml` 加载工具定义 + shell/HTTP 执行模板，不用重新编译。

**改动（巨）**：
- 新增 `internal/tool/dynamic/` 整套
- schema 校验 + sandbox 复用
- 工作量大，独立排期

---

### 🟢 P3-3 — 端到端 trace ID

**目标**：`X-Trace-Id` 头贯穿前端 → handler → agent → llm → tool → log，前端报错弹"复制 trace id"按钮。

**改动**：
- 后端 ctx 携带 trace id
- 前端自动生成 + header 注入
- log 全部带 trace 前缀
- UI 显示 + 复制

---

## 2. 本轮（round 3）实施计划

**本轮做 P2-2（1 个），P2-3（2 个），P2-4（3 个）。后 3 个独立排期。**

按 ROI 排序：
- **P2-2** 体验立刻提升，0.5d
- **P2-3** 调 prompt 神器，1d
- **P2-4** 改 shell / write / read 三个 tool + frontend 按钮，1.5d

合计 ~3d。每完成一个独立提交。

---

## 3. P2-2 详细设计

### 改动

```ts
// main.ts (新增)
import { marked } from 'marked'
import hljs from 'highlight.js/lib/core'
import javascript from 'highlight.js/lib/languages/javascript'
import typescript from 'highlight.js/lib/languages/typescript'
import python from 'highlight.js/lib/languages/python'
import go from 'highlight.js/lib/languages/go'
import rust from 'highlight.js/lib/languages/rust'
import java from 'highlight.js/lib/languages/java'
import json from 'highlight.js/lib/languages/json'
import yaml from 'highlight.js/lib/languages/yaml'
import bash from 'highlight.js/lib/languages/bash'
import sql from 'highlight.js/lib/languages/sql'
import xml from 'highlight.js/lib/languages/xml'
import css from 'highlight.js/lib/languages/css'

hljs.registerLanguage('javascript', javascript)
hljs.registerLanguage('js', javascript)
hljs.registerLanguage('typescript', typescript)
hljs.registerLanguage('ts', typescript)
// ... 10+ 语言

marked.setOptions({
  highlight(code: string, lang: string): string {
    if (lang && hljs.getLanguage(lang)) {
      try {
        return hljs.highlight(code, { language: lang }).value
      } catch { /* fall through */ }
    }
    return hljs.highlightAuto(code).value  // 自动检测
  },
})
```

```css
/* style.css (新增) */
@import 'highlight.js/styles/github-dark.css';

/* Custom override: keep marked's <code> background */
pre code.hljs {
  background: transparent;
  padding: 0;
}
```

### MessageBubble.vue 改？

不需要！marked 已有的 `highlight` 选项在 `marked.parse()` 时自动跑。但 `MessageBubble.vue:61` 是 `marked.parse(text, { async: false, breaks: true })` — **没传 highlight**。要改：

```ts
const html = marked.parse(text, { async: false, breaks: true }) as string
```

→ 改为：
```ts
const html = marked.parse(text, { async: false, breaks: true, highlight: markedHighlight }) as string
```

或者用 marked v12+ 的 extensions（更复杂），用旧 API 简单。

**或者** 改 main.ts 后，`marked.setOptions({ highlight })` 全局生效，所有 `marked.parse` 都自动用高亮 — 这样 MessageBubble 不用改！

让我用全局 setOptions 方案：最干净。

### 测试

- vue-tsc 通过
- npm run build 通过
- 手动：发问包含 ```python ... ``` 看是否高亮

### 验收
- [ ] 代码块有 syntax 颜色
- [ ] streaming 期不卡（高亮只跑一次，message 完成时）
- [ ] 不识别的语言不会 crash（fallback to auto-detect）
- [ ] bundle 增量 < 100KB

### 回滚
删 main.ts 的 import + style.css 的 @import。

---

## 4. P2-3 详细设计

### 后端

```go
// internal/server/handler.go 新增
// ContextInspector GET /api/v1/sessions/:id/context
func (h *Handler) ContextInspector(c *gin.Context) {
    // 1. 拿 session 的 provider/model/...
    // 2. 拉取 history + compressed summary
    // 3. 构造 llm.ChatMessage slice
    // 4. 调 llm.EstimateTokensMessages
    // 5. 调 llm.ContextWindow(provider, model)
    // 6. 返回 {session_id, model, context_window, estimated_tokens, utilization_pct, messages: [...], compressed_summary: ...}
}
```

### 前端

ContextInspectorDrawer.vue：
- Naive UI NDrawer (右侧滑出)
- 顶部：进度条（utilization_pct）
- 中间：可滚动 message 列表，每条带 role badge + token 估算 + preview
- tool result 用不同背景色
- 底部：close 按钮

TopBar 加一个 📊 按钮触发。

---

## 5. P2-4 详细设计

### shell_command dry_run

```go
// 工具 handler
if args.DryRun {
    // 不执行，只检查 sandbox + 返回"将执行 X"
    return fmt.Sprintf("[dry-run] would execute: %s\nworking_dir: %s", args.Command, wd), nil
}
```

### write_file dry_run

```go
if args.DryRun {
    existing, _ := os.ReadFile(args.Path)
    return fmt.Sprintf("[dry-run] would write %d bytes to %s\nfirst 200 chars: %s", len(args.Content), args.Path, args.Content[:min(200, len(args.Content))]), nil
}
```

### read_file dry_run

```go
if args.DryRun {
    info, err := os.Stat(args.Path)
    if err != nil { return "", err }
    return fmt.Sprintf("[dry-run] would read %s (%d bytes, mode %s)", args.Path, info.Size(), info.Mode()), nil
}
```

### 工具 schema

每个工具的 `Definition()` 加 `dry_run: { type: "boolean", description: "..." }` 字段。

### 前端

ToolCallCard 加 "干跑" 按钮：调 `api.runTool({...args, dry_run: true})` 单独走一个轻量 endpoint？还是在主 stream 里把 dry_run=true 嵌入 LLM 的 tool call？

**简化方案**：用户在 InputArea 加 `/dry-run <prompt>` 前缀，后端看到后自动给 tool 加 dry_run=true。这样 LLM 不会自动调，前端有按钮确认走 dry-run 路径。

**实际方案 v1**：**只做前端按钮**，点击时**调一个独立 endpoint** 直接以 dry_run 模式跑工具（不走 LLM）。这是"试试看这条命令会怎样"的快速入口。

需要新端点 `POST /api/v1/sessions/:id/tools/dry-run` 接 `{tool_name, args}`，服务端用 sandbox 检查 + 返回预览。

---

## 6. 改动文件汇总（本轮 3 项）

| 后端 | |
| --- | --- |
| `frontend/src/main.ts` | P2-2 |
| `frontend/src/style.css` | P2-2 |
| `internal/server/handler.go` | P2-3 + P2-4 |
| `internal/llm/chat_message.go` | P2-3（可能不需要，看是否已有 Estimate 函数） |
| `internal/tool/shell_command.go` | P2-4 |
| `internal/tool/write_file.go` | P2-4 |
| `internal/tool/read_file.go` | P2-4 |
| `internal/server/handler_context_test.go` (新) | P2-3 |
| `internal/tool/shell_command_test.go` | P2-4（可能已有） |

| 前端 | |
| --- | --- |
| `frontend/src/api/client.ts` | P2-3 + P2-4 |
| `frontend/src/stores/chat.ts` | P2-3 + P2-4 |
| `frontend/src/components/ContextInspectorDrawer.vue` (新) | P2-3 |
| `frontend/src/components/TopBar.vue` | P2-3（加按钮） |
| `frontend/src/components/ToolCallCard.vue` | P2-4 |

---

## 7. 实施顺序

```
Day 1 上午: P2-2 highlight.js (0.5d) → 提交
Day 1 下午: P2-3 后端 context endpoint (0.5d) → 提交
Day 2 上午: P2-3 前端 drawer (0.5d) → 提交
Day 2 下午: P2-4 后端 dry-run (0.5d) → 提交
Day 3 上午: P2-4 前端 dry-run 按钮 (0.5d) → 提交
Day 3 下午: 文档 + CHANGELOG v1.0.8 + README → 提交
```

---

## 8. 验收标准

### P2-2
- [ ] python / ts / go 代码块有 syntax 颜色
- [ ] 不识别语言 fallback 不 crash
- [ ] bundle 增量 < 100KB
- [ ] streaming 期不卡

### P2-3
- [ ] GET /sessions/:id/context 返回 200 + 完整 JSON
- [ ] 前端抽屉显示 token 利用率进度条
- [ ] 工具结果 message 用不同颜色
- [ ] estimated_tokens 在合理范围（每 4 chars ~1 token）

### P2-4
- [ ] shell_command dry_run 不真执行
- [ ] write_file dry_run 不真写
- [ ] read_file dry_run 不真读
- [ ] 前端按钮触发独立 endpoint

---

## 9. 风险

| 风险 | 等级 | 缓解 |
| --- | --- | --- |
| P2-2 highlight.js bundle 太大 | 中 | 只注册 10+ 主流语言，custom build |
| P2-3 token 估算不准确 | 低 | 标注"估算"，用户能调参压缩 |
| P2-4 dry_run bypass sandbox | 低 | 走同一个 sandbox checker |

---

## 10. 不在本轮（明确推迟）

- **P2-5** 多 LLM 并行对比 — 大特性，独立排期
- **P3-2** 工具 hot-reload — 需要新增 dynamic registry 整套
- **P3-3** 端到端 trace ID — 横切所有层
