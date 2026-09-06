# 媒体生成首版实现说明

状态：已实现首个可运行纵向切片（2026-09-06）。完整产品决策见
[media-generation-decision-map.md](media-generation-decision-map.md)。

## 已实现范围

- Provider 作为厂商账号和共享凭据边界，增加 `vendor`；同一个 API key 下可同时添加
  `llm` 与 `media_generation` 两类模型。
- 媒体模型显式选择文生图、图生图、文生视频、图生视频、视频生视频、文字转语音、
  文生音乐、文生音效、音频转音频能力。每项能力分别配置创建端点、查询端点、超时和
  默认参数。
- 内置 Volcengine、MiniMax 和 OpenAI 文生图端点预设；端点输入框可按模型、按能力覆盖。
  OpenAI 图生图需要 multipart，首版不自动填充端点，避免把它误路由到文生图接口。
- 应用设置为每项能力指定默认模型；会话设置只负责独立开启能力，开启后直接使用该能力
  的应用默认模型。所有会话能力默认关闭。
- 模型只看到三个稳定工具：`generate_image`、`generate_video`、`generate_audio`。
  工具 schema 中的 operation enum 会按会话开关收窄。
- 工具执行入口再次校验通用工具允许列表；三个媒体 handler 还会再次校验 operation、
  会话开关、模型路由和输入类型。关闭后返回 `blocked` 与“已在当前会话关闭”提示，
  不会调用厂商 API；该状态会原样贯通 SSE、实时卡片和历史消息重载。
- 图生图、图生视频、视频生视频和音频生音频只从工具参数接收会话内 `input_refs`。
  服务端验证附件/生成资产属于当前会话后才读取文件并在 adapter 内编码；工具参数不接收
  base64、URL 或文件路径。
- 对话 LLM 根据用户要求和上下文直接提供最终生成提示词；需要理解附件时，可先用当前
  视觉模型或 `media_recognize` 获取事实观察。工具不再暴露二次提示词增强参数。
- 同步响应和异步查询统一在 `HTTPExecutor` 内处理。厂商返回的 URL、data URI、base64
  或 MiniMax 十六进制音频最终都实体化到 `~/.p-chat/generated/`。
- 模型可传的生成选项使用固定白名单和取值上限；`callback_url`、厂商批量参数等控制字段
  只能写入管理员维护的模型默认参数。下载结果 URL 时不会携带 Provider API key。
- 媒体输入按能力限制引用数（图生图最多 4 个，其余媒体输入能力 1 个）并限制单次聚合
  读取 32 MiB；单个远程结果最多 128 MiB，单次响应最多实体化 4 个、合计 256 MiB。
  当前执行器全局单并发，生成目录默认设 20 GiB 写入配额，达到上限后明确失败，避免
  多会话或供应商异常响应耗尽内存、网络与磁盘。
- 远程资产下载会禁止代理，校验跨域目标必须解析到公网地址，并把本次校验所得 IP 固定到
  实际 TCP 连接；重定向逐跳重新校验并固定，避免 Provider 凭据泄漏、SSRF 和 DNS 重绑定。
- 工具结果只保存稳定资产 ID 和同源 `/api/v1/generated/:id` 地址；聊天卡片原生展示
  图片、HTML5 video/audio 控件和下载入口，历史消息重载后仍可使用。

## 配置入口

GUI 推荐流程：

1. 打开“应用设置 → LLM 提供商”，新建或编辑 Provider，选择厂商预设并填写共享 API key。
2. 在该 Provider 下添加模型，类型选择“媒体生成模型”，勾选能力；需要时覆盖该能力的
   创建/查询端点和超时。
3. 打开“应用设置 → 系统 → 媒体生成”，为每项能力选择应用默认模型。
4. 在对话框“会话设置 → 媒体生成”中开启本会话允许的能力。模型始终使用第 3 步配置的
   应用默认值。

若编辑模型时准备移除一项正被 `generation.defaults` 引用的能力，配置 API 返回 `409`；
需要先在“系统 → 媒体生成”修改或清除该默认项，不会静默改写用户配置。

运行时配置落在 `~/.p-chat/config.json`，结构示例：

```json
{
  "llm": {
    "providers": [
      {
        "name": "volc-main",
        "vendor": "volcengine",
        "protocol": "openai",
        "base_url": "https://ark.cn-beijing.volces.com/api/v3",
        "api_key": "<secret>",
        "models": [
          { "name": "doubao-chat", "type": "llm", "default": true },
          {
            "name": "video-model-id",
            "type": "media_generation",
            "generation": {
              "adapter": "volcengine",
              "operations": {
                "text_to_video": {
                  "endpoint": "/contents/generations/tasks",
                  "query_endpoint": "/contents/generations/tasks/{task_id}",
                  "timeout_seconds": 600
                },
                "image_to_video": {
                  "endpoint": "/contents/generations/tasks",
                  "query_endpoint": "/contents/generations/tasks/{task_id}",
                  "timeout_seconds": 600
                }
              }
            }
          }
        ]
      }
    ]
  },
  "generation": {
    "defaults": {
      "text_to_video": { "provider": "volc-main", "model": "video-model-id" }
    }
  }
}
```

会话只保存能力开关：

```json
{
  "enabled_generation_operations": ["text_to_video", "image_to_video"]
}
```

旧会话 metadata 中的 `generation_model_overrides` 和 `generation_prompt_assist` 为兼容字段，
不会再参与模型路由或提示词处理。

## 运行时边界

```text
会话 capability 开关
  ├─ Agent：隐藏工具或收窄 operation enum
  └─ Tool dispatcher：通用 allowed_tools 硬校验
       └─ generate_* handler：operation 开关 + 路由 + input_ref 硬校验
            └─ Generation Executor：解析会话归属 → 厂商调用 → 查询 → 本地实体化
```

`input_refs` 是授权引用，不是数据容器。当前轮上传由 Agent 给模型附加可用 ID 指引；
历史生成资产的 ID 存在工具结果中。解析器只接受当前会话拥有的上传或生成资产，媒体类型
必须满足 operation 的输入要求。

## API 与关键文件

- `GET /api/v1/generation/options?session_id=<id>`：返回规范能力矩阵、应用默认、会话启用状态
  和不可用原因。
- `GET /api/v1/generated/:id`：读取本地实体化后的媒体资产。
- Provider/Model CRUD 和 `/api/v1/config/system` 已包含 `vendor`、`type`、`generation`。
- 配置模型：`internal/config/generation.go`
- 工具硬边界：`internal/tool/media_generation.go`
- 执行与资产存储：`internal/generation/executor.go`
- 会话授权注入：`internal/agent/agent.go`
- 附件 ID 指引：`internal/agent/attachment.go`
- 前端配置/会话/展示：`AppSettingsModal.vue`、`InputArea.vue`、`ToolCallCard.vue`

## 首版限制与后续阶段

- 首版在一次工具调用内完成异步查询，页面会显示工具执行中；尚未实现独立的 durable
  `generation_jobs` 表、进程重启续查、取消和后台通知。下一阶段再增加
  `generation_status/wait/cancel` 与任务恢复器。
- 当前 adapter 覆盖 Volcengine、MiniMax、OpenAI-compatible 的常见 JSON 调用形状。
  厂商特有的 multipart、签名、复杂参考图角色或新增响应字段应留在 adapter 内扩展，
  不得把任意 vendor options 直接暴露给模型。
- 异步响应若只返回任务 ID、但该能力没有配置 `query_endpoint`，工具会返回配置错误，
  不会把没有资产的任务误报为成功。
- 生成资产当前按会话归属持久保存并受目录配额保护，但没有自动清理策略；后续应增加
  引用计数、孤儿扫描和用户可控保留期。
- `generation.require_confirm` 是预留配置，首版尚未接入计费确认状态机；真正生效前不能
  作为安全边界。当前授权边界是会话 capability 硬开关。
