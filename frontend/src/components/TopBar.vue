<script setup lang="ts">
/**
 * TopBar — sits between the sidebar and the chat canvas. Holds the
 * top-level "where am I" affordances so the chat canvas can stay
 * focused on messages.
 *
 * Layout (flex row, 44px tall):
 *   ┌─────────────────────────────────────────────────────────────┐
 *   │ [☰]   会话标题 · 项目路径                    [ctx] [panel] │
 *   └─────────────────────────────────────────────────────────────┘
 *
 *   left:    sidebar collapse toggle
 *   center:  session title (H1) + project breadcrumb separator
 *   right:   context / inspector toggle
 *
 * Brand mark lives in TitleBar (frameless chrome), not here.
 */
import { computed } from 'vue'
import { NButton, NTooltip } from 'naive-ui'
import { state, refreshContextUsage } from '../stores/chat'
import { formatCompactTokens } from '../utils/format'
import {
  FolderOpen, PanelLeftClose, PanelLeftOpen, PanelRight, PanelRightClose,
  BarChart3, GitBranch, CheckCircle2, AlertCircle,
} from './icons'

const props = defineProps<{
  /** Whether the sidebar is currently collapsed. Two-way bound. */
  collapsed?: boolean
  /** Whether the right inspector panel is open. */
  inspectorOpen?: boolean
}>()
const emit = defineEmits<{
  (e: 'toggle-sidebar'): void
  (e: 'toggle-inspector'): void
}>()

// --- Current session display ---------------------------------------------
const currentSession = computed(() =>
  state.sessions.find(s => s.id === state.currentID) || null,
)
const activeProject = computed(() => {
  if (!state.activeProjectPath) return null
  return state.projects.find(p => p.path === state.activeProjectPath) || null
})
// Only show the title when there's a real session — otherwise the
// fallback 'P-Chat' would duplicate the brand mark on the left
// (and look like a layout bug to the user).
const sessionTitle = computed(() => currentSession.value?.title || '')
const projectName = computed(() => {
  if (!state.activeProjectPath) return ''
  return activeProject.value?.name || state.activeProjectPath
})

const projectMetaItems = computed(() => {
  const p = activeProject.value
  if (!p) return []
  const items: Array<{ key: string; label: string; kind: 'branch' | 'clean' | 'dirty' }> = []
  if (p.branch) items.push({ key: 'branch', label: p.branch, kind: 'branch' })
  if (typeof p.dirty === 'boolean') {
    items.push({
      key: 'dirty',
      label: p.dirty ? '工作区有改动' : '工作区干净',
      kind: p.dirty ? 'dirty' : 'clean',
    })
  }
  return items
})

function toggleSidebar() { emit('toggle-sidebar') }
function toggleInspector() { emit('toggle-inspector') }

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
    <!-- Left: sidebar collapse only. Brand lives in TitleBar
         (frameless chrome); repeating it here adds noise. -->
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
    </div>

    <!-- Center: project / session breadcrumb. Hidden when there's
         no real session — otherwise the empty-string fallback
         would render a blank gap that pushes the badges off-center. -->
    <div v-if="sessionTitle || projectName" class="topbar-center">
      <template v-if="projectName">
        <div class="project-crumb" :title="state.activeProjectPath">
          <FolderOpen :size="13" class="project-crumb-icon" />
          <span class="project-crumb-name">{{ projectName }}</span>
        </div>
        <div v-if="projectMetaItems.length" class="project-meta-group" aria-label="项目状态">
          <span
            v-for="item in projectMetaItems"
            :key="item.key"
            class="project-meta-chip"
            :class="`project-meta-chip--${item.kind}`"
          >
            <GitBranch v-if="item.kind === 'branch'" :size="12" />
            <CheckCircle2 v-else-if="item.kind === 'clean'" :size="12" />
            <AlertCircle v-else :size="12" />
            <span>{{ item.label }}</span>
          </span>
        </div>
        <span v-if="sessionTitle" class="separator" aria-hidden="true">/</span>
      </template>
      <div
        v-if="sessionTitle"
        class="session-title"
        :title="sessionTitle"
      >{{ sessionTitle }}</div>
      <template v-else-if="!projectName">
        <div class="session-title">全局会话</div>
      </template>
    </div>

    <!-- Right section: context and inspector only. Project/session utilities
         live in the InspectorPanel so this toolbar stays calm. -->
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
      <NTooltip>
        <template #trigger>
          <NButton
            size="tiny"
            quaternary
            :aria-label="props.inspectorOpen ? '收起检查器' : '打开检查器'"
            @click="toggleInspector"
          >
            <component :is="props.inspectorOpen ? PanelRightClose : PanelRight" :size="16" />
          </NButton>
        </template>
        {{ props.inspectorOpen ? '收起检查器' : '打开检查器' }}
      </NTooltip>
    </div>

  </header>
</template>

<style scoped>
.topbar {
  height: 40px;
  flex-shrink: 0;
  display: flex;
  align-items: center;
  gap: var(--space-3);
  padding: 0 var(--space-4);
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
  width: var(--control-height);
  height: var(--control-height);
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
.project-meta-group {
  min-width: 0;
  display: inline-flex;
  align-items: center;
  gap: var(--space-1);
  flex-shrink: 0;
}
.project-meta-chip {
  display: inline-flex;
  align-items: center;
  gap: var(--space-1);
  min-height: calc(var(--control-height) - var(--space-1));
  padding: 0 var(--space-2);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-pill);
  background: var(--surface-0);
  color: var(--text-tertiary);
  font-size: 11px;
  line-height: 1;
  white-space: nowrap;
}
.project-meta-chip--branch {
  color: var(--text-secondary);
}
.project-meta-chip--clean {
  color: var(--success-500);
}
.project-meta-chip--dirty {
  color: var(--warn-500);
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
.ctx-badge.success { color: var(--success-500); }
.ctx-badge.warning { color: var(--warn-500); }
.ctx-badge.error   { color: var(--error-500); }

@media (max-width: 960px) {
  .project-meta-group {
    display: none;
  }
}
</style>
