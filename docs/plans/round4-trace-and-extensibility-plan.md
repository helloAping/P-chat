# 第四轮优化计划 — 端到端可观测性 + 平台可扩展性

> **状态**: 待评审
> **创建日期**: 2026-07-15
> **分支**: `feat_1.0.6`（基于第三轮 P2-2/P2-3/P2-4 之后）
> **关联**: `.claude/CLAUDE.md` §1, `.agents/docs/agent.md`, `.agents/docs/tool.md`, `.agents/docs/server.md`, `.agents/docs/llm.md`
> **调研产物**: 三项 surface-area 调研已完成（P3-3 / P2-5 / P3-2），本文档以调研结论为事实依据

---

## 0. 现状速览

| 项 | 状态 | 备注 |
| --- | --- | --- |
| **P3-3 端到端 trace ID** | 未做 | 9 个文件改 7 处，~1d；横切但每层增量小 |
| **P3-2 工具 hot-reload** | 未做 | `toolsDir()` 已存在；`Unregister` 已存在；缺 YAML loader + watcher + `GET /tools` + CLI 列表 |
| **P2-5 多 LLM 并行对比** | 未做 | LLM client 已支持 per-call `(provider, model)`；fork endpoint 已存在；缺 race 端点 + 多 pane UI + 选 winner 流程 |

按 P3-3 → P3-2 → P2-5 顺序：
- **P3-3** 1d，立刻带来"用户报错时直接给开发 trace id"的生产力提升
- **P3-2** 2d，给"想加自定义工具"的用户开路，复用现有 5s polling watcher
- **P2-5** 3d，最大特性，等前两项稳了再上

合计 ~6d。每完成一个独立提交 + 文档收尾。

---

## 1. 任务点列表

---

### 🟢 P3-3 — 端到端 trace ID

**现状**：P-Chat 链路覆盖前端 → HTTP handler → agent ReAct → LLM client（OpenAI/Anthropic 双协议）→ tool handler → log，但**没有任何贯穿的标识符**。一旦线上报错，用户只能贴整段 `server-debug.log`；开发者要在 log 里 grep 关键字。

**目标**：每个 SSE 会话自动生成 `trace_id`（短 8 字符 hex，如 `T-9f3c4a2b`），贯穿：
```
前端 (mint uuid v4) 
  → HTTP header X-Trace-Id (可选，前端无则后端生成)
    → handler 写 ctx，c.Header 写响应
      → agent ChatWithTools 读 ctx，stamps ChatRequest.TraceID
        → ChatStreamChunk.TraceID → SSE event trace_id
        → LLM HTTP header X-Trace-Id（带去第三方）
        → tool handler ctx 读，log 拼接
        → 所有 log.Printf 前缀 [trace=T-9f3c4a2b]
          → 前端 SSE event.trace_id
            → 错误气泡里 "复制 trace id" 按钮
```

**关键事实**（来自调研）：
- 现有 seq 基础设施（P3-1）就是同款模式：`atomic.Uint64` + `nextSeq` closure → `sendOrDrop` 注入 `chunk.Seq` → SSE `id:` line。新增 `trace_id` 完全沿用此模式。
- gin middleware 一处加（`server.go:108` 旁），agent `ChatWithTools` 一处读 `req.TraceID`（`agent.go:1910` 旁），`sendOrDrop` 增一个 stamp（`agent.go:1221` 旁）。
- LLM `http.NewRequestWithContext(ctx, ...)` 在 `client.go:375` 和 `anthropic.go:225`，加一行 `req.Header.Set("X-Trace-Id", tid)`。
- tool `ctx` key 新增 `traceKey{}` 镜像 `sandboxKey{}`（`registry.go:105-122`）。
- CORS allow-headers 需加 `X-Trace-Id`（`server.go:429`）。
- 前端用 `crypto.randomUUID()` 切片取 8 字符；fetch 路径 `client.ts:1143` 加 header；错误 chip 渲染 + 复制按钮。

**改动**：

| 文件 | 改动 |
| --- | --- |
| `internal/trace/trace.go` (新) | `NewID() string`（8 字符 hex），`WithID(ctx, id) context.Context`，`FromContext(ctx) string` |
| `internal/server/server.go` | 新增 `traceIDMiddleware()`（5 行）；注册到 middleware 链；CORS allow-headers 加 `X-Trace-Id` |
| `internal/server/handler.go` | `respondSSE` 增 `c.Header("X-Trace-Id", id)`（line 2610 旁）；`chunkToEvent` 复制 `chunk.TraceID` → `ev.TraceID`（line 2863 旁） |
| `internal/agent/agent.go` | `ChatRequest.TraceID` 字段；`ChatStreamChunk.TraceID` 字段；`ChatWithTools` 读 `req.TraceID` → `ctx` 注入；`sendOrDrop` 增 trace id stamp 闭包 |
| `internal/llm/client.go` | `openaiStream` 在 `http.NewRequestWithContext` 后加 `req.Header.Set("X-Trace-Id", trace.FromContext(ctx))` |
| `internal/llm/anthropic.go` | `AnthropicClient.ChatStream` 同上 |
| `internal/tool/registry.go` | `traceKey{}` + `WithTraceID/TraceIDFromCtx`；`ChatWithTools` 在工具派发前 `toolCtx = tool.WithTraceID(toolCtx, req.TraceID)` |
| `internal/server/handler_trace_test.go` (新) | 3 个测试：header 透传、缺失时生成、ctx 贯穿 |
| `frontend/src/api/client.ts` | `StreamEvent.trace_id?` 字段；`streamMessagesViaFetch` 增 header；Wails 路径 `client.ts:1027` 旁增 trace 透传 |
| `frontend/src/stores/chat.ts` | `case 'error':` (line 1823) 错误文本里追加 trace id chip；chip 渲染 + 复制 |
| `frontend/src/components/MessageBubble.vue` | `.trace-id-chip` CSS + `@click="copyText(ev.trace_id)"` |

**关键点**：
- trace id 用 8 字符 hex（不是全 uuid）— log/UI 都更可读；冲突概率在 1M 并发下仍 < 1e-6
- 前端 mint 时也生成一个；后端收到用前端的、缺失时自己生成；最终一致性靠 SSE 第一个 event 的 `trace_id` 字段纠正前端
- 第三方 LLM（OpenAI/Anthropic）的 `X-Trace-Id` 是我们送出的 header，对方不会回；价值在于"我们能 grep 到我们送出去的那条"
- log 格式：所有 `log.Printf("[xxx] ...")` 改为 `log.Printf("[%s] [xxx] ...", tid, ...)`。一次性脚本扫所有 `log.Printf` 加前缀

**测试**：
- `handler_trace_test.go`：3 个 SSE 测试（header 透传 / 缺失生成 / 错误时 chip 有 trace）
- 手动：发问制造 500，看 error chip 是否能复制

**回滚**：删 middleware + 删所有 stamp 即可，trace id 是只增字段。

---

### 🟢 P3-2 — 工具 hot-reload

**现状**：`paths.ToolsDir() = ~/.p-chat/tools` 已存在（`paths.go:137-140`），`EnsureGlobal` 启动时创建空目录。`Registry.Unregister` 已存在（`registry.go:225-230`）。但**无 YAML 格式定义**、**无 loader**、**无 watcher**、**无 API 暴露**、**无 CLI 列表**、**无前端 UI**。

**目标**：用户在 `~/.p-chat/tools/foo.yaml` 写一个文件，描述工具的 name/description/params/exec 模板，重启（或等 5s polling）后立即可用，无重编。

**YAML 格式（v1）**：
```yaml
# ~/.p-chat/tools/greet.yaml
name: greet
description: "向用户问好。可用于测试。"
parameters:
  type: object
  properties:
    name:
      type: string
      description: "用户名"
  required: [name]
template:
  type: exec          # exec | http | echo
  command: "echo Hello, {{.args.name}}!"  # 模板渲染
  timeout: 5s
# OR for HTTP tool:
# template:
#   type: http
#   method: POST
#   url: "https://api.example.com/{{.args.endpoint}}"
#   headers: {"X-Key": "{{.config.api_key}}"}
#   body: '{"q": "{{.args.q}}"}'
# OR for static (调试/干跑):
# template:
#   type: echo
#   text: "you called greet with name={{.args.name}}"
sandbox:
  exec: allow         # allow | deny | confirm
  read: deny
  write: deny
```

**关键事实**（来自调研）：
- `Registry.Register(t Tool, h ToolHandler)` 已存在并 thread-safe，`Unregister` 也已存在 — 增/删都是单 API
- LLM schema 走 `Registry.List()` → `ToolsFromRegistryDef` → 协议无关的 `ToolDef`（`client.go:802-826`），新工具零代码自动接 LLM
- 系统 prompt 的"Available Tools" 表格也走 `Registry.List()`（`agent.go:753-777`），自动更新
- 5s polling watcher 模板在 `internal/rules/rules.go:163-258`，复制即可用；不用 fsnotify（CLAUDE.md §4.4 解释了 Windows 兼容原因）
- `gopkg.in/yaml.v3` 已在 `go.mod`（`config/migrate.go` 用过）
- 唯一需要小心的：`agent/confirm_target.go:57-137` 的 sandbox 确认 switch 是**硬编码**工具名（`exec_command` / `write_file` / `read_file` / `read_docx` / `read_pdf` / `list_files`）—— 动态工具**目前**会 bypass confirm modal 直接执行。需要扩展 switch 加 `dynamic:` 分支走 `sandbox.CheckExecBool` 通用判断
- CLI `/tools` 已能 list（`commands.go:2083-2148`），但**无 `GET /api/v1/tools` HTTP 端点**，前端看不到工具列表

**改动**：

| 文件 | 改动 |
| --- | --- |
| `internal/tool/dynamic/dynamic.go` (新) | `LoadFromDir(dir string) ([]Spec, error)` — 扫描 `*.yaml` 调 `ParseSpec` |
| `internal/tool/dynamic/parse.go` (新) | `ParseSpec(data []byte) (Spec, error)` — yaml.v3 解析 + JSON schema 校验 |
| `internal/tool/dynamic/exec.go` (新) | `func(ctx, args) → render template → exec.CommandContext → return stdout/stderr` |
| `internal/tool/dynamic/http.go` (新) | `func(ctx, args) → render template → http.NewRequestWithContext(ctx,...) → return body` |
| `internal/tool/dynamic/echo.go` (新) | `func(ctx, args) → return text` |
| `internal/tool/dynamic/watcher.go` (新) | 5s polling（镜像 `internal/rules/rules.go`）— 对比 `(name, modtime)` 集合，增量调用 `Register/Unregister` |
| `internal/tool/dynamic/dynamic_test.go` (新) | parse 3 个 case + 模板渲染 + watcher 增删 |
| `internal/tool/registry.go` | `BuildDynamicHandler(spec Spec) ToolHandler` 工厂（按 `template.type` 选 exec/http/echo） |
| `internal/agent/confirm_target.go` | switch 加 `default:` 分支走 `sandbox.CheckExecBool(args.Command)`；动态工具的 confirm 文案显示工具名 |
| `internal/server/handler.go` | `GET /api/v1/tools` 端点返回 `[]Tool`（定义+handler 类型+source file） |
| `internal/server/server.go` | 注册 `/tools` 路由（line 119-257 旁）；启动时 `dynamic.Watch(toolReg, paths.ToolsDir(), 5*time.Second)` |
| `internal/cli/commands.go` | `/tools` 增 `reload` 子命令手动 reload（已有 `on/off/list`） |
| `frontend/src/api/client.ts` | `listTools() → Promise<Tool[]>` + `Tool` 类型 |
| `frontend/src/stores/chat.ts` | `loadTools()` action + `state.tools: Tool[]` |
| `frontend/src/components/TopBar.vue` 或新 `ToolListDrawer.vue` | 抽屉显示所有工具（高亮自定义/动态），点击可看 YAML 源 |

**关键点**：
- **沙箱仍走同一接口**：`sandbox.CheckExecBool` / `CheckWriteDecision`（已存在，dynamic tool 复用）
- **错误隔离**：YAML 解析失败 → log warning + 跳过该文件；不让一个坏 YAML 把所有动态工具都打挂
- **模板渲染用 `text/template`**（标准库），不要引入 sprig。`{{.args.foo}}` / `{{.config.api_key}}`（从 `~/.p-chat/config.yaml` 读 `dynamic.<tool_name>.config`）
- **不做权限管理**：v1 假设 `~/.p-chat/tools/` 是用户自己控制的；不引入"信任级别"机制（用户可绕过）
- **不做 schema 自动校验**：依赖 yaml 里 `parameters:` 字段；缺字段时 LLM 会传错，工具报错返回即可

**测试**：
- `dynamic_test.go`：3 个 parse 单元测试 + 模板渲染测试 + watcher 增删
- 手动：`echo 'name: t1\ndescription: x\ntemplate:\n  type: echo\n  text: hi' > ~/.p-chat/tools/t1.yaml` → 5s 后 `/tools` 看到 t1

**回滚**：删 `dynamic.Watch` 注册即可（已经在用但 Unregister 后立刻消失）。

---

### 🟢 P2-5 — 多 LLM 并行对比 (race mode)

**现状**：用户发问只能选 1 个 `(provider, model)`。`sessionLocks` (`handler.go:2548`) 拒绝同一 session 的第二次 send 409。`ForkConversation` 已能复制历史生成新 convID。LLM client 已支持 per-call `(provider, model)`。`streamEvents` 已带 `Provider` + `Model` 字段，但**无 `stream_id` 区分**——N 个 pane 在同一 session 没法 disambiguate。

**目标**：用户选 2-3 个 `(provider, model)` 对，发一条 prompt，3 个 LLM 同步跑，UI 分 3 pane 显示，结束后 3 个 pane 各有一个 "🏆 选这个" 按钮；点 winner → fork 它的 session 为新主线，原 session 留作"loser 仓库"。

**两种 race 模式**（v1 只做 A）：
- **A. Per-pane session**（推荐）：每个 pane 是独立的 `Conversation`（fork 自同一 baseline）；零 backend 改动，store 天然隔离
- **B. Single-session multiplex**：一个 session 跑 N 个 agent loop；需要 `stream_id` 字段 + 并发锁修改 + 多 parts slot

**A 模式流程**：
```
用户选 [gpt-4o, claude-sonnet-4, doubao-pro] 3 个模型
  → 前端先调 2 次 POST /sessions/:base/fork (BeforeID=last) 拿到 3 个 convId
    → 并发 3 个 POST /sessions/:convId/messages (各自的 provider/model)
      → 3 个独立 SSE 流（已支持）
        → 前端监听 3 个流，路由到 3 个 pane（按 convId）
          → 结束后每 pane 一个 "🏆" 按钮
            → 点击：调 POST /sessions/:winnerConvId/fork 把它 fork 成新主线
              → 切换 state.currentID = newConvId
```

**关键事实**（来自调研）：
- 已有 `ForkSession` endpoint（`handler.go:1302-1332`），调用 `ForkConversation(sourceConvID, beforeID)` 复制 rows 拿到新 convID。**0 backend 代码改动**。
- 已有并发 3 个 SSE 流的基建（Wails proxy 按 sessionId 过滤；fetch 路径无 filter 但多 session 也 OK）
- `streamMessages` / `streamMessagesViaFetch` 已支持任意数量并发；前端用 `AbortController` 管理
- `state.streaming[id]` 已按 sessionId 分键；3 个 session 各自一个 streaming entry
- 唯一的前端复杂度：3 pane UI 布局 + race 结束态

**改动**（A 模式，v1）：

| 文件 | 改动 |
| --- | --- |
| `frontend/src/api/client.ts` | `startRace(opts: { baseSessionId; candidates: {provider,model}[] })` 串行 fork + 并发 stream；返回 `RaceHandle { paneIds[], cancel() }` |
| `frontend/src/stores/chat.ts` | `state.race: { id, panes: paneState[], status: 'pending'\|'streaming'\|'complete' }` + `startRace()` / `cancelRace()` / `pickWinner(paneId)` |
| `frontend/src/components/RaceView.vue` (新) | 3-pane 布局（CSS grid 1×3 desktop / 单列 mobile）；每 pane 是 `MessageBubble` 子树；底部 "🏆 选这个" 按钮 |
| `frontend/src/components/ChatWindow.vue` | 收到 `state.race` 时切到 `RaceView` 而不是 `MessageList`；race 结束后切回（用 winner 替换 current session） |
| `frontend/src/components/InputArea.vue` | 模式切换器：`Single` / `Race`（race 模式多一个候选选择器，3 个 slot，可禁用） |
| `frontend/src/api/client.ts` | 增 `pickWinner(raceId, paneConvId)` — 调 fork + 切 currentID |
| `internal/server/handler.go` | `ForkSession` 已支持；**0 改动** |
| `internal/agent/agent.go` | **0 改动** |
| `internal/llm/client.go` | **0 改动** |
| `internal/memory/memory.go` | **0 改动** |

**关键点**：
- A 模式 = 纯前端 race orchestrator。复杂度都在前端。
- 候选上限 3（v1）；UI 用 CSS grid
- 工具调用跨 pane 共享：3 个 pane 都注册同一组工具（`Registry.List()` 全局唯一）
- sub-agent 跨 pane 不共享：sub-agent 是 per-session 的
- race 中途取消：`AbortController` 3 个一起 abort
- winner 持久化：`ForkSession(winnerConvId, lastID)` → 拿到 newConvId；前端 `loadSession(newConvId)` 切到新 session

**测试**：
- 手动：选 3 个不同 provider 发问 → 3 个 pane 同步流 → 点 winner
- E2E：暂无

**回滚**：删 `RaceView.vue` + `state.race` 字段 + `InputArea` 模式切换器。

**v2 (B 模式) 暂不做**——单 session multiplex 需要更多 infra（`stream_id` 字段、`sessionLocks` 改造、partsAcc 并发安全），ROI 不如 A 模式。v1 验证用户价值后再决定。

---

## 2. 本轮实施计划

按 ROI + 风险排序：

| 顺序 | 任务 | 工作量 | 风险 | 用户可见 |
| --- | --- | --- | --- | --- |
| Day 1 | **P3-3 trace ID** | 1d | 低（沿用 seq 模式） | 报错时一键复制 trace id |
| Day 2-3 | **P3-2 hot-reload** | 2d | 中（动态 sandbox 走通要小心） | `~/.p-chat/tools/foo.yaml` 即时生效 |
| Day 4-6 | **P2-5 race mode (A)** | 3d | 中（前端并发 UI 调试） | 3 pane 同步对比 + 选 winner |

合计 6d。每完成一个独立提交 + 文档。

---

## 3. P3-3 详细设计

### Trace ID 格式

```go
// internal/trace/trace.go
package trace

import (
    "crypto/rand"
    "encoding/hex"
    "context"
)

const prefix = "T-"

func NewID() string {
    b := make([]byte, 4)  // 4 bytes = 8 hex chars
    if _, err := rand.Read(b); err != nil {
        return prefix + "00000000"  // never happens; crypto/rand 不会失败
    }
    return prefix + hex.EncodeToString(b)
}

type ctxKey struct{}

func WithID(ctx context.Context, id string) context.Context {
    return context.WithValue(ctx, ctxKey{}, id)
}

func FromContext(ctx context.Context) string {
    if v, ok := ctx.Value(ctxKey{}).(string); ok { return v }
    return ""
}
```

### Middleware

```go
// internal/server/server.go (新加在 corsMiddleware 旁)
func traceIDMiddleware() gin.HandlerFunc {
    return func(c *gin.Context) {
        tid := c.GetHeader("X-Trace-Id")
        if tid == "" {
            tid = trace.NewID()
        }
        c.Set("trace_id", tid)
        c.Header("X-Trace-Id", tid)
        c.Request = c.Request.WithContext(trace.WithID(c.Request.Context(), tid))
        c.Next()
    }
}
```

### Agent 注入

```go
// internal/agent/agent.go ChatWithTools 头部
tid := req.TraceID
if tid == "" { tid = trace.FromContext(ctx) }
if tid != "" {
    ctx = trace.WithID(ctx, tid)
}
// 工具派发前:
toolCtx := tool.WithTraceID(tctx, tid)
```

### LLM header

```go
// internal/llm/client.go openaiStream, 在 http.NewRequestWithContext 后
if tid := trace.FromContext(ctx); tid != "" {
    httpReq.Header.Set("X-Trace-Id", tid)
}
// internal/llm/anthropic.go 同上
```

### Tool handler log

```go
// 工具 handler 起手 (registry.go 449)
tid := TraceIDFromCtx(ctx)
log.Printf("[%s] [tool/exec_command] start cmd=%q", tid, a.Command)
```

### 前端错误 chip

```vue
<!-- MessageBubble.vue 错误 part 渲染处 -->
<button v-if="ev.trace_id" class="trace-id-chip" @click="copyText(ev.trace_id)">
  📋 {{ ev.trace_id }}
</button>
```

### 测试

`internal/server/handler_trace_test.go`:
1. `TestTraceID_HeaderPassthrough` — 客户端发 `X-Trace-Id: T-abc12345` → 响应 header + SSE event.trace_id 一致
2. `TestTraceID_GeneratedWhenMissing` — 不发 header → 服务端生成 8-char `T-` 前缀
3. `TestTraceID_InErrorEvent` — 制造 500 → 错误 SSE event 带 `trace_id`

---

## 4. P3-2 详细设计

### Spec 解析

```go
// internal/tool/dynamic/parse.go
type Spec struct {
    Name        string                 `yaml:"name"`
    Description string                 `yaml:"description"`
    Parameters  json.RawMessage        `yaml:"parameters"`  // JSON Schema object
    Template    Template               `yaml:"template"`
    Sandbox     SandboxConfig          `yaml:"sandbox"`
    Source      string                 `yaml:"-"`           // 加载时填，watcher 用
    ModTime     time.Time              `yaml:"-"`
}

type Template struct {
    Type    string            `yaml:"type"`    // "exec" | "http" | "echo"
    Command string            `yaml:"command,omitempty"`
    Method  string            `yaml:"method,omitempty"`
    URL     string            `yaml:"url,omitempty"`
    Headers map[string]string `yaml:"headers,omitempty"`
    Body    string            `yaml:"body,omitempty"`
    Text    string            `yaml:"text,omitempty"`
    Timeout Duration          `yaml:"timeout,omitempty"`
}

type SandboxConfig struct {
    Exec  string `yaml:"exec"`  // "allow" | "deny" | "confirm"
    Read  string `yaml:"read"`
    Write string `yaml:"write"`
}

func ParseSpec(data []byte) (Spec, error) {
    var s Spec
    if err := yaml.Unmarshal(data, &s); err != nil { return s, err }
    if s.Name == "" || s.Description == "" { return s, errors.New("name and description required") }
    if s.Template.Type == "" { return s, errors.New("template.type required") }
    return s, nil
}
```

### Handler 工厂

```go
// internal/tool/registry.go (新增)
func BuildDynamicHandler(spec dynamic.Spec) ToolHandler {
    switch spec.Template.Type {
    case "exec":
        return dynamic.MakeExecHandler(spec)
    case "http":
        return dynamic.MakeHTTPHandler(spec)
    case "echo":
        return dynamic.MakeEchoHandler(spec)
    default:
        return func(ctx context.Context, args json.RawMessage) (*CallResult, error) {
            return &CallResult{Content: fmt.Sprintf("unknown template type: %s", spec.Template.Type), IsError: true}, nil
        }
    }
}
```

### 模板渲染

```go
// internal/tool/dynamic/render.go
type RenderCtx struct {
    Args   map[string]any  // 从 json.Unmarshal(args) 得来
    Config map[string]any  // ~/.p-chat/config.yaml -> dynamic.<name>.config
}

func render(s string, rc RenderCtx) (string, error) {
    t, err := template.New("").Option("missingkey=zero").Parse(s)
    if err != nil { return "", err }
    var buf bytes.Buffer
    if err := t.Execute(&buf, rc); err != nil { return "", err }
    return buf.String(), nil
}
```

### Watcher

```go
// internal/tool/dynamic/watcher.go
func Watch(reg *tool.Registry, dir string, onChange func(), interval time.Duration) error {
    state := map[string]time.Time{}  // name -> mtime
    go func() {
        ticker := time.NewTicker(interval)
        defer ticker.Stop()
        for range ticker.C {
            changed := false
            entries, _ := os.ReadDir(dir)
            seen := map[string]bool{}
            for _, e := range entries {
                if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") { continue }
                full := filepath.Join(dir, e.Name())
                info, err := e.Info()
                if err != nil { continue }
                name := strings.TrimSuffix(e.Name(), ".yaml")
                seen[name] = true
                if state[name] != info.ModTime() {
                    spec, err := loadSpec(full, name, info.ModTime())
                    if err != nil {
                        log.Printf("[dynamic] skip %s: %v", name, err)
                        continue
                    }
                    reg.Unregister(name)  // 先 unregister 防止残留
                    reg.Register(tool.Tool{
                        Name: spec.Name,
                        Description: spec.Description,
                        Parameters: spec.Parameters,
                    }, tool.BuildDynamicHandler(spec))
                    state[name] = info.ModTime()
                    changed = true
                }
            }
            // 删了的文件
            for name := range state {
                if !seen[name] {
                    reg.Unregister(name)
                    delete(state, name)
                    changed = true
                }
            }
            if changed && onChange != nil { onChange() }
        }
    }()
    return nil
}
```

### Sandbox 集成

```go
// internal/agent/confirm_target.go switch 增加 default:
default:
    // 动态工具：读 sandbox 配置
    if t, ok := reg.Lookup(name); ok {
        // 看 dynamic spec 里的 sandbox.exec 字段
        switch spec.Sandbox.Exec {
        case "deny": return false, nil
        case "allow": return false, nil
        case "confirm": return true, nil
        }
    }
    return false, nil
```

实际实现里需要把 `reg` 和 spec 串起来——可能改成 `confirm_target.go` 多收一个 `*Registry` 参数，lookup 失败就 fallback。

### 启动接入

```go
// internal/server/server.go (在 rules.Watch 旁)
if err := dynamic.Watch(toolReg, paths.ToolsDir(), func() {
    h.agent.Reload()  // 让 static prompt 重新生成 available tools 表格
}, 5*time.Second); err != nil {
    log.Printf("[server] WARN: dynamic tool watcher failed: %v", err)
}
```

### `GET /api/v1/tools` 端点

```go
// internal/server/handler.go
func (h *Handler) ListTools(c *gin.Context) {
    tools := h.toolReg.List()
    out := make([]ToolInfo, 0, len(tools))
    for _, t := range tools {
        info := ToolInfo{
            Name: t.Name,
            Description: t.Description,
            Parameters: t.Parameters,
        }
        // 标 dynamic 来源
        if source, ok := h.toolReg.SourceFile(t.Name); ok {
            info.Source = source
            info.Dynamic = true
        }
        out = append(out, info)
    }
    c.JSON(http.StatusOK, gin.H{"tools": out})
}
```

`SourceFile` 需要 `Registry` 加个 `meta[name].Source` 字段追踪；可在 `Register` 时填。

### 前端 ToolListDrawer

```vue
<!-- ToolListDrawer.vue (新) -->
<NDrawer v-model:show="show" :width="500" placement="right">
  <NDrawerContent title="可用工具">
    <NList>
      <NListItem v-for="t in tools" :key="t.name">
        <NThing>
          <template #header>
            <NTag :type="t.dynamic ? 'warning' : 'default'">{{ t.name }}</NTag>
            <NTag v-if="t.dynamic" size="small" type="info">自定义</NTag>
          </template>
          <template #description>{{ t.description }}</template>
        </NThing>
        <pre v-if="t.dynamic" class="yaml-source">{{ t.source }}</pre>
      </NListItem>
    </NList>
  </NDrawerContent>
</NDrawer>
```

### 测试

`internal/tool/dynamic/dynamic_test.go`:
1. `TestParseSpec_Valid` — 3 种 template type
2. `TestParseSpec_MissingFields` — 缺 name/desc/type
3. `TestRender_Args` — `{{.args.foo}}` 替换
4. `TestRender_Config` — `{{.config.key}}` 替换
5. `TestWatcher_AddRemove` — 创建文件 → 5s 内 Register；删文件 → Unregister

---

## 5. P2-5 详细设计 (A 模式)

### 数据流

```ts
// frontend/src/stores/chat.ts
type Race = {
  id: string
  baseSessionId: string
  panes: PaneState[]      // 长度 = candidates.length
  status: 'pending' | 'streaming' | 'complete' | 'cancelled'
  winnerPaneId: string | null
}

type PaneState = {
  paneId: string         // == convId（per-pane session）
  provider: string
  model: string
  status: 'pending' | 'streaming' | 'complete' | 'error'
  ctrls: AbortController[]
  messageIds: number[]   // 该 pane 产生的 message row IDs
}
```

### Race 启动

```ts
// frontend/src/stores/chat.ts
export async function startRace(opts: {
  baseSessionId: string
  prompt: string
  candidates: { provider: string; model: string }[]
  attachments?: Attachment[]
}) {
  const race: Race = {
    id: 'race-' + Date.now(),
    baseSessionId: opts.baseSessionId,
    panes: opts.candidates.map((c, i) => ({
      paneId: '',  // fill in below
      provider: c.provider,
      model: c.model,
      status: 'pending',
      ctrls: [],
      messageIds: [],
    })),
    status: 'pending',
    winnerPaneId: null,
  }
  state.race = race
  state.race.status = 'streaming'

  // 1. 并发 fork N 个 session (BeforeID = 最新的 user msg id)
  const lastUserMsgId = lastUserMessageId(opts.baseSessionId)
  const forks = await Promise.all(
    opts.candidates.map(c => api.forkSession(opts.baseSessionId, lastUserMsgId))
  )
  race.panes.forEach((p, i) => { p.paneId = forks[i].id })

  // 2. 并发 send 3 个 message（用各自的 paneId）
  await Promise.all(race.panes.map(async (p) => {
    const ctrl = new AbortController()
    p.ctrls.push(ctrl)
    try {
      await api.streamMessages(p.paneId, {
        message: opts.prompt,
        provider: p.provider,
        model: p.model,
        attachments: opts.attachments,
        signal: ctrl.signal,
        onEvent: ev => appendStreamEvent(p.paneId, ev),
      })
      p.status = 'complete'
    } catch (e) {
      p.status = 'error'
    }
  }))

  race.status = 'complete'
}
```

### Winner 选定

```ts
export async function pickWinner(raceId: string, paneId: string) {
  const race = state.race
  if (!race || race.id !== raceId) return
  const lastId = lastMessageId(paneId)
  const forked = await api.forkSession(paneId, lastId)
  await loadSession(forked.id)   // 切到新 session
  await cancelRace()              // 清理其他 pane 的 stream
  state.race = null
}
```

### UI 布局

```vue
<!-- RaceView.vue -->
<template>
  <div class="race-view">
    <div v-for="(pane, i) in race.panes" :key="pane.paneId" class="pane">
      <header class="pane-header">
        <span class="pane-model">{{ pane.provider }} / {{ pane.model }}</span>
        <span class="pane-status">{{ pane.status }}</span>
        <button v-if="race.status === 'complete'" @click="pick(pane.paneId)">🏆 选这个</button>
      </header>
      <MessageList :session-id="pane.paneId" />  <!-- 复用现有 MessageBubble -->
    </div>
  </div>
</template>

<style scoped>
.race-view {
  display: grid;
  grid-template-columns: repeat(v-bind('race.panes.length'), 1fr);
  gap: 8px;
  height: 100%;
  overflow: hidden;
}
.pane { overflow-y: auto; border-right: 1px solid var(--border); }
</style>
```

### InputArea 模式切换

```vue
<!-- InputArea.vue 顶部加 segmented -->
<NSegment v-model:value="mode" :options="[
  { label: '单线', value: 'single' },
  { label: '对比', value: 'race' },
]" />
<!-- race 模式时显示 3 个候选选择器 -->
```

### 测试

- 手动：选 3 模型 → 发问 → 3 pane 同步流 → 选 winner
- 取消：race 中途点取消 → 3 个 ctrl abort

---

## 6. 改动文件汇总

| 类别 | 文件 | 任务 |
| --- | --- | --- |
| 新建 | `internal/trace/trace.go` | P3-3 |
| 新建 | `internal/server/handler_trace_test.go` | P3-3 |
| 新建 | `internal/tool/dynamic/dynamic.go` | P3-2 |
| 新建 | `internal/tool/dynamic/parse.go` | P3-2 |
| 新建 | `internal/tool/dynamic/exec.go` | P3-2 |
| 新建 | `internal/tool/dynamic/http.go` | P3-2 |
| 新建 | `internal/tool/dynamic/echo.go` | P3-2 |
| 新建 | `internal/tool/dynamic/watcher.go` | P3-2 |
| 新建 | `internal/tool/dynamic/dynamic_test.go` | P3-2 |
| 新建 | `frontend/src/components/RaceView.vue` | P2-5 |
| 新建 | `frontend/src/components/ToolListDrawer.vue` | P3-2 |
| 改 | `internal/server/server.go` | P3-3 + P3-2 |
| 改 | `internal/server/handler.go` | P3-3 + P3-2 |
| 改 | `internal/agent/agent.go` | P3-3 |
| 改 | `internal/agent/confirm_target.go` | P3-2 |
| 改 | `internal/llm/client.go` | P3-3 |
| 改 | `internal/llm/anthropic.go` | P3-3 |
| 改 | `internal/tool/registry.go` | P3-2 + P3-3 |
| 改 | `frontend/src/api/client.ts` | 全部 3 项 |
| 改 | `frontend/src/stores/chat.ts` | 全部 3 项 |
| 改 | `frontend/src/components/MessageBubble.vue` | P3-3 |
| 改 | `frontend/src/components/ChatWindow.vue` | P2-5 |
| 改 | `frontend/src/components/InputArea.vue` | P2-5 |
| 改 | `frontend/src/components/TopBar.vue` | P3-2 |
| 改 | `internal/cli/commands.go` | P3-2 (`/tools reload`) |

总计 **9 新 + 14 改 = 23 个文件**。后端 / 前端 比例约 60/40。

---

## 7. 验收标准

### P3-3
- [ ] SSE 响应 header `X-Trace-Id: T-xxxxxxxx` 出现
- [ ] 每个 SSE event JSON 都有 `trace_id` 字段
- [ ] 错误气泡的"复制 trace id"按钮可点击，剪贴板内容正确
- [ ] 缺失 header 时服务端生成 8-char `T-` 前缀
- [ ] log 行 `[T-xxxxxxxx] [agent] ...` 格式统一
- [ ] LLM 出站请求 header 含 `X-Trace-Id`

### P3-2
- [ ] `~/.p-chat/tools/foo.yaml` 写完 5s 内 `/tools` 列表出现
- [ ] 删文件 5s 内 `/tools` 列表消失
- [ ] YAML 解析失败 log warn 不影响其他动态工具
- [ ] `GET /api/v1/tools` HTTP 端点返回
- [ ] CLI `/tools` 显示
- [ ] 抽屉 UI 显示工具 + 动态标 + YAML 源
- [ ] exec 模板走 sandbox（`sandbox.exec: deny` 时拒绝）

### P2-5
- [ ] InputArea 模式切换 single/race
- [ ] race 模式选 3 模型发问，3 pane 同步流
- [ ] 任一 pane 出错不影响其他 pane
- [ ] "🏆 选这个" 按钮可点；点完切到新 session
- [ ] race 中途取消所有 stream abort
- [ ] 切到 mobile 单列布局不破

---

## 8. 风险

| 风险 | 等级 | 缓解 |
| --- | --- | --- |
| P3-3 trace id 改所有 log.Printf 工作量大 | 中 | 一次性脚本扫；用 sed/python 批量改 |
| P3-2 动态工具 bypass sandbox | 高 | confirm_target.go default 分支走 `CheckExecBool`；文档明示用户责任 |
| P3-2 YAML 解析成 vector for code exec | 低 | yaml.v3 不解析 `!!python/...` 等危险 tag；不引入第三方模板引擎 |
| P2-5 3 pane SSE 互相错乱 | 中 | 用 sessionId 隔离；不用 multiplex |
| P2-5 race 中途 user 点新消息 | 中 | race 进行中 InputArea disabled |
| P2-5 mobile 单列 3 pane 体验差 | 低 | mobile 自动降级为 1 pane 串行 |

---

## 9. 不在本轮（明确推迟）

- **A/B 模式 race v2**（单 session multiplex）— ROI 不如 A；先验证用户价值
- **P3-2 权限模型**（trust level / signature 校验）— 假设用户控制自己 `~/.p-chat/`
- **P3-2 项目级 tools 目录**（`<root>/.p-chat/tools/`）— v1 仅全局
- **P3-3 跨 LLM 调用追踪**（OpenAI 的 response id 入 trace）— 第三方不返回

---

## 10. 文档收尾

每完成一个任务更新对应模块文档：

| 任务 | 更新 |
| --- | --- |
| P3-3 | `.agents/docs/server.md` 加 trace 章节；`.agents/docs/agent.md` 加 trace 注入说明；CHANGELOG v1.0.9 |
| P3-2 | `.agents/docs/tool.md` 加 hot-reload 章节（YAML 格式 + sandbox）；新增 `.agents/docs/dynamic-tools.md` 用户向文档；CHANGELOG v1.0.9 |
| P2-5 | `.agents/docs/frontend.md` 加 race mode 章节；CHANGELOG v1.0.9 |

汇总为一条文档收尾 commit。
