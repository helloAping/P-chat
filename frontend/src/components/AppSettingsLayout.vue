<script setup lang="ts">
/**
 * AppSettingsLayout — the shell for AppSettingsModal (and any
 * future modal that benefits from a "settings dialog" pattern).
 *
 * Before PR #8, the app's settings dialog used Naive UI's
 * `NTabs` rendered as a horizontal bar across the top of the
 * modal. That works for a 2-tab settings dialog but doesn't
 * scale to P-Chat's 8 tabs (LLM / 风格 / 系统 / 归档 /
 * 技能 / MCP / 知识库 / 网络搜索) — the top bar wraps
 * aggressively and the content area for the active tab has
 * to compete with the rest of the bar.
 *
 * The new layout is a 2-column flex:
 *
 *   ┌────────────────────────────────────────────────────┐
 *   │  [header: title · save/close]                       │  56px sticky
 *   ├──────────┬─────────────────────────────────────────┤
 *   │          │                                          │
 *   │  [nav1]  │                                          │
 *   │  [nav2]  │  content (active tab)                   │
 *   │  [nav3]  │                                          │
 *   │  ...     │                                          │
 *   │          │                                          │
 *   │  200px   │  flex: 1 (scrolls)                       │
 *   └──────────┴─────────────────────────────────────────┘
 *
 * The nav column holds the tab list. The right column holds
 * the active tab's content. The header is sticky so the
 * title + close button stay visible as the user scrolls
 * through long content (e.g. a list of 50 archived
 * sessions).
 *
 * Usage:
 *   <AppSettingsLayout
 *     v-model:current="currentTab"
 *     :tabs="[
 *       { name: 'providers', label: 'LLM 提供商', icon: Cpu },
 *       { name: 'styles',    label: '风格',        icon: Palette },
 *       ...
 *     ]"
 *     title="应用设置"
 *     :show="showSettings"
 *     @close="showSettings = false"
 *   >
 *     <!-- One conditional per tab (v-if) -->
 *     <div v-if="currentTab === 'providers'">...</div>
 *     <div v-else-if="currentTab === 'styles'">...</div>
 *     ...
 *   </AppSettingsLayout>
 */
import { computed } from 'vue'
import { NScrollbar } from 'naive-ui'
import { X as XIcon } from './icons'

interface TabDef {
  /** Unique id used as v-model:current value. */
  name: string
  /** Human-readable label shown in the nav. */
  label: string
  /** Lucide component reference. */
  icon?: any
  /** Optional short description shown under the label on
   * hover (tooltip). Most tabs skip this. */
  description?: string
}

const props = withDefaults(defineProps<{
  /** v-model:show — controls modal visibility. */
  show: boolean
  /** v-model:current — the active tab name. */
  current?: string
  /** Tabs metadata: name, label, icon, description. */
  tabs: TabDef[]
  /** Modal title shown in the sticky header. */
  title: string
  /** Optional subtitle / status text shown under the title. */
  subtitle?: string
  /** Width of the modal. Default 1080 — wide enough for the
   * provider / KB tabs' two-column layouts but not full
   * screen. */
  width?: number
  /** Optional save/apply button label. When set, the
   * header shows a save button next to close. Most tabs
   * don't need this (their forms auto-save on change). */
  saveLabel?: string
}>(), {
  current: '',
  width: 1080,
})

const emit = defineEmits<{
  (e: 'update:show', v: boolean): void
  (e: 'update:current', v: string): void
  (e: 'close'): void
  (e: 'save'): void
}>()

function pickTab(name: string) {
  emit('update:current', name)
}

function close() {
  emit('update:show', false)
  emit('close')
}

// Tab list is rendered in declaration order, so callers
// control visual order by ordering the tabs array. The
// active tab is identified by `current === name`; we
// compute a per-tab "is active" once for the template
// to avoid a triple-equality check per row.
const activeName = computed(() => props.current)
function isActive(name: string) {
  return activeName.value === name
}
</script>

<template>
  <Teleport to="body">
    <Transition name="settings-fade">
      <div
        v-if="show"
        class="settings-mask"
        role="dialog"
        aria-modal="true"
        :aria-label="title"
        @click.self="close"
      >
        <div
          class="settings-window"
          :style="{ width: width + 'px' }"
        >
          <!-- Sticky header: title + (optional save) + close -->
          <div class="settings-header">
            <div class="settings-header-text">
              <div class="settings-title">{{ title }}</div>
              <div v-if="subtitle" class="settings-subtitle">{{ subtitle }}</div>
            </div>
            <div class="settings-header-actions">
              <button
                v-if="saveLabel"
                type="button"
                class="settings-save-btn"
                @click="emit('save')"
              >
                {{ saveLabel }}
              </button>
              <button
                type="button"
                class="settings-close-btn"
                :aria-label="'关闭'"
                :title="'关闭 (Esc)'"
                @click="close"
              >
                <XIcon :size="18" />
              </button>
            </div>
          </div>

          <!-- Body: nav column + content column -->
          <div class="settings-body">
            <nav class="settings-nav" aria-label="设置分类">
              <button
                v-for="t in tabs"
                :key="t.name"
                type="button"
                class="settings-nav-item"
                :class="{ 'settings-nav-item--active': isActive(t.name) }"
                :aria-current="isActive(t.name) ? 'page' : undefined"
                :title="t.description || t.label"
                @click="pickTab(t.name)"
              >
                <component v-if="t.icon" :is="t.icon" :size="16" class="settings-nav-icon" />
                <span class="settings-nav-label">{{ t.label }}</span>
              </button>
            </nav>

            <div class="settings-content">
              <slot />
            </div>
          </div>
        </div>
      </div>
    </Transition>
  </Teleport>
</template>

<style scoped>
/* --- Backdrop ------------------------------------------------------- */
.settings-mask {
  position: fixed;
  inset: 0;
  z-index: 2000;
  background: var(--surface-overlay);
  backdrop-filter: blur(6px);
  display: flex;
  align-items: center;
  justify-content: center;
  padding: 24px;
}

/* --- Window --------------------------------------------------------- */
.settings-window {
  display: flex;
  flex-direction: column;
  height: min(88vh, 820px);
  max-height: 820px;
  background: var(--surface-1);
  border: 1px solid var(--border-default);
  border-radius: var(--radius-xl);
  box-shadow: var(--shadow-lg);
  overflow: hidden;
}

/* --- Header --------------------------------------------------------- */
.settings-header {
  height: 64px;
  padding: 0 20px;
  display: flex;
  align-items: center;
  justify-content: space-between;
  border-bottom: 1px solid var(--border-subtle);
  background: var(--surface-1);
  flex-shrink: 0;
  z-index: 2;
}
.settings-header-text {
  display: flex;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.settings-title {
  font-size: 17px;
  font-weight: 600;
  color: var(--text-primary);
  letter-spacing: -0.01em;
  line-height: 1.2;
}
.settings-subtitle {
  font-size: 12px;
  color: var(--text-tertiary);
}
.settings-header-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-shrink: 0;
}
.settings-save-btn {
  height: 32px;
  padding: 0 14px;
  background: var(--brand-500);
  color: #ffffff;
  border: 1px solid var(--brand-500);
  border-radius: var(--radius-md);
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
  transition: background var(--dur-fast) var(--ease-out);
}
.settings-save-btn:hover { background: var(--brand-600); border-color: var(--brand-600); }
.settings-close-btn {
  width: 32px;
  height: 32px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  background: transparent;
  border: none;
  color: var(--text-tertiary);
  cursor: pointer;
  border-radius: var(--radius-md);
  transition: background var(--dur-fast) var(--ease-out), color var(--dur-fast) var(--ease-out);
}
.settings-close-btn:hover {
  background: var(--surface-3);
  color: var(--text-primary);
}

/* --- Body (nav + content) ------------------------------------------- */
.settings-body {
  flex: 1;
  display: flex;
  min-height: 0;
}

/* Nav column — 200px wide, scrollable, sticky rail feel */
.settings-nav {
  width: 200px;
  flex-shrink: 0;
  padding: 12px 8px;
  background: var(--surface-1);
  border-right: 1px solid var(--border-subtle);
  display: flex;
  flex-direction: column;
  gap: 2px;
  overflow-y: auto;
  /* Use subtle inner shadow when content scrolls so the
   * nav feels anchored. */
  box-shadow: inset -1px 0 0 var(--border-subtle);
}
.settings-nav-item {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 12px;
  background: transparent;
  border: 1px solid transparent;
  border-radius: var(--radius-md);
  color: var(--text-secondary);
  font-size: 13px;
  cursor: pointer;
  text-align: left;
  transition: background var(--dur-fast) var(--ease-out),
              color var(--dur-fast) var(--ease-out);
  width: 100%;
  font-family: inherit;
}
.settings-nav-item:hover {
  background: var(--surface-2);
  color: var(--text-primary);
}
.settings-nav-item--active {
  background: var(--brand-50);
  color: var(--brand-600);
  font-weight: 500;
}
.settings-nav-item--active:hover {
  background: var(--brand-100);
  color: var(--brand-700);
}
.settings-nav-icon {
  flex-shrink: 0;
  color: inherit;
  opacity: 0.9;
}
.settings-nav-label {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

/* Content column — fills remaining space, scrolls */
.settings-content {
  flex: 1;
  min-width: 0;
  overflow: auto;
  padding: 0;
  background: var(--surface-0);
}

/* --- Modal open/close transition ---------------------------------- */
.settings-fade-enter-active,
.settings-fade-leave-active {
  transition: opacity var(--dur-base) var(--ease-out);
}
.settings-fade-enter-active .settings-window,
.settings-fade-leave-active .settings-window {
  transition: transform var(--dur-base) var(--ease-out),
              opacity var(--dur-base) var(--ease-out);
}
.settings-fade-enter-from,
.settings-fade-leave-to {
  opacity: 0;
}
.settings-fade-enter-from .settings-window,
.settings-fade-leave-to .settings-window {
  transform: translateY(8px) scale(0.98);
  opacity: 0;
}
</style>
