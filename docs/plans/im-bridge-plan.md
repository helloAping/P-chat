# IM 桥接方案（IM Bridge Plan）

> 目标：让 P-Chat 作为 IM 桥接端运行在 **飞书 / 企业微信 / 个人微信 / QQ / Telegram / 钉钉 / Slack** 等平台之上，复用现有 LLM + Agent + 工具 + 风格系统。
>
> 范围：设计方案 + 配置文件 schema + 实施分期。**不含代码**，代码落在 P-IM-1 之后逐阶段提 PR。
>
> 文档沿用 v0.x 演进史（v0.1 → v0.2 → v0.3），最终态以本文档"§13 终态 schema"为准。

---

## 0. 演进摘要

| 版本 | 关键变化 |
| --- | --- |
| v0.1 | 五大平台 + 风险分级 + 设置 Tab + CLI flag + session 粒度 |
| v0.2 | 飞书 Bot v3 / OpenAPI 双通道；QQ 私域 vs 频道；微信 wechatbot 第三方；SDK/依赖选型；build tag 隔离 |
| v0.3 | 引入 **Gateway 进程模型**（参考 Hermes Agent）；Adapter in/out 分离；跨平台 session 续传；per-channel persona；Cron 调度；Voice/Media 通道；限流/降级/Fallback |
| v0.4 | 补充当前落地状态、剩余任务队列、GUI 可视化接入方案；明确应用设置新增 `IM 桥接` Tab，通过 `/api/v1/im/*` 接口配置不同 IM 工具连接 |

参考外部范式（Hermes Agent 风格）：
- 单一长连接 Gateway 进程管理所有平台
- Adapter 做 in/out 翻译，统一事件规范化
- 跨平台 session 续传（同一用户从 TG 切到飞书能继续）
- per-channel persona / 工具集 / 行为模式

### 0.1 当前落地状态（2026-07-26）

已完成的后端基础能力：

- `internal/im` 骨架、Gateway、Adapter / OutboundRenderer 抽象、renderer factory / registry。
- `internal/config.IMConfig` 与 `GET/PATCH /api/v1/im/config` 配置读写；密钥按用户决策允许直接存入 config。
- `GET /api/v1/im/health`、`POST /api/v1/im/test`、`POST /api/v1/im/:type/test`、`GET /api/v1/im/events` 管理与观测接口。
- 飞书 webhook 入站基础能力：URL verification、文本消息解析。
- 飞书 OpenAPI 文本出站 renderer：tenant token、send text、edit text。
- `internal/im/outbound.Dispatcher`：renderer dispatch、typing no-op / edit 限频、MarkdownDialect 暴露、超长文本按 UTF-8 byte 切分。
- 构建 tag 验证路径已覆盖 `im_qq` / `im_wx` 组合，当前 Go 测试通过。

仍需完成的任务：

1. **GUI IM 设置 Tab**：把已有 `/api/v1/im/*` 管理接口接入应用设置页，支持不同 IM 平台连接配置、测试、健康状态与事件流可视化。
2. **入站消息进入 Agent 主流程**：mention 解析、命令解析、session resolver、persona resolver、限流后调用 `agent.ChatWithTools()`，再通过 outbound dispatcher 回发。
3. **飞书能力补齐**：加密回调、富文本 post / card renderer、Markdown 转飞书方言、文件/图片附件处理。
4. **Session / Identity / Persona**：跨平台 principal 绑定、per-channel persona 注入、群聊 require mention 策略。
5. **限流 / fallback / audit / metrics**：三层 token bucket、平台降级链、JSONL 审计、健康指标。
6. **更多平台 adapter**：Telegram、企微、QQ、微信、钉钉 / Slack 等按风险与 build tag 分批落地。
7. **Cron / Media**：IM 定时任务、STT/TTS、vision、文件文本提取。

---

## 1. 设计原则

1. **零侵入核心**：不动 `agent.go` 的 ReAct 主循环；桥接层只在 `messages` 入口之前和 `parts` 流出口之后做转译。
2. **一个 IM 会话 = 一个 P-Chat session**：复用 `memory.Session` / `message` 持久化层，平台消息 ↔ session 严格 1:1（可叠加跨平台 principal 聚合，见 §6）。
3. **复用 HTTP server**：`pchat-server` 已是进程内嵌的 HTTP + SSE；IM Gateway 作为同进程内的新长连接组件注册进 `Server`，复用 SSE / 事件分发管线。
4. **三处配置入口同源**：`config.json` (canonical) ↔ GUI 设置 tab ↔ CLI `--im.*` flag 读取同一份 schema，配置层做去重。
5. **平台 SDK 差异封装在 `internal/im/adapter/`**：每个平台一个 adapter，外部表现统一（In/Out 文本 + 附件 + 富文本回执）。
6. **build tag 隔离** 高风险 / 沙箱期平台（个人微信、QQ 私域）：默认不编译进 release，用户主动开启。

---

## 2. 整体架构

```
┌──────────────────────────────────────────────────────────────────┐
│                      pchat-server 进程                            │
│                                                                  │
│   ┌────────────────────────────────────────────────────────┐    │
│   │                  IM Gateway（单例）                    │    │
│   │                                                        │    │
│   │  ┌────────────┐ ┌────────────┐ ┌────────────┐         │    │
│   │  │FeishuAdapter│ │TgAdapter   │ │QQAdapter   │         │    │
│   │  │(in+out)    │ │(in+out)    │ │(in+out)    │         │    │
│   │  └─────┬──────┘ └─────┬──────┘ └─────┬──────┘         │    │
│   │        │              │              │                  │    │
│   │        ▼              ▼              ▼                  │    │
│   │  ┌─────────────────────────────────────────────────┐   │    │
│   │  │  Normalization Bus (typed channels)             │   │    │
│   │  │  IMEvent → SessionResolver → AgentRunner        │   │    │
│   │  │  IMResponse / IMOutChunk ← OutboundDispatcher   │   │    │
│   │  └─────────────────────────────────────────────────┘   │    │
│   │                                                        │    │
│   │  ┌──────┐  ┌──────┐  ┌──────┐  ┌──────┐                │    │
│   │  │Cron  │  │Voice │  │Audit │  │Metrics│               │    │
│   │  └──────┘  └──────┘  └──────┘  └──────┘                │    │
│   └────────────────────────────────────────────────────────┘    │
│                                                                  │
│   ┌──────────────┐   ┌──────────────┐                            │
│   │ HTTP server  │   │  GUI (Wails) │  ← 已有                    │
│   │  /api/v1/... │   │              │                            │
│   └──────────────┘   └──────────────┘                            │
└──────────────────────────────────────────────────────────────────┘
```

新增顶层包 `internal/im/`，根包下只放公共抽象与 manager；具体平台放：

```
internal/im/
├── gateway.go            // 单例 Gateway
├── adapter.go            // Adapter / OutboundRenderer 接口
├── event.go              // IMEvent / IMOutChunk / ChatRef / SenderRef
├── session.go            // IM 元组 → session key 解析
├── persona.go            // per-channel persona 匹配
├── ratelimit.go          // 三级限流
├── fallback.go           // Fallback 链
├── media/                // STT / TTS / Vision / File
├── cron/                 // 调度
├── outbound/             // 通用 OutboundDispatcher + Markdown 方言
├── cmd/                  // IM 端斜杠命令
└── <platform>/           // feishu | wecom | telegram | qq | wechat | ...
```

---

## 3. 平台能力矩阵（v0.3 终态）

| 平台 | 通道 | mode 枚举 | 鉴权 | 群/单聊 | 话题/线程 | 卡片 | 流式编辑 | 风险 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| **飞书** | **Bot v3** | `bot` (WS / Webhook) | 机器人 secret + 验签 | ✅ | ✅（root_id 话题）| ✅ Interactive Card | ✅ `edit_message` | **官方，低** |
| 飞书 | OpenAPI 自建应用 | `openapi` (WS/Webhook) | AppID + AppSecret + tenant_access_token | ✅ | ✅ | ✅ | ✅ | **官方，低** |
| **企业微信** | 自建应用 / 智能机器人 | `webhook` | CorpID + CorpSecret + AES 回调 | ✅ | ⚠ | ✅ 模板卡片 | ⚠ | **官方，低** |
| **Telegram** | Bot API | `polling` / `webhook` | Bot Token | ✅ | ✅ 论坛模式 | ❌（InlineKeyboard）| ✅ `editMessage` | **官方，低** |
| **QQ 频道** | botgo (官方) | `websocket` / `webhook` | Bot Token（频道）| ✅ 子频道 | ✅ 帖子/楼中楼 | ✅ 富文本 + 模板 | ✅ | **官方，低** |
| **QQ 私域机器人** | botgo (官方) | `websocket` / `webhook` | AppID + AppSecret + 沙箱 token | ✅ @bot | ❌ | ⚠ Markdown / 卡片 | ⚠ | **官方，中**（沙箱期）|
| **微信（个人号）** | wechatbot.dev (第三方) | `wechatbot` | 扫码登录 + 设备锁 | ✅ 私聊/群 | ❌ | ❌ 文本/图片 | ❌ | **第三方，高** ⚠ 封号 |
| **钉钉** | 机器人 / 应用 | `bot` / `app` | AppKey + AppSecret | ✅ | ⚠ | ✅ 卡片 | ⚠ | **官方，低** |
| **Slack** | Events API + Web API | `events` | Bot Token + Signing Secret | ✅ | ✅ thread_ts | ✅ Block Kit | ✅ `chat.update` | **官方，低** |
| **Discord** | Gateway + REST | `gateway` | Bot Token | ✅ | ✅ thread | ✅ Embed | ✅ | **官方，低** |

> **合规提示**（必须写进 README）：个人微信、QQ 个人号均无官方开放接口。`wechatbot.dev` 等第三方协议存在封号风险，仅供个人学习/自用，禁止商用。

---

## 4. 关键数据结构

```go
// internal/im/event.go
type IMEvent struct {
    ID          string            // 平台侧消息 ID（用于 reply/edit）
    TraceID     string            // 关联日志
    Platform    string            // feishu | telegram | ...
    Variant     string            // bot | openapi | guild | ...
    Chat        ChatRef           // 平台/chat/thread
    Sender      SenderRef         // user_id / display_name
    Text        string            // 规范化后的纯文本
    Mentions    []Mention         // @bot / @user
    ReplyTo     *string           // 引用的原消息 ID
    Attachments []Attachment      // 图片/文件/音频/卡片
    Timestamp   time.Time
    Raw         json.RawMessage   // 平台原始事件（调试用）
}

type IMOutChunk struct {
    TraceID   string
    Platform  string
    Chat      ChatRef
    MsgID     string            // 平台侧消息 ID（首次为新建，后续为 edit）
    Kind      string            // text | edit | typing | reaction | card
    Text      string
    Parts     []MessagePart     // 用于富文本渲染
    Done      bool              // 是否终结（最后一次 edit）
    Error     string
    Metadata  map[string]string  // platform-specific
}
```

```go
// internal/im/adapter.go
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

---

## 5. Session 粒度

| scope 值 | 适用场景 | key 形式 |
| --- | --- | --- |
| `per_sender` | 1v1 私聊，简化用户认知 | `im:{platform}:u:{sender_id}` |
| `per_chat` | 群聊多人共享上下文 | `im:{platform}:g:{chat_id}` |
| `per_thread` (默认) | 飞书话题 / TG 论坛 / 群多话题 | `im:{platform}:g:{chat_id}:t:{thread_id}` |

`SessionKey` 拼装（`internal/im/session.go`）：

```go
func BuildKey(platform string, meta IMChatMeta, scope string) string {
    parts := []string{"im", platform}
    if scope == "per_thread" && meta.ThreadID != "" {
        parts = append(parts, "t:"+meta.ThreadID)
    }
    if meta.ChatType == "group" {
        parts = append(parts, "g:"+meta.ChatID)
    } else {
        parts = append(parts, "u:"+meta.SenderID)
    }
    return strings.Join(parts, ":")
}
```

### 5.1 平台 → 推荐 scope 映射

| 平台 | 关键字段 | 推荐 scope |
| --- | --- | --- |
| 飞书 Bot v3 | `chat_id` + `chat_type` + `root_id` (话题) | `per_thread` |
| 飞书 OpenAPI | 同上 | `per_thread` |
| 企微 | `ChatId` + `FromUserName` | `per_chat`（群）/ `per_sender`（单聊）|
| Telegram | `chat.id` + `message_thread_id` | `per_thread` |
| QQ 频道 | `guild_id` + `channel_id` + `author_id` | `per_chat` |
| QQ 私域 | `group_openid` + `author_openid` | `per_thread`（无 thread 时退化为 per_chat）|
| 微信 | `wxid` / `roomid` | `per_sender` / `per_chat` |

---

## 6. 跨平台 Session 续传（v0.3 新增）

### 6.1 三层 Session 概念

| 层 | key 形式 | 用途 |
| --- | --- | --- |
| **Channel Session** | `im:{platform}:{chat_id}:{thread_id?}` | 平台侧，保留 platform-specific 上下文 |
| **Principal Session** | `principal:{principal_id}` | 跨平台汇总，同一用户所有平台合并 |
| **Topic Session** | `principal:{principal_id}:topic:{topic_hash}` | 跨平台内按"主题"分桶 |

### 6.2 Identity Link 配置

```yaml
im:
  identity:
    links:
      - principal: "u_aping"
        accounts:
          - { platform: telegram, id: "123456" }
          - { platform: feishu,   id: "ou_xxx" }
          - { platform: wechat,   id: "wxid_xxx" }
    auto_link:
      enabled: false
      trust: "manual"   # manual | high | none
```

每条 IM 入站消息先解析到 Channel Session；若 sender 命中 `identity.links` → 挂到 Principal Session 下，注入 `principal_id` 到 `SessionMeta`。

---

## 7. Per-Channel Persona（v0.3 新增）

```yaml
im:
  personas:
    default:
      style: tech
      work_mode: chat
      tools_allow: [read_file, search, web_search]
      prompt_inject: |
        你是 P-Chat，回答时优先简洁（< 500 字），避免长代码块。
      model: gpt-4o-mini

    "feishu:group:*":
      style: guofeng
      work_mode: agent
      prompt_inject: |
        飞书群聊风格，轻松一些，可加 emoji。
        长答案主动用 markdown 标题分段。

    "feishu:p2p:*":
      style: cute
      prompt_inject: |
        私聊模式，可以更口语化。

    "telegram:*":
      style: tech
      prompt_inject: |
        Respond in English unless user writes Chinese.
        Use MarkdownV2. Keep under 800 chars unless asked for detail.

    "wechat:*":
      style: tech
      tools_allow: [read_file, search]
```

匹配顺序（`internal/im/persona.go`）：

```go
// exact > glob(platform:chatType:*) > glob(platform:*) > default
keys := []string{
    fmt.Sprintf("%s:%s:%s", platform, chatType, senderID),
    fmt.Sprintf("%s:%s:*", platform, chatType),
    fmt.Sprintf("%s:*", platform),
    "default",
}
```

Persona 注入位置：`internal/agent/agent.go` 的 `buildStaticSystemPrompt()` 之前新增 `buildPersonaBlock(persona)`，与现有 `buildWorkModeBlock()` 同样机制。

---

## 8. 斜杠命令 + Mention-grammar（避免群聊噪声）

### 8.1 命令列表

```
/help                列出命令
/style <name>        切风格
/mode <name>         切工作模式
/new                 新建 session
/compact             压缩上下文
/cancel              中断
/auto-continue on|off
/model <name>        切模型
/whoami              显示当前 session 元数据
/forward feishu oc_xxx     跨平台转发当前对话
/voice on|off        切换语音回答
/cron add "0 9 * * *" "每天 9 点发新能源日报"   v0.3 新增
/who                 列出当前 chat 里的用户
/silent on|off       仅在被 @ 时响应
/recall <query>      知识库检索
/export <path>       导出当前 session
```

### 8.2 Mention-aware 解析

`internal/im/cmd/parser.go`：

```
Input: "@PChat /style guofeng"        → 切风格
Input: "@PChat 帮我看下日志"           → 非命令，走 Agent
Input: "帮我看下日志"                  (群)→ 走 Agent（如果 require_mention=true 则忽略）
Input: "/style guofeng"               (私聊)→ 切风格
Input: "@user /style guofeng"         → 不是对 bot 说的，丢弃（除非 reply-to-msg 是 bot）
```

关键：**先 mention 解析 → 再 command 解析 → 最后进 Agent**。

### 8.3 Question / Inline Confirmation

复用现有 `QuestionModal` 协议（`type=question` SSE 事件）；发送端由 `IMGateway` 渲染为平台 button，回调用 `IMEvent.CallbackQuery` 回到 `Question` handler。

---

## 9. Cron / Schedule（v0.3 新增）

```yaml
im:
  cron:
    enabled: true
    jobs:
      - id: stock-daily
        schedule: "0 9 * * 1-5"          # 工作日 9 点
        timezone: "Asia/Shanghai"
        platform: "feishu"
        chat_id: "oc_xxx"
        prompt: "把昨天的新能源板块复盘发到这里"
        persona: "feishu:group:*"
```

`internal/im/cron/manager.go`：
- 基于 `github.com/robfig/cron/v3`
- 每次触发 → 构造"虚拟 IMEvent"（platform/chat/prompt）→ 走完正常 agent pipeline → 用 `OutboundRenderer` 发送
- 支持 `/cron add ...` 命令动态增删
- 失败重试（指数退避），最多 3 次

---

## 10. Voice / Media 通道（v0.3 新增）

| 输入 | 处理 | 输出 |
| --- | --- | --- |
| 语音消息（飞书/QQ/TG/微信）| STT（Whisper / 阿里云 ASR / 腾讯云 ASR）→ 文本 | 走 Agent |
| 图片 | 多模态 LLM 解析（GPT-4V / Qwen-VL / Doubao-VL）| 走 Agent |
| 文件 | 提取文本（PDF/DOCX/TXT/MD）→ 入 context | 走 Agent |
| 视频 | ffmpeg 抽关键帧 + ASR 字幕 | 走 Agent |
| 输出文本 | TTS（Edge-TTS / ElevenLabs）| 语音消息（平台支持时）|

`internal/im/media/` 子包，**不耦合任何 LLM SDK**——通过 `internal/llm` 现有客户端 + 可插拔 provider。

```yaml
im:
  media:
    stt:
      provider: "openai"     # openai | aliyun | tencent | local(faster-whisper)
      model: "whisper-1"
    tts:
      provider: "edge"       # edge | openai | elevenlabs
      voice: "zh-CN-XiaoxiaoNeural"
      enabled_in: ["telegram", "wechat"]
    vision:
      enabled: true
      max_image_bytes: 5242880
    file_extract:
      enabled: true
      max_file_bytes: 20971520
      types: ["pdf", "docx", "txt", "md"]
```

---

## 11. 策略化限流 / Fallback 链

### 11.1 三级限流

```yaml
im:
  rate_limit:
    - { scope: platform, key: feishu, rps: 20, burst: 40 }
    - { scope: chat,   rps: 2, burst: 5 }
    - { scope: sender, rps: 1, burst: 3 }
```

`internal/im/ratelimit/` 用 `golang.org/x/time/rate`，按 `scope` 维护 `map[scopeKey]*rate.Limiter`。

### 11.2 Fallback 链（Hermes 模式）

```yaml
im:
  fallback:
    - { from: { platform: wecom }, to: { platform: feishu }, trigger: platform_down }
    - { from: { platform: feishu }, to: { platform: telegram, chat_id: "123" }, trigger: platform_down }
```

Health check 周期 30s，连续 3 次失败 → 触发 fallback；恢复后自动切回。Fallback 触发时通知用户：

```
"⚠️ 当前飞书通道异常，结果已自动转 Telegram 私聊 (chat_id=123)，请到那里查看。"
```

---

## 12. 流式输出与 Markdown 平台方言

### 12.1 平台发消息长度限制

| 平台 | 单条上限 | 策略 |
| --- | --- | --- |
| 飞书 | 4KB（文本）/ 30KB（卡片）| 超长走多段 + 续发 |
| 企微 | 4KB | 同上 |
| TG | 4096 字符 | 同上 |
| QQ 私域 | 约 4KB | 同上 |
| QQ 频道 | 较大 | — |
| 微信 | 约 4KB | 同上 |

`internal/im/render/chunker.go`：按段落 / 换行 / 句子切分，每段封一条。

### 12.2 Markdown 方言转换（`internal/im/render/markdown.go`）

| 平台 | 方言 | 转换器 |
| --- | --- | --- |
| 飞书 | 飞书富文本（`<at>`、`<text>`、`<a>`、`<code>`）| `md → feishu post` |
| 企微 | Markdown 子集 | `md → wecom md` |
| TG | MarkdownV2 / HTML | `md → tg md v2`（特殊字符转义）|
| QQ 私域 | 纯文本 + 简单 `**` | `md → plain` |
| QQ 频道 | Markdown + 模板卡片 | `md → qq guild md` |
| 微信 | 纯文本 | `md → plain` |

### 12.3 流式"正在输入"

| 平台 | 是否支持 | 实现 |
| --- | --- | --- |
| 飞书 | ❌ | 周期发"💭 思考中…"小消息，finalize 时 delete+重发；或用可编辑 Interactive Card |
| 企微 | ❌ | 同上 |
| TG | ✅ | `sendChatAction(typing)`，启动时发一次，4s 后再发；最终 editMessage |
| QQ 私域 | ❌ | 直接一次性发 |
| QQ 频道 | ❌ | 同上 |
| 微信 | ❌ | 同上 |

`Renderer` 接口新增 `Typing(ctx, chat)`，无能力平台 no-op。

---

## 13. 终态配置文件 schema（v0.3）

```yaml
im:
  enabled: true

  # ── Session 策略 ──
  session:
    scope: per_thread         # per_sender | per_chat | per_thread
    record_sender: true
    cross_platform: true      # 跨平台 principal 聚合

  # ── Identity Link（跨平台 session 续传）──
  identity:
    links:
      - principal: "u_aping"
        accounts:
          - { platform: telegram, id: "123456" }
          - { platform: feishu,   id: "ou_xxx" }
          - { platform: wechat,   id: "wxid_xxx" }
    auto_link: { enabled: false, trust: manual }

  # ── 斜杠命令策略 ──
  command:
    prefix: "/"
    forward_unknown_to_agent: true
    admin_senders: ["ou_admin", "tg:123456"]
    require_mention_in_group: true

  # ── 限流 ──
  rate_limit:
    - { scope: platform, key: feishu, rps: 20, burst: 40 }
    - { scope: chat,   rps: 2, burst: 5 }
    - { scope: sender, rps: 1, burst: 3 }

  # ── 审计 ──
  audit_log: true
  audit_local_only: true

  # ── 全局工具白名单（per persona 可覆盖）──
  tools_allowlist_default: [read_file, search, web_search, knowledge_search]

  # ── Per-channel Persona ──
  personas:
    default:
      style: tech
      work_mode: chat
      model: gpt-4o-mini
    "feishu:group:*":
      style: guofeng
      work_mode: agent
      prompt_inject: |
        飞书群聊风格，轻松一些，可加 emoji。
    "feishu:p2p:*":
      style: cute
    "telegram:*":
      style: tech
      prompt_inject: |
        Default to English. MarkdownV2. <800 chars.
    "wechat:*":
      style: tech
      tools_allow: [read_file, search]

  # ── 调度 ──
  cron:
    enabled: true
    jobs:
      - id: morning-brief
        schedule: "0 9 * * 1-5"
        timezone: Asia/Shanghai
        platform: feishu
        chat_id: oc_xxx
        prompt: "复盘昨晚美股 + 今天 A 股热点"

  # ── Fallback 链 ──
  fallback:
    - { from: { platform: wecom }, to: { platform: feishu }, trigger: platform_down }

  # ── 媒体 ──
  media:
    stt:  { provider: openai, model: whisper-1 }
    tts:  { provider: edge,   voice: zh-CN-XiaoxiaoNeural, enabled_in: [telegram, wechat] }
    vision: { enabled: true, max_image_bytes: 5242880 }
    file_extract: { enabled: true, max_file_bytes: 20971520, types: [pdf, docx, txt, md] }

  # ── 平台列表 ──
  platforms:
    # 飞书 Bot v3（推荐）
    - type: feishu
      variant: bot
      enabled: true
      mode: websocket
      app_id: "cli_xxx"
      app_secret: "${PCHAT_FEISHU_SECRET}"
      verification_token: ""
      encrypt_key: ""
      allowed_senders: ["*"]
      out: { use_openapi: true, api_base: "https://open.feishu.cn" }

    # 飞书 OpenAPI 自建应用（高级）
    - type: feishu
      variant: openapi
      enabled: false

    # Telegram
    - type: telegram
      enabled: true
      token: "${PCHAT_TG_TOKEN}"
      mode: polling
      webhook: { listen: ":9002", path: "/im/telegram" }
      allowed_updates: [message, edited_message, callback_query]

    # 企业微信
    - type: wecom
      enabled: true
      variant: app                  # app | bot
      corp_id: "..."
      corp_secret: "${PCHAT_WECOM_SECRET}"
      agent_id: 1000002
      callback_aes_key: "..."
      callback_token: "..."
      mode: webhook

    # QQ 频道（默认 -tags im_qq 编译）
    - type: qq
      enabled: false
      variant: guild                # guild | private
      app_id: "..."
      app_secret: "${PCHAT_QQ_SECRET}"
      sandbox: true
      mode: websocket

    # 微信（默认 -tags im_wx 编译，第三方高风险）
    - type: wechat
      enabled: false
      variant: wechatbot
      endpoint: "http://127.0.0.1:9000"
      api_key: "${PCHAT_WX_API_KEY}"
      mode: websocket
```

Go struct：

```go
type IMConfig struct {
    Enabled   bool                    `json:"enabled"`
    Session   IMSessionPolicy         `json:"session"`
    Identity  IMIdentityPolicy        `json:"identity"`
    Command   IMCommandPolicy         `json:"command"`
    RateLimit []IMRateLimitRule       `json:"rate_limit"`
    AuditLog  bool                    `json:"audit_log"`
    AuditLocalOnly bool               `json:"audit_local_only"`
    ToolsAllowlistDefault []string    `json:"tools_allowlist_default"`
    Personas  map[string]IMPersona    `json:"personas"`
    Cron      IMCronConfig            `json:"cron"`
    Fallback  []IMFallbackRule        `json:"fallback"`
    Media     IMMediaConfig           `json:"media"`
    Platforms []IMPlatformConfig      `json:"platforms"`
}

type IMPlatformConfig struct {
    Type     string         `json:"type"`      // feishu | wecom | telegram | qq | wechat | ...
    Variant  string         `json:"variant,omitempty"`
    Enabled  bool           `json:"enabled"`
    Mode     string         `json:"mode,omitempty"`
    Token    string         `json:"token,omitempty"`
    AppID    string         `json:"app_id,omitempty"`
    AppSecret string        `json:"app_secret,omitempty"`
    Webhook  IMWebhookConfig `json:"webhook,omitempty"`
    Out      IMOutboundConfig `json:"out,omitempty"`
    AllowedSenders []string `json:"allowed_senders,omitempty"`
    Extra    map[string]any `json:"extra,omitempty"`
}
```

---

## 14. GUI 设置 Tab

复用 `AppSettingsLayout` 的 2 列 nav，新增一项：

```ts
{
  name: 'im',
  label: 'IM 桥接',
  icon: MessageSquare,                    // lucide
  description: '飞书 / 企微 / TG / QQ / 微信'
}
```

现有前端结构：

- `frontend/src/components/AppSettingsLayout.vue` 已提供左侧 nav + 右侧内容的 2 列设置布局。
- `frontend/src/components/AppSettingsModal.vue` 用 `settingsTabs` 注册 tab，并用 `NTabPane` 承载每个设置页。
- `frontend/src/api/client.ts` 是统一 HTTP client，需要补 IM 类型和方法。

现有后端接口：

| 用途 | 接口 |
| --- | --- |
| 读取 IM 配置 | `GET /api/v1/im/config` |
| 部分更新 IM 配置 | `PATCH /api/v1/im/config` |
| 健康状态 | `GET /api/v1/im/health` |
| 测试连接 | `POST /api/v1/im/test` / `POST /api/v1/im/:type/test` |
| 生命周期事件流 | `GET /api/v1/im/events` |
| 飞书 webhook | `POST /api/v1/im/feishu/webhook` |

### 14.1 页面信息架构

IM Tab 不做营销式说明页，直接做可操作设置页。页面采用"顶部状态条 + 平台连接列表 + 高级策略折叠区"：

1. **状态条**：总开关、Gateway 运行状态、已启用平台数、最近错误、刷新按钮、保存按钮。
2. **全局策略**：`session.scope`、`session.record_sender`、`session.cross_platform`、`command.prefix`、`command.require_mention_in_group`、`command.forward_unknown_to_agent`、`audit_log`、`audit_local_only`。
3. **平台连接**：每个平台一行/折叠面板，默认显示启用状态、连接状态、variant、mode、最近错误；展开后编辑鉴权、webhook、outbound、白名单。
4. **Persona**：按 `personas` map 展示 `default`、`feishu:group:*`、`telegram:*` 等规则；支持新增/删除/编辑 style、work_mode、model、tools_allow、prompt_inject。
5. **身份链接 / 调度 / Fallback**：identity links、cron jobs、fallback rules 作为后续增强区，MVP 可只读或先折叠占位。
6. **事件流**：订阅 `/api/v1/im/events`，展示最近 20 条 Gateway lifecycle，用于调试重连、鉴权失败、平台上下线。

### 14.2 平台连接字段

通用字段：

| 字段 | UI 控件 | 说明 |
| --- | --- | --- |
| `enabled` | Switch | 是否启用该平台 |
| `type` | Select | `feishu` / `telegram` / `wecom` / `qq` / `wechat` / `dingtalk` / `slack` |
| `variant` | Select | 平台子类型，例如 `bot` / `openapi` / `guild` / `private` / `wechatbot` |
| `mode` | Segmented / Select | `webhook` / `websocket` / `polling` |
| `allowed_senders` | Tag input | `*` 或平台用户 ID 列表 |
| `extra` | JSON textarea | 平台临时扩展字段，避免 schema 未覆盖时阻塞接入 |

平台字段：

| 平台 | 必填 / 常用字段 |
| --- | --- |
| 飞书 | `app_id`、`app_secret`、`verification_token`、`encrypt_key`、`out.use_openapi`、`out.api_base` |
| Telegram | `token`、`mode`、`webhook.listen`、`webhook.path` |
| 企业微信 | `corp_id`、`corp_secret`、`agent_id`、`callback_aes_key`、`callback_token` |
| QQ | `app_id`、`app_secret`、`variant`、`mode`、`extra.sandbox` |
| 微信 | `endpoint`、`api_key`、`variant=wechatbot`、`mode=websocket` |

密钥字段在 UI 中使用 password input，默认不主动清空；用户输入新值后直接写入 `config`。

### 14.3 前端 API client

在 `frontend/src/api/client.ts` 增加类型与方法：

```ts
export interface IMConfig {
  enabled: boolean
  session: IMSessionPolicy
  identity: IMIdentityPolicy
  command: IMCommandPolicy
  rate_limit?: IMRateLimitRule[]
  audit_log: boolean
  audit_local_only: boolean
  tools_allowlist_default?: string[]
  personas?: Record<string, IMPersona>
  cron: IMCronConfig
  fallback?: IMFallbackRule[]
  media: IMMediaConfig
  platforms?: IMPlatformConfig[]
}

export const getIMConfig = () =>
  jsonFetch<IMConfig>('/api/v1/im/config')

export const updateIMConfig = (patch: Partial<IMConfig>) =>
  jsonFetch<IMConfig>('/api/v1/im/config', {
    method: 'PATCH',
    body: JSON.stringify(patch),
  })

export const getIMHealth = () =>
  jsonFetch<IMHealth>('/api/v1/im/health')

export const testIMConnection = (type: string, variant?: string) =>
  jsonFetch<IMTestResult>(`/api/v1/im/${encodeURIComponent(type)}/test`, {
    method: 'POST',
    body: JSON.stringify({ type, variant }),
  })
```

`/api/v1/im/events` 是 SSE，复用现有 `consumeSSEStream()`，但不要混入聊天消息流；IM 设置页只维护本页局部状态。

### 14.4 MVP 验收

- 应用设置左侧出现 `IM 桥接` tab，切入时加载 `/api/v1/im/config` 与 `/api/v1/im/health`。
- 可开关全局 `im.enabled`，可编辑并保存 `session`、`command`、`audit`。
- 可新增/删除/编辑 `platforms[]`，至少覆盖飞书字段；其他平台以通用字段 + `extra` 兜底。
- 每个平台有"测试连接"按钮，调用 `/api/v1/im/:type/test` 并在当前行显示结果。
- 已连接 / 未连接 / 鉴权失败等状态来自 `/api/v1/im/health`，手动刷新可更新。
- 事件流面板可显示 Gateway 最近事件；接口不可用时不阻塞保存配置。
- `npx vue-tsc -b` 与 `npm run build` 通过。

### 14.5 后续增强

- 把 IM 设置页从 `AppSettingsModal.vue` 拆为 `IMSettings.vue`，避免设置弹窗继续膨胀。
- 支持平台模板："添加飞书"、"添加 Telegram"、"添加企微"，自动填默认 `variant/mode/out.api_base`。
- 支持密钥状态：后端返回 `has_app_secret/has_token` 等掩码字段，避免 list/detail 往返暴露完整密钥。
- 支持 identity links、persona、cron、fallback 的可视化编辑器。
- 支持 `/api/v1/im/events` 自动重连与事件过滤。

---

## 15. CLI 增量

```
pchat-server im start   [--platforms=feishu,telegram]
pchat-server im stop    [--platform=feishu]
pchat-server im status  [--json]
pchat-server im test    <platform>
pchat-server im cron list
pchat-server im cron add "0 9 * * *" "复盘" --platform=feishu --chat=oc_xxx
pchat-server im persona list
pchat-server im link add <principal> --platform=telegram --id=123456

pchat-server \
  --im.enable \
  --im.platform=feishu:bot,telegram,qq:guild,wechat:wechatbot \
  --im.mode=daemon
```

---

## 16. SDK / 依赖选型

| 平台 | 选型 | 依赖体积 | 备选 |
| --- | --- | --- | --- |
| 飞书 Bot v3 | **自研 WS client**（不引 lark-sdk）| 只引 `gorilla/websocket` | 官方 `oapi-sdk-go`（仅 openapi variant 启用）|
| 飞书 OpenAPI | `oapi-sdk-go`（`mode: openapi` 启用）| build tag 控制 | — |
| 企业微信 | **自研**（HTTP + AES-256-CBC + XML 回调）| `github.com/forgoer/openssl` 或 stdlib | — |
| Telegram | `go-telegram-bot-api` 或**自研**（~300 行）| 0~10MB | — |
| QQ 频道 + 私域 | `github.com/tencent-connect/botgo` | 官方维护 | — |
| 微信 | `wechatbot.dev/go`（第三方）| 外部进程 + HTTP/WS | — |
| 通用 | `github.com/robfig/cron/v3` | cron 调度 | — |

**默认构建**（`//go:build !im_qq && !im_wx`）：

```bash
go build ./cmd/pchat-server                          # 内置：飞书 / 企微 / TG
go build -tags im_qq ./cmd/pchat-server              # + QQ
go build -tags "im_qq im_wx" ./cmd/pchat-server      # + 微信
```

---

## 17. 安全 / 合规

| 项 | 实现 |
| --- | --- |
| **发送方白名单** | 平台级 `allowed_senders`，全局 `im.identity.links` 信任链 |
| **Admin 提权** | `command.admin_senders` 列表内用户可调 `/model /cancel /cron` |
| **密钥不落盘** | `${ENV}` 引用 + 启动时校验必需环境变量 |
| **工具白名单 per persona** | Persona → tools_allow；群聊默认收紧到只读 + 搜索 |
| **审计** | `~/.p-chat/im-audit.jsonl`，含 trace_id / platform / sender / text hash / tool calls / tokens |
| **数据驻留** | `audit.local_only: true`（默认）→ 不上传第三方 |
| **微信封号风险** | build tag 隔离 + README 红色提示 + 启动时弹"风险确认" |
| **可观测** | `GET /api/v1/im/health`、`GET /api/v1/im/metrics` |

---

## 18. 关键模块清单（落地时需新增/改）

| 模块 | 新建 / 改 | 说明 |
| --- | --- | --- |
| `internal/im/gateway.go` | 新建 | Gateway 主循环、Bus、生命周期 |
| `internal/im/adapter.go` | 新建 | `Adapter` / `OutboundRenderer` 接口 |
| `internal/im/event.go` | 新建 | `IMEvent` / `IMOutChunk` / `ChatRef` / `SenderRef` |
| `internal/im/session.go` | 新建 | IM 元组 → session key 解析 + principal 聚合 |
| `internal/im/persona.go` | 新建 | per-channel persona 匹配 |
| `internal/im/ratelimit.go` | 新建 | 三级限流 |
| `internal/im/fallback.go` | 新建 | Fallback 链 |
| `internal/im/tokencache.go` | 新建 | 平台 token 续期缓存 |
| `internal/im/media/` | 新建 | STT / TTS / Vision / File |
| `internal/im/cron/` | 新建 | 调度 |
| `internal/im/outbound/` | 新建 | OutboundDispatcher + Markdown 方言 |
| `internal/im/cmd/` | 新建 | IM 端命令注册 + 解析 |
| `internal/im/<platform>/` | 新建 | 5 个平台 adapter |
| `internal/im/audit/` | 新建 | JSONL 审计日志 |
| `internal/im/metrics/` | 新建 | 健康 + 计数器 |
| `internal/config/im_config.go` | 新建 | `IMConfig` 定义 |
| `internal/config/config.go` | 改 | `Config.IM` 字段 |
| `internal/config/manager.go` | 改 | 增 IM 读写 + 部分更新 API |
| `internal/server/server.go` | 改 | 注入 `im.Gateway`、注册 `/api/v1/im/*` 路由、暴露 `StreamBroker` |
| `internal/server/handler.go` | 改 | `GET /api/v1/config` PATCH 走部分更新 |
| `internal/cli/commands.go` | 改 | 把命令注册抽到 `internal/cmdregistry`（共享）|
| `internal/cmdregistry/*` | 新建 | GUI/CLI/IM 三端共享命令注册表 |
| `internal/agent/agent.go` | 改 | 增 `buildPersonaBlock()`，与 `buildWorkModeBlock()` 同级 |
| `cmd/pchat-server/main.go` | 改 | 加 `--im.*` flag + 启动 Gateway |
| `frontend/src/components/AppSettingsLayout.vue` | 复用 | 左侧 nav + 右侧内容的设置页框架 |
| `frontend/src/components/AppSettingsModal.vue` | 改 | 注册新 tab `im`，挂载 IM 设置内容 |
| `frontend/src/components/IMSettings.vue` | 新建 | IM 可视化配置页：状态条、平台连接、Persona、事件流 |
| `frontend/src/api/client.ts` | 改 | 新增 `getIMConfig / updateIMConfig / testIMConnection` |
| `configs/config.yaml` | 改 | 模板加 `im:` 注释示例 |
| `Taskfile.yml` | 改 | 加 `build:im_qq` / `build:im_wx` 任务 |
| `.agents/AGENTS.md` | 改 | §0/§5 增 im 模块条目 |
| `.agents/docs/im.md` | 新建 | 模块级入口文档 |
| `.agents/docs/INDEX.md` | 改 | 加 im 条目 |

---

## 19. 实施分期

| 阶段 | 内容 | 估时 |
| --- | --- | --- |
| **P-IM-1 骨架** | `internal/im` + Gateway + `IMConfig` schema + CLI flag + build tag 机制 + Taskfile 加 `build:im_qq/im_wx` | 2~3 天 |
| **P-IM-1.5 GUI 设置 MVP** | 应用设置新增 `IM 桥接` tab；接入 `/api/v1/im/config`、`/api/v1/im/health`、`/api/v1/im/:type/test`；支持平台连接配置和飞书字段 | 1~2 天 |
| **P-IM-2 飞书 Bot v3** | 自研 WS + 收消息 + OpenAPI 发消息 + Markdown 方言 | 3 天 |
| **P-IM-3 飞书 OpenAPI** | 自建应用通道，企业场景 | 2 天 |
| **P-IM-4 Telegram** | polling + webhook + 流式 edit + MarkdownV2 | 1~2 天 |
| **P-IM-5 企微** | 自研 HTTP + AES + 模板卡片 | 3 天 |
| **P-IM-6 QQ (频道 + 私域)** | botgo 接入，沙箱支持 | 2~3 天 |
| **P-IM-7 微信 wechatbot** | 第三方 SDK 接入，build tag 隔离 | 1~2 天 |
| **P-IM-8 Session Resolver + Identity Link** | 跨平台 session 续传 | 2 天 |
| **P-IM-9 Persona + Mention-grammar** | per-channel persona + 群聊 mention 解析 | 2 天 |
| **P-IM-10 Cron + Voice + Media** | 调度 + STT/TTS + 视觉 + 文件 | 3~4 天 |
| **P-IM-11 OutboundDispatcher + 限流 + Fallback** | 流式聚合 + 降级链 | 2 天 |
| **P-IM-12 GUI + CLI polish** | 4 子区设置页 + cron 列表 + 跨平台 session 视图 | 3 天 |

合计 ≈ **27~33 人天**（一两个人全职 6~7 周）。

---

## 20. 待确认决策

1. **跨平台 session 续传**（§6）是否进 v1？（影响 P-IM-8 工作量）
2. **Cron**（§9）是否进 v1？（影响 P-IM-10 工作量，以及"agent 主动做事"的能力边界）
3. **Voice / Media**（§10）是否进 v1？（影响 P-IM-10 + 新增 LLM provider 依赖）
4. **openclaw** 你具体指哪个项目？（搜索未命中。可能是内部项目或非英文命名——若有公开链接，告诉我可再调研其 adapter 协议细节）
5. **Hermes SSE 模式 vs P-Chat 现有 SSE 模式**：v0.3 让 IM adapter 通过同一条 SSE 通道作为"前端"，让 GUI 实时看到 IM 用户的对话（无需额外 channel），是否纳入 v1？

确认后从 P-IM-1 骨架起步。

---

## 附录 A：参考资料

- [Hermes Agent — Messaging Gateway 用户指南](https://hermes-agent.nousresearch.com/docs/user-guide/messaging/)
- [Hermes Agent — Gateway Internals（开发者指南）](https://hermes-agent.nousresearch.com/docs/developer-guide/gateway-internals)
- [Hermes Agent — Integrations 总览](https://hermes-agent.nousresearch.com/docs/integrations/)
- [Hermes Agent — Programmatic Integration（GitHub）](https://github.com/nousresearch/hermes-agent/blob/main/website/docs/developer-guide/programmatic-integration.md)
- [Hermes Agent — Gateway Architecture Deep Dive](https://hermes-agent-lab.com/docs/deep-dives/gateway-architecture)
- [Hermes Agent — Multi-agent & per-channel persona（GitHub Issue）](https://github.com/NousResearch/hermes-agent/issues/11922)
- [飞书 Bot v3 概述](https://open.feishu.cn/document/client-docs/bot-v3/bot-overview?lang=zh-CN)
- [QQ 机器人 Wiki](https://bot.q.qq.com/wiki/)
- [WeChatBot Go SDK](https://www.wechatbot.dev/zh/golang)
