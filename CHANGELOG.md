# P-Chat 优化路线图 (CHANGELOG)

本文件记录 P-Chat 的长期改进计划，按优先级和实施步骤组织。

> 标记: `✅` 已完成 · `🔧` 实施中 · `📋` 待办 · `🚫` 取消

---

## 已完成 ✅

### v0.1.0 — 基础架构
- Go 1.22+ 项目, `go mod` + Cobra CLI
- 三种风格人格 (可爱/古风/科技) 完整实现
- 数据目录镜像 codex/claude/opencode
- AGENTS.md / Rules / Skills 系统
- OpenAI + Anthropic 双协议 LLM 客户端
- ReAct 工具调用循环
- REPL 13 个斜杠命令
- HTTP server (`pchat-server`)

### v0.2.0 — UX 提升
- `/` 命令 inline ghost 补全（无弹窗）
- 上下箭头 / Tab / Enter 接受补全
- Claude Code 风格 spinner + 状态栏
- 工具调用可视化（● / ✓ / ✗ 图标）

### v0.3.0 — 子代理系统
- `task` 工具：派生子 agent 隔离执行
- 防递归（子 agent 排除 `task` 工具）
- 嵌套事件显示（父 UI 缩进显示子 agent 步骤）
- 并发工具执行（多 task 同步跑）
- per-tool 5min timeout + 父 ctx 取消传播
- 结果缓存（5min TTL, 命中率统计）
- 工具白/黑名单（`subagent.allowed_tools` / `denied_tools`）
- `/debug cache` 命令

### v0.4.0 — 记忆与知识库
- SQLite 替换 JSON (`~/.p-chat/memory/store.db`)
- 自动迁移旧 JSON 文件
- IO debounce（批量写 + 2s flush）
- 多会话管理：`/history` / `switch` / `rename` / `forget`
- 语义检索（embedding + 余弦相似度）
- 本地 hash embedder（256 维, 零依赖）+ OpenAI embedder
- 外挂知识库：`/kb add|list|scan`
- `/recall <query>` 语义检索
- 自动摘要（超阈值时调 LLM 总结）
- WAL 模式 + 外键约束

### v0.5.0 — 上下文管理
- REPL.chat 加载历史消息传给 LLM
- agent 修复：assistant + tool 消息都持久化
- `tool_call_id` 从 metadata 读回
- `/new` (开启新对话，会话隔离)
- `/clear` (清空当前会话)

---

## 第1步: P0 — 沙箱与工具审批 🔧

**目标**: 防止 LLM 误操作（误删文件、执行危险命令）

### 任务清单
- [ ] 1.1 沙箱配置（`config.sandbox.*`）
  - [ ] `enabled`: 全局开关（默认 `true`）
  - [ ] `write_protected_paths`: 禁止写入路径（默认含 `~/.ssh/`, `~/.bashrc`）
  - [ ] `exec_dangerous_patterns`: 危险命令正则（`rm -rf`, `mkfs`, `dd if=`, etc.）
  - [ ] `require_confirm`: 触发确认的策略（`always` / `dangerous` / `never`）
- [ ] 1.2 工具层加沙箱检查
  - [ ] `exec_command`: 匹配 `exec_dangerous_patterns` → 返回 `IsError=true` 阻止执行
  - [ ] `write_file`: 路径在 `write_protected_paths` → 拒绝
  - [ ] 命中规则时返回明确的错误（"E_SANDBOX: pattern matched"）
- [ ] 1.3 REPL 审批 UI
  - [ ] 检测到 `IsError` + `sandbox` 标签 → 显示提示信息
  - [ ] 支持 `/unsafe on|off|once` 临时关掉沙箱
  - [ ] 命令级别的 `/unsafe allow <pattern>` 白名单
- [ ] 1.4 单元测试
  - [ ] 沙箱逻辑覆盖（白名单、黑名单、危险模式）
  - [ ] 沙箱关闭时的行为

### 设计草案

```go
// internal/sandbox/sandbox.go
type Sandbox struct {
    Enabled             bool
    WriteProtectedPaths []string
    ExecDangerousRegexp []*regexp.Regexp
    RequireConfirm      string // "always" | "dangerous" | "never"
}

func (s *Sandbox) CheckExec(command string) (allowed bool, reason string)
func (s *Sandbox) CheckWrite(path string) (allowed bool, reason string)
```

```yaml
# config.yaml
sandbox:
  enabled: true
  require_confirm: dangerous   # dangerous|always|never
  write_protected_paths:
    - "~/.ssh/"
    - "~/.bashrc"
    - "~/.zshrc"
    - "/etc/"
    - "/usr/"
  exec_dangerous_patterns:
    - 'rm\s+-rf\s+/'
    - 'mkfs\.'            # mkfs.ext4 等
    - 'dd\s+if=.*of=/dev/'
    - ':(){\s*:\|:&\s*};:'  # fork bomb
```

---

## 第2步: P0 — Panic 恢复 + Esc 取消

**目标**: 稳定性 + 流式响应可中断

### 任务清单
- [ ] 2.1 Panic 恢复
  - [ ] `agent.ChatWithTools` 入口 `defer recover()`
  - [ ] 捕获后 emit `Error` chunk 包含堆栈
  - [ ] 关闭 stream，让 REPL 继续
- [ ] 2.2 Esc 取消当前生成
  - [ ] REPL 在 chat 中检测 Esc（`0x1B` 后无跟随字节）
  - [ ] 触发 `cancel()` 当前 context
  - [ ] agent 检测 ctx cancel → emit partial `Done` chunk
  - [ ] REPL 退出 chat 循环，回到 prompt
- [ ] 2.3 测试
  - [ ] 注入 panic 的 mock LLM → REPL 不死
  - [ ] Esc 中断 → 验证 stream 关闭

### 设计草案

```go
// internal/cli/repl.go
type REPL struct {
    ...
    cancelCurrent context.CancelFunc  // 用来取消当前 turn
}

// run chat loop:
ctx, cancel := context.WithCancel(ctx)
r.cancelCurrent = cancel
defer func() { r.cancelCurrent = nil }()
go r.runChat(ctx, req)
```

```go
// internal/agent/agent.go
defer func() {
    if r := recover(); r != nil {
        ch <- ChatStreamChunk{
            Error: fmt.Sprintf("panic: %v\n%s", r, debug.Stack()),
            Done:  true,
        }
    }
}()
```

---

## 第3步: P1 — 工具结果可展开

**目标**: 工具返回几 KB / MB 数据时，用户能展开看完整内容

### 任务清单
- [ ] 3.1 工具结果存储
  - [ ] REPL 缓存最近一次工具调用的完整结果（带 size + 路径）
  - [ ] 限制：单条不超过 10MB, 全部不超过 100MB
- [ ] 3.2 展开命令
  - [ ] `/expand <N>` — 显示第 N 个工具调用的完整结果
  - [ ] `/expand last` — 展开最后一个
  - [ ] 内置 pager（less 风格）：↑↓/PageUp/PageDown/q 退出
- [ ] 3.3 UI 提示
  - [ ] 工具行后显示 `▸ 展开 (5.2KB)` 提示
  - [ ] 长结果自动建议展开

### 设计草案

```go
// internal/cli/repl.go
type toolResultCache struct {
    seq      int
    tool     string
    args     string
    result   string
    duration time.Duration
    at       time.Time
}

func (r *REPL) recordToolResult(tool, args, result string, dur time.Duration)
func (r *REPL) ExpandResult(n int) (string, error)
```

---

## 第4步: P1 — LLM 采样参数 + 多语言

**目标**: 用户能控制 LLM 输出风格 + 中文/英文输出

### 任务清单
- [ ] 4.1 LLM 采样参数
  - [ ] `cfg.LLM.Temperature` (default 0.7)
  - [ ] `cfg.LLM.TopP` (default 1.0)
  - [ ] `cfg.LLM.MaxTokens` (default 2048, 上限 8192)
  - [ ] 透传到 OpenAI/Anthropic 请求
- [ ] 4.2 多语言配置
  - [ ] `cfg.Output.Language` (`zh` / `en` / `auto`)
  - [ ] 注入到 system prompt 末尾
  - [ ] `auto` 模式：检测用户输入语言
- [ ] 4.3 verbose 模式
  - [ ] `cfg.UI.Verbose` (default `false`)
  - [ ] 显示完整 tool args, 不截断

---

## 第5步: P1 — 单元测试

**目标**: 覆盖率从 5% 提升到 60%+

### 任务清单
- [ ] 5.1 `internal/agent` 核心测试
  - [ ] `buildStaticSystemPrompt` 缓存命中/失效
  - [ ] `ChatWithTools` 工具循环（mock LLM）
  - [ ] 工具并发执行
- [ ] 5.2 `internal/memory` 测试
  - [ ] Open / Close / migration
  - [ ] AddMessage / GetMessages 往返
  - [ ] 多会话切换
  - [ ] 并发安全
- [ ] 5.3 `internal/tool` 测试
  - [ ] `read_file` / `write_file` / `list_files` / `exec_command`
  - [ ] 沙箱规则匹配
- [ ] 5.4 `internal/subagent` 测试
  - [ ] 并发执行
  - [ ] 缓存命中
  - [ ] 取消传播
  - [ ] 递归阻止
- [ ] 5.5 `internal/knowledge` 测试
  - [ ] `IndexDir` 跳过隐藏目录
  - [ ] `Search` 返回 top-k
  - [ ] `SplitText` 边界条件
- [ ] 5.6 `internal/llm` 测试
  - [ ] OpenAI / Anthropic 请求构造
  - [ ] 协议路由

---

## 第6步: P2 — Plan Mode + 子 agent 详情

**目标**: 大任务前先看计划；子 agent 调用工具时看到完整参数

### 任务清单
- [ ] 6.1 Plan Mode
  - [ ] `/plan <task>` 命令
  - [ ] 强制 LLM 只能输出文本（不发 tool calls）
  - [ ] 计划展示：编号列表 + 每步预计工具
  - [ ] 用户确认（y/n/edit）后才执行
- [ ] 6.2 Sub-agent 工具详情
  - [ ] 嵌套事件 `call-1` / `call-1-ok` 也显示工具 args 预览
  - [ ] 子 agent 工具结果也可展开

---

## 第7步: P2 — Recall 作为工具

**目标**: LLM 自主决定何时查知识库，而不是依赖用户手动 `/recall`

### 任务清单
- [ ] 7.1 新增 `recall` 工具
  - [ ] 工具签名：`{"query": "string"}`
  - [ ] 调用时通过 `recall.Engine.Search`
  - [ ] 把结果作为 `role: tool` 消息返回
- [ ] 7.2 System prompt 提示
  - [ ] 在 identity 段加 "if unsure, call recall first"
  - [ ] 限制调用频率（避免每轮都查）
- [ ] 7.3 子 agent 是否能用
  - [ ] 默认禁用（避免子 agent 递归查）
  - [ ] 可通过 `subagent.allowed_tools` 启用

---

## 第8步: P3 — 工程化（CI/CD, i18n, 文档）

**目标**: 项目可持续维护

### 任务清单
- [ ] 8.1 CI/CD
  - [ ] `.github/workflows/ci.yml`: test + vet + build
  - [ ] `.github/workflows/release.yml`: 跨平台构建 (Windows / macOS / Linux)
- [ ] 8.2 国际化 (i18n)
  - [ ] 提取所有用户可见字符串
  - [ ] `i18n/zh.toml` `i18n/en.toml`
  - [ ] `--lang` flag
- [ ] 8.3 文档
  - [ ] `docs/guide.md` 入门指南
  - [ ] `docs/advanced.md` 高级用法
  - [ ] `docs/troubleshooting.md` 故障排查
  - [ ] 完善 `README.md`

---

## 不实施 (P4 - 长期) 🚫

- 多 LLM 编排（cheap + smart model）
- Sub-agent 沙箱（bubblewrap / firejail）
- 远程协作（WebSocket + 多用户）
- Agent Marketplace
- 长任务后台化
- iOS 客户端

---

## 进度

```
第1步 P0 沙箱          ✅
第2步 P0 Panic/Esc     ✅
第3步 P1 工具展开       ✅
第4步 P1 采样/语言     ✅
第5步 P1 单元测试      ✅
第6步 P2 Plan Mode     ✅
第7步 P2 recall 工具化  ✅
第8步 P3 CI/CD        ✅ (i18n 跳过)
```

## 新增需求 (本次会话)

### 供应商多模型

```yaml
providers:
  - name: "openai"
    protocol: "openai"
    base_url: "..."
    api_key: ""
    models:
      - name: "gpt-4o"
        default: true
        display_name: "GPT-4o"
        description: "Most capable, multimodal"
      - name: "gpt-4o-mini"
        display_name: "GPT-4o mini"
      - name: "o1-preview"
```

- 同一供应商共享 base_url + api_key
- `default: true` 标记默认模型
- `display_name` / `description` 在 /model 中展示
- 旧 `model: "x"` 字段仍兼容

### /help 详细帮助

```
/help           # 列出所有命令
/help model     # 详细：用法 / 参数 / 示例
/help /expand
/help /unsafe
```

每个命令有 `Usage` / `Args` / `Examples` 字段
`matchCommand` 用 map 索引 O(1) 查找

### Plan Mode (/plan)

```
/plan 帮我把 *.go 文件加 license header
[LLM 输出纯文本计划，不调工具]
  > y
[执行]
  > n
[取消]
```

底层：`ChatRequest.PlanMode=true` → 注入 system hint，禁用工具，限制 1 轮

### recall 作为工具

LLM 现在可以自己调用 `recall(query="...")` 查知识库，不再依赖用户 `/recall`。
- system prompt 自动加 "if unsure, call recall first" 提示
- 子 agent 排除 recall 工具（防止递归）
- 顶层 helper `registerRecallTool` 在 cmd/pchat（避免 import cycle）

## 测试覆盖

```
config   8 tests
sandbox  16 tests
tool     18 tests (handlers + sandbox)
memory   14 tests
llm      2 tests
agent    5 tests
cli      21 tests (incl. 10 new cliContext tests)
─────────────────────
total   ~84 tests passing
```

## CI/CD

- `.github/workflows/ci.yml`: 跨平台 test (Linux/macOS/Windows) + lint + coverage
- `.github/workflows/release.yml`: tag push 自动 build 6 个 binary (linux/darwin/windows × amd64/arm64) + 创建 release

## 完整进度总结

### 已完成步骤

**第1步 沙箱 (P0)**: 18 个危险模式 + 16 个保护路径，0 改动到工具签名
**第2步 Panic/Esc (P0)**: defer recover 捕获 panic；Esc 取消 in-flight 调用
**第3步 工具展开 (P1)**: /expand <N> / /expand last，环形缓存最近 20 次
**第4步 采样/语言 (P1)**: temperature / top_p / max_tokens 透传；zh/en/auto 语言注入
**第5步 单元测试 (P1)**: 6 个包有测试，~50 个用例，捕获了 4 个真实 bug

### 测试覆盖

```
config   5 tests
sandbox  16 tests
tool     18 tests (handlers + sandbox)
memory   14 tests
llm      2 tests
agent    5 tests
cli      21 tests (incl. 10 new cliContext tests)
─────────────────────
total   ~84 tests passing
```

### Bug 修复（由测试发现）

1. `newConvID()` 用秒级精度 → 同秒创建多个 conv 触发 UNIQUE 冲突
   修复: 用 nanosecond + atomic counter
2. `DeleteConversation` 不创建新 current conv
   修复: 当 mostRecent 返回空时显式 NewConversation
3. `OpenAt` 不创建父目录
   修复: MkdirAll(parent)
4. `progress.go` 工具成功行对长结果没提示展开
   修复: 添加 `▸ /expand last` 提示

---

## 约定

- **每次改动**: 1 个 step 完成 → 跑 `go test ./...` + 端到端测试 → 更新本文档
- **commit 风格**: `chore(step-1): add sandbox config skeleton` / `feat(sandbox): add dangerous pattern check`
- **每个 step 结束**: 重新构建 `bin/pchat.exe` + `bin/pchat-server.exe` + 写测试


### ��1������ܽ�

- ɳ�����: 18 ��Ĭ��Σ��ģʽ + 16 ��Ĭ�ϱ���·��
- ͨ�� ctx ע�빤�ߣ�0 �Ķ������й���ǩ��
- 21 ����Ԫ����ȫ��ͨ����sandbox + tool integration��
- /unsafe �ṩ 3 ��: once / on / off
- ������Ϣ�Ѻ�: ������������/·�� + ��ʾ


## v0.6.0 �� Client-server architecture + Web GUI

### New: HTTP client (internal/httpcli)
- \Client\ wraps pchat-server's REST API
- 10 tests covering: Ping, sessions CRUD, messages, SSE streaming, error surfacing
- Used by both CLI (future) and the new web GUI (now)

### New: server process management (internal/serverproc)
- Auto-launches pchat-server as a subprocess on a free port
- Polls /health until ready (15s default)
- 4 tests including a real end-to-end binary launch
- Foundation for the future \pchat --server\ flag

### New: Web GUI (web/index.html)
- Self-contained vanilla HTML+JS chat client
- Served by pchat-server at /app/
- Uses the new \/api/v1/sessions/:id/messages\ SSE endpoint
- Sidebar session list + chat pane + tool/phase events
- No build step (no Node, no Wails toolchain)
- Open \http://localhost:8960/app/index.html\ in a browser

### Changed: pchat-server
- \/api/v1/sessions\ now supports full CRUD (list/create/get/rename/delete)
- \/api/v1/sessions/:id/messages\ supports history + SSE streaming
- \NewWithStaticDir(cfg, agt, store, dir)\ lets tests serve from arbitrary paths
- \Server.Engine()\ accessor for httptest
- \Server.RunAt(addr)\ for binding to a caller-supplied port (PCHAT_PORT env)

### Pending: CLI HTTP integration ✅ done
- The `cliContext` interface now covers every operation the slash
  commands need: sessions, providers, models, config writes, style,
  tools, sandbox, expand, /debug, /kb, /recall, /init, /skills,
  /rules, /agents, /export, /plan (local-only), and chat streaming.
- All 18 slash commands in `internal/cli/commands.go` now take
  `func(ctx cliContext, args string) error`. The REPL dispatches
  via `r.asContext()`, which returns a `*localContext` wrapping
  the REPL. An `*httpContext` (driven by `httpcli.Client`) is the
  other implementation; operations the server doesn't yet expose
  return `*ErrUnsupported` so the handler can show "only available
  locally".
- 10 new tests in `internal/cli/context_test.go` exercise the
  type predicates, the localContext read paths, the httpContext
  stub behaviour, and the `*ErrUnsupported` contract.
- The dead `chunkToHTTPEvent` / `toolStatusForStep` / unused-import
  guards are gone.

## v0.7.0 — Web GUI 补完

### web/index.html 重写 (12.8 KB → 33 KB)

**修复的 bug**
- `switchSession` 里 user 消息漏渲染 (line 349 当时是 `m.role === "tool" ? "tool" : m.role` 把 user 和 assistant 都当一类)
- 流式渲染时 `textContent =` 重建整块 DOM，性能差
- `/expand` 风格的 tool args 没有显示

**新增**
- 侧边栏底部 style 切换器 (3 pill 按钮: cute/guofeng/tech)
- 侧边栏底部 model 下拉选择器 (从 `/api/v1/providers` 拉)
- 真正的流式渲染：throttled DOM update (50ms 一次，20fps)
- 停止按钮 (流式时切换显示，绑 `state.abort.abort`)
- 状态栏：`tokens_in` / `tokens_out` / `elapsed` 三块 pill
- 轻量 Markdown 渲染 (无 CDN，自写)：
  - 标题、bold/italic、列表、blockquote、hr
  - 行内 code + 代码块 (用 placeholder 防 inline pass 误改)
  - 链接 (白名单 http/https/内部路径)
  - HTML escape
- 历史消息正确渲染 (user / assistant / tool 分类)
- Tool 调用面板：可折叠、显示 args、显示 result、显示 elapsed
- 停止生成时显示 "⏹ 已停止生成" 标记
- 双击会话标题可重命名 (PATCH /sessions/:id)
- Esc 取消生成、Enter 发送、Shift+Enter 换行
- Toast 提示 (风格切换、模型切换、错误)
- 流式光标动画 (▍ 闪烁)

**测试**
- `TestWebGUI_IndexPage` 扩展为 14 个 sanity check (style-picker、model-picker、selectStyle、selectModel、stopGeneration、updateHeaderMeta、scheduleRender 等)
- 18 个 markdown 渲染 case 全过 (plain / bold / italic / 各级 heading / link / code block / inline code / list / numbered / escape / blockquote / hr / nested / paragraphs / br / code at start)
