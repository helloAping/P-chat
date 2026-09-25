# P-Chat 多环境共存与端口分配

## 目标

同一台电脑可以同时运行正式版和源码开发版。两者的数据、进程身份、窗口身份和后端地址互相隔离；端口冲突只影响当前启动尝试，不得导致误连、覆盖数据或结束另一个环境的进程。

## 领域边界

- **数据目录（data home）**：配置、SQLite、日志、技能、知识库和上传文件的隔离边界。
- **运行配置（runtime profile）**：由数据目录规范路径派生稳定 `profile_id`，并带一个可读名称（默认 `prod` / `dev`）。名称不参与身份计算，改名不能绕过同目录单实例约束。
- **进程实例（runtime instance）**：每次 server 启动生成随机 `instance_id`。它只标识本次进程生命周期。

不同 `profile_id` 可以共存；同一个 `profile_id` 的桌面 GUI 仍保持单实例。

## 端口策略

| 场景 | 绑定策略 | 冲突行为 |
| --- | --- | --- |
| 正式 GUI | 服务端直接尝试 `15150-15159` | 顺序选择首个可绑定端口；全部占用时回退到 OS 临时端口 |
| dev / test GUI | `PCHAT_PORT=0` | 由 OS 原子分配临时端口 |
| CLI 拉起 server | 默认 `PCHAT_PORT=0` | 由 OS 原子分配临时端口 |
| 独立 server | 未指定环境变量时使用 `config.json` 的端口 | 端口被占用则明确启动失败；不静默连接其他实例 |

端口选择和监听必须是同一次 `net.Listen`。禁止“探测空闲端口 → 关闭 → 子进程重新绑定”，因为两个并发测试可能在中间窗口选中同一端口。

## 启动握手

1. 父进程确定 data home，派生 `profile_id`，生成随机 `instance_id` 和唯一临时握手文件路径。
2. 父进程向 server 传递 `PCHAT_DATA_HOME`、`PCHAT_PROFILE`、`PCHAT_INSTANCE_ID`、`PCHAT_RUNTIME_FILE`，以及端口策略。
3. server 先持有 listener，再原子写入 `{profile_id, profile_name, instance_id, pid, address, base_url}`。
4. 父进程校验 profile、instance 和 PID，随后访问 `/api/v1/health` 做第二次身份校验。
5. 只有两次校验都通过，GUI 才设置代理地址并显示窗口。

因此，端口上即使恰好已有另一个 P-Chat，也不能仅凭 HTTP 200 被当作本次启动的后端。

## 进程与前端约束

- Windows GUI mutex、tray window class 和非默认 WebView2 user-data 目录都包含 `profile_id`：dev 与 prod 可同时打开，且不会共享 localStorage；同一数据目录不能重复打开 GUI。默认正式环境继续使用 Wails 历史目录，避免升级后丢失已有前端状态。
- `task build:dev` 只允许停止当前仓库 `dev-bin/` 下的可执行文件；禁止按 `pchat-gui` / `pchat-server` 进程名全局结束。
- `task cli`、`task server`、`task gui` 和 `task build:dev` 显式使用 `PCHAT_PROFILE=dev` 与 `dev-bin/.p-chat`。
- 前端 REST、下载和 SSE 地址都从 GUI 注入的动态后端地址解析。纯浏览器开发可显式设置 `VITE_PCHAT_BACKEND`，不再内置固定代理端口。
- `/api/v1/health` 返回 `profile_id`、`profile_name`、`instance_id` 和 `pid`，供启动方和诊断工具验证目标。

## 验收标准

- 安装版运行时启动 `task gui` 或 `task build:dev`，两个窗口均可使用，标题能区分 dev，数据分别落到各自 data home。
- `15150-15159` 部分或全部占用时，正式 GUI 仍能启动；测试和 dev 并发启动时使用不同临时端口。
- 伪造或陈旧握手文件、错误 profile/instance/PID、端口上另一个 P-Chat 的健康响应都不能通过启动校验。
- 重新构建开发版不会结束安装目录中的正式版进程。
- 不需要修改现有用户配置或迁移 SQLite；运行握手文件位于 OS 临时目录，并在父进程退出时清理。

## 已知边界

浏览器扩展的自动扫描仍只覆盖正式端口范围和历史兼容端口。dev/test 使用临时端口时，需要从启动日志取得实际地址并在扩展中手动填写；扩展地址不参与桌面 GUI 的启动握手。
