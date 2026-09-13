# Frontend Design System

> **位置**：`frontend/src/style.css`（design tokens）+ 各 `.vue` 组件的 `<style scoped>`（组件规则）
> **目的**：固定 P-Chat 的视觉语言和编码约束，避免每个组件自由发挥造成"视觉碎片化"
> **产品主风格（已锁定）**：**Project-Aware Calm Workbench** — 后续所有主界面 / 聊天区 / 侧栏 / Inspector / Composer 改动必须遵守本文 §0.5 + §3.8–3.10
> **适用**：所有 Vue 3 + Naive UI 组件的样式修改
> **关联计划**：[`docs/plans/project-aware-ui-redesign-plan.md`](../../docs/plans/project-aware-ui-redesign-plan.md)

## 0. 单一原则

**所有颜色、间距、圆角、阴影、动效都从 CSS 变量读，不写裸值。**

```vue
<!-- ✅ 正确 -->
<a class="brand-button">Click</a>
<style scoped>
.brand-button { background: var(--brand-500); padding: var(--space-3); }
</style>

<!-- ❌ 禁止 -->
<style scoped>
.brand-button { background: #4A4DFF; padding: 12px; }
</style>
```

写裸值的代价：主题切换不生效、暗色对比度失控、和其它组件视觉冲突。AGENTS.md §2.3 已经写了"禁止硬编码颜色"——这个文档是它的扩展。

---

## 0.5 Product Visual Direction — Project-Aware Calm Workbench（已锁定）

> **这是 P-Chat 主界面的唯一产品视觉方向。** 新功能、重构、视觉打磨都必须对齐本节；不要另起一套“后台风 / 营销风 / 四列拥挤风”。

### 0.5.1 气质一句话

安静、克制、精致的**桌面 AI 编程工作台**，接近 Raycast / 原生生产力工具 / 轻量 IDE；**不是**管理后台、仪表盘或营销落地页。

### 0.5.2 信息架构（不可破坏）

1. **项目是一级上下文**；会话是当前项目下的二级对象。
2. **全局会话**是特殊空间，必须始终可进入（AppRail「全局」+ ProjectSwitcher 内固定入口）。
3. 新建项目、切换项目、项目内新建对话、搜索项目内会话 —— **默认界面可发现**；新建项目保留 AppRail 左下角快捷入口，并在 ProjectSwitcher popover 内提供行入口；禁止在 ProjectSwitcher 触发器旁重复放 `+` 挤压项目名。
4. **禁止**长期可见的四重竖栏把项目列表占满一整列；项目列表只活在侧栏顶部的 **ProjectSwitcher popover**。

### 0.5.3 主壳布局（token 宽度）

| 区域 | Token | 约值 | 规则 |
|---|---|---|---|
| AppRail | `--rail-width` | 52px | 常驻；折叠侧栏时仍可见 |
| 统一侧栏 | `--sidebar-width` | 首次默认 240px，可拖拽 220-420px | 项目切换器 + 当前项目会话列表；项目选项只在 **ProjectSwitcher popover** 中展示，不常驻占位 |
| 中央聊天 | flex 自适应 | 填充剩余空间 | 主阅读区；侧栏收窄后自然变宽 |
| Inspector | `--inspector-width` | 首次默认 240px，可拖拽 240-440px | 可折叠；会话 tab 只放会话操作和上下文进度，项目 tab 承载项目信息 / Git 状态 / 项目动作；轻量 section，禁止多层卡片套娃 |

首次进入时，`SessionSidebar` 与 `InspectorPanel` 默认宽度均为 **240px**；用户可通过侧栏右缘拖拽把手调整聊天记录宽度，通过聊天区与 Inspector 之间的把手调整右侧面板宽度；双击任一把手恢复 240px 默认宽度。`AppRail` 是主导航，并保留左下角新建项目/关于快捷入口；`TopBar` 只承载 breadcrumb、主题切换和 Inspector 开关。上下文占用进度条归入 `InspectorPanel` 的会话 tab「模型 / 上下文」，替换纯文字 token 描述。文件夹/终端这类当前项目动作归入 `InspectorPanel` 的项目区域；trace、工具列表、生成风格等当前会话动作归入 Inspector 的会话区域，避免顶部与侧栏重复入口。会话 tab 不重复展示项目卡片；项目名称、路径、Git 分支和工作区状态只在项目 tab 或顶部 breadcrumb 的弱 metadata 中出现。

```
TitleBar
└─ app-body
   ├─ AppRail                          ← 常驻
   ├─ SessionSidebar                   ← 可折叠（不含内嵌 project-rail）
   └─ main-column
      ├─ TopBar                        ← breadcrumb / theme / inspector toggle
      └─ workspace-row
         ├─ ChatWindow
         └─ InspectorPanel             ← 可折叠
```

### 0.5.4 色彩纪律

- **减少大面积高饱和蓝紫**。Brand 只用于：发送按钮、关键状态点、极轻 hover / selected wash。
- **默认 CTA**（如「新建对话」）用 **outline / surface** 次要按钮：`var(--surface-2)` + `var(--border-default)`；hover 才允许浅 brand wash。**禁止**整栏实心 `--brand-500` 大按钮抢戏。
- **列表 / Tab / Rail 激活态**：浅底 wash（`color-mix` brand 进 surface），**禁止**左侧或底部的 brand 竖条 / 弧线。
- User 气泡：只用 `--user-bubble-bg` / `--user-bubble-border`（低饱和 tint），禁止实心 brand 填充。
- Assistant 正文：**默认无厚重卡片底**；身份靠头像 / 名称，不靠整块紫色底板。

### 0.5.5 消息流与 parts（强制）

| Part | 视觉归属 | 规则 |
|---|---|---|
| `text`（assistant） | 正文流 | 无厚卡片；markdown 走 `.md-body` |
| `text`（user） | 右对齐柔和气泡 | `--user-bubble-bg` / `--user-bubble-border` |
| `thinking` | **独立组件，不进入 `.event-timeline`** | `ThinkingBlock` 扁平行；`--thinking-bg` 中性；**禁止**独立暖色厚卡；**禁止**左侧竖轨 |
| `tool` / `skill` / `sub_agent` | `.event-timeline` 浅底面板 | 与 thinking 同族但独立分组；无左侧竖轨；错误行才允许细红左边框 + 短摘要 |
| 附件 / 媒体结果 | 中性小卡片 | 不与 user 文字气泡混在一起 |

**分组铁律**（`MessageBubble.vue`）：

- 连续的 `tool` / `skill` / `sub_agent` 共用一个浅底 `.event-timeline` 面板，行与行扁平并列。
- `thinking` **永远**用 `ThinkingBlock` 独立渲染（不是 ToolCallCard），不进入 `.event-timeline` /「工具轨」。
- **不要**在面板左侧画竖线 / 时间线轨。
- **不改 SSE / parts 数据结构**；只改布局与样式。

**附件与生成物展示规则**：

- 用户多附件上传在消息气泡中使用中性 `.media-thumbnail` / `.attachment-file-card` 栅格，不与文字气泡混成一个实心色块；单附件保留舒适预览宽度，多附件自动压缩成紧凑网格。
- `generate_*` 与 `browser_screenshot` 的 `assets[]` 统一按生成物卡片展示；图片 / 视频可预览，音频可播放，普通文件 / 文本产物使用紧凑文件行，保留名称、来源和下载动作。
- 右侧 `InspectorPanel` 的会话 tab 必须汇总当前会话引用过的附件：用户上传、工具生成、浏览器截图，以及嵌套子代理中产生的工具生成物。默认展示前 5 条，使用展开按钮查看其余附件。

### 0.5.6 动效与密度

- 流式：只允许小 spinner / caret / 状态点；只动画 `opacity` / `transform`。
- **禁止** thinking / tool / sub-agent 运行态的大面积 shimmer、持续背景扫光。
- 密度：信息完整但不堆叠；用弱分割线、popover、Inspector section 组织，而不是层层嵌套卡片。

### 0.5.7 UI chrome

- 图标一律 `frontend/src/components/icons/index.ts` barrel。
- **UI chrome 不用 emoji**（按钮、label、rail、侧栏、Inspector）。
- 不删除现有功能入口；能力可搬家，不可消失。

### 0.5.8 反模式（看到就改）

| ❌ 不要 | ✅ 要 |
|---|---|
| 管理后台四列挤满 | AppRail + 单侧栏 + 可折叠 Inspector |
| 项目列表长期占一整列 | ProjectSwitcher popover |
| 大面积高饱和蓝紫 CTA | outline / 细 accent |
| thinking 与 tool 混成一条时间线或共享面板 | thinking 仍是独立组件；tool / skill / sub_agent 才进入 `.event-timeline` |
| 选中会话 / Tab 左侧或底部 brand 竖条 | 浅底 wash，无弧线 / 竖条 |
| tool error 大红铺满 | 细红边 + 短摘要 |
| assistant 正文厚卡片 | 透明/无底，正文直接落在画布 |
| Inspector 多层 card 套娃 | 弱 section divider |
| NScrollbar 包消息区 | native `overflow-y: auto` + flex 铁律 |

---

## 1. Design Tokens（设计变量）

完整定义在 `frontend/src/style.css`。**新增 token 必须先加到 `:root[data-theme="dark"]` 和 `:root[data-theme="light"]` 两处**，缺一会破坏主题切换。

### 1.1 Surfaces（层叠背景，从后到前）

| Token | 暗色值 | 用途 |
|---|---|---|
| `--surface-0` | `#0B0D12` | 应用最底层（页面背景） |
| `--surface-1` | `#14171F` | 一级卡片 / 模态 |
| `--surface-2` | `#1C1F29` | 二级卡片 / 输入框 / 折叠区 |
| `--surface-3` | `#262A36` | 三级（hover、active 高亮） |
| `--surface-input` | `#1C1F29` | 输入控件专用（暗色 = surface-2） |
| `--surface-overlay` | `rgba(11, 13, 18, 0.72)` | 模态蒙层 |

**用法**：modal 背景用 `--surface-1`，modal 内的卡片用 `--surface-2`，hover 态用 `--surface-3`。

### 1.2 Text（4 级灰度）

| Token | 暗色值 | 用途 |
|---|---|---|
| `--text-primary` | `#F4F5F7` | 主要内容、标题 |
| `--text-secondary` | `#A8ADBA` | 次要、label |
| `--text-tertiary` | `#6B7180` | hint、placeholder |
| `--text-quaternary` | `#4A4F5C` | disabled、divider 文字 |

**对比度原则**：primary 文字至少 4.5:1（WCAG AA），secondary 至少 3:1。改 token 值后跑 Lighthouse 验证。

### 1.3 Borders（3 级透明度）

| Token | 暗色值 | 用途 |
|---|---|---|
| `--border-subtle` | `rgba(255,255,255,0.06)` | 卡片内分割线 |
| `--border-default` | `rgba(255,255,255,0.10)` | 控件边框、卡片边 |
| `--border-strong` | `rgba(255,255,255,0.16)` | 强调边框、focus |

### 1.4 Brand + AI（双品牌色）

| Token | 暗色值 | 用途 |
|---|---|---|
| `--brand-50` | `rgba(74, 77, 255, 0.12)` | hover 背景 |
| `--brand-100` | `rgba(74, 77, 255, 0.22)` | active 背景 |
| `--brand-500` | `#4A4DFF` | 主品牌色（CTA、streaming dot） |
| `--brand-600` | `#3D3FE0` | hover 态 |
| `--brand-700` | `#3033BF` | pressed 态 |
| `--ai-500` | `#7C5BFF` | AI 身份色（assistant 消息边框/头像） |
| `--ai-50` / `--ai-100` | `rgba(124, 91, 255, ...)` | AI hover/active 背景 |

**关键设计**：brand 是用户/操作色（蓝紫），ai 是助手身份色（紫）。两者**不可混用**——`MessageBubble` 用 `--ai-500` 给 assistant 头像染色，但禁用按钮用 `--brand-500` 而不是 `--ai-500`。

### 1.5 Status

| Token | 用途 |
|---|---|
| `--success-50/500` | 成功态（绿） |
| `--warn-50/500` | 警告态（橙） |
| `--error-50/500` | 错误态（红） |

### 1.6 Spacing（4pt scale）

| Token | 值 | 典型用法 |
|---|---|---|
| `--space-1` | `4px` | 紧贴间距、icon 内边距 |
| `--space-2` | `8px` | 控件内 gap |
| `--space-3` | `12px` | section 内边距、控件间 |
| `--space-4` | `16px` | 卡片内边距、section gap |
| `--space-5` | `20px` | 大 section gap |
| `--space-6` | `24px` | 模态 padding、page gap |
| `--space-7` | `32px` | 大区块分隔 |
| `--space-8` | `40px` | hero 间距 |

**规则**：不要用 `5px` / `7px` / `13px` 等非 4 倍数的值。如果非要，做成 token（如 `--space-3-5: 14px`）。

### 1.7 Radius

| Token | 值 | 典型用法 |
|---|---|---|
| `--radius-sm` | `6px` | input、chip、inline button |
| `--radius-md` | `10px` | button、card |
| `--radius-lg` | `14px` | 大卡片、modal |
| `--radius-xl` | `20px` | 浮层、tooltip |
| `--radius-pill` | `9999px` | 头像、tag、brand 圆角 |

### 1.8 Shadow

| Token | 用途 |
|---|---|
| `--shadow-sm` | hover 微抬升 |
| `--shadow-md` | 浮层（dropdown、tooltip） |
| `--shadow-lg` | modal |

**禁止**：写 `box-shadow: 0 2px 4px rgba(0,0,0,0.1)` 这种裸值。

### 1.8b Glass + Titlebar（局部毛玻璃）

| Token | 用途 |
|---|---|
| `--glass-bg` | 标题栏 / 浮层毛玻璃底色 |
| `--glass-border` | 毛玻璃描边 |
| `--glass-blur` | `backdrop-filter: blur(...)` 半径 |
| `--glass-panel-bg` | 弹窗面板高不透明度毛玻璃（可读优先） |
| `--titlebar-height` | 自定义标题栏高度（36px） |
| `--control-height` | 壳层控件统一高度（28px） |

**只用在**：`TitleBar`、modal mask / **modal 面板（`--glass-panel-bg`）**、lightbox mask、下载 dock、toast/notification。侧栏 / 消息列表 / 输入区仍用实心 `--surface-*`。

弹窗面板用高不透明度 glass（`--glass-panel-bg` ≈ 92–94%），禁止把正文直接压在强模糊背景上。

**Frameless 约束**：
- 拖拽区：`--wails-draggable: drag`
- 按钮命中区：`--wails-draggable: no-drag`
- 关闭必须走 `RequestWindowClose` → `app:close-request`，禁止直接 `Quit`

### 1.9 Motion

| Token | 值 | 用途 |
|---|---|---|
| `--ease-out` | `cubic-bezier(0.16, 1, 0.3, 1)` | 进入 / hover 落定（减速入位） |
| `--ease-in` | `cubic-bezier(0.4, 0, 1, 1)` | 离开 / 关闭（加速离场） |
| `--ease-in-out` | `cubic-bezier(0.4, 0, 0.2, 1)` | 双向布局（侧边栏宽度、dock） |
| `--dur-fast` | `120ms` | hover、focus |
| `--dur-base` | `200ms` | 默认 transition；离开动画 |
| `--dur-slow` | `320ms` | modal / 侧边栏 / 大面板进入 |
| `--transition-colors` | shorthand | hover/focus 的颜色族一次性声明 |

**进入慢、离开快**：enter 用 `--dur-slow/--dur-base` + `--ease-out`，leave 用 `--dur-base/--dur-fast` + `--ease-in`。不要进出共用同一条曲线。

**标准 transition 写法**：
```css
transition: var(--transition-colors);
/* 需要位移时再叠 transform */
transition: var(--transition-colors),
            transform var(--dur-fast) var(--ease-out);
```

### 1.10 Typography

| Token | 栈 | 用途 |
|---|---|---|
| `--font-sans` | InterVariable → Inter → system | 全部 UI 文字 |
| `--font-mono` | JetBrains Mono → SF Mono → ui-monospace | 代码片段、id、token 计数 |
| `--font-display` | InterVariable → Inter → system-ui | 大标题（暂时和 sans 同栈） |

**InterVariable woff2 在 `frontend/src/assets/fonts/InterVariable.woff2`**（~344KB），全局 `@font-face` 加载，CJK 字符走系统栈（PingFang SC / Microsoft YaHei）。

**基线字号**：body 14px / line-height 1.55。OpenType features 已在 `html` 开启（`cv02 cv03 cv04 cv11 ss01 tnum`），所有文字自动获得 tabular numerals + 优化字形。

### 1.11 Legacy Aliases（向后兼容）

```css
--bg:        var(--surface-0);
--bg-2:      var(--surface-1);
--text-2:    var(--text-secondary);
--accent:    var(--brand-500);
--error:     var(--error-500);
...
```

**新组件必须直接读新 token（`--surface-1` 而不是 `--bg-2`）**。legacy alias 只为旧组件存在，不接受新代码用它们。

### 1.12 Workbench shell + message tokens（Calm Workbench）

主壳与消息流专用 token（**dark / light 必须成对定义**；真源在 `frontend/src/style.css`）：

| Token | 用途 |
|---|---|
| `--rail-width` | AppRail 宽度（52px） |
| `--sidebar-default-width` | 统一侧栏首次默认宽度（240px） |
| `--sidebar-width` | 统一侧栏最终宽度；默认读 `--sidebar-default-width`，用户拖拽后读 `--sidebar-user-width` |
| `--inspector-default-width` | Inspector 首次默认宽度（240px） |
| `--inspector-width` | Inspector 最终宽度；小屏先归零折叠 |
| `--project-switcher-width` | 项目切换下拉面板宽度（~320px；项目选项只在这里展开） |
| `--event-rail-color` | 已废弃于主界面过程面板（不再画左侧竖轨）；保留 token 以免旧引用断裂 |
| `--user-bubble-bg` / `--user-bubble-border` | 用户柔和气泡 |
| `--thinking-bg` / `--thinking-border` / `--thinking-icon` | 独立思考块（中性 surface，与时间线同族，不进工具轨） |

---

## 2. 主题切换

App.vue 维护 `themeName: 'dark' | 'light'`，通过 `<html data-theme="...">` 切换。**所有 token 自动级联**，组件代码不需要感知主题。

```vue
<!-- App.vue -->
<script setup>
const themeName = ref<'dark' | 'light'>('dark')
function applyDocumentTheme(name: 'dark' | 'light') {
  document.documentElement.setAttribute('data-theme', name)
}
</script>
```

**约束**：
- 不要在组件里写 `[data-theme="light"] .foo { ... }` —— 加新 token 到 `:root` 两边
- 不要用 `prefers-color-scheme` 自动切换 —— 用户偏好要显式（localStorage 持久化）
- Brand color 也走 theme：dark 用 `#4A4DFF`，light 用 `#4042E6`（饱和度更高以补偿白底）

---

## 3. 组件规则（已稳定的样式模式）

新组件**优先复用这些 pattern**，不要发明新的变体。

### 3.1 `.opt-pick` — 会话级 option picker

**用途**：风格、推理、知识库等"会话级"选项的下拉按钮。

**结构**（input-row 内）：
```html
<NDropdown :options="..." @select="...">
  <button class="opt-pick">
    <Database :size="12" class="opt-pick-icon" />  <!-- 可选前缀图标 -->
    <span class="opt-pick-label">{{ value }}</span>
    <ChevronDown :size="11" class="opt-pick-caret" />
  </button>
</NDropdown>
```

**变体**：
- `.opt-pick` — 默认 56px min-width（风格、知识库）
- `.opt-pick--narrow` — 36px min-width（推理，标签 1-2 字）

**规范**：
- 必须用 `NDropdown`，**不要用 NSelect**（NSelect 的 border+chevron 跟 input-wrap 边框冲突）
- 标签用 `var(--font-mono)` 12px
- chevron 放 label **右边**（不是传统左边）
- 当前值不存在时显示 fallback（如 `--off`）

### 3.2 `.ctrl-btn` — 底部 row 操作按钮

**用途**：model / plan / permission / mute / 更多 等 always-visible 操作。

**结构**：
```html
<button class="ctrl-btn" :class="{ 'ctrl-btn--active': planMode }">
  <Hammer :size="13" />
  <span class="ctrl-btn-label">构建</span>
</button>
```

**变体**：
- `.ctrl-btn--active` — 激活态背景 `var(--brand-50)` + 文字 `var(--brand-600)` + border `var(--brand-100)`
- `.ctrl-btn--active-warn` — 警告态（mute on）
- `.ctrl-btn--more` — 更多按钮，配合 `.ctrl-btn--expanded` 显示展开态

**规范**：
- 高度 28px，padding 0 8px
- 用 `var(--font-sans)` 12px（不是 mono）
- 激活态用 brand 色系，不用 ai 色系

### 3.3 `.settings-section` + `.settings-form` — 设置面板

**用途**：所有设置面板（providers / styles / system / mcp / knowledge / websearch）的内容容器。

**结构**：
```html
<div class="settings-section">
  <div class="settings-section-header">
    <h3 class="settings-section-title">{{ title }}</h3>
    <div class="settings-form-actions">
      <NButton>重置</NButton>
      <NButton type="primary">保存</NButton>
    </div>
  </div>
  <p class="settings-section-description">{{ desc }}</p>
  <div class="settings-form">
    <div class="settings-form-row">
      <label class="settings-form-label">字段</label>
      <NInput size="small" />
      <span class="settings-form-hint">辅助说明</span>
    </div>
    <!-- 开关型字段用 .settings-form-toggle 包装 -->
    <div class="settings-form-row">
      <div class="settings-form-toggle">
        <NSwitch />
        <label class="settings-form-label">开关</label>
      </div>
      <span class="settings-form-hint">辅助说明</span>
    </div>
  </div>
</div>
```

**间距规则**：
- `.settings-section` 之间 `margin-bottom: 24px`
- `.settings-section` 内部 `padding: 4px 0`（配合外层 `.provider-detail` 的 16-18px padding）
- `.settings-form` 行间距 18px（label → input 8px，input → hint 1px）
- `.settings-form--grid`：两列字段网格（名称/协议并排）；整行字段加 `.settings-form-row--span2`
- label 字号 13px `var(--text-primary)` 500 weight（**不是 secondary**）
- hint 字号 11.5px `var(--text-tertiary)` line-height 1.5

**LLM 提供商 tab**：
- 左侧只做列表；「+ 新增」打开弹窗（不要塞进左侧窄栏或右侧详情）
- 模型添加/编辑用 `NModal`；列表里点编辑打开弹窗
- 新增弹窗的默认模型支持「加载模型」→ `POST /api/v1/providers/probe-models`
- 「协议」字段表示 LLM 请求/响应端点协议，不是网络传输协议；UI 文案用「请求协议」或「LLM 协议」，选项显示 `OpenAI Chat` / `Anthropic Messages`，对应配置值仍按后端约束使用 `openai` / `anthropic`
- `Base URL` 是唯一展示 `http://` / `https://` 的位置；如需显示端点，可用只读提示 `/chat/completions` 或 `/messages`
- 模型添加/编辑弹窗不再展示「功能标签」字段；模型列表优先展示模型 ID、显示名、上下文、输出上限和启用状态

**禁止**：
- 用 `style="margin: 0;"` 内联覆盖（用专门的 modifier class）
- 用 `var(--text-secondary)` 写 label（label 必须是 `--text-primary`）
- 把 toggle 写成 `flex-direction: row` 散在 row 里（统一用 `.settings-form-toggle` 包装）

### 3.3b `.settings-collapse` — 设置页收缩面板

**用途**：应用设置里所有 `NCollapse`（系统 / 网络搜索 / 知识库 / IM / 诊断）。

**结构**：
```html
<NCollapse class="settings-collapse">
  <NCollapseItem title="工作模式" name="work-mode">…</NCollapseItem>
  <NCollapseItem title="图像识别" name="vision">…</NCollapseItem>
</NCollapse>

<!-- 已在 .settings-card 内时用 inset，避免双层边框 -->
<NCollapse class="settings-collapse settings-collapse--inset">…</NCollapse>
```

**规则**：
- **每项独立卡片**（`border` + `radius-md` + `surface-1` + `overflow: hidden`），禁止把多个 item 包进同一个外框
- Header / content 共用同一表面；展开时 header 用 `surface-2` + 底部分割线
- **必须覆盖** Naive 默认的 `first-child > header { padding-top: 0 }`（否则顶部标题会贴边，看起来像「顶部样式错乱」）
- 样式定义在 `frontend/src/style.css`（全局），组件里不要再复制一套 `.sys-collapse` / `.form-collapse`

### 3.4 `.model-card` / `.provider-item` / `.kb-node-*` — 列表项

**共同点**：
- 高度 36-44px（点击目标 36px 起步）
- padding 10-12px
- border-radius `var(--radius-md)` 或 `var(--radius-sm)`
- 默认 `background: var(--surface-1)`，hover/active 用 `var(--surface-2/3)`
- primary 文字 13-14px，meta 文字 11.5px `var(--text-tertiary)`

### 3.5 Chat 布局

**绝对模式**（不要改）：
```vue
<main class="chat-main">                    <!-- flex column, min-height: 0, overflow: hidden -->
  <div class="messages-scroll" @scroll>     <!-- flex: 1 1 0, min-height: 0, overflow-y: auto -->
    <div class="messages">...</div>         <!-- flex column, 不要 min-height: 100% -->
  </div>
  <QuestionPanel />                         <!-- 条件渲染 -->
  <TodoPanel />                             <!-- 条件渲染 -->
  <InputArea />                             <!-- flex-shrink: 0，固定底部 -->
</main>
```

**铁律**：
- input-area 必须 `flex-shrink: 0` —— 被压缩的是消息区，不是输入框
- messages-scroll 必须 `min-height: 0` —— 不设这个 flex 子项不收缩
- 永远不要在 chat-main 加 `max-height` —— 会让输入框被挤下去
- **不要用 NScrollbar**（`:native-scrollbar="false"` 模式）—— inner container 拿不到父 flex 高度，会出现"输入框漂移"bug

**InputArea 内部高度管理**：
- textarea 自身 cap 4 行（`resizeTextarea()`）
- attach-strip 自身 cap 96px
- 每个子项都 cap 自己，父容器自然适应

### 3.6 TopBar 品牌显隐

**当前规则**：侧边栏展开时 TopBar 隐藏 P-Chat logo，折叠时显示。

```vue
<button v-if="props.collapsed" class="brand">
  <BrandLogo :size="22" />
  <span class="brand-text">P-Chat</span>
</button>
```

**约束**：
- 改这条规则前先确认用户意图（早期版本是始终显示，后来改为条件显示）
- 不要用 `v-show`（用 `v-if` 完整移除 DOM，避免 brand 占位）

### 3.7 Message Bubble parts 渲染

**结构原则**（`MessageBubble.vue`）：按 segment 渲染，**thinking 与 tool 时间线分离**。

```vue
<template v-for="segment in visibleTimelineSegments" :key="segment.key">
  <!-- 仅 tool / skill / sub_agent -->
  <div v-if="segment.kind === 'events'" class="event-timeline">
    <ToolCallCard ... />
    <SkillCallCard ... />
    <SubAgentCard ... />
  </div>
  <template v-else>
    <!-- thinking 独立，绝不进 event-timeline -->
    <ThinkingBlock v-if="entry.part.kind === 'thinking'" ... />
    <div v-else-if="entry.part.kind === 'text'" class="md-body" ... />
  </template>
</template>
```

**色系 / 表面**：
- assistant 头像：`--ai-500` / `--ai-50`；正文默认透明无厚卡片
- user 气泡：`--user-bubble-bg` / `--user-bubble-border`（禁止实心 brand）
- thinking：`--thinking-bg` / `--thinking-border` / `--thinking-icon`（中性独立行，与 tool 同族）
- 过程面板：浅底 `.event-timeline`，**无左侧竖轨**；status：`start` 蓝 spinner、`ok` 绿勾、`error` 细红边、`warn` 橙；行内扁平无卡套卡

**Markdown**：用 `.md-body`（`style.css` 集中定义）。**不要在组件里重定义** p/code/pre/a/ul/ol/blockquote/table/hr/h1-h4/img/strong/em。

### 3.8 App shell 组件约定

| 组件 | 职责 | 样式要点 |
|---|---|---|
| `AppRail.vue` | 常驻 52px 导航 | active = `surface-2` 浅底，**无**左边线；禁用态降透明度 |
| `ProjectSwitcher.vue` | 侧栏顶项目 popover | 触发器优先完整显示当前项目名；搜索 / 新建项目 / 全局 / 项目列表只在 popover 内展开；触发器旁不放独立 `+`；当前项 Check + 浅 wash |
| `SessionSidebar.vue` | 统一侧栏 | 宽 `--sidebar-width`；「新建对话」outline CTA；会话 active = 浅 brand wash，**无**左边线 |
| `TopBar.vue` | 面包屑 + ctx + inspector toggle | 克制高度；不堆叠打开目录/终端/工具列表/生成风格 |
| `InspectorPanel.vue` | 右栏会话/项目摘要 + 归类操作 | 可折叠、可拖拽宽度；tab 切换用轻过渡；会话 tab 放 trace/工具列表/生成风格；项目 tab 放项目名、路径、Git 分支/工作区状态、AGENTS.md、打开目录/终端；section 用弱分割线 |
| `ChatWindow.vue` | 消息列 + dock | 遵守 §3.5 flex 铁律 |

### 3.9 Thinking vs Tool 视觉分工

| | ThinkingBlock | Tool / Skill / SubAgent |
|---|---|---|
| 容器 | 独立中性扁平块，不进入 `.event-timeline` | 浅底 `.event-timeline` 面板内的扁平行 |
| 左边轨 | **无** | **无**（仅 error 行可用细红左边框） |
| 错误态 | N/A | 细红左边框 + 一行摘要 |
| 运行动效 | 仅小 spinner | 仅小 spinner / 状态点 |
| 数据 | `part.kind === 'thinking'`（独立组件） | `tool` / `skill` / `sub_agent` |

### 3.9a SubAgent 视觉与展开态

`SubAgentCard` 必须把后端子代理类型显式展示出来，不只显示任务标题。

- 执行时间线里的子代理行至少显示：`sub_agent_type` 与 `sub_agent_run_mode` 合并后的紧凑 meta（例如 `frontend_agent · 后台`）、任务标题、状态、耗时；避免再拆成首字母图标、`类型` badge、`后台子代理` badge 三段重复信息；有 `sub_agent_model` / `sub_agent_task_id` 时作为弱信息展示
- 后台子代理列表（`SubAgentJobsPanel`）同样显示 `subagent_type` 文本，但不重复加「类型」标签，并区分 queued / running / done / error
- 展开后的子代理不是普通详情面板，应表现为「嵌套对话列表」：包含子代理自己的消息流、独立 `ThinkingBlock`、工具/技能执行时间线、结果摘要和错误行
- 嵌套对话列表内部仍遵守本节分工：thinking 独立；tool / skill / sub_agent 进入内部 `.event-timeline`；不要用卡片套卡片

### 3.10 CTA / 激活态配方（Calm）

```css
/* 默认次要 CTA —— 侧栏「新建对话」等 */
.calm-cta {
  background: var(--surface-2);
  color: var(--text-primary);
  border: 1px solid var(--border-default);
}
.calm-cta:hover {
  background: color-mix(in srgb, var(--brand-500) 8%, var(--surface-2));
  border-color: color-mix(in srgb, var(--brand-500) 28%, var(--border-default));
  color: var(--brand-600);
}

/* 列表 / Rail / Tab 激活 —— 浅 wash，不要竖条或底边条 */
.calm-active {
  background: color-mix(in srgb, var(--brand-500) 8%, var(--surface-1));
  color: var(--text-primary);
}
```

**唯一允许的大面积 brand 实心**：发送按钮等真正的 primary action（聊天主操作），不是侧栏列表 CTA。

---

## 4. 间距 / 尺寸规则

### 4.1 卡片内边距

| 场景 | padding |
|---|---|
| 小卡片（chip、tag、attach） | `4px 8px` 或 `2px 4px` |
| 中等卡片（model-card、provider-item） | `10px 12px` |
| 卡片（settings-card、message-bubble） | `12px 14px` |
| 模态内容 | `16px` 或 `20px` |
| 模态标题 | `16px 20px` |
| 大区块（settings-section） | `20px 24px` |

### 4.2 圆角

| 元素 | radius |
|---|---|
| inline 小元素（chip、tag、kbd） | `var(--radius-sm)` (6px) |
| 按钮、input、card | `var(--radius-md)` (10px) |
| 大卡片、modal | `var(--radius-lg)` (14px) |
| 浮层（tooltip、dropdown） | `var(--radius-xl)` (20px) |
| 头像、tag、brand 圆角 | `var(--radius-pill)` |

### 4.3 行高

| 元素 | line-height |
|---|---|
| body | 1.55 |
| markdown body | 1.6 |
| button / label | 1.3 |
| hint / meta | 1.5 |
| kbd | 1.0 |

### 4.4 字体大小

| 元素 | 字号 |
|---|---|
| h1 / 大标题 | 16-20px / 600 weight |
| h2 / section title | 14px / 600 weight |
| h3 / sub-section | 13px / 500 weight |
| body | 14px / 400 weight |
| label | 13px / 500 weight |
| button | 12-12.5px / 500 weight |
| hint | 11.5px / 400 weight |
| meta / micro | 11px / 400 weight |

---

## 5. 图标系统

**唯一来源**：`frontend/src/components/icons/index.ts` 桶导出，**所有图标用 `lucide-vue-next`**。

```ts
// ✅ 正确
import { Send, Paperclip, X } from './icons'
<Send :size="16" />

// ❌ 禁止
import { Send } from 'lucide-vue-next'  // 绕过 barrel，破坏 tree-shaking
```

**尺寸约定**：
| 尺寸 | 用途 |
|---|---|
| 11px | chevron（dropdown caret） |
| 12px | inline icon（12-13px font） |
| 13px | ctrl-btn 内 icon |
| 14px | input 内 inline icon |
| 16px | 工具栏、sidebar item、topbar collapse |
| 18px | 输入区 paperclip、send |
| 20-22px | modal 标题、status badge |
| 24px+ | empty state、hero icon |

**颜色**：所有 icon 继承 `currentColor`（lucide 默认行为），通过父元素 text-color 控制。

**添加新图标**：先在 `lucide-vue-next` 文档里查名字，然后加到 `icons/index.ts` 的 export 列表 + 在 `AGENTS.md` §0 的目录里更新（可选）。

**emoji 政策**：AGENTS.md §2.3 没明说，但项目惯例是**不用 emoji**，全部换成 lucide 图标。message 里的 emoji 渲染保留（用户输入/AI 输出），但 UI chrome 不应该出现 emoji。

---

## 6. 滚动条

全局样式（已在 style.css）：
```css
::-webkit-scrollbar { width: 8px; height: 8px; }
::-webkit-scrollbar-track { background: transparent; }
::-webkit-scrollbar-thumb {
  background: var(--border-strong);
  border-radius: var(--radius-pill);
  border: 2px solid transparent;
  background-clip: padding-box;
}
::-webkit-scrollbar-thumb:hover { background: var(--text-quaternary); }
```

**自定义滚动条例外**：
- ChatWindow 的 messages-scroll 用 `scrollbar-width: thin; scrollbar-color: ...`（Firefox 兼容）
- 不要再加新的 `::-webkit-scrollbar` 规则 —— 8px pill 样式全局生效

---

## 7. Focus / Accessibility

```css
:focus-visible {
  outline: 2px solid var(--brand-500);
  outline-offset: 2px;
  border-radius: var(--radius-sm);
}
```

**规则**：
- 永远不要 `outline: none` 除非替换为自定义 focus ring
- 自定义 focus ring 必须用 `var(--brand-500)` 而不是 `outline: auto`（浏览器默认）
- 按钮、链接、input 都必须有可见 focus 态（默认 `:focus-visible` 已覆盖）

**键盘快捷键约定**：
- `Enter` 发送
- `Shift+Enter` 换行
- `Esc` 停止流 / 关闭 dropdown
- `Ctrl/Cmd+K` 命令面板（如有）
- `/` 前缀 = 斜杠命令

---

## 8. 动画 / Transition

### 8.1 标准 transition

```css
.foo {
  transition: var(--transition-colors);
}
```

### 8.2 Enter / Leave 动画

优先复用 `style.css` 里已经注册的全局 name，不要再在组件里复制一份：

| name | 用途 |
|---|---|
| `fade` | 纯透明度（lightbox 蒙层、轻提示） |
| `fade-scale` | 弹层 / 卡片出现（scale 0.96） |
| `fade-up` | 从下方 8px 浮入（FAB、dock） |
| `row-slide` | 输入区 advanced row（max-height + opacity） |

Enter 用 `--ease-out`（稍慢），leave 用 `--ease-in`（稍快）。Naive UI 的 `fade-in-scale-up` / `slide-in-from-right` 已在 `style.css` 覆盖成同一套节奏。

```vue
<Transition name="fade-scale">
  <div v-if="show">...</div>
</Transition>
```

**新增前先查重**，不要发明第四种 fade。

### 8.3 Reduced Motion

全局已有：
```css
@media (prefers-reduced-motion: reduce) {
  *, *::before, *::after {
    animation-duration: 0.01ms !important;
    transition-duration: 0.01ms !important;
    scroll-behavior: auto !important;
  }
}
```

不要在组件里再写自己的 reduced-motion 处理。

### 8.4 Streaming 动画性能约束

WebView2 **GPU 进程**（合成器/光栅线程）对流式期间的动画极其敏感——流式
可以持续几分钟，一个每帧都在动的动画会让 GPU 进程整段时间满转。规则
（2026-08-06 起强制）：

- **禁止**给整个消息气泡 / 大图层加 streaming opacity 脉冲动画
  （已从 `MessageBubble.vue` 移除，改用 6px stream-dot 提示）
- **禁止**用 `background-position` 动画做 shimmer（每帧重绘 + GPU raster；
  已从 `SubAgentCard.vue` 移除）
- 流式指示优先用 ≤20px 的小组件（dot / spinner / caret），且只动画
  `opacity` / `transform`（compositor-only，不动 layout/paint）。**唯一例外**：
  对话页顶部的**全宽加载条**（`StreamingBar.vue`，3px）——它用 `transform`
  平移一条超宽渐变条 + `background-size` 重复模式实现无缝滚动（纯合成器，
  无 `background-position` 动画），所以允许存在
- 流式文本用 `textContent` 直写（TypedText 模式），**不**每 delta 跑
  `marked.parse`；markdown 只渲染一次，走共享缓存
  `utils/markdownCache.ts`

---

## 9. 主题持久化

```ts
// App.vue
const THEME_KEY = 'pchat-theme'
const themeName = ref<'dark' | 'light'>('dark')

onMounted(() => {
  const stored = localStorage.getItem(THEME_KEY)
  if (stored === 'dark' || stored === 'light') themeName.value = stored
  else {
    // OS preference fallback
    const os = useOsTheme()
    themeName.value = os.value === 'light' ? 'light' : 'dark'
  }
})

watch(themeName, (n) => {
  document.documentElement.setAttribute('data-theme', n)
  localStorage.setItem(THEME_KEY, n)
})
```

**不要**在组件里自己读 localStorage 主题 —— 走 App.vue 的单一来源。

---

## 9.5 Composer Dock Stack（输入区上方面板栈）

聊天主列底部（[`ChatWindow.vue`](../../frontend/src/components/ChatWindow.vue)）在消息列表与输入框之间叠一组 dock。**顺序固定，不要随意插入或换位**：

| 自上而下 | 组件 | 角色 | 高度预算 |
|---|---|---|---|
| 1 | `QuestionModal` | LLM 提问（需立即回答） | `max-height: min(42vh, 420px)` |
| 2 | `TodoPanel` | 会话待办进度 | 折叠 36px；展开 `min(50vh, 320px)` |
| 3 | `SubAgentJobsPanel` | 后台子代理状态 | 默认折叠；展开列最多 5 条 |
| 4 | `InputArea` 内 `.turn-queue` | 用户排队待发 | 默认一行摘要；展开约 3 行（`calc(var(--space-8) * 2.5)`） |
| 5 | `InputArea` 输入框 + 底栏 | 输入 | 自身内容高度，`flex-shrink: 0` |

全屏模态（`ToolConfirmModal` / 设置 / `ImageLightbox`）**不进此栈**，走独立 mask。

### 互斥与让路规则

- Todo / SubAgent / TurnQueue **同时只允许一个展开**，由 `state.composerExpandedDock`（`'todo' | 'subagent' | 'queue' | null`）协调：`setComposerExpandedDock` / `toggleComposerExpandedDock`。
- **Question 打开时**：Todo 禁展开、SubAgent 强制折叠、TurnQueue 收成摘要条；若队列里有 **失败项**，TurnQueue 仍可展开以便重试/删除。
- 切会话时清空 `composerExpandedDock`。
- 跳转 FAB（`jump-to-bottom` / `anchor-fab`）按「折叠 Todo + 排队摘要 + 输入」估算 `bottom`，不随展开动态测高。

新增底部面板前：先确认属于交互阻断 / 进度 / 后台状态 / 贴输入哪一类，再决定插入位置与是否参与互斥。

---

## 10. 强制约束（改动前先读）

### 10.1 必须遵守

1. **所有颜色/间距/圆角/阴影/动效走 token**，不写裸值
2. **新 token 必须双主题都加**（`:root[data-theme="dark"]` 和 `:root[data-theme="light"]`）
3. **主界面必须遵守 §0.5 Project-Aware Calm Workbench**（壳层、色彩纪律、thinking/tool 分离）
4. **图标用 `lucide-vue-next`**，通过 `./icons` barrel 导入
5. **按钮优先复用 `.opt-pick` / `.ctrl-btn` / §3.10 calm-cta**，不发明高饱和新变体
6. **设置面板用 `.settings-section` + `.settings-form-row`** 模板
7. **Markdown 走 `.md-body` class**，不重新定义
8. **CSS 写在 `<style scoped>`**，不用全局 `<style>`（除非在 style.css）
9. **改动后跑 `npm run build`**，vue-tsc 必须通过
10. **子代理 UI 必须展示 `sub_agent_type` / `subagent_type` 与运行模式，但折叠态要合并为一段紧凑 meta，展开态按嵌套对话列表设计**
11. **供应商协议字段表示 LLM 请求协议，不写成 HTTP/HTTPS**

### 10.2 禁止事项

1. ❌ 写 `color: #4A4DFF` / `padding: 13px` / `border-radius: 8px` 这类裸值
2. ❌ 在组件里写 `[data-theme="light"] .foo { ... }` —— 加 token 解决
3. ❌ 用 NSelect 替代 opt-pick / NDropdown
4. ❌ 在 chat-main 加 `max-height` / 在 input-area 加 `max-height`
5. ❌ 用 NScrollbar 替代普通 `div` + `overflow-y: auto`
6. ❌ 改 `.md-body` 里的样式（除非是修 markdown 渲染问题）
7. ❌ 在 `<style>` 块（非 scoped）写组件样式
8. ❌ `outline: none` 不替换为自定义 focus ring
9. ❌ emoji 出现在 UI chrome（按钮、label、icon 位置）
10. ❌ 直接从 `lucide-vue-next` 导入（绕过 barrel）
11. ❌ 把 `thinking` 并入 `.event-timeline` / 与 tool 合并展示
12. ❌ 侧栏主 CTA、Rail active、Inspector Tab 使用大面积高饱和 brand 实心块
13. ❌ 恢复长期可见的四列「项目列表独占一栏」布局
14. ❌ tool error 使用大面积红底警告块（只用细红边 + 短摘要）
15. ❌ 删除现有功能入口而不提供等价可达路径
16. ❌ 子代理展开态做成普通详情说明卡，缺少自己的消息列表
17. ❌ 供应商协议字段显示 HTTPS / HTTP，或模型弹窗继续展示「功能标签」
18. ❌ 子代理折叠态同时展示首字母图标、`类型` badge、`后台子代理` badge，造成重复噪声
19. ❌ 在 ProjectSwitcher 触发器旁重复放「新建项目」`+` 按钮，挤压当前项目名称；AppRail 左下角快捷 `+` 需要保留

### 10.3 改动现有样式前

1. 先读 **§0.5**，确认不偏离 Calm Workbench
2. 读相关组件的 `<style scoped>` 块（不是只看 template）
3. 找到现有 token 复用 —— 不要创造并行 token（如 `my-button-bg`）
4. 跑 `npm run build` 验证 vue-tsc
5. 亮色 + 暗色都做视觉确认
6. 涉及消息 parts 时，确认 thinking 仍独立于 tool 时间线
7. 涉及设置页时，确认请求协议、Base URL、模型字段语义准确

---

## 11. 新组件 checklist

写新 Vue 组件时按此顺序检查：

- [ ] 是否落在主壳内？若是 → 对齐 §0.5 / §3.8
- [ ] 容器用 `display: flex` 或 `display: grid`，**不用 `position: absolute`** 除非确有需求
- [ ] 颜色全部走 token，间距全部走 `--space-*`
- [ ] 圆角用 `--radius-sm/md/lg/xl/pill` 之一
- [ ] 字号用 `13px / 12.5px / 11.5px` 三档（label / button / hint）
- [ ] 按钮复用 `.opt-pick` / `.ctrl-btn` / calm-cta（不发明高饱和新变体）
- [ ] 若含 thinking / tool parts → 遵守 §0.5.5 / §3.9 分离规则
- [ ] Hover/active/focus/disabled 四个态都有
- [ ] 暗色 + 亮色都视觉验证
- [ ] UI chrome 无 emoji
- [ ] 跑 `npm run build` 通过 vue-tsc
- [ ] 若是新核心壳层组件，在 §12 与 AGENTS.md 速查表补一行

---

## 12. 相关文件位置速查

| 内容 | 文件 |
|---|---|
| 设计 tokens（含 workbench） | `frontend/src/style.css` |
| **产品视觉方向（已锁定）** | `.agents/docs/frontend-design.md` §0.5 |
| 字体 woff2 | `frontend/src/assets/fonts/InterVariable.woff2` |
| 图标 barrel | `frontend/src/components/icons/index.ts` |
| App 壳层 | `frontend/src/App.vue` |
| AppRail | `frontend/src/components/AppRail.vue` |
| ProjectSwitcher | `frontend/src/components/ProjectSwitcher.vue` |
| SessionSidebar | `frontend/src/components/SessionSidebar.vue` |
| TopBar | `frontend/src/components/TopBar.vue` |
| InspectorPanel | `frontend/src/components/InspectorPanel.vue` |
| Chat 布局 / Dock Stack | `frontend/src/components/ChatWindow.vue` |
| MessageBubble parts / timeline | `frontend/src/components/MessageBubble.vue` |
| ThinkingBlock（独立） | `frontend/src/components/ThinkingBlock.vue` |
| ToolCallCard / SkillCallCard / SubAgentCard | `frontend/src/components/*Card.vue` |
| opt-pick / ctrl-btn / composer | `frontend/src/components/InputArea.vue` |
| settings-section / settings-form | `frontend/src/components/AppSettingsModal.vue` |
| Markdown 渲染样式 | `frontend/src/style.css`（`.md-body`） |
| 主题切换 / 持久化 | `frontend/src/App.vue` |
| 重构计划（历史） | `docs/plans/project-aware-ui-redesign-plan.md` |
