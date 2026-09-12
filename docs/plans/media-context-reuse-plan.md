# 媒体工具上下文复用设计

状态：P0/P1/P2 后端主链路已实现；不提供输入区显式上下文选择器，是否复用由用户自然语言说明后交给 LLM/Resolver 决定（2026-09-12）。本文补充
[media-generation-implementation.md](media-generation-implementation.md) 中“生成结果可追问”和
“对话 LLM 根据上下文整理最终提示词”的后续能力边界。

## 实现状态

已落地：

- 新增 `media_contexts` 持久化表和 V14 升级步骤。
- `media_contexts` 记录 `regen_group_id`、`message_id`、`archive_reason`，可跟随重答、
  rollback/undo 和 sibling 激活同步可见性。
- 成功的 `media_recognize` / `image_recognize` / `generate_image` / `generate_video` /
  `generate_audio` 会记录媒体上下文。
- 后续轮次会把最近 active 媒体上下文注入系统提示，保留识别结果换行结构，供“基于刚才继续/合并前两次/沿用上次生成提示词”等自然语言指令使用。
- 重答归档同一 regen group 下旧回复时，也会归档对应媒体上下文，避免旧分支污染新分支。
- 工具 schema 已显式暴露 `context_refs` / `context_mode`。`fresh` 是默认；`continue`、
  `merge`、`verify`、`summarize` 会通过 resolver 校验并展开。
- `MediaContextResolver` 已支持显式 context id 的多级引用展开、按类型自动选取最近上下文、
  session/active 分支校验和有界文本注入。
- 本轮图片识别 preflight 会保存为待绑定 media context，assistant 回复落库后再绑定到对应 sibling。
- 生成工具之间、识别工具与生成工具之间共享同一套 `input_refs` / `context_refs` 语义：
  资产接力走 `input_refs`，提示词/识别结果接力走 `context_refs`。
- 工具结果 SSE/parts 会透传 `tool_context_refs`，工具卡片折叠态显示 context 数量，展开后显示具体
  `mctx_*` ID。
- 输入区不展示独立的“媒体上下文”选择器；用户用自然语言说明“基于刚才继续 / 重新识别 /
  合并前两次”等意图，LLM 再按工具 schema 和 resolver 选择 `context_refs` / `input_refs`。

尚未落地：

- token 预算下的 LLM 摘要压缩、结构化提取和后台摘要任务。
- 成本型链式生成确认、保留期和资产引用计数清理策略。

## 背景

当前 `media_recognize`、`generate_image`、`generate_video`、`generate_audio`
都把工具调用结果作为一次性 tool result 返回。图片、视频、音频资产本身已经通过
`input_ref` / `asset_id` 实体化并可在后续轮次复用，但“这一次识别的问题和结果”、
“这一次生成的原始 prompt、options 和输出摘要”没有形成可引用记录。

这会带来两个体验缺口：

- 用户说“基于上一次识别结果继续补充”时，系统只能依赖聊天文本里残留的工具结果，无法可靠区分是
  “重新识别旧图片”还是“沿用上次识别上下文继续”。
- 用户说“沿用刚才视频的提示词，再增加一段说明”时，系统缺少一份稳定的生成提示词血缘，只能重新生成
  一个全新的 prompt，难以表达“在上一版基础上追加”。

目标不是增加更多模型可见工具，而是让媒体工具调用产生可审计、可引用、可压缩的上下文记录。

## 核心结论

把媒体相关历史拆成两个正交概念：

1. **媒体资产复用**：复用原图片、原视频、原音频或生成资产本身，用 `input_refs` 表达。
2. **工具上下文复用**：复用某次识别或生成调用的 prompt、问题、结果、摘要和结构化数据，用
   `context_refs` 表达。

二者不能自动绑定。默认行为永远是 fresh：只复用用户明确指向的媒体资产，不自动继承任何旧工具上下文。
只有用户语义明确出现“基于上次、沿用、继续、补充、合并、参考之前结果、校正前两次”等表达时，才附带
`context_refs`。

```text
input_refs    = 看哪个媒体 / 用哪个生成资产
context_refs  = 沿用哪些历史工具调用上下文
```

## 数据模型

建议新增内部模块 `media_context`，负责保存、解析、展开和压缩媒体上下文。外部接口保持小而深：

```go
type MediaContext struct {
    ID             string
    SessionID      string
    Kind           string // recognition | generation
    ToolName       string
    InputRefs      []string
    OutputRefs     []string
    Prompt         string
    ResultText     string
    Summary        string
    StructuredJSON string
	ContextRefs    []string
	ToolCallID     string
	MessageID      int64
	RegenGroupID   string
	Archived       bool
	ArchiveReason  string
	CreatedAt      time.Time
}
```

说明：

- `InputRefs` 是本次工具实际读取或传给生成器的媒体引用。
- `OutputRefs` 是生成工具产出的资产 ID，例如生成视频或图片。
- `Prompt` 对识别工具表示识别问题，对生成工具表示最终提交给媒体厂商的有效提示词。
- `Summary` 是常驻上下文摘要，`ResultText` 是完整工具结果，`StructuredJSON` 用于表格、列表、时间轴等可选结构化输出。
- `ContextRefs` 支持多个直接引用，形成有向无环图，而不是只能指向单个父级。
- `Archived` 用于回滚或重答后的历史记录隐藏，不建议直接物理删除。

## 模式边界

Agent 或服务端 resolver 应先把用户意图归类，再决定是否调用工具、是否附带 context。

| 模式 | 用户表达 | 行为 | 工具调用 |
| --- | --- | --- | --- |
| `fresh_media_read` | “重新识别刚才图片”“换个角度看原图” | 新工具调用旧媒体，不复用旧识别结果 | `input_refs` only |
| `continue_last_context` | “基于上一次识别继续”“沿用刚才提示词” | 引用最近一个相关 context | `input_refs + context_refs[latest]` |
| `merge_contexts` | “基于之前两次识别调整”“合并前几次结果” | 引用多个直接 context | `context_refs[multiple]`，必要时加 `input_refs` |
| `verify_contexts` | “前两次可能错了，重新核对” | 旧 context 仅作参考，以原媒体重新观察为准 | `input_refs + context_refs` |
| `summarize_contexts` | “整理前两次识别结果” | 不重新看媒体，只汇总 context | 不调用识别工具 |
| `continue_generation_prompt` | “沿用刚才提示词，再加雨夜氛围” | 复用上次 prompt/options，生成新版 prompt | 生成工具可带 `context_refs` |
| `use_generated_asset` | “用刚才视频作为参考继续” | 先看 operation 是否支持对应媒体输入 | 支持则 `input_refs=asset`，否则先识别资产再拼 prompt |
| `generation_chain` | “用刚才那张图生成视频”“把生成图继续改成雨夜” | 生成资产作为另一个生成工具的输入 | `input_refs=generated asset`，可选 `context_refs` |

### 识别工具示例

重新识别旧图，不继承上下文：

```json
{
  "input_ref": "upl_1",
  "question": "识别右下角的文字"
}
```

基于上次识别继续：

```json
{
  "input_ref": "upl_1",
  "question": "在上一次识别结果基础上，补充第二列",
  "context_refs": ["ctx_rec_1"]
}
```

基于前两次识别校正：

```json
{
  "input_ref": "upl_1",
  "question": "基于前两次识别结果重新核对并校正",
  "context_refs": ["ctx_rec_1", "ctx_rec_2"],
  "context_mode": "verify"
}
```

### 生成工具示例

沿用上一版 prompt 继续生成：

```json
{
  "operation": "text_to_video",
  "prompt": "在上一版提示词基础上增加雨夜霓虹、地面反光和更慢的镜头推进",
  "context_refs": ["ctx_gen_1"]
}
```

使用上一版生成视频作为输入：

```json
{
  "operation": "video_to_video",
  "prompt": "保持主体动作，增加雨夜氛围",
  "input_refs": ["asset_video_1"],
  "context_refs": ["ctx_gen_1"]
}
```

若当前会话未启用 `video_to_video` 或没有可用模型，则不能伪装为基于视频输入继续。可退化为：

1. 对 `asset_video_1` 调 `media_recognize` 得到内容观察。
2. 把观察摘要和用户新增要求合并为新的 `text_to_video` prompt。
3. 明确告知用户这是基于视频内容摘要再生成，不是视频到视频编辑。

### 生成工具接力

生成工具之间应当可以通过同一套 `input_refs` / `context_refs` 机制接力。每次生成产出的
`output_refs` 都进入当前会话资产池，后续生成工具可在 operation 支持时把这些资产作为媒体输入。

```text
generate_image(text_to_image) -> asset_img_1
asset_img_1 + generate_video(image_to_video) -> asset_video_1
asset_img_1 + generate_image(image_to_image) -> asset_img_2
asset_video_1 + generate_video(video_to_video) -> asset_video_2
```

示例：用生成图片继续生成视频：

```json
{
  "operation": "image_to_video",
  "prompt": "让画面中的城市灯光缓慢闪烁，镜头向前推进",
  "input_refs": ["asset_img_1"],
  "context_refs": ["ctx_gen_img_1"]
}
```

示例：用生成图片继续生成图片：

```json
{
  "operation": "image_to_image",
  "prompt": "保持主体构图，把天气改成暴雨夜晚",
  "input_refs": ["asset_img_1"],
  "context_refs": ["ctx_gen_img_1"]
}
```

`input_refs` 与 `context_refs` 仍然分离：

- `input_refs=["asset_img_1"]` 表示真的把生成图片作为媒体输入。
- `context_refs=["ctx_gen_img_1"]` 表示复用那次生成的 prompt、options、风格意图和摘要。
- 如果用户只是说“按照刚才图片的风格重新画一张，不要用原图”，应只带 `context_refs`，不带
  `input_refs`。
- 如果用户说“用刚才那张图作为首帧生成视频”，应带 `input_refs`，并可选带生成 context。

直接接力必须满足目标 operation 的输入类型：

| 来源资产 | 目标 operation | 直接接力 |
| --- | --- | --- |
| 图片 | `image_to_image` | 是 |
| 图片 | `image_to_video` | 是 |
| 视频 | `video_to_video` | 是 |
| 音频 | `audio_to_audio` | 是 |
| 视频 | `text_to_image` | 否，需先识别、截帧或新增规范 operation |
| 图片 | `text_to_music` / `text_to_sound` | 否，需先识别图片内容再生成音频 |
| 音频 | `text_to_video` | 否，需先转写/识别音频内容再生成视频 |

若后续要支持 `video_to_image`、`image_to_audio`、`audio_to_video` 等跨模态能力，应追加新的
canonical operation，而不是让现有工具在 `input_refs` 中偷偷接受任意媒体类型。

## 多级引用

`context_refs` 是直接引用集合，间接祖先由 resolver 展开：

```text
ctx_rec_1: 识别第一列
ctx_rec_2: 基于 ctx_rec_1 补充第二列
ctx_rec_3: 基于 ctx_rec_2 继续识别第三列
ctx_rec_4: 基于 ctx_rec_1 + ctx_rec_2 重新校正前两次
```

展开规则：

- 直接引用的 context 优先给完整内容。
- 祖先 context 默认只给摘要。
- 同一 context 只出现一次，按拓扑顺序和创建时间去重。
- 超过预算时，保留 `context_id + summary + prompt preview`，不塞完整 `ResultText`。
- 若用户明确说“完整考虑前两次”，可提升直接引用内容的展开等级。

旧 context 不是事实真源。对于 `verify` 类调用，提示词必须包含：

```text
历史识别结果只是参考；若与当前媒体重新观察冲突，以当前媒体为准。
```

## Resolver 职责

不要把所有判断交给模型自由发挥。建议 Agent 或服务端新增 `MediaContextResolver`：

```go
type ResolveRequest struct {
    SessionID string
    UserText  string
    ToolName  string
    InputRefs []string
}

type ResolveResult struct {
    Mode        string
    InputRefs   []string
    ContextRefs []string
    Notes       []string
}
```

职责：

- 根据用户文本识别 fresh / continue / merge / verify / summarize 等模式。
- 从当前会话历史中选择最近相关 context。
- 约束 context 必须属于当前 session。
- 约束 context 类型与目标工具兼容。
- 展开引用图并生成有界上下文块。
- 在回滚或重答时跳过 archived context。

这让工具接口保持稳定，复杂性集中在一个深模块中测试。

## 实现约束

`context_refs` 不应成为模型自由越权读取历史的通道。建议采用“模型表达意图，resolver 补全引用，
handler 再校验”的三段式：

```text
LLM / UI action
  -> 表达 fresh / continue / merge / verify / chain 意图
  -> MediaContextResolver 选择 input_refs / context_refs
  -> tool handler 校验 session、类型、operation、archived 状态
```

具体约束：

- 模型可看到最近 context 摘要和短 ID，但不能仅凭任意 ID 访问完整内容。
- UI 快捷动作可以携带明确 context id / asset id；仍必须经过 resolver 和 handler 校验。
- `context_refs` 若由模型直接填入，应视为候选值，服务端必须重算并过滤。
- handler 不能因为 `context_refs` 合法就跳过 `input_refs` 的媒体类型校验。
- resolver 低置信度时应触发澄清，而不是选择最近记录硬凑。例如“基于之前两次”但最近两次来自不同图片。
- `summarize_contexts` 这类纯整理任务不应调用 `media_recognize` 或生成工具，避免让用户误以为系统重新看过媒体。
- `verify_contexts` 调用识别工具时必须把旧 context 标成参考，最终以当前媒体观察为准。

实现上，`context_refs` 可以先作为内部元数据保存在工具调用上下文里；是否进入模型可见 tool schema
应单独评估。第一版更稳的做法是：模型只写最终 prompt / question，resolver 在派发前补齐引用关系。

## 持久化与生命周期

凡涉及 SQLite schema、新表、新列或数据目录结构变化，都必须通过 `internal/upgrade/` 增加版本化升级步骤。

建议首版表形状：

```sql
CREATE TABLE media_contexts (
  id TEXT PRIMARY KEY,
  session_id TEXT NOT NULL,
  kind TEXT NOT NULL,
  tool_name TEXT NOT NULL,
  input_refs_json TEXT NOT NULL DEFAULT '[]',
  output_refs_json TEXT NOT NULL DEFAULT '[]',
  prompt TEXT NOT NULL DEFAULT '',
  result_text TEXT NOT NULL DEFAULT '',
  summary TEXT NOT NULL DEFAULT '',
  structured_json TEXT NOT NULL DEFAULT '',
  context_refs_json TEXT NOT NULL DEFAULT '[]',
  tool_call_id TEXT NOT NULL DEFAULT '',
  message_id INTEGER NOT NULL DEFAULT 0,
  regen_group_id TEXT NOT NULL DEFAULT '',
  archived INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);

CREATE INDEX idx_media_contexts_session_created
  ON media_contexts(session_id, created_at);

CREATE INDEX idx_media_contexts_message
  ON media_contexts(session_id, message_id);
```

生命周期规则：

- 工具成功返回后再创建 context；失败工具调用不创建可复用 context，可在日志或 tool part 中保留错误。
- 生成工具保存最终提交给厂商的 prompt、规范化 options、`input_refs`、`output_refs` 和上游 `context_refs`。
- 识别工具保存 question、识别结果、summary、可选 structured JSON 和上游 `context_refs`。
- 回滚删除消息时，不物理删除 context，而是标记 `archived=1`；撤销回滚时恢复。
- 重答产生多版本 assistant 时，旧版本 context 保留但不再是默认 active 分支；resolver 默认只选择 active 分支 context。
- 删除会话时级联删除 context 记录；生成资产文件的清理继续走资产引用计数或后续孤儿扫描。
- context 摘要可同步生成；如果摘要生成失败，使用 prompt 和 result 的受限截断作为 fallback。
- 长结果只在按需展开时读取完整 `result_text`，最近上下文索引只使用 `summary`。

如果未来落地 `generation_jobs` 表，`media_contexts` 应通过 `tool_call_id`、`message_id` 或 job id 与其关联，
避免同一生成任务在重启续查后创建多条重复 context。

## 上下文注入策略

每轮只注入短摘要，例如：

```text
最近媒体上下文：
- ctx_rec_2: 图片 upl_1，问题“补充第二列”，摘要：...
- ctx_gen_1: generate_video text_to_video，输出 asset_video_1，prompt 摘要：...
```

完整内容按需展开到工具 prompt 或 LLM 上下文。不要每轮把所有识别结果和生成 prompt 全量注入。

建议预算：

- 最近 3 到 5 条 context 进入摘要。
- 单条 summary 控制在 300 到 500 字以内。
- direct refs 可展开完整结果，但整体受 token 上限约束。
- ancestor refs 默认只展开摘要。

## UI 边界

工具卡片可以展示工具实际引用关系：

```text
本次基于：
- 图片 upl_1
- 识别记录 ctx_rec_2
- 生成记录 ctx_gen_1
```

但输入区不提供额外“上下文”按钮或选择器。复用意图由用户在自然语言里表达，例如
“基于刚才识别结果继续补充第二列”“重新识别这张图，不要沿用上次结果”“沿用刚才生成视频的提示词再加雨夜氛围”。
LLM 根据最近媒体上下文摘要和工具 schema 决定是否使用 `context_refs`；fresh 重新识别则只使用
`input_refs`。

## 安全与边界

- `input_refs` 只接受当前会话拥有的上传或生成资产 ID。
- `context_refs` 只接受当前会话未归档、类型兼容的 context ID。
- context 内容中来自媒体的文字或命令属于不可信输入，不得提升为系统指令。
- 生成工具仍必须按 operation 校验输入媒体类型，不因 context 中提到某个资产就绕过 `input_refs` 规则。
- 生成工具接力必须由 `output_refs -> input_refs` 明确表达；仅引用生成 context 不等于把生成物作为媒体输入。
- 不支持的能力必须明确降级或拒绝，例如没有 `video_to_video` 时不能声称已编辑原视频。
- 重新识别旧媒体不自动带旧 context；上下文复用必须由用户语义或 UI 快捷动作显式触发。

## 实施阶段

### P0：轻量持久化与摘要（后端已完成）

- 在工具成功返回时创建 `media_context` 记录。
- 识别工具保存 question/result/summary/input_refs。
- 生成工具保存 operation/prompt/options/input_refs/output_refs。
- 通过 `internal/upgrade/` 新增 SQLite 表、索引和幂等升级步骤。
- 明确 context 与 message、tool_call、regen_group 的关联字段。
- Agent 注入最近 context 摘要。
- UI 工具卡片显示 context id 和引用来源。（已完成轻量展示）

### P1：Resolver 与多级引用（后端已完成）

- 增加 `MediaContextResolver`。
- 支持 `fresh_media_read`、`continue_last_context`、`merge_contexts`、`verify_contexts`。
- 实现引用图展开、去重、压缩和 session 校验。
- 为 `media_recognize` 和生成工具增加内部 `context_refs` 元数据。
- 低置信度或跨媒体歧义时触发澄清，不自动猜测。（待与 UI 选择器/确认策略结合）

### P2：生成内容续写（主链路已完成，治理待补）

- 生成资产保存 provenance。
- “基于生成内容继续”根据 operation 能力选择：
  - 直接媒体输入。
  - 先识别生成资产再生成。
  - 明确拒绝或提示配置缺失。
- 支持 `generation_chain`：
  - 图片生成图片。
  - 图片生成视频。
  - 视频生成视频。
  - 音频生成音频。
- 对不支持的跨模态接力给出识别/转写/截帧或新增 operation 的降级路径。（待增强）
- 与未来 `generation_jobs` 表对齐。

### P3：治理与压缩

- 支持归档、回滚、重答后的 context 状态同步。（主链路已完成，继续补边界测试）
- 增加用户可控保留期和孤儿清理。

## 还需改进的问题

1. **结构化结果是否强制要求**
   表格、清单、时间轴类识别很适合 `StructuredJSON`，但普通图片描述不一定需要。建议第一版允许为空，
   只在模型返回可解析 JSON 或后处理可提取时保存。

2. **context id 是否暴露给模型**
   可以暴露短 ID，但不要要求用户记 ID。模型看到的是摘要目录；真正选择和校验由 resolver 完成。

3. **多媒体多 context 的歧义**
   如果用户说“基于之前两次”但最近两次分别来自不同图片，应先要求澄清，或按最近同一 `input_ref`
   的两次 context 解析并在回复中说明。

4. **长视频识别的成本**
   基于生成视频内容继续时，如果没有 `video_to_video`，先识别视频可能很慢且昂贵。应在 UI 上让用户知道
   “这会先分析视频内容”，未来可接入确认策略。

5. **和回滚/重答的关系**
   context 应跟消息生命周期对齐。回滚删除一段消息时，对应 context 标记 archived；撤销回滚时恢复。
   重答时旧 context 保留为历史版本，但默认 resolver 只选 active 分支上的 context。

6. **提示词合并策略**
   不建议暴露复杂 `prompt_merge_mode` 给模型。第一版由 Agent 生成最终 prompt，并把引用关系存档。
   只有当 UI 需要明确“追加 / 替换 / 强化 / 反向提示词”时，再把合并策略做成前端快捷动作。

7. **生成资产角色**
   当前 `input_refs` 是字符串列表，足够表达“这些资产作为输入”。图生视频常见的首帧/尾帧、
   多参考图角色、角色一致性参考等更细语义，未来可能需要升级为 `{id, role}` 结构。第一版不要把厂商
   专用角色直接暴露给模型，可由 UI 快捷动作或 operation 默认规则生成受限角色。

8. **接力链的成本与确认**
   生成工具接力可能连续触发多次高成本任务。若 `generation.require_confirm` 后续真正生效，应把
   “使用生成资产继续生成”和“先识别生成资产再生成”都纳入确认文案，说明会消耗哪些能力。

9. **重答分支里的资产选择**
   同一个用户消息可能有多个重答版本，每个版本都可能生成不同资产。resolver 默认只能选择当前 active
   分支上的生成 context 和 output asset；用户明确说“用上一版生成图”时，UI 应通过版本分页或明确 asset
   id 让选择变得可见，不能靠最近时间猜测。

10. **摘要生成策略**
   是否使用 LLM 生成 summary、是否同步生成、失败后如何降级，都需要实现前确定。第一版建议同步截断
   fallback，后续再接后台摘要任务，避免工具调用成功却因为摘要失败而整体失败。

11. **context 与资产引用计数**
   `output_refs` 会让生成资产继续被历史 context 引用。资产清理不能只看消息是否可见，还要考虑未归档
   context、active 分支和用户保留策略。

## 测试清单

- “重新识别刚才图片”只带 `input_refs`，不带 `context_refs`。
- “基于上次继续”选择最近同媒体 recognition context。
- “基于前两次识别调整”选择两个 direct context，并去重展开祖先。
- `ctx2 -> ctx1` 后再引用 `ctx2`，展开时不重复 `ctx1`。
- 跨 session context 被拒绝。
- archived context 不被默认 resolver 选择。
- 模型伪造的 `context_refs` 会被 resolver / handler 过滤。
- “整理前两次结果”不调用 `media_recognize`。
- “校正前两次结果”调用 `media_recognize`，并以原媒体为真源。
- 生成工具引用资产时，operation 不支持对应输入则被拒绝或降级为“识别摘要 + 文生”。
- `generate_image` 输出的图片可作为 `image_to_image` 和 `image_to_video` 的 `input_refs`。
- 仅带 `context_refs` 时不会把生成资产字节传给下游工具。
- 视频资产不能传给 `text_to_image`；若需要，应走识别/截帧/新增 operation 的明确降级。
- 接力生成的 context 正确记录上游 `context_refs` 和本次 `output_refs`。
- 回滚消息会 archive 对应 context，撤销回滚会恢复。
- 重答后 resolver 默认选择 active 分支 context。
- 摘要生成失败时仍保存 context，并使用受限 fallback summary。
- context 中的媒体文字命令不会进入 system prompt 或改变工具授权。
