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

import { computed, onMounted, ref, watch } from 'vue'
import {
  NConfigProvider, NMessageProvider, NDialogProvider, NNotificationProvider,
  darkTheme, lightTheme, useOsTheme,
  type GlobalTheme,
} from 'naive-ui'
import SessionSidebar from './components/SessionSidebar.vue'
import ChatWindow from './components/ChatWindow.vue'
import AppSettingsModal from './components/AppSettingsModal.vue'
import ImageLightbox from './components/ImageLightbox.vue'
import { state, loadSessions, loadProviders } from './stores/chat'

const showAppSettings = ref(false)

// 'dark' | 'light' — what the user picked. Persisted.
const THEME_KEY = 'pchat-theme'
const themeName = ref<'dark' | 'light'>('dark')

// Single accent color used by both Naive UI's primaryColor
// and the rest of the app's --accent. Reading the CSS var
// (rather than hard-coding) keeps the two in lock-step.
function readAccent(): string {
  if (typeof window === 'undefined') return '#4a4dff'
  const v = getComputedStyle(document.documentElement).getPropertyValue('--accent').trim()
  return v || '#4a4dff'
}

// Apply the chosen theme to <html data-theme=…> so the CSS
// variables in style.css cascade into all components.
function applyDocumentTheme(name: 'dark' | 'light') {
  if (typeof document === 'undefined') return
  document.documentElement.setAttribute('data-theme', name)
}

// themeOverrides is rebuilt when themeName flips so the
// resolved primaryColor always matches the current
// --accent (which itself depends on data-theme). The
// explicit dependency on themeName.value is what makes
// Vue recompute after a toggle — readAccent() reads the
// live CSS var, so it doesn't register as a reactive
// dep on its own.
const themeOverrides = computed(() => {
  // Touch themeName so this computed re-runs on toggle.
  const _t = themeName.value
  void _t
  const accent = readAccent()
  return {
    common: {
      primaryColor: accent,
      primaryColorHover: accent,
      primaryColorPressed: accent,
      primaryColorSuppl: accent,
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
    await Promise.all([loadSessions(), loadProviders()])
  } catch (e) {
    console.error('init failed', e)
  }
})
</script>

<template>
  <NConfigProvider :theme="naiveTheme" :theme-overrides="themeOverrides">
    <NMessageProvider>
      <NDialogProvider>
        <NNotificationProvider>
          <div class="app">
            <SessionSidebar
              @open-settings="showAppSettings = true"
              v-model:theme-name="themeName"
            />
            <ChatWindow />
            <ImageLightbox />
            <AppSettingsModal v-if="showAppSettings" />
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
}
</style>
