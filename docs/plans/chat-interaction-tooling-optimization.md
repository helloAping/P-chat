# Chat Interaction and Tooling Optimization Plan

## 背景

本轮优化聚焦对话过程中会打断用户或阻塞 agent loop 的交互点：LLM 提问、todo 展示、命令执行、沙箱授权。目标不是重做整体架构，而是在现有 SSE parts、session meta、tool confirm 机制上补齐缺口，让对话继续性更稳定，授权语义更符合用户选择。

## 范围

1. `question` 交互从全局全屏弹窗改为只覆盖当前对话区域。
2. `question` 单选、多选均支持用户自定义输入。
3. 梳理并修正 `question` 超时后的前端残留状态。
4. `todo_list` 在对话框上方展示时只读，不允许用户点击内容编辑。
5. `exec_command` 遇到 `node`、`java`、`npm run dev` 等持久运行命令时不能卡住整轮对话。
6. 沙箱确认要正确尊重权限级别，并支持“始终允许”同类命令。

## 当前问题

### question

- 组件挂在 `App.vue` 顶层，`NModal` mask 覆盖整个应用，用户无法操作侧边栏、设置等非对话区域。
- 自定义输入依赖 LLM 提供一个固定“其他”选项；如果 LLM 没给，用户没有自由输入出口。
- 后端等待答案有 5 分钟超时，agent 工具上下文也有 5 分钟超时。超时后后端会返回工具错误，但前端 `pendingQuestion` 只在提交、切换项目、删除会话等路径清理，可能留下过期弹窗。

### todo_list

- `TodoPanel.vue` 内维护 `editingId` / `editingContent`，支持双击任务内容进入 `NInput` 编辑。
- 编辑只修改前端本地 `state.sessionTodos` 并插入 system message，和“todo 状态由 LLM / todo_write 工具负责”的模型不一致。
- 该区域处于输入框上方，更应作为当前执行计划展示，不应产生隐式用户指令。

### exec_command

- 内置 `exec_command` 通过 shell 启动命令后读取 stdout/stderr，再 `Wait()` 等待进程退出。
- 对 `node`、`java`、`vite`、`npm run dev`、本地 server 等长期进程，工具会一直占用当前 round，直到 5 分钟工具上下文超时或用户停止。
- Windows shell 启动的子进程还可能在父 context 超时后残留，造成“对话卡住但实际服务还在跑”的体验。

### 沙箱授权

- session meta 已有 `permission_level: ask | auto | full`，agent 的普通确认分支也有跳过逻辑。
- 用户在确认等待期间修改权限时，正在等待的 pending confirm 不会重新评估，仍然要求点弹窗。
- 自门控工具或浏览器类工具使用的确认 emitter 与 agent 普通确认分支可能没有统一复用权限级别。
- 前端确认窗只有“拒绝 / 允许一次”，缺少“始终允许”用于同类命令或同类目标路径。

## 目标行为

### question

- 弹窗视觉上只遮罩 `ChatWindow` 区域，不盖住侧边栏和顶部外壳。
- 每个问题都提供一个内置“自定义”选项，不依赖 LLM options。
- 单选：选择自定义后答案为输入框内容。
- 多选：自定义输入可与其他选项并存，提交时合并为同一个答案字符串。
- 收到 `done` / `error` / session 进入 idle 且仍有未答 pending question 时，清理前端 pending 状态，并把对应 question part 标记为 `error`。

### todo_list

- todo dock 只展示任务内容和状态。
- 点击任务内容不进入编辑态，不写本地 todo，不插入 system message。
- 展示仍保留展开、收起、进度、状态标签。

### exec_command

- 保留 `exec_command` 作为短命令入口：运行测试、构建、一次性 shell 命令仍同步返回。
- 增加持久命令识别：明显的 dev server / watch / java 服务 / node server 等命令，不用同步 `Wait()` 卡住对话。
- 对持久命令返回后台进程 id、启动状态、最近输出预览，并提示使用进程工具读取或停止。
- 增加后台进程管理工具：启动、读取输出、停止、列表。只管理 P-Chat 自己启动的进程。

### 沙箱授权

- `ask`：遇到 confirm 弹窗询问。
- `auto`：自动批准 confirm 级别请求，不弹窗；仍阻止 block 级别请求。
- `full`：跳过沙箱确认和阻止逻辑，仅保留工具内部必要安全防护。
- 用户在 pending confirm 期间将权限改成 `auto` 或 `full`，当前 pending confirm 应自动通过并关闭。
- 确认窗新增“始终允许”。本轮先做 session 级同类规则，不写入用户全局 config：
  - exec：按规范化命令前缀或完整命令记录。
  - file/browser：按 tool + pathClass + resolvedPath 记录。
- 后续可扩展为持久化 allowlist，并在设置页展示、撤销。

## 改动计划

1. 新增本报告文档，作为本轮实现依据。
2. 修改 `TodoPanel.vue`，移除编辑状态、编辑函数和 `NInput` 分支。
3. 扩展 confirm 协议：
   - 前端 `ConfirmResponse` 增加 `action`。
   - 后端 `ConfirmResponse` 兼容旧的 `approved`，新增 always allow 语义。
   - agent / tool confirm 等待结构返回 action，而非单纯 bool。
4. 补 session 级 allow 规则：
   - 后端运行时内存保存。
   - agent confirm 前先查 allow rule。
   - 用户点“始终允许”后立即批准当前请求，并保存规则。
5. 修正权限切换：
   - 前端更新 `permission_level` 失败要 toast。
   - 切到 `auto/full` 时主动批准当前 pending confirm。
   - 后端保持 send / regen / IM bridge 读取最新 meta。
6. exec 后台进程：
   - 增加 process manager。
   - 注册 `start_process` / `read_process_output` / `stop_process` / `list_processes`。
   - `exec_command` 识别持久命令时转后台启动。
7. 调整 `QuestionModal`：
   - 从 `App.vue` 顶层移到 `ChatWindow.vue` 内。
   - 用局部 overlay 包裹，不遮盖整个 app。
   - 内置自定义选项，单选多选都支持。
   - store 增加超时/结束清理 helper。
8. 验证：
   - Go targeted tests：`go test -count=1 ./internal/tool ./internal/server ./internal/agent`。
   - 前端类型检查：`cd frontend && npx vue-tsc -b`。
   - 如时间允许再跑 `go test -count=1 ./...` 和前端 build。

## 风险与边界

- 后台进程只管理 P-Chat 自己启动的进程，不扫描或 kill 用户已有进程。
- “始终允许”首版只做 session 级运行时规则，不跨 app 重启持久化，避免误把危险授权写入用户配置。
- `full` 权限不等于关闭所有工具内部保护，例如 upload 目录、防二进制污染、web_fetch loopback 限制等仍可保留。
- question 超时由后端 5 分钟控制，本轮前端先做状态清理；后续可把 timeout 暴露成配置项。

## 追加修复：消息链接外部打开

### 问题

聊天消息通过 Markdown 渲染后，URL 会成为 `<a>` 链接。当前 Wails WebView 对链接点击使用默认导航行为，导致用户点击链接后在 P-Chat 内部 WebView 直接跳转，聊天界面被目标网页铺满，且缺少返回、退出等恢复入口。

### 目标行为

- 用户点击消息中的 `http://` 或 `https://` 链接时，不在 P-Chat WebView 内跳转。
- 链接交给用户系统默认浏览器打开。
- 继续保留 Markdown 代码块复制、下载按钮的点击委托行为。
- 暂不允许消息内容直接打开 `file://`、自定义协议等非 http(s) URL，避免扩大安全面。

### 改动点

- `cmd/pchat-gui/main.go`：新增 Wails 方法 `OpenURL(rawURL string)`，校验仅允许 http(s)，并使用系统默认浏览器打开。
- `frontend/src/api/client.ts`：新增 `openExternalURL(url)`，Wails 环境调用 `OpenURL`，普通浏览器环境 fallback 到 `window.open(..., '_blank', 'noopener,noreferrer')`。
- `frontend/src/components/MessageBubble.vue`：为 Markdown 容器增加链接点击委托，拦截外部链接默认行为后调用 `openExternalURL`；非链接点击继续走原代码块工具栏处理。
- `frontend/wailsjs/go/main/App.{js,d.ts}`：同步补充 `OpenURL` 绑定声明，保证当前工作区类型检查通过。

### 验证

- `frontend`: `npx.cmd vue-tsc -b`
- `cmd/pchat-gui`: `go test -count=1 .`
