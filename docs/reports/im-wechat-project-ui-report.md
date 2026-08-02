# IM 微信接入与项目 UI 优化梳理报告

日期：2026-07-29

## 1. 背景

本报告合并两部分问题梳理：

1. 当前 IM / 微信接入中，微信扫码后界面显示已连接，但实际收不到微信消息，也无法确认连接信息稳定持久化。
2. 项目目录选择 UI 中，下拉项只展示项目名称、不展示路径；中文路径保存后可能乱码；新增项目缺少可见的必填校验。

本报告只做现状梳理和后续优化建议，不包含代码修改。

## 2. 当前 IM / 微信链路

### 2.1 已落地模块

当前仓库已经具备以下 IM 基础能力：

- `internal/im` Gateway / Adapter / OutboundRenderer 抽象。
- `internal/config.IMConfig` 配置结构与 `/api/v1/im/config` 读写。
- `/api/v1/im/health`、`/api/v1/im/test`、`/api/v1/im/events` 管理与观测接口。
- 微信 QR 登录接口：
  - `POST /api/v1/im/wechat/qr`
  - `GET /api/v1/im/wechat/qr/:id`
- 微信长轮询 adapter：`internal/im/wechat.go`。
- IM 入站消息进入 Agent 的处理入口：`internal/server/im_bridge.go`。

### 2.2 扫码登录链路

1. 前端点击微信“扫码”，调用 `connectPlatform('wechat')`。
2. 前端先保存 IM 配置，然后调用 `api.startWeChatQR()`。
3. 后端 `StartWeChatQR` 请求 iLink / OpenClaw 风格接口获取二维码。
4. 前端定时调用 `pollWeChatQR(id)`。
5. 如果轮询返回 `status=confirmed`，前端提示“微信已连接”。
6. 后端只有在 `session.Status == "confirmed" && cred.Token != ""` 时，才会持久化微信凭证。
7. 凭证持久化后，后端重新注册微信 adapter 并重启 Gateway。
8. 微信 adapter 启动后调用 `getupdates` 长轮询，解析消息为 `IMEvent`。
9. Gateway 收到 `IMEvent` 后调用 `Handler.ProcessIMEvent`。
10. `ProcessIMEvent` 调用 Agent，最后通过 IM outbound renderer 回发微信。

### 2.3 关键文件

| 领域 | 文件 |
| --- | --- |
| 微信 QR 登录 | `internal/im/wechat_qr.go` |
| 微信 adapter / 长轮询 / 发送 | `internal/im/wechat.go` |
| Gateway 生命周期 | `internal/im/gateway.go` |
| IM REST API | `internal/server/im_handler.go` |
| IM 入站进入 Agent | `internal/server/im_bridge.go` |
| IM 配置 | `internal/config/im_config.go` |
| 前端 IM 设置 | `frontend/src/components/IMSettings.vue` |

## 3. 微信问题判断

### 3.1 核心结论

当前最大问题是：系统把“扫码确认成功”和“消息通道真实可用”混成了一个用户可见状态。

前端只要看到二维码轮询返回 `confirmed`，就提示“微信已连接”。但后端只有在同时解析到有效 `cred.Token` 时才会写入配置并重启 Gateway。如果真实接口返回的 token 字段名不在当前 parser 支持范围内，就可能出现：

- 前端显示“微信已连接”。
- 实际 `im.platforms[].token` 为空。
- Gateway 没有真正启动微信长轮询。
- 后续收不到微信消息。
- 用户误以为连接信息已保存。

### 3.2 高概率断点

#### 断点 A：QR confirmed 但没有 token

当前 token 解析字段主要是：

- `bot_token`
- `token`
- `access_token`

如果 iLink / OpenClaw / 微信代理实际返回字段不同，前端仍可能显示成功，但后端不会持久化。

建议后续抓取一次真实 `get_qrcode_status` 响应，对照 `internal/im/wechat_qr.go` 的解析字段。

#### 断点 B：健康状态存在“假阳性”

Gateway 中存在一个特殊判断：如果微信平台配置里有 token，但 adapter 没注册，也可能显示 `authenticated`。

这只能说明“配置里看到了凭证”，不能说明：

- adapter 已启动；
- 长轮询正在运行；
- `getupdates` 正常返回；
- 已收到入站消息；
- 消息已进入 Agent。

UI 上应区分：

- QR 已确认；
- 凭证已保存；
- Gateway 已启动；
- adapter 正在 polling；
- 最近收到入站消息。

#### 断点 C：Gateway 健康状态不是严格实时状态

微信 adapter 后续如果遇到 session 过期或 HTTP 错误，adapter 内部会更新自身状态，但 Gateway 的 `/im/health` 主要依赖启动时缓存，不一定实时反映 adapter 当前状态。

这会造成 UI 健康状态滞后，用户看到“已连接”，但底层轮询已经失败。

#### 断点 D：`getupdates` 响应兼容面偏窄

当前微信长轮询只直接消费顶层 `msgs`。如果真实响应形态是：

```json
{
  "data": {
    "msgs": []
  }
}
```

则可能出现请求成功、cursor 更新正常，但消息没有被解析进入 Gateway。

建议补充真实响应样本，并增强 parser 对 `data.msgs`、`message`、`msg` 等包装结构的兼容。

#### 断点 E：配置保存路径可能看错

P-Chat 的全局数据目录由 `paths.GlobalDir()` 决定，优先级是：

1. `PCHAT_DATA_HOME`
2. 二进制位于 `bin/` 或 `dev-bin/` 时使用对应目录下的 `.p-chat`
3. 用户 HOME 下的 `.p-chat`

启动日志会打印实际 home dir。若用户检查的是项目根 `.p-chat`，但运行时写入的是 `bin/.p-chat` 或用户目录，就会误判为“没有持久化”。

## 4. 对标 Hermes / OpenClaw 的建议

### 4.1 Hermes Gateway 思路

Hermes 类 Gateway 方案强调：

- 单一 Gateway 管理所有平台连接。
- Adapter 只做平台协议翻译。
- Gateway 统一做 session resolution、identity、persona、rate limit、agent runner、outbound delivery。
- 连接状态应反映完整链路，而不是只反映配置存在。

P-Chat 当前方向与 Hermes 模式一致，但微信接入还需要把“认证态”和“收发态”拆开。

### 4.2 OpenClaw / iLink 微信思路

OpenClaw 风格微信接入通常围绕：

- QR 登录获取 bot token。
- 本地保存 token / bot id / user id。
- 调用 `notifystart`。
- 使用 `getupdates` 长轮询。
- 使用 cursor / buffer 续传。
- 用 context token 发送回复。

P-Chat 当前已有这些核心元素，但还需要补充：

- token 字段兼容；
- `getupdates` 响应兼容；
- 轮询状态可观测；
- 登录态与消息态拆分展示；
- session 过期后的重新扫码提示。

## 5. 微信排查顺序

建议按以下顺序确认：

1. 扫码确认后立即调用 `/api/v1/im/config`，检查：
   - `im.enabled == true`
   - 微信 platform `enabled == true`
   - `token` 非空
   - `extra.ilink_bot_id` 或 `extra.ilink_user_id` 存在
2. 查看启动日志中的 `home dir`，确认配置写入目录是否是预期目录。
3. 调用 `/api/v1/im/health`，确认状态不是单纯 `authenticated`，而是真正 polling / ok。
4. 打开 `/api/v1/im/events`，观察是否出现：
   - `adapter_started`
   - `inbound_received`
   - `inbound_processing`
   - `inbound_ok`
5. 若没有 `inbound_received`，抓取真实 `getupdates` 响应，优先检查响应包装和字段名。
6. 若有 `inbound_received` 但无回复，继续检查 `ProcessIMEvent`、LLM 调用和 outbound dispatch。
7. 若 outbound 失败，重点检查 context token 是否保存，以及 `sendmessage` 请求是否被平台接受。

## 6. 微信后续优化建议

### P0

- 前端不要仅凭 `confirmed` 显示“微信已连接”，必须确认后端返回凭证已保存。
- 后端在 `confirmed` 但 `cred.Token` 为空时返回明确状态，例如 `confirmed_without_token`。
- `/im/health` 区分 `authenticated`、`polling`、`error`、`expired`、`last_inbound_at`。
- 增强 token 字段解析，基于真实响应样本补齐。
- 增强 `getupdates` 响应解析，支持 `data.msgs` 等包装。

### P1

- UI 展示最近一次入站消息时间、最近错误、最近轮询时间。
- session expired 时提示重新扫码。
- 增加微信 adapter 的端到端测试：QR confirmed -> config saved -> adapter started -> fake getupdates -> inbound event -> agent -> outbound。

### P2

- 将微信高风险接入与 build tag 策略重新对齐。
- 增加 IM audit / metrics，用于排查生产环境问题。

## 7. 项目选择 UI 当前问题

### 7.1 当前链路

项目目录管理由以下模块组成：

| 领域 | 文件 |
| --- | --- |
| 项目读写 | `internal/project/project.go` |
| 项目 CRUD API | `internal/server/projects.go` |
| 文件夹选择 | `internal/server/dialog.go` |
| 项目下拉与新增弹窗 | `frontend/src/components/SessionSidebar.vue` |
| 前端 API | `frontend/src/api/client.ts` |

项目列表存储在：

```text
<GlobalDir>/projects.json
```

其中每个项目结构是：

```json
{
  "name": "项目名称",
  "path": "项目路径"
}
```

### 7.2 问题 A：下拉项只展示项目名称

当前 `SessionSidebar.vue` 中 `projectOptions` 使用：

- `label: p.name`
- `value: p.path`

`renderLabel` 也只渲染 `option.label`。

结果是：

- 多个项目同名时不可区分。
- 用户看不到当前项目实际路径。
- 选中指定目录后，无法确认是否选对路径。

建议：

- 下拉列表展示两行：
  - 主行：项目名称
  - 副行：完整路径
- 选中态展示：
  - `项目名`
  - 或 `项目名 · ...\parent\dir`
- hover/title 展示完整路径。

### 7.3 问题 B：中文路径可能乱码

当前 Windows 文件夹选择使用 PowerShell 打开 `.NET FolderBrowserDialog`，然后通过 stdout 返回路径。

风险点：

- Windows PowerShell 默认输出编码不一定是 UTF-8。
- Go 端直接 `string(out)` 读取 stdout。
- 中文目录可能在 stdout 编码转换时损坏。

建议：

- PowerShell 脚本显式设置 UTF-8 输出。
- 或改用 Wails/native dialog，避免通过 shell stdout 传中文路径。
- 后端返回 JSON 前确保 path 是有效 UTF-8。
- 前端保存后立即回显路径，方便用户发现乱码。

### 7.4 问题 C：新增项目缺少可见必填校验

当前前端逻辑：

```ts
if (!newProjectName.value.trim() || !newProjectPath.value.trim()) return
```

这会直接静默返回，用户看起来像按钮无响应。

后端确实会校验 `name/path` 必填，但前端没有字段级提示。

建议：

- 项目名称必填。
- 项目目录必填。
- 无效时显示字段错误。
- 添加按钮可在无效时禁用。
- 点击添加时如字段为空，给 toast 提示。

## 8. 项目 UI 优化建议

### 8.1 下拉展示

推荐结构：

```text
📁 P-Chat
   D:\develop\project\P-chat
```

如果路径过长：

```text
📁 P-Chat
   ...\develop\project\P-chat
```

需要注意：

- 保持侧栏宽度稳定。
- 路径单行省略。
- 选中态不要挤压删除/添加按钮。
- 完整路径放入 `title`。

### 8.2 添加项目弹窗

建议交互：

1. 用户点击“浏览”选择目录。
2. 自动填充项目目录。
3. 如果项目名称为空，自动用目录 basename 填入项目名称。
4. 用户仍可手动修改项目名称。
5. 点击添加前校验：
   - 名称不能为空；
   - 路径不能为空；
   - 路径必须是绝对路径；
   - 路径必须存在且是目录；
   - 路径不能重复。

### 8.3 后端校验

建议在 `AddProject` 或 `project.Add` 层补充：

- `strings.TrimSpace(name/path)`。
- `filepath.Clean(path)`。
- `filepath.IsAbs(path)`。
- `os.Stat(path)` 必须存在且是目录。
- Windows 下路径去重大小写不敏感。
- 重复路径返回明确业务错误，而不是静默返回旧列表。

### 8.4 中文路径防护

建议增加测试样例：

```text
D:\项目\测试工程
C:\Users\admin\桌面\中文项目
```

验证点：

- 文件夹选择返回值不乱码。
- POST `/api/v1/projects` payload 中中文正常。
- `projects.json` 中中文正常。
- GET `/api/v1/projects` 返回中文正常。
- 下拉选中态中文正常。

## 9. 合并后的优先级建议

### P0：先解决误导和数据损坏

- 微信 QR confirmed 但未保存 token 时，不显示“已连接”。
- 微信凭证保存成功后再提示连接完成。
- 明确 Gateway / adapter / polling 的真实状态。
- 修复中文路径选择返回乱码。
- 新增项目必填字段给出明确提示。

### P1：增强可观测与可区分性

- `/im/health` 增加 `last_poll_at`、`last_inbound_at`、`last_error`。
- `/im/events` 展示微信轮询错误、session 过期、入站消息。
- 项目下拉展示项目路径。
- 添加项目时自动填项目名。
- 重复路径给出明确提示。

### P2：补测试与架构对齐

- 补微信 QR 到长轮询的端到端测试。
- 补项目中文路径单元 / API / 前端测试。
- 对齐 IM build tag 策略，明确微信第三方接入风险。

## 10. 建议验收清单

### 微信接入

- 扫码未返回 token 时，UI 不显示“已连接”。
- 扫码返回 token 后，`/api/v1/im/config` 中 token 已保存。
- Gateway health 显示 adapter 正在 polling。
- 发一条微信消息后，`/api/v1/im/events` 出现 `inbound_received`。
- 微信消息进入 P-Chat session，并能收到 Agent 回复。
- 断线 / session expired 后 UI 提示重新扫码。

### 项目 UI

- 项目下拉展示项目名和路径。
- 同名项目可以通过路径区分。
- 中文目录选择后显示不乱码。
- 中文目录保存后 `projects.json` 不乱码。
- 名称为空时无法添加，并提示“项目名称必填”。
- 路径为空时无法添加，并提示“项目目录必填”。
- 不存在的路径无法添加。
- 重复路径无法重复添加，并提示已存在。

## 11. 参考资料

- Hermes Gateway Internals：https://hermes-agent.nousresearch.com/docs/developer-guide/gateway-internals/
- Hermes Adding Platform Adapter：https://hermes-agent.nousresearch.com/docs/developer-guide/adding-platform-adapters
- Tencent OpenClaw WeChat：https://github.com/Tencent/openclaw-weixin
- P-Chat IM 方案：`docs/plans/im-bridge-plan.md`
- P-Chat IM 模块文档：`.agents/docs/im.md`
- P-Chat 前端模块文档：`.agents/docs/frontend.md`
- P-Chat 基础设施模块文档：`.agents/docs/infrastructure.md`
