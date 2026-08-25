# IM 连接优化报告（2026-08-25）

## 结论

本次优先修复并重构了当前 IM 桥接中已经落地链路里的确定问题：群聊噪声触发 Agent、IM session 串话、远程工具调用缺少请求级白名单约束、IM 策略散落在 server handler 中导致入口层不独立，以及微信扫码后“显示已连接但消息收不到 / 不回复”的假连接与协议兼容问题。

参考 Hermes / OpenClaw 后，本次调整采用的原则是：IM 是与 CLI / GUI 并列的入口层，平台 adapter 只负责连接和事件规范化，IM Gateway/路由层负责鉴权、session、persona、工具策略，然后通过清晰接口调用现有 Agent，而不是侵入现有 GUI/HTTP 聊天流程。

参考点：

- Hermes Messaging Gateway：单后台 Gateway 管理多平台连接、session、cron、语音与出站分发。
- Hermes Gateway Internals：平台 adapter 接收消息、按 chat session 路由，再交给 Agent 处理。
- OpenClaw：Gateway 是 sessions/channels/tools/events 的单一 control plane，CLI/WebChat/移动节点都作为入口连接它。
- OpenClaw Workspace：Web UI 是薄入口，通过 bridge 连接 Gateway，核心 agent/memory/tools 保持在 Gateway 内。

当前代码状态仍不是“飞书 / 微信 / QQ 全量 websocket 已完成”：

- 飞书：已支持 webhook 入站、OpenAPI 文本出站；websocket adapter 仍未实现。
- 微信：当前是 iLink 兼容协议的长轮询 + 文本回发，不是 websocket。
- QQ：当前只有 `im_qq` build tag 标记，没有 QQ adapter 实现。

## 已修复问题

### 1. 远程工具调用没有执行级 allowlist

问题：`im.tools_allowlist_default` 和 persona 的 `tools_allow` 已存在于配置结构，但入站 IM 消息进入 `agent.ChatStream()` 时没有传给 Agent。即使工具没有出现在 LLM 可见工具列表里，模型仍可能通过手写 `tool_call` 触发已注册工具。

修复：

- `agent.ChatRequest` 新增 `AllowedTools`。
- Agent 构造 LLM 请求前按 `AllowedTools` 收窄工具定义。
- Agent 执行工具前再次检查工具是否在本次请求可用列表内，防止 markdown/tool_call 绕过。

覆盖测试：

- `internal/agent/tool_allowlist_test.go`

### 2. 群聊 require_mention 未生效

问题：`im.command.require_mention_in_group` 已有配置，但 `ProcessIMEvent` 没有检查群聊是否 @ bot，导致群内任意消息都可能触发 Agent。

修复：

- `ProcessIMEvent` 前置 `shouldProcessIMEvent()`。
- 群聊类型 `group/supergroup/channel/guild` 在 require mention 开启时，必须包含 `Mention.Bot=true` 才处理。

覆盖测试：

- `internal/im/routing_test.go`

### 3. IM session 粒度未按配置生效

问题：旧逻辑固定使用 `im:{platform}:{chat_id}`，没有使用 `im.session.scope`。飞书话题、群聊、多用户私聊容易共享上下文。

修复：

- 新增 `im.BuildSessionKey()`。
- 支持 `per_thread`、`per_chat`、`per_sender`。
- 私聊在 `per_thread/per_chat` 下自动退化为 sender session。

覆盖测试：

- `internal/im/session_test.go`

### 4. sender 白名单未进入入站处理

问题：平台配置里的 `allowed_senders` 没有在 Agent 前执行，远程平台连接后任何 sender 都可能进入 Agent。

修复：

- `shouldProcessIMEvent()` 增加平台配置匹配和 sender 白名单校验。
- 支持 `*`、裸 sender id、`platform:sender_id` 三种写法。

覆盖测试：

- `internal/im/routing_test.go`

### 5. persona 配置未作用于 IM 请求

问题：`im.personas` 已有配置，但 IM 入站没有应用 style、work_mode、model、tools_allow、prompt_inject。

修复：

- 新增 `resolveIMPersona()`，匹配顺序：
  - `platform:chatType:senderID`
  - `platform:chatType:*`
  - `platform:*`
  - `default`
- persona 可覆盖 `style`、`work_mode`、`model`。
- `tools_allow` 传入 Agent `AllowedTools`；为空时使用 `tools_allowlist_default`。
- `prompt_inject` 注入到本次请求的 `SkillContext`。

覆盖测试：

- `internal/im/routing_test.go`

### 6. IM 策略散落在 server handler

问题：上一版修复先把 mention、sender allowlist、persona、session scope 逻辑放进 `internal/server/im_bridge.go`，能修功能，但不符合“IM = 独立入口层”的架构要求。server 开始知道太多 IM 协议策略，后续接飞书 / QQ / 微信 websocket 时会继续膨胀。

修复：

- 新增 `internal/im/routing.go`。
- 暴露单一深接口 `im.PlanInbound(cfg, ev)`。
- `server.ProcessIMEvent()` 只消费 `InboundPlan`，负责把 IM 请求转成 Agent 请求并回发结果。
- mention、sender allowlist、platform match、session key、persona、tools allowlist 均归入 IM 层。

覆盖测试：

- `internal/im/routing_test.go`

### 7. 微信扫码后显示已连接但无收发

问题：微信 QR confirmed 只能说明登录确认或凭证返回成功，不能证明消息 adapter 已注册、已启动并正在轮询。旧 Gateway 还会在“微信 token 存在但 adapter 未注册”时返回 `authenticated`，前端把它按成功态展示，容易出现“已连接但没有任何入站事件”的假阳性。

同时，微信 iLink/OpenClaw 风格接口需要统一公共 header；旧代码只给 QR poll 补了部分 header，长轮询 `getupdates` 和 `sendmessage` 没有带 `iLink-App-Id` / `iLink-App-ClientVersion`。`getupdates` 解析也只覆盖 `msgs/messages`，对 `msg_list/message_list/updates/list` 这类包装不够宽。

修复：

- Gateway 不再把“有 token 但 adapter 未注册”视为连接成功，健康状态返回 `unavailable`，连接自检返回 `not_implemented`。
- 微信 QR、`notifystart`、`getupdates`、`sendmessage` 请求统一带 iLink 公共 header。
- 微信 `getupdates` 支持更多消息列表别名：`msg_list`、`message_list`、`updates`、`list`。
- 扫码保存凭证后把微信平台模式规整为当前实际支持的 `polling`，避免 GUI 默认 `websocket` 与后端实现不一致。
- 前端把 QR confirmed 文案改为“登录已确认”，真正成功提示以 health 进入 `polling/ok` 为准。

覆盖测试：

- `internal/im/gateway_test.go`
- `internal/im/wechat_test.go`
- `internal/im/wechat_qr_test.go`
- `internal/server/im_handler_test.go`

## 验证

已通过：

```powershell
go test -count=1 ./internal/im ./internal/im/feishu ./internal/im/outbound ./internal/agent ./internal/server ./cmd/pchat-server
go test -count=1 ./...
```

最终复测：

```powershell
go test -count=1 ./internal/im ./internal/server ./internal/config
go test -count=1 ./...
cd frontend
npm run build
```

结果：

- `internal/im` 通过
- `internal/server` 通过
- `internal/config` 通过
- 全量 Go 测试通过
- 前端 `vue-tsc -b && vite build` 通过

## 剩余优化点

### P0：补统一 websocket 控制面

OpenClaw 的做法是把 Gateway WS 当作单一控制面，CLI / UI / 节点都通过 JSON-over-WebSocket 连接 Gateway。P-Chat 目前只有 IM adapter 连接外部平台，还没有给“IM 作为独立客户端入口”抽出统一 WS/RPC 协议。建议新增：

- `internal/im/transport`：Gateway JSON frame 定义。
- `POST/WS /api/v1/im/gateway` 或独立 loopback WS：供 QQ / 微信 sidecar、未来移动端节点连接。
- 连接级鉴权、设备 ID、心跳、重连游标。
- frame 类型至少包含 `connect`、`message.in`、`agent.event`、`message.out`、`ack`、`error`。

### P0：不要把未实现 websocket 平台显示为可用

当前“websocket”目标和实现状态不一致。建议在配置测试和健康状态中明确：

- 飞书 `mode=websocket`：返回 `not_implemented`，不要显示 ready。
- 微信 `mode=websocket`：当前 adapter 只支持 iLink 长轮询，应返回 `unsupported_mode` 或在保存时规整为 `polling`。
- QQ：未编译 / 未实现 adapter 时返回 `not_implemented`，并提示需要 `im_qq` build tag + adapter。

### P1：实现真正 websocket adapter

按优先级建议：

1. 飞书 Bot v3 websocket：官方低风险，最适合作为第一个 websocket 竖切。
2. QQ 频道 websocket：需要 `im_qq` build tag 隔离。
3. 微信 websocket：第三方协议风险高，建议继续 build tag 隔离，并在 GUI 中显式风险提示。

### P1：Outbound 流式聚合

当前 IM 入站处理只在 Agent done 后发送最终文本。若要远程体验接近 GUI，需要做：

- content chunk 聚合。
- 平台支持 edit 时节流编辑。
- 不支持 edit 的平台只发 final。
- tool / sub-agent 事件渲染为简短状态文本或卡片。

### P1：审计与限流

当前修复了 allowlist 和 sender gate，但还缺：

- platform/chat/sender 三级限流。
- trace_id + sender + tool call 审计 JSONL。
- 远程工具调用的默认 permission 降级策略。

### P2：GUI 配置状态要区分“配置完成”和“连接运行”

健康状态建议拆分：

- `configured`
- `authenticated`
- `running`
- `unsupported_mode`
- `adapter_unavailable`
- `error`

避免用户看到 token 已保存就误以为 websocket 已连接。
