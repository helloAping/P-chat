<script setup lang="ts">
/**
 * TitleBar — frameless window chrome.
 *
 * Drag region spans the bar; window controls are marked
 * no-drag. Close goes through RequestWindowClose so the
 * existing tray / confirm flow (OnBeforeClose) stays intact.
 */
import { onMounted, onUnmounted, ref } from 'vue'
import BrandLogo from './BrandLogo.vue'
import { Minus, Square, Copy, X } from './icons'

const maximised = ref(false)
const applicationTitle = ref('P-Chat')
let maximisedPoll: ReturnType<typeof setInterval> | null = null

async function withRuntime<T>(
  fn: (runtime: typeof import('../../wailsjs/runtime/runtime')) => T | Promise<T>,
): Promise<T | void> {
  try {
    const runtime = await import('../../wailsjs/runtime/runtime')
    return await fn(runtime)
  } catch {
    // Browser / vite preview has no Wails runtime.
  }
}

async function refreshMaximised() {
  await withRuntime(async (runtime) => {
    maximised.value = await runtime.WindowIsMaximised()
  })
}

async function minimise() {
  await withRuntime((runtime) => runtime.WindowMinimise())
}

async function toggleMaximise() {
  await withRuntime((runtime) => runtime.WindowToggleMaximise())
  // State updates after the native toggle settles.
  window.setTimeout(() => { void refreshMaximised() }, 50)
}

async function requestClose() {
  try {
    const app = await import('../../wailsjs/go/main/App')
    await app.RequestWindowClose()
  } catch {
    // Browser preview: no-op.
  }
}

async function refreshApplicationTitle() {
  const getApplicationTitle = (window as any).go?.main?.App?.GetApplicationTitle
  if (typeof getApplicationTitle !== 'function') return
  try {
    const title = await getApplicationTitle()
    if (typeof title === 'string' && title.trim()) applicationTitle.value = title
  } catch {
    // 浏览器预览没有桌面端绑定。
    // Browser preview has no desktop binding.
  }
}

onMounted(() => {
  void refreshApplicationTitle()
  void refreshMaximised()
  maximisedPoll = setInterval(() => { void refreshMaximised() }, 800)
})

onUnmounted(() => {
  if (maximisedPoll) clearInterval(maximisedPoll)
})
</script>

<template>
  <header class="titlebar" @dblclick="toggleMaximise">
    <div class="titlebar-brand">
      <BrandLogo :size="16" />
      <span class="titlebar-name">{{ applicationTitle }}</span>
    </div>
    <div class="titlebar-drag" aria-hidden="true" />
    <div class="titlebar-controls">
      <button type="button" class="titlebar-btn" title="最小化" aria-label="最小化" @click.stop="minimise">
        <Minus :size="14" />
      </button>
      <button
        type="button"
        class="titlebar-btn"
        :title="maximised ? '还原' : '最大化'"
        :aria-label="maximised ? '还原' : '最大化'"
        @click.stop="toggleMaximise"
      >
        <Copy v-if="maximised" :size="12" />
        <Square v-else :size="12" />
      </button>
      <button
        type="button"
        class="titlebar-btn titlebar-btn--close"
        title="关闭"
        aria-label="关闭"
        @click.stop="requestClose"
      >
        <X :size="14" />
      </button>
    </div>
  </header>
</template>

<style scoped>
.titlebar {
  display: flex;
  align-items: center;
  height: var(--titlebar-height);
  flex-shrink: 0;
  padding: 0 var(--space-2) 0 var(--space-3);
  background: var(--glass-bg);
  border-bottom: 1px solid var(--glass-border);
  backdrop-filter: blur(var(--glass-blur));
  -webkit-backdrop-filter: blur(var(--glass-blur));
  z-index: 50;
  /* Entire bar is a drag region; controls opt out below. */
  --wails-draggable: drag;
}
.titlebar-brand {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  flex-shrink: 0;
  --wails-draggable: no-drag;
}
.titlebar-name {
  font-size: 12px;
  font-weight: 600;
  color: var(--text-secondary);
  letter-spacing: 0.02em;
}
.titlebar-drag {
  flex: 1;
  align-self: stretch;
  min-width: var(--space-4);
}
.titlebar-controls {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  flex-shrink: 0;
  --wails-draggable: no-drag;
}
.titlebar-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 36px;
  height: 28px;
  padding: 0;
  border: 0;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--text-secondary);
  cursor: pointer;
  transition: var(--transition-colors);
}
.titlebar-btn:hover {
  background: var(--surface-3);
  color: var(--text-primary);
}
.titlebar-btn--close:hover {
  background: var(--error-50);
  color: var(--error-500);
}
</style>
