# 单项目空白会话复用方案

## 背景

当前点击「新建对话」会无条件创建一条新的 `conversations` 记录。用户未输入任何内容时，连续点击会在同一项目下堆出多条 `(无标题)` 空会话，增加列表噪声，也让用户难以判断哪个才是当前草稿。

目标是把“空白会话”作为项目内唯一草稿：同一项目中没有发送过内容时，重复点击「新建对话」应复用同一条会话；一旦该会话已有用户内容，再点击才创建新的空白会话。

## 空白状态定义

不要用标题判断空白状态。标题可能为空、自动生成、或被用户手动修改。

会话可被复用必须同时满足：

- `project_path` 与当前项目相同，路径比较要做清理，并在 Windows 上忽略大小写。
- 会话 ID 不是 `im:` 前缀，避免复用飞书、微信、TG、QQ 等外部桥接会话。
- `messages` 中没有可见用户消息：`role = 'user'` 且 `is_archived = 0` 的行数为 0。
- `turn_queue` 中没有未完成用户意图：`queued`、`running`、`failed` 的行数为 0。

对外返回状态：

- `conversation_state = "blank"`：满足上述空白条件。
- `conversation_state = "active"`：已有用户消息、未完成队列项，或属于 IM 会话。
- `has_user_messages`、`user_message_count`、`pending_turn_count`：供前端判断、展示和兼容调试。

## 后端实现

`memory.Conversation` 增加活动统计字段，但不改数据库 schema：

- `UserMessageCount`
- `PendingTurnCount`

`ListConversationsLimit`、`ListArchivedConversationsLimit`、`GetConversation` 的查询用子查询聚合这两个计数。这样列表、单会话读取、创建后回读都能得到一致状态。

`POST /api/v1/sessions` 增加可选参数：

```json
{ "reuse_empty": true }
```

当 `reuse_empty=true` 且未显式传 `title` 时，后端在同一把 store 锁内执行：

1. 查找当前项目最新的可复用空白会话。
2. 找到则设为 current 并返回该会话。
3. 找不到则创建新会话。

这个服务端兜底用于处理多窗口、快速连点和前端状态过期场景。旧客户端不传 `reuse_empty` 时保持原有“每次创建新会话”的 API 语义。

## 前端实现

`Session` 类型增加后端返回的活动状态字段，创建请求增加 `reuse_empty`。

`createSession()` 始终调用 `api.createSession({ ..., reuse_empty: true })`：

- 前端不再本地早退复用，避免绕过后端刷新 provider/model/style/plan 等会话 meta。
- 服务端返回已有空白会话时，前端用返回结果替换本地旧记录，避免重复插入。
- 服务端返回新会话时，前端按普通新建流程切换过去。

`loadSessions()` 对同项目多条历史 `blank` 会话做列表折叠，只保留最新的一条，避免旧重复空会话继续污染侧栏。该处理不删除数据库记录。

发送路径需要乐观更新状态：

- 立即发送并插入用户气泡后，当前 session 变为 `active`，`has_user_messages = true`。
- 忙碌时入队后，当前 session 也变为 `active`，`pending_turn_count >= 1`。
- 队列被领取并渲染用户气泡时，同步标记为已有用户消息。

## 不做的事

- 不做 schema migration。
- 不自动删除或归档历史重复空会话。
- 不把标题 `(无标题)` 作为任何判断依据。
- 不复用 IM 桥接会话。

## 验证点

- 同一项目连续新建，返回同一个空白会话。
- 空白会话发送用户消息后，再新建会生成新的空白会话。
- 不同项目各自拥有自己的空白会话。
- queued/running/failed 队列项会让会话变为 active，不会被复用。
- `D:\Repo`、`d:\repo\` 这类路径在 Windows 上不会生成两个项目空白会话。
- 前端发送或入队后立即阻止本地误复用。
