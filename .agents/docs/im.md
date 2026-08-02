# IM 桥接模块

> **位置**：`internal/im/`
> **依赖**：`agent`（只读），`memory`（写 session），`config`（读 `IMConfig`），`style`（读 persona），`tool`（白名单）
> **被依赖**：`server`（注册 `/api/v1/im/*` 路由 + SSE lifecycle 事件）、`cmd/pchat-server`（启动 Gateway）

## 概述

IM 桥接模块把 P-Chat 暴露为多平台聊天机器人。**核心形态**（参考 Hermes Agent）：

- 单一长连接 **Gateway 进程** 管理所有平台
- 每个平台一个 **Adapter**（inbound 翻译）+ **OutboundRenderer**（outbound 渲染）
- **统一事件总线**（`IMEvent` / `IMOutChunk`）把消息规范化后送入现有 `agent.ChatWithTools()`
- 跨平台 session 续传（同一用户在 TG / 飞书共享 principal session）
- per-channel persona（不同平台不同风格 / 工具集 / 模型）

**当前实现状态**：🚧 P-IM-1 后端骨架已部分落地：Gateway / adapter 抽象、IMConfig、管理 API、飞书 webhook 文本入站、飞书文本出站 renderer、OutboundDispatcher 与长文本切分已具备；下一步优先做 P-IM-1.5 GUI 设置 MVP，再补入站到 Agent 主流程。完整方案见 [`docs/plans/im-bridge-plan.md`](../../docs/plans/im-bridge-plan.md)。

## 用户面向说明

- **GUI 怎么用**：设置 → "IM 桥接" Tab；可视化连接配置见 `docs/plans/im-bridge-plan.md` §14
- **CLI 怎么用**：`pchat-server im start / stop / status / test / cron add`
- **配置文件**：`~/.p-chat/config.json` 的 `im` 块（schema 见 plan §13）

## 文件结构（落地后）

| 文件 / 目录 | 职责 |
| --- | --- |
| `gateway.go` | Gateway 主循环、Bus、生命周期 |
| `adapter.go` | `Adapter` / `OutboundRenderer` 接口 |
| `event.go` | `IMEvent` / `IMOutChunk` / `ChatRef` / `SenderRef` |
| `session.go` | IM 元组 → session key 解析 + principal 聚合 |
| `persona.go` | per-channel persona 匹配 |
| `ratelimit.go` | 三级限流（platform / chat / sender）|
| `fallback.go` | Fallback 链 |
| `tokencache.go` | 平台 token 续期缓存 |
| `audit/` | JSONL 审计日志 |
| `metrics/` | 健康检查 + 计数器 |
| `outbound/` | OutboundDispatcher + Markdown 平台方言 |
| `media/` | STT / TTS / Vision / File extract |
| `cron/` | 调度（`robfig/cron/v3`）|
| `cmd/` | IM 端斜杠命令注册 + mention 解析 |
| `<platform>/` | 平台 adapter：feishu / wecom / telegram / qq / wechat / slack / discord / dingtalk |

## 核心概念

### 1. Gateway 进程模型

`Gateway` 单例常驻 `pchat-server` 进程内。**不**在每个 adapter 内自起 HTTP server / WS loop；adapter 启动后把事件灌入 `Gateway.in` channel，由 Gateway 统一：

1. **Mention 解析** → 是否对 bot 说的？
2. **Session 解析** → Channel / Principal / Topic
3. **Persona 解析** → per-channel 风格 / 工具 / 模型
4. **限流** → 三级 token bucket
5. **命令解析**（`/` 前缀）→ IM 端 cmd registry
6. **委派 agent** → `ChatWithTools()` 拿 SSE chunk
7. **Outbound 派发** → 平台 Renderer 渲染 + 限频 edit
8. **审计 + 指标**

### 2. Adapter 抽象

```go
type Adapter interface {
    Platform() string
    Variants() []string
    Start(ctx context.Context, in chan<- IMEvent) error
    Stop(ctx context.Context) error
    Health() HealthStatus
}

type OutboundRenderer interface {
    Send(ctx context.Context, chunk IMOutChunk) error
    Edit(ctx context.Context, ref ChatRef, msgID string, chunk IMOutChunk) error
    Typing(ctx context.Context, ref ChatRef) error
    MaxTextLen() int
    MarkdownDialect() MarkdownDialect
}
```

每个平台一个 adapter + renderer，**in/out 严格分离**（同一平台多 renderer 不会冲突）。

### 3. 事件规范化

| 类型 | 用途 | 关键字段 |
| --- | --- | --- |
| `IMEvent` | Inbound 入站 | `Chat` `Sender` `Text` `Mentions` `ReplyTo` `Attachments` `Raw` |
| `IMOutChunk` | Outbound 出站（流式）| `MsgID` `Kind` `Text` `Parts` `Done` |

### 4. Session 三层

| 层 | key | 用途 |
| --- | --- | --- |
| **Channel** | `im:{platform}:{chat_id}:{thread_id?}` | 平台侧独立 |
| **Principal** | `principal:{principal_id}` | 跨平台汇总（identity link 命中后）|
| **Topic** | `principal:{principal_id}:topic:{topic_hash}` | 跨平台按主题分桶 |

### 5. Per-channel Persona

`IMConfig.Personas` map，key 形如 `feishu:group:*`，匹配顺序：

```
exact(platform:chatType:senderID) > glob(platform:chatType:*) > glob(platform:*) > default
```

每个 persona 含 `style` / `work_mode` / `model` / `tools_allow` / `prompt_inject`。注入位置在 `internal/agent/agent.go` 的 `buildStaticSystemPrompt()` 之前，与 `buildWorkModeBlock()` 同级。

### 6. 命令总线

GUI / CLI / IM 三端共享 `internal/cmdregistry/` 注册表；IM 端额外做 **mention 解析**（避免群聊噪声）和 **平台方言 button**（飞书 / TG 的 callback_query）。

### 7. Build Tag 隔离

```bash
go build ./cmd/pchat-server                          # 内置：飞书 / 企微 / TG
go build -tags im_qq ./cmd/pchat-server              # + QQ
go build -tags "im_qq im_wx" ./cmd/pchat-server      # + 微信（第三方高风险）
```

微信 / QQ 私域 build tag 隔离的原因：合规与封号风险。

## 平台支持矩阵

| 平台 | 通道 | 风险 | 默认 |
| --- | --- | --- | --- |
| 飞书 Bot v3 | WS / Webhook | 官方低 | ✅ |
| 飞书 OpenAPI 自建应用 | WS / Webhook | 官方低 | ✅（variant 切换）|
| 企业微信 | Webhook | 官方低 | ✅ |
| Telegram | Polling / Webhook | 官方低 | ✅ |
| QQ 频道 | WS / Webhook | 官方低 | `-tags im_qq` |
| QQ 私域 | WS / Webhook | 官方中（沙箱）| `-tags im_qq` |
| 微信 wechatbot | WS | **第三方高** ⚠ | `-tags im_wx` |
| Slack | Events API | 官方低 | 占位 |
| Discord | Gateway | 官方低 | 占位 |
| 钉钉 | Bot / App | 官方低 | 占位 |

完整能力矩阵见 plan §3。

## 关键扩展点（与现有模块的接口边界）

| 接入点 | 现有模块 | 改动量 |
| --- | --- | --- |
| 发送消息 | `agent.ChatWithTools(ctx, req)` | 0（直接调用）|
| Persona 注入 | `internal/agent/agent.go:buildStaticSystemPrompt()` | 新增 `buildPersonaBlock()` |
| 斜杠命令 | `internal/cli/commands.go` | 抽出 `internal/cmdregistry/` |
| Session 持久化 | `internal/memory.Store` | 0（复用 SessionKey）|
| 配置 | `internal/config/IMConfig` | 新增字段 |
| SSE 事件流 | `internal/server/handler.go:chunkToEvent()` | `Gateway` 暴露 `StreamBroker()` 给 GUI |
| HTTP 路由 | `internal/server/server.go` | 新增 `/api/v1/im/*` group |
| Wails / GUI | `frontend/src/components/AppSettingsModal.vue` + `IMSettings.vue` | 新增 `im` Tab，接入配置、健康状态、测试连接、事件流 |

## 测试策略

- **单元**：每个 adapter 一组 `*_test.go`，mock 平台 HTTP/WS server，验证 `IMEvent` 转换正确
- **集成**：用 `httptest` 起 `Gateway` + 假 adapter，验证端到端消息流
- **E2E**：人工跑通 Telegram polling + 飞书 WS + 企微 Webhook
- **回归**：`go test -count=1 ./...` 必须通过

## 排期

| 阶段 | 内容 | 估时 |
| --- | --- | ---|
| P-IM-1 骨架 | Gateway + 抽象 + Config + CLI flag + build tag | 2~3 天 |
| P-IM-1.5 GUI 设置 MVP | 应用设置新增 IM 桥接 Tab，配置不同 IM 平台连接 | 1~2 天 |
| P-IM-2 ~ 7 | 5 个平台 adapter | 13~17 天 |
| P-IM-8 ~ 12 | Session/Persona/Cron/Media/Outbound/GUI/CLI | 12~13 天 |
| **合计** | | **27~33 人天** |

详见 plan §19。

## 待确认决策

详见 plan §20。
