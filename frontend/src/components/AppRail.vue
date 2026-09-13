<script setup lang="ts">
/**
 * AppRail — always-visible 52px app chrome.
 *
 * Global / Project / Settings live here as app-level navigation.
 * Project list itself is no longer a permanent column; "项目" asks
 * the parent to focus the ProjectSwitcher inside the session sidebar.
 * Contextual project actions (open folder / terminal) stay in the TopBar
 * so this rail doesn't duplicate the current-session shortcut area.
 */
import { computed } from 'vue'
import { state, setActiveProject } from '../stores/chat'
import BrandLogo from './BrandLogo.vue'
import {
  Globe, Folder, Settings, Plus, Info,
} from './icons'

const emit = defineEmits<{
  (e: 'about'): void
  (e: 'focus-projects'): void
  (e: 'open-settings'): void
  (e: 'add-project'): void
}>()

const isGlobal = computed(() => !state.activeProjectPath)
const hasProject = computed(() => !!state.activeProjectPath)

function goGlobal() {
  void setActiveProject('')
}
</script>

<template>
  <nav class="app-rail" aria-label="应用导航">
    <button
      type="button"
      class="rail-btn rail-brand"
      title="关于 P-Chat"
      aria-label="关于 P-Chat"
      @click="emit('about')"
    >
      <BrandLogo :size="26" />
    </button>

    <div class="rail-nav" role="group" aria-label="主导航">
      <button
        type="button"
        class="rail-btn"
        :class="{ 'rail-btn--active': isGlobal }"
        title="全局"
        aria-label="全局会话"
        :aria-current="isGlobal ? 'page' : undefined"
        @click="goGlobal"
      >
        <Globe :size="18" />
        <span class="rail-label">全局</span>
      </button>
      <button
        type="button"
        class="rail-btn"
        :class="{ 'rail-btn--active': hasProject }"
        title="项目"
        aria-label="项目"
        :aria-current="hasProject ? 'page' : undefined"
        @click="emit('focus-projects')"
      >
        <Folder :size="18" />
        <span class="rail-label">项目</span>
      </button>
      <button
        type="button"
        class="rail-btn"
        title="设置"
        aria-label="设置"
        @click="emit('open-settings')"
      >
        <Settings :size="18" />
        <span class="rail-label">设置</span>
      </button>
    </div>

    <div class="rail-footer">
      <button
        type="button"
        class="rail-btn"
        title="新建项目"
        aria-label="新建项目"
        @click="emit('add-project')"
      >
        <Plus :size="18" />
      </button>
      <button
        type="button"
        class="rail-btn"
        title="关于"
        aria-label="关于"
        @click="emit('about')"
      >
        <Info :size="18" />
      </button>
    </div>
  </nav>
</template>

<style scoped>
.app-rail {
  width: var(--rail-width);
  flex: 0 0 var(--rail-width);
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-3) var(--space-1);
  background: var(--surface-0);
  border-right: 1px solid var(--border-subtle);
  z-index: 2;
}
.rail-nav {
  flex: 1;
  min-height: 0;
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: var(--space-1);
  overflow-y: auto;
  width: 100%;
}
.rail-footer {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: var(--space-1);
  padding-top: var(--space-2);
  border-top: 1px solid var(--border-subtle);
  width: 100%;
}
.rail-btn {
  width: 44px;
  min-height: calc(var(--control-height) + var(--space-4));
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex-direction: column;
  gap: 2px;
  position: relative;
  border: none;
  border-radius: var(--radius-md);
  background: transparent;
  color: var(--text-secondary);
  cursor: pointer;
  transition: var(--transition-colors);
}
.rail-label {
  max-width: 100%;
  overflow: hidden;
  color: inherit;
  font-size: 10px;
  line-height: 1;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.rail-footer .rail-btn,
.rail-brand {
  width: 36px;
  min-height: 36px;
  flex-direction: row;
  gap: 0;
}
.rail-btn:hover:not(:disabled) {
  background: var(--surface-2);
  color: var(--text-primary);
}
.rail-btn:disabled {
  opacity: 0.35;
  cursor: not-allowed;
}
.rail-btn--active {
  background: var(--surface-2);
  color: var(--text-primary);
}
.rail-brand {
  color: var(--text-primary);
  margin-bottom: var(--space-1);
}
</style>
