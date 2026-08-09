# 重试机制梳理与设计修订报告（turn_timeout / MaxTurnRetries / todo 续跑）

> 状态：**设计梳理，未实施**（仅分析，未改动任何代码）
> 关联：`docs/reports/webview-memory-hang-report.md`（该报告描述了前端卡死 → SSE 停止消费 → 回合被 MaxTurnSeconds 兜底终止的场景；本报告是其中"重试/恢复机制"部分的补充延伸）
> 涉及文件：`internal/server/messages.go`、`internal/server/auto_resume.go`、`internal/config/config.go`、`internal/agent/auto_continue.go`、`internal/agent/llm_retry.go`、`internal/agent/agent.go`、`internal/agent/breaker.go`

---

## 1. 背景与目标

### 1.1 触发问题

用户对话中出现终态错误帧：

```
回合超出最长执行时间被终止: context deadline exceeded
（ErrorKind: "turn_timeout"）
```

初步定位结论（见 1.2 现状）：该错误由 L3 回合硬时限 `MaxTurnSeconds`（默认 900s）触发，且当次重试预算 `MaxTurnRetries`（默认 2）已耗尽，服务端放弃续跑、发出终态帧。

### 1.2 本次修订目标（用户需求）

1. **耗尽 MaxTurnSeconds 时限且存在未完成 todo_list 时，应当重新触发重试**——语义等同于用户手动发送"继续"，即使 `MaxTurnRetries` 预算已耗尽。
2. **`MaxTurnRetries` 是"同一条重试链（同一次 `SendMessage` 调用）"内的连续重试计数（1→2→3 递增）**，不是整个对话的全局异常次数限制；计数与"用户消息"这一概念**无绑定**——归零发生在每次**新的 `SendMessage` 调用**（每个 HTTP 请求 = 每条新重试链的起点），而非"用户发新消息"。
3. **`MaxTurnRetries` 仅作为"无 todo 的异常重试"次数兜底**；todo 驱动的续跑（同一任务、同一段"继续"）不消耗该预算，仅受 T3 无进展熔断约束。

### 1.3 机制的大白话理解（"同一单换骑手"）

用骑手送单来理解 `MaxTurnRetries`：

- 用户发一条消息 = **下一单**（一个任务）。
- 骑手（执行回合）跑这一单，跑到 `MaxTurnSeconds`（900s）超时 → **这一单没跑完** → 系统自动**换一个骑手**（触发一次"继续"），重新跑**同一单、同一段指令**。
- 换第一个骑手 = 第 1 次重试（`MaxTurnRetries=1`）；又超时/异常 → 再换一个 = 第 2 次重试（`MaxTurnRetries=2`）…… **计数是同一单内连续递增的（1→2→3），不是每次超时清零**。
- 默认最多换 2 个骑手（`maxRetries=2`）；2 个都接不住 → 报"回合超出最长执行时间被终止"（终态 `turn_timeout`）。
- 这一单跑完 / 终止（重试链结束：预算耗尽 / 终态错误 / 完成）后，系统**重新接单**（新的 `SendMessage` 调用）→ 计数归零，新单又有 2 次换骑手机会——计数跟"重新接单"绑定，不跟"用户发消息"绑定。
- **关键差异**：如果骑手手里有**未完成的任务清单（todo）**，系统会一直换骑手继续跑（相当于你一直按"继续"），**不受 2 次上限约束**——直到清单完成，或连续换了 3 个骑手清单都没进展（T3 熔断）才停下来问你。

---

## 2. 现状机制梳理（精确到代码位置）

### 2.1 三层防护总览

| 层级 | 机制 | 代码位置 | 配置（默认） | 触发条件 | 预算 |
| --- | --- | --- | --- | --- | --- |
| L1 | 上游 LLM 阶梯重试 | `internal/agent/llm_retry.go` | `llm_retry_backoffs`（默认 `[5,60,180,300,600]` 秒） | `rate_limit` / `server_error` / network / timeout | 由 backoff 列表长度决定，**脱离回合预算**（走 abortCtx，只受用户取消限制） |
| L2 | 单轮流停滞看门狗 | `internal/agent/agent.go` | `round_stream_stall_timeout`（默认 180s） | 一轮内完全收不到任何 chunk | 转重试 / error+done |
| L3 | 回合硬时限 + 服务端自动续跑 | `internal/server/messages.go` | `max_turn_seconds`=900、`max_turn_retries`=2 | 整个回合（SendMessage→done）超墙钟上限 | `MaxTurnRetries`（attempt 计数） |

### 2.2 L3 内部：SendMessage 重试循环

**位置**：`internal/server/messages.go`

- **L283-285**：`retryNotice` 仅在 `attempt < maxRetries` 时生成，文案 `"⏱ 回合超出最长执行时间，自动重试（第 %d/%d 次）——相当于自动发送"继续"…"`。
- **L289**：`res := h.respondSSE(c, stream, id, provider, model, retryNotice)`。
- **L294**：循环退出条件 `if res != turnStreamRetry || attempt >= maxRetries { break }`。
- **L310-327**：续跑时重载持久化会话 → 追加用户风格"继续"提示（`BuildAutoResumePrompt(id, resumeReason)`）→ `TodoMode = TodoModeResume` → 以全新 `MaxTurnSeconds` 预算再跑一轮。
- **attempt 作用域**：`for attempt := 0; ; attempt++` 是**每次 SendMessage 调用（每个 HTTP 请求 = 每条重试链）新建**的循环，天然从 0 开始；会话锁全程持有。
  - ⚠️ **准确机制**：同一条重试链内 attempt 是**递增**的——首次执行 `attempt=0`，第 1 次自动"继续" `attempt=1`，第 2 次 `attempt=2`……直到 `attempt >= maxRetries` 终止。**不是**每次超时都归零重计；归零只发生在**每次新的 `SendMessage` 调用**（新请求 = 新重试链）时——计数绑定请求/重试链，与"用户消息"概念**无关联**。

### 2.3 L3 内部：respondSSE 的三个续跑决策分支

**位置**：`internal/server/messages.go`（`respondSSE` 定义于 L530）

| 分支 | 位置 | 条件 | 行为 | 受 T3 无进展熔断保护？ |
| --- | --- | --- | --- | --- |
| (a) deadline 超时续跑 | L596 | 流关闭无 done 帧 + `ctx.Err()==DeadlineExceeded` + **`retryNotice != ""`** | 写 `turn-retry` notice + `SessionStatus:"retry"` → 返回 `turnStreamRetry`；否则发终态 `turn_timeout` 帧 | **否**（不经过 `shouldAutoResume`） |
| (b) retryable error + todo 续跑 | L646 | `chunk.Done && chunk.Error != "" && retryNotice != "" && IsRetryableErrorKind && HasPendingTodos` | 经 `shouldAutoResume` → `turnStreamRetry` | **是** |
| (c) 正常完成但 todo 未完成续跑 | L646 之后 | `chunk.Done && chunk.Error == "" && retryNotice != "" && HasPendingTodos` | 经 `shouldAutoResume` → `turnStreamRetry`（不发出 done 帧，流保持打开） | **是** |

**关键事实**：三个分支**共享同一个 `retryNotice != ""` 预算闸门**（由 SendMessage 的 `attempt` 控制）。预算耗尽后，(a)(b)(c) 全部失效。

### 2.4 T3 无进展熔断（`shouldAutoResume`）

**位置**：`internal/server/auto_resume.go`

- 每个 session 维护 `resumeTracker`（快照 = 未完成 todo ID 指纹）。
- 续跑前后快照**无变化**则累计 `noProgressCount`，达到 `maxNoProgressResumes=3` 后熔断，返回 `(false, notice)`，链转终态帧并提示用户手动介入。
- 重试链结束（SendMessage 循环 break：成功 / 终态错误 / 预算耗尽）时 `h.resumeTrackers.Delete(id)` 删除 tracker；下一条用户消息重新 `LoadOrStore` 新建，计数清零。
- **现状缺口**：分支 (a) deadline 超时不经过 T3 —— 若未来放开其预算限制，必须接入，否则无限续跑死循环。

### 2.5 六层重试全景（完整梳理）

> 本节将项目内**所有**与"重试 / 续跑 / 熔断"相关的机制一次性列出，修正早期"重试机制本质上都是同一个逻辑"的误判——**至少存在三种本质不同的语义**（见 2.5.3）。

#### 2.5.1 六层机制总览

| 层 | 机制 | 代码位置 | 配置（默认） | 触发条件 | 预算 / 归零 |
| --- | --- | --- | --- | --- | --- |
| L1 | 上游 LLM 阶梯重试 | `internal/agent/llm_retry.go` | `llm_retry_backoffs`（默认 `[5,60,180,300,600]`s） | `rate_limit` / `server_error` / network / timeout | 次数 = `len(backoffs)`，总尝试 = `len+1`；走 `abortCtx` **脱离回合预算**，仅被用户取消打断；**同回合内固定次数，非换骑手** |
| L2 | 单轮流停滞看门狗 | `internal/agent/agent.go`（`round_stream_stall_timeout`） | 默认 180s | 一轮内完全收不到任何 chunk | 注入错误 → 转入 L1 重试；每轮独立 |
| L3 | 回合硬时限 + 服务端自动续跑 | `internal/server/messages.go` | `max_turn_seconds`=900、`max_turn_retries`=2 | 整个回合（SendMessage→done）超墙钟上限 | `attempt` 同请求内连续递增（1→2→3），**新 SendMessage 请求归零（与用户消息无绑定）**；todo 在飞时经 T3 续跑 |
| T3 | 无进展熔断 | `internal/server/auto_resume.go` | `maxNoProgressResumes`=3 | 续跑前后未完成 todo 快照无变化 | 链结束后用户新消息删除 tracker 归零；熔断后提示手动介入 |
| ⑤ | 回合内工具失败熔断 | `internal/agent/agent.go`（`sameToolErrCount` / `cumToolErrCount` / `stuckStreak`） | `sameToolErrMax`=4、`CumToolErrMax`=8 | 同工具连错 4 次 / 回合内累计 8 次 / 无进展循环 | **ChatWithTools 局部变量 → 回合边界重置** |
| ⑥ | 跨回合工具失败熔断（T4） | `internal/agent/breaker.go`（`breakerState`，Agent 级 `breakers sync.Map`，按 session 隔离） | `crossTurnSameToolMax`=4、`CumToolErrMax`=8 | 同"命令签名"跨回合连败 4 次 / 累计失败 8 次 | **自动续跑链内不清零**；用户新消息 / 任一熔断触发 / 链结束 `ClearBreakerState` 时清零 |

#### 2.5.2 层次关系（嵌套 / 并列 / 正交）

```
L3 回合 attempt（"换骑手"）────────────── 每次尝试 = 全新 MaxTurnSeconds 预算
 └─ L2 停滞看门狗（180s 无 chunk）
     └─ L1 LLM 阶梯重试（5s→60s→…→600s，abortCtx 不计入回合预算）
         └─ 成功 / 工具调用 → ⑤ 回合内熔断（局部计数）
                         └─ ⑥ 跨回合熔断（Agent 级累计，按 session 隔离）
T3 无进展熔断 ── 与 L3 正交：L3 决定"要不要换骑手"，T3 决定"todo 驱动的换骑手能否继续"
```

- **L1 ⊂ L2 ⊂ L3**：L2 停滞 → 进入 L1 重试；L1+L2 的整个"原地重试"阶段运行在 L3 的**一个 attempt 内**。
- **⑤ ⊂ ⑥**：⑤ 是回合内快照，⑥ 是跨回合累计（my-blog 死循环的修复，见 `breaker.go` 头注释）；**⑥ 触发时同步重置 ⑤ 的局部计数**，⑤ 触发时也同步清 ⑥（避免双消息，见 2.5.4）。
- **T3 与 L3 正交**：T3 只约束 todo 驱动的续跑链，不参与 L3 的 `attempt` 预算。

#### 2.5.3 三种本质不同的语义（修正早期误判）

| 语义 | 代表机制 | 特征 | 例子 |
| --- | --- | --- | --- |
| A. 原地缓一缓再跑 | L1、L2 | **同一骑手、同一单**内固定次数重连；等待不计入回合预算 | LLM 瞬时 5xx，等 30s 再请求一次 |
| B. 换骑手 | L3 | attempt 递增，**每次全新回合预算**，重跑同一段指令 | 900s 超时 → 自动"继续" |
| C. 平台熔断 | T3、⑤、⑥ | **不再重试**，强制转总结 / 换方式 / 提示用户 | 同命令跨回合连败 4 次 → 注入"不要重试，改用其他方式" |

早期"重试本质上都是同一个逻辑（同链递增、新请求归零）"只对 **B（L3）** 成立；**A 是同回合内固定次数且不消耗回合预算**，**C 根本不再重试**。

#### 2.5.4 ⑤ 与 ⑥ 的协同验证（精确到行）

```
工具失败 → agent.go:2851 breaker.recordFailure(sig)   ← 每次失败喂入跨回合计数
无失败轮次 → agent.go:2880 breaker.resetStreak()        ← 只清 streak，保留 cum
回合内同工具连错 → agent.go:2882 sameToolErrCount >= sameToolErrMax(4)
回合内累计失败 → agent.go:2935 cumToolErrCount >= CumToolErrMax(8)
intra-turn 熔断触发 → agent.go:2907 / 2940 breaker.reset()   ← 同步清跨回合，避免双消息
cross-turn 熔断触发 → agent.go:2966 / 2981 breaker.reset()
                     + resetGuardCounters(...) + resetSameToolErr(...)  ← 同时清 ⑤ 局部计数
新回合开始 → agent.go:1075 a.breakers.LoadOrStore(sessionID, &breakerState{})
            agent.go:1077-1078 if !isAutoResumeTurn(req) { breaker.reset() }
```

- **归零判定**：`isAutoResumeTurn`（`breaker.go:97`）= `ClientMsgID==0 && TodoMode==resume`——服务端注入的自动续跑**不清零**（跨回合累计正是 T4 的目的）；用户真实新消息清零（新意图新预算）。
- **命令签名归一化**：`normalizeToolFailureSig`（`breaker.go`）对 `exec_command` 剥掉 `-run` 过滤器、引号段、数字、`> 重定向`、`& type 尾巴`、管道尾巴——"同命令换参数"（`test_out3.txt` → `test_all.txt`）视为**同一次失败**，堵住 LLM 换参数绕过熔断的路径（my-blog 810 次失败的根因）。
- **状态清理**：retry 链完全结束后服务端调 `ClearBreakerState(sessionID)`（`breaker.go:159`）删除 session 条目，防进程生命周期内死 session 堆积；下次用户 turn 的 `LoadOrStore` 重建新状态。

---

## 3. 现状与目标的差距分析

### 3.1 差距一：todo 驱动续跑被 `MaxTurnRetries` 预算错误消耗

现状三个分支共享 `attempt < maxRetries` 预算闸门：

```
用户消息 → attempt=0 → 超时 → 重试(1/2)
         → attempt=1 → 超时 → 重试(2/2)
         → attempt=2 → 超时 → 终态 turn_timeout   ← todo 还在飞，却被预算截断
```

**问题**：todo 驱动的续跑（语义 = 用户手动"继续"）本应不受限次约束，却与无 todo 的异常重试共享预算；一次 LLM 错误风暴就能把预算耗尽，之后 todo 任务被硬切。

### 3.2 差距二：`MaxTurnRetries` 语义模糊

- 现状它同时服务"无 todo 异常重试"与"有 todo 任务续跑"两类完全不同的场景，且在一次 SendMessage 内跨异常累积（超时用了 1 次、LLM 错误又用 1 次……）。
- 目标语义：**同一条重试链（同一次 `SendMessage` 调用）内，`MaxTurnRetries` 是"无 todo 的异常重试"的次数兜底**，从 1 开始递增，直到上限；**todo 在飞 → 不消耗预算、持续续跑**（受 T3 保护）；归零发生在**新的 `SendMessage` 调用**（新请求 = 新重试链），与"用户消息"无绑定。

### 3.3 差距三：deadline 分支缺 T3 熔断保护

一旦放开 (a) 的预算限制，若无 `shouldAutoResume` 兜底，todo 永不完成时将无限重启，形成真正的死循环（每次都以全新 900s 预算重启）。

---

## 4. 目标设计

### 4.1 预算语义重定义

```
MaxTurnRetries = 同一任务（同一条用户消息）内，"无 todo 异常重试"的次数兜底
  ├─ 首次执行 attempt=0（MaxTurnSeconds 全新预算）
  ├─ 超时/异常 → 自动触发"继续"（同一任务、同一段指令、更换执行回合 = 换骑手）
  │     → attempt=1（第 1 次重试）→ 再失败 → attempt=2（第 2 次重试）→ ……
  ├─ 本质：同一单没跑完 → 换一个骑手继续跑，发送的始终是同一段"继续"
  ├─ 达到上限 attempt >= maxRetries（默认 2）→ 不再自动重试 → 终态 turn_timeout
  ├─ 只被"无 todo 的 deadline/异常重试"消耗；todo 驱动续跑不消耗
  └─ 归零时机：每次新的 SendMessage 调用（新请求 = 新重试链）时循环重建，attempt 从 0 开始

todo 续跑（deadline / retryable-error / 正常完成但 todo 未完成）
  ├─ 不消耗 MaxTurnRetries（同任务同"继续"，无次数上限）
  ├─ 无条件续跑（语义 = 用户手动"继续"）
  └─ 仅受 T3 无进展熔断（shouldAutoResume，连续 3 次无进展）约束
```

### 4.2 新决策表（respondSSE / SendMessage 修订后）

| 场景 | 条件 | 行为 |
| --- | --- | --- |
| deadline 超时 + 有 todo | 流关闭无 done + `DeadlineExceeded` + `HasPendingTodos` | 经 `shouldAutoResume`：通过 → 写 notice + `turnStreamRetry`；熔断 → 写停止 notice + 终态 `turn_timeout` 帧 |
| deadline 超时 + 无 todo + 预算内 | `attempt < maxRetries` | 写"自动重试（第 X/Y 次）"notice + `turnStreamRetry` |
| deadline 超时 + 无 todo + 预算耗尽 | `attempt >= maxRetries` | 终态 `turn_timeout` 帧（现状不变） |
| retryable error + 有 todo | `chunk.Done && Error != "" && retryable && HasPendingTodos` | 经 `shouldAutoResume` → `turnStreamRetry`（现状不变，仅预算闸门放宽为"todo 存在即可"） |
| 正常完成但 todo 未完成 | `chunk.Done && Error == "" && HasPendingTodos` | 经 `shouldAutoResume` → `turnStreamRetry`（现状不变，仅预算闸门放宽） |
| 正常完成且 todo 全终态 | — | 正常 done 帧（现状不变） |

### 4.3 状态机（一次 SendMessage 调用）

```
用户消息
  └─ attempt=0（新预算 900s）
       ├─ 正常 done + todo 终态 ──────────────► 终态帧 done（结束）
       ├─ 正常 done + todo 未完成 ──T3 通过──► 续跑（预算不消耗） ─┐
       ├─ retryable error + todo ────T3 通过──► 续跑（预算不消耗） ─┤ 回到 attempt+1
       ├─ deadline + todo ──────────T3 通过──► 续跑（预算不消耗） ─┘（每次全新 900s 预算）
       ├─ deadline + 无 todo + attempt<Max ──► 续跑（消耗预算，文案 第X/Y次）
       ├─ deadline + 无 todo + attempt≥Max ──► 终态 turn_timeout 帧
       └─ T3 熔断（连续 3 次 todo 无进展）────► 停止 notice + 终态帧
```

---

## 5. 改动点规划（**未实施**，仅设计）

> 以下为实施时的预期改动，供后续编码参考；本次不写代码。

### 5.1 `internal/server/messages.go` — SendMessage 循环

- **L283-285**：`retryNotice` 生成条件从 `attempt < maxRetries` 放宽为 `attempt < maxRetries || agent.HasPendingTodos(id)`；文案分流：
  - 预算内：`"⏱ 回合超出最长执行时间，自动重试（第 %d/%d 次）——相当于自动发送"继续"…"`
  - 预算外（todo 驱动）：`"⏱ 回合超出最长执行时间，检测到未完成任务，自动继续…"`
- **L294**：循环退出条件简化为 `if res != turnStreamRetry { break }`（respondSSE 已做完整终态决策：预算外 + 无 todo 返回 `turnStreamEnded` 并已发终态帧；预算外 + 有 todo 返回 `turnStreamRetry`）。
- **L310-327**：续跑组装逻辑不变（`TodoModeResume`、`BuildAutoResumePrompt`），仅提示语 reason 复用现有 `HasPendingTodos` 分流（"任务尚未完成" vs "中断"）。

### 5.2 `internal/server/messages.go` — respondSSE deadline 分支

- **L596**：`retryNotice != ""` 已覆盖"预算内 或 有 todo"，判断不变；但**新增 T3 保护**：
  ```go
  if errors.Is(err, context.DeadlineExceeded) && retryNotice != "" {
      if resume, stopNotice := h.shouldAutoResume(sessionID); resume {
          // 写 notice + SessionStatus:"retry" → turnStreamRetry（现状）
      } else {
          // 写 stopNotice + 终态 turn_timeout 帧（新：deadline 分支接入 T3）
      }
  }
  ```
- (b)(c) 分支无需改动：`retryNotice != ""` 新语义自动使 todo 驱动续跑脱离预算限制，且已有 T3 保护。

### 5.3 `internal/server/auto_resume.go`

- 无需改动。T3 熔断逻辑、`maxNoProgressResumes=3`、快照算法保持现状即可兜住"无限续跑"。

### 5.4 `internal/config/config.go` / 文档注释

- `MaxTurnRetries` 字段注释需更新为"单次异常事件的次数兜底，todo 驱动的续跑不消耗该预算"。

### 5.5 测试影响

- `internal/server/auto_resume_test.go` / `messages_autoresume_test.go` 中"预算耗尽后终态帧"的断言需更新：
  - 预算耗尽 + **有 todo** → 期望 `turnStreamRetry`（新行为）
  - 预算耗尽 + **无 todo** → 期望终态 `turn_timeout`（不变）
- 新增用例：deadline + todo + 预算耗尽 → 续跑；deadline 分支 T3 熔断 → 终态帧 + stopNotice；预算外续跑文案。

---

## 6. 边界情况与风险

| # | 风险 | 缓解 |
| --- | --- | --- |
| 1 | deadline+todo 无限续跑死循环（每次全新 900s 预算） | 接入 T3 无进展熔断：todo 集合连续 3 次续跑无变化 → 熔断 + 提示用户手动介入 |
| 2 | 大 todo 任务长期占用 session 锁 / HTTP 长连接 | SSE 本就是长连接；续跑预算仍受 `MaxTurnSeconds` 每次限制，且用户可随时 cancel-stream；可接受 |
| 3 | 预算外续跑文案出现"第 X/Y 次"误导（X>Y） | 文案分流：预算外改"检测到未完成任务，自动继续…"，不复用次数文案 |
| 4 | 前端对连续 `SessionStatus:"retry"` 事件的展示 | 现有前端已支持 retry 状态；多次续跑只是重复该状态，无需改动 |
| 5 | 无 todo 场景行为变化 | 完全向后兼容：无 todo 时仍走原 `attempt < maxRetries` 预算逻辑 |
| 6 | `MaxTurnRetries=0`（用户显式关闭）时的 todo 场景 | 新语义下 todo 存在仍续跑（不受 0 限制）；`MaxTurnRetries=0` 仅关闭"无 todo 的异常重试"——需在报告/文档中明确 |

---

## 7. 结论

本次修订把"重试预算"从**全局共享的次数上限**重构为**同一条重试链（同一次 `SendMessage` 调用）内、无 todo 异常重试的连续兜底**（1→2→3 递增，新请求才归零），并将"有未完成 todo 的续跑"（语义 = 用户手动"继续"，同一段指令反复重跑）从预算中剥离，改为**任务驱动、无条件续跑、仅受 T3 无进展熔断约束**。这同时消除了 3.1 中"todo 在飞却被预算硬切"的核心缺陷，并为 deadline 分支补齐了防死循环保护。

预期效果：用户看到的 `回合超出最长执行时间被终止: context deadline exceeded` 终态错误将显著减少——只要任务列表还在，服务端就会持续以"继续"方式续跑，直到任务完成或 todo 无进展熔断。
