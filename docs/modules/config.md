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
}
```

LLMConfig 核心字段：
- `Default` — 默认 provider 名称
- `Providers[]` — 每个 provider 的端点、API key、模型列表
- `Protocol` — "openai" | "anthropic"
- `Models[]` — 每个模型的能力标记（vision, thinking）

### 3. 项目级配置合并

`LoadWithProjectRoot(globalRoot, projectRoot)`：
1. 加载全局 `~/.p-chat/config.json`
2. 若 `projectRoot` 非空，加载 `<project>/.p-chat/config.json`
3. 项目配置覆盖全局配置（shallow merge）

### 4. 运行时热重载

`Manager.Reload()` 重新加载配置文件并更新内存中的 Agent `SetLLM()`。

### 5. VisionRecognitionConfig

`vision_recognition` 是全局系统配置，用于给不支持多模态的主对话模型外接一个多模态模型：
- `enabled` — 是否允许会话启用专门的图片识别模式。
- `provider` / `model` — 使用已配置 LLM provider 中的模型。
- `timeout_seconds` — 单次识别超时，默认 60 秒。
- `max_image_bytes` — 单张图片大小上限，默认 10 MiB。

单会话是否优先使用该能力存放在 `conversations.metadata.use_image_recognition`。当系统配置和会话开关都开启时，当前轮图片先由配置模型识别，主模型只接收识别文本。

`image_recognize` 的历史图片回看能力不只依赖该开关：如果会话没有开启专门识图，但历史上下文里有带 `upload_id` 的图片引用，并且当前对话模型支持视觉输入，Agent 也可以暴露 `image_recognize`，由工具 fallback 到当前 provider/model 做一次非流式识别。没有任何可用视觉能力时，历史图片占位会提示用户重新上传图片或切换/配置视觉模型。

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
