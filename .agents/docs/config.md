# Config 模块

> **位置**：`internal/config/`  
> **依赖**：paths  
> **被依赖**：agent, server, subagent, cli, llm

## 概述

Config 模块管理 P-Chat 的全局和项目级配置：LLM provider/model 设置、服务器参数、沙箱规则、子代理配置、样式主题等。

## 文件结构

| 文件 | 职责 | 关键函数/类型 |
|---|---|---|
| `config.go` | 配置结构定义、加载/保存 | `Config`, `Load()`, `LoadWithProjectRoot()`, `Save()` |
| `manager.go` | 运行时配置管理（热重载） | `Manager`, `Reload()` |
| `migrate.go` | 配置格式迁移 | `migrate()` |

## 核心概念

### 1. 配置文件位置

- **全局配置**：`~/.p-chat/config.json`
- **项目级配置**：`<project>/.p-chat/config.json`（合并到全局配置）

### 2. 配置结构

```go
type Config struct {
    LLM    LLMConfig    // LLM provider + model 设置
    Server ServerConfig  // HTTP 服务器设置
    UI     UIConfig     // 前端主题/布局
    Sandbox SandboxConfig // 命令/文件写入保护模式
    SubAgent SubAgentConfig // 子代理超时/工具过滤
    WorkMode WorkModeConfig // 默认工作侧重点：coding / daily
    Recognition RecognitionConfig // 图片/视频/音频外接识别路由
    Generation GenerationConfig // 媒体生成的应用级默认模型
    Vision VisionRecognitionConfig // 旧版图片识别配置（兼容读取）
}
```

LLMConfig 核心字段：
- `Default` — 默认 provider 名称
- `Providers[]` — 每个 provider 的端点、API key、模型列表
- `Protocol` — "openai" | "anthropic"
- `Models[]` — 每个模型的能力标记；`capabilities.input_modalities` 可显式配置 `image`、`video`、`audio`，旧版 `supports_vision` / `supports_audio` 仅作兼容回退
- `Provider.Vendor` — `volcengine` / `minimax` / `openai` / `custom` 等厂商 preset；凭据仍由 Provider 共享
- `Model.Type` — `llm`（缺省兼容值）或 `media_generation`

### 3. 项目级配置合并

`LoadWithProjectRoot(globalRoot, projectRoot)`：
1. 加载全局 `~/.p-chat/config.json`
2. 若 `projectRoot` 非空，加载 `<project>/.p-chat/config.json`
3. 项目配置覆盖全局配置（shallow merge）

### 4. 运行时热重载

`Manager.Reload()` 重新加载配置文件并更新内存中的 Agent `SetLLM()`。

### 5. WorkModeConfig

`work_mode` 是任务侧重点，不是说话风格。它与 `style` 正交：

- `style` 控制“怎么说”和对应的风格记忆注入；可设为 `off`，关闭后不注入风格 prompt，也不注入风格记忆。
- `work_mode.default` 控制“优先做什么”，目前固定为 `coding` / `daily` 两种。
- 默认值是 `coding`，保证旧用户升级后仍保持编程助手的行为。
- `WorkMode.Normalize()` 会把空值或未知值回落到 `coding`；`IsValid()` 只接受 `coding` 和 `daily`。

全局默认通过 `/api/v1/config` 读写；单会话覆盖值存放在 `conversations.metadata.work_mode`。

### 6. RecognitionConfig

`recognition.routes` 为 `image`、`video`、`audio` 分别配置外接识别路由。每条路由包含：

- `enabled` — 是否启用该能力。
- `provider` / `model` — 使用已配置 LLM provider 中的模型；任一缺失时该能力不可用。
- `timeout_seconds` — 单次识别超时，默认 60 秒。
- `max_bytes` — 单个媒体文件大小上限，默认 10 MiB。

单会话选择保存在 `conversations.metadata.enabled_recognition_capabilities`，它是媒体类型数组。只有系统路由完整、协议受支持、目标模型明确声明对应输入能力且会话明确选中的能力，才会暴露给 Agent。模型的 `input_modalities: []` 是明确的“无媒体能力”，不会再被模型名启发式判断覆盖；字段完全缺失时，图片仍保留旧版兼容判断。图片兼容旧版 `vision_recognition`、`use_image_recognition` 与 `supports_vision`；V9 升级步骤把旧开关迁移为 `image` 选项。音频和视频外接识别当前要求 OpenAI 兼容协议。

`media_recognize` 是模型可见的统一入口：根据上传引用解析图片、音频或视频，并按媒体类型选择配置路由；图片在没有独立路由时仍可 fallback 到当前视觉模型。旧名 `image_recognize` 只作为隐藏兼容别名存在。

### 7. GenerationConfig

媒体生成配置定义在 `generation.go`。媒体模型通过
`generation.operations` 显式声明规范能力；每项能力有独立的 `endpoint`、
`query_endpoint`、`timeout_seconds` 和 `default_params`。Volcengine、MiniMax、
OpenAI 有可编辑的默认端点，用户配置始终优先。

顶层 `generation.defaults` 按 operation 选择应用默认 Provider/Model；会话只在
`conversations.metadata.enabled_generation_operations` 保存能力开关，不再覆盖生成模型。
能力默认关闭，开启后直接使用该 operation 的应用默认模型。旧 metadata 中的
`generation_model_overrides` / `generation_prompt_assist` 仅兼容读取且不会参与运行。
完整结构与运行边界见
[媒体生成首版实现说明](../../docs/plans/media-generation-implementation.md)。

### 8. SubAgentConfig

`subagent` 控制 `task` 子代理：
- `cache_ttl` / `timeout` — 子代理缓存和显式 wall-clock 超时策略。
- `allowed_tools` / `denied_tools` — 在子代理安全集内进一步收窄可见工具。

持久化配置中的旧工具名会在运行时映射到 canonical 名称：`image_recognize → media_recognize`、`read_docx/read_pdf → read_file`、`start_process → exec_command`，无需修改现有用户配置。

单会话可通过 `conversations.metadata.sub_agent_model_enabled`、`sub_agent_provider`、`sub_agent_model` 指定子代理模型。默认关闭时继承父对话 provider/model；用户在会话设置里开启自定义后才选择模型。子代理最终模型优先级是：`task` 工具显式参数 → 专用 agent 定义的模型 → 会话自定义 → 父对话模型。

## 修改指南

### 要添加新的配置字段
1. 在 `config.go` 的结构体中添加字段
2. 在 `manager.go` 的 `Reload()` 中处理新字段
3. 更新前端设置 UI（若需暴露给用户）

### 要修改配置加载逻辑
- `Load()` / `LoadWithProjectRoot()` (config.go)

### 要修改配置格式迁移
- `migrate.go`

## 相关模块

- [agent.md](agent.md) — 使用 Config 构建 LLM 客户端
- [server.md](server.md) — 使用 Config 启动 HTTP 服务器
- [llm.md](llm.md) — Provider/Model 配置的使用方
