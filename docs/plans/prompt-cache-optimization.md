# 系统提示词与 DeepSeek 缓存优化

## 目标与边界

针对 DeepSeek 官方 Flash、官方仪表盘约 85% 的缓存命中率，减少客户端造成的前缀变动，
并补齐请求级命中统计。真实收益需上线后以相同模型、任务类型和统计窗口比较；本地测试
只能验证请求构造，不能测出供应商实际命中率。

## 已实现

1. 固定应用规则与通用工具说明前置，再放风格、工作模式、项目规则、Skill 目录与运行环境；
   主代理移除重复的工作目录段。
2. Todo、知识库索引、激活 Skill、风格记忆、媒体摘要改为追加式命名快照；未变不追加，
   清空有显式撤销记录，跨回合保留，压缩后补回最新状态。动态值变化不再重写首条 system。
3. 工具调用按流式 index 排序；按助手文本、工具调用、工具结果顺序保存历史。
   已配对的 task 结果恢复后保持 tool 角色，缺少调用的旧记录保留兼容回退。
4. 利用既有 thinking 元数据，DeepSeek 带 tools 请求完整回传已有推理内容；纯思考工具轮
   及跨用户回合的助手历史均覆盖。其他模型不自动注入 DeepSeek 思考字段。
5. 解析官方命中/未命中 token，输出 `[llm/cache]` 请求级日志；多轮用量从取最大值改为累加，
   同一请求重复 usage 帧仍去重。

普通轮次的消息布局：

```text
固定 system → 已发送历史 → 当前用户消息 → 本轮变化的命名快照
→ 助手文本/推理/工具调用 → 工具结果 → 变化的 Todo 快照 → …
```

OpenAI 保持动态快照为原位置 system；Anthropic 使用原位置 user 内容包装，防止 system
被统一提到最前面。固定规则说明同名最新快照覆盖旧状态，快照不代表用户新请求。

## 验证

- `internal/agent/prompt_cache_test.go`：模拟流式接口、乱序并行调用、两次 Todo 变动，
  以及读取数据库后新用户回合，逐条核对先前请求仍为后续请求前缀。
- `internal/agent/todo_guard_test.go`：重复状态、完成/清空状态及旧 system 不变。
- `internal/llm/reasoning_replay_test.go`：两种协议回传思考、动态上下文位置、其他模型隔离。
- `internal/llm/cache_usage_test.go`、`internal/agent/usage_test.go`：usage 别名、零命中、
  缺失字段、跨请求累加及重复帧。
- `internal/server/handler_send_toolrole_test.go`：task 配对角色与旧孤立结果兼容。
- `internal/agent/agent_cancel_test.go`：工具不响应取消时及时结束，同时保留已发出的调用。

2026-09-12 验证结果：`go test -p 1 -count=1 ./...` 全部通过；最后补充取消分支后，
`go test -count=1 ./internal/agent ./internal/server` 及 `go build ./...` 通过。
首次全量并行测试曾触发取消测试的 2 秒工具启动超时，单独连续复测 3 次通过，
随后按包串行全量回归通过；未放宽原测试的超时断言。

## 后续评估

观察新版本 `[llm/cache]` 日志，按相同 provider/model、任务类型区分新会话首轮、工具续轮、
跨用户回合、压缩后首轮。以 `sum(hit) / sum(hit + miss)` 计算加权命中率，同时比较未命中
token 总量与请求次数。日志仅覆盖经过该 Agent/OpenAI 流式路径且返回缓存 usage 的请求；
官方仪表盘可能还计入摘要、识别等辅助模型调用，不能直接要求两边全量一致。

首次请求、新输入、模型/工具集合切换、规则更新、上下文压缩、供应商缓存淘汰均可能产生
未命中。快照历史会占用上下文，通过现有压缩回收；不要靠不断加长固定提示词来抬高百分比。

本次不增加缓存 key/TTL 开关，不改变 temperature、thinking 模式或工具权限。DeepSeek
官方缓存自动生效；后续可基于数据单独评估压缩阈值、减少冗余工具返回、固定 Skill/工具目录
序列化及辅助调用占比，而非直接放宽工具暴露范围。

## 官方依据

- [上下文缓存](https://api-docs.deepseek.com/guides/kv_cache/)
- [思考模式与工具调用](https://api-docs.deepseek.com/guides/thinking_mode/)
- [Chat Completion usage 字段](https://api-docs.deepseek.com/api/create-chat-completion/)
- [Anthropic 兼容接口](https://api-docs.deepseek.com/guides/anthropic_api/)
