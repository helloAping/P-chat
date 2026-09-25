# Runtime Trust 决策地图

状态：完成 — 全部 8 票已决策（2026-09-24）

目标：让一个 Runtime Instance 只接受被明确授权的调用主体，同时保持默认本机 GUI、CLI、Web、Browser Extension 与已签名 IM webhook 可用。本文只记录待决策问题和答案；实现细节放在后续 ticket 资产中。

## 已确认事实

- `Runtime Profile` 与 `Runtime Instance` 已在根 `CONTEXT.md` 定义，本地图沿用这两个术语。
- 默认监听 `127.0.0.1`，但 `server.host` 是可配置项；非 loopback 请求已有 rate limit，说明源码允许这种运行形态，却没有身份验证。
- Web、Wails 与 Browser Extension 是三种真实 adapter：same-origin HTTP、`wails.localhost` 跨域 HTTP/SSE、`chrome-extension://` WebSocket。
- CORS / Origin 只约束浏览器来源，不是调用主体身份；无 `Origin` 的客户端仍可访问。
- IM webhook 不能套用普通客户端登录流程，必须继续通过各平台的签名 adapter 验证。
- `allow_eval_write=false` 尚未形成可执行策略；这是独立的 fail-closed 修复，不应被鉴权设计长期阻塞。

## #1：非本机访问是不是正式产品能力？

Blocked by: —
Type: Discuss

### Question

当 `server.host` 绑定到局域网或公网地址时，P-Chat 应该：

1. 正式支持，但必须显式启用并配置认证；
2. 只支持本机访问，发现非 loopback 配置就拒绝启动；
3. 保持当前“能绑定就能访问”的开放行为。

推荐 **1**：默认仍是零配置 loopback；非 loopback 变成明确的高级能力，启动时没有认证配置就 fail closed。这样保留 Web/远程运维空间，又不会让 Data Home、LLM secret 和浏览器控制面裸露。

### Answer

已确认（2026-09-24）：采用方案 1。

- 本机模式继续作为默认值，只监听 loopback，且不要求用户手工配置凭据。
- 非本机模式是显式启用的高级能力，必须配置认证；缺少有效认证配置时，Runtime Instance 拒绝以非 loopback 地址启动。
- 这项决定只确定产品安全默认值，不代表所有本机调用主体天然等价；Browser Extension 配对、敏感 route 能力和 secret 回读仍由后续 tickets 决定。
- 根 `CONTEXT.md` 新增规范术语 `Runtime Access Mode`。

## #2：调用主体与能力如何建模？

Blocked by: #1
Type: Discuss

### Question

确定最小调用主体集合，以及每类主体能获得哪些能力。候选主体：Desktop Host、Local Web Client、CLI Client、Browser Extension、Remote Client、Signed Webhook。需要决定能力是 Runtime Profile 级长期凭证，还是 Runtime Instance 级短期 capability，及其撤销语义。

推荐把入口归并为四类主体，而不是每个客户端各造一套权限：

1. **Local Operator**：Desktop Host、同机 Web 与 CLI；默认本机体验不需要人工复制 token。
2. **Paired Extension**：每个 Browser Extension 独立配对、独立撤销，只获得浏览器桥接能力。
3. **Remote Operator**：用户显式创建的 Runtime Profile 级凭据，可按操作与管理能力分级。
4. **Signed Webhook**：只允许平台签名验证后的 IM 入站，不获得通用 HTTP 能力。

Runtime Instance 级短期 capability 用于自动启动的 Desktop/CLI 进程身份；Runtime Profile 级凭据只用于需要跨进程重启保持有效的配对与远程访问。

### Answer

已确认（2026-09-24）：采用四类调用主体模型；Local Operator 的身份边界是持票进程，不是 loopback 网卡。

- **Local Operator（本机操作者）**：Desktop Host、同机 Web 与 CLI。身份 = 持有 Runtime Instance 短期 capability 的本机进程；capability 由实例启动时随机生成并自动分发（spawn 环境、仅本用户可读的启动公告、Web UI 服务时注入），默认本机体验零配置、无需手工复制 token。loopback 只是 Runtime Access Mode 的网络范围，不是身份本身。该 capability 不防御同用户恶意软件（其本就能读 Data Home），防的是恶意网页与跨安全上下文越界——当前 Browser WebSocket 无 Origin 检查、DNS rebinding 可绕过 same-origin CORS 判定，"loopback 即信任"等于把浏览器控制面暴露给任何网页。
- **Paired Extension（已配对扩展）**：每个 Browser Extension 安装独立配对、独立撤销，只获得浏览器桥接能力。配对独立于网络路径成立（扩展的 serverURL 用户可改，可能指向非本机地址）。
- **Remote Operator（远程操作者）**：用户显式创建的 Runtime Profile 级长期凭据，仅在非本机模式存在；操作能力与管理能力可分级。不存在隐式远程主体（#1 的 fail-closed 已保证）。
- **Signed Webhook（已签名 Webhook）**：IM 入站只经平台签名 adapter 验证（当前飞书竖切为 verification token，加密回调待补），不获得通用 HTTP 能力，也不套用 Operator 凭据。

凭据生命周期：

- Runtime Instance 短期 capability：随进程实例生灭，重启即换新，不持久化；撤销语义 = 进程退出。用于识别自动启动的 Desktop/CLI 进程与 server 自服务的同机 Web UI。
- Runtime Profile 长期凭据：持久化于 Data Home，跨进程重启保持有效；撤销语义 = 用户显式、按主体独立撤销。用于扩展配对与远程访问。

约束与源码证据：

- 现有 `instance_id`（`runtimeprofile.NewInstanceID()`）经 `/api/v1/health` 公开暴露，只是标识符；短期 capability 必须是不经任何 HTTP 响应暴露的独立随机密钥。
- 本机与非本机模式共用同一套主体校验路径；Runtime Access Mode 只决定凭据来源与是否允许远程主体，不改变校验逻辑本身。
- Wails webview 不能随意加请求 header（现有 trace_id 走 body 的变通为证），WebSocket 握手也不能带自定义 header；各 adapter 的凭据传输方式由 #3 与新增 #8 分别设计，不影响本主体模型。
- `/api/v1/health` 保持无凭据可达（父进程就绪探测与身份发现），其公开字段不得包含任何 capability。
- 根 `CONTEXT.md` 新增规范术语：调用主体（Runtime Caller）、本机操作者（Local Operator）、已配对扩展（Paired Extension）、远程操作者（Remote Operator）、已签名 Webhook（Signed Webhook）、实例凭据（Instance Capability）、运行配置凭据（Profile Credential）。

## #3：Browser Extension 如何配对和撤销？

Blocked by: #2
Type: Prototype

### Question

在不要求用户手工复制长 token 的前提下，验证扩展身份；同时支持重装扩展、多个浏览器、撤销单个浏览器、Runtime Instance 重启和协议升级。

### Answer

已确认（2026-09-24）：WS 通道内配对 + GUI 单击确认。原型 `internal/browser/pairing.go` + `pairing_test.go` 已验证全部必备路径（10 个测试通过）；生产 handler、扩展 JS 与 GUI 批准界面的接线属于 #7 切片。

**协议流程**（配对期望的协议版本与生产 `ProtocolVersion` 解耦，提升节奏由 #6 决定）：

1. 扩展 hello 携带自报 browserID 与可选 `pair_token`；无凭据连接进入待配对：不注册进 Hub、命令不可达，server 回 `pair_required{request_id}`，请求方信息（浏览器名、扩展版本、来源地址）进入待批准列表。
2. Local Operator 在 GUI 单击批准 → server 复用或生成 browserID、签发 256 位随机凭据、**先落盘再下发** `pair_approved{browser_id, pair_token}` → 以 1000 关闭当前连接 → 扩展持久化凭据后重连，凭据 hello 走唯一注册路径。拒绝则 `pair_denied` + 4003；待配对超时（原型 2 分钟）关闭 4000。
3. 批准锚点 = GUI 单击确认（已确认）。已知残余风险：恶意网页/进程可发起配对"请求"诱导误批；缓解 = 展示请求方信息 + 随时可撤销。双端数字比对作为可选加固保留，不进首版。

**各场景结论**：

- 重装扩展：chrome.storage.local 随卸载清空 → 新 browserID → 重新走首次配对（用户再点一次批准）。
- 多浏览器：各自独立 browserID + 凭据，独立撤销（测试覆盖）。
- 撤销单个浏览器：删除凭据即失效；在线连接的踢出（hub.Unregister + 4002）由接线层完成。扩展收到 4002 后应清除本地凭据并回到可重新配对状态（扩展端改动属 #7）。
- Runtime Instance 重启：凭据持久化于 Data Home（`pairing.json`，0600，tmp+rename 原子写），重启后自动恢复，零人工步骤（测试覆盖）。
- 协议升级 / 旧扩展：配对要求与协议版本绑定；v3 旧扩展收到 hello-ok + update_required 后以 4001 关闭——扩展现有逻辑把 4000-4999 视为永久失败、popup 优先展示"请重新下载扩展"，fail closed 且无需改动旧扩展（测试覆盖）。
- 启用语义变化：浏览器控制的自动启用从"任意连接触发"改为"首个配对批准触发"；未配对连接不再产生任何副作用。

**记录约束**：

- 凭据只经 WS 首包与持久化文件流转，不进 URL / 日志 / HTTP 响应体（与 #8 约束一致）。
- 配对独立于网络路径成立（扩展 serverURL 可指向非本机地址）；非本机配对请求的额外策略由 #4/#6 决定。
- 凭据文件明文存储是原型的有意简化（同用户恶意软件本就能读 Data Home，见 #2）；是否改存哈希在 #7 切片时定。
- 加固备选（不进首版）：WS Origin 为 `chrome-extension://` 时才允许发起配对；待配对请求频率限制。

## #4：路由能力矩阵是什么？

Blocked by: #2
Type: Discuss

### Question

将 route 按能力而不是按 URL 前缀分类。至少区分：公开健康探测、静态 Web、普通会话读写、配置与 secret、工具执行、诊断/pprof、Browser WebSocket、平台签名 webhook。明确每类失败默认值。

### Answer

已确认（2026-09-24）：八个能力类 + 主体映射 + 失败默认值。全量路由逐条分类见资产文档 [`runtime-trust-route-matrix.md`](runtime-trust-route-matrix.md)（#7 "route classification" 切片以它为准）。本轮无需提问：分类骨架由本题给定，主体映射是 #2 的直接推论，失败默认值是机械落地。

- **八个能力类**：P0 公开探测（health/version）、P1 静态资产（/app、扩展 zip）、IO 普通会话读写、X 工具执行（触发 agent/LLM/工具/沙箱批准的端点）、M 配置与 secret、D 诊断（含 pprof）、B 浏览器桥接 WS、S 平台签名 webhook。
- **主体映射**：Local Operator 全能力；Paired Extension 仅 B；Remote Operator 操作级 = IO+X、管理级 = IO+X+M+D（管理级包含操作级）；Signed Webhook 仅 S。B 与 S 永不要求 Operator 凭据。
- **失败默认值**：P0/P1 放行；IO/X 无凭据或凭据无效 → 401（`auth_required` / `auth_invalid`）；M/D 另加级别不足 → 403（`auth_forbidden`）；B 无凭据 → 待配对（#3）、凭据无效 → WS 关闭 4002；S 验签失败 → 401。SSE 在流开始前失败，流中不降级。错误形态统一 `{error, error_kind}`。
- **关键裁定**：
  - pprof 默认值从"开"翻转为"关"（`PC_PPROF=1` opt-in），开启后仍按 D 类要求管理级凭据；
  - `GET /providers*` 当前回传明文 `api_key`，其 IO 归类以 #5 脱敏落地为前提（脱敏前视同 M）；
  - `POST /dialog/folder` 仅 Local Operator 有意义（宿主机原生对话框）；
  - 未来配对管理端点（待配对列表/批准/撤销）归 M；
  - 能力类与沙箱 Decision、BR-04 浏览器策略、work_mode 白名单正交：凭据回答"能进哪扇门"，沙箱回答"这次操作要不要确认"。
- 根 `CONTEXT.md` 新增规范术语 `路由能力类（Route Capability Class）`。

## #5：secret 的读取与更新契约是什么？

Blocked by: #2, #4
Type: Discuss

### Question

Provider API key、custom headers、IM token 与动态工具 secret 是否永远不再回传明文；前端如何表达“已配置 / 保持原值 / 清空 / 替换”，以及是否在本轮引入 OS credential store。

### Answer

已确认（2026-09-24）：**明文永不回传，对所有主体无例外**（含本机 Local Operator 的 GUI——已确认，不引入 reveal 端点）。统一采用仓内已有的 web_search 模式为全仓规范；OS credential store 本轮不引入，作为独立迁移。

**规范契约（Secret 脱敏契约）**：

- 读侧：secret 字段一律脱敏为 `has_*` 布尔（已配置/未配置），任何端点、任何主体都拿不到明文。
- 写侧三态：字段省略 = 保持原值；显式 clear flag = 清空；携带新值 = 替换。配合读侧布尔即覆盖前端「已配置 / 保持原值 / 清空 / 替换」四态表达。
- 仓内先例即模板：`GET /search/settings` 的 `has_key` + `PATCH` 的 `api_key` 指针/`clear_api_key`（`internal/server/search_api.go`），前端 UX 模板为 `WebSearchSettings.vue`（「已配置」标签 + 占位符 + 清除按钮）。

**各 secret 面结论**（现状勘察 → 目标）：

- Provider `api_key`：当前明文回传（`ProviderFull`，GET 与 PATCH 响应；`AppSettingsModal.vue` 依赖明文预填编辑框）。目标：读侧改 `has_api_key`；HTTP 层 `UpdateProviderRequest` 补 `clear_api_key`——config 层 `ProviderPatch` 本已支持保持/清空/替换三态，缺的只是 HTTP 字段。
- Provider `custom_headers`：当前整个 map 明文回传。目标：header 名可见、值全部脱敏（值皆视为潜在 secret，名不敏感）；写侧保持现有 nil=保持 / 空 map=清空 / 非空=整体替换语义。
- IM 平台 8 个 secret 字段：当前明文回传（`GET /im/config` 及 PATCH 响应），且 `IMConfigPatch.Platforms` 是数组整体替换——客户端必须持有明文才能改任何字段，与脱敏目标强耦合。目标：读侧逐字段 `has_*`；写侧重构为逐平台、逐字段三态 patch。这是本票唯一的结构性改动，其余都是响应层收窄。
- web_search `api_key`：已是目标契约，不动。
- MCP `env`：当前不回传（`ServerInfo` 只含 name/state/tool_count/error），仅创建时写入、无编辑端点。保持写后不读。
- 动态工具 `dynamic.*.config`：无 API 回传，仅文件编辑。保持。

**约束**：

- 脱敏是响应层契约，不改变存储形态；secret 仍明文存于 Data Home 配置文件（与 #2/#3 对同用户恶意软件的既定立场一致）。存储加密 / OS credential store 是独立迁移，不进第一条安全切片。
- 不引入任何「显示明文」端点；用户确认或迁移 key 的方式 = 重新粘贴。
- 脱敏落地前，`GET /providers*` 按 #4 裁定视同 M 类；落地后归 IO。
- 失败默认值沿用 #4，脱敏不改变错误形态。
- 根 `CONTEXT.md` 新增规范术语 `Secret 脱敏契约（Secret Redaction Contract）`。

## #6：兼容迁移怎样 fail closed？

Blocked by: #2, #3, #4, #5, #8
Type: Prototype

### Question

设计旧 GUI、旧 CLI、旧 Browser Extension 与新 Server 的滚动升级矩阵；决定协议版本、capability discovery、过渡期提示和最终拒绝点。

### Answer

已确认（2026-09-24）：**立即 fail closed，无 legacy-open 过渡窗**（已确认）。原型：`internal/auth/mode.go`（健康广告）+ `compat_test.go`（兼容矩阵可执行规格，5 格全过）；401 文案已改为可操作双语指引（`middleware.go`）。全仓 build + browser/server 回归通过。

**滚动升级矩阵**（每格都有原型测试或既有测试锚点）：

- **标准升级（主路径）**：updater 流程本已「等待旧 GUI 退出 → 替换二进制 → 重启新 GUI」（`pchat-updater --parent-pid/--launch`），新 GUI spawn 新 server、经环境变量下发凭据——**默认 loopback 升级零人工步骤** ✓（`TestCompatNewParentNewServer`）。
- **旧父进程 spawn 新 server**（唯一现实触发点：旧 GUI 运行中 server 崩溃，`watchAndRestart` 拉起已升级的新二进制）：server 自铸凭据并 fail closed；旧客户端收到 401 `auth_required`，错误体自带「请重启或升级客户端」双语指引——旧前端 `jsonFetch`/`consumeStreamRequest` 会把错误体文本原样抛给用户（已核实 `client.ts:438`），重启客户端即恢复（`TestCompatOldParentNewServerFailsClosed`）。
- **旧浏览器标签页（旧 JS）+ 新 server**：同上 401 路径；用户按指引重新打开（经 GUI 菜单 / `pchat web` 的 bootstrap 链接进入）。
- **旧扩展 + 新 server**：#3 原型已覆盖——hello 协议版本不符 → `update_required` + 4001 关闭；扩展现有逻辑把 4000-4999 视为永久失败、停止自动重连、popup 提示重新下载，**不会因自动重连重新开启 Browser Control** ✓。
- **新客户端 + 旧 server（前向兼容）**：新客户端多发的 `Authorization` 头被旧 server 忽略；生产 CORS 预检本已放行 `Authorization`（`server.go:658` `Access-Control-Allow-Headers`），跨版本零摩擦 ✓（`TestCompatNewClientOldServer`）。
- **凭据轮换**：server 重启即换新凭据，旧 cookie / 旧粘贴值一律 401 `auth_invalid` + 重启指引（`TestCompatRotatedCapabilityRejectsOldCredential`）。

**协议版本与 capability discovery**：

- HTTP 侧不引入新协议版本握手：401 `auth_required` 自描述，且 `/api/v1/health` 新增 `auth_required: true` 广告（P0 公开），浏览器端 JS 可在首个业务请求前主动展示引导页（`TestCompatHealthAdvertisesAuthRequired`）。
- WS 侧沿用 #3 的 hello 协议版本机制， pairing 期望版本与生产 `ProtocolVersion` 解耦，提升节奏由接线切片决定。

**最终拒绝点**：即首个鉴权强制版本本身。不航运任何「无凭据下发就回退开放」的窗口——该窗口无法区分「旧父进程」与「裸奔的独立 server」，且等于在安全修复版里继续航运漏洞。无弃用时间线需要管理。

## #7：第一条实现切片如何验收？

Blocked by: #6
Type: Discuss

### Question

把方案切成可独立发布、可回滚的小提交，并明确 interface 级测试。候选顺序：行为测试基线 → Authority module → route classification → Browser pairing → secret redaction → non-loopback enforcement → 删除旧浅规则。

### Answer

已确认（2026-09-24）：采用题面候选顺序，七片；**发布单元 = 单一安全版本**（#6「最终拒绝点即首个鉴权强制版本」已预决），切片是提交/审查/回滚粒度，不逐片发版。本轮无需提问：所有分叉已被 #1-#6 预决。

**切片序列**（每片独立可回滚；回滚 = git revert，无持久格式破坏）：

1. **行为测试基线**：为信任相关表面补 characterization 测试——SSE 事件形态、CORS Origin 矩阵、WS hello 流程、无 Origin 客户端、health 字段、扩展 zip 分发。只 pin 不得改变的行为；即将被 #5 改变的明文回传不 pin。验收：新基线全绿。
2. **Authority module（dark launch）**：`internal/auth` 落生产形态；serverproc / pchat-gui spawn 环境变量下发（含 `watchAndRestart` 重建路径）；GUI 经 `WindowExecJS` 注入 `__PCHAT_CAPABILITY__`；前端 `client.ts` 集中挂 `Authorization` 头；CLI `httpcli` 挂 Bearer；server 启动 `ResolveCapability` + `/health` 广告 `auth_required`。**只分发不强制**——无任何路由拒绝。**关键约束：bootstrap 铸造端点不得在本片出现**（它从诞生起就必须是 M 类，否则任何人可铸 token 换凭据，整个机制失效）。
3. **Route classification + HTTP 强制**：按 [`runtime-trust-route-matrix.md`](runtime-trust-route-matrix.md) 给路由分组挂 `RequireCapability`；全局挂 `HostAllowlist`；P0/P1 放行，IO/X/M/D 要凭据，S 验签不变（Local Operator 凭据亦可通行），B 暂保持现状（既有暴露，由 #4 片关闭）；pprof 默认值翻转为关（`PC_PPROF=1` opt-in）；bootstrap mint/unlock 端点与前端引导页、GUI「在浏览器中打开」、CLI 打印登录链接同片落地。验收：SSE 在流开始前 401、流中不降级；旧客户端 401 文案可操作。
4. **Browser pairing**：`Pairer` 接入 `BrowserWebSocket`；配对管理端点（M 类）；GUI 批准界面；扩展 JS（凭据 hello、pair_required/approved/denied、4xxx 关闭码处理、凭据持久化）；扩展 zip 重打包；删除「连接即自动启用」旧语义。
5. **Secret redaction**：providers 读侧 `has_api_key` + custom_headers 值脱敏、HTTP 层补 `clear_api_key`；IM 配置逐字段 `has_*` + 逐平台逐字段三态 patch 重构；前端设置页改四态 UX（模板：`WebSearchSettings.vue`）。MCP / 动态工具不动（本就不回传）。
6. **Non-loopback enforcement**：**含 Remote Operator 凭据签发**（候选清单的隐含缺口——没有它 fail-closed 的非本机模式永远无法启用）；启动闸门（非 loopback 绑定 + 无有效凭据配置 → 拒绝启动并给出可操作错误）；操作/管理分级在 M/D 路由生效；配置新增字段按 versioning 文档确认是否需 upgrade 步骤。
7. **删除旧浅规则 + 收尾**：移除过渡残留；全量不变量测试套件定稿；更新 `.agents/docs`（server.md 等）与 README。`allow_eval_write` 不在本片——已确认事实将其列为独立的 fail-closed 修复。

**Interface 级测试**（每片的缝合处契约）：

- 片 2：子进程环境变量含 `PCHAT_INSTANCE_CAPABILITY`（serverproc / GUI 各一）；`httpcli` 请求带 Bearer（httptest 断言）；前端 `tests/` 新增头部附加测试（node --test 设施已存在）；`/health` 广告字段。
- 片 3：**路由分类可执行测试**——枚举 `engine.Routes()` 对照分类表，未知路由即测试失败（分类表成为唯一权威，新路由不得未分类落地）；逐类中间件集成测试；bootstrap 一次性/过期/重放；unlock cookie 属性。
- 片 4：WS 握手状态机与关闭码（#3 原型已备）+ 接线集成 + `pairing.json` 重启持久化（已备）。
- 片 5：逐端点响应形态测试（`api_key` 字段不存在、`has_api_key` 存在）；IM patch 三态。
- 片 6：启动闸门测试（非 loopback 无凭据 → 退出；loopback 无凭据 → 正常）。
- 片 7：全量回归 + 不变量套件。

**票面必测清单映射**：错误/过期/重放凭据 → 片 2/3（原型已备）；撤销 → 片 4；恶意 Origin → 片 3（CORS 矩阵 + HostAllowlist）；无 Origin 客户端 → 片 3（无 Origin ≠ 可信，由凭据层裁决）；signed webhook → 片 3（验签通过无需凭据、验签失败 401）；SSE → 片 3；WebSocket → 片 4；实例重启 → 片 2（凭据轮换拒绝旧值，已备）+ 片 4（配对存续，已备）；旧客户端失败提示 → 片 3（#6 已备，集成复验）。

**全计划无 SQLite schema 变更、无 `~/.p-chat` 目录结构变更**（pairing.json 为新增文件、凭据仅存内存、配置为可选新增字段），故不触发 §2.5 强制 upgrade 步骤；片 6 配置新增按 versioning 文档复核。

至此 8 票全部决策完毕，地图收敛。

## #8：Local Operator 的实例凭据如何分发与传输？

Blocked by: #2
Type: Prototype

### Question

在零配置前提下，实例凭据（Instance Capability）如何到达 Desktop Host、CLI 与同机 Web UI，并在每种 transport 上呈现？已知传输约束：Wails webview 不能加任意请求 header、WebSocket 握手无自定义 header、fetch 可带 header。候选来源：spawn 环境变量、仅本用户可读的启动公告、Web UI 服务时注入、loopback bootstrap 端点。需同时决定旧客户端无凭据时的失败形态（结论输入给 #6）。

### Answer

已确认（2026-09-24）：**spawn 环境变量分发 + 按 transport 分流呈现**。原型 `internal/auth/`（capability.go / middleware.go / bootstrap.go + 19 个测试全过，全仓 build 通过）；接入生产路由与前端属 #7 切片。

**勘察发现（设计依据）**：

- 前端全部 SSE 走 fetch + ReadableStream（`client.ts` `consumeStreamRequest`），**无 EventSource**；且 Wails webview 的 fetch 已携带自定义头（`X-Trace-Id` 在设）。故 `Authorization: Bearer` 头方案覆盖 CLI、GUI 后端与 webview 的全部传输——#2 记录的"webview 不能加 header"被源码证据修正。
- 唯一不能带头的浏览器传输是 WebSocket 握手，而扩展凭据已在 #3 解决（hello 首包），不经过本票机制。
- 启动公告基础设施已存在（`runtimeprofile.Announcement`，0600 原子写，父进程经 `PCHAT_RUNTIME_FILE` 传递路径）；且 **CLI 永远 spawn 自己的 server**（`cmd/pchat/main.go` `serverproc.Start`，无 attach 路径）——所有本机进程客户端都是 spawn 父进程，凭据分发因此不需要带凭据的公告文件。
- server 无 access 日志（`gin.New()` 未挂 Logger 中间件，已核实），query string 当前不进日志；这是必须保持的不变量。

**分发矩阵（凭据如何到达客户端）**：

- CLI / `pchat web` / GUI（spawn 场景）：父进程铸造 256 位凭据，经环境变量 `PCHAT_INSTANCE_CAPABILITY` 下发（与 `PCHAT_INSTANCE_ID` 同通道）。自动重启时父进程保持凭据不变、随重启命令重新下发（webview 无需重新注入）。
- 独立 `pchat-server`（无父进程）：server 自行生成，经 stdout 打印带 bootstrap token 的登录链接（Jupyter 模式）。
- Wails webview JS：GUI 经 `wailsruntime.WindowExecJS` 注入 `window.__PCHAT_CAPABILITY__`（与 `__PCHAT_BACKEND__` 同通道）——凭据完全不经过 HTTP。
- 同源浏览器 Web UI：启动方铸造**一次性、60s TTL 的 bootstrap token** 放进它打开的 URL；页面 JS 立即调 Unlock 兑换 HttpOnly cookie 并 `history.replaceState` 擦除 URL。手动打开（无 token 链接）时看到引导页：指示去 GUI 菜单 / CLI 获取登录链接，并提供粘贴框（粘贴实例凭据或 token 均可解锁）——已确认采用「引导页 + 粘贴兜底」。

**呈现矩阵（凭据如何在每种 transport 上呈现）**：

- Go 客户端与 webview：`Authorization: Bearer` 头（头存在时排他，不回退 cookie）。
- 同源浏览器：Unlock 设置 `pchat_cap` cookie（HttpOnly + SameSite=Strict + 会话期，值即凭据本身、无状态），同源 fetch/SSE/WS 自动携带，JS 不可读。
- 校验常数时间比较；失败形态沿用 #4：无凭据 → 401 `auth_required`，不匹配 → 401 `auth_invalid`。

**传输不变量**：

- **Host 允许列表**（新防线）：只放行 loopback 与配置的绑定 Host，拦截 DNS rebinding（重绑定后攻击页的 Host 仍是攻击者域名）。原型 `HostAllowlist` 中间件已验证含伪装形态（`127.0.0.1.evil.com` 等）。
- 凭据不得出现在任何 HTTP 响应体、URL 或日志中；bootstrap token 可出现在 URL 一次（一次性 + 短 TTL + 无 access 日志 + 前端擦除，泄露窗口收敛到兑换前瞬间）。
- 不把凭据注入所服务的 HTML——任何 loopback 客户端都能 GET / 读到它，等于把"loopback 可达"重新变成身份本身（题面约束）。
- 非本机模式启用 HTTPS 时 cookie 须补 `Secure`；CORS 须放行 `Authorization` 头；前端 uploads 当前 `credentials: 'omit'`——后两项是 #7 接线清单项。

**旧客户端失败形态（输入给 #6）**：无凭据请求一律 401 `auth_required`；server 在"父进程未下发凭据"状态（旧父进程 spawn 新 server）下的过渡行为由 #6 决定（fail open 窗口期或直接 fail closed）。

根 `CONTEXT.md` 新增规范术语 `引导令牌（Bootstrap Token）`。
