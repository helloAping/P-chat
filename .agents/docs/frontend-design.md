# Frontend Design System

> **位置**：`frontend/src/style.css`（design tokens）+ 各 `.vue` 组件的 `<style scoped>`（组件规则）
> **目的**：固定 P-Chat 的视觉语言和编码约束，避免每个组件自由发挥造成"视觉碎片化"
> **适用**：所有 Vue 3 + Naive UI 组件的样式修改

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

### 1.9 Motion

| Token | 值 | 用途 |
|---|---|---|
| `--ease-out` | `cubic-bezier(0.16, 1, 0.3, 1)` | 进入动画（"out" 曲线） |
| `--ease-in-out` | `cubic-bezier(0.4, 0, 0.2, 1)` | 状态变化 |
| `--dur-fast` | `120ms` | hover、focus |
| `--dur-base` | `200ms` | 默认 transition |
| `--dur-slow` | `320ms` | modal 出现/消失 |

**标准 transition 写法**：
```css
transition: background var(--dur-fast) var(--ease-out),
            color var(--dur-fast) var(--ease-out),
            border-color var(--dur-fast) var(--ease-out);
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
- label 字号 13px `var(--text-primary)` 500 weight（**不是 secondary**）
- hint 字号 11.5px `var(--text-tertiary)` line-height 1.5

**禁止**：
- 用 `style="margin: 0;"` 内联覆盖（用专门的 modifier class）
- 用 `var(--text-secondary)` 写 label（label 必须是 `--text-primary`）
- 把 toggle 写成 `flex-direction: row` 散在 row 里（统一用 `.settings-form-toggle` 包装）

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

**结构**（`MessageBubble.vue`）：
```vue
<div v-for="(part, i) in message.parts" :key="i">
  <ThinkingBlock v-if="part.kind === 'thinking'" :text="part.text" :streaming="part.streaming" />
  <TypedText v-else-if="part.kind === 'text' && streaming" :text="part.text" />
  <div v-else-if="part.kind === 'text'" v-html="renderMd(part.text)" />
  <ToolCallCard v-else-if="part.kind === 'tool'" :... />
  <SubAgentCard v-else-if="part.kind === 'sub_agent'" :... />
</div>
```

**色系**：
- assistant 消息用 `--ai-500` 头像底色 + `--ai-50` 头像 hover
- user 消息用 `--brand-500` 头像底色
- thinking 块 collapsible header 用 `--surface-2`，expanded 用 `--surface-1`
- tool call status 颜色：`start` 灰、`ok` 绿、`error` 红、`warn` 橙

**Markdown 渲染**：用 `.md-body` class（已在 style.css 集中定义），包括 p/code/pre/a/ul/ol/blockquote/table/hr/h1-h4/img/strong/em。**不要在组件里重新定义**这些 markdown 元素的样式。

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
  transition: background var(--dur-fast) var(--ease-out),
              color var(--dur-fast) var(--ease-out),
              border-color var(--dur-fast) var(--ease-out);
}
```

### 8.2 Enter / Leave 动画

Modal、tooltip 用 Vue `<Transition>`：
```vue
<Transition name="fade-scale">
  <div v-if="show">...</div>
</Transition>
```

```css
.fade-scale-enter-active,
.fade-scale-leave-active {
  transition: opacity var(--dur-base) var(--ease-out),
              transform var(--dur-base) var(--ease-out);
}
.fade-scale-enter-from,
.fade-scale-leave-to {
  opacity: 0;
  transform: scale(0.96);
}
```

**已有的 transition name**：`.fade` (default)、`.row-slide` (max-height + opacity)。新增前先查重。

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

## 10. 强制约束（改动前先读）

### 10.1 必须遵守

1. **所有颜色/间距/圆角/阴影/动效走 token**，不写裸值
2. **新 token 必须双主题都加**（`:root[data-theme="dark"]` 和 `:root[data-theme="light"]`）
3. **图标用 `lucide-vue-next`**，通过 `./icons` barrel 导入
4. **按钮优先复用 `.opt-pick` / `.ctrl-btn`**，不发明新的按钮变体
5. **设置面板用 `.settings-section` + `.settings-form-row`** 模板
6. **Markdown 走 `.md-body` class**，不重新定义
7. **CSS 写在 `<style scoped>`**，不用全局 `<style>`（除非在 style.css）
8. **改动后跑 `npm run build`**，vue-tsc 必须通过

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

### 10.3 改动现有样式前

1. 读相关组件的 `<style scoped>` 块（不是只看 template）
2. 找到现有 token 复用 —— 不要创造并行 token（如 `my-button-bg`）
3. 跑 `npm run build` 验证 vue-tsc
4. 跑预览 (`scripts/preview-app.py`) 视觉验证两种主题
5. 截图对比改动前后

---

## 11. 新组件 checklist

写新 Vue 组件时按此顺序检查：

- [ ] 容器用 `display: flex` 或 `display: grid`，**不用 `position: absolute`** 除非确有需求
- [ ] 颜色全部走 token，间距全部走 `--space-*`
- [ ] 圆角用 `--radius-sm/md/lg/xl/pill` 之一
- [ ] 字号用 `13px / 12.5px / 11.5px` 三档（label / button / hint）
- [ ] 按钮复用 `.opt-pick` 或 `.ctrl-btn`（不发明新变体）
- [ ] Hover/active/focus/disabled 四个态都有
- [ ] 暗色 + 亮色都视觉验证
- [ ] 跑 `npm run build` 通过 vue-tsc
- [ ] 在 AGENTS.md §5 关键文件位置表里加一行（如果是新核心组件）

---

## 12. 相关文件位置速查

| 内容 | 文件 |
|---|---|
| 设计 tokens | `frontend/src/style.css` |
| 字体 woff2 | `frontend/src/assets/fonts/InterVariable.woff2` |
| 图标 barrel | `frontend/src/components/icons/index.ts` |
| TopBar 品牌显隐 | `frontend/src/components/TopBar.vue:119` |
| opt-pick / ctrl-btn | `frontend/src/components/InputArea.vue` |
| settings-section / settings-form | `frontend/src/components/AppSettingsModal.vue:2533-2602` |
| chat 布局 | `frontend/src/components/ChatWindow.vue:142-170` |
| 输入区高度管理 | `frontend/src/components/InputArea.vue:1402-1415` |
| MessageBubble parts | `frontend/src/components/MessageBubble.vue` |
| Markdown 渲染样式 | `frontend/src/style.css:246-304` |
| 主题切换 | `frontend/src/App.vue:32-118` |
| 主题持久化 | `frontend/src/App.vue:93-103` |
