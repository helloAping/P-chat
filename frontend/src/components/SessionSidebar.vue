<script setup lang="ts">
/**
 * SessionSidebar — the left rail of the P-Chat window.
 *
 * Layout (PR #4 of the UI refresh):
 *
 *   ┌─────────────────────────────┐
 *   │  BrandLogo  P-Chat  🌞 ⋯    │  header
 *   ├─────────────────────────────┤
 *   │  📁 项目: P-Chat       v    │  project bar
 *   ├─────────────────────────────┤
 *   │  🔍 搜索会话内容…            │  search bar
 *   ├─────────────────────────────┤
 *   │  [ + 新建对话 ]              │  primary CTA
 *   ├─────────────────────────────┤
 *   │  置顶 · 3                    │  group (pinned)
 *   │  ⭐  会话标题                │
 *   │  今天                        │  group
 *   │  · 会话标题          14:32  │
 *   │  昨天                        │
 *   │  本周                        │
 *   │  本月                        │
 *   │  更早 ▾                      │
 *   ├─────────────────────────────┤
 *   │  BrandLogo  v1.0.4    ⚙    │  user card
 *   └─────────────────────────────┘
 *
 * The list is grouped by relative time (pinned / today /
 * yesterday / this week / this month / older) so the user can
 * scan recent work at a glance. Pinned sessions are sticky and
 * stay at the top across reloads — the ID set is persisted in
 * localStorage (P-Chat is local-first, the server doesn't track
 * this; a future schema migration could move it to the DB).
 */
import { computed, ref, onMounted, watch } from 'vue'
import { NButton, NInput, NScrollbar, NSpace, NSelect, NModal, NTag, NSpin, NDropdown, useMessage } from 'naive-ui'
import { h } from 'vue'
import {
  state, createSession, deleteSessionById, renameSession, switchSession,
  loadProjects, setActiveProject,
} from '../stores/chat'
import * as api from '../api/client'
import type { SelectOption } from 'naive-ui'
import { checkUpdate } from '../api/update'
import type { UpdateInfo } from '../api/update'
import type { SearchResult } from '../api/client'
import TokenStatsModal from './TokenStatsModal.vue'
import AppModal from './AppModal.vue'
import BrandLogo from './BrandLogo.vue'
import {
  Plus, BarChart3, Settings, Info, Bell, Globe, Folder, Sun, Moon, MoreHorizontal,
  Search as SearchIcon, Pencil, X as XIcon, Pin, PinOff,
  ChevronDown, ChevronRight, Circle, MessageSquare,
} from './icons'

const APP_VERSION = __APP_VERSION__
const GITHUB_REPO = __GITHUB_REPO__

const emit = defineEmits<{ (e: 'open-settings'): void }>()

const themeName = defineModel<'dark' | 'light'>('themeName', { default: 'dark' })
const showTokenStats = ref(false)

const message = useMessage()
const showAddProject = ref(false)
const newProjectName = ref('')
const newProjectPath = ref('')
const showConfirmDeleteProject = ref(false)
const showAbout = ref(false)
const showRename = ref(false)
const renameId = ref('')
const renameTitle = ref('')
const updateInfo = ref<UpdateInfo | null>(null)
const pendingDeleteSessionId = ref('')
const showConfirmDeleteSession = ref(false)
const showOlderExpanded = ref(false)

// ---------------------------------------------------------------------------
// Pinned sessions (client-side, persisted in localStorage)
//
// P-Chat is a local-first app and the server doesn't track which
// sessions are pinned — that would require a DB schema migration
// (see .agents/docs/memory.md for the upgrade flow). For now the
// pin set is per-browser; a user with two browsers can pin
// independently in each. Acceptable trade-off given the feature
// is mostly personal organization.
// ---------------------------------------------------------------------------
const PINNED_KEY = 'pchat-pinned-sessions'
const pinnedIds = ref<Set<string>>(new Set())

function loadPinned() {
  try {
    const raw = localStorage.getItem(PINNED_KEY)
    if (raw) pinnedIds.value = new Set(JSON.parse(raw))
  } catch { /* ignore — fall back to empty set */ }
}
function persistPinned() {
  try { localStorage.setItem(PINNED_KEY, JSON.stringify([...pinnedIds.value])) } catch { /* ignore */ }
}
function isPinned(id: string) { return pinnedIds.value.has(id) }
function togglePin(id: string) {
  const next = new Set(pinnedIds.value)
  if (next.has(id)) next.delete(id)
  else next.add(id)
  pinnedIds.value = next
  persistPinned()
}
loadPinned()

// ---------------------------------------------------------------------------
// Session grouping by relative time
//
// `groupKey(ts)` returns one of 'pinned' | 'today' | 'yesterday'
// | 'thisWeek' | 'thisMonth' | 'older'. The same function is
// used to build the `groupedSessions` computed below.
// ---------------------------------------------------------------------------
function groupKey(ts: number, id: string): string {
  if (isPinned(id)) return 'pinned'
  const d = new Date(ts * 1000)
  const now = new Date()
  const today = new Date(now.getFullYear(), now.getMonth(), now.getDate())
  const yesterday = new Date(today.getTime() - 86_400_000)
  if (d >= today) return 'today'
  if (d >= yesterday) return 'yesterday'
  const weekAgo = new Date(today.getTime() - 7 * 86_400_000)
  if (d >= weekAgo) return 'thisWeek'
  const monthStart = new Date(now.getFullYear(), now.getMonth(), 1)
  if (d >= monthStart) return 'thisMonth'
  return 'older'
}

const GROUP_LABELS: Record<string, string> = {
  pinned: '置顶',
  today: '今天',
  yesterday: '昨天',
  thisWeek: '本周',
  thisMonth: '本月',
  older: '更早',
}

const GROUP_ORDER = ['pinned', 'today', 'yesterday', 'thisWeek', 'thisMonth', 'older'] as const

const sortedSessions = computed(() =>
  [...state.sessions].sort((a, b) => b.updated_at - a.updated_at),
)

const groupedSessions = computed(() => {
  const groups: Record<string, typeof sortedSessions.value> = {}
  for (const s of sortedSessions.value) {
    const k = groupKey(s.updated_at, s.id)
    if (!groups[k]) groups[k] = []
    groups[k].push(s)
  }
  return GROUP_ORDER
    .filter(k => groups[k]?.length)
    .map(k => ({ key: k, label: GROUP_LABELS[k], sessions: groups[k] }))
})

// ---------------------------------------------------------------------------
// Per-item time format
//
// Inside a group, the time column shows the most useful unit:
//   - today  : HH:MM
//   - yesterday: 昨天
//   - thisWeek : 周X
//   - thisMonth: MM/DD
//   - older    : MM/DD
// The group label already provides coarse time, so the per-item
// time only needs to be a fine-grained hint.
// ---------------------------------------------------------------------------
function shortTime(ts: number, group: string): string {
  const d = new Date(ts * 1000)
  switch (group) {
    case 'today':
      return d.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })
    case 'yesterday':
      return '昨天'
    case 'thisWeek': {
      const days = ['日', '一', '二', '三', '四', '五', '六']
      return `周${days[d.getDay()]}`
    }
    case 'thisMonth':
    case 'older':
    default:
      return `${String(d.getMonth() + 1).padStart(2, '0')}/${String(d.getDate()).padStart(2, '0')}`
  }
}

// ---------------------------------------------------------------------------
// Top-bar menu (theme-agnostic): Token usage / Settings / About
// ---------------------------------------------------------------------------
const menuOptions = computed(() => [
  {
    label: 'Token 用量',
    key: 'token-stats',
    icon: () => h(BarChart3, { size: 16 }),
  },
  {
    label: '设置',
    key: 'settings',
    icon: () => h(Settings, { size: 16 }),
  },
  { type: 'divider' as const, key: 'd1' },
  {
    label: updateInfo.value?.hasUpdate ? `关于 (新版本 ${updateInfo.value.latest})` : '关于',
    key: 'about',
    icon: () => h(updateInfo.value?.hasUpdate ? Bell : Info, { size: 16 }),
  },
])

function handleMenuSelect(key: string) {
  switch (key) {
    case 'token-stats': showTokenStats.value = true; break
    case 'settings': emit('open-settings'); break
    case 'about': openAbout(); break
  }
}

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------
const searchQuery = ref('')
const searchResults = ref<SearchResult[]>([])
const searchLoading = ref(false)
let searchTimer: ReturnType<typeof setTimeout> | null = null

function doSearch() {
  const q = searchQuery.value.trim()
  if (!q) {
    searchResults.value = []
    searchLoading.value = false
    return
  }
  searchLoading.value = true
  api.searchMessages(q, 15).then(
    r => { searchResults.value = r.results; searchLoading.value = false },
    () => { searchLoading.value = false },
  )
}

watch(searchQuery, () => {
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(doSearch, 300)
})

function clearSearch() {
  searchQuery.value = ''
  searchResults.value = []
}

function jumpToResult(r: SearchResult) {
  clearSearch()
  switchSession(r.conversation_id)
}

// ---------------------------------------------------------------------------
// Per-session action menu (NDropdown triggered by the hover ⋯
// button). Lets the user pin / unpin / rename / delete without
// leaving the keyboard / mouse position. We use NDropdown — not
// a custom popover — because each item has a tiny callback
// difference per session (id), and NDropdown's options array
// model expresses that cleanly. Custom icons render via the
// `renderIcon` (provided by NDropdown for the render-label
// function), here we use a plain function icon() — NDropdown
// accepts both.
// ---------------------------------------------------------------------------
function sessionMenuOptions(id: string) {
  const pinned = isPinned(id)
  return [
    {
      key: 'pin',
      label: pinned ? '取消置顶' : '置顶',
      icon: () => h(pinned ? PinOff : Pin, { size: 14 }),
    },
    {
      key: 'rename',
      label: '重命名',
      icon: () => h(Pencil, { size: 14 }),
    },
    { type: 'divider' as const, key: 'd' },
    {
      key: 'delete',
      label: '归档',
      icon: () => h(XIcon, { size: 14 }),
    },
  ]
}

function onSessionMenu(key: string, id: string) {
  switch (key) {
    case 'pin': togglePin(id); break
    case 'rename': onRename(id); break
    case 'delete': {
      // Delete needs a synthetic event so the confirmation
      // modal's stopPropagation() call (in the click handler
      // path) doesn't try to also fire on the parent item.
      onDelete(id, new MouseEvent('click'))
      break
    }
  }
}

// ---------------------------------------------------------------------------
// Per-session NDropdown instance registry. We render one
// NDropdown per row (each session has its own menu with the
// session id baked in), and we need a ref to call
// `show()`/`hide()` from a right-click handler on the title
// — the user can now open the action menu either by:
//   (1) clicking the three-dot button (primary, anchored
//       to the button itself), or
//   (2) right-clicking on the conversation title (anchored
//       to the three-dot button, but triggered by the
//       contextmenu event so the user doesn't have to aim
//       at the small icon).
// The ref map is keyed by session id; Vue's template ref
// callback re-runs on every render, so the setter is
// idempotent and stable across the dropdown's lifetime.
// ---------------------------------------------------------------------------
const sessionMenuRefs = ref<Record<string, { show: () => void; hide: () => void } | null>>({})
function bindSessionMenuRef(id: string) {
  return (el: any) => {
    // NDropdown exposes `show()` / `hide()` on the instance.
    // `el` is null on unmount — clear the slot so the map
    // doesn't grow unbounded across re-renders.
    if (el) {
      sessionMenuRefs.value[id] = el
    } else {
      delete sessionMenuRefs.value[id]
    }
  }
}
function openSessionMenu(e: MouseEvent, id: string) {
  // Suppress the browser's native context menu so the
  // dropdown replaces it. The dropdown is anchored to
  // the three-dot button, not to the cursor, but that's
  // the standard naive-ui pattern — the menu is associated
  // with the row, not the click position.
  e.preventDefault()
  sessionMenuRefs.value[id]?.show()
}

// ---------------------------------------------------------------------------
// Session create / rename / delete
// ---------------------------------------------------------------------------
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
  // Drop any pin the user had set on the archived session so
  // it doesn't leak into the next mount.
  if (pinnedIds.value.has(id)) togglePin(id)
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
  renameId.value = id
  renameTitle.value = s.title || ''
  showRename.value = true
}

async function confirmRename() {
  const title = renameTitle.value.trim()
  if (title) {
    try {
      await renameSession(renameId.value, title)
      message.success('已重命名')
    } catch (e: any) {
      message.error(`重命名失败: ${e.message}`)
    }
  }
  showRename.value = false
  renameId.value = ''
  renameTitle.value = ''
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

const projectOptions = computed<SelectOption[]>(() => [
  {
    label: '全局',
    value: '',
    renderLabel: (option: SelectOption) =>
      h('span', { class: 'project-option' }, [
        h(Globe, { size: 14, class: 'project-option-icon' }),
        option.label as string,
      ]),
  },
  ...state.projects.map(p => ({
    label: p.name,
    value: p.path,
    renderLabel: (option: SelectOption) =>
      h('span', { class: 'project-option' }, [
        h(Folder, { size: 14, class: 'project-option-icon' }),
        option.label as string,
      ]),
  })),
])

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
    <!-- Brand header: logo + wordmark, then theme toggle + menu. -->
    <div class="sidebar-header">
      <div class="logo">
        <BrandLogo :size="22" />
        <span>P-Chat</span>
      </div>
      <NSpace size="small">
        <NButton size="small" quaternary @click="toggleTheme" :title="themeName === 'dark' ? '切换到浅色主题' : '切换到深色主题'" aria-label="切换主题">
          <component :is="themeName === 'dark' ? Sun : Moon" :size="16" />
        </NButton>
        <NDropdown trigger="click" :options="menuOptions" @select="handleMenuSelect">
          <NButton size="small" quaternary title="更多" aria-label="更多">
            <MoreHorizontal :size="16" />
          </NButton>
        </NDropdown>
      </NSpace>
    </div>

    <!-- Project bar: which project's sessions are we browsing? -->
    <div class="project-bar">
      <NSelect
        :value="state.activeProjectPath"
        :options="projectOptions"
        size="small"
        placeholder="选择项目"
        @update:value="onProjectChange"
      />
      <NButton size="tiny" quaternary @click="showAddProject = true" title="添加项目目录" aria-label="添加项目目录">
        <Plus :size="14" />
      </NButton>
      <NButton v-if="state.activeProjectPath" size="tiny" quaternary @click="showConfirmDeleteProject = true" title="删除当前项目" class="project-remove-btn">
        <XIcon :size="14" />
      </NButton>
    </div>

    <!-- Search bar (filters across all sessions in current project). -->
    <div class="search-bar">
      <NInput
        v-model:value="searchQuery"
        size="small"
        placeholder="搜索会话内容..."
        clearable
        @clear="clearSearch"
      >
        <template #prefix>
          <SearchIcon :size="14" class="search-icon" />
        </template>
      </NInput>
    </div>

    <!-- New session CTA: pinned to the top so it's always
         one click away. Replaces the old footer "新建会话"
         button which used to push the action below the fold
         once the sidebar scrolled. -->
    <div class="new-session-bar">
      <button class="new-session-btn" @click="onNew" aria-label="新建对话">
        <Plus :size="14" />
        <span>新建对话</span>
      </button>
    </div>

    <NScrollbar style="flex: 1; min-height: 0">
      <!-- Search results -->
      <div v-if="searchQuery.trim()" class="search-results">
        <NSpin :show="searchLoading" size="small">
          <div v-if="searchResults.length === 0 && !searchLoading" class="search-empty">
            无匹配结果
          </div>
          <div
            v-for="r in searchResults"
            :key="`${r.conversation_id}-${r.message_id}`"
            class="search-result-item"
            @click="jumpToResult(r)"
          >
            <div class="result-header">
              <span class="result-title">{{ r.conversation_title || '(无标题)' }}</span>
              <span class="result-time">{{ shortTime(r.created_at, 'today') }}</span>
            </div>
            <div class="result-snippet">{{ r.snippet }}</div>
          </div>
        </NSpin>
      </div>

      <!-- Session list, grouped by relative time. -->
      <div v-else class="session-list">
        <template v-for="group in groupedSessions" :key="group.key">
          <div class="group">
            <div class="group-header">
              <span class="group-label">{{ group.label }}</span>
              <span v-if="group.key === 'pinned'" class="group-count">{{ group.sessions.length }}</span>
            </div>
            <div
              v-for="s in group.key === 'older' && !showOlderExpanded ? [] : group.sessions"
              :key="s.id"
              class="session-item"
              :class="{ active: s.id === state.currentID, pinned: isPinned(s.id) }"
            >
              <div class="item-row" @click="switchSession(s.id)">
                <div class="item-main" @contextmenu="openSessionMenu($event, s.id)">
                  <span v-if="isPinned(s.id)" class="item-pin" :title="'已置顶'" aria-label="已置顶">
                    <Pin :size="11" />
                  </span>
                  <span class="item-title">{{ s.title || '(无标题)' }}</span>
                  <span v-if="state.streaming[s.id]" class="streaming-dot" title="正在生成" aria-label="正在生成">
                    <Circle :size="7" fill="currentColor" />
                  </span>
                </div>
                <div class="item-meta">
                  <span class="item-time">{{ shortTime(s.updated_at, group.key) }}</span>
                  <NDropdown
                    :ref="bindSessionMenuRef(s.id)"
                    trigger="click"
                    placement="bottom-end"
                    :options="sessionMenuOptions(s.id)"
                    @select="(key) => onSessionMenu(key, s.id)"
                  >
                    <button
                      class="item-menu-btn"
                      :aria-label="'会话操作'"
                      title="更多（支持右键标题打开）"
                      @click.stop
                    >
                      <MoreHorizontal :size="12" />
                    </button>
                  </NDropdown>
                </div>
              </div>
            </div>
            <div
              v-if="group.key === 'older' && !showOlderExpanded && group.sessions.length > 0"
              class="older-toggle"
              @click="showOlderExpanded = true"
            >
              <ChevronDown :size="12" />
              <span>展开更早的 {{ group.sessions.length }} 个会话</span>
            </div>
            <div
              v-else-if="group.key === 'older' && showOlderExpanded"
              class="older-toggle"
              @click="showOlderExpanded = false"
            >
              <ChevronRight :size="12" />
              <span>收起</span>
            </div>
          </div>
        </template>
      </div>
    </NScrollbar>

    <!-- Footer user card: brand mark + version + quick action.
         Replaces the old "新建会话" footer with app-level info
         (since the new-session button moved to the top). The
         gear icon opens settings — the highest-traffic footer
         action — and clicking the brand area opens About. -->
    <!-- PR #9 follow-up: removed the BrandLogo from the
         user card — it's already in the sidebar header
         (the prominent logo at the top), so showing it
         again in the footer was visually redundant. The
         footer now shows just the app name + version as
         informational text, with the settings button on the
         right. The whole left side stays clickable to open
         About, preserving the existing behavior. -->
    <div class="user-card">
      <button
        class="user-card-brand"
        type="button"
        :title="'关于 P-Chat'"
        :aria-label="'关于 P-Chat'"
        @click="openAbout"
      >
        <span class="user-card-text">
          <span class="user-card-name">P-Chat</span>
          <span class="user-card-version">v{{ APP_VERSION }}</span>
        </span>
      </button>
      <NButton
        size="small"
        quaternary
        :title="'设置'"
        :aria-label="'设置'"
        @click="emit('open-settings')"
      >
        <Settings :size="14" />
      </NButton>
    </div>

    <!-- Modals (PR #7: migrated to AppModal for consistent
         styling). The about-modal is left as a raw NModal
         for now because it has rich body content (update
         banner, version info) that doesn't fit the
         "header + body + footer" pattern cleanly. -->

    <AppModal
      v-model:show="showAddProject"
      title="添加项目"
      size="md"
    >
      <div class="add-project-form">
        <label>项目名称</label>
        <NInput v-model:value="newProjectName" placeholder="例如：我的项目" />
        <label style="margin-top: 12px">项目目录</label>
        <div class="path-row">
          <NInput v-model:value="newProjectPath" placeholder="例如：D:\projects\my-app" style="flex:1" />
          <NButton size="small" @click="pickDirectory" title="选择目录">浏览</NButton>
        </div>
      </div>
      <template #footer>
        <NButton size="small" @click="showAddProject = false">取消</NButton>
        <NButton size="small" type="primary" @click="onAddProject">添加</NButton>
      </template>
    </AppModal>

    <AppModal
      v-model:show="showConfirmDeleteSession"
      title="确认归档"
      size="sm"
      accent-top
      accent-variant="warn"
    >
      <p>确定要归档此会话吗？归档后可在「设置 → 归档」中恢复。</p>
      <template #footer>
        <NButton size="small" @click="showConfirmDeleteSession = false">取消</NButton>
        <NButton size="small" type="warning" @click="confirmDeleteSession">归档</NButton>
      </template>
    </AppModal>

    <AppModal
      v-model:show="showConfirmDeleteProject"
      title="确认删除项目"
      size="sm"
      accent-top
      accent-variant="error"
    >
      <p>确定要删除当前项目吗？该项目的会话不会被删除，但将不再关联到此项目。</p>
      <template #footer>
        <NButton size="small" @click="showConfirmDeleteProject = false">取消</NButton>
        <NButton size="small" type="error" @click="confirmDeleteProject">删除</NButton>
      </template>
    </AppModal>

    <AppModal
      v-model:show="showRename"
      title="重命名会话"
      size="sm"
    >
      <NInput
        v-model:value="renameTitle"
        placeholder="输入新标题"
        @keyup.enter="confirmRename"
        autofocus
      />
      <template #footer>
        <NButton size="small" @click="showRename = false">取消</NButton>
        <NButton size="small" type="primary" @click="confirmRename">确认</NButton>
      </template>
    </AppModal>

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
            <p>发现新版本 <strong>{{ updateInfo.latest }}</strong> · 当前 {{ APP_VERSION }}</p>
            <p class="update-body" v-if="updateInfo.body">{{ updateInfo.body }}</p>
            <NButton size="small" type="primary" tag="a" :href="updateInfo.url" target="_blank">前往下载</NButton>
          </div>
          <p v-else class="update-ok">当前已是最新版本 {{ APP_VERSION }}</p>
        </template>
        <p v-else class="update-ok">正在检查更新…</p>

        <div class="about-links">
          <a :href="'https://github.com/' + GITHUB_REPO" target="_blank">GitHub</a>
          <span class="sep">·</span>
          <a :href="'https://github.com/' + GITHUB_REPO + '/issues'" target="_blank">反馈问题</a>
        </div>
      </div>
    </NModal>

    <TokenStatsModal v-model:show="showTokenStats" />
  </aside>
</template>

<style scoped>
.sidebar {
  width: 280px;
  background: var(--surface-1);
  border-right: 1px solid var(--border-subtle);
  display: flex;
  flex-direction: column;
  flex-shrink: 0;
  transition: width var(--dur-base) var(--ease-out);
}

/* --- Brand header ------------------------------------------------------ */
.sidebar-header {
  padding: 10px 12px;
  display: flex;
  justify-content: space-between;
  align-items: center;
  flex-shrink: 0;
}
.logo {
  display: flex;
  align-items: center;
  gap: 8px;
  font-weight: 600;
  font-size: 14px;
  letter-spacing: -0.01em;
  color: var(--text-primary);
}

/* --- Project bar ------------------------------------------------------- */
.project-bar {
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 6px 10px;
  border-bottom: 1px solid var(--border-subtle);
  flex-shrink: 0;
}
.project-bar :deep(.n-base-select) { flex: 1; }
.project-remove-btn { color: var(--warn-500); }

/* --- Search bar -------------------------------------------------------- */
.search-bar {
  padding: 6px 10px;
  border-bottom: 1px solid var(--border-subtle);
  flex-shrink: 0;
}
.search-bar :deep(.search-icon) { color: var(--text-tertiary); }

/* --- New session CTA (top, full width) -------------------------------- */
.new-session-bar {
  padding: 8px 10px;
  flex-shrink: 0;
}
.new-session-btn {
  width: 100%;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  padding: 7px 12px;
  background: var(--brand-500);
  color: #ffffff;
  border: 1px solid var(--brand-500);
  border-radius: var(--radius-md);
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
  transition: background var(--dur-fast) var(--ease-out), border-color var(--dur-fast) var(--ease-out);
}
.new-session-btn:hover {
  background: var(--brand-600);
  border-color: var(--brand-600);
}
.new-session-btn:active {
  background: var(--brand-700);
  border-color: var(--brand-700);
}

/* --- Session list (grouped) ------------------------------------------- */
.session-list { padding: 4px 8px 8px; }
.group { margin-bottom: 4px; }
.group-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 10px 8px 4px;
  font-size: 11px;
  font-weight: 600;
  color: var(--text-tertiary);
  text-transform: uppercase;
  letter-spacing: 0.06em;
}
.group-count {
  background: var(--surface-2);
  color: var(--text-tertiary);
  padding: 1px 6px;
  border-radius: var(--radius-pill);
  font-size: 10px;
  font-weight: 500;
  letter-spacing: 0;
  text-transform: none;
}

.session-item {
  position: relative;
  border-radius: var(--radius-md);
  cursor: pointer;
  margin: 1px 0;
  /* Selected indicator: 2px brand vertical bar drawn via
   * box-shadow inset so we don't need to set border + offset
   * the contents. The bar lives on the very left edge and
   * runs the full height of the item. */
  transition: background var(--dur-fast) var(--ease-out);
}
.session-item:hover { background: var(--surface-3); }
.session-item.active {
  background: var(--surface-3);
  box-shadow: inset 2px 0 0 0 var(--brand-500);
}

.item-row {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 6px 8px 6px 10px;
  min-height: 30px;
}
.item-main {
  flex: 1;
  min-width: 0;
  display: flex;
  align-items: center;
  gap: 4px;
  font-size: 13px;
  color: var(--text-primary);
}
.item-pin {
  color: var(--brand-500);
  flex-shrink: 0;
  display: inline-flex;
}
.item-title {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  font-weight: 500;
}
.session-item.active .item-title { color: var(--text-primary); font-weight: 600; }
.streaming-dot {
  color: var(--brand-500);
  animation: pulse 1.2s infinite;
  display: inline-flex;
  align-items: center;
  flex-shrink: 0;
}
@keyframes pulse { 0%,100% { opacity: 1; } 50% { opacity: 0.35; } }

.item-meta {
  display: flex;
  align-items: center;
  gap: 4px;
  flex-shrink: 0;
}
.item-time {
  font-size: 11px;
  color: var(--text-tertiary);
  font-variant-numeric: tabular-nums;
  letter-spacing: -0.01em;
}
.session-item.active .item-time { color: var(--text-secondary); }
.item-menu-btn {
  background: none;
  border: none;
  color: var(--text-tertiary);
  cursor: pointer;
  padding: 3px 4px;
  border-radius: 3px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  opacity: 0;
  transition: opacity var(--dur-fast) var(--ease-out), background var(--dur-fast) var(--ease-out);
}
.session-item:hover .item-menu-btn { opacity: 1; }
.item-menu-btn:hover {
  background: var(--surface-3);
  color: var(--text-primary);
}

/* Older-group collapsible toggle. */
.older-toggle {
  display: flex;
  align-items: center;
  gap: 4px;
  padding: 6px 10px;
  font-size: 12px;
  color: var(--text-tertiary);
  cursor: pointer;
  border-radius: var(--radius-sm);
  user-select: none;
}
.older-toggle:hover { color: var(--text-primary); background: var(--surface-3); }

/* --- Per-session NDropdown style overrides --------------------------- */
/* NDropdown teleports the menu to body, so scoped styles don't
 * reach it. NDropdown already styles its options reasonably
 * (uses our --text-primary etc. via the theme overrides), but
 * we tighten the icon-text gap and the hover background to
 * match the rest of the sidebar. */
:deep(.n-dropdown-option) {
  display: flex;
  align-items: center;
  gap: 8px;
}
:deep(.n-dropdown-option .n-dropdown-option-body__prefix) {
  width: 14px;
  height: 14px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  color: var(--text-tertiary);
}

/* --- Search results --------------------------------------------------- */
.search-results { padding: 8px; }
.search-result-item {
  padding: 10px 12px; margin-bottom: 6px; border-radius: 6px;
  cursor: pointer; background: var(--surface-2); transition: background 0.1s;
}
.search-result-item:hover { background: var(--surface-3); }
.result-header { display: flex; justify-content: space-between; align-items: center; margin-bottom: 4px; }
.result-title { font-size: 12px; font-weight: 600; }
.result-time { font-size: 10px; color: var(--text-tertiary); }
.result-snippet { font-size: 12px; color: var(--text-secondary); line-height: 1.4; white-space: pre-wrap; word-break: break-all; }
.search-empty { text-align: center; padding: 24px; color: var(--text-tertiary); font-size: 13px; }

/* --- Footer user card ------------------------------------------------- */
.user-card {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 8px;
  padding: 10px 12px;
  border-top: 1px solid var(--border-subtle);
  background: var(--surface-1);
  flex-shrink: 0;
}
.user-card-brand {
  display: flex;
  align-items: center;
  gap: 8px;
  background: transparent;
  border: none;
  padding: 4px 6px;
  border-radius: var(--radius-sm);
  cursor: pointer;
  color: var(--text-secondary);
  transition: background var(--dur-fast) var(--ease-out);
  min-width: 0;
}
.user-card-brand:hover { background: var(--surface-3); color: var(--text-primary); }
.user-card-text {
  display: flex;
  flex-direction: column;
  align-items: flex-start;
  min-width: 0;
}
.user-card-name {
  font-size: 12.5px;
  font-weight: 600;
  line-height: 1.2;
  color: var(--text-primary);
}
.user-card-version {
  font-size: 10.5px;
  color: var(--text-tertiary);
  font-variant-numeric: tabular-nums;
  line-height: 1.2;
}

/* --- Add-project / confirm / about modals ---------------------------- */
/* AppModal handles body padding + footer button row, so the
 * migrated modals (add-project / archive / delete-project /
 * rename) only need the form-internal styles. */
.add-project-form label {
  display: block; font-size: 13px; margin-bottom: 4px; color: var(--text-secondary);
  font-weight: 500;
}
.path-row { display: flex; gap: 8px; align-items: center; }
.about-body { padding: 4px 0; }
.about-name { font-size: 18px; font-weight: 600; margin: 0 0 4px; }
.about-version { font-size: 13px; color: var(--text-tertiary); margin: 0 0 12px; }
.about-desc { font-size: 13px; color: var(--text-secondary); margin: 0 0 4px; }
.update-banner {
  margin: 12px 0;
  padding: 12px;
  background: var(--warn-50);
  border: 1px solid var(--warn-500);
  border-radius: 6px;
}
.update-banner p { margin: 4px 0; font-size: 13px; }
.update-body { color: var(--text-tertiary); font-size: 12px !important; max-height: 120px; overflow: auto; white-space: pre-wrap; }
.update-ok { font-size: 13px; color: var(--text-tertiary); margin: 12px 0; }
.about-links { margin-top: 16px; padding-top: 12px; border-top: 1px solid var(--border-default); font-size: 13px; }
.about-links a { color: var(--brand-500); text-decoration: none; }
.about-links a:hover { text-decoration: underline; }
.about-links .sep { color: var(--text-tertiary); margin: 0 6px; }
</style>

<style>
/* NSelect dropdown items are teleported to body, so scoped styles
 * don't reach them. Project picker uses renderLabel() to inject an
 * icon + label combo; this rule styles those elements globally. */
.project-option {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}
.project-option-icon {
  color: var(--text-tertiary);
  flex-shrink: 0;
}
</style>
