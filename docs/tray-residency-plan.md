# P-Chat GUI 托盘驻留落地计划

## 1. Context

当前 `pchat-gui.exe` 的关闭行为是：用户点击窗口关闭按钮后，Wails `OnBeforeClose` 会停止由 GUI 拉起的 `pchat-server.exe` 子进程，然后 GUI 退出。

这会导致 P-Chat 无法像常见 IM / 桌面助手一样长期驻留后台。为了支持后台聊天助手体验，本轮需要实现：

- 可配置的关闭窗口行为；
- Windows 托盘图标；
- 关闭窗口时可隐藏到托盘；
- 托盘菜单可重新打开 P-Chat；
- 托盘菜单可真正退出并清理 `pchat-server.exe`；
- 后续扩展托盘会话入口：新增对话、打开对话、最近会话。

本计划优先落地最小闭环：

1. 关闭到托盘；
2. 托盘打开；
3. 托盘退出；
4. `pchat-server.exe` 正确保活与清理；
5. 设置页可切换关闭行为。

---

## 2. 当前代码现状

### 2.1 GUI 生命周期

关键文件：

- `cmd/pchat-gui/main.go`

当前行为：

- Wails 配置中存在：
  - `StartHidden: true`
  - `HideWindowOnClose: false`
  - `OnStartup`
  - `OnDomReady`
  - `OnBeforeClose`
- `startup()` 中启动 `spawnAndWatch()`。
- `spawnAndWatch()` 负责：
  - 查找 `pchat-server.exe`；
  - 选择端口；
  - 设置 `PCHAT_PORT` / `PCHAT_DATA_HOME`；
  - 启动 server 子进程；
  - 等待 `/api/v1/health`；
  - 注入前端 backend URL；
  - 调用 `runtime.WindowShow()` 显示窗口。
- `beforeClose()` 当前直接停止 server，并返回 `false`，即允许 Wails 继续关闭窗口和退出应用。

当前缺口：

- 没有托盘图标；
- 没有托盘菜单；
- 没有 `quitting` 状态区分“隐藏窗口”和“真正退出”；
- 停止 server 的逻辑还没有抽成幂等方法；
- 没有从配置读取关闭行为。

### 2.2 配置系统

关键文件：

- `internal/config/config.go`
- `internal/config/system_config.go`
- `internal/server/system_config.go`
- `configs/config.yaml`

当前配置结构没有 `ui` 块。

现有可复用模式：

- `WorkMode` enum：
  - string 类型；
  - constants；
  - `Normalize()`；
  - `IsValid()`；
  - `Default()` 中设置默认值；
  - `Load()` 后做 normalize。
- 系统配置 PATCH：
  - `SystemConfigPatch` 使用 pointer 字段；
  - 避免 omitted field 覆盖旧值；
  - `UpdateSystemConfig()` 负责合并和持久化。

建议新增：

```yaml
ui:
  close_behavior: "exit"
```

取值：

- `exit`
- `tray`

默认：

```yaml
ui:
  close_behavior: "exit"
```

保持旧用户行为不变。

### 2.3 前端设置与会话能力

关键文件：

- `frontend/src/components/AppSettingsModal.vue`
- `frontend/src/api/client.ts`
- `frontend/src/stores/chat.ts`
- `frontend/src/App.vue`
- `frontend/src/utils/notify.ts`

现有能力：

- `chat.ts` 已有：
  - `loadSessions()`
  - `createSession()`
  - `switchSession(id)`
- `AppSettingsModal.vue` 已有系统设置 tab 和 radio 控件模式，可复用。
- `client.ts` 已有 `/api/v1/config` 类型和 API。
- `notify.ts` 已有窗口唤醒参考逻辑：
  - `WindowUnminimise()`
  - `WindowShow()`
  - `WindowSetAlwaysOnTop()`

注意：

- Wails runtime 不能在浏览器 preview 中静态假设存在。
- 前端监听托盘事件时，需要参考 `client.ts` 中动态 import / runtime guard 的写法。
- Wails `EventsOn()` 返回 off 函数，HMR / unmount 时要清理，避免重复注册。

---

## 3. 推荐实现方案

### 3.1 第一阶段：GUI 生命周期最小闭环

目标：

- 重构关闭逻辑；
- 增加托盘基础菜单；
- 支持关闭到托盘；
- 支持托盘打开和真正退出。

#### 3.1.1 重构 `App` 状态

文件：

- `cmd/pchat-gui/main.go`

建议在 `App` struct 增加：

```go
quitting atomic.Bool
serverMu sync.Mutex
serverStopped bool
```

用途：

- `quitting`：区分普通点 X 和托盘菜单“退出 P-Chat”。
- `serverMu/serverStopped`：保证停止 server 幂等，避免重复 kill/wait。

#### 3.1.2 抽取 server 停止逻辑

新增方法：

```go
func (a *App) stopServer()
```

要求：

- 幂等；
- server 未启动时直接返回；
- server 已退出时不报错；
- 不 panic；
- 不 kill 用户手动启动的其他 server；
- 只停止当前 GUI 持有的 `a.serverCmd` 子进程。

当前 `beforeClose()` 中直接 kill process 的逻辑移动到 `stopServer()`。

#### 3.1.3 抽取窗口控制方法

新增方法：

```go
func (a *App) showMainWindow()
func (a *App) hideMainWindow()
func (a *App) quitApp()
```

职责：

##### `showMainWindow()`

- `runtime.WindowShow(a.ctx)`
- `runtime.WindowUnminimise(a.ctx)`
- 可选：短暂 AlwaysOnTop 再取消，参考前端 `notify.ts`

##### `hideMainWindow()`

- `runtime.WindowHide(a.ctx)`
- 不停止 server。

##### `quitApp()`

- `a.quitting.Store(true)`
- `a.stopServer()`
- `runtime.Quit(a.ctx)`

#### 3.1.4 改造 `beforeClose()`

逻辑：

```text
beforeClose:
  if quitting == true:
      stopServer()
      return false

  closeBehavior = readCloseBehavior()

  if closeBehavior == "tray":
      hideMainWindow()
      return true

  stopServer()
  return false
```

说明：

- 返回 `true`：阻止窗口真正关闭；
- 返回 `false`：允许 Wails 继续退出；
- 默认配置缺失或非法时必须走 `exit`，保持旧行为。

### 3.2 第二阶段：配置接入

目标：

- 增加 `ui.close_behavior`；
- 后端 API 可读写；
- GUI close 逻辑可读取该配置；
- 默认行为保持关闭即退出。

#### 3.2.1 新增配置类型

文件：

- `internal/config/config.go`

新增：

```go
type CloseBehavior string

const (
    CloseBehaviorExit CloseBehavior = "exit"
    CloseBehaviorTray CloseBehavior = "tray"
)

type UIConfig struct {
    CloseBehavior CloseBehavior `json:"close_behavior,omitempty"`
}
```

新增方法：

```go
func (b CloseBehavior) Normalize() CloseBehavior
func (b CloseBehavior) IsValid() bool
```

在 root `Config` 增加：

```go
UI UIConfig `json:"ui"`
```

在 `Default()` 设置：

```go
UI: UIConfig{
    CloseBehavior: CloseBehaviorExit,
}
```

在 `Load()` 后 normalize：

```go
cfg.UI.CloseBehavior = cfg.UI.CloseBehavior.Normalize()
```

#### 3.2.2 更新默认配置模板

文件：

- `configs/config.yaml`

新增：

```yaml
ui:
  close_behavior: "exit"
```

#### 3.2.3 系统配置 PATCH

文件：

- `internal/config/system_config.go`

新增 patch：

```go
type UIConfigPatch struct {
    CloseBehavior *CloseBehavior `json:"close_behavior,omitempty"`
}
```

`SystemConfigPatch` 增加：

```go
UI *UIConfigPatch `json:"ui,omitempty"`
```

合并逻辑：

```go
func mergeUI(dst *UIConfig, patch *UIConfigPatch)
```

要求：

- patch nil 不覆盖；
- value nil 不覆盖；
- 非法值 normalize 到默认值。

#### 3.2.4 系统配置 API response

文件：

- `internal/server/system_config.go`

增加 response DTO：

```go
type uiResponse struct {
    CloseBehavior string `json:"close_behavior"`
}
```

`systemConfigResponse` 增加：

```go
UI uiResponse `json:"ui"`
```

GET/PATCH 返回：

```go
UI: uiResponse{
    CloseBehavior: string(cfg.UI.CloseBehavior.Normalize()),
}
```

### 3.3 第三阶段：托盘基础菜单

目标：

- GUI 启动后显示托盘图标；
- 菜单包含：
  - 打开 P-Chat；
  - 新增对话；
  - 打开对话...；
  - 退出 P-Chat。

第一版可以先实现：

- 打开 P-Chat；
- 退出 P-Chat。

#### 3.3.1 优先使用 Wails 原生托盘能力

需要先确认当前 Wails v2.12.0 是否支持：

- 设置托盘图标；
- 设置 tooltip；
- 菜单项；
- 菜单项点击回调；
- Windows 打包后图标正常。

如果可用，优先使用 Wails 原生能力。

如果 Wails v2.12.0 原生托盘能力不足，再考虑：

```go
github.com/getlantern/systray
```

但 `systray` 可能和 Wails/WebView2 主事件循环有兼容风险，因此只作为备选。

#### 3.3.2 托盘图标资源

可复用资源：

- `cmd/pchat-gui/build/appicon.png`
- `cmd/pchat-gui/build/windows/icon.ico`

优先使用已有 app icon，避免新增资源管理复杂度。

#### 3.3.3 托盘菜单行为

菜单项：

```text
打开 P-Chat
────────────
退出 P-Chat
```

行为：

- 打开 P-Chat：
  - 调用 `showMainWindow()`。
- 退出 P-Chat：
  - 调用 `quitApp()`。

### 3.4 第四阶段：托盘会话事件

目标：

- 托盘菜单支持新增对话；
- 托盘菜单支持打开对话入口；
- 后续支持最近会话。

#### 3.4.1 Go 侧事件

文件：

- `cmd/pchat-gui/main.go`

托盘菜单新增：

```text
新增对话
打开对话...
```

点击后：

```go
runtime.EventsEmit(a.ctx, "tray:new-session")
runtime.EventsEmit(a.ctx, "tray:show-session-picker")
```

推荐流程：

- 先 `showMainWindow()`；
- 再 emit 事件；
- 避免前端窗口隐藏时用户看不到结果。

#### 3.4.2 前端监听事件

文件：

- `frontend/src/App.vue`
- 或新增：`frontend/src/utils/trayEvents.ts`

推荐新增封装：

```ts
export async function setupTrayEventListeners(): Promise<() => void>
```

监听：

```ts
tray:new-session
tray:switch-session
tray:show-session-picker
```

处理：

##### `tray:new-session`

```ts
await createSession()
```

##### `tray:switch-session`

```ts
if payload?.session_id:
  await switchSession(payload.session_id)
```

##### `tray:show-session-picker`

第一版：

- 只打开主窗口；
- 可不做额外 UI。

后续：

- 展开侧边栏；
- 聚焦会话搜索框；
- 或打开快速切换 modal。

注意：

- 动态 import Wails runtime；
- 检查 `window.runtime`；
- 捕获 browser preview 异常；
- 保存 off 函数；
- `onUnmounted()` 清理。

### 3.5 第五阶段：设置 UI

目标：

- 用户可在设置页切换关闭行为。

文件：

- `frontend/src/api/client.ts`
- `frontend/src/components/AppSettingsModal.vue`

#### 3.5.1 API 类型

新增：

```ts
export interface UIConfig {
  close_behavior: 'exit' | 'tray' | string
}
```

`SystemConfig` 增加：

```ts
ui: UIConfig
```

#### 3.5.2 设置状态

在 `AppSettingsModal.vue` 增加：

```ts
const sysCloseBehavior = ref<'exit' | 'tray'>('exit')
```

`loadSystemConfig()`：

```ts
sysCloseBehavior.value = normalizeCloseBehavior(sc.ui?.close_behavior)
```

`saveSystemConfig()` patch 增加：

```ts
ui: {
  close_behavior: sysCloseBehavior.value,
}
```

`resetSystemConfig()`：

```ts
sysCloseBehavior.value = 'exit'
```

#### 3.5.3 设置 UI

在系统设置 tab 中增加 collapse item：

```text
窗口行为

关闭窗口时：
  ○ 退出 P-Chat
  ○ 最小化到通知区域

说明：
选择“最小化到通知区域”后，关闭窗口不会退出应用。
P-Chat 会继续在后台运行，可通过右下角托盘图标重新打开或退出。
```

复用现有 `NRadioGroup` + `NRadioButton` 模式。

---

## 4. 推荐提交拆分

### Commit 1: Refactor GUI shutdown lifecycle

内容：

- `cmd/pchat-gui/main.go`
  - 增加 `quitting` 状态；
  - 抽取 `stopServer()`；
  - 抽取 `showMainWindow()` / `hideMainWindow()` / `quitApp()`；
  - 保持默认关闭行为不变。

验收：

- 默认点击 X 仍退出；
- server 正常停止；
- 重复调用 `stopServer()` 不 panic。

### Commit 2: Add UI close behavior config

内容：

- `internal/config/config.go`
- `internal/config/system_config.go`
- `internal/server/system_config.go`
- `configs/config.yaml`
- config 测试 / API 测试

新增：

```yaml
ui:
  close_behavior: "exit"
```

验收：

- 旧配置缺失 `ui` 时默认为 `exit`；
- GET `/api/v1/config` 返回 `ui.close_behavior`；
- PATCH `/api/v1/config` 可修改；
- 非法值 normalize 到默认值。

### Commit 3: Support hide to tray on close

内容：

- `cmd/pchat-gui/main.go`

行为：

- `close_behavior=tray` 时点击 X 隐藏窗口；
- 不停止 server；
- 返回 true 阻止关闭；
- `close_behavior=exit` 保持旧行为。

验收：

- 配置为 tray 后点击 X，窗口消失但进程仍在；
- 重新显示窗口后状态保留；
- streaming 中隐藏不打断请求。

### Commit 4: Add tray icon with open and quit actions

内容：

- 使用 Wails 原生托盘或备选 systray；
- 托盘菜单：
  - 打开 P-Chat；
  - 退出 P-Chat。

验收：

- 托盘图标显示；
- 点击打开可恢复窗口；
- 点击退出清理 GUI 和 server；
- 打包后图标可见。

### Commit 5: Add settings UI for close behavior

内容：

- `frontend/src/api/client.ts`
- `frontend/src/components/AppSettingsModal.vue`

验收：

- 设置页可切换关闭行为；
- 保存后配置持久化；
- 重启后配置仍生效；
- `vue-tsc` 和前端 build 通过。

### Commit 6: Add tray session actions

内容：

- 托盘菜单新增：
  - 新增对话；
  - 打开对话...
- Go emit：
  - `tray:new-session`
  - `tray:show-session-picker`
- 前端监听并处理。

验收：

- 隐藏到托盘后点击“新增对话”可显示窗口并创建会话；
- 点击“打开对话...”可显示窗口；
- 无 Wails runtime 的浏览器预览不报错。

---

## 5. 关键文件清单

### GUI

- `cmd/pchat-gui/main.go`
- `cmd/pchat-gui/main_test.go`
- `cmd/pchat-gui/wails.json`
- `cmd/pchat-gui/build/appicon.png`
- `cmd/pchat-gui/build/windows/icon.ico`

### 配置

- `internal/config/config.go`
- `internal/config/system_config.go`
- `internal/config/config_test.go`
- `internal/server/system_config.go`
- `internal/server/api_test.go`
- `configs/config.yaml`

### 前端

- `frontend/src/api/client.ts`
- `frontend/src/App.vue`
- `frontend/src/components/AppSettingsModal.vue`
- `frontend/src/stores/chat.ts`
- `frontend/src/utils/notify.ts`
- 可新增：`frontend/src/utils/trayEvents.ts`

---

## 6. 测试计划

### 6.1 Go 测试

```powershell
go test -count=1 ./...
```

重点：

- `CloseBehavior.Normalize()`
- 默认配置；
- PATCH `/api/v1/config`；
- `stopServer()` 幂等 helper；
- close behavior 纯逻辑 helper。

### 6.2 前端检查

```powershell
cd frontend
npx vue-tsc -b
npm run build
```

重点：

- `SystemConfig` 类型；
- 设置页表单；
- Wails event listener；
- HMR cleanup。

### 6.3 GUI 构建

```powershell
task build:gui
```

或：

```powershell
task package:gui
```

重点：

- Wails build 是否成功；
- 托盘 icon 是否打包；
- `pchat-server.exe` 是否正常拉起；
- 退出后是否清理子进程。

### 6.4 手工验收

1. 启动 `pchat-gui.exe`。
2. 确认 server 拉起，窗口正常显示。
3. 默认配置下点击 X。
4. 确认 GUI 和 server 都退出。
5. 修改设置为“最小化到通知区域”。
6. 点击 X。
7. 确认窗口隐藏，但：
   - `pchat-gui.exe` 仍在；
   - `pchat-server.exe` 仍在。
8. 右键托盘，点击“打开 P-Chat”。
9. 确认窗口恢复。
10. 右键托盘，点击“退出 P-Chat”。
11. 确认 GUI 和 server 都退出。
12. streaming 中重复测试关闭到托盘，确认任务不中断。
13. 打包后重复以上流程。

---

## 7. 风险与应对

### 7.1 Wails 托盘能力不足

风险：

- Wails v2.12.0 可能不支持完整动态托盘菜单。

应对：

- 第一版只做固定菜单；
- 如原生能力不足，再引入 `github.com/getlantern/systray`；
- 动态最近会话延后。

### 7.2 子进程残留

风险：

- 托盘退出和窗口退出路径不同，可能漏停 server。

应对：

- 所有真正退出路径统一调用 `quitApp()`；
- `quitApp()` 统一调用 `stopServer()`；
- `stopServer()` 幂等。

### 7.3 多实例问题

风险：

- 已经驻留托盘时再次启动，会出现多个 GUI、多个托盘图标、多个 server。

应对：

- 本轮先列为已知风险；
- 后续新增 single instance lock；
- 第二实例启动时通知已有实例显示窗口，然后自行退出。

### 7.4 前端事件丢失

风险：

- Go emit tray event 时，前端还没有完成初始化。

应对：

- 托盘事件先 `showMainWindow()`；
- 前端 listener 在 `App.vue` 初始化后注册；
- 第一版“打开对话...”即使事件丢失，也至少显示主窗口；
- 后续可加 pending action 队列。

### 7.5 用户误以为已退出

风险：

- 用户点击 X 后程序仍驻留，可能误解。

应对：

- 默认仍为 `exit`；
- 设置页明确说明；
- 后续可加首次关闭到托盘通知。

---

## 8. 本轮推荐落地范围

本轮优先实现：

1. `ui.close_behavior` 配置；
2. GUI close behavior 读取配置；
3. 关闭到托盘；
4. 托盘打开；
5. 托盘退出；
6. 设置页切换关闭行为。

本轮可暂缓：

1. 最近会话动态菜单；
2. 单实例保护；
3. shell extension；
4. URL protocol；
5. 开机自启。

---

## 9. 完成定义

当以下条件全部满足时，本轮可视为完成：

- 默认配置下，关闭窗口仍退出应用；
- 设置为托盘模式后，关闭窗口隐藏到托盘；
- 隐藏后 server 继续运行；
- 托盘菜单可重新打开窗口；
- 托盘菜单可真正退出；
- 退出后不残留 GUI 拉起的 server；
- 设置页可切换并持久化关闭行为；
- Go 测试通过；
- 前端类型检查和构建通过；
- GUI build/package 通过；
- Windows 手工验收通过。
