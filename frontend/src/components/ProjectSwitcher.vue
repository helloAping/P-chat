<script setup lang="ts">
/**
 * ProjectSwitcher — compact trigger + popover for the unified sidebar.
 *
 * Replaces the permanent project-rail list. AppRail "项目" can call
 * open() via defineExpose to expand this popover.
 */
import { computed, nextTick, ref } from 'vue'
import { NInput, NPopover } from 'naive-ui'
import { state, setActiveProject } from '../stores/chat'
import {
  ChevronDown, Plus, Globe, Folder, Check, Search, GitBranch,
} from './icons'

const emit = defineEmits<{
  (e: 'add-project'): void
}>()

const open = ref(false)
const query = ref('')
const searchInputRef = ref<{ focus?: () => void } | null>(null)

type SwitcherProject = {
  name: string
  path: string
  branch?: string
  dirty?: boolean
}

function compactPath(path: string, maxSegments = 3): string {
  const value = path.trim()
  if (!value) return ''
  const parts = value.split(/[\\/]+/).filter(Boolean)
  if (parts.length <= maxSegments) return value
  return `…\\${parts.slice(-maxSegments).join('\\')}`
}

const activeProject = computed<SwitcherProject | null>(() => {
  if (!state.activeProjectPath) return null
  const p = state.projects.find(p => p.path === state.activeProjectPath)
  return p || null
})

const activeName = computed(() => {
  if (!state.activeProjectPath) return '全局会话'
  return activeProject.value?.name || state.activeProjectPath
})

const activeMeta = computed(() => {
  if (!state.activeProjectPath) return '未绑定项目目录的会话'
  if (activeProject.value) return projectMeta(activeProject.value)
  return compactPath(state.activeProjectPath, 3)
})

const activePathMeta = computed(() => {
  if (!state.activeProjectPath) return '未绑定项目目录的会话'
  return compactPath(state.activeProjectPath, 3)
})

const activeBranch = computed(() => activeProject.value?.branch || '')

const hasActiveStatus = computed(() => typeof activeProject.value?.dirty === 'boolean')

const activeStatusTitle = computed(() => {
  if (!hasActiveStatus.value) return ''
  return activeProject.value?.dirty ? '工作区有改动' : '工作区干净'
})

const filteredProjects = computed(() => {
  const q = query.value.trim().toLowerCase()
  const list = state.projects
  if (!q) return list
  return list.filter(p =>
    p.name.toLowerCase().includes(q) ||
    p.path.toLowerCase().includes(q) ||
    (p.branch || '').toLowerCase().includes(q),
  )
})

function projectMeta(project: SwitcherProject): string {
  const meta = [compactPath(project.path, 3)]
  if (project.branch) meta.push(project.branch)
  if (typeof project.dirty === 'boolean') meta.push(project.dirty ? '有改动' : '干净')
  return meta.join(' · ')
}

function hasProjectStatus(project: SwitcherProject): boolean {
  return !!project.branch || typeof project.dirty === 'boolean'
}

function projectStatusTitle(project: SwitcherProject): string {
  if (typeof project.dirty !== 'boolean') return project.branch || ''
  return project.dirty ? '工作区有改动' : '工作区干净'
}

async function selectProject(path: string) {
  await setActiveProject(path)
  open.value = false
  query.value = ''
}

function onAddProject() {
  open.value = false
  emit('add-project')
}

async function openSwitcher() {
  open.value = true
  await nextTick()
  searchInputRef.value?.focus?.()
}

defineExpose({ open: openSwitcher })
</script>

<template>
  <div class="project-switcher">
    <NPopover
      v-model:show="open"
      trigger="click"
      placement="bottom-start"
      :show-arrow="false"
      raw
      display-directive="show"
    >
      <template #trigger>
        <button
          type="button"
          class="switcher-trigger"
          :title="activeMeta"
          aria-label="切换项目"
          aria-haspopup="listbox"
          :aria-expanded="open"
        >
          <span class="switcher-icon" aria-hidden="true">
            <Globe v-if="!state.activeProjectPath" :size="18" />
            <Folder v-else :size="18" />
          </span>
          <span class="switcher-copy">
            <span class="switcher-name">{{ activeName }}</span>
            <span class="switcher-meta-row">
              <span class="switcher-meta">{{ activePathMeta }}</span>
              <span v-if="activeBranch" class="switcher-branch">
                <GitBranch :size="11" />
                <span>{{ activeBranch }}</span>
              </span>
              <span
                v-if="hasActiveStatus"
                class="switcher-status-dot"
                :class="{ dirty: activeProject && activeProject.dirty }"
                :title="activeStatusTitle"
                aria-hidden="true"
              />
            </span>
          </span>
          <ChevronDown :size="14" class="switcher-chevron" />
        </button>
      </template>

      <div class="switcher-panel" role="listbox" aria-label="项目列表">
        <div class="switcher-search">
          <NInput
            ref="searchInputRef"
            v-model:value="query"
            size="small"
            clearable
            placeholder="搜索项目"
          >
            <template #prefix>
              <Search :size="14" class="search-icon" />
            </template>
          </NInput>
        </div>

        <button type="button" class="switcher-row" @click="onAddProject">
          <Plus :size="15" />
          <span class="row-label">新建项目</span>
        </button>

        <button
          type="button"
          class="switcher-row"
          :class="{ 'switcher-row--active': !state.activeProjectPath }"
          role="option"
          :aria-selected="!state.activeProjectPath"
          @click="selectProject('')"
        >
          <Globe :size="15" />
          <span class="row-body">
            <span class="row-label">全局会话</span>
            <span class="row-meta">未绑定项目</span>
          </span>
          <Check v-if="!state.activeProjectPath" :size="14" class="row-check" />
        </button>

        <div v-if="filteredProjects.length" class="switcher-divider" />

        <button
          v-for="p in filteredProjects"
          :key="p.path"
          type="button"
          class="switcher-row"
          :class="{ 'switcher-row--active': p.path === state.activeProjectPath }"
          role="option"
          :aria-selected="p.path === state.activeProjectPath"
          :title="projectMeta(p)"
          @click="selectProject(p.path)"
        >
          <Folder :size="15" />
          <span class="row-body">
            <span class="row-label">{{ p.name }}</span>
            <span class="row-meta-line">
              <span
                v-if="hasProjectStatus(p)"
                class="row-status-dot"
                :class="{ dirty: p.dirty }"
                :title="projectStatusTitle(p)"
                aria-hidden="true"
              />
              <span class="row-meta">{{ projectMeta(p) }}</span>
            </span>
          </span>
          <Check
            v-if="p.path === state.activeProjectPath"
            :size="14"
            class="row-check"
          />
        </button>

        <div v-if="query.trim() && !filteredProjects.length" class="switcher-empty">
          无匹配项目
        </div>
      </div>
    </NPopover>
  </div>
</template>

<style scoped>
.project-switcher {
  min-width: 0;
  flex: 1 1 auto;
  display: flex;
  align-items: center;
}
.switcher-trigger {
  min-width: 0;
  flex: 1 1 auto;
  display: flex;
  align-items: center;
  gap: var(--space-2);
  min-height: 44px;
  padding: var(--space-2);
  border: 1px solid var(--border-default);
  border-radius: var(--radius-md);
  background: var(--surface-0);
  color: var(--text-primary);
  cursor: pointer;
  text-align: left;
  transition: var(--transition-colors);
}
.switcher-trigger:hover {
  background: color-mix(in srgb, var(--brand-500) 5%, var(--surface-0));
  border-color: color-mix(in srgb, var(--brand-500) 22%, var(--border-default));
}
.switcher-icon {
  width: 28px;
  height: 28px;
  flex: 0 0 28px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border-radius: var(--radius-sm);
  background: var(--brand-50);
  color: var(--brand-600);
}
.switcher-copy {
  min-width: 0;
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 1px;
  overflow: hidden;
}
.switcher-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 13.5px;
  font-weight: 600;
  line-height: 1.25;
  letter-spacing: 0;
}
.switcher-meta {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--text-tertiary);
  font-size: 11px;
  font-family: var(--font-mono);
  line-height: 1.25;
}
.switcher-meta-row {
  min-width: 0;
  display: flex;
  align-items: center;
  gap: var(--space-1);
  overflow: hidden;
}
.switcher-meta-row .switcher-meta {
  flex: 1 1 auto;
}
.switcher-branch {
  flex: 0 0 auto;
  display: inline-flex;
  align-items: center;
  gap: 2px;
  color: var(--text-secondary);
  font-size: 11px;
  font-family: var(--font-mono);
  line-height: 1.25;
}
.switcher-status-dot {
  width: var(--space-1);
  height: var(--space-1);
  flex: 0 0 var(--space-1);
  border-radius: var(--radius-pill);
  background: var(--success-500);
}
.switcher-status-dot.dirty {
  background: var(--warn-500);
}
.switcher-chevron {
  flex-shrink: 0;
  color: var(--text-tertiary);
}
.switcher-panel {
  width: min(var(--project-switcher-width), calc(100vw - var(--space-6)));
  max-height: min(420px, 70vh);
  overflow-y: auto;
  padding: var(--space-2);
  background: var(--surface-1);
  border: 1px solid var(--border-default);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-lg);
}
.switcher-search {
  padding: 0 0 var(--space-2);
}
.switcher-search :deep(.search-icon) {
  color: var(--text-tertiary);
}
.switcher-row {
  width: 100%;
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: var(--space-2);
  border: none;
  border-radius: var(--radius-md);
  background: transparent;
  color: var(--text-secondary);
  cursor: pointer;
  text-align: left;
  transition: var(--transition-colors);
}
.switcher-row:hover {
  background: var(--surface-2);
  color: var(--text-primary);
}
.switcher-row--active {
  background: var(--brand-50);
  color: var(--brand-600);
}
.row-body {
  min-width: 0;
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 1px;
}
.row-label {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 12.5px;
  font-weight: 500;
  color: inherit;
}
.row-meta {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-size: 11px;
  font-family: var(--font-mono);
  color: var(--text-tertiary);
}
.row-meta-line {
  min-width: 0;
  display: flex;
  align-items: center;
  gap: var(--space-1);
}
.row-status-dot {
  width: var(--space-1);
  height: var(--space-1);
  flex: 0 0 var(--space-1);
  border-radius: var(--radius-pill);
  background: var(--success-500);
}
.row-status-dot.dirty {
  background: var(--warn-500);
}
.row-check {
  flex-shrink: 0;
  color: var(--brand-500);
}
.switcher-divider {
  height: 1px;
  margin: var(--space-1) 0;
  background: var(--border-subtle);
}
.switcher-empty {
  padding: var(--space-3);
  text-align: center;
  font-size: 12px;
  color: var(--text-tertiary);
}
</style>
