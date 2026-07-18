<script setup lang="ts">
/**
 * TopBar — sits between the sidebar and the chat canvas. Holds the
 * top-level "where am I" affordances so the chat canvas can stay
 * focused on messages.
 *
 * Layout (flex row, 56px tall):
 *   ┌─────────────────────────────────────────────────────────────┐
 *   │ [☰] [logo]  会话标题 · 项目路径  ·     Claude 3.5 [📂][🖥] │
 *   └─────────────────────────────────────────────────────────────┘
 *
 *   left:    sidebar collapse toggle + brand logo (clickable to
 *            go home / new chat)
 *   center:  session title (H1) + project breadcrumb separator
 *   right:   current model badge + project-level actions
 *            (open folder, open terminal — only when a project
 *            is active)
 *
 * Token count + user avatar (from the design plan) are not
 * implemented yet — the P-Chat runtime doesn't track per-session
 * token totals on the client, and there's no user-account
 * concept (it's a local app). The right section will fill in as
 * those become real.
 */
import { computed, ref } from 'vue'
import { NButton, NTooltip, useMessage } from 'naive-ui'
import { state, currentMeta, openContextInspector } from '../stores/chat'
import * as api from '../api/client'
import BrandLogo from './BrandLogo.vue'
import ToolListDrawer from './ToolListDrawer.vue'
import { FolderOpen, Terminal, PanelLeftClose, PanelLeftOpen, Sparkles, BarChart3, Wrench, Hash } from './icons'
import { copyText } from '../utils/clipboard'

const props = defineProps<{
  /** Whether the sidebar is currently collapsed. Two-way bound. */
  collapsed?: boolean
}>()
const emit = defineEmits<{
  (e: 'toggle-sidebar'): void
}>()

// --- Current session display ---------------------------------------------
const currentSession = computed(() =>
  state.sessions.find(s => s.id === state.currentID) || null,
)
// Only show the title when there's a real session — otherwise the
// fallback 'P-Chat' would duplicate the brand mark on the left
// (and look like a layout bug to the user).
const sessionTitle = computed(() => currentSession.value?.title || '')
const projectName = computed(() => {
  if (!state.activeProjectPath) return ''
  const p = state.projects.find(p => p.path === state.activeProjectPath)
  return p?.name || state.activeProjectPath
})

// --- Current model display -----------------------------------------------
// currentMeta resolves the active provider + model for the current
// session. The provider list has the protocol so we can color the
// badge dot accordingly. P-Chat doesn't assign explicit provider
// colors — we derive a hue from the protocol so visually
// distinguishable, but stable.
const modelLabel = computed(() => {
  const m = currentMeta.value
  if (!m.model) return '未选择模型'
  return m.model
})
const providerLabel = computed(() => currentMeta.value.provider || '')

// Provider color: deterministic mapping from protocol → CSS color.
// We use semantic hues (Anthropic = orange, OpenAI = green, CS
// proxy = brand purple) so a glance tells the user which protocol
// their request will go through. Falls back to neutral gray.
const providerColor = computed(() => {
  const m = currentMeta.value
  if (!m.provider) return 'var(--text-quaternary)'
  const p = state.providers.find(p => p.name === m.provider) as any
  const protocol = p?.protocol as string | undefined
  if (protocol === 'anthropic') return 'var(--provider-anthropic)'
  if (protocol === 'openai') return 'var(--provider-openai)'
  // Custom / CS proxy: use brand purple so it ties into the
  // app's identity. Could be overridden by a per-provider color
  // field in a future schema change.
  if (m.provider === 'cs' || protocol === 'openai-compatible') return 'var(--provider-openai-compatible)'
  return 'var(--text-tertiary)'
})

const canOpenProject = computed(() => !!state.activeProjectPath)

async function openExplorer() {
  if (!state.activeProjectPath) return
  try { await api.openExplorer(state.activeProjectPath) } catch { /* ignore */ }
}
async function openTerminal() {
  if (!state.activeProjectPath) return
  try { await api.openTerminal(state.activeProjectPath) } catch { /* ignore */ }
}

// showToolList toggles the P3-2 ToolListDrawer.
// Kept local (not in the chat store) because the tool
// list is per-server, not per-session.
const showToolList = ref(false)
const message = useMessage()

// currentTraceId: the P3-3 end-to-end correlation id for the
// active turn. Minted server-side on POST /messages, mirrored
// on every SSE event's `trace_id` field, and rendered in the
// "trace" tooltip + one-click copy button in the TopBar so
// users reporting bugs can paste it without having to scroll
// to the error chip. Falls back to the X-Trace-Id response
// header (also minted server-side) when the SSE stream has
// not produced any events yet.
//
// We don't try to aggregate across multiple in-flight
// sessions; this is the trace for whatever the user is
// looking at right now, which is also the trace they'd
// paste when reporting "this went wrong".
const currentTraceId = computed(() => state.currentTraceId || '')

async function copyTrace() {
  const id = currentTraceId.value
  if (!id) {
    message.info('当前没有 trace id（最近一次请求尚未产生事件）')
    return
  }
  const ok = await copyText(id)
  if (ok) {
    message.success(`已复制 trace id: ${id}`)
  } else {
    message.error('复制失败，请手动选择')
  }
}
function toggleSidebar() { emit('toggle-sidebar') }
</script>

<template>
  <header class="topbar">
    <!-- Left section: collapse toggle + (optional) brand mark.
         The brand mark is hidden when the sidebar is expanded
         — the sidebar's own header already shows the logo +
         "P-Chat" name, so showing it twice in the top bar is
         redundant. When the sidebar is collapsed the top bar
         becomes the only place for the brand mark, so it
         reappears. The collapse button stays either way
         (it's how the user gets the sidebar back). -->
    <div class="topbar-left">
      <button
        type="button"
        class="collapse-btn"
        :aria-label="props.collapsed ? '展开侧边栏' : '收起侧边栏'"
        :title="props.collapsed ? '展开侧边栏' : '收起侧边栏'"
        @click="toggleSidebar"
      >
        <component :is="props.collapsed ? PanelLeftOpen : PanelLeftClose" :size="18" />
      </button>
      <button
        v-if="props.collapsed"
        type="button"
        class="brand"
        :title="'返回主页'"
        :aria-label="'返回主页'"
      >
        <BrandLogo :size="22" />
        <span class="brand-text">P-Chat</span>
      </button>
    </div>

    <!-- Center section: session title + project breadcrumb. Hidden
         when there's no real session — otherwise the empty-string
         fallback would render a blank gap that pushes the model
         badge off-center. -->
    <div v-if="sessionTitle" class="topbar-center">
      <div class="session-title" :title="sessionTitle">{{ sessionTitle }}</div>
      <template v-if="projectName">
        <span class="separator" aria-hidden="true">·</span>
        <div class="project-crumb" :title="state.activeProjectPath">
          <FolderOpen :size="13" class="project-crumb-icon" />
          <span class="project-crumb-name">{{ projectName }}</span>
        </div>
      </template>
    </div>

    <!-- Right section: model badge + project actions. -->
    <div class="topbar-right">
      <!-- P2-3: context inspector trigger. The drawer
           itself lives in ChatWindow (so it overlays
           the chat area, not the top bar). Clicking
           this button opens it AND kicks off the
           fetch in one go. -->
      <NTooltip>
        <template #trigger>
          <NButton
            size="tiny"
            quaternary
            aria-label="查看上下文占用"
            @click="openContextInspector(state.currentID)"
          >
            <BarChart3 :size="16" />
          </NButton>
        </template>
        上下文占用
      </NTooltip>
      <!-- P3-3: global trace id. Clicking copies the
           current session's correlation id to the
           clipboard so users reporting a bug can
           paste it into the issue without scrolling
           to the error chip. Tooltip shows the id
           (or a hint when no stream is in flight). -->
      <NTooltip>
        <template #trigger>
          <NButton
            size="tiny"
            quaternary
            :aria-label="currentTraceId ? `复制 trace id ${currentTraceId}` : '当前无 trace id'"
            @click="copyTrace"
          >
            <Hash :size="16" />
          </NButton>
        </template>
        {{ currentTraceId ? `trace: ${currentTraceId}` : '当前无 trace id' }}
      </NTooltip>
      <!-- P3-2: tool list trigger. Opens the drawer
           that lists every tool the LLM can call —
           built-ins plus the user's dynamic YAML
           tools. Sits next to the context inspector
           because both are "what's the agent doing
           right now?" affordances. -->
      <NTooltip>
        <template #trigger>
          <NButton
            size="tiny"
            quaternary
            aria-label="查看工具列表"
            @click="showToolList = true"
          >
            <Wrench :size="16" />
          </NButton>
        </template>
        工具列表
      </NTooltip>
      <div class="model-badge" :title="`提供商: ${providerLabel || '未选择'}`">
        <span class="model-dot" :style="{ background: providerColor }" aria-hidden="true" />
        <Sparkles :size="13" class="model-badge-icon" />
        <span class="model-name">{{ modelLabel }}</span>
      </div>
      <template v-if="canOpenProject">
        <NTooltip>
          <template #trigger>
            <NButton
              size="tiny"
              quaternary
              aria-label="打开资源管理器"
              @click="openExplorer"
            >
              <FolderOpen :size="16" />
            </NButton>
          </template>
          打开资源管理器
        </NTooltip>
        <NTooltip>
          <template #trigger>
            <NButton
              size="tiny"
              quaternary
              aria-label="打开终端"
              @click="openTerminal"
            >
              <Terminal :size="16" />
            </NButton>
          </template>
          打开终端
        </NTooltip>
      </template>
    </div>

    <!-- P3-2: tool list drawer. Rendered at the
         top-bar level (not in ChatWindow) so it's
         available even on screens that don't have
         an active session — the user might want to
         browse their tool list before picking a
         session. -->
    <ToolListDrawer v-model:show="showToolList" />
  </header>
</template>

<style scoped>
.topbar {
  height: 56px;
  flex-shrink: 0;
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 0 16px;
  background: var(--surface-1);
  border-bottom: 1px solid var(--border-subtle);
  z-index: 10;
}

/* Left section ------------------------------------------------------- */
.topbar-left {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-shrink: 0;
}
.collapse-btn {
  width: 32px;
  height: 32px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border: 1px solid transparent;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--text-secondary);
  cursor: pointer;
  transition: background var(--dur-fast) var(--ease-out), color var(--dur-fast) var(--ease-out);
}
.collapse-btn:hover {
  background: var(--surface-3);
  color: var(--text-primary);
}
.brand {
  display: inline-flex;
  align-items: center;
  gap: 8px;
  padding: 4px 8px;
  background: transparent;
  border: 1px solid transparent;
  border-radius: var(--radius-sm);
  color: var(--text-primary);
  cursor: pointer;
  font-size: 14px;
  font-weight: 600;
  letter-spacing: -0.01em;
}
.brand:hover {
  background: var(--surface-3);
}
.brand-text {
  /* Hide the wordmark in narrow viewports — the logo alone
   * carries the brand. The breakpoint matches Naive UI's
   * default breakpoint for icon-only toolbars. */
}
@media (max-width: 720px) {
  .brand-text { display: none; }
}

/* Center section ----------------------------------------------------- */
.topbar-center {
  flex: 1;
  min-width: 0;
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 13.5px;
  color: var(--text-primary);
}
.session-title {
  font-weight: 500;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  max-width: 360px;
}
.separator {
  color: var(--text-quaternary);
  flex-shrink: 0;
}
.project-crumb {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  color: var(--text-tertiary);
  font-size: 12.5px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  min-width: 0;
}
.project-crumb-icon {
  flex-shrink: 0;
  color: var(--text-tertiary);
}
.project-crumb-name {
  overflow: hidden;
  text-overflow: ellipsis;
}

/* Right section ------------------------------------------------------ */
.topbar-right {
  display: flex;
  align-items: center;
  gap: 4px;
  flex-shrink: 0;
}
.model-badge {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  padding: 4px 10px;
  margin-right: 4px;
  background: var(--surface-2);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-pill);
  font-size: 12px;
  color: var(--text-secondary);
  cursor: default;
  user-select: none;
}
.model-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  flex-shrink: 0;
  box-shadow: 0 0 0 2px var(--surface-2);
}
.model-badge-icon {
  color: var(--text-tertiary);
  flex-shrink: 0;
}
.model-name {
  font-weight: 500;
  color: var(--text-primary);
  white-space: nowrap;
  max-width: 160px;
  overflow: hidden;
  text-overflow: ellipsis;
}
</style>
