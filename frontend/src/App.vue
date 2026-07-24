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

import { computed, onMounted, onUnmounted, ref, watch } from 'vue'
import {
  NConfigProvider, NMessageProvider, NDialogProvider, NNotificationProvider,
  darkTheme, lightTheme, useOsTheme,
  type GlobalTheme,
} from 'naive-ui'
import SessionSidebar from './components/SessionSidebar.vue'
import ChatWindow from './components/ChatWindow.vue'
import TopBar from './components/TopBar.vue'
import AppSettingsModal from './components/AppSettingsModal.vue'
import ImageLightbox from './components/ImageLightbox.vue'
import PlanReviewModal from './components/PlanReviewModal.vue'
import QuestionModal from './components/QuestionModal.vue'
import ToolConfirmModal from './components/ToolConfirmModal.vue'
import CloseConfirmModal from './components/CloseConfirmModal.vue'
import { state, loadSessions, loadProviders, loadProjects, currentPendingQuestion } from './stores/chat'
import { setupTrayEventListeners } from './utils/trayEvents'

const showAppSettings = ref(false)
let cleanupTrayEvents: (() => void) | null = null

// Sidebar collapse state. Persisted in localStorage so the user's
// preference survives reloads. Toggled by the collapse button in
// the TopBar. Default 'false' (sidebar visible) — matches the
// pre-PR-3 layout so existing users see no change on upgrade.
const SIDEBAR_COLLAPSED_KEY = 'pchat-sidebar-collapsed'
const sidebarCollapsed = ref(false)

// 'dark' | 'light' — what the user picked. Persisted.
const THEME_KEY = 'pchat-theme'
const themeName = ref<'dark' | 'light'>('dark')

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
const themeOverrides = computed(() => {
  // Touch themeName so this computed re-runs on toggle.
  const _t = themeName.value
  void _t
  const brand = readBrand()
  return {
    common: {
      primaryColor: brand,
      primaryColorHover: brand,
      primaryColorPressed: brand,
      primaryColorSuppl: brand,
      fontFamily: readFontFamily(),
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

function toggleSidebar() {
  sidebarCollapsed.value = !sidebarCollapsed.value
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

  // Expose a global close handle for AppSettingsModal so it can
  // dismiss itself without prop-drilling.
  ;(window as any).closeAppSettings = () => { showAppSettings.value = false }
  // Expose an open handle too, in case something other than the
  // sidebar needs it.
  ;(window as any).openAppSettings = () => { showAppSettings.value = true }
  try {
    // Providers must load before the chat NSelect first
    // renders, so currentMeta() has something to fall back
    // to. Run in parallel with loadSessions.
    await Promise.all([loadSessions(), loadProviders(), loadProjects()])
  } catch (e) {
    console.error('init failed', e)
  }
  cleanupTrayEvents = await setupTrayEventListeners()
})

onUnmounted(() => {
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
          <div class="app" :class="{ 'app--sidebar-collapsed': sidebarCollapsed }">
            <SessionSidebar
              :class="{ 'sidebar-collapsed': sidebarCollapsed }"
              @open-settings="showAppSettings = true"
              v-model:theme-name="themeName"
            />
            <div class="main-column">
              <TopBar
                :collapsed="sidebarCollapsed"
                @toggle-sidebar="toggleSidebar"
              />
              <ChatWindow />
            </div>
            <ImageLightbox />
            <AppSettingsModal
              v-if="showAppSettings"
              v-model:show="showAppSettings"
            />
            <ToolConfirmModal />
            <PlanReviewModal />
            <QuestionModal />
            <CloseConfirmModal />
            </div>
        </NNotificationProvider>
      </NDialogProvider>
    </NMessageProvider>
  </NConfigProvider>
</template>

<style scoped>
.app {
  display: flex;
  height: 100vh;
  width: 100vw;
  background: var(--bg);
  min-width: 0;
}
.main-column {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  background: var(--bg);
}

/* Sidebar collapse: when the user toggles the sidebar closed,
 * the SessionSidebar hides itself (the component owns its own
 * collapsed visuals). The main column then expands to fill the
 * available space. The transition is a brief width fade; the
 * chat canvas reflows immediately so the user sees the new
 * layout without waiting for the sidebar's exit animation. */
.sidebar-collapsed {
  display: none;
}
</style>
