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
import { state, refreshContextUsage } from '../stores/chat'
import * as api from '../api/client'
import { formatCompactTokens } from '../utils/format'
import BrandLogo from './BrandLogo.vue'
import ToolListDrawer from './ToolListDrawer.vue'
import StyleGenModal from './StyleGenModal.vue'
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
// Kept local (not in the chat store); the drawer fetches a
// session-scoped tool view when it opens so project tools follow
// the active conversation.
const showToolList = ref(false)
const message = useMessage()

// showStyleGen toggles the StyleGenModal (generate / optimize an AI
// persona style from the current conversation). Disabled without an
// active session — there's no conversation to learn from.
const showStyleGen = ref(false)

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

// --- Context utilisation badge (P2-3) -----------------------------
// The badge reads the silently-refreshed context inspector data
// (the chat store's refreshContextUsage, fired on session switch
// and after each turn). No data, a stale session, or a missing
// context window → the badge collapses back to a bare icon button.
const ctxData = computed(() => {
  const s = state.currentID
  const d = state.contextInspector?.data
  if (!s || !d || d.session_id !== s) return null
  if (!d.context_window || d.context_window <= 0) return null
  return d
})

// ctxPct is the percentage against the FULL context window — the
// denominator the user reads as "how full the model is" (200K/1M).
// Prefers the server's context_window_pct, falls back to a client
// computation for older servers.
const ctxPct = computed(() => {
  const d = ctxData.value
  if (!d) return 0
  const p = d.context_window_pct
  if (p != null && Number.isFinite(p)) return Math.min(p, 999.9)
  return (d.estimated_tokens / d.context_window) * 100
})
const ctxBarPct = computed(() => Math.min(Math.max(ctxPct.value, 0), 100))
const ctxPctText = computed(() => `${ctxPct.value.toFixed(1)}%`)
const ctxTokensText = computed(() => {
  const d = ctxData.value
  if (!d) return ''
  return `${formatCompactTokens(d.estimated_tokens)} / ${formatCompactTokens(d.context_window)}`
})

// ctxColor mirrors the drawer's thresholds (<60% green, 60-80%
// yellow, >80% red) so the badge and the auto-compact behaviour
// tell the same story at a glance.
const ctxColor = computed(() => {
  const p = ctxPct.value
  if (p >= 80) return 'error'
  if (p >= 60) return 'warning'
  return 'success'
})

// ctxTip is the hover tooltip: model + full-window ratio + usable
// number (all estimates). Plain label when no data is loaded.
const ctxTip = computed(() => {
  const d = ctxData.value
  if (!d) return '上下文占用'
  const usable = d.usable_tokens || d.context_window
  return `上下文占用 · ${d.model || '未知模型'} · ${formatCompactTokens(d.estimated_tokens)} / ${formatCompactTokens(d.context_window)}（${ctxPct.value.toFixed(1)}%）· 可用 ${formatCompactTokens(usable)}（估算）`
})
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
        title="打开会话列表"
        aria-label="打开会话列表"
        @click="toggleSidebar"
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

    <!-- Right section: project actions. The model name used to
         live here but was removed — the per-message assistant
         header already shows which model answered. -->
    <div class="topbar-right">
      <!-- P2-3: context usage. Clicking refreshes the estimate
           (no popup — the old drawer is gone). When the badge
           data is loaded (silent refreshContextUsage on session
           switch / turn end), we render a compact utilisation
           badge so the context ratio is visible at a glance;
           without data it falls back to a bare icon. -->
      <NTooltip>
        <template #trigger>
          <NButton
            v-if="!ctxData"
            size="tiny"
            quaternary
            aria-label="查看上下文占用"
            @click="refreshContextUsage(state.currentID)"
          >
            <BarChart3 :size="16" />
          </NButton>
          <button
            v-else
            type="button"
            class="ctx-badge"
            :class="ctxColor"
            :aria-label="`查看上下文占用：${ctxTip}`"
            @click="refreshContextUsage(state.currentID)"
          >
            <BarChart3 :size="15" />
            <span class="ctx-badge-bar">
              <span class="ctx-badge-bar-fill" :style="{ width: ctxBarPct + '%' }" />
            </span>
            <span class="ctx-badge-pct">{{ ctxPctText }}</span>
            <span class="ctx-badge-tokens">{{ ctxTokensText }}</span>
          </button>
        </template>
        {{ ctxTip }}（点击刷新）
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
      <!-- StyleGen: generate / optimize an AI persona from the current
           conversation. Disabled when there's no active session. -->
      <NTooltip>
        <template #trigger>
          <NButton
            size="tiny"
            quaternary
            aria-label="从当前对话生成风格"
            :disabled="!state.currentID"
            @click="showStyleGen = true"
          >
            <Sparkles :size="16" />
          </NButton>
        </template>
        生成风格
      </NTooltip>
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
    <!-- StyleGen modal: generate / optimize an AI style from the
         current conversation (sources state.currentID read-only). -->
    <StyleGenModal v-model:show="showStyleGen" />
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

/* --- Context utilisation badge -----------------------------------
 * Replaces the bare context icon when badge data is loaded. The
 * fill colour follows the same thresholds as the context drawer
 * (<60% success, 60-80% warning, >80% error) so the badge and the
 * auto-compact behaviour tell one story. */
.ctx-badge {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 26px;
  padding: 0 8px;
  background: var(--surface-2);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-pill);
  color: var(--text-secondary);
  cursor: pointer;
  transition: background var(--dur-fast) var(--ease-out),
              border-color var(--dur-fast) var(--ease-out);
}
.ctx-badge:hover {
  background: var(--surface-3);
  border-color: var(--border-default);
}
.ctx-badge-bar {
  width: 36px;
  height: 4px;
  border-radius: 2px;
  background: var(--surface-3);
  overflow: hidden;
  flex-shrink: 0;
}
.ctx-badge-bar-fill {
  display: block;
  height: 100%;
  border-radius: 2px;
  background: currentColor;
  transition: width var(--dur-base) var(--ease-out);
}
.ctx-badge-pct {
  font-size: 11px;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
}
.ctx-badge-tokens {
  font-size: 10.5px;
  font-family: var(--font-mono);
  font-variant-numeric: tabular-nums;
  white-space: nowrap;
  color: var(--text-tertiary);
}
.ctx-badge.success { color: var(--success-500, #10b981); }
.ctx-badge.warning { color: var(--warn-500, #f59e0b); }
.ctx-badge.error   { color: var(--error-500, #ef4444); }
</style>
