<script setup lang="ts">
import { computed, ref } from 'vue'
import { NButton, NInput, NScrollbar, NSpace, NSelect, NModal, NCard, useMessage } from 'naive-ui'
import {
  state, createSession, deleteSessionById, renameSession, switchSession,
  loadProjects, setActiveProject,
} from '../stores/chat'
import * as api from '../api/client'
import type { SelectOption } from 'naive-ui'

const emit = defineEmits<{ (e: 'open-settings'): void }>()

const themeName = defineModel<'dark' | 'light'>('themeName', { default: 'dark' })

const message = useMessage()
const showAddProject = ref(false)
const newProjectName = ref('')
const newProjectPath = ref('')

const sortedSessions = computed(() =>
  [...state.sessions].sort((a, b) => b.updated_at - a.updated_at),
)

const projectOptions = computed<SelectOption[]>(() => [
  { label: '🌐 全局', value: '' },
  ...state.projects.map(p => ({ label: `📁 ${p.name}`, value: p.path })),
])

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

async function onProjectChange(path: string) {
  await setActiveProject(path)
}

async function onAddProject() {
  if (!newProjectName.value.trim() || !newProjectPath.value.trim()) return
  try {
    await api.addProject(newProjectName.value.trim(), newProjectPath.value.trim())
    await loadProjects()
    message.success('项目已添加')
    showAddProject.value = false
    newProjectName.value = ''
    newProjectPath.value = ''
  } catch (e: any) {
    message.error(e.message || '添加失败')
  }
}

async function pickDirectory() {
  try {
    const { path } = await api.pickFolder()
    if (path) {
      newProjectPath.value = path
    }
  } catch (e: any) {
    message.error(e.message || '选取目录失败')
  }
}

async function onRemoveProject(path: string) {
  try {
    await api.removeProject(path)
    await loadProjects()
    if (state.activeProjectPath === path) {
      await setActiveProject('')
    }
    message.info('项目已移除')
  } catch (e: any) {
    message.error(e.message || '移除失败')
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

function toggleTheme() {
  themeName.value = themeName.value === 'dark' ? 'light' : 'dark'
}
</script>

<template>
  <aside class="sidebar">
    <div class="sidebar-header">
      <div class="logo">💬 P-Chat</div>
      <NSpace size="small">
        <NButton size="small" type="primary" @click="onNew" title="新建会话">+ 新建</NButton>
        <NButton size="small" quaternary @click="toggleTheme" :title="themeName === 'dark' ? '切换到浅色主题' : '切换到深色主题'">
          {{ themeName === 'dark' ? '🌙' : '☀' }}
        </NButton>
        <NButton size="small" quaternary @click="openSettings" title="设置">⚙</NButton>
      </NSpace>
    </div>
    <div class="project-bar">
      <NSelect
        :value="state.activeProjectPath"
        :options="projectOptions"
        size="small"
        placeholder="选择项目"
        @update:value="onProjectChange"
      />
      <NButton size="tiny" quaternary @click="showAddProject = true" title="添加项目">+</NButton>
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

    <NModal v-model:show="showAddProject" preset="card" title="添加项目" style="width: 420px">
      <div class="add-project-form">
        <label>项目名称</label>
        <NInput v-model:value="newProjectName" placeholder="例如：我的项目" />
        <label style="margin-top: 12px">项目目录</label>
        <div class="path-row">
          <NInput v-model:value="newProjectPath" placeholder="例如：D:\projects\my-app" style="flex:1" />
          <NButton size="small" @click="pickDirectory" title="选择目录">浏览</NButton>
        </div>
        <div class="project-actions">
          <NButton size="small" @click="showAddProject = false">取消</NButton>
          <NButton size="small" type="primary" @click="onAddProject">添加</NButton>
        </div>
      </div>
    </NModal>
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
.project-bar {
  display: flex; align-items: center; gap: 4px;
  padding: 6px 10px;
  border-bottom: 1px solid var(--border);
}
.project-bar :deep(.n-base-select) { flex: 1; }
.add-project-form {
  padding: 16px; min-width: 320px;
}
.add-project-form label {
  display: block; font-size: 13px; margin-bottom: 4px; color: var(--text-2);
}
.project-actions {
  margin-top: 16px; display: flex; gap: 8px; justify-content: flex-end;
}
.path-row {
  display: flex; gap: 8px; align-items: center;
}
</style>
