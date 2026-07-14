# 浏览器控制功能开发计划

> **状态**: 规划中  
> **创建日期**: 2026-07-13  
> **预估工期**: 11 个工作日  
> **优先级**: High

---

## 1. 需求说明

### 1.1 背景

用户希望 P-Chat 能够控制真实浏览器执行自动化任务：
- 网页导航、点击、输入、滚动等操作
- 获取页面内容和状态
- 执行 JavaScript 代码
- 管理标签页

### 1.2 设计决策

| 问题 | 决策 | 理由 |
|------|------|------|
| 截图返回格式 | base64 直接返回 LLM | 保留视觉信息，支持多模态理解 |
| 多浏览器支持 | 支持（每个浏览器独立 ID） | 用户可能同时操作多个页面 |
| CDP 模式 | 暂不需要 | 扩展模式已满足需求，避免复杂度 |
| 扩展分发 | .zip 压缩包侧载 | 无需 Chrome Web Store 发布 |
| 工具动态注册 | 仅连接时注册 | 避免 LLM 调用不可用工具浪费 turn |

### 1.3 功能范围

**必须实现**:
- WebSocket Hub 管理多浏览器连接
- 15 个浏览器操控工具（navigate/click/type/screenshot 等）
- Chrome Manifest V3 扩展（background + content + popup）
- 前端"浏览器控制"设置 Tab
- 截图 base64 在消息中渲染
- API 端点（状态查询、配置管理、WebSocket）

**不在范围内**:
- CDP 直接控制模式
- Firefox/Safari 扩展（仅 Chrome/Edge/Chromium）
- Chrome Web Store 自动发布
- 录制/回放功能

---

## 2. 技术架构

### 2.1 整体架构图

```
┌─────────────────────────────────────────────────────────┐
│  Chrome 浏览器 (多个实例)                                │
│  ┌────────────────────────────────────────────────┐    │
│  │  Browser Extension (MV3)                        │    │
│  │  ├─ background.js (Service Worker)             │    │
│  │  │   ├─ WebSocket Client                       │    │
│  │  │   ├─ chrome.tabs.* API                      │    │
│  │  │   └─ chrome.debugger (可选)                 │    │
│  │  ├─ content.js (页面内 DOM 操控)                │    │
│  │  └─ popup.html (状态显示/手动控制)             │    │
│  └────────────────────────────────────────────────┘    │
└─────────────────────┬───────────────────────────────────┘
                      │ WebSocket (wss://localhost:xxxxx)
                      ▼
┌─────────────────────────────────────────────────────────┐
│  pchat-server                                            │
│  ┌──────────────────────────────────────────────────┐   │
│  │  internal/browser/                                │   │
│  │  ├─ hub.go (BridgeHub)                           │   │
│  │  │   ├─ clients: map[string]*BrowserClient       │   │
│  │  │   ├─ register/unregister channels             │   │
│  │  │   └─ run() goroutine (select loop)            │   │
│  │  ├─ protocol.go (JSON-RPC 2.0)                   │   │
│  │  │   ├─ Request{ID,Method,Params}                │   │
│  │  │   └─ Response{ID,Result,Error}                │   │
│  │  ├─ tools.go (15个工具, 动态注册)                │   │
│  │  │   ├─ RegisterBrowserTools(registry, hub)      │   │
│  │  │   └─ UnregisterBrowserTools(registry)         │   │
│  │  └─ manager.go (生命周期)                        │   │
│  │      ├─ Start() / Stop()                         │   │
│  │      └─ Config persistence                       │   │
│  └──────────────────────────────────────────────────┘   │
│                                                          │
│  ┌──────────────────────────────────────────────────┐   │
│  │  internal/agent/                                  │   │
│  │  └─ ReAct loop → tool dispatcher → browser tools │   │
│  └──────────────────────────────────────────────────┘   │
│                                                          │
│  ┌──────────────────────────────────────────────────┐   │
│  │  internal/server/handler.go                       │   │
│  │  ├─ GET  /api/v1/browser/status                  │   │
│  │  ├─ GET  /api/v1/browser/list                    │   │
│  │  ├─ POST /api/v1/browser/config                  │   │
│  │  └─ WS   /api/v1/browser/ws                      │   │
│  └──────────────────────────────────────────────────┘   │
└─────────────────────────────────────────────────────────┘
```

### 2.2 数据流

**工具调用流**:
```
LLM 决策调用 browser_click
  → agent.ChatWithTools() ReAct loop
    → tool dispatcher 派发浏览器工具
      → handler 构造 Request{method:"browser/click", params:{ref:"#btn"}}
        → Hub.SendCommand(browserID, req)
          → BrowserClient.conn.WriteJSON(req)
            → 扩展 background.js 收到请求
              → chrome.tabs.sendMessage(tabId, {action:"click", ref:"#btn"})
                → content.js 执行 DOM 点击
              ← {success:true}
            ← Response{id:1, result:{success:true}}
          ← pending[id] channel 收到响应
        ← handler 返回 CallResult{Content:"点击成功: [登录按钮]"}
      ← tool_result 走 LLM（下轮可用）
    ← SSE type=tool 推送前端
  ← 前端 MessageBubble 渲染 tool part
```

**截图流** (base64):
```
LLM 调 browser_screenshot
  → Hub.SendCommand → 扩展 chrome.tabs.captureVisibleTab()
    → 返回 JPEG base64 (quality 80)
  ← CallResult{Content: "data:image/jpeg;base64,/9j/4AAQSkZJRg..."}
  → LLM 收到 image_url 格式 (OpenAI vision)
  → SSE → 前端 tool part 识别 base64 前缀 → <img src="data:...">
  → 历史消息第二轮后 strip: "[截图省略 - 节省 tokens]"
```

---

## 3. 任务分解

### 3.1 P1: WebSocket Hub + 协议定义

**目标**: 建立浏览器连接管理基础设施

**任务清单**:

| ID | 任务 | 预估 | 文件 |
|----|------|------|------|
| P1.1 | 定义 JSON-RPC 2.0 协议结构 | 0.5h | `internal/browser/protocol.go` |
| P1.2 | 实现 BrowserClient 结构 | 1h | `internal/browser/protocol.go` |
| P1.3 | 实现 BridgeHub (register/unregister/send) | 3h | `internal/browser/hub.go` |
| P1.4 | 配置结构定义 (BrowserConfig) | 0.5h | `internal/browser/manager.go` |
| P1.5 | Manager 生命周期管理 (Start/Stop) | 2h | `internal/browser/manager.go` |

**执行方式**:
1. 参考 `internal/mcp/manager.go` 的 `Manager` + `client` 模式
2. 复用 `gorilla/websocket` 库（已在 go.mod）
3. Hub 的 `run()` 使用 `select` loop 处理 register/unregister/send

**依赖**: 无

**验收标准**:
- `BridgeHub.SendCommand(id, req) <-chan Response` 能发送并接收响应
- 多连接管理（add/remove）线程安全
- 单元测试: `hub_test.go` 覆盖并发场景

**注意点**:
- **gorilla/websocket 已在 go.mod 中**（被 MCP SSE transport 间接使用），无需新增依赖
- **Hub.run() 必须用 select 而非 mutex**（避免 channel 阻塞），参见 `serverproc` 的 `select` 模式
- **pending map 用 mutex 保护**（sendCommand 与 readLoop 并发访问）
- **超时控制**：默认 30s，可配置

---

### 3.2 P2: 浏览器工具注册

**目标**: 15 个工具动态注册到 LLM

**任务清单**:

| ID | 任务 | 预估 | 备注 |
|----|------|------|------|
| P2.1 | 导航类 (navigate/close) | 1h | 最基础 |
| P2.2 | 交互类 (click/type/press_key/scroll/hover/select) | 2.5h | 共 7 个 |
| P2.3 | 高级交互 (drag/evaluate/file_upload) | 1.5h | 需要扩展配合 |
| P2.4 | 信息获取 (snapshot/screenshot/find) | 2h | screenshot 走多模态 |
| P2.5 | 标签管理 (tabs) | 1h | list/new/close/select |
| P2.6 | 动态注册逻辑 (RegisterBrowserTools/Unregister) | 1h | 参考 MCP 的 registerMCPTools |

**执行方式**:
```go
// internal/browser/tools.go
func RegisterBrowserTools(r *tool.Registry, hub *BridgeHub) {
    if !hub.HasActiveConnection() { return }  // 条件注册
    tools := buildBrowserToolDefs()           // 15个 Tool{Name,Desc,Params}
    handlers := buildBrowserHandlers(hub)      // 15个 ToolHandler
    for i, t := range tools {
        r.Register(t, handlers[i])
    }
}

func UnregisterBrowserTools(r *tool.Registry) {
    for _, t := range browserToolDefs {
        r.Unregister(t.Name)
    }
}
```

**依赖**: P1 完成

**验收标准**:
- `tool.Registry.List()` 输出包含 `browser_*`（有连接时）
- LLM 能调用 `browser_navigate` 并收到结果
- 多浏览器场景下 `browser_id` 路由正确

**注意点**:
- **工具描述要详尽**（LLM 看不到代码，只能看描述），参考 `internal/tool/registry.go:326-375` 的 `question` 工具描述
- **参数校验前置**：在 handler 内 unmarshal 后立即检查必填字段，返回 `CallResult{IsError: true}`
- **错误永远返回 (*CallResult, nil)**：`error` 返回值仅用于致命错误（终止整个 agent loop），参见 `agent.go:1880-1895`
- **`browser_evaluate` 默认只读**：除非用户在配置中明确开启 `browser.allow_write_js: true`

---

### 3.3 P3: Chrome 扩展开发

**目标**: Manifest V3 扩展，实现 DOM 操控和页面信息获取

**任务清单**:

| ID | 任务 | 预估 | 文件 |
|----|------|------|------|
| P3.1 | manifest.json + icons | 0.5h | `browser-extension/manifest.json` |
| P3.2 | background.js WebSocket 客户端 | 2h | `browser-extension/background.js` |
| P3.3 | content.js DOM 操控 + 快照生成 | 3h | `browser-extension/content.js` |
| P3.4 | content.css 元素高亮 (可选) | 0.5h | `browser-extension/content.css` |
| P3.5 | popup.html + popup.js 状态面板 | 1h | `browser-extension/popup.*` |

**执行方式**:
```js
// background.js (Service Worker)
const socket = new WebSocket(serverURL);
socket.onmessage = async (event) => {
    const req = JSON.parse(event.data);
    const result = await handleCommand(req.method, req.params);
    socket.send(JSON.stringify({ id: req.id, result }));
};

// 路由命令到 chrome.tabs API 或 content.js
function handleCommand(method, params) {
    if (method === "browser/navigate") {
        return chrome.tabs.update(params.tabId, { url: params.url });
    }
    if (method === "browser/click") {
        return chrome.tabs.sendMessage(params.tabId, { action: "click", ref: params.ref });
    }
    // ...
}

// content.js
chrome.runtime.onMessage.addListener((msg, sender, respond) => {
    if (msg.action === "click") {
        const el = document.querySelector(`[data-pchat-ref="${msg.ref}"]`);
        if (el) { el.click(); respond({success: true}); }
        else { respond({error: "element not found"}); }
    }
});

// 页面快照：遍历可点击元素，生成 ref 编号
function generateSnapshot() {
    const refs = document.querySelectorAll("a,button,input,select,textarea");
    const snapshot = [];
    for (const el of refs) {
        const ref = `${el.tagName.toLowerCase()}-${snapshot.length}`;
        el.setAttribute("data-pchat-ref", ref);
        snapshot.push({ ref, tag: el.tagName, text: el.textContent?.slice(0,50), role: el.getAttribute("role") });
    }
    return snapshot;
}
```

**依赖**: P1 协议定义（但可并行开发）

**验收标准**:
- 扩展安装成功（`chrome://extensions` 加载已解包）
- 弹窗显示连接状态（绿/红）
- 能点击按钮、输入文本、滚动页面
- 截图返回 base64 字符串
- JSON-RPC 协议与 server 端匹配

**注意点**:
- **Manifest V3 限制**：Service Worker 5 分钟超时，WebSocket 可能断开 → 用 `chrome.alarms` 心跳保活
- **权限清单**：`tabs`, `activeTab`, `scripting`, `storage`
- **CSP 限制**：`manifest.json` 的 `content_security_policy` 要允许 `wss://` 连接
- **ref 编号**：每次快照生成新的 data attribute，避免跨页冲突
- **错误边界**：try-catch 包裹所有 DOM 操作，返回结构化错误给 server

**并行开发策略**:
- P3.1 完成后立即开始 background.js 开发（用 mock server 测试）
- P2 工具开发完成后端到端联调

---

### 3.4 P4: API 路由注册

**目标**: HTTP API + WebSocket 端点

**任务清单**:

| ID | 任务 | 预估 | 文件 |
|----|------|------|------|
| P4.1 | WebSocket upgrade handler | 1.5h | `internal/server/browser_handler.go` |
| P4.2 | REST API (status/list/config) | 1.5h | `internal/server/browser_handler.go` |
| P4.3 | 路由注册到 Gin | 1h | `internal/server/server.go` |
| P4.4 | Handler struct 注入 browser.Manager | 0.5h | `internal/server/handler.go` |

**执行方式**:
```go
// internal/server/browser_handler.go (新文件)
func (h *Handler) BrowserWebSocket(c *gin.Context) {
    if !h.browserMgr.IsEnabled() {
        c.JSON(403, gin.H{"error": "browser control disabled"})
        return
    }
    conn, err := upgrader.Upgrade(c.Writer, c.Request, nil);
    if err != nil { c.JSON(500, gin.H{"error": err.Error()}); return }
    
    client := &browser.BrowserClient{conn: conn, send: make(chan []byte, 256)}
    h.browserHub.Register(client)
    defer h.browserHub.Unregister(client)
    
    go client.writePump()
    client.readPump()
}

func (h *Handler) BrowserList(c *gin.Context) {
    list := h.browserHub.GetClients()
    c.JSON(200, gin.H{"browsers": list})
}
// ...

// internal/server/server.go (添加路由)
api.GET("/browser/ws", h.BrowserWebSocket)
api.GET("/browser/list", h.BrowserList)
api.GET("/browser/status", h.BrowserStatus)
api.POST("/browser/config", h.UpdateBrowserConfig)
```

**依赖**: P1 (Hub), P2 (Manager)

**验收标准**:
- `curl -i -N -H "Connection: Upgrade" ... /api/v1/browser/ws` 成功 upgrade
- `GET /browser/list` 返回活跃连接数组
- `POST /browser/config` 持久化到 `~/.p-chat/config.json`
- 前端 API client 能调用这些端点

**注意点**:
- **WebSocket 路由放在 `/api/v1/browser/ws`**：与 REST API 同组，避免额外端口
- **升级失败返回 500**：不要用 `c.AbortWithStatusJSON()`（WebSocket 库接管后会乱）
- **心跳**：每 30s 发送 pong（gorilla Upgrader 的 `SetPongHandler`）
- **配置写入**：调用 `config.Save()` 原子替换（`temp file + rename`），参见 `config.go:598-620`
- **CORS**：WebSocket 同域，无需配置 CORS

**新文件**: `internal/server/browser_handler.go`（独立文件，避免 `handler.go` 继续膨胀）

---

### 3.5 P5: 前端"浏览器控制"设置 Tab

**目标**: 用户可在设置中启用/配置浏览器控制

**任务清单**:

| ID | 任务 | 预估 | 文件 |
|----|------|------|------|
| P5.1 | API client 函数 (5个) | 0.5h | `frontend/src/api/client.ts` |
| P5.2 | AppSettingsModal 添加 Tab | 0.5h | `frontend/src/components/AppSettingsModal.vue` |
| P5.3 | Tab 内容 (开关/列表/指引/测试) | 3h | `frontend/src/components/AppSettingsModal.vue` |
| P5.4 | 实时连接状态展示 | 1.5h | `frontend/src/components/AppSettingsModal.vue` |

**执行方式**:
```ts
// frontend/src/api/client.ts
export interface BrowserInfo {
  id: string
  name: string
  url: string
  connected_at: string
  tabs_count: number
}

export const listBrowsers = () =>
  jsonFetch<{ browsers: BrowserInfo[] }>('/api/v1/browser/list')

export const getBrowserStatus = () =>
  jsonFetch<{ enabled: boolean; count: number }>('/api/v1/browser/status')

export const updateBrowserConfig = (enabled: boolean) =>
  jsonFetch<{ok: boolean}>('/api/v1/browser/config', {
    method: 'POST',
    body: JSON.stringify({ enabled }),
  })
```

```vue
<!-- AppSettingsModal.vue 新增 tab -->
<script setup lang="ts">
const tab = ref<'...' | 'browser'>('...')
const browserState = ref({ enabled: false, browsers: [] })

watch(tab, (v) => {
  if (v === 'browser') {
    const status = await getBrowserStatus()
    browserState.value.enabled = status.enabled
    browserState.value.browsers = await listBrowsers()
  }
})
</script>

<template>
  <NTabPane name="browser" tab="浏览器控制">
    <NSwitch v-model:value="browserState.enabled" @update:value="updateBrowserConfig" />
    <div v-for="b in browserState.browsers" :key="b.id" class="browser-item">
      <span>{{ b.name }} - {{ b.url }}</span>
      <NTag :type="b.connected_at ? 'success' : 'error'">
        {{ b.connected_at ? '在线' : '离线' }}
      </NTag>
    </div>
    <NAlert title="安装扩展" type="info">
      1. 下载 browser-extension.zip<br>
      2. 解压到任意目录<br>
      3. Chrome → chrome://extensions → 开发者模式 → 加载已解包
    </NAlert>
  </NTabPane>
</template>
```

**依赖**: P4 (API 端点)

**验收标准**:
- 设置面板出现"浏览器控制"Tab
- 开关能启用/禁用（写入 `~/.p-chat/config.json → browser.enabled`）
- 连接列表实时更新（5s 轮询或 WebSocket 推送）
- 安装指引清晰可执行

**注意点**:
- **Tab 类型 union 修改**：`line 170` 的 `tab` ref 类型要添加 `'browser'`
- **懒加载**：`watch(tab, ...)` 仅在切换到 browser 时加载数据，避免首次打开设置弹窗耗时
- **列表实时更新**：用 `setInterval` 3s 轮询 `/browser/list`（简单但有效），或未来升级为 WebSocket 推送
- **安装指引步骤**：截图 + 文字说明，避免纯文字
- **错误处理**：API 失败时显示 NAlert 而非空白页

---

### 3.6 P6: 截图 base64 渲染

**目标**: 截图在消息气泡中正确显示

**任务清单**:

| ID | 任务 | 预估 | 文件 |
|----|------|------|------|
| P6.1 | 扩展 MessagePart 支持 image | 1h | `frontend/src/api/client.ts` |
| P6.2 | tool result 中识别 base64 前缀 | 1h | `frontend/src/stores/chat.ts` |
| P6.3 | 渲染 `<img src="data:...">` | 0.5h | `frontend/src/components/MessageBubble.vue` |
| P6.4 | 历史消息去重 (stripImageContent) | 1h | `internal/agent/attachment.go` |

**执行方式**:
```ts
// frontend/src/api/client.ts
export type MessagePart =
  | { kind: 'text'; text: string }
  | { kind: 'thinking'; text: string; streaming?: boolean }
  | { kind: 'tool'; ... result?: string; ... }
  | { kind: 'sub_agent'; ... }
  | { kind: 'question'; ... }
  | { kind: 'image'; data: string; alt?: string }  // 新增

// frontend/src/stores/chat.ts
function appendToolResult(tool: MessagePart, result: string) {
  // 识别 base64 image
  if (result.startsWith('data:image/')) {
    tool.result = '[截图已生成]'
    tool.image = { data: result, alt: '截图' }
  } else {
    tool.result = result
  }
}

// frontend/src/components/MessageBubble.vue
<template>
  <div v-if="part.kind === 'tool'">
    <!-- ... -->
    <img v-if="part.image" :src="part.image.data" :alt="part.image.alt" class="screenshot" />
  </div>
</template>
```

**历史去重** (Go端):
```go
// internal/agent/attachment.go:stripImageContent()
// 第二轮后 base64 截图替换为占位
func stripImageContent(msgs []llm.ChatMessage) []llm.ChatMessage {
    for i, m := range msgs {
        if m.Type == llm.TypeText && strings.HasPrefix(m.Content, "data:image/") {
            msgs[i].Content = "[截图省略 - 节省 tokens]"
        }
    }
    return msgs
}
```

**依赖**: P2 (screenshot 工具)、P5 (前端框架已就绪)

**验收标准**:
- tool part 显示截图缩略图
- 点击可放大查看
- 第二轮后历史消息不含 base64（节省 tokens）
- LLM 能"看到"截图并正确描述内容

**注意点**:
- **base64 体积**：JPEG quality 80 时单图 ~50-100KB，多轮累积需控制上下文窗口
- **前端渲染性能**：用 `loading="lazy"` + `max-height: 300px` 避免首屏卡顿
- **stripImageContent 已有实现**：参考 `internal/agent/attachment.go:265-275`，只需扩展支持 `data:image/` 前缀
- **LLM 多模态格式**：OpenAI 用 `image_url.content[0].image_url`，Anthropic 用 `content[].type: "image"`，adapter 层已处理 base64 → 多模态格式转换
- **错误处理**：若 LLM 不支持多模态（`vision==false`），工具返回 `[截图已生成，当前模型不支持视觉输入]`

---

### 3.7 P7: 集成测试 + 扩展打包

**目标**: 端到端验证 + 交付物

**任务清单**:

| ID | 任务 | 预估 | 文件 |
|----|------|------|------|
| P7.1 | Go unit tests (Hub + tools) | 1.5h | `internal/browser/*_test.go` |
| P7.2 | 手动 E2E 测试 (扩展 + server) | 2h | — |
| P7.3 | 扩展 ZIP 打包脚本 | 0.5h | `scripts/package-browser-ext.ps1` |
| P7.4 | 文档编写 | 1h | `docs/browser-control.md` |
| P7.5 | README 更新 | 0.5h | `README.md` |

**执行方式**:
```powershell
# scripts/package-browser-ext.ps1
$dst = "build/browser-extension.zip"
Compress-Archive -Path "browser-extension/*" -DestinationPath $dst -Force
Write-Host "Extension packed: $dst"
```

```markdown
# docs/browser-control.md
## 简介
控制 Chrome 浏览器执行自动化任务...

## 安装扩展
1. 下载 `build/browser-extension.zip`
2. 解压到任意目录（如 `D:\extensions\pchat-browser`）
3. Chrome 打开 `chrome://extensions`
4. 开启"开发者模式"
5. 点击"加载已解包扩展"，选择解压目录
6. 扩展弹窗输入 server 地址 `http://localhost:8960`

## 使用示例
...
```

**依赖**: 全部前置任务

**验收标准**:
- `go test ./internal/browser` 全部通过
- `task build:all` 不报错
- `build/browser-extension.zip` 生成
- 用户按文档能完成安装和基本操作

**注意点**:
- **测试数据**：`hub_test.go` 用 `httptest` + mock WebSocket 连接，不依赖真实扩展
- **E2E 测试范围**：仅覆盖 happy path（连接 → 导航 → 点击 → 截图），异常路径留给单元测试
- **build 目录已 gitignore**：打包产物不入库
- **文档语言**：中文，与项目其他文档一致

---

## 4. 依赖关系

```
P1 (Hub+协议) ──────┬── P2 (工具)
                   │
                   └── P3 (扩展) ── P6 (截图渲染)
                                      │
P4 (API) ──────────────────────────────┘
                   │
P5 (前端 Tab) ─────┘
                   │
                   └── P7 (测试打包)
```

**关键路径**: `P1 → P2 → P3 → P6 → P7`  
**可并行**: `P3 + P2`（协议定义完成后并行）  
**阻塞点**: `P1 协议定义`（P2 和 P3 都依赖）— 需 0.5h 完成 `protocol.go` 后才开始并行

---

## 5. 执行顺序

按依赖关系排序的任务执行流：

```
  ┌────────────────────────┐
  │  P1: Hub + 协议定义      │  Day 1-2
  │  (protocol.go, hub.go)  │
  └──────────┬─────────────┘
             │
    ┌────────┴────────┐
    ▼                 ▼
┌────────────┐  ┌────────────────┐
│ P2: 工具    │  │ P3: 扩展开发    │  Day 2-4 & Day 3-5
│ (tools.go) │  │ (background.js)│
└──────┬─────┘  └───────┬────────┘
       │                │
       │                │  (并行)
       │                │
       │    ┌───────────┘
       ▼    │
┌────────────┴──────────┐
│ P4: API 路由           │  Day 4-5
│ (browser_handler.go)   │
└──────────┬────────────┘
           │
           ▼
┌──────────────────────┐
│ P5: 前端 Tab           │  Day 5-6
│ (AppSettingsModal)    │
└──────────┬───────────┘
           │
           ▼
┌──────────────────────┐
│ P6: 截图渲染           │  Day 6-7
│ (MessageBubble.vue)   │
└──────────┬───────────┘
           │
           ▼
┌──────────────────────┐
│ P7: 测试 + 打包        │  Day 8-9
│ (tests + zip script)  │
└──────────────────────┘
```

**每个阶段结束后必须执行**:
1. `go build ./internal/browser` 确保编译通过
2. 修改了配置结构后：`go build ./internal/config`
3. 修改了前端：`cd frontend && npx vue-tsc -b` 类型检查
4. 全部完成后：`task build:all` 全量构建
5. `task test:go` 运行所有单元测试

---

## 6. 风险与缓解

| 风险 | 概率 | 影响 | 缓解措施 |
|------|------|------|----------|
| WebSocket 库缺失或冲突 | 中 | 高 | 优先检查 go.mod；若不存在，添加 `nhooyr.io/websocket`（与 gorilla 选其一） |
| Chrome 扩展 API 变更 | 低 | 中 | 开发时用 Chrome 稳定版（v132）；文档注明版本兼容 |
| 截图 base64 过大导致 token 爆炸 | 中 | 高 | JPEG quality 80 + stripImageContent 两轮后去重 |
| Service Worker 超时断连 | 高 | 中 | 扩展端 `chrome.alarms` 心跳保活；server 端重连逻辑 |
| 工具描述不准确导致 LLM 误调用 | 中 | 中 | 每个工具附详细示例（成功/失败场景） |

---

## 7. 文件清单

### 新增文件

```
internal/browser/
├── protocol.go        # JSON-RPC 类型 + BrowserClient 结构
├── hub.go             # BridgeHub (连接池)
├── tools.go           # 15 工具定义 + handler
├── manager.go         # 生命周期管理
├── browser_test.go    # 单元测试

internal/server/
└── browser_handler.go # HTTP API + WebSocket handler

browser-extension/
├── manifest.json      # MV3 配置
├── background.js      # Service Worker + WebSocket
├── content.js         # DOM 操控
├── content.css        # 元素高亮 (可选)
├── popup.html         # UI
├── popup.js
└── icons/{16,48,128}.png

docs/plans/
└── browser-control-plan.md   # 本文件

docs/
└── browser-control.md        # 用户文档

scripts/
└── package-browser-ext.ps1    # ZIP 打包脚本

build/                         # gitignored
└── browser-extension.zip
```

### 修改文件

```
internal/config/config.go             # +BrowserConfig 结构 + Default() 字段
internal/server/server.go             # +4 路由注册
internal/server/handler.go            # +browserMgr 字段 (Handler struct)

frontend/src/api/client.ts            # +5 API 函数 + image MessagePart
frontend/src/stores/chat.ts           # +tool result 识别 base64
frontend/src/components/
  ├── AppSettingsModal.vue            # +browser tab 面板
  └── MessageBubble.vue               # +截图渲染

Taskfile.yml                          # +package:browser-ext 任务
```

---

## 8. 验收清单

### 功能验收

- [ ] Chrome 扩展安装成功，popup 显示连接状态
- [ ] 扩展连接到 pchat-server（WebSocket）
- [ ] LLM 决定调用 `browser_navigate`，页面跳转
- [ ] LLM 调用 `browser_click`，元素被点击
- [ ] LLM 调用 `browser_screenshot`，截图 base64 显示在气泡
- [ ] 第二轮后历史记录中截图被替换为占位
- [ ] 多浏览器连接（2+ 实例）同时工作
- [ ] 设置面板开关能启用/禁用
- [ ] `task build:all` 成功
- [ ] `task test:go` 通过

### 代码质量

- [ ] 无 lint 错误 (`go vet ./internal/browser`)
- [ ] 注释符合双语规范
- [ ] 错误处理使用 `fmt.Errorf(...: %w, err)`
- [ ] 类型检查通过 (`vue-tsc -b`)

---

**文档结束。按此计划执行。**
