<script setup lang="ts">
// Top-level app shell. Owns the Naive UI theme — we always
// layer a `themeOverrides.common.primaryColor` on top of
// darkTheme/lightTheme so the brand accent drives every
// "primary" callout, the same way --accent drives the
// non-Naive components.
//
// Theme choice ("dark" / "light") is persisted in
// localStorage so the user's preference survives reloads.
// First run falls back to the OS preference (useOsTheme).

import { computed, nextTick, onMounted, onUnmounted, ref, watch, type CSSProperties } from 'vue'
import {
  NConfigProvider, NMessageProvider, NDialogProvider, NNotificationProvider,
  darkTheme, lightTheme, useOsTheme,
  type GlobalTheme, type GlobalThemeOverrides,
} from 'naive-ui'
import SessionSidebar from './components/SessionSidebar.vue'
import AppRail from './components/AppRail.vue'
import InspectorPanel from './components/InspectorPanel.vue'
import ChatWindow from './components/ChatWindow.vue'
import TitleBar from './components/TitleBar.vue'
import TopBar from './components/TopBar.vue'
import AppSettingsModal from './components/AppSettingsModal.vue'
import ImageLightbox from './components/ImageLightbox.vue'
import PlanReviewModal from './components/PlanReviewModal.vue'
import ToolConfirmModal from './components/ToolConfirmModal.vue'
import CloseConfirmModal from './components/CloseConfirmModal.vue'
import DownloadDock from './components/DownloadDock.vue'
import { loadSessions, loadProviders, loadProjects } from './stores/chat'
import { setupTrayEventListeners } from './utils/trayEvents'

const showAppSettings = ref(false)
let cleanupTrayEvents: (() => void) | null = null

// Sidebar collapse state. Persisted in localStorage so the user's
// preference survives reloads. Toggled by the collapse button in
// the TopBar. Default 'false' (sidebar visible) — matches the
// pre-PR-3 layout so existing users see no change on upgrade.
const SIDEBAR_COLLAPSED_KEY = 'pchat-sidebar-collapsed'
const SIDEBAR_WIDTH_KEY = 'pchat-sidebar-width'
const SIDEBAR_MIN_WIDTH = 220
const SIDEBAR_MAX_WIDTH = 420
const sidebarCollapsed = ref(false)
const sidebarWidthPx = ref<number | null>(null)
const sidebarDragging = ref(false)
const appBodyRef = ref<HTMLElement | null>(null)
const sidebarDragStartX = ref(0)
const sidebarDragStartWidth = ref(0)

// Right inspector. Persisted separately so collapsing the
// session list doesn't also hide the context panel.
const INSPECTOR_OPEN_KEY = 'pchat-inspector-open'
const inspectorOpen = ref(true)

const sidebarRef = ref<InstanceType<typeof SessionSidebar> | null>(null)

// 'dark' | 'light' — what the user picked. Persisted.
const THEME_KEY = 'pchat-theme'
const themeName = ref<'dark' | 'light'>('dark')

const appStyle = computed<CSSProperties>(() => {
  if (sidebarWidthPx.value == null) return {}
  return { '--sidebar-user-width': `${sidebarWidthPx.value}px` } as CSSProperties
})

const sidebarResizeValue = computed(() =>
  Math.round(sidebarWidthPx.value ?? defaultSidebarWidth()),
)

const sidebarResizeMax = computed(() => maxSidebarWidth())

function railWidthPx(): number {
  if (typeof window === 'undefined') return 52
  const value = getComputedStyle(document.documentElement)
    .getPropertyValue('--rail-width')
    .trim()
  const parsed = Number.parseFloat(value)
  return Number.isFinite(parsed) ? parsed : 52
}

function defaultSidebarWidth(): number {
  if (typeof window === 'undefined') return 280
  const proportional = (window.innerWidth - railWidthPx()) / 5
  return clampSidebarWidth(proportional)
}

function maxSidebarWidth(): number {
  const available = appBodyRef.value?.clientWidth ?? (typeof window === 'undefined' ? 1280 : window.innerWidth)
  const proportionalMax = Math.round(available * 0.34)
  return Math.min(SIDEBAR_MAX_WIDTH, Math.max(SIDEBAR_MIN_WIDTH, proportionalMax))
}

function clampSidebarWidth(width: number): number {
  const max = maxSidebarWidth()
  return Math.round(Math.min(max, Math.max(SIDEBAR_MIN_WIDTH, width)))
}

function loadSidebarWidth() {
  try {
    const raw = localStorage.getItem(SIDEBAR_WIDTH_KEY)
    if (!raw) return
    const parsed = Number.parseFloat(raw)
    if (Number.isFinite(parsed)) sidebarWidthPx.value = clampSidebarWidth(parsed)
  } catch { /* ignore */ }
}

function persistSidebarWidth() {
  try {
    if (sidebarWidthPx.value == null) localStorage.removeItem(SIDEBAR_WIDTH_KEY)
    else localStorage.setItem(SIDEBAR_WIDTH_KEY, String(sidebarWidthPx.value))
  } catch { /* ignore */ }
}

function currentSidebarWidth(): number {
  const el = sidebarRef.value?.$el as HTMLElement | undefined
  const width = el?.getBoundingClientRect().width
  if (width && width > 0) return width
  return sidebarWidthPx.value ?? defaultSidebarWidth()
}

function removeSidebarResizeListeners() {
  if (typeof window !== 'undefined') {
    window.removeEventListener('pointermove', onSidebarResize)
    window.removeEventListener('pointerup', stopSidebarResize)
    window.removeEventListener('pointercancel', stopSidebarResize)
  }
  if (typeof document !== 'undefined') {
    document.body.classList.remove('is-resizing-sidebar')
  }
}

function startSidebarResize(event: PointerEvent) {
  if (sidebarCollapsed.value) return
  event.preventDefault()
  sidebarDragging.value = true
  sidebarDragStartX.value = event.clientX
  sidebarDragStartWidth.value = currentSidebarWidth()
  const target = event.currentTarget as HTMLElement | null
  target?.setPointerCapture?.(event.pointerId)
  document.body.classList.add('is-resizing-sidebar')
  window.addEventListener('pointermove', onSidebarResize)
  window.addEventListener('pointerup', stopSidebarResize)
  window.addEventListener('pointercancel', stopSidebarResize)
}

function onSidebarResize(event: PointerEvent) {
  if (!sidebarDragging.value) return
  const delta = event.clientX - sidebarDragStartX.value
  sidebarWidthPx.value = clampSidebarWidth(sidebarDragStartWidth.value + delta)
}

function stopSidebarResize() {
  if (sidebarDragging.value) {
    sidebarDragging.value = false
    persistSidebarWidth()
  }
  removeSidebarResizeListeners()
}

function resetSidebarWidth() {
  sidebarWidthPx.value = null
  persistSidebarWidth()
}

function nudgeSidebarWidth(delta: number) {
  sidebarWidthPx.value = clampSidebarWidth((sidebarWidthPx.value ?? defaultSidebarWidth()) + delta)
  persistSidebarWidth()
}

function onSidebarResizeKeydown(event: KeyboardEvent) {
  if (event.key === 'ArrowLeft') {
    event.preventDefault()
    nudgeSidebarWidth(-16)
  } else if (event.key === 'ArrowRight') {
    event.preventDefault()
    nudgeSidebarWidth(16)
  } else if (event.key === 'Home') {
    event.preventDefault()
    sidebarWidthPx.value = SIDEBAR_MIN_WIDTH
    persistSidebarWidth()
  } else if (event.key === 'End') {
    event.preventDefault()
    sidebarWidthPx.value = sidebarResizeMax.value
    persistSidebarWidth()
  } else if (event.key === 'Enter') {
    event.preventDefault()
    resetSidebarWidth()
  }
}

function keepSidebarWidthInBounds() {
  if (sidebarWidthPx.value == null) return
  sidebarWidthPx.value = clampSidebarWidth(sidebarWidthPx.value)
}

// Single brand color used by both Naive UI's primaryColor
// and the rest of the app's --brand-500. Reading the CSS var
// (rather than hard-coding) keeps the two in lock-step. The
// legacy --accent is an alias of --brand-500, so reading either
// yields the same value; --brand-500 is the canonical source.
function readBrand(): string {
  if (typeof window === 'undefined') return '#4a4dff'
  const v = getComputedStyle(document.documentElement).getPropertyValue('--brand-500').trim()
  return v || '#4a4dff'
}

// Font family used by Naive UI components. Mirrors the
// --font-sans token so Naive-rendered text uses the same
// Inter stack as the hand-rolled components.
function readFontFamily(): string {
  if (typeof window === 'undefined') return 'system-ui, sans-serif'
  const v = getComputedStyle(document.documentElement).getPropertyValue('--font-sans').trim()
  return v || 'system-ui, sans-serif'
}

// Motion curves used by Naive UI's built-in transitions
// (dropdowns, drawers, tooltips, NModal). Read from the
// same CSS tokens as the hand-rolled Vue <Transition>s so
// the two families settle on the same timing language.
function readMotion(): { easeOut: string; easeIn: string; easeInOut: string } {
  const fallback = {
    easeOut: 'cubic-bezier(0.16, 1, 0.3, 1)',
    easeIn: 'cubic-bezier(0.4, 0, 1, 1)',
    easeInOut: 'cubic-bezier(0.4, 0, 0.2, 1)',
  }
  if (typeof window === 'undefined') return fallback
  const cs = getComputedStyle(document.documentElement)
  return {
    easeOut: cs.getPropertyValue('--ease-out').trim() || fallback.easeOut,
    easeIn: cs.getPropertyValue('--ease-in').trim() || fallback.easeIn,
    easeInOut: cs.getPropertyValue('--ease-in-out').trim() || fallback.easeInOut,
  }
}

// Apply the chosen theme to <html data-theme=…> so the CSS
// variables in style.css cascade into all components.
function applyDocumentTheme(name: 'dark' | 'light') {
  if (typeof document === 'undefined') return
  document.documentElement.setAttribute('data-theme', name)
}

// themeOverrides is rebuilt when themeName flips so the
// resolved primaryColor always matches the current
// --brand-500 (which itself depends on data-theme). The
// explicit dependency on themeName.value is what makes
// Vue recompute after a toggle — readBrand() reads the
// live CSS var, so it doesn't register as a reactive
// dep on its own.
const themeOverrides = computed<GlobalThemeOverrides>(() => {
  // Touch themeName so this computed re-runs on toggle.
  const _t = themeName.value
  void _t
  const brand = readBrand()
  const motion = readMotion()
  return {
    common: {
      primaryColor: brand,
      primaryColorHover: brand,
      primaryColorPressed: brand,
      primaryColorSuppl: brand,
      fontFamily: readFontFamily(),
      cubicBezierEaseOut: motion.easeOut,
      cubicBezierEaseIn: motion.easeIn,
      cubicBezierEaseInOut: motion.easeInOut,
    },
    Select: {
      menuBoxShadow: 'var(--shadow-lg)',
      peers: {
        InternalSelection: {
          fontSizeTiny: '12px',
          fontSizeSmall: '12px',
          fontSizeMedium: '12.5px',
          heightTiny: '26px',
          heightSmall: '28px',
          heightMedium: '32px',
          borderRadius: 'var(--radius-sm)',
          fontWeight: '500',
          textColor: 'var(--text-secondary)',
          textColorDisabled: 'var(--text-quaternary)',
          placeholderColor: 'var(--text-tertiary)',
          placeholderColorDisabled: 'var(--text-quaternary)',
          color: 'var(--surface-2)',
          colorActive: 'var(--surface-3)',
          colorDisabled: 'var(--surface-2)',
          border: '1px solid var(--border-subtle)',
          borderHover: '1px solid var(--border-default)',
          borderActive: '1px solid var(--border-default)',
          borderFocus: '1px solid var(--brand-500)',
          boxShadowHover: 'none',
          boxShadowActive: '0 0 0 2px var(--brand-100)',
          boxShadowFocus: '0 0 0 2px var(--brand-100)',
          caretColor: 'var(--brand-500)',
          arrowColor: 'var(--text-tertiary)',
          arrowColorDisabled: 'var(--text-quaternary)',
          loadingColor: 'var(--brand-500)',
        },
        InternalSelectMenu: {
          borderRadius: 'var(--radius-md)',
          color: 'var(--surface-1)',
          optionFontSizeTiny: '12px',
          optionFontSizeSmall: '12.5px',
          optionFontSizeMedium: '12.5px',
          optionHeightTiny: '28px',
          optionHeightSmall: '32px',
          optionHeightMedium: '32px',
          optionTextColor: 'var(--text-secondary)',
          optionTextColorPressed: 'var(--brand-600)',
          optionTextColorDisabled: 'var(--text-quaternary)',
          optionTextColorActive: 'var(--brand-600)',
          optionCheckColor: 'var(--brand-500)',
          optionColorPending: 'var(--surface-3)',
          optionColorActive: 'var(--brand-50)',
          optionColorActivePending: 'var(--brand-100)',
        },
      },
    },
  }
})

const naiveTheme = computed<GlobalTheme>(() =>
  themeName.value === 'light' ? lightTheme : darkTheme,
)

// Persist + apply the data-theme attribute whenever the
// user flips the toggle.
watch(themeName, (n) => {
  applyDocumentTheme(n)
  try { localStorage.setItem(THEME_KEY, n) } catch { /* ignore */ }
})

// Persist sidebar collapse state whenever it changes.
watch(sidebarCollapsed, (v) => {
  try { localStorage.setItem(SIDEBAR_COLLAPSED_KEY, v ? '1' : '0') } catch { /* ignore */ }
})

watch(inspectorOpen, (v) => {
  try { localStorage.setItem(INSPECTOR_OPEN_KEY, v ? '1' : '0') } catch { /* ignore */ }
})

function toggleSidebar() {
  sidebarCollapsed.value = !sidebarCollapsed.value
}

function toggleInspector() {
  inspectorOpen.value = !inspectorOpen.value
}

async function focusProjects() {
  if (sidebarCollapsed.value) sidebarCollapsed.value = false
  // Wait a frame so the sidebar width transition has applied
  // before opening the popover (otherwise it anchors off-screen).
  await nextTick()
  sidebarRef.value?.openProjectSwitcher?.()
}

function openAddProject() {
  if (sidebarCollapsed.value) sidebarCollapsed.value = false
  sidebarRef.value?.openAddProject?.()
}

function openAbout() {
  sidebarRef.value?.openAbout?.()
}

onMounted(async () => {
  // Theme bootstrap: localStorage > OS preference > dark.
  let initial: 'dark' | 'light' | null = null
  try {
    const stored = localStorage.getItem(THEME_KEY)
    if (stored === 'dark' || stored === 'light') initial = stored
  } catch { /* localStorage may be disabled */ }
  if (!initial) {
    const os = useOsTheme()
    initial = os.value === 'light' ? 'light' : 'dark'
  }
  themeName.value = initial
  applyDocumentTheme(initial)

  // Sidebar collapse bootstrap: localStorage > default visible.
  try {
    const stored = localStorage.getItem(SIDEBAR_COLLAPSED_KEY)
    if (stored === '1' || stored === '0') {
      sidebarCollapsed.value = stored === '1'
    }
  } catch { /* ignore */ }
  loadSidebarWidth()

  // Inspector bootstrap: localStorage > default open.
  try {
    const stored = localStorage.getItem(INSPECTOR_OPEN_KEY)
    if (stored === '1' || stored === '0') {
      inspectorOpen.value = stored === '1'
    }
  } catch { /* ignore */ }

  // Expose a global close handle for AppSettingsModal so it can
  // dismiss itself without prop-drilling.
  ;(window as any).closeAppSettings = () => { showAppSettings.value = false }
  // Expose an open handle too, in case something other than the
  // sidebar needs it.
  ;(window as any).openAppSettings = () => { showAppSettings.value = true }
  try {
    // Projects must load before sessions so the store can restore
    // the last active project, then pick that project's last session.
    await Promise.all([loadProviders(), loadProjects()])
    await loadSessions()
  } catch (e) {
    console.error('init failed', e)
  }
  cleanupTrayEvents = await setupTrayEventListeners()
  window.addEventListener('resize', keepSidebarWidthInBounds)
})

onUnmounted(() => {
  removeSidebarResizeListeners()
  if (typeof window !== 'undefined') {
    window.removeEventListener('resize', keepSidebarWidthInBounds)
  }
  if (cleanupTrayEvents) {
    cleanupTrayEvents()
    cleanupTrayEvents = null
  }
})
</script>

<template>
  <NConfigProvider :theme="naiveTheme" :theme-overrides="themeOverrides">
    <NMessageProvider>
      <NDialogProvider>
        <NNotificationProvider>
          <div
            class="app"
            :class="{
              'app--sidebar-collapsed': sidebarCollapsed,
              'app--resizing-sidebar': sidebarDragging,
            }"
            :style="appStyle"
          >
            <TitleBar />
            <div class="app-body" ref="appBodyRef">
              <AppRail
                @about="openAbout"
                @focus-projects="focusProjects"
                @open-settings="showAppSettings = true"
                @add-project="openAddProject"
              />
              <SessionSidebar
                ref="sidebarRef"
                :class="{ 'sidebar-collapsed': sidebarCollapsed }"
                @open-settings="showAppSettings = true"
                v-model:theme-name="themeName"
              />
              <button
                v-if="!sidebarCollapsed"
                type="button"
                class="sidebar-resize-handle"
                :class="{ 'sidebar-resize-handle--dragging': sidebarDragging }"
                role="separator"
                aria-orientation="vertical"
                :aria-valuemin="SIDEBAR_MIN_WIDTH"
                :aria-valuemax="sidebarResizeMax"
                :aria-valuenow="sidebarResizeValue"
                aria-label="调整聊天记录宽度"
                title="拖动调整聊天记录宽度，双击恢复 1:3:1"
                @pointerdown="startSidebarResize"
                @dblclick="resetSidebarWidth"
                @keydown="onSidebarResizeKeydown"
              />
              <div class="main-column">
                <TopBar
                  :collapsed="sidebarCollapsed"
                  :inspector-open="inspectorOpen"
                  @toggle-sidebar="toggleSidebar"
                  @toggle-inspector="toggleInspector"
                />
                <div class="workspace-row">
                  <ChatWindow />
                  <InspectorPanel
                    :open="inspectorOpen"
                    @close="inspectorOpen = false"
                  />
                </div>
              </div>
            </div>
            <ImageLightbox />
            <AppSettingsModal
              v-if="showAppSettings"
              v-model:show="showAppSettings"
            />
            <ToolConfirmModal />
            <PlanReviewModal />
            <CloseConfirmModal />
            <DownloadDock />
            </div>
        </NNotificationProvider>
      </NDialogProvider>
    </NMessageProvider>
  </NConfigProvider>
</template>

<style scoped>
.app {
  display: flex;
  flex-direction: column;
  height: 100vh;
  width: 100vw;
  background: var(--surface-0);
  min-width: 0;
  overflow: hidden;
}
.app-body {
  display: flex;
  flex: 1;
  min-height: 0;
  min-width: 0;
}
.main-column {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  background: var(--surface-0);
}
.workspace-row {
  flex: 1;
  min-height: 0;
  min-width: 0;
  display: flex;
}

.sidebar-resize-handle {
  position: relative;
  z-index: 5;
  flex: 0 0 var(--space-2);
  width: var(--space-2);
  min-width: var(--space-2);
  margin: 0 calc(-1 * var(--space-1));
  padding: 0;
  border: 0;
  background: transparent;
  cursor: col-resize;
  -webkit-app-region: no-drag;
}
.sidebar-resize-handle::before {
  content: '';
  position: absolute;
  top: var(--space-3);
  bottom: var(--space-3);
  left: 50%;
  width: 1px;
  border-radius: var(--radius-pill);
  background: var(--border-subtle);
  transform: translateX(-50%);
  transition:
    width var(--dur-fast) var(--ease-out),
    background var(--dur-fast) var(--ease-out),
    box-shadow var(--dur-fast) var(--ease-out);
}
.sidebar-resize-handle:hover::before,
.sidebar-resize-handle:focus-visible::before,
.sidebar-resize-handle--dragging::before,
.app--resizing-sidebar .sidebar-resize-handle::before {
  width: 2px;
  background: var(--brand-500);
  box-shadow: 0 0 0 2px var(--brand-50);
}
.sidebar-resize-handle:focus-visible {
  outline: none;
}
.app--resizing-sidebar :deep(.sidebar) {
  transition: none;
}
:global(body.is-resizing-sidebar) {
  cursor: col-resize;
  user-select: none;
}
:global(body.is-resizing-sidebar *) {
  cursor: col-resize !important;
}

/* Sidebar collapse: SessionSidebar animates its own width
 * (var(--sidebar-width) → 0) via the `.sidebar-collapsed` class
 * on its root. AppRail stays visible. The main column flex-grows
 * into the freed space on the same --dur-slow / --ease-in-out
 * curve, so the chat canvas and the sidebar move together. */
</style>
