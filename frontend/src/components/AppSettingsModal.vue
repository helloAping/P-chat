<script setup lang="ts">
// App-level (software) settings. Split into two tabs by user request:
//
//   1. LLM 提供商 — provider / model / API key CRUD
//   2. 风格配置   — built-in + user-added style CRUD
//
// Providers tab is a left/right split:
//   - Left column: provider list (name + protocol + model count +
//     default tag). Click to select. "+" adds a new provider.
//   - Right column: detail pane for the selected provider. Shows
//     a top "basic" form (name / protocol / base_url / api_key /
//     default toggle) and a bottom model table (per-model
//     display_name / context / max_tokens / capabilities).
//     Each model row has edit / set-default / delete actions; the
//     table footer has a "+ 添加模型" button.
//
// The form is intentionally a single form covering the whole
// provider — name and protocol are editable on existing entries
// (the unified PATCH endpoint supports renames and protocol
// switches). The "保存" button only sends the fields the user
// actually changed, mirroring the backend's "non-empty means
// write, otherwise leave alone" contract.

import { computed, onMounted, ref, watch } from 'vue'
import {
  NModal, NCard, NSelect, NButton, NSpace, NInput, NInputNumber, NSwitch,
  NTag, NTabs, NTabPane, NDataTable, NPopconfirm, NTooltip, NIcon, NPopover, useMessage,
} from 'naive-ui'
import * as api from '../api/client'
import { loadProviders, loadSessions } from '../stores/chat'
import type { Session } from '../api/client'

const message = useMessage()

// ---- 风格样例模板 ----
const EXAMPLE_SCENARIOS = ['编程', '日常工作', '角色扮演'] as const

const EXAMPLE_TEMPLATES: Record<string, Record<string, string>> = {
  prompt: {
    '编程': `# 编程助手

你是 P-Chat，一个本地 AI 编程助手。

## 人设
- 精通 Go / Vue 3 / TypeScript / SQLite
- 可以读写文件、执行 shell 命令
- 仅操作工作目录内的文件

## 性格
- 简洁、直击要害，像 code review 评论
- 不使用「当然可以」「没问题」等寒暄
- 先给答案，再给解释

## 格式
- 代码块用 \`\`\`go 标注语言
- 路径用反引号 \`internal/agent/agent.go\`
- 错误用中文描述，不贴原始 stack trace

## 行为准则
- 先读代码再给建议，不凭空猜测
- 修改后建议跑测试验证
- 不输出密钥 / token 到任何地方`,
    '日常工作': `# 办公助手

你是 P-Chat，一个全能办公助手。

## 人设
- 撰写邮件、报告、文档
- 整理会议纪要、任务清单
- 分析数据、制作摘要
- 搜索本地文件、管理知识库

## 性格
- 专业但不冰冷，像靠谱的同事
- 适当使用「建议」「推荐」等柔和词汇
- 主动追问不清晰的指令

## 格式
- 要点用 - 列表
- 关键信息加 **粗体**
- 重要结论写在最前面

## 行为准则
- 回复结构清晰，善用标题和列表
- 主动提醒遗漏的事项
- 对不确定的信息标注"待确认"`,
    '角色扮演': `# 小灵

你是小灵，用户的私人 AI 助理。

## 人设
- 活泼开朗，偶尔俏皮
- 对技术问题也保持耐心和热情
- 像一个靠谱又有趣的朋友

## 性格
- 温暖、随和，像深夜聊天的朋友
- 偶尔用「哈哈」「~」等轻松表达
- 先共情再建议

## 格式
- 用自然段对话，不用列表
- 技术内容用比喻解释
- 回复长度适中，不写小作文

## 行为准则
- 管理用户的日程和任务
- 帮用户学习和研究新技术
- 在用户焦虑时给予鼓励`,
  },
  memory: {
    '编程': `- 当前项目：P-Chat (Go + Vue 3 桌面聊天应用)
- 数据库：SQLite @ ~/.p-chat/memory/store.db
- 后端端口：动态分配，默认随机
- 测试命令：go test -count=1 ./...
- 构建命令：task build:all
- 代码风格：Go camelCase, TS camelCase, 中文注释`,
    '日常工作': `- 我负责的产品线：用户增长与留存
- 团队规模：6 人（2 后端 + 2 前端 + 1 设计 + 1 PM）
- 本周重点：Q3 OKR 评审 & 新版首页 A/B
- 常用文档：docs/内部 Wiki / Notion
- 会议时间：每天 10:00 站会`,
    '角色扮演': `- 用户喜欢在晚上 9 点后专心工作
- 用户最近在学习 Rust 和系统编程
- 用户喜欢简洁的 UI 和 dark theme
- 用户的理想早餐是可颂 + 冰美式
- 用户这周压力比较大，需要鼓励`,
  },
}

const exampleActiveScene = ref<Record<string, string>>({
  prompt: '编程',
  memory: '编程',
})

function fillExample(field: 'prompt' | 'memory') {
  const scene = exampleActiveScene.value[field]
  const tpl = EXAMPLE_TEMPLATES[field]?.[scene] || ''
  if (field === 'prompt') newStylePrompt.value = tpl
  else if (field === 'memory') newStyleMemory.value = tpl
}

// ID 唯一性校验（仅在新增时检查，编辑时 ID 只读）
const idConflict = computed(() => {
  if (isEdit.value) return false
  const v = newStyleId.value.trim()
  if (!v) return false
  return styles.value.some(s => s.id === v)
})
const tab = ref<'providers' | 'styles' | 'archive' | 'skills' | 'mcp'>('providers')

// --- Provider state ---
const providers = ref<api.ProviderInfo[]>([])
const selectedName = ref<string | null>(null)
const selected = computed(() =>
  providers.value.find(p => p.name === selectedName.value) || null,
)

// Edit form (top of right pane). Mirrors the unified PATCH body.
const editName = ref('')
const editProtocol = ref<'openai' | 'anthropic'>('openai')
const editBaseURL = ref('')
const editAPIKey = ref('')
const editIsDefault = ref(false)

// Track which fields the user actually touched so we only PATCH
// the dirty ones. The backend treats non-empty values as "set"
// and empty values as "leave alone", so this list is the
// source of truth for the request body.
const dirty = ref<Set<string>>(new Set())

// Add-provider form
const showAddProvider = ref(false)
const newName = ref('')
const newProtocol = ref<'openai' | 'anthropic'>('openai')
const newBaseURL = ref('')
const newAPIKey = ref('')
const newModel = ref('')

// Add-model form
const showAddModel = ref(false)

// Single model form backing the add/edit panel. The form
// template binds to these refs regardless of which mode
// (`editingModelName` set = edit, otherwise = add). This
// keeps the form's `v-model` wiring trivial and guarantees
// that opening edit mode actually populates the visible
// fields (the previous code had a parallel `newModel*` ref
// family the form was wired to, so clicking "edit" did
// nothing on screen — values were silently written to the
// unused `editModel*` family).
const editingModelName = ref<string | null>(null)
const editModelName = ref('')
const editModelDisplay = ref('')
const editModelCtx = ref<number | null>(null)
const editModelOut = ref<number | null>(null)
const editModelVision = ref(false)

// Upstream models
const showUpstreamModels = ref(false)
const upstreamModels = ref<api.UpstreamModelItem[]>([])
const fetchingUpstream = ref(false)
const upstreamError = ref('')

// --- Style state ---
const styles = ref<api.StyleInfo[]>([])
const showAddStyle = ref(false)
const editingStyle = ref<api.StyleDetail | null>(null)
const newStyleId = ref('')
const newStyleLabel = ref('')
const newStylePrompt = ref('')
const newStyleMemory = ref('')
const isEdit = ref(false)

onMounted(async () => {
  await refresh()
})

watch(tab, (v) => {
  if (v === 'archive' && !archivedSessions.value.length) {
    loadArchived()
  } else if (v === 'skills') {
    refreshSkills()
    refreshRepos()
  } else if (v === 'mcp') {
    refreshMCP()
  }
})

async function refresh() {
  await Promise.all([refreshProviders(), refreshStyles()])
}

async function refreshProviders() {
  try {
    const p = await api.listProviders()
    providers.value = p.providers || []
    // If nothing is selected yet, pick the first one so the
    // right pane is never empty.
    if (!selectedName.value && providers.value.length > 0) {
      selectProvider(providers.value[0].name)
    } else if (selectedName.value && !providers.value.find(x => x.name === selectedName.value)) {
      // The selected provider just got deleted; fall back to
      // the first remaining one (or nothing).
      selectedName.value = providers.value[0]?.name ?? null
      if (selectedName.value) hydrateEditForm(selectedName.value)
    }
  } catch (e: any) {
    message.error(`加载 providers 失败: ${e.message}`)
  }
  // Also refresh the chat store so active sessions pick up
  // capability changes (e.g. toggling vision support) without
  // needing to close and reopen the chat.
  loadProviders()
}

async function refreshStyles() {
  try {
    const r = await api.getStyles()
    styles.value = r.styles || []
  } catch (e: any) {
    message.error(`加载 styles 失败: ${e.message}`)
  }
}

// selectProvider switches the right pane to `name`. The
// slim list endpoint doesn't carry base_url / api_key (those
// are secrets kept off the wire for the list view), so we
// also fetch the rich per-provider view (GET
// /api/v1/providers/:name) and hydrate the form from that
// — otherwise the user would see empty Base URL / API Key
// fields even though the server has values for them.
async function selectProvider(name: string) {
  selectedName.value = name
  // First, paint the form with whatever the slim list view
  // already has (name / protocol / is_default) so the
  // right pane doesn't show a flash of "← 选择左侧" while
  // the rich request is in flight.
  hydrateEditForm(name)
  try {
    const rich = await api.getProvider(name)
    // Update the providers cache so the model table also
    // gets the full per-model settings.
    const idx = providers.value.findIndex(x => x.name === name)
    if (idx >= 0) providers.value[idx] = rich
    // Now re-hydrate the form with the rich view (which
    // carries base_url + api_key).
    hydrateEditForm(name, rich)
  } catch (e: any) {
    message.error(`加载 provider "${name}" 失败: ${e.message}`)
  }
}

// hydrateEditForm copies the server-resolved fields into the
// edit form. dirty is reset so an out-of-band server change
// doesn't get clobbered. The second `p` argument lets the
// caller pass the rich per-provider view (which has
// base_url + api_key); if omitted, we fall back to whatever
// is in the providers cache.
function hydrateEditForm(name: string, p?: api.ProviderInfo) {
  const src = p || providers.value.find(x => x.name === name)
  if (!src) return
  editName.value = src.name
  editProtocol.value = (src.protocol as 'openai' | 'anthropic') || 'openai'
  editBaseURL.value = src.base_url || ''
  editAPIKey.value = src.api_key || ''
  editIsDefault.value = !!src.is_default
  dirty.value = new Set()
}

function markDirty(field: string) {
  dirty.value.add(field)
}

function resetDirty() { dirty.value = new Set() }

// --- Provider handlers ---

async function onAddProvider() {
  if (!newName.value.trim() || !newProtocol.value || !newModel.value.trim()) {
    message.warning('名称、协议、模型为必填')
    return
  }
  try {
    const addedName = newName.value.trim()
    await api.addProvider({
      name: addedName,
      protocol: newProtocol.value,
      base_url: newBaseURL.value.trim(),
      api_key: newAPIKey.value.trim(),
      model: newModel.value.trim(),
    })
    message.success('已添加')
    showAddProvider.value = false
    newName.value = ''; newBaseURL.value = ''; newAPIKey.value = ''; newModel.value = ''
    await refreshProviders()
    await selectProvider(addedName)
  } catch (e: any) {
    message.error(`添加失败: ${e.message}`)
  }
}

async function onDeleteProvider(name: string) {
  try {
    await api.deleteProvider(name)
    message.success('已删除')
    await refreshProviders()
  } catch (e: any) {
    message.error(`删除失败: ${e.message}`)
  }
}

async function onSaveProvider() {
  if (!selected.value) return
  const name = selected.value.name
  if (dirty.value.size === 0) {
    message.info('没有改动')
    return
  }
  // Build a PATCH body with only the dirty fields. The server
  // treats empty strings as "leave alone" for every field
  // except api_key, which we always send when the user
  // touched the form (even if they only "re-typed" the same
  // value — the user explicitly edited the field, so we
  // honour that). The rename is a separate concern.
  const body: api.UpdateProviderRequest = {}
  if (dirty.value.has('name') && editName.value.trim() && editName.value.trim() !== name) {
    body.name = editName.value.trim()
  }
  if (dirty.value.has('protocol')) {
    body.protocol = editProtocol.value
  }
  if (dirty.value.has('base_url')) {
    body.base_url = editBaseURL.value.trim()
  }
  if (dirty.value.has('api_key')) {
    body.api_key = editAPIKey.value
  }
  if (dirty.value.has('is_default') && editIsDefault.value) {
    body.set_default = true
  }
  try {
    const updated = await api.updateProvider(name, body)
    message.success('已保存')
    await refreshProviders()
    // If renamed, the new name is in the response; select it
    // so the user can keep editing. selectProvider also
    // fetches the rich per-provider view, which re-hydrates
    // the form with the up-to-date base_url / api_key.
    await selectProvider(updated.name)
  } catch (e: any) {
    message.error(`保存失败: ${e.message}`)
  }
}

// --- Model handlers ---

// Reset the model form to a blank "add" state.
function resetModelForm() {
  editingModelName.value = null
  editModelName.value = ''
  editModelDisplay.value = ''
  editModelCtx.value = null
  editModelOut.value = null
  editModelVision.value = false
}

function onShowAddModel() {
  resetModelForm()
  showAddModel.value = !showAddModel.value
}

async function onAddModel() {
  if (!selected.value) return
  const name = editModelName.value.trim()
  if (!name) {
    message.warning('模型名称为必填')
    return
  }
  const providerName = selected.value.name
  try {
    await api.addModel(providerName, {
      name,
      display_name: editModelDisplay.value.trim() || undefined,
      max_tokens_context: editModelCtx.value ?? undefined,
      max_tokens_output: editModelOut.value ?? undefined,
    })
    // The capabilities block is a separate PATCH; if it
    // fails, the model is still created — surface the error
    // but don't roll back.
    if (editModelVision.value) {
      try {
        await api.setModelCapabilities(providerName, name, {
          supports_vision: true,
          context_window: editModelCtx.value ?? 0,
        })
      } catch (capErr: any) {
        message.warning(`模型已添加, 但能力标记失败: ${capErr.message}`)
      }
    }
    message.success('已添加模型')
    resetModelForm()
    showAddModel.value = false
    await refreshProviders()
  } catch (e: any) {
    message.error(`添加失败: ${e.message}`)
  }
}

function onEditModel(m: api.ModelInfo) {
  if (!selected.value) return
  // Switch the form into "edit" mode and pre-populate every
  // field with the model's current values. The previous
  // version had two parallel ref families (`newModel*` for
  // add, `editModel*` for edit) but the form template was
  // wired only to `newModel*`, so opening edit left the
  // form blank. Now there's one set of refs; the template
  // branches on `editingModelName` for the model-id input
  // and the submit button label.
  editingModelName.value = m.name
  editModelName.value = m.name
  editModelDisplay.value = m.display_name || ''
  editModelCtx.value = m.max_tokens_context ?? null
  editModelOut.value = m.max_tokens_output ?? null
  editModelVision.value = !!m.capabilities?.supports_vision
  showAddModel.value = true
}

async function onSaveModel() {
  if (!selected.value || !editingModelName.value) return
  const provider = selected.value.name
  const model = editingModelName.value
  try {
    // updateModel semantics: 0 / "" in a numeric field means
    // "leave alone" (see internal/config/manager.go
    // UpdateModel). So we only send positive values; the
    // capabilities PATCH (which always replaces the block)
    // carries the rest.
    const ctx = editModelCtx.value && editModelCtx.value > 0 ? editModelCtx.value : 0
    const out = editModelOut.value && editModelOut.value > 0 ? editModelOut.value : 0
    await api.updateModel(provider, model, {
      display_name: editModelDisplay.value,
      max_tokens_context: ctx,
      max_tokens_output: out,
    })
    await api.setModelCapabilities(provider, model, {
      supports_vision: editModelVision.value,
      context_window: editModelCtx.value ?? 0,
    })
    message.success('已保存')
    resetModelForm()
    showAddModel.value = false
    await refreshProviders()
  } catch (e: any) {
    message.error(`保存失败: ${e.message}`)
  }
}

function onCancelEditModel() {
  resetModelForm()
  showAddModel.value = false
}

async function onDeleteModel(model: string) {
  if (!selected.value) return
  try {
    await api.deleteModel(selected.value.name, model)
    message.success('已删除')
    await refreshProviders()
  } catch (e: any) {
    message.error(`删除失败: ${e.message}`)
  }
}

async function onSetDefaultModel(model: string) {
  if (!selected.value) return
  try {
    await api.setDefaultModel(selected.value.name, model)
    message.success(`已设为默认模型: ${model}`)
    await refreshProviders()
  } catch (e: any) {
    message.error(`设置失败: ${e.message}`)
  }
}

async function onFetchUpstreamModels() {
  if (!selected.value) return
  fetchingUpstream.value = true
  upstreamError.value = ''
  try {
    const res = await api.fetchUpstreamModels(selected.value.name)
    upstreamModels.value = res.models || []
    showUpstreamModels.value = true
  } catch (e: any) {
    upstreamError.value = e.message || '获取失败'
  } finally {
    fetchingUpstream.value = false
  }
}

async function onImportUpstreamModel(m: api.UpstreamModelItem) {
  if (!selected.value || m.added) return
  const providerName = selected.value.name
  try {
    await api.addModel(providerName, { name: m.id })
    message.success(`已添加模型: ${m.id}`)
    await refreshProviders()
    // Refresh the upstream list to mark this one as added.
    const idx = upstreamModels.value.findIndex(x => x.id === m.id)
    if (idx >= 0) upstreamModels.value[idx] = { ...upstreamModels.value[idx], added: true }
  } catch (e: any) {
    message.error(`添加失败: ${e.message}`)
  }
}

// --- Style handlers ---

function resetNewStyle() {
  newStyleId.value = ''
  newStyleLabel.value = ''
  newStylePrompt.value = ''
  newStyleMemory.value = ''
  isEdit.value = false
  editingStyle.value = null
}

async function onCreateStyle() {
  if (!newStyleId.value.trim()) {
    message.warning('风格 id 为必填')
    return
  }
  try {
    await api.createStyle({
      id: newStyleId.value.trim(),
      label: newStyleLabel.value.trim(),
      prompt: newStylePrompt.value,
      memory: newStyleMemory.value,
    })
    message.success(`已创建: ${newStyleId.value}`)
    showAddStyle.value = false
    resetNewStyle()
    await refreshStyles()
  } catch (e: any) {
    message.error(`创建失败: ${e.message}`)
  }
}

async function onEditStyle(id: string) {
  try {
    const s = await api.getStyle(id)
    editingStyle.value = s
    newStyleId.value = s.id
    newStyleLabel.value = s.label || ''
    newStylePrompt.value = s.prompt || ''
    newStyleMemory.value = s.memory || ''
    isEdit.value = true
    showAddStyle.value = true
  } catch (e: any) {
    message.error(`读取失败: ${e.message}`)
  }
}

async function onUpdateStyle() {
  if (!editingStyle.value) return
  try {
    await api.updateStyle(editingStyle.value.id, {
      label: newStyleLabel.value,
      prompt: newStylePrompt.value,
      memory: newStyleMemory.value,
    })
    message.success(`已保存: ${editingStyle.value.id}`)
    showAddStyle.value = false
    resetNewStyle()
    await refreshStyles()
  } catch (e: any) {
    message.error(`保存失败: ${e.message}`)
  }
}

async function onDeleteStyle(id: string) {
  try {
    await api.deleteStyle(id)
    message.success(`已删除: ${id}`)
    await refreshStyles()
  } catch (e: any) {
    message.error(`删除失败: ${e.message}`)
  }
}

function close() { (window as any).closeAppSettings?.() }

// --- Archive state ---
const archivedSessions = ref<Session[]>([])
const loadingArchived = ref(false)
const showConfirmPermDelete = ref(false)
const pendingPermDeleteId = ref('')

function groupByProject(sessions: Session[]): Map<string, Session[]> {
  const map = new Map<string, Session[]>()
  for (const s of sessions) {
    const key = s.project_path || '(全局)'
    if (!map.has(key)) map.set(key, [])
    map.get(key)!.push(s)
  }
  return map
}
const archivedGroups = computed(() => {
  const map = groupByProject(archivedSessions.value)
  return Array.from(map.entries())
})

async function loadArchived() {
  loadingArchived.value = true
  try {
    const r = await api.listArchived()
    archivedSessions.value = r.sessions || []
  } catch (e: any) {
    message.error(e.message || '加载归档失败')
  } finally {
    loadingArchived.value = false
  }
}

async function onUnarchive(id: string) {
  try {
    await api.unarchiveSession(id)
    archivedSessions.value = archivedSessions.value.filter(s => s.id !== id)
    await loadSessions()
    message.success('已恢复')
  } catch (e: any) {
    message.error(e.message || '恢复失败')
  }
}

function onPermDelete(id: string) {
  pendingPermDeleteId.value = id
  showConfirmPermDelete.value = true
}

async function confirmPermDelete() {
  const id = pendingPermDeleteId.value
  if (!id) return
  try {
    await api.permanentDeleteSession(id)
    archivedSessions.value = archivedSessions.value.filter(s => s.id !== id)
    showConfirmPermDelete.value = false
    pendingPermDeleteId.value = ''
    message.info('已永久删除')
  } catch (e: any) {
    message.error(e.message || '删除失败')
  }
}

// --- Skills state ---
const loadedSkills = ref<api.SkillItem[]>([])
const searchResults = ref<api.SearchSkillItem[]>([])
const searchQuery = ref('')
const searching = ref(false)
const installing = ref('')
const savedRepos = ref<api.SavedRepo[]>([])
const showAddRepo = ref(false)
const newRepoName = ref('')
const newRepoUrl = ref('')
const activeRepoUrl = ref('')
const skillFilter = ref('')

const builtInRepos = [
  { name: 'Anthropic 官方技能', url: 'https://github.com/anthropics/skills' },
  { name: 'Awesome Claude Skills', url: 'https://github.com/ComposioHQ/awesome-claude-skills' },
]

async function refreshRepos() {
  try {
    const r = await api.listSkillRepos()
    savedRepos.value = r.repos || []
  } catch { /* ignore */ }
}

function onSelectRepo(url: string) {
  activeRepoUrl.value = url
  searchQuery.value = url
  onSearchSkills()
}

async function onAddRepo() {
  if (!newRepoName.value.trim() || !newRepoUrl.value.trim()) return
  try {
    const r = await api.addSkillRepo(newRepoName.value.trim(), newRepoUrl.value.trim())
    savedRepos.value = r.repos || []
    showAddRepo.value = false
    newRepoName.value = ''
    newRepoUrl.value = ''
    message.success('仓库已添加')
  } catch (e: any) {
    message.error(e.message || '添加失败')
  }
}

async function onRemoveRepo(url: string) {
  try {
    const r = await api.removeSkillRepo(url)
    savedRepos.value = r.repos || []
    message.info('仓库已移除')
  } catch (e: any) {
    message.error(e.message || '移除失败')
  }
}

const filteredSkills = computed(() => {
  const q = skillFilter.value.trim().toLowerCase()
  if (!q) return loadedSkills.value
  return loadedSkills.value.filter(s =>
    s.name.toLowerCase().includes(q) || s.description.toLowerCase().includes(q),
  )
})

const filteredSearchResults = computed(() => {
  const q = skillFilter.value.trim().toLowerCase()
  if (!q) return searchResults.value
  return searchResults.value.filter(s =>
    s.name.toLowerCase().includes(q) || s.description.toLowerCase().includes(q),
  )
})

async function refreshSkills() {
  try {
    const r = await api.listSkills()
    loadedSkills.value = r.skills || []
  } catch { /* ignore */ }
}

async function onSearchSkills() {
  if (!searchQuery.value.trim()) return
  searching.value = true
  try {
    const r = await api.searchSkills(searchQuery.value.trim())
    searchResults.value = r.results || []
  } catch (e: any) {
    message.error(e.message || '搜索失败')
    searchResults.value = []
  } finally {
    searching.value = false
  }
}

async function onInstallSkill(name: string, url: string) {
  installing.value = name
  try {
    await api.installSkill(name, url)
    message.success(`已安装: ${name}`)
    await refreshSkills()
  } catch (e: any) {
    message.error(e.message || '安装失败')
  } finally {
    installing.value = ''
  }
}

async function onDeleteSkill(name: string) {
  try {
    await api.deleteSkill(name)
    loadedSkills.value = loadedSkills.value.filter(s => s.name !== name)
    message.success(`已删除: ${name}`)
  } catch (e: any) {
    message.error(e.message || '删除失败')
  }
}
function closeStyleEditor() { showAddStyle.value = false; resetNewStyle() }

function formatArchiveTime(ts: number): string {
  const d = new Date(ts * 1000)
  return d.toLocaleDateString('zh-CN', { month: 'numeric', day: 'numeric' }) + ' ' +
    d.toLocaleTimeString('zh-CN', { hour: '2-digit', minute: '2-digit' })
}

const builtInStyles = new Set(['cute', 'guofeng', 'tech'])
function isBuiltIn(id: string) { return builtInStyles.has(id) }

// protocol options reused in two places.
const protocolOptions = [
  { label: 'OpenAI 兼容', value: 'openai' },
  { label: 'Anthropic (Claude)', value: 'anthropic' },
]

// model-table row helpers
function fmtContext(n?: number) {
  if (!n || n <= 0) return '—'
  if (n >= 1024) return `${Math.round(n / 1024)}k`
  return String(n)
}

// --- MCP ---
const mcpServers = ref<api.MCPServerInfo[]>([])
const mcpGlobalEnabled = ref(false)
const showAddMCP = ref(false)
const newMCPType = ref<'stdio' | 'sse'>('stdio')
const newMCPName = ref('')
const newMCPCommand = ref('')
const newMCPArgs = ref('')
const newMCPEnv = ref('')
const newMCPUrl = ref('')
const newMCPEnabled = ref(true)

async function refreshMCP() {
  try {
    const r = await api.listMCPServers()
    mcpServers.value = r.servers || []
    mcpGlobalEnabled.value = r.global_enabled
  } catch (e: any) {
    message.error(`加载 MCP 失败: ${e.message}`)
  }
}

async function onToggleMCPGlobal(v: boolean) {
  try {
    await api.setMCPGlobal(v)
    mcpGlobalEnabled.value = v
  } catch (e: any) {
    message.error(`切换 MCP 全局状态失败: ${e.message}`)
  }
}

async function onToggleMCPServer(name: string, enabled: boolean) {
  try {
    if (enabled) {
      await api.addMCPServer({
        name,
        command: '', // use existing config, server restores from persisted config
        enabled: true,
      })
    } else {
      await api.removeMCPServer(name)
    }
    await refreshMCP()
  } catch (e: any) {
    message.error(`操作失败: ${e.message}`)
  }
}

async function onRestartMCPServer(name: string) {
  try {
    await api.restartMCPServer(name)
    await refreshMCP()
  } catch (e: any) {
    message.error(`重启失败: ${e.message}`)
  }
}

async function onAddMCPServer() {
  if (!newMCPName.value) {
    message.error('名称为必填项')
    return
  }
  if (newMCPType.value === 'stdio' && !newMCPCommand.value) {
    message.error('Stdio 模式需要填写命令')
    return
  }
  if (newMCPType.value === 'sse' && !newMCPUrl.value) {
    message.error('SSE 模式需要填写 URL')
    return
  }
  try {
    const args = newMCPArgs.value
      ? newMCPArgs.value.split(/\s+/).filter(Boolean)
      : []
    let env: Record<string, string> | undefined
    if (newMCPEnv.value) {
      try { env = JSON.parse(newMCPEnv.value) } catch { /* ignore */ }
    }
    await api.addMCPServer({
      name: newMCPName.value,
      type: newMCPType.value,
      command: newMCPCommand.value,
      args,
      env,
      url: newMCPUrl.value || undefined,
      enabled: newMCPEnabled.value,
    })
    showAddMCP.value = false
    newMCPType.value = 'stdio'
    newMCPName.value = ''
    newMCPCommand.value = ''
    newMCPArgs.value = ''
    newMCPEnv.value = ''
    newMCPUrl.value = ''
    newMCPEnabled.value = true
    await refreshMCP()
  } catch (e: any) {
    message.error(`添加失败: ${e.message}`)
  }
}

async function onRemoveMCPServer(name: string) {
  try {
    await api.removeMCPServer(name)
    await refreshMCP()
  } catch (e: any) {
    message.error(`删除失败: ${e.message}`)
  }
}

function mcpStateLabel(s: api.MCPServerInfo['state']) {
  switch (s) {
    case 'running': return '运行中'
    case 'starting': return '启动中'
    case 'stopped': return '已停止'
    case 'error': return '错误'
    default: return s
  }
}

function mcpStateType(s: api.MCPServerInfo['state']): 'success' | 'warning' | 'error' | 'default' {
  switch (s) {
    case 'running': return 'success'
    case 'starting': return 'warning'
    case 'error': return 'error'
    default: return 'default'
  }
}
</script>

<template>
  <NModal :show="true" @update:show="close" preset="card" title="应用设置" style="width: 920px; max-height: 80vh; overflow: hidden; display: flex; flex-direction: column">
    <NTabs v-model:value="tab" type="line" animated style="flex: 1; min-height: 0; display: flex; flex-direction: column">
      <NTabPane name="providers" tab="LLM 提供商" style="flex: 1; min-height: 0; overflow: auto">
        <div class="providers-split">
          <!-- Left: provider list -->
          <div class="provider-list">
            <div class="provider-list-header">
              <span class="list-title">提供商 ({{ providers.length }})</span>
              <NButton size="tiny" type="primary" ghost @click="showAddProvider = !showAddProvider">
                {{ showAddProvider ? '取消' : '+ 新增' }}
              </NButton>
            </div>
            <div v-if="showAddProvider" class="add-form">
              <NSpace vertical size="small">
                <NInput v-model:value="newName" placeholder="名称" size="tiny" />
                <NSelect v-model:value="newProtocol" :options="protocolOptions" size="tiny" />
                <NInput v-model:value="newBaseURL" placeholder="Base URL" size="tiny" />
                <NInput v-model:value="newAPIKey" placeholder="API Key" type="password" size="tiny" show-password-on="click" />
                <NInput v-model:value="newModel" placeholder="默认模型" size="tiny" />
                <NButton type="primary" size="tiny" @click="onAddProvider">提交</NButton>
              </NSpace>
            </div>
            <div class="provider-items">
              <div
                v-for="p in providers"
                :key="p.name"
                class="provider-item"
                :class="{ active: p.name === selectedName }"
                @click="selectProvider(p.name)"
              >
                <div class="provider-item-head">
                  <NTag v-if="p.is_default" type="success" size="tiny" :bordered="false">默认</NTag>
                  <strong class="provider-item-name">{{ p.name }}</strong>
                  <NPopconfirm
                    v-if="!p.is_default"
                    @positive-click="(e: Event) => { e.stopPropagation(); onDeleteProvider(p.name) }"
                    positive-text="删除"
                    negative-text="取消"
                  >
                    <template #trigger>
                      <NButton size="tiny" quaternary type="error" @click.stop title="删除供应商" class="provider-del-btn">✕</NButton>
                    </template>
                    确定删除 "{{ p.name }}" 及其下所有模型？
                  </NPopconfirm>
                </div>
                <div class="provider-item-sub">
                  <NTag size="tiny" :bordered="false">{{ p.protocol }}</NTag>
                  <span class="muted">{{ p.models?.length || 0 }} 模型</span>
                </div>
              </div>
              <div v-if="providers.length === 0" class="muted empty-hint">还没有 provider</div>
            </div>
          </div>

          <!-- Right: detail pane -->
          <div class="provider-detail">
            <div v-if="!selected" class="muted empty-hint">← 选择左侧的 provider</div>
            <template v-else>
              <!-- Basic info form -->
              <div class="detail-section">
                <div class="detail-section-head">
                  <h3 class="section-title">基本信息</h3>
                  <NSpace>
                    <NButton size="small" @click="onSaveProvider" type="primary" :disabled="dirty.size === 0">
                      保存
                    </NButton>
                    <NButton size="small" @click="hydrateEditForm(selected.name)" :disabled="dirty.size === 0">重置</NButton>
                  </NSpace>
                </div>
                <div class="detail-form">
                  <NSpace vertical size="small">
                    <div class="form-row">
                      <span class="form-label">名称</span>
                      <NInput
                        v-model:value="editName"
                        size="small"
                        :status="dirty.has('name') && editName.trim() === selected.name ? 'warning' : undefined"
                        @update:value="markDirty('name')"
                      />
                    </div>
                    <div class="form-row">
                      <span class="form-label">协议</span>
                      <NSelect
                        v-model:value="editProtocol"
                        :options="protocolOptions"
                        size="small"
                        @update:value="markDirty('protocol')"
                      />
                    </div>
                    <div class="form-row">
                      <span class="form-label">Base URL</span>
                      <NInput
                        v-model:value="editBaseURL"
                        size="small"
                        placeholder="https://api.openai.com/v1"
                        @update:value="markDirty('base_url')"
                      />
                    </div>
                    <div class="form-row">
                      <span class="form-label">API Key</span>
                      <NInput
                        v-model:value="editAPIKey"
                        size="small"
                        type="password"
                        show-password-on="click"
                        placeholder="sk-..."
                        @update:value="markDirty('api_key')"
                      />
                    </div>
                    <div class="form-row">
                      <span class="form-label">设为默认</span>
                      <NSwitch
                        :value="editIsDefault"
                        :disabled="selected.is_default"
                        @update:value="(v: boolean) => { editIsDefault = v; markDirty('is_default') }"
                      />
                      <span v-if="selected.is_default" class="muted form-hint">当前已是默认</span>
                    </div>
                  </NSpace>
                </div>
              </div>

              <!-- Model table -->
              <div class="detail-section">
                <div class="detail-section-head">
                  <h3 class="section-title">模型 ({{ selected.models?.length || 0 }})</h3>
                  <NSpace size="small">
                    <NButton size="small" type="primary" ghost @click="onShowAddModel">
                      {{ showAddModel ? '取消' : '+ 添加模型' }}
                    </NButton>
                    <NButton size="small" @click="onFetchUpstreamModels" :loading="fetchingUpstream">
                      获取模型
                    </NButton>
                  </NSpace>
                </div>
                <div v-if="showAddModel" class="add-form">
                  <NSpace vertical size="small">
                    <div v-if="editingModelName" class="muted form-hint">编辑模型: <code>{{ editingModelName }}</code></div>
                    <NInput
                      v-model:value="editModelName"
                      :disabled="!!editingModelName"
                      placeholder="模型 ID (例: gpt-4o-mini)"
                      size="small"
                    />
                    <NInput v-model:value="editModelDisplay" placeholder="显示名 (例: GPT-4o mini)" size="small" />
                    <div class="form-row">
                      <span class="form-label">上下文 (tokens)</span>
                      <NInputNumber v-model:value="editModelCtx" :min="0" :step="1024" placeholder="例: 128000" size="small" style="flex: 1" />
                    </div>
                    <div class="form-row">
                      <span class="form-label">最大输出 (tokens)</span>
                      <NInputNumber v-model:value="editModelOut" :min="0" :step="512" placeholder="例: 4096" size="small" style="flex: 1" />
                    </div>
                    <div class="form-row">
                      <span class="form-label">支持视觉</span>
                      <NSwitch v-model:value="editModelVision" />
                    </div>
                    <NSpace>
                      <NButton type="primary" size="small" @click="editingModelName ? onSaveModel() : onAddModel()">
                        {{ editingModelName ? '保存修改' : '添加模型' }}
                      </NButton>
                      <NButton size="small" @click="onCancelEditModel">取消</NButton>
                    </NSpace>
                  </NSpace>
                </div>
                <div v-if="selected.models && selected.models.length" class="model-list">
                  <div
                    v-for="m in selected.models"
                    :key="m.name"
                    class="model-card"
                    :class="{ 'is-default': m.default }"
                  >
                    <div class="model-card-top">
                      <span class="model-card-name">{{ m.name }}</span>
                      <NTag v-if="m.default" type="success" size="tiny" :bordered="false">默认</NTag>
                      <NTag v-if="m.capabilities?.supports_vision" size="tiny" :bordered="false" type="info">视觉</NTag>
                    </div>
                    <div class="model-card-meta" v-if="m.display_name || m.max_tokens_context || m.max_tokens_output">
                      <span v-if="m.display_name" class="model-meta-item">{{ m.display_name }}</span>
                      <span v-if="m.max_tokens_context" class="model-meta-item">上下文 {{ fmtContext(m.max_tokens_context) }}</span>
                      <span v-if="m.max_tokens_output" class="model-meta-item">输出 {{ m.max_tokens_output }}</span>
                    </div>
                    <div class="model-card-actions">
                      <NButton size="tiny" quaternary @click="onEditModel(m)" title="编辑">✎</NButton>
                      <NButton v-if="!m.default" size="tiny" quaternary @click="onSetDefaultModel(m.name)" title="设为默认">★</NButton>
                      <NPopconfirm @positive-click="onDeleteModel(m.name)" positive-text="删除" negative-text="取消">
                        <template #trigger>
                          <NButton size="tiny" quaternary type="error" title="删除">✕</NButton>
                        </template>
                        确定删除模型 "{{ m.name }}"？
                      </NPopconfirm>
                    </div>
                  </div>
                </div>
                <div v-else class="muted empty-hint">还没有模型。点击「+ 添加模型」配置。</div>
              </div>
            </template>
          </div>
        </div>

        <!-- Upstream models modal -->
        <NModal v-model:show="showUpstreamModels" preset="card" title="上游模型列表" style="width: 500px">
          <div v-if="upstreamError" class="upstream-error">{{ upstreamError }}</div>
          <template v-else>
            <p class="upstream-hint">以下是从 {{ selected?.name }} 上游获取的可用模型，点击即可添加。</p>
            <div v-if="!upstreamModels.length" class="muted empty-hint">未获取到模型列表</div>
            <div v-else class="upstream-list">
              <div
                v-for="m in upstreamModels"
                :key="m.id"
                class="upstream-item"
                :class="{ added: m.added }"
              >
                <span class="upstream-id">{{ m.id }}</span>
                <span class="upstream-owner" v-if="m.owned_by">{{ m.owned_by }}</span>
                <NButton v-if="!m.added" size="tiny" type="primary" ghost @click="onImportUpstreamModel(m)">
                  添加
                </NButton>
                <NTag v-else size="tiny" type="default">已添加</NTag>
              </div>
            </div>
          </template>
        </NModal>
      </NTabPane>

      <NTabPane name="styles" tab="风格配置">
        <div class="styles-tab-body">
          <h3 class="section-title">已配置的风格</h3>
          <div class="style-grid">
            <div v-for="s in styles" :key="s.id" class="style-card">
              <div class="style-card-top">
                <NTag size="small" :type="isBuiltIn(s.id) ? 'success' : 'info'">
                  {{ isBuiltIn(s.id) ? '内置' : '自定义' }}
                </NTag>
                <span class="style-card-id"><code>{{ s.id }}</code></span>
              </div>
              <div class="style-card-label">{{ s.label }}</div>
              <div class="style-card-desc">{{ s.desc || '（无描述）' }}</div>
              <div class="style-card-actions">
                <NButton size="small" quaternary @click="onEditStyle(s.id)">查看/编辑</NButton>
                <NPopconfirm
                  v-if="!isBuiltIn(s.id)"
                  @positive-click="onDeleteStyle(s.id)"
                  positive-text="删除"
                  negative-text="取消"
                >
                  <template #trigger>
                    <NButton size="small" quaternary type="error">删除</NButton>
                  </template>
                  确定删除风格 "{{ s.id }}" ? 该会话将回退到默认风格。
                </NPopconfirm>
              </div>
            </div>
          </div>

          <NButton size="small" style="margin-top:12px"
            @click="() => { showAddStyle = !showAddStyle; if (showAddStyle) resetNewStyle() }" type="primary" ghost>
            {{ showAddStyle ? '取消' : '+ 新增风格' }}
          </NButton>

          <!-- ---- 编辑/新增表单 ---- -->
          <div v-if="showAddStyle" class="style-editor">
            <div class="editor-header">
              <span>{{ isEdit ? '编辑风格' : '新增风格' }}</span>
            </div>

            <!-- 元数据行 -->
            <div class="editor-meta">
              <div class="meta-item">
                <label>ID</label>
                <NInput v-model:value="newStyleId" placeholder="英文/数字/下划线，如 warm" size="small" :disabled="isEdit"
                  :status="idConflict ? 'error' : undefined" />
                <span class="meta-hint" :class="{ 'meta-hint-err': idConflict }">
                  {{ idConflict ? '该 ID 已被占用' : '纯英文+数字+下划线，唯一标识，不可重复' }}
                </span>
              </div>
              <div class="meta-item">
                <label>显示名称</label>
                <NInput v-model:value="newStyleLabel" placeholder="如：温暖" size="small" />
              </div>
            </div>

            <!-- 内容区：Prompt -->
            <div class="editor-content">
              <div class="editor-col" style="flex: 1">
                <div class="field-head">
                  Prompt<span class="field-hint">— 人设 + 性格 + 说话风格 + 表达模板</span>
                  <NPopover trigger="hover" placement="bottom" style="max-width:440px">
                    <template #trigger><span class="help-badge">?</span></template>
                    <div class="example-card">
                      <div class="example-scenes">
                        <NButton v-for="sc in EXAMPLE_SCENARIOS" :key="sc" size="tiny"
                          :type="exampleActiveScene.prompt === sc ? 'primary' : 'default'"
                          @click="exampleActiveScene.prompt = sc">{{ sc }}</NButton>
                      </div>
                      <pre class="example-content">{{ EXAMPLE_TEMPLATES.prompt[exampleActiveScene.prompt] }}</pre>
                      <NButton size="tiny" type="primary" ghost block @click="fillExample('prompt')">填入此样例</NButton>
                    </div>
                  </NPopover>
                </div>
                <NInput v-model:value="newStylePrompt" placeholder="人设 + 性格 + 说话风格 + 表达模板，支持 markdown"
                  type="textarea" :rows="12" size="small" class="editor-textarea" />
              </div>
            </div>

            <!-- 记忆：全宽 -->
            <div class="editor-memory">
              <div class="field-head">
                记忆 (Memory)<span class="field-hint">— 定义「我记得的事情」</span>
                <NPopover trigger="hover" placement="bottom" style="max-width:440px">
                  <template #trigger><span class="help-badge">?</span></template>
                  <div class="example-card">
                    <div class="example-scenes">
                      <NButton v-for="sc in EXAMPLE_SCENARIOS" :key="sc" size="tiny"
                        :type="exampleActiveScene.memory === sc ? 'primary' : 'default'"
                        @click="exampleActiveScene.memory = sc">{{ sc }}</NButton>
                    </div>
                    <pre class="example-content">{{ EXAMPLE_TEMPLATES.memory[exampleActiveScene.memory] }}</pre>
                    <NButton size="tiny" type="primary" ghost block @click="fillExample('memory')">填入此样例</NButton>
                  </div>
                </NPopover>
              </div>
              <NInput v-model:value="newStyleMemory" placeholder="背景资料、项目信息、用户偏好，每行一条"
                type="textarea" :rows="4" size="small" class="editor-textarea" />
            </div>

            <div class="editor-actions">
              <NButton size="small" @click="closeStyleEditor">取消</NButton>
              <NButton type="primary" size="small" @click="isEdit ? onUpdateStyle() : onCreateStyle()">
                {{ isEdit ? '保存修改' : '创建风格' }}
              </NButton>
            </div>
          </div>
        </div>
      </NTabPane>

      <NTabPane name="archive" tab="归档" style="flex: 1; min-height: 0; overflow: auto">
        <div v-if="!archivedSessions.length && !loadingArchived" class="empty-hint">
          暂无归档对话
        </div>
        <div v-else v-for="[project, sessions] in archivedGroups" :key="project" class="archive-group">
          <div class="archive-group-title">{{ project }}</div>
          <div v-for="s in (sessions as Session[])" :key="s.id" class="archive-row">
            <span class="archive-title">{{ s.title || '(无标题)' }}</span>
            <span class="archive-meta">{{ formatArchiveTime(s.updated_at) }}</span>
            <NButton size="tiny" quaternary @click="onUnarchive(s.id)">恢复</NButton>
            <NButton size="tiny" quaternary @click="onPermDelete(s.id)" style="color: var(--warn)">删除</NButton>
          </div>
        </div>
      </NTabPane>

      <NTabPane name="skills" tab="技能" style="flex: 1; min-height: 0; overflow: auto">
        <!-- Built-in repos -->
        <div class="skill-repos">
          <span class="skill-hint">官方仓库</span>
          <div class="repo-chips">
            <NButton v-for="r in builtInRepos" :key="r.url" size="tiny" :type="activeRepoUrl === r.url ? 'primary' : 'default'" @click="onSelectRepo(r.url)">{{ r.name }}</NButton>
          </div>
        </div>
        <!-- Saved repos -->
        <div v-if="savedRepos.length" class="skill-repos" style="margin-top:12px">
          <div class="skill-repos-header">
            <span class="skill-hint">我的仓库</span>
            <NButton size="tiny" quaternary @click="showAddRepo = true">+ 添加</NButton>
          </div>
          <div class="repo-chips">
            <template v-for="r in savedRepos" :key="r.url">
              <NButton size="tiny" :type="activeRepoUrl === r.url ? 'primary' : 'default'" @click="onSelectRepo(r.url)">{{ r.name }}</NButton>
              <NButton size="tiny" quaternary @click="onRemoveRepo(r.url)" style="color:var(--warn);font-size:10px">×</NButton>
            </template>
          </div>
        </div>
        <div v-if="!savedRepos.length" class="skill-repos" style="margin-top:8px">
          <span class="skill-hint" style="color:var(--text-4)">我的仓库（暂无）</span>
          <NButton size="tiny" quaternary @click="showAddRepo = true" style="margin-left:4px">+ 添加</NButton>
        </div>
        <div class="skill-divider" />
        <!-- Filter & search -->
        <div class="skill-search">
          <NInput v-model:value="skillFilter" placeholder="搜索/筛选技能..." size="small" clearable style="flex:1" />
        </div>
        <!-- Search results from remote repo -->
        <div v-if="searchResults.length" class="skill-search-results">
          <div class="skill-section-title">仓库技能（{{ filteredSearchResults.length }}）</div>
          <div v-for="r in filteredSearchResults" :key="r.name" class="skill-search-row">
            <div class="skill-search-info">
              <span class="skill-search-name">{{ r.name }}</span>
              <span class="skill-search-desc">{{ r.description }}</span>
            </div>
            <NButton size="tiny" type="primary" :loading="installing === r.name" @click="onInstallSkill(r.name, r.url)">安装</NButton>
          </div>
        </div>

        <!-- Loaded skills -->
        <div class="skill-section-title" style="margin-top:12px">已加载（{{ filteredSkills.length }}）</div>
        <div v-if="!filteredSkills.length" class="empty-hint" style="padding:12px">暂无匹配技能</div>
        <div v-for="s in filteredSkills" :key="s.name" class="skill-row">
          <div class="skill-info">
            <span class="skill-name">{{ s.name }}</span>
            <span class="skill-desc">{{ s.description }}</span>
          </div>
          <NButton size="tiny" quaternary @click="onDeleteSkill(s.name)" style="color:var(--warn)">删除</NButton>
        </div>
      </NTabPane>

      <NTabPane name="mcp" tab="MCP" style="flex: 1; min-height: 0; overflow: auto">
        <div style="display:flex;flex-direction:column;gap:16px;padding:4px 0">
          <!-- Global toggle -->
          <div style="display:flex;align-items:center;justify-content:space-between">
            <span style="font-weight:600">启用 MCP</span>
            <NSwitch :value="mcpGlobalEnabled" @update:value="onToggleMCPGlobal" />
          </div>

          <!-- Server list -->
          <div style="display:flex;flex-direction:column;gap:8px">
            <div style="display:flex;align-items:center;justify-content:space-between">
              <span style="font-size:13px;color:var(--text-3)">服务器</span>
              <NButton size="tiny" quaternary @click="showAddMCP = !showAddMCP">
                {{ showAddMCP ? '取消' : '+ 添加' }}
              </NButton>
            </div>

            <!-- Add form -->
            <div v-if="showAddMCP" style="display:flex;flex-direction:column;gap:8px;padding:12px;border:1px solid var(--border);border-radius:8px;background:var(--bg-2)">
              <div style="display:flex;gap:8px">
                <NButton size="tiny" :type="newMCPType === 'stdio' ? 'primary' : 'default'" @click="newMCPType = 'stdio'">
                  Stdio
                </NButton>
                <NButton size="tiny" :type="newMCPType === 'sse' ? 'primary' : 'default'" @click="newMCPType = 'sse'">
                  SSE
                </NButton>
              </div>
              <NInput v-model:value="newMCPName" placeholder="名称 (如 playwright)" size="small" />
              <template v-if="newMCPType === 'stdio'">
                <NInput v-model:value="newMCPCommand" placeholder="命令 (如 npx)" size="small" />
                <NInput v-model:value="newMCPArgs" placeholder="参数 (空格分隔，如 -y @anthropic/mcp-server-playwright)" size="small" />
                <NInput v-model:value="newMCPEnv" placeholder='环境变量 (JSON 格式)' size="small" />
              </template>
              <template v-else>
                <NInput v-model:value="newMCPUrl" placeholder="SSE URL (如 http://localhost:3001/mcp)" size="small" />
              </template>
              <div style="display:flex;align-items:center;gap:8px">
                <span style="font-size:12px">启动</span>
                <NSwitch v-model:value="newMCPEnabled" size="small" />
              </div>
              <NButton type="primary" size="small" @click="onAddMCPServer" style="align-self:flex-start">添加</NButton>
            </div>

            <!-- Server rows -->
            <div v-if="!mcpServers.length && !showAddMCP" style="padding:20px;text-align:center;color:var(--text-4);font-size:13px">
              暂无 MCP 服务器，点击 "+ 添加" 开始
            </div>
            <div
              v-for="s in mcpServers"
              :key="s.name"
              style="display:flex;align-items:center;justify-content:space-between;padding:8px 12px;border:1px solid var(--border);border-radius:6px"
            >
              <div style="display:flex;flex-direction:column;gap:2px;min-width:0">
                <span style="font-weight:500">{{ s.name }}</span>
                <div style="display:flex;align-items:center;gap:8px;font-size:12px;color:var(--text-3)">
                  <NTag :type="mcpStateType(s.state)" size="tiny" :bordered="false">{{ mcpStateLabel(s.state) }}</NTag>
                  <span>{{ s.tool_count }} 个工具</span>
                  <span v-if="s.error" style="color:var(--error)">{{ s.error }}</span>
                </div>
              </div>
              <div style="display:flex;align-items:center;gap:4px;flex-shrink:0">
                <NSwitch
                  :value="s.state === 'running' || s.state === 'starting'"
                  @update:value="(v: boolean) => onToggleMCPServer(s.name, v)"
                  size="small"
                />
                <NButton size="tiny" quaternary @click="onRestartMCPServer(s.name)" title="重启">↻</NButton>
                <NPopconfirm @positive-click="onRemoveMCPServer(s.name)">
                  <template #trigger>
                    <NButton size="tiny" quaternary style="color:var(--warn)">×</NButton>
                  </template>
                  确定删除 "{{ s.name }}"？
                </NPopconfirm>
              </div>
            </div>
          </div>
        </div>
      </NTabPane>
    </NTabs>

    <!-- Confirmation: permanent delete from archive -->
    <NModal v-model:show="showConfirmPermDelete" preset="card" title="确认永久删除" style="width: 360px">
      <div class="confirm-body">
        <p>确定要永久删除此会话吗？此操作不可撤销。</p>
        <div class="confirm-actions">
          <NButton size="small" @click="showConfirmPermDelete = false">取消</NButton>
          <NButton size="small" type="error" @click="confirmPermDelete">永久删除</NButton>
        </div>
      </div>
    </NModal>

    <!-- Add skill repo -->
    <NModal v-model:show="showAddRepo" preset="card" title="添加技能仓库" style="width: 420px">
      <div class="add-project-form">
        <label>仓库名称</label>
        <NInput v-model:value="newRepoName" placeholder="例如：我的技能仓库" />
        <label style="margin-top: 12px">GitHub 地址</label>
        <NInput v-model:value="newRepoUrl" placeholder="例如：https://github.com/user/repo" />
        <div class="project-actions">
          <NButton size="small" @click="showAddRepo = false">取消</NButton>
          <NButton size="small" type="primary" @click="onAddRepo">添加</NButton>
        </div>
      </div>
    </NModal>

    <template #footer>
      <NSpace justify="end">
        <NButton @click="close">关闭</NButton>
      </NSpace>
    </template>
  </NModal>
</template>

<style scoped>
.section-title { margin: 0; font-size: 13px; font-weight: 600; }

/* Providers tab — left/right split */
.providers-split {
  display: grid;
  grid-template-columns: 240px 1fr;
  gap: 12px;
  height: 60vh;
  min-height: 480px;
}
.provider-list {
  border: 1px solid var(--border-2);
  border-radius: 6px;
  background: var(--bg-2);
  display: flex; flex-direction: column;
  overflow: hidden;
}
.provider-list-header {
  display: flex; align-items: center; justify-content: space-between;
  padding: 8px 10px;
  border-bottom: 1px solid var(--border-2);
  background: var(--bg-3);
}
.list-title { font-size: 12px; font-weight: 600; }
.provider-items { flex: 1; overflow: auto; padding: 4px; }
.provider-item {
  padding: 8px 10px;
  border-radius: 4px;
  cursor: pointer;
  margin-bottom: 2px;
  transition: background 0.15s;
}
.provider-item:hover { background: var(--bg-3); }
.provider-item.active {
  background: var(--accent);
  color: var(--on-accent);
}
.provider-item.active .muted { color: rgba(255, 255, 255, 0.85); }
.provider-item-head {
  display: flex; align-items: center; gap: 6px;
  margin-bottom: 2px;
}
.provider-item-name { font-size: 13px; }
.provider-del-btn {
  opacity: 0;
  margin-left: auto;
  transition: opacity 0.15s;
}
.provider-item:hover .provider-del-btn { opacity: 1; }
.provider-item-sub {
  display: flex; align-items: center; gap: 6px;
  font-size: 11px;
}

.provider-detail {
  border: 1px solid var(--border-2);
  border-radius: 6px;
  background: var(--bg-2);
  display: flex; flex-direction: column;
  overflow: hidden;
}
.detail-section {
  border-bottom: 1px solid var(--border-2);
  padding: 12px 14px;
}
.detail-section:last-child { border-bottom: none; flex: 1; overflow: auto; }
.detail-section-head {
  display: flex; align-items: center; justify-content: space-between;
  margin-bottom: 10px;
}
.detail-form { padding: 4px 0; }

.form-row {
  display: flex; align-items: center; gap: 10px;
}
.form-label {
  font-size: 12px;
  width: 100px;
  color: var(--text-2);
}
.form-hint { font-size: 11px; }

/* Model cards */
.model-list {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.model-card {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 10px;
  border-radius: 6px;
  background: var(--bg-3);
  border: 1px solid var(--border-2);
  font-size: 12.5px;
}
.model-card.is-default {
  border-color: var(--success);
  background: color-mix(in srgb, var(--success) 6%, var(--bg-3));
}
.model-card-top {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 140px;
  flex-shrink: 0;
}
.model-card-name {
  font-family: ui-monospace, Menlo, Consolas, monospace;
  font-size: 12px;
  font-weight: 500;
}
.model-card-meta {
  display: flex;
  align-items: center;
  gap: 10px;
  flex: 1;
  min-width: 0;
  color: var(--text-3);
  font-size: 11.5px;
}
.model-meta-item {
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.model-card-actions {
  display: flex;
  gap: 2px;
  flex-shrink: 0;
}
.model-id {
  background: var(--bg-3);
  padding: 1px 6px;
  border-radius: 3px;
  font-family: ui-monospace, Menlo, monospace;
  font-size: 11px;
}

.muted { color: var(--text-3); font-size: 12px; }
.empty-hint { padding: 20px; text-align: center; }
.add-form {
  margin-top: 8px;
  padding: 10px;
  background: var(--bg-2);
  border: 1px solid var(--border-2);
  border-radius: 6px;
}
.styles-tab-body {
  max-height: calc(80vh - 160px);
  overflow: auto;
}
/* ---- 风格卡片网格 ---- */
.style-grid {
  display: grid;
  grid-template-columns: repeat(2, 1fr);
  gap: 10px;
}
.style-card {
  background: var(--bg-3);
  border: 1px solid var(--border-2);
  border-radius: 8px;
  padding: 14px 16px;
  display: flex; flex-direction: column; gap: 8px;
  transition: border-color .15s;
}
.style-card:hover { border-color: var(--accent); }
.style-card-top { display: flex; align-items: center; gap: 8px; }
.style-card-id { font-size: 12px; color: var(--text-3); }
.style-card-label { font-size: 16px; font-weight: 600; }
.style-card-desc {
  font-size: 12px; color: var(--text-3); line-height: 1.5;
  display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden;
}
.style-card-actions {
  display: flex; gap: 4px; margin-top: 4px;
  border-top: 1px solid var(--border-2); padding-top: 10px;
}
/* ---- 风格编辑器 ---- */
.style-editor {
  margin-top: 14px;
  background: var(--bg-3);
  border: 1px solid var(--border-2);
  border-radius: 8px;
  padding: 16px 20px;
  display: flex; flex-direction: column; gap: 14px;
}
.editor-header {
  font-size: 15px; font-weight: 700;
  padding-bottom: 10px; border-bottom: 1px solid var(--border-2);
}
.editor-meta { display: flex; gap: 16px; }
.meta-item { flex: 1; display: flex; flex-direction: column; gap: 4px; }
.meta-item label { font-size: 12px; font-weight: 600; color: var(--text-2); }
.meta-hint { font-size: 11px; color: var(--text-3); }
.meta-hint-err { color: var(--warn); }
.editor-content { display: flex; gap: 16px; }
.editor-col { flex: 1; display: flex; flex-direction: column; gap: 6px; }
.editor-memory { display: flex; flex-direction: column; gap: 6px; }
.editor-textarea { flex: 1; }
.editor-actions { display: flex; justify-content: flex-end; gap: 8px; padding-top: 6px; }
/* ---- 旧样式保留 ---- */
.field-head {
  font-size: 13px; font-weight: 600; color: var(--text-1);
}
.field-hint {
  font-weight: 400; color: var(--text-3); font-size: 12px;
}
.field-desc {
  font-size: 12px; color: var(--text-3); line-height: 1.5;
  margin-top: -4px;
}
.help-badge {
  display: inline-flex; align-items: center; justify-content: center;
  width: 16px; height: 16px; border-radius: 50%;
  background: var(--bg-3); border: 1px solid var(--border-2);
  font-size: 10px; font-weight: 700; color: var(--text-2);
  cursor: help; margin-left: 4px; vertical-align: middle;
  user-select: none;
}
.help-badge:hover { background: var(--accent); color: #fff; border-color: var(--accent); }
.example-card { padding: 4px 0; }
.example-scenes { display: flex; gap: 4px; margin-bottom: 8px; }
.example-content {
  margin: 0; padding: 8px; background: var(--bg-3); border-radius: 4px;
  font-size: 12px; line-height: 1.6; white-space: pre-wrap; word-break: break-all;
  max-height: 300px; overflow: auto;
  font-family: ui-monospace, Menlo, monospace;
}
code {
  background: var(--bg-3); padding: 1px 6px; border-radius: 3px;
  font-family: ui-monospace, Menlo, monospace; font-size: 12px;}
.archive-group { margin-bottom: 16px; }
.archive-group-title {
  font-weight: 600; font-size: 13px;
  padding: 4px 0 8px; border-bottom: 1px solid var(--border-2);
  margin-bottom: 8px; color: var(--text-2);
}
.archive-row {
  display: flex; align-items: center; gap: 12px;
  padding: 6px 8px; border-radius: 6px;
}
.archive-row:hover { background: var(--bg-3); }
.archive-title { flex: 1; font-size: 13px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.archive-meta { font-size: 11px; color: var(--text-4); white-space: nowrap; }
.confirm-body { padding: 8px 0; }
.confirm-body p { margin: 0 0 16px; font-size: 14px; color: var(--text-2); }
.confirm-actions { display: flex; gap: 8px; justify-content: flex-end; }
.skill-search { display: flex; gap: 8px; margin-bottom: 12px; }
.skill-repos { margin-bottom: 4px; }
.skill-repos-header { display: flex; justify-content: space-between; align-items: center; }
.repo-chips { display: flex; gap: 4px; flex-wrap: wrap; margin-top: 4px; }
.repo-chip-row { display: inline-flex; align-items: center; }
.skill-search-results { margin-bottom: 12px; }
.skill-search-row {
  display: flex; align-items: center; gap: 8px;
  padding: 6px 8px; border-radius: 6px;
}
.skill-search-row:hover { background: var(--bg-3); }
.skill-search-info { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 2px; }
.skill-search-name { font-size: 13px; font-weight: 500; }
.skill-search-desc { font-size: 11px; color: var(--text-4); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.skill-divider { border-top: 1px solid var(--border-2); margin: 12px 0; }
.skill-hint { font-size: 12px; color: var(--text-3); margin-bottom: 8px; }
.skill-section-title { font-size: 12px; color: var(--text-3); margin-bottom: 6px; }
.skill-row {
  display: flex; align-items: center; gap: 8px;
  padding: 6px 8px; border-radius: 6px;
}
.skill-row:hover { background: var(--bg-3); }
.skill-info { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 2px; }
.skill-name { font-size: 13px; font-weight: 500; }
.skill-desc { font-size: 11px; color: var(--text-4); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.upstream-error { color: var(--warn); padding: 8px 0; }
.upstream-hint { font-size: 13px; color: var(--text-3); margin: 0 0 12px; }
.upstream-list { max-height: 400px; overflow: auto; }
.upstream-item {
  display: flex; align-items: center; gap: 10px;
  padding: 8px 10px;
  border-bottom: 1px solid var(--border-2);
  font-size: 13px;
}
.upstream-item:hover { background: var(--bg-3); }
.upstream-id { flex: 1; font-family: ui-monospace, monospace; font-size: 12.5px; }
.upstream-owner { color: var(--text-4); font-size: 11px; }
.upstream-item.added { opacity: 0.6; }
</style>
