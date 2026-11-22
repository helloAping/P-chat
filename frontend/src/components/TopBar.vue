<script setup lang="ts">
/**
 * TopBar — sits between the sidebar and the chat canvas. Holds the
 * top-level "where am I" affordances so the chat canvas can stay
 * focused on messages.
 *
 * Layout (flex row, 44px tall):
 *   ┌─────────────────────────────────────────────────────────────┐
 *   │ [☰]   会话标题 · 项目路径                  [theme] [panel] │
 *   └─────────────────────────────────────────────────────────────┘
 *
 *   left:    sidebar collapse toggle
 *   center:  session title (H1) + project breadcrumb separator
   *   right:   mute / theme / inspector toggle
 *
 * Brand mark lives in TitleBar (frameless chrome), not here.
 */
import { computed, ref } from 'vue'
import { NButton, NTooltip } from 'naive-ui'
import { state } from '../stores/chat'
import { notifyManager } from '../utils/notify'
import {
  FolderOpen, PanelLeftClose, PanelLeftOpen, PanelRight, PanelRightClose,
  GitBranch, CheckCircle2, AlertCircle, Sun, Moon, Volume2, VolumeX,
} from './icons'

const props = defineProps<{
  /** Whether the sidebar is currently collapsed. Two-way bound. */
  collapsed?: boolean
  /** Whether the right inspector panel is open. */
  inspectorOpen?: boolean
  /** Current application theme; the toggle lives in the top-right utility group. */
  themeName?: 'dark' | 'light'
}>()
const emit = defineEmits<{
  (e: 'toggle-sidebar'): void
  (e: 'toggle-inspector'): void
  (e: 'toggle-theme'): void
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
function toggleTheme() { emit('toggle-theme') }

const muted = ref(notifyManager.mute)
function toggleMute() {
  notifyManager.unlock()
  notifyManager.mute = !notifyManager.mute
  muted.value = notifyManager.mute
}
const muteTitle = computed(() => muted.value ? '开启提示音' : '关闭提示音')

const themeToggleTitle = computed(() =>
  props.themeName === 'dark' ? '切换到浅色主题' : '切换到深色主题',
)
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
      <div v-if="sessionTitle || !projectName" class="title-block">
        <div
          v-if="sessionTitle"
          class="session-title"
          :title="sessionTitle"
        >{{ sessionTitle }}</div>
        <div v-else class="session-title">全局会话</div>
      </div>
    </div>

    <!-- Right section: mute, theme, and inspector. Session controls
         live as floating capsules above the composer. -->
    <div class="topbar-right">
      <NTooltip>
        <template #trigger>
          <NButton
            size="tiny"
            quaternary
            :aria-label="muteTitle"
            :title="muteTitle"
            @click="toggleMute"
          >
            <component :is="muted ? VolumeX : Volume2" :size="16" />
          </NButton>
        </template>
        {{ muteTitle }}
      </NTooltip>
      <NTooltip>
        <template #trigger>
          <NButton
            size="tiny"
            quaternary
            :aria-label="themeToggleTitle"
            :title="themeToggleTitle"
            @click="toggleTheme"
          >
            <component :is="props.themeName === 'dark' ? Sun : Moon" :size="16" />
          </NButton>
        </template>
        {{ themeToggleTitle }}
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
  position: relative;
  display: flex;
  align-items: center;
  gap: var(--space-3);
  padding: 0 var(--space-4);
  background: var(--surface-0);
  border-bottom: none;
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
.title-block {
  min-width: 0;
}
/* The idle/empty mascot lives above the composer (see InputArea);
 * the top bar no longer hosts a cat. */
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
  /* Pin to the far right even when topbar-center isn't rendered
   * (brand-new untitled session has no title, so center is absent). */
  margin-left: auto;
}

@media (max-width: 960px) {
  .project-meta-group {
    display: none;
  }
}
</style>
