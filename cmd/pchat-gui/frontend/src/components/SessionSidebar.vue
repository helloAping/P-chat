<script setup lang="ts">
import { computed, ref, onMounted } from 'vue'
import { NButton, NInput, NScrollbar, NSpace, NSelect, NModal, NCard, NTag, NPopconfirm, useMessage } from 'naive-ui'
import {
  state, createSession, deleteSessionById, renameSession, switchSession,
  loadProjects, setActiveProject, loadProviders,
} from '../stores/chat'
import * as api from '../api/client'
import type { SelectOption } from 'naive-ui'
import { checkUpdate } from '../api/update'
import type { UpdateInfo } from '../api/update'

const APP_VERSION = __APP_VERSION__
const GITHUB_REPO = __GITHUB_REPO__

const emit = defineEmits<{ (e: 'open-settings'): void }>()

const themeName = defineModel<'dark' | 'light'>('themeName', { default: 'dark' })

const message = useMessage()
const showAddProject = ref(false)
const newProjectName = ref('')
const newProjectPath = ref('')
const showConfirmDeleteProject = ref(false)
const showConfirmDeleteSession = ref(false)
const showAbout = ref(false)
const updateInfo = ref<UpdateInfo | null>(null)
const pendingDeleteSessionId = ref('')
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
  pendingDeleteSessionId.value = id
  showConfirmDeleteSession.value = true
}

async function confirmDeleteSession() {
  const id = pendingDeleteSessionId.value
  if (!id) return
  await deleteSessionById(id)
  showConfirmDeleteSession.value = false
  pendingDeleteSessionId.value = ''
  message.info('已归档')
}

async function confirmDeleteProject() {
  const path = state.activeProjectPath
  if (!path) return
  await onRemoveProject(path)
  showConfirmDeleteProject.value = false
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

async function onDeleteProvider(name: string) {
  try {
    await api.deleteProvider(name)
    message.success(`已删除 ${name}`)
    await loadProviders()
  } catch (e: any) {
    message.error(`删除失败: ${e.message}`)
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

function openAbout() {
  showAbout.value = true
  checkUpdate().then(info => {
    if (info) updateInfo.value = info
  })
}

onMounted(() => {
  checkUpdate().then(info => {
    if (info) updateInfo.value = info
  })
})
</script>

<template>
  <aside class="sidebar">
    <div class="sidebar-header">
      <div class="logo">💬 P-Chat</div>
      <NSpace size="small">
        <NButton size="small" quaternary @click="openAbout" title="关于我们" style="position:relative">
          ℹ
          <span v-if="updateInfo?.hasUpdate" class="update-dot"></span>
        </NButton>
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
      <NButton size="tiny" quaternary @click="showAddProject = true" title="添加项目目录" style="font-size:11px">+目录</NButton>
      <NButton v-if="state.activeProjectPath" size="tiny" quaternary @click="showConfirmDeleteProject = true" title="删除当前项目" style="color: var(--warn); font-size:11px">移除</NButton>
    </div>
    <div class="provider-bar" v-if="state.providers.length > 0">
      <div class="provider-bar-title">供应商</div>
      <div class="provider-list">
        <div
          v-for="p in state.providers"
          :key="p.name"
          class="provider-item"
        >
          <span class="provider-name">{{ p.name }}</span>
          <NTag size="tiny" :bordered="false">{{ p.protocol }}</NTag>
          <NPopconfirm
            v-if="!p.is_default"
            @positive-click="onDeleteProvider(p.name)"
            positive-text="删除"
            negative-text="取消"
          >
            <template #trigger>
              <button class="icon-btn" title="删除供应商">✕</button>
            </template>
            确定删除 "{{ p.name }}" 及其下所有模型？
          </NPopconfirm>
          <NTag v-else size="tiny" :bordered="false" type="default">默认</NTag>
        </div>
      </div>
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

    <div class="sidebar-footer">
      <NButton size="small" type="primary" block @click="onNew">+ 新建会话</NButton>
    </div>

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

    <!-- Confirmation: archive session -->
    <NModal v-model:show="showConfirmDeleteSession" preset="card" title="确认归档" style="width: 360px">
      <div class="confirm-body">
        <p>确定要归档此会话吗？归档后可在「设置 → 归档」中恢复。</p>
        <div class="confirm-actions">
          <NButton size="small" @click="showConfirmDeleteSession = false">取消</NButton>
          <NButton size="small" type="warning" @click="confirmDeleteSession">归档</NButton>
        </div>
      </div>
    </NModal>

    <!-- Confirmation: delete project -->
    <NModal v-model:show="showConfirmDeleteProject" preset="card" title="确认删除项目" style="width: 360px">
      <div class="confirm-body">
        <p>确定要删除当前项目吗？该项目的会话不会被删除，但将不再关联到此项目。</p>
        <div class="confirm-actions">
          <NButton size="small" @click="showConfirmDeleteProject = false">取消</NButton>
          <NButton size="small" type="error" @click="confirmDeleteProject">删除</NButton>
        </div>
      </div>
    </NModal>

    <!-- About modal -->
    <NModal v-model:show="showAbout" preset="card" title="关于 P-Chat" style="width: 380px">
      <div class="about-body">
        <p class="about-name">P-Chat</p>
        <p class="about-version">版本 v{{ APP_VERSION }}</p>
        <p class="about-desc">对话式 AI Agent · CLI / HTTP / 桌面端三端同源</p>
        <p class="about-desc">Go + Vue 3 + Vite + SQLite · Wails v2</p>
        <p class="about-desc">OpenAI / Anthropic 双协议 · ReAct 工具调用循环</p>

        <template v-if="updateInfo">
          <div v-if="updateInfo.hasUpdate" class="update-banner">
            <NTag type="warning" size="small">发现新版本</NTag>
            <p><strong>{{ updateInfo.latest }}</strong> (当前 {{ APP_VERSION }})</p>
            <p class="update-body" v-if="updateInfo.body">{{ updateInfo.body }}</p>
            <NButton size="small" type="primary" tag="a" :href="updateInfo.url" target="_blank">前往下载</NButton>
          </div>
          <p v-else class="update-ok">当前已是最新版本 ({{ APP_VERSION }})</p>
        </template>
        <p v-else class="update-ok">正在检查更新…</p>

        <div class="about-links">
          <a :href="'https://github.com/' + GITHUB_REPO" target="_blank">GitHub</a>
          <span class="sep">·</span>
          <a :href="'https://github.com/' + GITHUB_REPO + '/issues'" target="_blank">反馈问题</a>
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
.provider-bar {
  padding: 6px 10px;
  border-bottom: 1px solid var(--border);
}
.provider-bar-title {
  font-size: 11px;
  color: var(--text-4);
  text-transform: uppercase;
  margin-bottom: 4px;
}
.provider-list {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.provider-item {
  display: flex;
  align-items: center;
  gap: 4px;
  font-size: 12px;
  padding: 2px 4px;
  border-radius: 3px;
}
.provider-item:hover {
  background: var(--bg-3);
}
.provider-name {
  flex: 1;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
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
.confirm-body { padding: 8px 0; }
.confirm-body p { margin: 0 0 16px; font-size: 14px; color: var(--text-2); }
.confirm-actions { display: flex; gap: 8px; justify-content: flex-end; }
.sidebar-footer {
  padding: 8px 10px;
  border-top: 1px solid var(--border);
}
.update-dot {
  position: absolute;
  top: 2px;
  right: 2px;
  width: 7px;
  height: 7px;
  background: var(--warn, #f0a020);
  border-radius: 50%;
  animation: pulse 1.2s infinite;
}
.about-body { padding: 4px 0; }
.about-name { font-size: 18px; font-weight: 600; margin: 0 0 4px; }
.about-version { font-size: 13px; color: var(--text-3); margin: 0 0 12px; }
.about-desc { font-size: 13px; color: var(--text-2); margin: 0 0 4px; }
.update-banner {
  margin: 12px 0;
  padding: 12px;
  background: var(--warn-soft, rgba(240, 160, 32, 0.1));
  border: 1px solid var(--warn, #f0a020);
  border-radius: 6px;
}
.update-banner p { margin: 4px 0; font-size: 13px; }
.update-body { color: var(--text-3); font-size: 12px !important; max-height: 120px; overflow: auto; white-space: pre-wrap; }
.update-ok { font-size: 13px; color: var(--text-4); margin: 12px 0; }
.about-links { margin-top: 16px; padding-top: 12px; border-top: 1px solid var(--border-2); font-size: 13px; }
.about-links a { color: var(--accent); text-decoration: none; }
.about-links a:hover { text-decoration: underline; }
.about-links .sep { color: var(--text-4); margin: 0 6px; }
</style>
