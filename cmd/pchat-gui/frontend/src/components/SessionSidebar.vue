<script setup lang="ts">
import { computed } from 'vue'
import { NButton, NInput, NScrollbar, NSpace, useMessage } from 'naive-ui'
import { state, createSession, deleteSessionById, renameSession, switchSession } from '../stores/chat'

const emit = defineEmits<{ (e: 'open-settings'): void }>()

const message = useMessage()

const sortedSessions = computed(() =>
  [...state.sessions].sort((a, b) => b.updated_at - a.updated_at),
)

async function onNew() {
  const id = await createSession()
  message.success('已创建新会话')
}

async function onDelete(id: string, e: Event) {
  e.stopPropagation()
  await deleteSessionById(id)
  message.info('已删除')
}

async function onRename(id: string) {
  const s = state.sessions.find(s => s.id === id)
  if (!s) return
  const title = window.prompt('新标题', s.title || '')
  if (title != null && title.trim()) {
    await renameSession(id, title.trim())
  }
}

function formatTime(t: number) {
  const d = new Date(t * 1000)
  const now = new Date()
  if (d.toDateString() === now.toDateString()) {
    return d.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })
  }
  return d.toLocaleDateString('zh-CN', { month: 'numeric', day: 'numeric' })
}

function openSettings() {
  emit('open-settings')
}
</script>

<template>
  <aside class="sidebar">
    <div class="sidebar-header">
      <div class="logo">💬 P-Chat</div>
      <NSpace size="small">
        <NButton size="small" type="primary" @click="onNew" title="新建会话">+ 新建</NButton>
        <NButton size="small" quaternary @click="openSettings" title="设置">⚙</NButton>
      </NSpace>
    </div>
    <NScrollbar style="flex: 1">
      <div class="session-list">
        <div
          v-for="s in sortedSessions"
          :key="s.id"
          class="session-item"
          :class="{ active: s.id === state.currentID }"
          @click="switchSession(s.id)"
        >
          <div class="title-row">
            <span class="title">{{ s.title || '(无标题)' }}</span>
            <span v-if="state.streaming[s.id]" class="streaming-dot" title="正在生成">●</span>
          </div>
          <div class="meta-row">
            <span class="time">{{ formatTime(s.updated_at) }}</span>
            <span class="actions">
              <button class="icon-btn" @click="onRename(s.id)" title="重命名">✎</button>
              <button class="icon-btn" @click="onDelete(s.id, $event)" title="删除">✕</button>
            </span>
          </div>
        </div>
      </div>
    </NScrollbar>
  </aside>
</template>

<style scoped>
.sidebar {
  width: 240px;
  background: var(--bg-2);
  border-right: 1px solid var(--border);
  display: flex;
  flex-direction: column;
  flex-shrink: 0;
}
.sidebar-header {
  padding: 12px;
  display: flex;
  justify-content: space-between;
  align-items: center;
  border-bottom: 1px solid var(--border);
}
.logo { font-weight: 600; font-size: 14px; }
.session-list { padding: 8px; }
.session-item {
  padding: 10px 12px;
  border-radius: 6px;
  cursor: pointer;
  margin-bottom: 4px;
  transition: background 0.1s;
}
.session-item:hover { background: var(--bg-3); }
.session-item.active { background: var(--bg-3); border-left: 2px solid var(--accent); }
.title-row { display: flex; justify-content: space-between; align-items: center; }
.title { font-size: 13px; font-weight: 500; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.streaming-dot { color: var(--accent); animation: pulse 1.2s infinite; }
@keyframes pulse { 0%,100% { opacity: 1; } 50% { opacity: 0.3; } }
.meta-row { display: flex; justify-content: space-between; align-items: center; margin-top: 4px; }
.time { font-size: 11px; color: var(--text-4); }
.actions { display: flex; gap: 2px; opacity: 0; transition: opacity 0.1s; }
.session-item:hover .actions { opacity: 1; }
.icon-btn {
  background: none; border: none; color: var(--text-3);
  cursor: pointer; padding: 2px 6px; border-radius: 3px; font-size: 12px;
}
.icon-btn:hover { background: var(--bg-4); color: var(--text); }
</style>
