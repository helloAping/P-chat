# Webview 内存膨胀与对话卡住排查报告

日期：2026-08-03
分支：feat_1.0.9
状态：主问题已修复（未发布）；数据库图片方案已实施（未发布）；回合超时配置已调整；子代理超时误判 + 空转熔断已修复；二查发现并修复 2 个遗留 bug（未发布）

---

## 1. 背景

用户在实际使用 P-Chat（菜谱项目 `D:\demo\food`）时遇到三个相互关联的问题：

1. **长对话 aicoding 后，工具执行完成但对话卡住**——停在最后一个工具调用，等 1 小时无响应。
2. **msedgewebview2.exe 内存飙到 3-5GB**；重开软件 → 发"继续" → 无输出但 2 分钟内存从 100M 涨到 3GB。
3. **dev-bin 环境（my-blog 项目）出现"回合超出最长执行时间被终止: context deadline exceeded"**，todo_list 未执行完被中断。

本报告合并这三部分排查结论 + 已实施的修复 + 待决策方案。

---

## 2. 问题一：webview 内存膨胀 + 工具后对话卡住（已修复）

### 2.1 现场证据

| 观测项 | 数值 | 结论 |
| --- | --- | --- |
| `msedgewebview2.exe` | 3-5GB，CPU 接近 0 | 前端内存堆积，非渲染风暴 |
| `pchat-server.exe` | 20-30MB，CPU 8% | 服务端无泄漏、无死循环，工具早完成 |
| 数据库（`D:\Downloads\store.db`） | 90MB，messages 38518 条，文本 67.8MB | 菜谱会话 4580 条消息、168 张图共 24MB base64 |
| 单条最大 | metadata 94KB、content 165KB（图片 base64） | 无单条庞然大物，但总量大且全在前端渲染路径 |

### 2.2 根因链（证据闭合）

```
前端 Vue reactive store 无界累积
  ├─ 单会话 4580 条消息全量驻留（parts/工具结果被 Vue 深层代理，开销放大数倍）
  ├─ 大 tool 结果（exec_command 输出等）全量进 state
  └─ 历史图片 base64（120-165KB/张）转 blob URL 后仍驻留
        ↓
webview JS 堆膨胀到 3-5GB → 渲染冻结（CPU≈0 是"被内存压死"非"死循环"）
        ↓
SSE fetch 停止消费（reader.read() 挂起，前端 150s 空闲看门狗回调也执行不了）
        ↓
服务端 sendOrDrop 阻塞在 cap-64 channel（agent.go:963，只认 ctx.Done，最长 900s）
        ↓
前端收不到 done → state.streaming[id] 永不清 → 输入框永远"停止"按钮 → "卡住对话"
```

**关键澄清**：服务端 20-30MB / CPU 8% 证明"工具执行完还卡住"不是服务端在等什么——工具早就跑完了，是**前端把整个流冻住**导致服务端 `sendOrDrop` 阻塞（阻塞 ≠ 死循环，所以 CPU 低）。

### 2.3 已实施的修复（17 文件，+670 行）

**后端（Go）**：

| 改动 | 文件 | 作用 |
| --- | --- | --- |
| `sendOrDrop` 30s 逃生 | `internal/agent/agent.go` | 非关键事件（content/tool 增量）在 channel 满 30s 后丢弃继续，不再阻塞 900s。`Done`/`idle` 事件不逃生（保协议） |
| SSE 写超时 10s + cancelTurn | `internal/server/messages.go` | 客户端停止读取时 10s 内取消 turn → agent 循环退出 → sessionLock 释放 |
| `/cancel-stream` 端点 | `messages.go` + `server.go` + `handler.go` | 前端 abort 后显式通知服务端取消 turn，幂等 |
| `turnCancels` 注册（SendMessage + **Regenerate**） | `messages.go` + `regen.go` | 写超时/cancel-stream 对 regen 回合同样生效（审查发现 M1 已修） |
| 大 tool 结果 >32KB 截断 | `agent.go` + `auto_continue.go` + `util.go` | `ToolResultFull` 不再进前端，只发预览 + 截断标记 |
| 有界截断缓存 | `tool_result_cache.go`（新） | 64 条/64MB 上限、30 分钟 TTL、会话隔离、子代理 rekey 到父会话 |
| `/tool-result` 按需拉取 | `messages.go` + `server.go` | 前端点"查看完整输出"时按需取全文 |

**前端（Vue/TS）**：

| 改动 | 文件 | 作用 |
| --- | --- | --- |
| 消息数 cap 300 条 | `stores/chat.ts` | `capSessionMessages` 在 done/startStream 裁掉最旧消息并 revoke blob；滚动加载不裁（避免破坏浏览位置） |
| `cancelStream` 接入 | `client.ts` + `chat.ts` + `conversationTurn.ts` | 停止/abort 时通知服务端取消 turn |
| "查看完整输出"按钮 | `ToolCallCard.vue` | 截断的大结果点击按需拉全文（本地 ref，不进 reactive store） |
| 超长文本截断 32KB + 展开 | `MessageBubble.vue` | 防巨型 markdown 文本每次全量 parse（O(n²)） |
| CSS 虚拟化 | `ChatWindow.vue` | `content-visibility: auto` 浏览器原生跳过视口外渲染，不改滚动逻辑 |

**测试**：新增 `TestCancelStream_AbortsStalledTurnAndReleasesLock`、`TestTruncatedResultCache_*`（round-trip/会话隔离/TTL/驱逐）、`TestChunkToEvent/tool_truncated_result_marker`。`go test ./...` 全绿，`vue-tsc` + `npm run build` 通过。

**代码审查**（子代理执行）：发现 M1（Regenerate 不注册 turnCancels → 锁卡死）+ L5（截断事件无测试覆盖），均已修复；L2（字符数/字节数阈值不一致）、L4（TTL 兜底）确认可接受。

### 2.4 待验证

修复尚未在真实环境验证。验证步骤：
1. `task build:gui` 重新打包。
2. 打开卡住的菜谱会话，发"继续"。
3. 观察 `msedgewebview2.exe` 内存是否不再 2 分钟涨 3GB；卡住是否 10-30 秒内恢复。

---

## 3. 问题二：数据库图片 base64 爆炸（已实施）

### 3.1 现状与问题

| 环节 | 现状 | 问题 |
| --- | --- | --- |
| 前端发送 | 图片 base64 内联进 `POST /messages`（`inlineAttachments`） | 10MB 请求体上限 |
| 服务端落库 | `ExpandAttachmentsCM`（attachment.go:167-180）把 base64 完整存 SQLite `messages.content`（`msg_type=1`） | **DB 爆炸**（实测 90MB 库 67MB 是图片）；base64 比原始文件大 33% |
| 历史加载 | `buildMessageResponse`（message_helpers.go:540-553）拼 `data:` URL 返回前端 | **webview 内存上涨**（每条 120-165KB base64 进 Vue state） |
| LLM 上下文 | base64 verbatim 传给 LLM | 必要（模型要看图），不能省 |

### 3.2 方案：图片实体化存储（✅ 已实施，2026-08-03）

**核心：`messages.content` 只存引用（`upl://<id>`），实体文件在 `~/.p-chat/uploads/`，前端通过 `GET /api/v1/uploads/:id` 按需取。**

```
发送：前端选图 → POST /uploads（已存在）拿 uploadID → POST /messages 只带 { upload_id }
     → ExpandAttachmentsCM：有 upload_id 的从磁盘读 base64 给 LLM；content 写 "upl://<id>"
历史：buildMessageResponse：content 是 "upl://" 时返回 /api/v1/uploads/<id> URL（不读文件）
     → 前端 <img src="/api/v1/uploads/<id>"> 按需加载
```

**收益**：DB 90MB → ~25MB；webview 历史加载不再涨内存（直接治 devbin 实测的 100M 上涨）；请求体不再限 10MB。

**关键决策点（实施时落地情况）**：
- **D1 LLM 上下文**：每次从磁盘读文件 → base64 → 拼请求。已落地：`loadHistoryForSend` → `resolveHistoryUploads`（message_helpers.go）把历史 `upl://` 行重新读盘成 base64，模型看到原图；文件缺失降级为文本标记，不中断回合。
- **D2 旧数据兼容**：已按"双读兼容"实现——content 以 `upl://` 开头走新路径，否则按旧 base64 处理。旧库图片不迁移也能看。迁移脚本（启动扫描 `msg_type=1` 抽文件）后续做。
- **D3 清理策略**：✅ 已实施（2026-08-03）。详见下方 §3.3b。
- **D4 截图工具**：browser_screenshot 走 `ToolResultFull`（已截断 32KB），是另一条路径，本次不动。

### 3.3b D3 uploads 孤儿文件清理（✅ 已实施，2026-08-03）

**设计原则**：文件只有在**全库无消息引用**时才可删；rollback 路径**不删**（有 undo/RestoreMessages，删了文件 undo 后图片 404）。

| 触发点 | 行为 |
| --- | --- |
| `PermanentDeleteSession` / `ClearSessionMessages`（不可逆） | 删前 `UploadRefsForConversation` 收集会话内 `upl://` 引用 → 删后 `pruneUploadFiles`（逐 id 查 `CountUploadRefs` 全库引用数，为 0 才删文件） |
| `RollbackMessages` / `Regenerate`（可逆/软归档） | **不删**——undo 会恢复消息，regen 是软归档不物理删 |
| 启动清扫 | `sweepOrphanUploads(24h)`：上传但从未发送的消息（前端先 POST /uploads 后可能放弃发送）是孤儿，mtime 超过 24h 且全库无引用 → 删除；goroutine 后台跑不阻塞启动 |

**新增**：`memory.go` `UploadRefsForConversation` + `CountUploadRefs`（跨全库引用计数）；`upload.go` `pruneUploadFiles` + `sweepOrphanUploads`；`server.go` 启动清扫。

**测试**：`TestUploadRefsAndPrune`（引用收集、跨会话引用保护、清空后仅删孤儿、sweep 尊重 mtime grace）。`go test ./...` 全绿。

### 3.3 改动明细（已实施）

| 文件 | 改动 |
| --- | --- |
| `internal/llm/chat_message.go` | `ChatMessage` 加 `UploadID` 字段（`json:"upload_id,omitempty"`） |
| `internal/agent/attachment.go` | `Attachment` 加 `UploadID` + `UnmarshalJSON` 镜像到 ID；`ExpandAttachmentsCM` image 分支填 `UploadID`；`resolveAttachmentData` 入口归一化 ID |
| `internal/agent/agent.go` | 持久化段：`UploadID != ""` 时写库副本 content 改 `upl://<id>`（LLM 请求用的 msgs 保持 base64） |
| `internal/memory/memory.go` | `encodeChatMeta` 写 `upload_id`；`decodeChatMessages` 读回 `UploadID` |
| `internal/server/message_helpers.go` | `buildMessageResponse`：`upl://` → `/api/v1/uploads/<id>` URL；新增 `uploadIDFromContent` + `resolveHistoryUploads`（upl:// → 磁盘 base64） |
| `internal/server/messages.go` | `loadHistoryForSend` 末尾调用 `resolveHistoryUploads` |
| `internal/server/handler.go` | Handler 加 `attachResolver` 字段 + `SetAttachmentResolver`（agent 与 handler 共用同一个 resolver） |
| `frontend/src/api/client.ts` | `InlineAttachment` 加 `upload_id?` 字段 |
| `frontend/src/components/InputArea.vue` | 图片附件先 `api.uploadFile(file)` 拿 id → `{ type:'image_url', upload_id, name, kind, mime }`，不再内联 base64；bubble 预览仍用本地 data URL 即时显示；上传失败 fallback 到内联 base64 |

**行为细节**：
- 前端请求体：图片从 ~165KB base64 → ~200B 引用，10MB 上限不再拦多图。
- 气泡即时预览：`bubbleAttachments` 仍用本地 data URL（发送瞬间显示），仅 wire 层走 upload_id。
- 后端无透传映射问题：`agent.Attachment` 的 `upload_id` JSON 标签直接绑定，`UnmarshalJSON` 自动镜像到 `ID`，resolver 路径（读盘）无需改动。

### 3.4 测试与验证（✅ 已通过）

- **新增测试**：
  - `agent/attachment_test.go`：`TestExpandAttachmentsCM_UploadImage`（UploadID → LLM base64 + 元数据）、`TestExpandAttachmentsCM_UploadIDMapsToResolver`（UnmarshalJSON 镜像）
  - `agent/agent_persistence_test.go`：`TestChatWithTools_UploadImagePersistsReference`（ChatWithTools 全链路：落库 content = `upl://<id>`，非 base64）
  - `server/attachment_test.go`：`TestSendMessageRequest_AcceptsUploadID`（upload_id → ID 镜像）、`TestBuildMessageResponse_UploadRef`（upl:// → `/api/v1/uploads/<id>` + 旧 base64 data URL 双读）、`TestResolveHistoryUploads_RehydratesBase64`（历史 upl:// 行读盘 → base64；缺失降级文本标记）
- **全量验证**：`go test -count=1 ./...` 全绿；`npx vue-tsc -b` + `npm run build` 通过。
- **真实库复现**（可选，未执行）：`dev-bin/.p-chat/memory/store.db`（21MB，my-blog 会话）或 `D:\Downloads\store.db`（90MB），启动 pchat-server 加载会话，确认历史图片走新 URL 路径。

**风险**：LLM 每次多一次磁盘读（可忽略）；旧库图片不迁移则继续 base64 双读（功能不受影响）；uploads 清理策略已做（§3.3b）。

---

## 4. 问题三：dev-bin 回合超时中断 todo（已定位，配置问题）

### 4.1 现场

dev-bin 环境（my-blog 项目 `D:\develop\project\my-blog`，会话 `conv_1785593685713979400_2`）出现：
- 前端显示"⚠ 回合超出最长执行时间被终止: context deadline exceeded"
- todo_list 停在 3/6 项完成，2 pending + 1 in_progress

### 4.2 排查结论：这是设计内的兜底在正常工作

**实锤**（`dev-bin/.p-chat/memory/store.db`）：
- 最后用户消息 `t=1785747347` → 最后活动 `t=1785748247`，差 = **900 秒整 = `MaxTurnSeconds` 默认值**。
- dev-bin config.json 的 `limits` 块没有 `max_turn_seconds` 字段 → 走 `Default()` 的 900s。
- 该回合是**真实的多轮 ReAct 循环**，不是卡死：

| 指标 | 数值 |
| --- | --- |
| 回合内消息 | 199 条 |
| LLM 轮次 | 51 轮 |
| 工具调用 | 75 次（exec_command 11、read_file 13、grep、list_files、todo_write 3） |
| 平均每轮 | ~17 秒（6-16 秒间隔，dev-bin 用 ollama/llama3 本地模型） |
| 进度 | 执行到第 4/6 项 todo |

**根因**：6 项 todo 的完整功能开发（51 轮 × 17 秒 ≈ 900 秒）超过 15 分钟上限，被兜底切断。`MaxTurnSeconds` 设计初衷是兜底**卡死**（工具挂起/LLM 无响应），但对正常长任务也是一刀切。

### 4.3 修复（✅ 已实施）

- **A. 调高上限**（已做）：`dev-bin/.p-chat/config.json` `limits` 加 `"max_turn_seconds": 3600`（1 小时；不设 0——之前等 1 小时卡住的教训是纯靠前端看门狗不可靠）。3600 覆盖 6 项 todo 的完整开发（15-20 分钟），同时保留兜底。
- **B. 超时更聪明**（代码改进，未做）：超时前发"即将超时"SSE 事件让 LLM 收尾；超时时先优雅写回 todo 状态再终止。
- **C. 提高每轮效率**（治本，未做）：ollama/llama3 每轮 6-16 秒偏慢；换更快模型或拆小 todo。

---

## 4b. 子代理超时的误判问题（已修复，2026-08-03）

### 4b.1 现象与现场（dev-bin store.db 实锤）

用户反馈：explore 子代理（`task` 工具，`subagent_type: explore`）明明在正常工作，只是耗时长，却被 5 分钟超时砍断，主对话**收不到任何内容**。库里两处现场：

| 会话 | 现场 |
| --- | --- |
| `conv_1785559024884049200_1`（my-blog 源码分析） | task 调用 → 子代理产出 6056 字节架构报告 → 但父 LLM 说 "The explore agent's report was truncated"，随后父级转向手工 read_file —— 子代理结果没被有效利用 |
| `conv_1785593685713979400_2`（my-blog 安全审计） | 3 个并行 explore 子代理：1 个 4m41s 完成（ok，53 parts），另外 2 个撞 5m 超时（`sub_agent status=start` 卡死 / `sub-agent stream ended without completion`）——父 LLM 收到 `(sub-agent returned no content)`，**没有材料可总结**，安全审计的注入/XSS、CSRF 两部分丢失 |

### 4b.2 根因

1. **固定 5 分钟硬超时**（`subagent.timeout` 默认 5m，dev-bin 未配置 → 默认值）对"正常但耗时"的长任务一刀切。explore 子代理用本地 ollama/llama3（每轮 6-16 秒），读 12+ 文件 + 多轮 ReAct 轻松超 5 分钟。
2. **超时后丢弃部分输出**：原实现把"静默关闭（超时/中断）"一律按硬失败处理——即使子代理已产出 6KB 有效调研结果，也只返回错误、不返回内容，父 LLM 拿到 `sub-agent failed: ...` 无从总结。

### 4b.3 修复：超时保留部分结果（对齐 Claude Code 的收集策略）

Claude Code 的做法：子代理不设 wall-clock 总时长上限（只有单次 API 请求的超时），子代理的完整输出（含失败时已产出的部分）作为工具结果整体返回主对话，主 LLM 基于内容自行判断完成度。P-Chat 保留硬超时兜底（本地模型确实可能真卡死），但**超时时不再丢弃已产出的内容**：

| 场景 | 原行为 | 新行为 |
| --- | --- | --- |
| 静默关闭（超时/中断）+ 有部分输出 | `sub_agent_err` + 错误，**内容丢弃** | `sub_agent_err`（卡片"失败（部分内容）"）+ **部分内容作为 tool result 返回**，带 `[sub-agent was interrupted ... PARTIAL]` 前缀 + stats footer |
| 静默关闭 + 无输出 | 硬失败 | 不变（无内容可救） |
| Error chunk | 硬失败 | 不变（子代理自报错误，尾部内容不可信） |
| 正常 Done / 软失败 | ok | 不变 |

**父 LLM 现在拿到**：`[sub-agent was interrupted and did not finish; the content below is PARTIAL — summarise what it did accomplish and continue the remaining work]\n\n<已产出的架构报告>...` → 能总结已完成部分、继续剩余调研，而不是整段作废。

### 4b.4 追加根因（2026-08-03 二查）：子代理工具被拒/命令语义错误导致空转

上一轮"超时保留部分结果"缓解了**症状**（超时后内容不再丢），但二查 dev-bin 日志发现超时的**真正诱因**：

**证据**：
- 子代理内部出现 `Access denied - INTERNAL/HANDLER`、`find internal/handler internal/service ... -name "*.go"` —— 这是 **Windows 上运行 Unix 的 `find` 命令**（Windows 的 `find` 是字符串搜索工具，对目录参数报 "Access denied"）。explore prompt 教子代理用 `find/grep/ls`，但 dev-bin 是 Windows。
- 子代理工具失败后 **LLM 每轮换一个新命令变体重试**（`find internal/handler` → `find internal/service` → `find web/templates`...），每次失败都不同，**stuck-loop 守卫（要求签名相同）和 same-tool 守卫（要求同名）都看不出来**，于是空转到 5 分钟超时。

**根因链**：explore prompt 是 Unix 导向 + 子代理失败后"打地鼠式"换命令重试 → 空转烧掉 5 分钟 → 超时触发 → （旧实现）内容丢弃。

### 4b.5 修复：累计失败熔断 + Windows 命令提示 + 子代理轮数上限（✅ 已实施）

| 改动 | 文件 | 作用 |
| --- | --- | --- |
| **累计失败熔断** `CumToolErrMax=8` | `internal/agent/agent.go` | 单回合累计 8 次工具失败（**不要求签名/工具名相同**）就注入"停止调用工具，基于已有信息总结"系统消息——治"打地鼠式空转"，之前同工具/同签名守卫都拦不住 |
| **Windows 命令提示** | `internal/subagent/builtins.go` | explore prompt 加一句"Windows 上 `find`/`ls` 是 Unix 工具；用 `dir`/`findstr`/`powershell`；命令失败就换工具，不要换参数重试" |
| **子代理轮数上限** `MaxRounds: 30` | `internal/subagent/subagent.go` | 子代理从默认 300 轮收紧到 30 轮——失败循环在 ~30 轮就强制总结，不再空转到 5 分钟超时（硬兜底） |
| 测试 | `agent_no_progress_int_test.go` | `TestChatWithTools_CumulativeToolErrors_BreaksWhackAMole`：fake LLM 每轮发**不同**失败命令，断言 `cum-tool-err-limit` 事件触发 |

**验证**：`go test ./...` 33 包全绿 + `vue-tsc` + `npm run build` 通过。

### 4b.7 追加修复：子代理 wall-clock 默认放宽 5m → 30m（✅ 已实施）

上轮修了"空转"诱因后，5m wall-clock 仍是**正常长任务的误杀源**（explore 读 50 文件 / 慢速本地模型多轮可合法跑 10-20 分钟）。分析确认真卡死已被更细粒度的守卫提前拦截：

| 守卫 | 阈值 | 覆盖 |
| --- | --- | --- |
| LLM stream idle | 120s | 上游无字节（LLM 挂起） |
| 工具超时 | exec 5m / read 60s / question 10m | 单个工具卡死 |
| 累计失败熔断 | 8 次 | 打地鼠式空转 |
| 子代理轮数上限 | 30 | 失败循环 |

**改动**：
- `config.go` `SubAgentConfig.TimeoutDuration()` 默认 5m → **30m**（wall-clock 只作极端兜底）
- `subagent.go` fallback 同步 30m
- `dev-bin/.p-chat/config.json` 显式 `subagent.timeout: "30m"`
- `configs/config.yaml` 默认模板 + CLI help + AppSettingsModal 提示同步
- **前端 SubAgentCard 兜底改活动感知**：原 6 分钟固定计时会在合法长任务（仍 'start'）时误标 err；现在 `parts.length` 增长即重置计时器，只有**静默**（无新事件）6 分钟才强制 err
- 测试 `TestSubAgentConfig_Timeout` 更新为 30m 断言

**验证**：`go test ./...` 全绿 + `vue-tsc` + `npm run build` 通过。

### 4b.6 前端内存现状（P1 已实施部分 + 现状）

二查确认前端内存治理的既有修复**已在分支上生效**：
- `MAX_LOADED_SESSIONS=4` + `evictColdSessions()`（chat.ts）：切会话时按 LRU 驱逐非当前会话，释放 `sessionMessages` + revoke blob URLs
- `MAX_MESSAGES_PER_SESSION=300` + `capSessionMessages()`：单会话消息上限
- `upl://` 图片实体化（任务一）：历史图片走 `/api/v1/uploads/:id` 相对 URL 按需加载，不内联 base64
- 前端渲染路径（MessageBubble）已兼容相对 URL：`<img>`/lightbox 直接渲染，复制/下载走 `fetchAsBlob`（同源相对 fetch）

**仍待验证**：webview 实际内存曲线需真实复现（`task build:gui` 打包后打开长会话观察 msedgewebview2.exe）。

**改动文件**：
- `internal/subagent/subagent.go`：`Result.Interrupted` 字段；`Run()` silent-close 分支区分"有输出→部分结果返回（err 标记）"与"无输出→硬失败"；`Tool()` handler 给部分结果加 PARTIAL 前缀
- `frontend/src/components/SubAgentCard.vue`：err 卡片带文本 parts 时状态显示"失败（部分内容）"

**测试**：`TestDefault_SilentCloseIsFailure`（改为断言部分内容返回 + Interrupted 标记）、`TestDefault_SilentCloseNoContentIsHardFailure`（无输出仍硬失败）、新增 `TestToolHandler_InterruptedResultCarriesPartialContent`（PARTIAL 前缀 + stats footer 契约）。`go test ./...` 全绿 + `vue-tsc` + `npm run build` 通过。

**可选后续**（本次未做）：
- 把 `subagent.timeout` 从固定值改为"空闲超时"（参考 LLM 客户端 120s streamIdleTimeout）：有事件流动就不算超时，只有卡住（无任何输出）才触发——真正治本，但改动大、风险高，留给下一轮。
- `docs/reports/` 之外：`subagent.timeout` 调大（如 15m）作为临时缓解。

---

## 5. 关联结论

三个问题实际上是**一条链**：

```
数据库图片 base64 爆炸（问题二）
  → 历史加载时 165KB×N base64 进前端 → webview 内存膨胀（问题一）
  → 渲染冻结 → SSE 停止消费 → 服务端阻塞 → 对话卡住（问题一）
  → 用户被迫重启 → 回合重新计时 → 长任务撞上 900s 上限 → todo 中断（问题三）
```

问题二是问题一的**上游放大器**，问题三是**叠加的配置上限**。本次修复：问题一已落地；问题二已实施（图片实体化存储，DB 只存 `upl://<id>` 引用）；问题三已调配置（dev-bin `max_turn_seconds: 3600`）。

## 5b. 二查遗留 bug（2026-08-03，✅ 已修复）

修复主线时二次审查发现并修复 2 个被日志噪音掩盖的真实 bug：

### B1. 子代理临时 store 外键失败 → 历史静默丢失

- **现象**：日志反复 `[subagent] close ephemeral store: constraint failed: FOREIGN KEY constraint failed (787)`（8-03 日志 6 次）
- **根因**：子代理的临时 store（`:memory:`）**从没为它的 SessionID 创建 conversations 行**。`messages.conversation_id` 外键指向不存在的会话 → 所有 `AddChatMessageTo` 在 Flush 时 FK 失败 → **子代理历史全部丢失**（读回 0 行）
- **危害**：① 子代理 auto-compaction 读不到历史 → 长 ReAct 上下文无限膨胀撞 context window；② 本应落盘的 assistant/tool 消息没落盘；③ 每次 Close 日志噪音
- **修复**：`subagent.go` Run() 里 `store.EnsureConversation(buildSubAgentSessionID(subType, taskID), "")`；新增 `buildSubAgentSessionID` helper 与 `buildSubAgentChatRequest` 共用
- **测试**：`TestDefault_SubAgentStoreSeedsConversation`（验证 FK 不再失败 + 读回 round-trip）

### B2. loadHistoryForSend 丢 CompressedSummary 返回 → 压缩后上下文盲区

- **现象**：任务一重构 `loadHistoryForSend` 时误删了 `CompressedSummaryFor(id)` 的返回（统一 `return histMsgs, ""`）
- **根因**：`lastComp > 0` 分支本该带出前文摘要
- **危害**：会话被压缩后，下次 SendMessage 的 system prompt 不再注入 `[前文摘要]`——LLM 对压缩掉的历史完全失明，长会话上下文严重受损（IM 桥 `im_bridge.go` 同路径）
- **修复**：`messages.go` 恢复 `compSummary = h.store.CompressedSummaryFor(id)`
- **测试**：`TestLoadHistoryForSend_CarriesCompressedSummary`（压缩后 summary 必须 round-trip + 压缩行不泄漏回历史）

---

## 6. 关键文件索引

| 模块 | 文件 |
| --- | --- |
| ReAct 主循环 / 工具派发 | `internal/agent/agent.go` |
| sendOrDrop 逃生 | `internal/agent/agent.go:981`（`sendOrDropTimeout`） |
| 大结果截断 | `internal/agent/agent.go:2452-2458` + `auto_continue.go`（`MaxToolResultFullBytes`） |
| 截断缓存 | `internal/agent/tool_result_cache.go` |
| SSE 写超时 / cancelTurn | `internal/server/messages.go`（`writeSSEWithTimeout`） |
| cancel-stream / tool-result 端点 | `internal/server/messages.go` + `server.go` |
| 图片落库 | `internal/agent/attachment.go:167-180`（`ExpandAttachmentsCM`） |
| 图片历史响应 | `internal/server/message_helpers.go:540-553`（`buildMessageResponse`） |
| uploads 设施 | `internal/server/upload.go`（`Upload`/`GetUpload` 已存在） |
| 前端消息 cap | `frontend/src/stores/chat.ts`（`capSessionMessages`） |
| 前端大结果按钮 | `frontend/src/components/ToolCallCard.vue` |
| 前端超长文本 | `frontend/src/components/MessageBubble.vue`（`MAX_MD_INLINE_CHARS`） |
| 前端虚拟化 | `frontend/src/components/ChatWindow.vue`（`content-visibility`） |
