<script setup lang="ts">
/**
 * DownloadDock — compact glass panel for recent desktop saves.
 * Replaces the unstyleable WebView2 downloads flyout.
 */
import { FolderOpen, X } from './icons'
import { revealInFolder } from '../utils/download'
import { useDownloadDock } from '../composables/useDownloadDock'

const { open, recent, dismiss, remove } = useDownloadDock()

function shortName(name: string): string {
  if (name.length <= 36) return name
  const extIdx = name.lastIndexOf('.')
  const ext = extIdx > 0 ? name.slice(extIdx) : ''
  const stem = extIdx > 0 ? name.slice(0, extIdx) : name
  const keep = Math.max(12, 36 - ext.length - 1)
  return `${stem.slice(0, keep)}…${ext}`
}

async function openItem(path: string) {
  await revealInFolder(path)
}
</script>

<template>
  <Transition name="dock-fade">
    <aside
      v-if="open && recent.length"
      class="download-dock"
      role="dialog"
      aria-label="下载"
    >
      <header class="download-dock-head">
        <span class="download-dock-title">下载</span>
        <button
          type="button"
          class="download-dock-icon-btn"
          title="关闭"
          aria-label="关闭"
          @click="dismiss"
        >
          <X :size="14" />
        </button>
      </header>
      <ul class="download-dock-list">
        <li v-for="item in recent" :key="item.id" class="download-dock-item">
          <div class="download-dock-meta">
            <span class="download-dock-name" :title="item.name">{{ shortName(item.name) }}</span>
            <button
              type="button"
              class="download-dock-link"
              @click="openItem(item.path)"
            >
              打开文件
            </button>
          </div>
          <button
            type="button"
            class="download-dock-icon-btn"
            title="在文件夹中显示"
            aria-label="在文件夹中显示"
            @click="openItem(item.path)"
          >
            <FolderOpen :size="14" />
          </button>
          <button
            type="button"
            class="download-dock-icon-btn"
            title="移除"
            aria-label="移除"
            @click="remove(item.id)"
          >
            <X :size="12" />
          </button>
        </li>
      </ul>
    </aside>
  </Transition>
</template>

<style scoped>
.download-dock {
  position: fixed;
  top: calc(var(--titlebar-height) + var(--space-3));
  right: var(--space-3);
  z-index: 80;
  width: min(320px, calc(100vw - var(--space-6)));
  padding: var(--space-2);
  background: var(--glass-bg);
  border: 1px solid var(--glass-border);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-lg);
  backdrop-filter: blur(var(--glass-blur));
  -webkit-backdrop-filter: blur(var(--glass-blur));
}
.download-dock-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-2);
  padding: var(--space-1) var(--space-2);
  margin-bottom: var(--space-1);
}
.download-dock-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--text-primary);
  letter-spacing: -0.01em;
}
.download-dock-list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
  max-height: 240px;
  overflow-y: auto;
}
.download-dock-item {
  display: flex;
  align-items: center;
  gap: var(--space-1);
  padding: var(--space-2);
  border-radius: var(--radius-md);
  background: var(--surface-1);
  border: 1px solid var(--border-subtle);
}
.download-dock-meta {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.download-dock-name {
  font-size: 12.5px;
  font-weight: 500;
  color: var(--text-primary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.download-dock-link {
  align-self: flex-start;
  padding: 0;
  border: 0;
  background: transparent;
  color: var(--brand-500);
  font-size: 12px;
  cursor: pointer;
}
.download-dock-link:hover {
  color: var(--brand-600);
  text-decoration: underline;
}
.download-dock-icon-btn {
  width: 28px;
  height: 28px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  flex-shrink: 0;
  border: 0;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--text-tertiary);
  cursor: pointer;
  transition: var(--transition-colors);
}
.download-dock-icon-btn:hover {
  background: var(--surface-3);
  color: var(--text-primary);
}
.dock-fade-enter-active,
.dock-fade-leave-active {
  transition: opacity var(--dur-base) var(--ease-out),
              transform var(--dur-base) var(--ease-out);
}
.dock-fade-enter-from,
.dock-fade-leave-to {
  opacity: 0;
  transform: translateY(-6px);
}
</style>
