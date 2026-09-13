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
import { computed, ref, onMounted, watch, h, type Component } from 'vue'
import { NButton, NInput, NScrollbar, NTag, NSpin, NDropdown, useMessage, useDialog, useNotification } from 'naive-ui'
import {
  state, createSession, deleteSessionById, renameSession, switchSession,
  loadProjects, setActiveProject,
} from '../stores/chat'
import * as api from '../api/client'
import type { DropdownMenuProps, DropdownOption } from 'naive-ui'
import { checkUpdate, downloadUpdate, installDownloadedUpdate, SOFTWARE_RELEASE_PAGE } from '../api/update'
import type { UpdateArtifact, UpdateDownloadResult, UpdateInfo } from '../api/update'
import type { SearchResult, Session } from '../api/client'
import AppModal from './AppModal.vue'
import ProjectSwitcher from './ProjectSwitcher.vue'
import TokenStatsModal from './TokenStatsModal.vue'
import { suggestFilename, dedupeFilename, type ExportFormat } from '../utils/export'
import { displaySessionTitle, sessionSourceFromID } from '../im/sessionSource'
import {
  Plus, BarChart3, Settings, Info, Bell, Globe, Folder, FolderOpen, Sun, Moon, MoreHorizontal,
  Search as SearchIcon, Pencil, X as XIcon, Pin, PinOff, Archive,
  ChevronDown, ChevronRight, Circle, MessageSquare, FileText, File,
  Download, RotateCw, ExternalLink,
} from './icons'

const APP_VERSION = __APP_VERSION__
const GITHUB_REPO = __GITHUB_REPO__

// Online usage documentation, opened in the system browser from
// the About dialog (moved here from the old Settings nav footer).
const DOCS_URL = 'http://www.08ms.cn/article/p-chat'
function openDocs() {
  api.openExternalURL(DOCS_URL)
}

const emit = defineEmits<{ (e: 'open-settings'): void }>()

const themeName = defineModel<'dark' | 'light'>('themeName', { default: 'dark' })
const showTokenStats = ref(false)

const message = useMessage()
const dialog = useDialog()
const notification = useNotification()
const showAddProject = ref(false)
const newProjectName = ref('')
const newProjectPath = ref('')
const projectNameError = ref('')
const projectPathError = ref('')
const showConfirmDeleteProject = ref(false)
const showAbout = ref(false)
const showRename = ref(false)
const renameId = ref('')
const renameTitle = ref('')
const showExport = ref(false)
const exportSessionId = ref('')
const exportFormat = ref<ExportFormat>('pdf')
const exportSaving = ref(false)
const updateInfo = ref<UpdateInfo | null>(null)
const downloadedUpdate = ref<UpdateDownloadResult | null>(null)
const updateDownloading = ref(false)
const updateInstalling = ref(false)
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
const UPDATE_NOTIFY_KEY = 'pchat-update-notified-version'
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

function sessionDisplayTitle(s: Session): string {
  return displaySessionTitle(s.title || '', s.id)
}

function sessionSourceTitle(id: string): string {
  const source = sessionSourceFromID(id)
  return source ? `${source.label} 会话` : ''
}

// ---------------------------------------------------------------------------
// Header menu: project actions + app actions.
//
// Labels render icon+text in one `.app-action-item` row. We do not
// use NDropdown's `icon` slot — Naive reserves a 36px prefix column
// plus an empty suffix gutter, which is what made the session ⋯
// menu look sparse and misaligned.
// ---------------------------------------------------------------------------
function actionOption(
  key: string,
  label: string,
  icon: Component,
  extra: Partial<DropdownOption> = {},
): DropdownOption {
  return {
    key,
    label: () =>
      h('span', { class: 'app-action-item' }, [
        h(icon, { size: 16, class: 'app-action-item__icon' }),
        h('span', { class: 'app-action-item__label' }, label),
      ]),
    ...extra,
  }
}

const menuOptions = computed<DropdownOption[]>(() => [
  ...(state.activeProjectPath
    ? [
        actionOption('remove-project', '移除当前项目', XIcon),
        { type: 'divider' as const, key: 'project-divider' },
      ]
    : []),
  actionOption('token-stats', 'Token 用量', BarChart3),
  actionOption('settings', '设置', Settings),
  { type: 'divider' as const, key: 'd1' },
  actionOption(
    'about',
    updateInfo.value?.hasUpdate ? `关于 (新版本 ${updateInfo.value.latest})` : '关于',
    updateInfo.value?.hasUpdate ? Bell : Info,
  ),
])

const actionMenuProps: DropdownMenuProps = () => ({
  class: 'app-action-menu',
})

function handleMenuSelect(key: string) {
  switch (key) {
    case 'remove-project': {
      if (state.activeProjectPath) showConfirmDeleteProject.value = true
      break
    }
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
  api.searchMessages(q, 15, state.activeProjectPath).then(
    r => { searchResults.value = r.results; searchLoading.value = false },
    () => { searchLoading.value = false },
  )
}

watch(searchQuery, () => {
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(doSearch, 300)
})

watch(showAddProject, (show) => {
  if (!show) {
    newProjectName.value = ''
    newProjectPath.value = ''
    projectNameError.value = ''
    projectPathError.value = ''
  }
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
    actionOption('pin', pinned ? '取消置顶' : '置顶', pinned ? PinOff : Pin),
    actionOption('rename', '重命名', Pencil),
    { type: 'divider' as const, key: 'd' },
    actionOption('export', '导出对话', FileText),
    { type: 'divider' as const, key: 'd2' },
    actionOption('delete', '归档', Archive, { props: { class: 'app-action-danger' } }),
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
    case 'export': {
      openExportModal(id)
      break
    }
  }
}

// doExport triggers a server-side export. The rendering
// lives in pchat-server (internal/export +
// internal/server.ExportSession) and reads straight from
// the memory store, so the output is self-contained: no
// in-memory blob URLs to break, no dependency on what the
// SPA has hydrated.
//
// Format / size guards:
//   * sessions with > 5k messages show a confirmation
//     dialog. Large HTML/PDF exports can take a few
//     seconds when attachment data URLs are inlined.
//     The dialog
//     uses `useDialog().warning` so the user explicitly
//     approves.
//   * filename collisions are deduplicated (-2, -3, …)
//     by probing the suggested name against an in-memory
//     set of filenames we've already offered this
//     session.
//   * Wails desktop uses SaveExportFile, which opens the
//     OS-native save dialog and writes to the chosen path.
//     Browser preview falls back to the normal download
//     link because it cannot write arbitrary local paths.

// Module-level dedup cache. Reset on page reload;
// that's fine — it only guards rapid repeated exports in
// the same renderer process.
const recentlyExported = new Set<string>()

const exportTarget = computed(() => state.sessions.find(s => s.id === exportSessionId.value) || null)
const exportFilename = computed(() => suggestFilename(exportTarget.value?.title || '', exportFormat.value))

function openExportModal(id: string) {
  exportSessionId.value = id
  exportFormat.value = 'pdf'
  showExport.value = true
}

async function doExport(id: string, format: ExportFormat) {
  try {
    // Pre-flight: ask the API for the message count so
    // the 5k guard can fire BEFORE we burn a server-side
    // render. listMessages returns 200 + empty array for
    // a brand-new session, which is fine to export (the
    // server writes a header-only file in that case).
    const result = await api.listMessages(id)
    const messages = result.messages || []
    if (messages.length === 0) {
      message.warning('该会话没有消息可导出')
      return
    }
    if (messages.length > 5000) {
      const proceed = await new Promise<boolean>((resolve) => {
        dialog.warning({
          title: '会话较大',
          content: `该会话有 ${messages.length} 条消息,导出可能需要数秒并产生较大的文件。继续?`,
          positiveText: '继续导出',
          negativeText: '取消',
          onPositiveClick: () => resolve(true),
          onNegativeClick: () => resolve(false),
          onClose: () => resolve(false),
        })
      })
      if (!proceed) return
    }
    const title = state.sessions.find(s => s.id === id)?.title ?? ''
    // The server is the source of truth: it reads from
    // the store, renders the selected archive format, and
    // returns the file with Content-Disposition.
    const fmtQuery = format
    const resp = await fetch(`/api/v1/sessions/${encodeURIComponent(id)}/export?format=${fmtQuery}`, {
      method: 'GET',
    })
    if (!resp.ok) {
      const t = await resp.text()
      throw new Error(`HTTP ${resp.status}: ${t}`)
    }
    const blob = await resp.blob()
    // Prefer the server's Content-Disposition filename
    // (it knows the session id + title + timestamp) but
    // fall back to the client-side suggestion if the
    // header is missing for any reason.
    const cd = resp.headers.get('Content-Disposition') || ''
    const serverName = parseContentDispositionFilename(cd)
    const baseFilename = serverName || suggestFilename(title, format)
    const filename = dedupeFilename(baseFilename, (p) => recentlyExported.has(p))
    recentlyExported.add(filename)
    const savedPath = await saveBlobWithPicker(blob, filename, format)
    if (!savedPath) return
    message.success(`已导出到 ${savedPath}`)
    showExport.value = false
  } catch (e) {
    console.error('[export] failed:', e)
    message.error('导出失败: ' + (e instanceof Error ? e.message : String(e)))
  }
}

async function confirmExport() {
  if (!exportSessionId.value || exportSaving.value) return
  exportSaving.value = true
  try {
    await doExport(exportSessionId.value, exportFormat.value)
  } finally {
    exportSaving.value = false
  }
}

async function saveBlobWithPicker(blob: Blob, filename: string, format: ExportFormat): Promise<string> {
  try {
    const { SaveExportFile } = await import('../../wailsjs/go/main/App')
    const dataBase64 = await blobToBase64(blob)
    const path = await SaveExportFile(filename, format, dataBase64)
    return path || ''
  } catch (e) {
    // Browser preview has no Wails binding. Keep a fallback so
    // the web build remains usable outside the desktop shell.
    console.warn('[export] Wails save unavailable, falling back to browser download:', e)
    const url = URL.createObjectURL(blob)
    const a = document.createElement('a')
    a.href = url
    a.download = filename
    document.body.appendChild(a)
    a.click()
    document.body.removeChild(a)
    setTimeout(() => URL.revokeObjectURL(url), 1000)
    return filename
  }
}

function blobToBase64(blob: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onload = () => {
      const value = String(reader.result || '')
      const comma = value.indexOf(',')
      resolve(comma >= 0 ? value.slice(comma + 1) : value)
    }
    reader.onerror = () => reject(reader.error || new Error('read export blob failed'))
    reader.readAsDataURL(blob)
  })
}

// parseContentDispositionFilename pulls a usable
// filename out of a Content-Disposition header. Order
// of preference (per RFC 6266 / 5987):
//   1. `filename*=UTF-8''<percent-encoded>` — the
//      Unicode-aware form. Decoded, this is the
//      human-readable title. Browsers honour this in
//      preference to the plain `filename=` parameter.
//   2. `filename="..."` — always ASCII (the server
//      builds this from the session id). Safe
//      fallback for HTTP clients that don't decode
//      `filename*`.
// Returns the empty string on any parse failure — the
// caller falls back to the client-side suggestion.
function parseContentDispositionFilename(cd: string): string {
  if (!cd) return ''
  // Try the RFC 5987 form first. The charset is
  // hard-coded to UTF-8 because that's the only
  // value the server emits.
  const ext = /filename\*\s*=\s*UTF-8''([^;]+)/i.exec(cd)
  if (ext) {
    try {
      const decoded = decodeURIComponent(ext[1])
      if (decoded) return decoded
    } catch {
      // Malformed percent-encoding — fall through
      // to the plain form.
    }
  }
  const plain = /filename="([^"]+)"/.exec(cd)
  return plain ? plain[1] : ''
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

async function onAddProject() {
  if (!validateProjectForm()) return
  try {
    await api.addProject(newProjectName.value.trim(), newProjectPath.value.trim())
    await loadProjects()
    message.success('项目已添加')
    showAddProject.value = false
    newProjectName.value = ''
    newProjectPath.value = ''
  } catch (e: any) {
    const raw = e.message || '添加失败'
    if (raw.includes('already exists')) {
      projectPathError.value = '该目录已经在项目列表中'
      message.warning(projectPathError.value)
    } else if (raw.includes('absolute')) {
      projectPathError.value = '请输入绝对路径'
      message.warning(projectPathError.value)
    } else if (raw.includes('existing directory')) {
      projectPathError.value = '项目目录不存在或不是文件夹'
      message.warning(projectPathError.value)
    } else {
      message.error(raw)
    }
  }
}

async function pickDirectory() {
  try {
    const { path } = await api.pickFolder()
    if (path) {
      newProjectPath.value = path
      projectPathError.value = ''
      if (!newProjectName.value.trim()) {
        newProjectName.value = basenameFromPath(path)
        projectNameError.value = ''
      }
    }
  } catch (e: any) {
    message.error(e.message || '选取目录失败')
  }
}

function validateProjectForm() {
  const name = newProjectName.value.trim()
  const path = newProjectPath.value.trim()
  projectNameError.value = ''
  projectPathError.value = ''
  if (!name) {
    projectNameError.value = '项目名称必填'
  }
  if (!path) {
    projectPathError.value = '项目目录必填'
  } else if (!isAbsolutePath(path)) {
    projectPathError.value = '请输入绝对路径'
  } else if (state.projects.some(p => sameProjectPath(p.path, path))) {
    projectPathError.value = '该目录已经在项目列表中'
  }
  if (projectNameError.value || projectPathError.value) {
    message.warning(projectNameError.value || projectPathError.value)
    return false
  }
  return true
}

function isAbsolutePath(path: string) {
  const value = path.trim()
  return /^[a-zA-Z]:[\\/]/.test(value) || value.startsWith('\\\\') || value.startsWith('/')
}

function normalizeProjectPath(path: string) {
  return path.trim().replace(/[\\/]+$/, '').replace(/\//g, '\\').toLowerCase()
}

function sameProjectPath(a: string, b: string) {
  return normalizeProjectPath(a) === normalizeProjectPath(b)
}

function basenameFromPath(path: string) {
  const parts = path.trim().split(/[\\/]+/).filter(Boolean)
  return parts[parts.length - 1] || '新项目'
}

async function onRemoveProject(path: string) {
  try {
    await api.removeProject(path)
    await loadProjects()
    delete state.projectSessions[path]
    delete state.lastSessionByProject[path]
    if (state.activeProjectPath === path) {
      await setActiveProject('')
    }
    message.info('项目已移除')
  } catch (e: any) {
    message.error(e.message || '移除失败')
  }
}

const hasUpdateBlockingWork = computed(() =>
  Object.keys(state.streaming).length > 0 ||
  Object.values(state.sessionWorking).some(Boolean) ||
  Object.values(state.sessionBackgroundSubAgentJobs).some(count => count > 0) ||
  Object.values(state.sessionBackgroundHookMerging).some(Boolean),
)

function toggleTheme() {
  themeName.value = themeName.value === 'dark' ? 'light' : 'dark'
}

function versionLabel(value: string): string {
  if (!value) return 'v' + APP_VERSION
  return value.startsWith('v') || value.startsWith('V') ? value : `v${value}`
}

function updateSizeLabel(size?: number): string {
  if (!size || size <= 0) return ''
  const units = ['B', 'KB', 'MB', 'GB']
  let value = size
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value = value / 1024
    unit++
  }
  const digits = value >= 10 || unit === 0 ? 0 : 1
  return `${value.toFixed(digits)} ${units[unit]}`
}

function updatePackageSize(info: UpdateInfo): string {
  return updateSizeLabel(selectedUpdateArtifact(info)?.size || info.patch?.size || info.full?.size)
}

function selectedUpdateArtifact(info: UpdateInfo): UpdateArtifact | undefined {
  return info.artifact || info.patch || info.full
}

function selectedUpdateURL(info: UpdateInfo): string {
  return selectedUpdateArtifact(info)?.url || info.url || ''
}

function updateArtifactLabel(info: UpdateInfo): string {
  switch (selectedUpdateArtifact(info)?.kind) {
    case 'patch':
      return '差分更新包'
    case 'full':
      return '全量更新包'
    default:
      return '更新压缩包'
  }
}

function updateDownloadButtonText(info: UpdateInfo): string {
  return `下载${updateArtifactLabel(info)}`
}

function externalUpdateButtonText(info: UpdateInfo): string {
  return selectedUpdateArtifact(info)?.kind === 'full' ? '下载全量包' : '前往下载'
}

function updatePackageSummary(info: UpdateInfo): string {
  const size = updatePackageSize(info)
  const suffix = size ? ` · ${size}` : ''
  if (info.installable) return `自动更新将使用 ${updateArtifactLabel(info)}${suffix}`
  if (!info.patch?.url) return `更新源未返回当前版本可用的差分包，将提供全量包下载${suffix}`
  return `更新源返回的${updateArtifactLabel(info)}无法自动安装，将前往下载${suffix}`
}

function fullPackageURL(info: UpdateInfo): string {
  return info.full?.url || ''
}

function hasSeparateFullPackage(info: UpdateInfo): boolean {
  const full = fullPackageURL(info)
  return !!full && full !== selectedUpdateURL(info)
}

function updateErrorMessage(err: unknown): string {
  if (err instanceof Error && err.message) return err.message
  if (typeof err === 'string' && err) return err
  return '未知错误'
}

function notifyUpdateAvailable(info: UpdateInfo) {
  if (!info.hasUpdate) return
  const latest = info.latest || APP_VERSION
  try {
    if (localStorage.getItem(UPDATE_NOTIFY_KEY) === latest) return
    localStorage.setItem(UPDATE_NOTIFY_KEY, latest)
  } catch {
    // ignore
  }

  let close: (() => void) | null = null
  const fullPackageHint = hasSeparateFullPackage(info) ? '，也可下载全量包' : ''
  const notice = notification.info({
    title: '发现可用更新',
    content: `最新版本 ${versionLabel(latest)} 可以更新，关于页可下载${updateArtifactLabel(info)}${fullPackageHint}。`,
    duration: 8000,
    keepAliveOnHover: true,
    action: () => h(
      NButton,
      {
        size: 'small',
        type: 'primary',
        onClick: () => {
          close?.()
          openAbout()
        },
      },
      { default: () => '查看更新' },
    ),
  })
  close = () => notice.destroy()
}

function notifyDownloadedUpdate(update: UpdateDownloadResult) {
  const packageLabel = updateArtifactLabel(update)
  let close: (() => void) | null = null
  const notice = notification.success({
    title: `${packageLabel}已下载`,
    content: `${update.fileName || packageLabel} 已通过校验。点击重启后会自动替换为最新版本 ${versionLabel(update.latest)}。`,
    duration: 0,
    keepAliveOnHover: true,
    action: () => h(
      NButton,
      {
        size: 'small',
        type: 'primary',
        disabled: updateInstalling.value,
        onClick: () => {
          close?.()
          confirmRestartUpdate(update)
        },
      },
      {
        icon: () => h(RotateCw, { size: 14 }),
        default: () => '重启并更新',
      },
    ),
  })
  close = () => notice.destroy()
}

async function refreshUpdate(force = false, notify = false) {
  const info = await checkUpdate(force)
  if (!info) return
  updateInfo.value = info
  if (!info.hasUpdate || info.latest !== downloadedUpdate.value?.latest) {
    downloadedUpdate.value = null
  }
  if (notify) notifyUpdateAvailable(info)
}

async function openUpdateURL() {
  const info = updateInfo.value
  const url = info ? selectedUpdateURL(info) : ''
  if (!url) {
    message.warning('当前更新源没有提供下载地址')
    return
  }
  try {
    await api.openExternalURL(url)
  } catch (err) {
    message.error(`打开下载地址失败: ${updateErrorMessage(err)}`)
  }
}

async function openFullPackageURL() {
  const info = updateInfo.value
  const url = info ? fullPackageURL(info) : ''
  if (!url) {
    message.warning('当前更新源没有提供全量包下载地址')
    return
  }
  try {
    await api.openExternalURL(url)
  } catch (err) {
    message.error(`打开全量包下载地址失败: ${updateErrorMessage(err)}`)
  }
}

async function openReleasePage() {
  try {
    await api.openExternalURL(SOFTWARE_RELEASE_PAGE)
  } catch (err) {
    message.error(`打开软件发布页失败: ${updateErrorMessage(err)}`)
  }
}

async function onDownloadUpdate() {
  const info = updateInfo.value
  if (!info?.hasUpdate) return
  if (!info.installable) {
    await openUpdateURL()
    return
  }

  updateDownloading.value = true
  try {
    const result = await downloadUpdate()
    downloadedUpdate.value = result
    updateInfo.value = result
    notifyDownloadedUpdate(result)
  } catch (err) {
    message.error(`下载更新失败: ${updateErrorMessage(err)}`)
  } finally {
    updateDownloading.value = false
  }
}

function confirmRestartUpdate(update = downloadedUpdate.value) {
  if (!update) return
  const busyText = hasUpdateBlockingWork.value
    ? '当前仍有会话或后台任务运行，重启会中断这些任务。'
    : ''
  dialog.warning({
    title: '重启并更新 P-Chat',
    content: `${update.fileName || updateArtifactLabel(update)} 已下载并通过校验。${busyText}是否现在重启并替换为最新版本 ${versionLabel(update.latest)}？`,
    positiveText: '立即重启并更新',
    negativeText: '稍后',
    onPositiveClick: () => installUpdateNow(update),
  })
}

async function installUpdateNow(update = downloadedUpdate.value) {
  if (!update) return
  updateInstalling.value = true
  try {
    await installDownloadedUpdate(update)
    message.info('正在重启并更新 P-Chat')
  } catch (err) {
    updateInstalling.value = false
    message.error(`启动更新失败: ${updateErrorMessage(err)}`)
  }
}

function openAbout() {
  showAbout.value = true
  void refreshUpdate()
}

const projectSwitcherRef = ref<InstanceType<typeof ProjectSwitcher> | null>(null)

function openProjectSwitcher() {
  projectSwitcherRef.value?.open()
}

function openAddProject() {
  showAddProject.value = true
}

defineExpose({
  openProjectSwitcher,
  openAddProject,
  openAbout,
})

onMounted(() => {
  void refreshUpdate(false, true)
})
</script>

<template>
  <aside class="sidebar">
    <section class="session-panel" aria-label="会话">
      <!-- Project switcher + theme / app actions. -->
      <div class="sidebar-header">
        <ProjectSwitcher
          ref="projectSwitcherRef"
          @add-project="showAddProject = true"
        />
        <div class="sidebar-actions">
          <NButton size="small" quaternary @click="toggleTheme" :title="themeName === 'dark' ? '切换到浅色主题' : '切换到深色主题'" aria-label="切换主题">
            <component :is="themeName === 'dark' ? Sun : Moon" :size="16" />
          </NButton>
          <NDropdown
            trigger="click"
            placement="bottom-end"
            size="small"
            :options="menuOptions"
            :menu-props="actionMenuProps"
            @select="(key) => handleMenuSelect(String(key))"
          >
            <NButton size="small" quaternary title="更多" aria-label="更多">
              <MoreHorizontal :size="16" />
            </NButton>
          </NDropdown>
        </div>
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

      <!-- New session CTA: pinned to the top so it's always one click away. -->
      <div class="new-session-bar">
        <button class="new-session-btn" @click="onNew" aria-label="新建对话">
          <Plus :size="14" />
          <span>新建对话</span>
        </button>
      </div>

      <NScrollbar class="session-scroll">
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
                    <span
                      v-if="sessionSourceFromID(s.id)"
                      class="item-source-badge"
                      :class="`item-source-badge--${sessionSourceFromID(s.id)?.platform}`"
                      :title="sessionSourceTitle(s.id)"
                      :aria-label="sessionSourceTitle(s.id)"
                    >
                      <MessageSquare :size="10" />
                      <span>{{ sessionSourceFromID(s.id)?.label }}</span>
                    </span>
                    <span class="item-title">{{ sessionDisplayTitle(s) }}</span>
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
                      size="small"
                      :options="sessionMenuOptions(s.id)"
                      :menu-props="actionMenuProps"
                      @select="(key) => onSessionMenu(String(key), s.id)"
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

      <!-- Footer brand card: app version + About. Settings lives in AppRail. -->
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
      </div>
    </section>

    <!-- Modals (PR #7: migrated to AppModal for consistent glass chrome). -->

    <AppModal
      v-model:show="showAddProject"
      title="新建项目"
      size="md"
    >
      <div class="add-project-form">
        <p class="add-project-lead">
          选择一个本地项目根目录，后续会话会自动归入该项目空间。
        </p>
        <div class="form-field">
          <label for="add-project-path">项目路径</label>
          <div class="path-row">
            <NInput
              id="add-project-path"
              v-model:value="newProjectPath"
              size="small"
              placeholder="例如：D:\Workspace\P-Chat"
              class="path-input"
              :status="projectPathError ? 'error' : undefined"
              @update:value="projectPathError = ''"
              @keyup.enter="onAddProject"
            />
            <NButton
              size="small"
              secondary
              class="path-browse"
              title="选择目录"
              @click="pickDirectory"
            >
              <template #icon>
                <FolderOpen :size="14" />
              </template>
              浏览
            </NButton>
          </div>
          <p v-if="projectPathError" class="field-error">{{ projectPathError }}</p>
          <p v-else class="field-hint">选择项目的根目录（含源代码的那一层）</p>
        </div>
        <div class="form-field">
          <label for="add-project-name">项目名称</label>
          <NInput
            id="add-project-name"
            v-model:value="newProjectName"
            size="small"
            placeholder="输入项目名称"
            :status="projectNameError ? 'error' : undefined"
            @update:value="projectNameError = ''"
            @keyup.enter="onAddProject"
          />
          <p v-if="projectNameError" class="field-error">{{ projectNameError }}</p>
        </div>
        <div class="project-create-notes" aria-label="项目创建说明">
          <div class="project-create-note">
            <Circle :size="8" />
            <span>仅登记本地目录，不修改项目文件</span>
          </div>
          <div class="project-create-note">
            <Circle :size="8" />
            <span>存在 AGENTS.md 时，会在项目会话中作为上下文读取</span>
          </div>
        </div>
      </div>
      <template #footer>
        <NButton size="small" quaternary @click="showAddProject = false">取消</NButton>
        <NButton size="small" type="primary" @click="onAddProject">新建项目</NButton>
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
        <NButton size="small" quaternary @click="showConfirmDeleteSession = false">取消</NButton>
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
        <NButton size="small" quaternary @click="showConfirmDeleteProject = false">取消</NButton>
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
        <NButton size="small" quaternary @click="showRename = false">取消</NButton>
        <NButton size="small" type="primary" @click="confirmRename">确认</NButton>
      </template>
    </AppModal>

    <AppModal
      v-model:show="showExport"
      title="导出对话"
      size="md"
      accent-top
    >
      <div class="export-dialog">
        <div class="export-session">
          <span class="export-session-label">会话</span>
          <strong>{{ exportTarget?.title || '未命名会话' }}</strong>
          <code>{{ exportFilename }}</code>
        </div>
        <div class="export-format-grid">
          <button
            type="button"
            class="export-format-card"
            :class="{ active: exportFormat === 'pdf' }"
            @click="exportFormat = 'pdf'"
          >
            <span class="export-format-icon"><File :size="18" /></span>
            <span class="export-format-main">
              <strong>PDF</strong>
              <span>分页归档，适合发送和长期保存</span>
            </span>
          </button>
          <button
            type="button"
            class="export-format-card"
            :class="{ active: exportFormat === 'html' }"
            @click="exportFormat = 'html'"
          >
            <span class="export-format-icon"><FileText :size="18" /></span>
            <span class="export-format-main">
              <strong>HTML</strong>
              <span>保留更完整样式，适合浏览器查看和打印</span>
            </span>
          </button>
        </div>
      </div>
      <template #footer>
        <NButton size="small" quaternary :disabled="exportSaving" @click="showExport = false">取消</NButton>
        <NButton size="small" type="primary" :loading="exportSaving" @click="confirmExport">选择路径并保存</NButton>
      </template>
    </AppModal>

    <AppModal
      v-model:show="showAbout"
      title="关于 P-Chat"
      size="sm"
    >
      <div class="about-body">
        <p class="about-name">P-Chat</p>
        <p class="about-version">版本 {{ versionLabel(updateInfo?.current || APP_VERSION) }}</p>
        <p class="about-desc">对话式 AI Agent · CLI / HTTP / 桌面端三端同源</p>
        <p class="about-desc">Go + Vue 3 + Vite + SQLite · Wails v2</p>
        <p class="about-desc">OpenAI / Anthropic 双协议 · ReAct 工具调用循环</p>

        <template v-if="updateInfo">
          <div v-if="updateInfo.hasUpdate" class="update-banner">
            <NTag type="warning" size="small">发现新版本</NTag>
            <p>最新版本 <strong>{{ versionLabel(updateInfo.latest) }}</strong></p>
            <p class="update-body" v-if="updateInfo.body">{{ updateInfo.body }}</p>
            <p class="update-meta">{{ updatePackageSummary(updateInfo) }}</p>
            <p class="update-meta" v-if="hasSeparateFullPackage(updateInfo)">
              也可下载全量包，或打开软件发布页选择需要的软件包。
            </p>
            <p class="update-meta" v-if="downloadedUpdate">
              已下载 {{ downloadedUpdate.fileName || updateArtifactLabel(downloadedUpdate) }}，重启后会自动替换为最新版本。
            </p>
            <div class="update-actions">
              <NButton
                v-if="downloadedUpdate"
                size="small"
                type="primary"
                :loading="updateInstalling"
                :disabled="updateDownloading"
                @click="confirmRestartUpdate()"
              >
                <template #icon><RotateCw :size="14" /></template>
                立即重启并更新
              </NButton>
              <NButton
                v-else-if="updateInfo.installable"
                size="small"
                type="primary"
                :loading="updateDownloading"
                :disabled="updateInstalling"
                @click="onDownloadUpdate"
              >
                <template #icon><Download :size="14" /></template>
                {{ updateDownloadButtonText(updateInfo) }}
              </NButton>
              <NButton
                v-else
                size="small"
                type="primary"
                @click="openUpdateURL"
              >
                <template #icon><ExternalLink :size="14" /></template>
                {{ externalUpdateButtonText(updateInfo) }}
              </NButton>
              <NButton
                v-if="hasSeparateFullPackage(updateInfo)"
                size="small"
                secondary
                @click="openFullPackageURL"
              >
                <template #icon><ExternalLink :size="14" /></template>
                下载全量包
              </NButton>
              <NButton
                size="small"
                quaternary
                @click="openReleasePage"
              >
                <template #icon><Globe :size="14" /></template>
                软件发布页
              </NButton>
            </div>
          </div>
          <p v-else class="update-ok">当前已是最新版本 {{ versionLabel(updateInfo.current || APP_VERSION) }}</p>
        </template>
        <p v-else class="update-ok">正在检查更新…</p>

        <button type="button" class="about-docs" @click="openDocs">
          <Globe :size="14" class="about-docs-icon" />
          查看使用文档
        </button>

        <div class="about-links">
          <a :href="'https://github.com/' + GITHUB_REPO" target="_blank">GitHub</a>
          <span class="sep">·</span>
          <a :href="'https://github.com/' + GITHUB_REPO + '/issues'" target="_blank">反馈问题</a>
        </div>
      </div>
    </AppModal>

    <TokenStatsModal v-model:show="showTokenStats" />
  </aside>
</template>

<style scoped>
.sidebar {
  width: var(--sidebar-width);
  min-width: 0;
  background: var(--surface-1);
  border-right: 1px solid var(--border-subtle);
  display: flex;
  flex-shrink: 0;
  overflow: hidden;
  transition: width var(--dur-slow) var(--ease-in-out),
              border-color var(--dur-slow) var(--ease-in-out);
}
.sidebar.sidebar-collapsed {
  width: 0;
  border-right-color: transparent;
  pointer-events: none;
}

/* --- Session panel + header ------------------------------------------- */
.session-panel {
  min-width: 0;
  flex: 1;
  display: flex;
  flex-direction: column;
  background: var(--surface-1);
}
.sidebar-header {
  padding: var(--space-3);
  display: flex;
  justify-content: space-between;
  align-items: stretch;
  gap: var(--space-2);
  flex-shrink: 0;
  border-bottom: 1px solid var(--border-subtle);
}
.sidebar-actions {
  flex: 0 0 auto;
  display: flex;
  align-items: center;
  justify-content: flex-end;
  gap: var(--space-1);
  white-space: nowrap;
}
.sidebar-actions :deep(.n-button) {
  flex: 0 0 auto;
}

/* --- Search bar -------------------------------------------------------- */
.search-bar {
  padding: var(--space-3) var(--space-3) var(--space-2);
  border-bottom: 1px solid var(--border-subtle);
  flex-shrink: 0;
}
.search-bar :deep(.search-icon) { color: var(--text-tertiary); }

/* --- New session CTA (top, full width) -------------------------------- */
.new-session-bar {
  padding: var(--space-2) var(--space-3) var(--space-3);
  flex-shrink: 0;
}
.new-session-btn {
  width: 100%;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  gap: var(--space-2);
  padding: var(--space-2) var(--space-3);
  min-height: var(--control-height);
  /* Calm outline CTA — avoid large saturated brand fill */
  background: var(--surface-2);
  color: var(--text-primary);
  border: 1px solid var(--border-default);
  border-radius: var(--radius-md);
  font-size: 13px;
  font-weight: 500;
  cursor: pointer;
  transition:
    background var(--dur-fast) var(--ease-out),
    border-color var(--dur-fast) var(--ease-out),
    color var(--dur-fast) var(--ease-out);
}
.new-session-btn:hover {
  background: color-mix(in srgb, var(--brand-500) 8%, var(--surface-2));
  border-color: color-mix(in srgb, var(--brand-500) 28%, var(--border-default));
  color: var(--brand-600);
}
.new-session-btn:active {
  background: color-mix(in srgb, var(--brand-500) 12%, var(--surface-2));
}

/* --- Session list (grouped) ------------------------------------------- */
.session-scroll {
  flex: 1;
  min-height: 0;
}
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
  transition: background var(--dur-fast) var(--ease-out);
}
.session-item:hover { background: var(--surface-3); }
.session-item.active {
  background: color-mix(in srgb, var(--brand-500) 8%, var(--surface-1));
}
.session-item.active:hover {
  background: color-mix(in srgb, var(--brand-500) 12%, var(--surface-1));
}

.item-row {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: var(--space-2) var(--space-3);
  min-height: 36px;
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
.item-source-badge {
  display: inline-flex;
  align-items: center;
  gap: var(--space-1);
  flex-shrink: 0;
  padding: 1px var(--space-1);
  border-radius: var(--radius-sm);
  background: var(--brand-50);
  color: var(--brand-600);
  font-size: 10px;
  font-weight: 600;
  line-height: 1.2;
}
.item-source-badge svg {
  flex-shrink: 0;
}
.item-source-badge--wechat {
  background: var(--success-50);
  color: var(--success-500);
}
.item-title {
  min-width: 0;
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
  letter-spacing: 0;
}
.session-item.active .item-time { color: var(--text-secondary); }
.item-menu-btn {
  width: 22px;
  height: 22px;
  background: transparent;
  border: 1px solid transparent;
  color: var(--text-tertiary);
  cursor: pointer;
  padding: 0;
  border-radius: var(--radius-sm);
  display: inline-flex;
  align-items: center;
  justify-content: center;
  opacity: 0;
  transition:
    opacity var(--dur-fast) var(--ease-out),
    background var(--dur-fast) var(--ease-out),
    border-color var(--dur-fast) var(--ease-out),
    color var(--dur-fast) var(--ease-out);
}
.session-item:hover .item-menu-btn,
.item-menu-btn:focus-visible {
  opacity: 1;
}
.item-menu-btn:hover {
  background: var(--surface-2);
  border-color: var(--border-default);
  color: var(--text-primary);
}
.item-menu-btn:active {
  background: var(--brand-50);
  border-color: var(--brand-100);
  color: var(--brand-600);
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

/* Session / project NDropdowns teleport to <body>; their layout
 * lives on the shared `.app-action-menu` class in style.css. */

/* --- Search results --------------------------------------------------- */
.search-results { padding: 8px; }
.search-result-item {
  padding: var(--space-2) var(--space-3);
  margin-bottom: var(--space-2);
  border-radius: var(--radius-sm);
  cursor: pointer; background: var(--surface-2); transition: background var(--dur-fast) var(--ease-out);
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
  flex: 1;
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
.add-project-form {
  display: flex;
  flex-direction: column;
  gap: var(--space-5);
}
.add-project-lead {
  margin: 0;
  color: var(--text-secondary);
  font-size: 12.5px;
  line-height: 1.5;
}
.form-field {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
  min-width: 0;
}
.form-field label {
  color: var(--text-secondary);
  font-size: 12px;
  font-weight: 600;
  letter-spacing: 0;
}
.form-field :deep(.n-input) {
  --n-height: var(--control-height);
  --n-font-size: 13px;
  --n-border-radius: var(--radius-sm);
}
.path-row {
  display: flex;
  gap: var(--space-2);
  align-items: center;
  min-width: 0;
}
.path-input { flex: 1; min-width: 0; }
.path-browse {
  flex-shrink: 0;
  height: var(--control-height) !important;
  padding: 0 var(--space-3);
}
.project-create-notes {
  display: grid;
  gap: var(--space-2);
  padding: var(--space-3);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-md);
  background: color-mix(in srgb, var(--surface-2) 82%, transparent);
}
.project-create-note {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  min-width: 0;
  color: var(--text-secondary);
  font-size: 12px;
  line-height: 1.45;
}
.project-create-note svg {
  flex: 0 0 auto;
  color: var(--brand-500);
  fill: currentColor;
}
.field-hint {
  margin: 0;
  color: var(--text-tertiary);
  font-size: 11.5px;
  line-height: 1.4;
}
.field-error {
  margin: 0;
  color: var(--error-500);
  font-size: 12px;
  line-height: 1.35;
}
.export-dialog {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
}
.export-session {
  display: grid;
  gap: var(--space-1);
  padding: var(--space-3);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-md);
  background: color-mix(in srgb, var(--surface-2) 80%, transparent);
}
.export-session-label {
  color: var(--text-tertiary);
  font-size: 11px;
  font-weight: 600;
  letter-spacing: 0.02em;
  text-transform: uppercase;
}
.export-session strong {
  color: var(--text-primary);
  font-size: 13px;
  line-height: 1.35;
  overflow-wrap: anywhere;
}
.export-session code {
  color: var(--text-secondary);
  font-family: var(--font-mono);
  font-size: 11px;
  line-height: 1.45;
  overflow-wrap: anywhere;
}
.export-format-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: var(--space-3);
}
.export-format-card {
  display: flex;
  align-items: flex-start;
  gap: var(--space-3);
  min-width: 0;
  padding: var(--space-3);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-md);
  background: var(--surface-1);
  color: var(--text-secondary);
  text-align: left;
  cursor: pointer;
  transition: var(--transition-colors),
              border-color var(--dur-fast) var(--ease-out),
              box-shadow var(--dur-fast) var(--ease-out);
}
.export-format-card:hover {
  background: var(--surface-2);
  color: var(--text-primary);
  border-color: var(--border-default);
}
.export-format-card.active {
  border-color: color-mix(in srgb, var(--brand-500) 45%, var(--border-subtle));
  background: color-mix(in srgb, var(--brand-50) 75%, var(--surface-1));
  color: var(--text-primary);
  box-shadow: inset 0 0 0 1px color-mix(in srgb, var(--brand-500) 18%, transparent);
}
.export-format-icon {
  width: 32px;
  height: 32px;
  flex: 0 0 32px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border-radius: var(--radius-md);
  background: var(--surface-2);
  color: var(--brand-500);
}
.export-format-card.active .export-format-icon {
  background: var(--brand-100);
}
.export-format-main {
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
}
.export-format-main strong {
  color: var(--text-primary);
  font-size: 13px;
  line-height: 1.3;
}
.export-format-main span {
  color: var(--text-tertiary);
  font-size: 11.5px;
  line-height: 1.45;
}
@media (max-width: 520px) {
  .export-format-grid {
    grid-template-columns: 1fr;
  }
}
.about-body { padding: 2px 0; }
.about-name {
  font-size: 17px;
  font-weight: 650;
  letter-spacing: 0;
  margin: 0 0 2px;
  color: var(--text-primary);
}
.about-version { font-size: 12.5px; color: var(--text-tertiary); margin: 0 0 10px; }
.about-desc { font-size: 12.5px; color: var(--text-secondary); margin: 0 0 3px; line-height: 1.45; }
.update-banner {
  margin: var(--space-3) 0;
  padding: var(--space-3);
  background: color-mix(in srgb, var(--warn-50) 80%, transparent);
  border: 1px solid color-mix(in srgb, var(--warn-500) 35%, var(--border-subtle));
  border-radius: var(--radius-md);
}
.update-banner p { margin: var(--space-1) 0; font-size: 13px; }
.update-body { color: var(--text-tertiary); font-size: 12px !important; max-height: 100px; overflow: auto; white-space: pre-wrap; }
.update-meta { color: var(--text-tertiary); font-size: 12px !important; }
.update-actions {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
  margin-top: var(--space-2);
}
.update-ok { font-size: 12.5px; color: var(--text-tertiary); margin: var(--space-3) 0; }

/* Docs CTA — secondary glass chip, not a heavy solid bar. */
.about-docs {
  display: flex;
  align-items: center;
  justify-content: center;
  gap: 6px;
  width: 100%;
  margin: 12px 0 0;
  padding: 0 var(--space-3);
  height: var(--control-height);
  background: color-mix(in srgb, var(--brand-50) 70%, transparent);
  color: var(--brand-600);
  border: 1px solid color-mix(in srgb, var(--brand-500) 28%, transparent);
  border-radius: var(--radius-md);
  font-size: 12.5px;
  font-weight: 550;
  cursor: pointer;
  transition: var(--transition-colors);
}
.about-docs:hover {
  background: var(--brand-50);
  color: var(--brand-700, var(--brand-600));
  border-color: color-mix(in srgb, var(--brand-500) 45%, transparent);
}
.about-docs:focus-visible {
  outline: 2px solid var(--brand-500);
  outline-offset: 1px;
}
.about-docs-icon { flex-shrink: 0; }

.about-links {
  margin-top: 14px;
  padding-top: 12px;
  border-top: 1px solid var(--border-subtle);
  font-size: 12.5px;
}
.about-links a { color: var(--brand-500); text-decoration: none; }
.about-links a:hover { text-decoration: underline; }
.about-links .sep { color: var(--text-tertiary); margin: 0 6px; }
</style>
