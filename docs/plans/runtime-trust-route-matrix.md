# Runtime Trust 路由能力矩阵（决策票 #4 资产）

> 本文是 [`runtime-trust-decision-map.md`](runtime-trust-decision-map.md) #4 的工作文档：
> 把 server 的全部入口按**能力**（而非 URL 前缀）分类，给出主体 → 能力映射与每类失败默认值。
> #7 的 "route classification" 切片以本文为准逐类落地；新增路由时必须先归入某一能力类。

## 能力类定义

| 类 | 名称 | 语义 |
| --- | --- | --- |
| P0 | 公开探测 | 父进程就绪探测与身份发现；任何主体可达，响应字段冻结，永不携带凭据/secret |
| P1 | 静态资产 | Web UI 与扩展安装包；只含静态内容，不含用户数据 |
| IO | 普通会话读写 | 会话/消息/内容状态的读取与变更，**不触发** LLM 调用、工具执行或沙箱批准 |
| X | 工具执行 | 触发 agent 循环、LLM 调用、工具/斜杠命令执行、沙箱批准应答的端点 |
| M | 配置与 secret | 修改持久化配置、管理凭据、管理外部连接（provider/MCP/IM/知识库/技能仓库）的端点 |
| D | 诊断 | 内存/goroutine 快照与 pprof；快照可能含进程内存内容 |
| B | 浏览器桥接 | Browser Extension 的 WebSocket 通道（配对见 #3） |
| S | 平台签名 webhook | IM 平台入站回调，只经平台签名 adapter 验证 |

## 主体 → 能力映射

| 能力类 | Local Operator | Paired Extension | Remote Operator（操作级） | Remote Operator（管理级） | Signed Webhook |
| --- | --- | --- | --- | --- | --- |
| P0 公开探测 | ✓ | ✓ | ✓ | ✓ | ✓ |
| P1 静态资产 | ✓ | ✓ | ✓ | ✓ | ✓ |
| IO 会话读写 | ✓ | ✗ | ✓ | ✓ | ✗ |
| X 工具执行 | ✓ | ✗ | ✓ | ✓ | ✗ |
| M 配置与 secret | ✓ | ✗ | ✗ | ✓ | ✗ |
| D 诊断 | ✓ | ✗ | ✗ | ✓ | ✗ |
| B 浏览器桥接 | ✗ | ✓（配对后） | ✗ | ✗ | ✗ |
| S 签名 webhook | ✗ | ✗ | ✗ | ✗ | ✓ |

- Local Operator = 全能力（本机持票进程即"机主"）。
- 管理级凭据包含操作级能力（层级包含，非正交）。
- 未来的配对管理端点（待配对列表 / 批准 / 撤销）归 **M**。
- B 类只服务 Paired Extension；Local Operator 管理浏览器走 M 类 HTTP 端点。
- 分级只保留操作/管理两级（#2 已定）；IO 与 X 分列是为了审计与未来可能的"只读远程"凭据，不新增级别。

## 每类失败默认值

| 类 | 无凭据 | 凭据无效/已撤销 | 级别不足（操作级→M/D） |
| --- | --- | --- | --- |
| P0 / P1 | 放行 | 放行 | — |
| IO / X | 401 `auth_required` | 401 `auth_invalid` | — |
| M / D | 401 `auth_required` | 401 `auth_invalid` | 403 `auth_forbidden` |
| B | 进入待配对（#3），不注册 | WS 关闭 4002 | — |
| S | 401（验签失败） | 401 | — |

- 错误形态统一为 `{error, error_kind}`，`error_kind ∈ {auth_required, auth_invalid, auth_forbidden}`，沿用现有 error_kind 规范。
- SSE 端点在流开始前失败（401），流中不做权限降级。
- B 类永不要求 Operator 凭据；S 类永不要求 Operator 凭据，也绝不因缺少普通 token 被误拒。
- 能力类与沙箱（sandbox Decision）、BR-04 浏览器策略、work_mode 工具白名单**正交**：凭据回答"你是谁、能进哪扇门"，沙箱回答"这次操作要不要确认"。

## 全量路由分类

### P0 公开探测（无凭据放行）

| 路由 | 说明 |
| --- | --- |
| `GET /api/v1/health` | 父进程就绪探测 + profile/instance/PID 身份发现；字段冻结 |
| `GET /api/v1/version` | 版本字符串 |

### P1 静态资产（无凭据放行）

| 路由 | 说明 |
| --- | --- |
| `/app/*` | Web UI 静态资源；UI 加载后调 API 仍需凭据（#8 注入） |
| `GET /api/v1/browser/extension` | 扩展安装包 zip（二进制内嵌，非秘密） |

### B 浏览器桥接（仅 Paired Extension）

| 路由 | 说明 |
| --- | --- |
| `GET /api/v1/browser/ws` | WS 升级 + #3 配对握手；未配对不注册进 Hub |

### S 平台签名 webhook（仅平台签名验证）

| 路由 | 说明 |
| --- | --- |
| `POST /api/v1/im/feishu/webhook` | 飞书 verification token（加密回调待补）；未来各平台 webhook 同归此类 |

### IO 普通会话读写（Local Operator / 操作级）

| 路由 | 说明 |
| --- | --- |
| `GET/POST /api/v1/sessions`、`GET/PATCH/DELETE /api/v1/sessions/:id` | 会话 CRUD |
| `POST /api/v1/sessions/:id/archive|unarchive|fork`、`GET /api/v1/sessions/archived`、`DELETE .../permanent`、`DELETE .../messages` | 归档/派生/删除 |
| `POST /api/v1/sessions/:id/rollback`、`.../rollback/undo` | 消息回滚 |
| `GET /api/v1/sessions/:id/messages`、`.../snapshot`、`.../context`、`.../export`、`.../replies`、`.../tool-result/:tool_id` | 消息读取/恢复/导出 |
| `POST /api/v1/sessions/:id/messages/:reply_id/activate` | 切换活动回复 |
| `PATCH .../reasoning-effort`、`POST .../system-message` | 会话参数 |
| `GET/DELETE .../todos` | 待办 |
| `GET/POST/DELETE/PATCH .../turn-queue*`（含 claim/complete/fail/retry） | 回合队列（入队不执行，执行复用 /messages） |
| `GET .../subagent-jobs*`、`POST .../subagent-jobs/:task_id/cancel` | 子代理查看与取消（取消是减副作用，归 IO） |
| `POST /api/v1/sessions/:id/cancel-stream` | 客户端中止 |
| `GET /api/v1/search`、`GET /api/v1/token-stats` | 搜索与统计 |
| `GET/POST /api/v1/styles`、`GET/PATCH/DELETE /api/v1/styles/:id` | 人格风格内容管理 |
| `GET /api/v1/stylegen/:job`、`.../events` | 风格生成进度读取 |
| `GET /api/v1/providers`、`GET /api/v1/providers/:name`、`GET /api/v1/provider-presets` | ⚠ 当前响应携带明文 `api_key`；归 IO 以 #5 脱敏落地为前提，脱敏前视同 M |
| `GET /api/v1/generation/options` | 生成能力选项 |
| `POST /api/v1/uploads`、`GET /api/v1/uploads/:id`、`GET /api/v1/generated/:id` | 会话附件与生成资产 |
| `GET /api/v1/commands`、`GET /api/v1/tools` | 命令/工具列表 |
| `GET /api/v1/projects` | 项目列表 |
| `GET /api/v1/skills*`（含 search/repos 读） | 技能目录读取 |
| `GET /api/v1/mcp/servers` | MCP 列表读取 |
| `GET /api/v1/knowledge/models|bases|bases/:name/nodes|nodes/:id/content|scan/status`、`POST /api/v1/knowledge/search` | 知识库读取与搜索 |
| `GET /api/v1/updates/check`、`GET /api/v1/migrations` | 更新检查与迁移状态读取 |
| `GET /api/v1/browser/status` | 浏览器控制开关状态 |
| `GET /api/v1/im/health`、`GET /api/v1/im/events` | IM 网关状态与事件流 |

### X 工具执行（Local Operator / 操作级；触发真实副作用）

| 路由 | 说明 |
| --- | --- |
| `POST /api/v1/sessions/:id/messages` | 发送消息 → agent 循环 + 工具执行 + LLM 花费 |
| `POST /api/v1/sessions/:id/regenerate` | 重跑 agent 循环 |
| `POST /api/v1/sessions/:id/execute-plan` | 执行计划 |
| `POST /api/v1/sessions/:id/question-response`、`.../confirm-response` | 沙箱/提问应答（批准危险操作的钥匙） |
| `POST /api/v1/commands/:name` | 斜杠命令执行 |
| `POST /api/v1/tools/:name/trial` | 工具试运行 |
| `POST /api/v1/stylegen` | 风格生成（LLM） |
| `POST /api/v1/sessions/:id/title`、`.../compress` | LLM 工具型端点（标题/压缩） |

### M 配置与 secret（Local Operator / 管理级）

| 路由 | 说明 |
| --- | --- |
| `POST/PATCH/DELETE /api/v1/providers*`、models CRUD、`default`、`capabilities` | Provider/Model 写 |
| `GET /api/v1/providers/:name/upstream-models`、`POST .../probe-models`、`POST .../test` | 携带 secret 的上游 egress |
| `GET/PATCH /api/v1/config` | 系统配置；⚠ 部分段含 secret（#5）。前端启动依赖需在 #7 验证，必要时拆出非 secret 公共子集端点 |
| `GET/PUT /api/v1/settings/web_search`、`POST .../test` | ⚠ 含搜索 provider key（#5） |
| `PATCH /api/v1/knowledge/config`、`POST/DELETE .../bases`、`.../scan`、`.../clear`、`DELETE .../nodes/:id` | 知识库管理 |
| `POST /api/v1/skills/install`、`DELETE /api/v1/skills/:name`、`POST/DELETE /api/v1/skills/repos` | 技能安装/仓库管理（向 Data Home 装代码） |
| `POST/DELETE /api/v1/mcp/servers*`、start/stop/restart、`PATCH /api/v1/mcp/global` | MCP 管理（spawn 进程） |
| `POST/DELETE /api/v1/projects` | 项目目录注册 |
| `POST /api/v1/updates/download`、`POST /api/v1/migrations/rollback` | 二进制下载（供应链）/ 迁移回滚（破坏性） |
| `POST /api/v1/dialog/folder` | 宿主机原生对话框；**仅 Local Operator 有意义** |
| `GET /api/v1/browser/list`、`.../tabs`、`POST .../active-tab`、`POST /api/v1/browser/config` | 浏览器控制面管理（标签页 URL 属浏览隐私） |
| `GET/PATCH /api/v1/im/config`、`POST /api/v1/im/test*`、`POST/GET /api/v1/im/wechat/qr*` | ⚠ IM 配置含平台 token（#5）；QR 发起平台登录 |
| 未来：配对管理端点（pending 列表/批准/撤销） | 凭据管理，归 M |

### D 诊断（Local Operator / 管理级）

| 路由 | 说明 |
| --- | --- |
| `GET /api/v1/diagnostics/memory`、`GET/PATCH /api/v1/diagnostics/config` | 内存状态与监控配置 |
| `GET /api/v1/diagnostics/snapshot/heap|goroutine` | 快照下载，含进程内存内容 |
| `/debug/pprof*` | **默认从"开"翻转为"关"**（`PC_PPROF=1` 显式 opt-in）；开启后仍按 D 类要求管理级凭据 |

## 明确的非目标

- 不改变 maxBody / rateLimit / CORS / trace 中间件的传输层语义（与能力类正交）。
- 不在本轮实现任何鉴权 middleware；本文只是 #7 "route classification" 切片的分类依据。
- 不为"只读远程"新增第三凭据级别；IO/X 分列已预留该可能。
