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

import { computed, h, onBeforeUnmount, onMounted, onUnmounted, ref, watch } from 'vue'
import {
  NModal, NCard, NSelect, NButton, NSpace, NInput, NInputNumber, NSwitch,
  NTag, NTabs, NTabPane, NDataTable, NPopconfirm, NPopover, NCollapse, NCollapseItem, NTree,
  NRadioGroup, NRadioButton, useMessage,
} from 'naive-ui'
import {
  X, Pencil, Star, Trash2, RotateCw, Eye, Clipboard, FileText, File, Hash,
  Cpu, Palette, Archive, Settings as SettingsIcon, Wrench, Terminal, Database, Globe, Monitor,
} from './icons'
import * as api from '../api/client'
import { loadProviders, loadSessions, bumpKBConfigVersion, state as chatState } from '../stores/chat'
import type { Session } from '../api/client'
import WebSearchSettings from './WebSearchSettings.vue'
import AppSettingsLayout from './AppSettingsLayout.vue'

const message = useMessage()

// Two-way bind for the modal's open/close state. App.vue
// passes `show` and listens to `update:show` so toggling the
// X close button (or pressing Esc) actually unmounts the
// component via App.vue's v-if. Before PR #8, this emit
// wasn't declared at all, so clicking X did nothing visible
// until the user navigated away and back.
const emit = defineEmits<{
  (e: 'update:show', v: boolean): void
}>()

function close() {
  emit('update:show', false)
}

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
const tab = ref<'providers' | 'styles' | 'system' | 'archive' | 'skills' | 'mcp' | 'knowledge' | 'websearch' | 'browser'>('providers')

// Modal visibility (v-model). The default is `true` so that
// when App.vue mounts this component (it only mounts when
// `showAppSettings` is true), the layout is visible without
// an extra tick. When the user closes the X, the layout
// emits `update:show=false` and the parent re-renders with
// the new value.
const show = ref(true)

// settingsTabs feeds AppSettingsLayout's left nav. The order
// here is also the visual order in the nav. Each entry binds
// a tab name (the same name used in NTabPane) to a lucide
// icon + label. Adding a new settings section means adding
// a tab here, a NTabPane below, and the corresponding
// content — the rest of the layout picks it up
// automatically.
const settingsTabs = [
  { name: 'providers', label: 'LLM 提供商',  icon: Cpu,      description: 'API key 与模型管理' },
  { name: 'styles',    label: '风格',          icon: Palette,  description: '人格与记忆模板' },
  { name: 'system',    label: '系统',          icon: SettingsIcon, description: '限额、子代理、行为' },
  { name: 'archive',   label: '归档',          icon: Archive,  description: '已归档的会话' },
  { name: 'skills',    label: '技能',          icon: Wrench,   description: '可加载的技能包' },
  { name: 'mcp',       label: 'MCP',           icon: Terminal, description: 'Model Context Protocol 服务器' },
  { name: 'knowledge', label: '知识库',        icon: Database, description: 'RAG 文档检索' },
  { name: 'websearch', label: '网络搜索',      icon: Globe,    description: 'Tavily / Brave 等搜索提供商' },
  { name: 'browser',   label: '浏览器',        icon: Monitor,  description: '浏览器扩展与自动化控制' },
]

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

// --- System config state ---
const sysLimits = ref<api.LimitsConfig>({
  auto_compact_buffer: 20000,
  tool_result_exec_cap: 4000,
  tool_result_read_cap: 8000,
  tool_result_default_cap: 6000,
  prune_after_rounds: 15,
  max_rounds: 300,
  max_stored_messages: 0,
})
const sysSubAgent = ref<api.SubAgentConfig>({
  cache_ttl: '',
  timeout: '',
})
const sysWorkMode = ref('coding')
const sysDirty = ref(false)
const sysSaving = ref(false)

async function loadSystemConfig() {
  try {
    const sc = await api.getSystemConfig()
    sysLimits.value = sc.limits
    sysSubAgent.value = sc.sub_agent
    sysWorkMode.value = sc.work_mode?.default || 'coding'
    chatState.globalWorkMode = sysWorkMode.value
    sysDirty.value = false
  } catch { /* ignore */ }
}

function markSysDirty() { sysDirty.value = true }

async function saveSystemConfig() {
  sysSaving.value = true
  try {
    const patch: Record<string, unknown> = {}
    const limits: Record<string, unknown> = {}
    const sa: Record<string, unknown> = {}

    limits.auto_compact_buffer = sysLimits.value.auto_compact_buffer
    limits.tool_result_exec_cap = sysLimits.value.tool_result_exec_cap
    limits.tool_result_read_cap = sysLimits.value.tool_result_read_cap
    limits.tool_result_default_cap = sysLimits.value.tool_result_default_cap
    limits.prune_after_rounds = sysLimits.value.prune_after_rounds
    limits.max_rounds = sysLimits.value.max_rounds
    limits.max_stored_messages = sysLimits.value.max_stored_messages
    patch.limits = limits

    sa.cache_ttl = sysSubAgent.value.cache_ttl
    sa.timeout = sysSubAgent.value.timeout
    patch.sub_agent = sa
    patch.work_mode = { default: sysWorkMode.value }

    const updated = await api.updateSystemConfig(patch)
    chatState.globalWorkMode = updated.work_mode?.default || sysWorkMode.value
    sysDirty.value = false
    message.success('系统配置已保存')
  } catch (e: any) {
    message.error('保存失败: ' + (e?.message || e))
  } finally {
    sysSaving.value = false
  }
}

function resetSystemConfig() {
  sysLimits.value = {
    auto_compact_buffer: 20000,
    tool_result_exec_cap: 4000,
    tool_result_read_cap: 8000,
    tool_result_default_cap: 6000,
    prune_after_rounds: 15,
    max_rounds: 300,
    max_stored_messages: 0,
  }
  sysSubAgent.value = { cache_ttl: '', timeout: '' }
  sysWorkMode.value = 'coding'
  sysDirty.value = true
}

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
  } else if (v === 'knowledge') {
    refreshKB()
    refreshKBModels()
  } else if (v === 'system') {
    loadSystemConfig()
  } else if (v === 'browser') {
    refreshBrowser()
    startBrowserPolling()
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

onBeforeUnmount(() => {
  for (const timer of Object.values(kbScanTimers)) {
    clearInterval(timer as number)
  }
})

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
const repoSkillFilter = ref('')
const installedSkillFilter = ref('')

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
  const q = installedSkillFilter.value.trim().toLowerCase()
  if (!q) return loadedSkills.value
  return loadedSkills.value.filter(s =>
    s.name.toLowerCase().includes(q) || s.description.toLowerCase().includes(q),
  )
})

const filteredSearchResults = computed(() => {
  const q = repoSkillFilter.value.trim().toLowerCase()
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

// --- Browser Control ---
const browserEnabled = ref(false)
const browserList = ref<api.BrowserInfo[]>([])
const browserWSURL = ref('')
const browserHTTPURL = ref('')
let browserPollTimer: ReturnType<typeof setInterval> | null = null

async function refreshBrowser() {
  try {
    const [status, list] = await Promise.all([api.getBrowserStatus(), api.listBrowsers()])
    browserEnabled.value = status.enabled
    browserList.value = list.browsers || []
    browserWSURL.value = status.ws_url || ''
    browserHTTPURL.value = status.http_url || ''
  } catch (e: any) {
    // Browser endpoints may not be initialised yet; swallow.
  }
}

async function onToggleBrowser(v: boolean) {
  try {
    await api.updateBrowserConfig(v)
    browserEnabled.value = v
  } catch (e: any) {
    message.error(`切换浏览器控制失败: ${e.message}`)
  }
}

function copyBrowserURL() {
  const url = browserHTTPURL.value
  if (!url) return
  // Wails WebView2 does not support navigator.clipboard API —
  // fall back to document.execCommand('copy') with a textarea.
  if (navigator.clipboard && typeof navigator.clipboard.writeText === 'function') {
    navigator.clipboard.writeText(url).then(() => {
      message.success('服务器地址已复制到剪贴板')
    }).catch(() => fallbackCopy(url))
  } else {
    fallbackCopy(url)
  }
}

function fallbackCopy(text: string) {
  try {
    const ta = document.createElement('textarea')
    ta.value = text
    ta.style.position = 'fixed'
    ta.style.left = '-9999px'
    ta.style.opacity = '0'
    document.body.appendChild(ta)
    ta.focus()
    ta.select()
    const ok = document.execCommand('copy')
    document.body.removeChild(ta)
    if (ok) {
      message.success('服务器地址已复制到剪贴板')
    } else {
      throw new Error('execCommand copy failed')
    }
  } catch {
    // Last resort: select the visible <code> element
    const codeEls = document.querySelectorAll('.browser-server-url-box code')
    if (codeEls.length > 0) {
      const range = document.createRange()
      range.selectNodeContents(codeEls[0])
      const sel = window.getSelection()
      sel?.removeAllRanges()
      sel?.addRange(range)
      message.info('请手动 Ctrl+C 复制选中的地址')
    } else {
      message.error('复制失败，请手动选择地址复制')
    }
  }
}

function startBrowserPolling() {
  if (browserPollTimer) return
  browserPollTimer = setInterval(() => {
    if (tab.value === 'browser') refreshBrowser()
    else { stopBrowserPolling() }
  }, 3000)
}

function stopBrowserPolling() {
  if (browserPollTimer) { clearInterval(browserPollTimer); browserPollTimer = null }
}

onUnmounted(() => stopBrowserPolling())

// --- Knowledge Base state ---
const kbEnabled = ref(false)
const kbAutoIndex = ref(false)
const kbBases = ref<api.KnowledgeBaseItem[]>([])
const kbSelectedName = ref<string | null>(null)
const kbSelected = computed(() => kbBases.value.find(b => b.name === kbSelectedName.value) || null)
const showAddKB = ref(false)
const newKBName = ref('')
const newKBPath = ref('')
const kbModels = ref<api.KnowledgeModel[]>([])
const scanningKBs = ref<Set<string>>(new Set())
const kbScanStatus = ref<Map<string, { current: number; total: number; chunks: number; done: boolean; error?: string }>>(new Map())
let kbScanTimers: Record<string, ReturnType<typeof setInterval>> = {}

// Three-level index nodes tree view
const kbNodes = ref<api.NodeTreeItem[]>([])
const kbNodesLoading = ref(false)
const kbNodeFilter = ref('')

const kbSelectedNodeKeys = ref<string[]>([])
const kbExpandedNodeKeys = ref<string[]>([])
const kbActiveNode = ref<api.NodeTreeItem | null>(null)
const kbActiveNodeContent = ref<api.NodeContentItem[]>([])
const kbActiveChildId = ref<number | null>(null)

const l1Node = computed(() => kbNodes.value.find(n => n.level === 1) || null)
const l2Nodes = computed(() => kbNodes.value.filter(n => n.level === 2))

function getChildren(parentId: number) {
  return kbNodes.value.filter(n => n.parent_id === parentId && n.level === 3)
}

const kbActiveChildren = computed(() => {
  if (!kbActiveNode.value || kbActiveNode.value.level !== 2) return []
  return getChildren(kbActiveNode.value.id)
})

interface TreeNode {
  key: string
  label: string
  level: number
  nodeData: api.NodeTreeItem
  children?: TreeNode[]
  isLeaf?: boolean
}

const kbTreeNodeData = computed<any[]>(() => {
  const nodes = kbNodes.value
  if (nodes.length === 0) return []

  const l1 = nodes.find(n => n.level === 1)
  if (!l1) return []

  function buildChildren(parentId: number): TreeNode[] {
    return nodes
      .filter(n => n.parent_id === parentId && n.level > 1)
      .sort((a, b) => a.title.localeCompare(b.title))
      .map(n => {
        const grandchildren = nodes.filter(gc => gc.parent_id === n.id && gc.level === 3)
        return {
          key: `node-${n.id}`,
          label: n.title,
          level: n.level,
          nodeData: n,
          isLeaf: grandchildren.length === 0,
          children: grandchildren.length > 0
            ? grandchildren.map(gc => ({
                key: `node-${gc.id}`,
                label: gc.title || '(无标题)',
                level: gc.level,
                nodeData: gc,
                isLeaf: true,
              }))
            : undefined,
        }
      })
  }

  return [{
    key: `node-${l1.id}`,
    label: l1.title,
    level: l1.level,
    nodeData: l1,
    children: buildChildren(l1.id),
  }]
})

function nodeIcon(level: number) {
  if (level === 1) return Clipboard
  if (level === 2) return File
  return Hash
}

function renderTreeLabel({ option }: any) {
  const n = option.nodeData as api.NodeTreeItem
  const Icon = nodeIcon(n.level)
  if (n.level === 1) {
    return h('span', { class: 'kb-tree-label l1' }, [h(Icon, { size: 14, class: 'kb-tree-icon' }), n.title])
  }
  if (n.level === 2) {
    return h('span', { class: 'kb-tree-label l2' }, [
      h(Icon, { size: 14, class: 'kb-tree-icon' }),
      h('span', { class: 'kb-tree-label-text' }, n.title),
      n.kind ? h('span', { class: 'kb-tree-label-tag' }, n.kind) : null,
      n.child_count > 0 ? h('span', { class: 'kb-tree-label-cnt' }, `${n.child_count}`) : null,
    ])
  }
  return h('span', { class: 'kb-tree-label l3' }, [
    h(Icon, { size: 14, class: 'kb-tree-icon' }),
    h('span', { class: 'kb-tree-label-text' }, n.title || '(无标题)'),
    n.content_count > 0 ? h('span', { class: 'kb-tree-label-cnt' }, `${n.content_count}`) : null,
  ])
}

function renderTreeSuffix({ option }: any) {
  const n = option.nodeData as api.NodeTreeItem
  if (n.level <= 1) return null
  return h(NPopconfirm, {
    positiveText: '删除',
    negativeText: '取消',
    placement: 'left-start',
    onPositiveClick: (e: Event) => { e.stopPropagation(); onDeleteNode(n.id) },
  }, {
    trigger: () => h(NButton, { size: 'tiny', quaternary: true, type: 'error', onClick: (e: Event) => e.stopPropagation() }, {
      default: () => h(Trash2, { size: 12 }),
    }),
    default: () => `确定删除「${n.title}」${n.level === 2 ? '及其所有章节和内容' : '及其内容'}？`,
  })
}

async function refreshKB() {
  try {
    const cfg = await api.getKnowledgeConfig()
    kbEnabled.value = cfg.enabled
    kbAutoIndex.value = cfg.auto_index
    kbBases.value = await api.getKnowledgeBases()
    if (!kbSelectedName.value && kbBases.value.length > 0) {
      kbSelectedName.value = kbBases.value[0].name
    } else if (kbSelectedName.value && !kbBases.value.find(b => b.name === kbSelectedName.value)) {
      kbSelectedName.value = kbBases.value[0]?.name ?? null
    }
    for (const b of kbBases.value) {
      if (b.status === 'scanning') {
        scanningKBs.value = new Set(scanningKBs.value).add(b.name)
        pollScan(b.name)
      }
    }
  } catch (e: any) {
    message.error(`加载知识库配置失败: ${e.message}`)
  }
}

async function refreshKBModels() {
  try { kbModels.value = await api.listKnowledgeModels() || [] } catch {}
}

async function onToggleKBEnabled(v: boolean) {
  try { await api.updateKnowledgeConfig({ enabled: v }); kbEnabled.value = v; bumpKBConfigVersion() } catch (e: any) { message.error(`切换失败: ${e.message}`) }
}

async function onToggleKBAutoIndex(v: boolean) {
  try { await api.updateKnowledgeConfig({ auto_index: v }); kbAutoIndex.value = v; bumpKBConfigVersion() } catch (e: any) { message.error(`切换失败: ${e.message}`) }
}

async function onAddKB() {
  if (!newKBName.value.trim() || !newKBPath.value.trim()) { message.warning('名称和路径为必填'); return }
  try {
    await api.addKnowledgeBase({ name: newKBName.value.trim(), path: newKBPath.value.trim(), enabled: true, file_types: [], scan_model: '', scan_media_types: [], exclude_patterns: [], max_file_size: 0 })
    message.success('已添加')
    showAddKB.value = false
    newKBName.value = ''; newKBPath.value = ''
    await refreshKB()
    kbSelectedName.value = kbBases.value[kbBases.value.length - 1]?.name ?? null
    bumpKBConfigVersion()
  } catch (e: any) { message.error(`添加失败: ${e.message}`) }
}

async function onDeleteKB(name: string) {
  try { await api.removeKnowledgeBase(name); message.success('已删除'); await refreshKB(); bumpKBConfigVersion() } catch (e: any) { message.error(`删除失败: ${e.message}`) }
}

async function onScanKB(name: string) {
  try {
    await api.scanKnowledgeBase(name)
    scanningKBs.value = new Set(scanningKBs.value).add(name)
    kbScanStatus.value.set(name, { current: 0, total: 0, chunks: 0, done: false })
    pollScan(name)
  } catch (e: any) { message.error(`扫描失败: ${e.message}`) }
}

function pollScan(name: string) {
  const timer = setInterval(async () => {
    try {
      const s = await api.getScanStatus(name)
      kbScanStatus.value.set(name, { current: s.current, total: s.total, chunks: s.chunks, done: s.done, error: s.error })
      if (s.done) {
        clearInterval(timer)
        scanningKBs.value = new Set([...scanningKBs.value].filter(n => n !== name))
        delete kbScanTimers[name]
        await refreshKB()
        bumpKBConfigVersion()
      }
    } catch { clearInterval(timer); scanningKBs.value.delete(name); delete kbScanTimers[name] }
  }, 800)
  kbScanTimers[name] = timer
}

async function onCancelScan(name: string) {
  try {
    await api.cancelScan(name)
    clearInterval(kbScanTimers[name])
    delete kbScanTimers[name]
    scanningKBs.value = new Set([...scanningKBs.value].filter(n => n !== name))
  } catch (e: any) { message.error(`取消失败: ${e.message}`) }
}

async function onClearKB(name: string) {
  try {
    await api.clearKnowledgeBase(name)
    message.success('已清除')
    kbScanStatus.value.delete(name)
    kbActiveNode.value = null
    kbActiveNodeContent.value = []
    await refreshKB()
    bumpKBConfigVersion()
  } catch (e: any) { message.error(`清除失败: ${e.message}`) }
}

function onToggleKBSwitch(name: string, enabled: boolean) {
  const idx = kbBases.value.findIndex(b => b.name === name)
  if (idx < 0) return
  const updated = { ...kbBases.value[idx], enabled }
  api.updateKnowledgeConfig({ bases: kbBases.value.map((b, i) => i === idx ? updated : b) }).then(() => {
    kbBases.value[idx] = updated
    bumpKBConfigVersion()
  }).catch(e => message.error(`更新失败: ${e.message}`))
}

function onUpdateKBField(name: string, field: string, value: any) {
  const idx = kbBases.value.findIndex(b => b.name === name)
  if (idx < 0) return
  const updated = { ...kbBases.value[idx], [field]: value }
  api.updateKnowledgeConfig({ bases: kbBases.value.map((b, i) => i === idx ? updated : b) }).then(() => {
    kbBases.value[idx] = updated
    bumpKBConfigVersion()
  }).catch(e => message.error(`更新失败: ${e.message}`))
}

async function loadKBNodes(name: string) {
  if (!name) { kbNodes.value = []; kbActiveNode.value = null; return }
  kbNodesLoading.value = true
  try {
    const res = await api.listKnowledgeNodes(name)
    kbNodes.value = res.nodes || []
    if (kbNodes.value.length > 0) {
      const allIds = kbNodes.value.map(n => `node-${n.id}`)
      const l1 = kbNodes.value.find(n => n.level === 1)
      const sel = l1 ? [`node-${l1.id}`] : []
      kbExpandedNodeKeys.value = allIds
      kbSelectedNodeKeys.value = sel
      if (l1) await selectNode(l1, name)
    }
  } catch { kbNodes.value = [] }
  finally { kbNodesLoading.value = false }
}

async function selectNode(node: api.NodeTreeItem, baseName: string) {
  kbActiveNode.value = node
  kbActiveChildId.value = null
  if (node.level > 1 && node.content_count > 0) {
    try {
      const r = await api.getNodeContent(baseName, node.id)
      kbActiveNodeContent.value = r.contents || []
    } catch { kbActiveNodeContent.value = [] }
  } else {
    kbActiveNodeContent.value = []
  }
}

function selectChildNode(child: api.NodeTreeItem) {
  kbActiveChildId.value = child.id
  if (!kbSelected.value) return
  selectNode(child, kbSelected.value.name)
}

async function onTreeNodeSelect(keys: string[]) {
  if (keys.length === 0) return
  kbSelectedNodeKeys.value = keys
  const key = keys[0]
  const id = parseInt(key.replace('node-', ''), 10)
  const node = kbNodes.value.find(n => n.id === id)
  if (node && kbSelected.value) {
    await selectNode(node, kbSelected.value.name)
  }
}

function onTreeNodeExpand(keys: string[]) {
  kbExpandedNodeKeys.value = keys
}

async function onDeleteNode(nodeId: number) {
  if (!kbSelected.value) return
  try {
    await api.deleteKnowledgeNode(kbSelected.value.name, nodeId)
    message.success('已删除')
    if (kbActiveNode.value?.id === nodeId) {
      kbActiveNode.value = null
      kbActiveNodeContent.value = []
    }
    await loadKBNodes(kbSelected.value.name)
    await refreshKB()
    bumpKBConfigVersion()
  } catch (e: any) { message.error(`删除失败: ${e.message}`) }
}

watch(kbSelectedName, (name) => {
  kbActiveNode.value = null
  kbActiveNodeContent.value = []
  kbNodeFilter.value = ''
  loadKBNodes(name || '')
})

const mediaTypeOptions = [
  { label: '图片 (.png .jpg .gif .webp .bmp)', value: 'image' },
  { label: '视频 (.mp4 .mov .webm .avi)', value: 'video' },
  { label: '音频 (.mp3 .wav .ogg .m4a)', value: 'audio' },
  { label: '文档 (.pdf)', value: 'pdf' },
]

const kbModelOptions = computed(() => [
  { label: '纯文本解析（推荐，零 API 消耗）', value: '' },
  ...kbModels.value.map(m => {
    const suffix = m.supports_vision ? ' · 视觉' : ''
    return { label: `${m.provider} / ${m.model}${suffix}`, value: `${m.provider}/${m.model}` }
  }),
])

function scanLabel(name: string) {
  const s = kbScanStatus.value.get(name)
  if (!s) return '开始扫描'
  if (s.error) return '扫描失败'
  if (s.done) return `✓ ${s.chunks} sections`
  return `扫描中 ${s.current}/${s.total}`
}

function kbModelSupportsVision(scanModel: string) {
  const parts = scanModel.split('/')
  if (parts.length !== 2) return false
  return kbModels.value.find(m => m.provider === parts[0] && m.model === parts[1])?.supports_vision || false
}
</script>
<template>
  <!-- AppSettingsLayout (PR #8) provides the new shell:
       200px left nav + sticky header + content area. The
       old top-tab NTabs is kept INSIDE the layout slot
       purely as a content container — its tab bar is hidden
       via the .settings-no-bar class on the wrapper, and
       the nav column drives the active pane via the same
       `tab` ref both v-models share. show is v-modeled to
       App.vue so closing the X actually unmounts the
       component (rather than just hiding the layout). -->
  <AppSettingsLayout
    v-model:show="show"
    v-model:current="tab"
    :tabs="settingsTabs"
    title="应用设置"
    @close="close"
  >
    <NTabs
      v-model:value="tab"
      type="line"
      animated
      class="settings-no-bar"
      style="flex: 1; min-height: 0; display: flex; flex-direction: column;"
    >
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
                      <NButton size="tiny" quaternary type="error" @click.stop title="删除供应商" class="provider-del-btn">
                        <X :size="12" />
                      </NButton>
                    </template>
                    确定删除 "{{ p.name }}" 及其下所有模型？
                  </NPopconfirm>
                </div>
                <div class="provider-item-sub">
                  <NTag size="tiny" :bordered="false">{{ p.protocol }}</NTag>
                  <span class="muted">{{ p.models?.length || 0 }} 模型</span>
                </div>
              </div>
              <div v-if="providers.length === 0" class="settings-empty">还没有 provider</div>
            </div>
          </div>

          <!-- Right: detail pane -->
          <div class="provider-detail">
            <div v-if="!selected" class="settings-empty">← 选择左侧的 provider</div>
            <template v-else>
              <!-- Basic info form. PR #9: refactored to use
                   the unified .settings-section +
                   .settings-form pattern. The old
                   .detail-section / .detail-form / NSpace
                   wrappers produced inconsistent spacing;
                   the new classes give 14px row gap, 6px
                   label-to-input gap, and a 1px border under
                   the section header. -->
              <div class="settings-section">
                <div class="settings-section-header">
                  <h3 class="settings-section-title">基本信息</h3>
                  <div class="settings-form-actions">
                    <NButton size="small" @click="hydrateEditForm(selected.name)" :disabled="dirty.size === 0">重置</NButton>
                    <NButton size="small" @click="onSaveProvider" type="primary" :disabled="dirty.size === 0">
                      保存
                    </NButton>
                  </div>
                </div>
                <p class="settings-section-description">
                  提供商的基础信息。点击「保存」将修改持久化到本地数据库。
                </p>
                <div class="settings-form">
                  <div class="settings-form-row">
                    <label class="settings-form-label">名称</label>
                    <NInput
                      v-model:value="editName"
                      size="small"
                      :status="dirty.has('name') && editName.trim() === selected.name ? 'warning' : undefined"
                      @update:value="markDirty('name')"
                    />
                    <span class="settings-form-hint">本地唯一标识，新建后不可改</span>
                  </div>
                  <div class="settings-form-row">
                    <label class="settings-form-label">协议</label>
                    <NSelect
                      v-model:value="editProtocol"
                      :options="protocolOptions"
                      size="small"
                      @update:value="markDirty('protocol')"
                    />
                    <span class="settings-form-hint">OpenAI 兼容 / Anthropic / 自定义</span>
                  </div>
                  <div class="settings-form-row">
                    <label class="settings-form-label">Base URL</label>
                    <NInput
                      v-model:value="editBaseURL"
                      size="small"
                      placeholder="https://api.openai.com/v1"
                      @update:value="markDirty('base_url')"
                    />
                    <span class="settings-form-hint">API endpoint，OpenAI 兼容服务可填自有地址</span>
                  </div>
                  <div class="settings-form-row">
                    <label class="settings-form-label">API Key</label>
                    <NInput
                      v-model:value="editAPIKey"
                      size="small"
                      type="password"
                      show-password-on="click"
                      placeholder="sk-..."
                      @update:value="markDirty('api_key')"
                    />
                    <span class="settings-form-hint">留空保留原值，点击眼睛图标可临时显示</span>
                  </div>
                  <div class="settings-form-row">
                    <div class="settings-form-toggle">
                      <NSwitch
                        :value="editIsDefault"
                        :disabled="selected.is_default"
                        @update:value="(v: boolean) => { editIsDefault = v; markDirty('is_default') }"
                      />
                      <label class="settings-form-label" for="provider-default-switch">设为默认</label>
                    </div>
                    <span v-if="selected.is_default" class="settings-form-hint">当前已是默认</span>
                    <span v-else class="settings-form-hint">未选 provider 时使用第一个可用的</span>
                  </div>
                </div>
              </div>

              <!-- Model table -->
              <!-- Model table. PR #9: same .settings-section
                   treatment as the basic info form. The
                   add-model form is wrapped in a
                   .settings-card so it visually separates
                   from the existing model list below it. -->
              <div class="settings-section">
                <div class="settings-section-header">
                  <h3 class="settings-section-title">模型 ({{ selected.models?.length || 0 }})</h3>
                  <div class="settings-form-actions">
                    <NButton size="small" @click="onFetchUpstreamModels" :loading="fetchingUpstream">
                      获取模型
                    </NButton>
                    <NButton size="small" type="primary" ghost @click="onShowAddModel">
                      {{ showAddModel ? '取消' : '+ 添加模型' }}
                    </NButton>
                  </div>
                </div>
                <div v-if="showAddModel" class="settings-card">
                  <div class="settings-form">
                    <div v-if="editingModelName" class="settings-form-hint">编辑模型: <code>{{ editingModelName }}</code></div>
                    <div class="settings-form-row">
                      <label class="settings-form-label">模型 ID</label>
                      <NInput
                        v-model:value="editModelName"
                        :disabled="!!editingModelName"
                        placeholder="例: gpt-4o-mini"
                        size="small"
                      />
                    </div>
                    <div class="settings-form-row">
                      <label class="settings-form-label">显示名</label>
                      <NInput v-model:value="editModelDisplay" placeholder="例: GPT-4o mini" size="small" />
                    </div>
                    <div class="settings-form-row">
                      <label class="settings-form-label">上下文 (tokens)</label>
                      <NInputNumber v-model:value="editModelCtx" :min="0" :step="1024" placeholder="例: 128000" size="small" />
                    </div>
                    <div class="settings-form-row">
                      <label class="settings-form-label">最大输出 (tokens)</label>
                      <NInputNumber v-model:value="editModelOut" :min="0" :step="512" placeholder="例: 4096" size="small" />
                    </div>
                    <div class="settings-form-row">
                      <div class="settings-form-toggle">
                        <NSwitch v-model:value="editModelVision" />
                        <label class="settings-form-label" for="model-vision-switch">支持视觉输入</label>
                      </div>
                      <span class="settings-form-hint">开启后用户可发送图片附件</span>
                    </div>
                    <div class="settings-form-actions">
                      <NButton size="small" @click="onCancelEditModel">取消</NButton>
                      <NButton type="primary" size="small" @click="editingModelName ? onSaveModel() : onAddModel()">
                        {{ editingModelName ? '保存修改' : '添加模型' }}
                      </NButton>
                    </div>
                  </div>
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
                      <NButton size="tiny" quaternary @click="onEditModel(m)" title="编辑" aria-label="编辑">
                        <Pencil :size="12" />
                      </NButton>
                      <NButton v-if="!m.default" size="tiny" quaternary @click="onSetDefaultModel(m.name)" title="设为默认" aria-label="设为默认">
                        <Star :size="12" />
                      </NButton>
                        <NPopconfirm @positive-click="onDeleteModel(m.name)" positive-text="删除" negative-text="取消">
                        <template #trigger>
                          <NButton size="tiny" quaternary type="error" title="删除" aria-label="删除">
                            <X :size="12" />
                          </NButton>
                        </template>
                        确定删除模型 "{{ m.name }}"？
                      </NPopconfirm>
                    </div>
                  </div>
                </div>
                <div v-else class="settings-empty">还没有模型。点击「+ 添加模型」配置。</div>
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
          <!-- PR #9: section header now uses the unified
               .settings-section pattern. Action buttons
               (新增风格) live in the right-aligned
               .settings-form-actions slot. -->
          <div class="settings-section">
            <div class="settings-section-header">
              <h3 class="settings-section-title">已配置的风格</h3>
              <div class="settings-form-actions">
                <NButton size="small" type="primary" ghost
                  @click="() => { showAddStyle = !showAddStyle; if (showAddStyle) resetNewStyle() }">
                  {{ showAddStyle ? '取消' : '+ 新增风格' }}
                </NButton>
              </div>
            </div>
            <p class="settings-section-description">
              风格定义 LLM 的人设与记忆。内置风格不可编辑，可复制后创建自定义版本。
            </p>
          </div>
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

          <!-- 旧的「+ 新增风格」按钮已迁到上方 section header (PR #9) -->

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

      <NTabPane name="system" tab="系统配置">
        <div class="system-config-shell">
          <div class="system-config-scroll">
            <div class="system-config-body">
              <!-- PR #9: top-level section header for the
                   system config. The inner NCollapse groups
                   still use the existing .sys-* class names
                   for backward compat. -->
              <div class="settings-section">
                <div class="settings-section-header">
                  <h3 class="settings-section-title">系统行为</h3>
                  <p class="settings-section-description" style="margin: 0; flex: 1">
                    上下文、Agent 循环、子代理的全局行为。修改后点击右下「保存」生效。
                  </p>
                </div>
              </div>
              <NCollapse class="sys-collapse" :default-expanded-names="['work-mode', 'context', 'agent', 'subagent']">
                <NCollapseItem title="工作模式" name="work-mode">
                  <div class="sys-form-grid">
                    <div class="sys-form-row">
                      <span class="sys-label">新会话默认侧重点</span>
                      <NRadioGroup v-model:value="sysWorkMode" size="small" @update:value="markSysDirty">
                        <NRadioButton value="coding">编码</NRadioButton>
                        <NRadioButton value="daily">日常工作</NRadioButton>
                      </NRadioGroup>
                      <span class="sys-hint">风格只决定怎么说；工作模式决定优先处理哪类任务。</span>
                    </div>
                  </div>
                </NCollapseItem>

                <NCollapseItem title="上下文管理" name="context">
                  <div class="sys-form-grid">
                    <div class="sys-form-row">
                      <span class="sys-label">历史消息加载条数</span>
                      <NInputNumber v-model:value="sysLimits.max_stored_messages" :min="0" :step="10" size="small" style="width:100px" @update:value="markSysDirty" />
                      <span class="sys-hint">0 = 按模型上下文自动计算</span>
                    </div>
                    <div class="sys-form-row">
                      <span class="sys-label">自动压缩缓冲区</span>
                      <NInputNumber v-model:value="sysLimits.auto_compact_buffer" :min="0" :step="1024" size="small" style="width:100px" @update:value="markSysDirty" />
                      <span class="sys-hint">tokens，默认 20000</span>
                    </div>
                    <div class="sys-form-row">
                      <span class="sys-label">工具结果截断轮次</span>
                      <NInputNumber v-model:value="sysLimits.prune_after_rounds" :min="0" :step="5" size="small" style="width:100px" @update:value="markSysDirty" />
                      <span class="sys-hint">轮后内容置 [pruned]，0 = 禁用</span>
                    </div>
                  </div>
                </NCollapseItem>

                <NCollapseItem title="Agent 执行限制" name="agent">
                  <div class="sys-form-grid">
                    <div class="sys-form-row">
                      <span class="sys-label">最大回合数</span>
                      <NInputNumber v-model:value="sysLimits.max_rounds" :min="0" :step="10" size="small" style="width:100px" @update:value="markSysDirty" />
                      <span class="sys-hint">0 = 不限制，默认 300</span>
                    </div>
                    <div class="sys-form-row">
                      <span class="sys-label">exec_command 截断</span>
                      <NInputNumber v-model:value="sysLimits.tool_result_exec_cap" :min="0" :step="512" size="small" style="width:100px" @update:value="markSysDirty" />
                      <span class="sys-hint">bytes，默认 4000</span>
                    </div>
                    <div class="sys-form-row">
                      <span class="sys-label">read_file 截断</span>
                      <NInputNumber v-model:value="sysLimits.tool_result_read_cap" :min="0" :step="512" size="small" style="width:100px" @update:value="markSysDirty" />
                      <span class="sys-hint">bytes，默认 8000</span>
                    </div>
                    <div class="sys-form-row">
                      <span class="sys-label">默认截断</span>
                      <NInputNumber v-model:value="sysLimits.tool_result_default_cap" :min="0" :step="512" size="small" style="width:100px" @update:value="markSysDirty" />
                      <span class="sys-hint">bytes，默认 6000（含子代理/task 结果）</span>
                    </div>
                  </div>
                </NCollapseItem>

                <NCollapseItem title="子代理" name="subagent">
                  <div class="sys-form-grid">
                    <div class="sys-form-row">
                      <span class="sys-label">结果缓存 TTL</span>
                      <NInput v-model:value="sysSubAgent.cache_ttl" size="small" placeholder="例如: 10m" style="width:140px" @update:value="markSysDirty" />
                      <span class="sys-hint">留空 = 禁用缓存</span>
                    </div>
                    <div class="sys-form-row">
                      <span class="sys-label">超时时间</span>
                      <NInput v-model:value="sysSubAgent.timeout" size="small" placeholder="例如: 5m" style="width:140px" @update:value="markSysDirty" />
                      <span class="sys-hint">留空 = 默认 5 分钟</span>
                    </div>
                  </div>
                </NCollapseItem>
              </NCollapse>
            </div>
          </div>

          <div class="sys-actions">
            <NButton size="small" @click="resetSystemConfig">恢复默认</NButton>
            <NButton size="small" type="primary" :disabled="!sysDirty" :loading="sysSaving" @click="saveSystemConfig">保存</NButton>
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

      <NTabPane name="skills" tab="技能" style="flex: 1; min-height: 0; display: flex; flex-direction: column">
        <!-- Repo chips (top bar) -->
        <div class="skill-repos-bar">
          <span class="skill-hint" style="white-space:nowrap">官方仓库</span>
          <div class="repo-chips">
            <NButton v-for="r in builtInRepos" :key="r.url" size="tiny"
              :type="activeRepoUrl === r.url ? 'primary' : 'default'"
              @click="onSelectRepo(r.url)">{{ r.name }}</NButton>
          </div>
          <span v-if="savedRepos.length" class="skill-hint" style="margin-left:8px">我的</span>
          <div v-if="savedRepos.length" class="repo-chips">
            <template v-for="r in savedRepos" :key="r.url">
              <NButton size="tiny" :type="activeRepoUrl === r.url ? 'primary' : 'default'"
                @click="onSelectRepo(r.url)">{{ r.name }}</NButton>
              <NButton size="tiny" quaternary @click="onRemoveRepo(r.url)"
                style="color:var(--warn);font-size:10px;min-width:16px;padding:0 2px">×</NButton>
            </template>
          </div>
          <NButton size="tiny" quaternary @click="showAddRepo = true" style="margin-left:2px">+添加</NButton>
          <span v-if="searching" style="font-size:11px;color:var(--text-4);margin-left:8px">加载中…</span>
        </div>

        <!-- Two-column body -->
        <div class="skill-columns">
          <!-- Left: repo search results -->
          <div class="skill-col skill-col-left">
            <div class="skill-col-header">
              <span class="skill-section-title">仓库技能</span>
              <NInput v-model:value="repoSkillFilter" placeholder="筛选…" size="tiny" clearable style="width:130px" />
            </div>
            <div class="skill-col-body">
              <div v-if="!activeRepoUrl" class="empty-hint">选择上方仓库查看可安装的技能</div>
              <div v-else-if="searching" class="empty-hint">正在加载…</div>
              <div v-else-if="!filteredSearchResults.length" class="empty-hint">
                {{ searchResults.length ? '无匹配' : '该仓库无可安装技能' }}
              </div>
              <template v-else>
                <div v-for="r in filteredSearchResults" :key="r.name" class="skill-result-row">
                  <div class="skill-result-info">
                    <span class="skill-result-name">{{ r.name }}</span>
                    <span class="skill-result-desc">{{ r.description }}</span>
                  </div>
                  <NButton size="tiny" type="primary" :loading="installing === r.name"
                    @click="onInstallSkill(r.name, r.url)">安装</NButton>
                </div>
              </template>
            </div>
          </div>

          <!-- Right: installed skills -->
          <div class="skill-col skill-col-right">
            <div class="skill-col-header">
              <span class="skill-section-title">已安装（{{ loadedSkills.length }}）</span>
              <NInput v-model:value="installedSkillFilter" placeholder="筛选…" size="tiny" clearable style="width:130px" />
            </div>
            <div class="skill-col-body">
              <div v-if="!filteredSkills.length" class="empty-hint">{{ installedSkillFilter ? '无匹配' : '暂无已安装技能' }}</div>
              <template v-else>
                <div v-for="s in filteredSkills" :key="s.name" class="skill-result-row">
                  <div class="skill-result-info">
                    <span class="skill-result-name">{{ s.name }}</span>
                    <span class="skill-result-desc">{{ s.description }}</span>
                  </div>
                  <NButton size="tiny" quaternary @click="onDeleteSkill(s.name)" style="color:var(--warn)">删除</NButton>
                </div>
              </template>
            </div>
          </div>
        </div>
      </NTabPane>

      <NTabPane name="mcp" tab="MCP" style="flex: 1; min-height: 0; overflow: auto">
        <!-- PR #9 follow-up: refactored to the unified
             .settings-section + .settings-form pattern. The
             old version had a single flex column with hard-coded
             inline styles, inconsistent typography, and a tight
             add-form. The new version mirrors the system tab
             rhythm: 14px row gap, 12.5px label, 11.5px hint,
             dividers between rows, and a single section header
             explaining the feature. -->
        <div class="settings-section">
          <div class="settings-section-header">
            <h3 class="settings-section-title">MCP 服务器</h3>
            <div class="settings-form-actions">
              <NButton size="small" type="primary" ghost @click="showAddMCP = !showAddMCP">
                {{ showAddMCP ? '取消' : '+ 添加' }}
              </NButton>
            </div>
          </div>
          <p class="settings-section-description">
            MCP (Model Context Protocol) 让 LLM 通过标准化的 JSON-RPC 访问外部工具和资源。
            关闭后 LLM 完全看不到 MCP 注册的工具。
          </p>
          <div class="settings-form-row">
            <div class="settings-form-toggle">
              <NSwitch :value="mcpGlobalEnabled" @update:value="onToggleMCPGlobal" size="small" />
              <label class="settings-form-label" for="mcp-global-switch">全局启用 MCP</label>
            </div>
            <span class="settings-form-hint">关闭后所有 MCP 服务器都不会被加载</span>
          </div>
        </div>

        <!-- Server list section. The list of configured
             servers is rendered as .settings-card items,
             matching the visual weight of the providers tab
             model list. -->
        <div class="settings-section">
          <div class="settings-section-header">
            <h3 class="settings-section-title">已配置 ({{ mcpServers.length }})</h3>
          </div>
          <div v-if="!mcpServers.length && !showAddMCP" class="settings-empty">
            暂无 MCP 服务器，点击「+ 添加」开始配置
          </div>

          <!-- Add form. Wrapped in .settings-card so it
               visually separates from the server list. -->
          <div v-if="showAddMCP" class="settings-card">
            <div class="settings-form">
              <!-- Transport selector as inline radio-style
                   buttons. The two buttons (Stdio / SSE) live
                   in a 4px-gap row, matching the .settings-form
                   rhythm. -->
              <div class="settings-form-row">
                <label class="settings-form-label">传输方式</label>
                <div style="display: flex; gap: 6px;">
                  <NButton
                    size="small"
                    :type="newMCPType === 'stdio' ? 'primary' : 'default'"
                    @click="newMCPType = 'stdio'"
                  >
                    Stdio
                  </NButton>
                  <NButton
                    size="small"
                    :type="newMCPType === 'sse' ? 'primary' : 'default'"
                    @click="newMCPType = 'sse'"
                  >
                    SSE
                  </NButton>
                </div>
              </div>
              <div class="settings-form-row">
                <label class="settings-form-label">名称</label>
                <NInput v-model:value="newMCPName" placeholder="如：playwright" size="small" style="max-width: 320px" />
              </div>
              <template v-if="newMCPType === 'stdio'">
                <div class="settings-form-row">
                  <label class="settings-form-label">命令</label>
                  <NInput v-model:value="newMCPCommand" placeholder="如：npx" size="small" style="max-width: 320px" />
                </div>
                <div class="settings-form-row">
                  <label class="settings-form-label">参数</label>
                  <NInput v-model:value="newMCPArgs" placeholder="空格分隔，如 -y @anthropic/mcp-server-playwright" size="small" style="max-width: 480px" />
                </div>
                <div class="settings-form-row">
                  <label class="settings-form-label">环境变量</label>
                  <NInput v-model:value="newMCPEnv" placeholder='JSON 格式，如 {"API_KEY": "xxx"}' size="small" style="max-width: 480px" />
                </div>
              </template>
              <template v-else>
                <div class="settings-form-row">
                  <label class="settings-form-label">SSE URL</label>
                  <NInput v-model:value="newMCPUrl" placeholder="如：http://localhost:3001/mcp" size="small" style="max-width: 480px" />
                </div>
              </template>
              <div class="settings-form-row">
                <div class="settings-form-toggle">
                  <NSwitch v-model:value="newMCPEnabled" size="small" />
                  <label class="settings-form-label" for="mcp-autostart-switch">添加后立即启动</label>
                </div>
              </div>
              <div class="settings-form-actions">
                <NButton size="small" @click="showAddMCP = false">取消</NButton>
                <NButton type="primary" size="small" @click="onAddMCPServer">添加服务器</NButton>
              </div>
            </div>
          </div>

          <!-- Configured server rows. .settings-card provides
               the bordered card; .server-row is the row
               layout inside. -->
          <div
            v-for="s in mcpServers"
            :key="s.name"
            class="settings-card server-row"
          >
            <div class="server-row-main">
              <div class="server-row-name">{{ s.name }}</div>
              <div class="server-row-meta">
                <NTag :type="mcpStateType(s.state)" size="tiny" :bordered="false">{{ mcpStateLabel(s.state) }}</NTag>
                <span class="server-row-count">{{ s.tool_count }} 个工具</span>
                <span v-if="s.error" class="server-row-error">{{ s.error }}</span>
              </div>
            </div>
            <div class="server-row-actions">
              <NSwitch
                :value="s.state === 'running' || s.state === 'starting'"
                @update:value="(v: boolean) => onToggleMCPServer(s.name, v)"
                size="small"
              />
              <NButton size="tiny" quaternary @click="onRestartMCPServer(s.name)" title="重启" aria-label="重启">
                <RotateCw :size="12" />
              </NButton>
              <NPopconfirm @positive-click="onRemoveMCPServer(s.name)">
                <template #trigger>
                  <NButton size="tiny" quaternary title="删除" aria-label="删除">
                    <X :size="12" />
                  </NButton>
                </template>
                确定删除 "{{ s.name }}"？
              </NPopconfirm>
            </div>
          </div>
        </div>
      </NTabPane>

      <NTabPane name="knowledge" tab="知识库" style="flex: 1; min-height: 0; overflow: auto">
        <div class="providers-split">
          <!-- Left: KB list -->
          <div class="provider-list">
            <div class="provider-list-header">
              <span class="list-title">知识库 ({{ kbBases.length }})</span>
              <NButton size="tiny" type="primary" ghost @click="showAddKB = !showAddKB">
                {{ showAddKB ? '取消' : '+ 新增' }}
              </NButton>
            </div>
            <div v-if="showAddKB" class="add-form">
              <NSpace vertical size="small">
                <NInput v-model:value="newKBName" placeholder="名称" size="tiny" />
                <NInput v-model:value="newKBPath" placeholder="路径" size="tiny" />
                <NButton type="primary" size="tiny" @click="onAddKB">提交</NButton>
              </NSpace>
            </div>
            <div class="provider-items">
              <div
                v-for="b in kbBases"
                :key="b.name"
                class="provider-item"
                :class="{ active: b.name === kbSelectedName }"
                @click="kbSelectedName = b.name"
              >
                <div class="provider-item-head">
                  <NTag v-if="b.enabled" type="success" size="tiny" :bordered="false">启用</NTag>
                  <strong class="provider-item-name">{{ b.name }}</strong>
                  <NPopconfirm @positive-click="onDeleteKB(b.name)" positive-text="删除" negative-text="取消">
                    <template #trigger>
                      <NButton size="tiny" quaternary type="error" @click.stop class="provider-del-btn">
                        <X :size="12" />
                      </NButton>
                    </template>
                    确定删除知识库「{{ b.name }}」？此操作不可撤销。
                  </NPopconfirm>
                </div>
                <div class="provider-item-sub">
                  <span class="muted">{{ b.path }}</span>
                  <span v-if="b.status === 'scanning' || scanningKBs.has(b.name)" style="font-size:10px;color:var(--accent)"> 扫描中</span>
                  <span v-else-if="b.status === 'ok' && b.doc_count" style="font-size:10px;color:var(--text-3)"> · {{ b.doc_count }} sections</span>
                  <span v-else-if="b.status === 'error'" style="font-size:10px;color:var(--warn)"> · 错误</span>
                </div>
              </div>
              <div v-if="kbBases.length === 0" class="muted empty-hint">还没有知识库</div>
            </div>
          </div>

          <!-- Right: detail pane -->
          <div class="provider-detail">
            <div v-if="!kbSelected" class="muted empty-hint">← 选择左侧的知识库</div>
            <template v-else>
              <div class="detail-section kb-detail-scroll">
                <!-- Header -->
                <div class="kb-header">
                  <div class="kb-header-left">
                    <h3 class="section-title">{{ kbSelected.name }}</h3>
                    <NTag :type="kbSelected.enabled ? 'success' : 'default'" size="tiny" :bordered="false">{{ kbSelected.enabled ? '启用' : '禁用' }}</NTag>
                    <span class="muted" style="font-size:11px">{{ kbSelected.doc_count || 0 }} 条索引</span>
                  </div>
                  <NSpace size="small">
                    <NButton size="small" type="primary" @click="onScanKB(kbSelected.name)" :disabled="scanningKBs.has(kbSelected.name) || !kbSelected.scan_model">
                      {{ scanningKBs.has(kbSelected.name) ? '扫描中...' : '扫描' }}
                    </NButton>
                    <NButton v-if="scanningKBs.has(kbSelected.name)" size="small" @click="onCancelScan(kbSelected.name)">取消</NButton>
                    <NPopconfirm @positive-click="onClearKB(kbSelected.name)" positive-text="确定清除" negative-text="取消" placement="left-start">
                      <template #trigger>
                        <NButton size="small" type="error" ghost :disabled="scanningKBs.has(kbSelected.name)">清除</NButton>
                      </template>
                      确定清除知识库「{{ kbSelected.name }}」的所有扫描数据？此操作不可撤销。
                    </NPopconfirm>
                  </NSpace>
                </div>

                <!-- Scan progress -->
                <div v-if="kbScanStatus.has(kbSelected.name)" class="scan-progress">
                  <div class="scan-info">
                    <span>{{ scanLabel(kbSelected.name) }}</span>
                    <span v-if="kbScanStatus.get(kbSelected.name)?.error" style="color:var(--warn);font-size:11px">{{ kbScanStatus.get(kbSelected.name)?.error }}</span>
                  </div>
                  <div v-if="!kbScanStatus.get(kbSelected.name)?.done && !kbScanStatus.get(kbSelected.name)?.error" style="display:flex;align-items:center">
                    <div class="scan-bar">
                      <div class="scan-bar-fill" :style="{ width: kbScanStatus.get(kbSelected.name)!.total > 0 ? ((kbScanStatus.get(kbSelected.name)!.current / kbScanStatus.get(kbSelected.name)!.total) * 100) + '%' : '0%' }"></div>
                    </div>
                    <span class="scan-bar-pct">{{ kbScanStatus.get(kbSelected.name)!.total > 0 ? Math.round((kbScanStatus.get(kbSelected.name)!.current / kbScanStatus.get(kbSelected.name)!.total) * 100) : 0 }}%</span>
                  </div>
                </div>

                <!-- AI Scan Settings -->
                <NCollapse class="kb-collapse-card">
                  <NCollapseItem title="AI 扫描设置" name="scan">
                    <div class="kb-settings-grid">
                      <div class="kb-settings-row">
                        <div class="kb-settings-card">
                          <div class="kb-settings-card-title">解析引擎</div>
                          <div class="kb-config-row">
                            <span class="kb-config-label">模型</span>
                            <NSelect
                              :value="kbSelected.scan_model || ''"
                              :options="kbModelOptions"
                              size="small"
                              placeholder="纯文本解析"
                              style="flex:1"
                              @update:value="(v: string) => onUpdateKBField(kbSelected!.name, 'scan_model', v)"
                            />
                          </div>
                          <div v-if="kbSelected.scan_model" class="kb-config-hint">
                            <span v-if="kbModelSupportsVision(kbSelected.scan_model)" class="kb-hint-accent">视觉模型 — 可处理图片/视频/PDF</span>
                            <span v-else class="kb-hint-muted">纯文本模型 — 仅处理文本</span>
                          </div>
                          <div class="kb-config-row">
                            <span class="kb-config-label">媒体类型</span>
                            <NSelect
                              :value="kbSelected.scan_media_types || []"
                              :options="mediaTypeOptions"
                              size="small"
                              multiple
                              placeholder="选择可 AI 处理的媒体"
                              style="flex:1"
                              @update:value="(v: string[]) => onUpdateKBField(kbSelected!.name, 'scan_media_types', v)"
                            />
                          </div>
                        </div>
                        <div class="kb-settings-card">
                          <div class="kb-settings-card-title">自动化</div>
                          <div class="kb-config-row">
                            <span class="kb-config-label">自动扫描</span>
                            <NSwitch size="small" :value="kbSelected.auto_scan" @update:value="(v: boolean) => onUpdateKBField(kbSelected!.name, 'auto_scan', v)" />
                          </div>
                          <div class="kb-config-hint">启动时自动扫描变更</div>
                          <div class="kb-config-row">
                            <span class="kb-config-label">排除模式</span>
                            <NInput
                              :value="(kbSelected.exclude_patterns || []).join(', ')"
                              size="small"
                              placeholder="*.log, *.tmp"
                              style="flex:1"
                              @update:value="(v: string) => onUpdateKBField(kbSelected!.name, 'exclude_patterns', v.split(',').map(s => s.trim()).filter(Boolean))"
                            />
                          </div>
                          <div class="kb-config-row">
                            <span class="kb-config-label">文件上限</span>
                            <NInputNumber
                              :value="kbSelected.max_file_size ? kbSelected.max_file_size / 1048576 : 5"
                              :min="1" :step="1"
                              size="small"
                              style="width:80px"
                              @update:value="(v: number | null) => onUpdateKBField(kbSelected!.name, 'max_file_size', (v || 5) * 1048576)"
                            />
                            <span class="kb-unit">MB</span>
                          </div>
                        </div>
                      </div>
                      <div class="kb-settings-card">
                        <div class="kb-settings-card-title">存储</div>
                        <div class="kb-config-row">
                          <span class="kb-config-label">路径</span>
                          <span class="kb-config-val path">{{ kbSelected.path }}</span>
                        </div>
                        <div class="kb-config-row">
                          <span class="kb-config-label">状态</span>
                          <NSwitch size="small" :value="kbSelected.enabled" @update:value="(v: boolean) => onToggleKBSwitch(kbSelected!.name, v)" />
                        </div>
                      </div>
                    </div>
                  </NCollapseItem>
                </NCollapse>

                <!-- Tree + Detail split -->
                <div class="kb-tree-panel">
                  <div class="kb-tree-left">
                    <div class="kb-tree-toolbar">
                      <NInput v-model:value="kbNodeFilter" placeholder="筛选节点..." size="tiny" clearable style="flex:1" />
                      <NButton size="tiny" quaternary @click="loadKBNodes(kbSelected!.name)" title="刷新" aria-label="刷新">
                        <RotateCw :size="12" />
                      </NButton>
                    </div>
                    <div class="kb-tree-scroll">
                      <div v-if="kbNodesLoading" class="muted" style="padding:16px;text-align:center">加载中...</div>
                      <NTree
                        v-else-if="kbTreeNodeData.length > 0"
                        :data="kbTreeNodeData"
                        :selected-keys="kbSelectedNodeKeys"
                        :expanded-keys="kbExpandedNodeKeys"
                        :pattern="kbNodeFilter"
                        block-node
                        selectable
                        :render-label="renderTreeLabel"
                        :render-suffix="renderTreeSuffix"
                        @update:selected-keys="onTreeNodeSelect"
                        @update:expanded-keys="onTreeNodeExpand"
                        virtual-scroll
                        style="max-height:100%"
                      />
                      <div v-else class="muted" style="padding:16px;text-align:center;font-size:11px">（暂无索引节点，请先扫描）</div>
                    </div>
                  </div>

                  <div class="kb-tree-right">
                    <div v-if="!kbActiveNode" class="muted" style="padding:24px;text-align:center;font-size:12px">选择左侧节点查看详情</div>
                    <template v-else>
                      <div class="kb-node-detail">
                        <div class="kb-node-detail-header">
                          <div class="kb-node-detail-title">
                            <span class="kb-node-icon">{{ nodeIcon(kbActiveNode.level) }}</span>
                            <span class="kb-node-title-text">{{ kbActiveNode.title }}</span>
                          </div>
                          <NSpace size="small">
                            <NTag v-if="kbActiveNode.kind" size="tiny" :bordered="false">{{ kbActiveNode.kind }}</NTag>
                            <NPopconfirm
                              v-if="kbActiveNode.level > 1"
                              @positive-click="onDeleteNode(kbActiveNode.id)"
                              positive-text="删除"
                              negative-text="取消"
                              placement="left-start"
                            >
                              <template #trigger>
                                <NButton size="tiny" quaternary type="error" title="删除" aria-label="删除">
                                  <Trash2 :size="12" />
                                </NButton>
                              </template>
                              确定删除「{{ kbActiveNode.title }}」{{ kbActiveNode.level === 2 ? '及其所有章节和内容' : '及其内容' }}？
                            </NPopconfirm>
                          </NSpace>
                        </div>
                        <div v-if="kbActiveNode.source" class="kb-node-meta">
                          <span class="kb-node-meta-label">来源</span>
                          <span class="kb-node-meta-val">{{ kbActiveNode.source }}</span>
                        </div>
                        <div v-if="kbActiveNode.keywords" class="kb-node-meta">
                          <span class="kb-node-meta-label">关键词</span>
                          <span class="kb-node-meta-val">{{ kbActiveNode.keywords }}</span>
                        </div>
                        <div v-if="kbActiveNode.level === 1" class="kb-node-stats">
                          <div class="kb-stat-item">
                            <span class="kb-stat-num">{{ l2Nodes.length }}</span>
                            <span class="kb-stat-label">文件</span>
                          </div>
                          <div class="kb-stat-item">
                            <span class="kb-stat-num">{{ kbNodes.filter(n => n.level === 3).length }}</span>
                            <span class="kb-stat-label">章节</span>
                          </div>
                          <div class="kb-stat-item">
                            <span class="kb-stat-num">{{ kbSelected.doc_count || 0 }}</span>
                            <span class="kb-stat-label">索引</span>
                          </div>
                        </div>
                        <div v-if="kbActiveNode.overview" class="kb-node-section">
                          <div class="kb-node-section-title">概览</div>
                          <div class="kb-node-overview">{{ kbActiveNode.overview }}</div>
                        </div>
                        <div v-if="kbActiveNode.level > 1 && kbActiveNodeContent.length > 0" class="kb-node-section">
                          <div class="kb-node-section-title">内容块 ({{ kbActiveNodeContent.length }})</div>
                          <div v-for="c in kbActiveNodeContent" :key="'c-' + c.id" class="kb-node-content-block">
                            <pre class="kb-tree-pre">{{ c.content }}</pre>
                          </div>
                        </div>
                        <div v-if="kbActiveNode.level === 2 && kbActiveChildren.length > 0" class="kb-node-section">
                          <div class="kb-node-section-title">章节 ({{ kbActiveChildren.length }})</div>
                          <div
                            v-for="ch in kbActiveChildren"
                            :key="ch.id"
                            class="kb-node-child-row"
                            :class="{ active: kbActiveChildId === ch.id }"
                            @click="selectChildNode(ch)"
                          >
                            <span class="kb-node-child-icon">§</span>
                            <span class="kb-node-child-title">{{ ch.title || '(无标题)' }}</span>
                            <span v-if="ch.content_count > 0" class="kb-node-child-meta">{{ ch.content_count }} 块</span>
                            <NPopconfirm
                              @positive-click="onDeleteNode(ch.id)"
                              positive-text="删除"
                              negative-text="取消"
                              placement="left-start"
                              @click.stop
                            >
                              <template #trigger>
                                <NButton size="tiny" quaternary type="error" @click.stop title="删除" aria-label="删除">
                                  <Trash2 :size="12" />
                                </NButton>
                              </template>
                              确定删除「{{ ch.title }}」及其内容？
                            </NPopconfirm>
                          </div>
                        </div>
                      </div>
                    </template>
                  </div>
                </div>
              </div>
            </template>
          </div>
        </div>
      </NTabPane>

      <NTabPane name="websearch" tab="网络搜索" style="flex: 1; min-height: 0; overflow: auto">
        <WebSearchSettings />
      </NTabPane>

      <NTabPane name="browser" tab="浏览器" style="flex: 1; min-height: 0; overflow: auto">
        <div class="settings-section">
          <div class="settings-section-header">
            <h3 class="settings-section-title">浏览器控制</h3>
          </div>
          <p class="settings-section-description">
            通过浏览器扩展让 LLM 直接控制网页：导航、点击、输入、截图、提取内容。
            关闭后所有 browser_* 工具不可用。
          </p>
          <div class="settings-form-row">
            <div class="settings-form-toggle">
              <NSwitch :value="browserEnabled" @update:value="onToggleBrowser" size="small" />
              <label class="settings-form-label">启用浏览器控制</label>
            </div>
            <span class="settings-form-hint">需安装 Chrome 扩展才能实际使用</span>
          </div>
        </div>

        <div class="settings-section">
          <div class="settings-section-header">
            <h3 class="settings-section-title">已连接浏览器 ({{ browserList.length }})</h3>
          </div>
          <div v-if="!browserList.length" class="settings-empty">
            暂无连接的浏览器，请先安装扩展
          </div>
          <div
            v-for="b in browserList"
            :key="b.id"
            class="settings-card server-row"
          >
            <div class="server-row-main">
              <div class="server-row-name">{{ b.name || b.id }}</div>
              <div class="server-row-meta">
                <NTag type="success" size="tiny" :bordered="false">已连接</NTag>
                <span class="server-row-count">{{ b.connected_at }}</span>
              </div>
            </div>
          </div>
        </div>

        <div class="settings-section">
          <div class="settings-section-header">
            <h3 class="settings-section-title">安装扩展</h3>
            <div class="settings-form-actions">
              <NButton size="small" type="primary" ghost tag="a" href="/api/v1/browser/extension" download>
                下载扩展包
              </NButton>
            </div>
          </div>
          <div class="settings-section-description" style="margin-bottom: 0;">
            <ol style="padding-left: 18px; line-height: 1.7; margin: 0;">
              <li>
                <a href="/api/v1/browser/extension" download class="browser-ext-download-link">
                  下载浏览器扩展 zip
                </a>
                并解压到本地任意目录
              </li>
              <li>Chrome 打开 <code>chrome://extensions</code></li>
              <li>右上角开启「开发者模式」</li>
              <li>点击「加载已解包扩展」，选择解压目录</li>
              <li>
                在扩展弹窗「服务器」输入框粘贴以下地址：
                <div class="browser-server-url-box">
                  <code>{{ browserHTTPURL || 'http://127.0.0.1:8960' }}</code>
                  <NButton
                    size="tiny" quaternary
                    @click="copyBrowserURL"
                    :disabled="!browserHTTPURL"
                    title="复制"
                  >
                    复制
                  </NButton>
                </div>
              </li>
            </ol>
          </div>
        </div>
      </NTabPane>
    </NTabs>
  </AppSettingsLayout>

  <!-- Inner confirmation modals — teleported to body by
       NModal, so they sit on top of the settings dialog
       regardless of where they're declared in the
       template. Sibling of AppSettingsLayout (multi-root
       template) — they're conceptually owned by the
       settings dialog but rendered on top of everything. -->
  <NModal v-model:show="showConfirmPermDelete" preset="card" title="确认永久删除" style="width: 360px">
    <div class="confirm-body">
      <p>确定要永久删除此会话吗？此操作不可撤销。</p>
      <div class="confirm-actions">
        <NButton size="small" @click="showConfirmPermDelete = false">取消</NButton>
        <NButton size="small" type="error" @click="confirmPermDelete">永久删除</NButton>
      </div>
    </div>
  </NModal>

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
</template>

<style scoped>
/* =================================================================
 * Settings design system (PR #9)
 * =================================================================
 * Replaces the ad-hoc inline styles that accumulated in earlier
 * versions with a small set of semantic classes. The goal is
 * consistent visual rhythm across all tabs:
 *
 *   - Typography scale:  14 (section title) / 12.5 (label) /
 *                         11.5 (hint) / 13 (body) px
 *   - Spacing scale:      4 / 6 / 8 / 12 / 16 / 24 / 28 px
 *   - Color usage:        text-primary for body, text-secondary
 *                         for labels, text-tertiary for hints;
 *                         never hardcode the colors.
 *   - Form pattern:       label above input (vertical, NOT
 *                         side-by-side), with a hint text
 *                         optional below.
 *   - Action pattern:     buttons right-aligned in their own
 *                         row, separated from the form.
 * ================================================================= */

/* --- Tab content wrapper ------------------------------------------
 * Every tab's content goes inside .settings-tab-content. The
 * 28px horizontal / 24px vertical padding is the consistent
 * inner gutter across all tabs — was previously inconsistent
 * (some tabs had 0, others had 16/20/24 inline). */
.settings-tab-content {
  padding: 24px 28px;
  max-width: 920px;
}

/* --- Section ------------------------------------------------------
 * A logical group inside a tab (e.g. "基本信息", "模型").
 * Sections stack vertically with 24px between them and a
 * subtle bottom border on the header. */
.settings-section {
  margin-bottom: 24px;
  /* 20px outer padding so the form fields don't touch the
   * edge of the parent card (provider-detail etc.) — this
   * is the margin the form used to be missing, which made
   * labels and inputs feel glued to the left/right
   * borders. */
  padding: 4px 0;
}
.settings-section:last-child {
  margin-bottom: 0;
}
.settings-section-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
  padding-bottom: 12px;
  margin-bottom: 14px;
  border-bottom: 1px solid var(--border-subtle);
}
.settings-section-title {
  font-size: 14px;
  font-weight: 600;
  color: var(--text-primary);
  margin: 0;
  letter-spacing: -0.005em;
  line-height: 1.3;
}
.settings-section-description {
  font-size: 12.5px;
  color: var(--text-tertiary);
  line-height: 1.55;
  margin: -4px 0 14px;
}

/* --- Form ---------------------------------------------------------
 * A form is a vertical stack of form rows. 18px gap gives each
 * field clear visual separation — too tight and the form reads
 * as one block, too loose and the eye loses the grouping.
 * 18px is the sweet spot for Chinese labels at 13px. */
.settings-form {
  display: flex;
  flex-direction: column;
  gap: 18px;
}
.settings-form-row {
  display: flex;
  flex-direction: column;
  /* 8px between the label and its input — enough to make
   * the grouping obvious without floating the label
   * awkwardly far from the control it describes. */
  gap: 8px;
}
/* Horizontal variant: label and control on the same row. Use
 * for short controls like switches where stacking would waste
 * vertical space. The label sits next to the control rather
 * than above it. */
.settings-form-row--inline {
  flex-direction: row;
  align-items: center;
  gap: 10px;
  flex-wrap: wrap;
}
/* In inline rows the label is a sibling of the control (not a
 * stacked caption), so we drop the heavy-weight treatment that
 * the column-form label needs. */
.settings-form-row--inline .settings-form-label {
  font-weight: 500;
  margin: 0;
  cursor: pointer;
  user-select: none;
}
.settings-form-label {
  font-size: 13px;
  font-weight: 500;
  color: var(--text-primary);
  line-height: 1.3;
  display: block;
}
.settings-form-hint {
  font-size: 11.5px;
  color: var(--text-tertiary);
  line-height: 1.5;
  /* A small top margin so the hint feels like a continuation of
   * the field above it rather than a new paragraph — connects
   * the help text to its input visually. */
  margin-top: 1px;
}
.settings-form-hint--error {
  color: var(--error-500);
}
/* Wrapper for switch-style controls in a stacked row. The switch
 * sits on its own line, label is right next to it, and the hint
 * spans the row below — matches the visual rhythm of a stacked
 * field while keeping the switch's tap target inline. */
.settings-form-toggle {
  display: inline-flex;
  align-items: center;
  gap: 8px;
}

/* --- Action row ---------------------------------------------------
 * Buttons in their own row, right-aligned. Use when a form
 * needs a save/reset/submit pair at the bottom. The 8px gap
 * between buttons matches the sidebar / input bar. */
.settings-form-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 4px;
  justify-content: flex-end;
}
.settings-form-actions--left {
  justify-content: flex-start;
}
.settings-form-actions--between {
  justify-content: space-between;
}

/* --- Card ---------------------------------------------------------
 * A bordered card that groups related items (a list of
 * providers, a list of skills, a list of MCP servers). The
 * 1px border + 8px radius + 12px padding matches the visual
 * weight of a normal surface but draws the eye to its
 * contents as a unit. */
.settings-card {
  background: var(--surface-1);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-md);
  padding: 12px 14px;
}
.settings-card + .settings-card {
  margin-top: 8px;
}
.settings-card-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  margin-bottom: 8px;
  gap: 8px;
}
.settings-card-title {
  font-size: 13px;
  font-weight: 500;
  color: var(--text-primary);
  display: inline-flex;
  align-items: center;
  gap: 8px;
}
.settings-card-meta {
  font-size: 11.5px;
  color: var(--text-tertiary);
}

/* --- Muted text helper --------------------------------------------
 * Use sparingly. The new design prefers .settings-form-hint
 * for form-related help text. .muted is the older catch-all
 * kept for one-off grey text. */
.muted { color: var(--text-tertiary); font-size: 12px; }

/* --- Empty state helper ------------------------------------------
 * Centered grey text for "no items yet" messages. */
.settings-empty {
  padding: 24px 16px;
  text-align: center;
  color: var(--text-tertiary);
  font-size: 12.5px;
  background: var(--surface-1);
  border: 1px dashed var(--border-subtle);
  border-radius: var(--radius-md);
}

/* --- Hide the NTabs top bar (unchanged from PR #8) --------------
 * The new AppSettingsLayout provides a left vertical nav that
 * drives the active tab. The NTabPanes themselves are still
 * rendered (and animated) so transitions stay smooth; we
 * just don't want the top tab strip to take up vertical
 * space. */
.settings-no-bar :deep(.n-tabs-nav) {
  display: none !important;
}
.settings-no-bar :deep(.n-tabs-pane-wrapper) {
  flex: 1;
  min-height: 0;
}
/* PR #9: every tab pane gets the same outer padding so the
 * left/right gutters are consistent across tabs. Tabs that
 * need extra spacing (e.g. providers, with a 2-column
 * inner split) can override with their own wrapper — the
 * 24px/28px gutter is a baseline, not a hard rule. */
.settings-no-bar :deep(.n-tab-pane) {
  padding: 0 !important;
}
.settings-no-bar :deep(.n-tab-pane) > * {
  /* Direct children of the pane get the standard gutter
   * unless they opt out (with .full-bleed or similar). */
  padding: 24px 28px;
  max-width: 100%;
}

/* --- MCP server row (used inside the MCP tab's .settings-card
 * list). Each card holds a left-aligned name + meta column
 * and a right-aligned actions cluster. The actions cluster
 * uses a 6px gap to match the rest of the settings rhythm. */
.server-row {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 12px;
}
.server-row-main {
  display: flex;
  flex-direction: column;
  gap: 4px;
  min-width: 0;
  flex: 1;
}
.server-row-name {
  font-size: 13.5px;
  font-weight: 500;
  color: var(--text-primary);
  letter-spacing: -0.003em;
}
.server-row-meta {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 11.5px;
  color: var(--text-tertiary);
  flex-wrap: wrap;
}
.server-row-count {
  font-variant-numeric: tabular-nums;
}
.server-row-error {
  color: var(--error-500);
  font-size: 11.5px;
  /* Truncate long error messages so a 2-line error doesn't
   * push the actions cluster down. */
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 280px;
}
.server-row-actions {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-shrink: 0;
}

/* --- Legacy aliases (kept for backward compat during migration)
 * Most existing code uses .section-title / .form-row /
 * .form-label. These map to the new system so the migration
 * can be done tab by tab. */
.section-title {
  margin: 0;
  font-size: 14px;
  font-weight: 600;
  color: var(--text-primary);
  letter-spacing: -0.005em;
}
.form-row {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.form-label {
  font-size: 12.5px;
  font-weight: 500;
  color: var(--text-secondary);
}
.form-hint {
  font-size: 11.5px;
  color: var(--text-tertiary);
}

/* Providers tab — left/right split */
.providers-split {
  display: grid;
  grid-template-columns: 240px 1fr;
  gap: 12px;
  /* No fixed height. The grid sizes to its content; if
   * content overflows, the parent .n-tab-pane (which has
   * overflow: auto via AppSettingsLayout's content area)
   * provides the scroll. The 60vh / 480px min was
   * creating a visible empty band at the bottom of the
   * settings dialog when the actual content (form +
   * model list) was shorter than 60vh. */
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
  overflow-y: auto;
  /* Outer padding so form fields don't sit on the border.
   * The basic info / model sections use .settings-section
   * which adds its own internal padding, so this is the
   * main visual buffer between the card edge and any
   * direct children. 16px is the standard settings-card
   * padding (matches .settings-card in line ~2615). */
  padding: 16px 18px;
  gap: 4px;
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
  gap: 12px;
  padding: 10px 12px;
  border-radius: var(--radius-md);
  background: var(--surface-1);
  border: 1px solid var(--border-subtle);
  font-size: 13px;
  transition: border-color var(--dur-fast) var(--ease-out);
}
.model-card:hover {
  border-color: var(--border-default);
}
.model-card.is-default {
  border-color: var(--success-500);
  background: var(--success-50);
}
.model-card-top {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 140px;
  flex-shrink: 0;
}
.model-card-name {
  font-family: var(--font-mono);
  font-size: 12.5px;
  font-weight: 500;
  color: var(--text-primary);
}
.model-card-meta {
  display: flex;
  align-items: center;
  gap: 12px;
  flex: 1;
  min-width: 0;
  color: var(--text-tertiary);
  font-size: 11.5px;
  font-variant-numeric: tabular-nums;
}
.model-meta-item {
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.model-card-actions {
  display: flex;
  gap: 4px;
  flex-shrink: 0;
}
.model-id {
  background: var(--surface-2);
  padding: 1px 6px;
  border-radius: 3px;
  font-family: var(--font-mono);
  font-size: 11px;
  color: var(--text-secondary);
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
.skill-repos-bar {
  display: flex; align-items: center; gap: 6px;
  padding: 6px 0;
  border-bottom: 1px solid var(--border-2);
  flex-shrink: 0;
}
.repo-chips { display: flex; gap: 4px; flex-wrap: wrap; }
.skill-columns {
  flex: 1; min-height: 0;
  display: flex; gap: 0;
  margin-top: 8px;
}
.skill-col {
  flex: 1; min-width: 0;
  display: flex; flex-direction: column;
}
.skill-col-left { border-right: 1px solid var(--border-2); padding-right: 12px; }
.skill-col-right { padding-left: 12px; }
.skill-col-header {
  display: flex; justify-content: space-between; align-items: center;
  margin-bottom: 6px; gap: 8px; flex-shrink: 0;
}
.skill-col-body {
  flex: 1 1 auto; min-height: 0;
  max-height: calc(80vh - 220px);
  overflow-y: auto;
}
.skill-section-title { font-size: 12px; color: var(--text-3); white-space: nowrap; }
.skill-hint { font-size: 12px; color: var(--text-3); }
.skill-result-row {
  display: flex; align-items: center; gap: 8px;
  padding: 5px 8px; border-radius: 4px;
}
.skill-result-row:hover { background: var(--bg-3); }
.skill-result-info { flex: 1; min-width: 0; display: flex; flex-direction: column; gap: 1px; }
.skill-result-name { font-size: 12.5px; font-weight: 500; }
.skill-result-desc {
  font-size: 11px; color: var(--text-4);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.skill-search { display: flex; gap: 8px; margin-bottom: 12px; }
.skill-search-results { margin-bottom: 12px; }
.skill-divider { border-top: 1px solid var(--border-2); margin: 12px 0; }
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
/* ---- Knowledge Base ---- */
.kb-detail-scroll { flex: 1; overflow-y: auto; }
.scan-progress { padding: 8px 0; }
.scan-info { display: flex; gap: 12px; align-items: center; font-size: 13px; }
.scan-bar { height: 4px; border-radius: 2px; background: var(--bg-3); overflow: hidden; margin-top: 4px; flex: 1; }
.scan-bar-fill { height: 100%; background: var(--accent); transition: width 0.3s ease; }
.scan-bar-pct { font-size: 10px; color: var(--text-4); margin-left: 6px; white-space: nowrap; }
.scan-meta { font-size: 11px; color: var(--text-3); margin-top: 4px; }

/* ---- Knowledge Base Cards ---- */
.kb-header {
  display: flex; align-items: center; justify-content: space-between;
  margin-bottom: 10px;
}
.kb-header-left { display: flex; align-items: center; gap: 8px; }

.kb-collapse-card {
  border: 1px solid var(--border-2);
  border-radius: 6px;
  margin-bottom: 8px;
}
.kb-config-row {
  display: flex; align-items: center; gap: 10px; padding: 3px 0;
}
.kb-config-label {
  font-size: 12px; color: var(--text-3); width: 56px; flex-shrink: 0;
  line-height: 1.4;
}
.kb-config-val { font-size: 12px; color: var(--text-2); }
.kb-config-val.path { word-break: break-all; font-family: ui-monospace, monospace; font-size: 11.5px; }
.kb-config-hint { font-size: 11px; color: var(--text-4); padding: 1px 0 3px 66px; line-height: 1.4; }
.kb-hint-accent { color: var(--accent); }
.kb-hint-muted { color: var(--text-4); }
.kb-unit { font-size: 11px; color: var(--text-4); margin-left: 4px; }

/* AI scan settings layout */
.kb-settings-grid { margin-top: 2px; }
.kb-settings-row {
  display: grid; grid-template-columns: 1fr 1fr; gap: 10px;
  margin-bottom: 10px;
}
.kb-settings-card {
  border: 1px solid var(--border-2);
  border-radius: 6px;
  padding: 10px 12px;
  background: var(--bg-2);
}
.kb-settings-card-title {
  font-size: 12px; font-weight: 600; color: var(--text-1);
  margin-bottom: 6px;
}

/* Tree panel split layout */
.kb-tree-panel {
  display: flex; gap: 0; min-height: 300px; max-height: 400px;
  border: 1px solid var(--border-2); border-radius: 6px;
  overflow: hidden;
}
.kb-tree-left {
  width: 260px; flex-shrink: 0; display: flex; flex-direction: column;
  border-right: 1px solid var(--border-2); background: var(--bg-2);
}
.kb-tree-right {
  flex: 1; min-width: 0; overflow-y: auto;
  background: var(--bg-1);
}
.kb-tree-toolbar {
  display: flex; align-items: center; gap: 4px;
  padding: 6px 8px; border-bottom: 1px solid var(--border-2);
}
.kb-tree-scroll { flex: 1; overflow-y: auto; padding: 4px 0; }

/* Tree node label styles */
.kb-tree-label {
  display: inline-flex; align-items: center; gap: 4px;
  font-size: 12px; min-width: 0;
}
.kb-tree-label.l1 { font-weight: 600; color: var(--text-primary); }
.kb-tree-label.l2 { color: var(--text-secondary); }
.kb-tree-label.l3 { font-size: 11.5px; color: var(--text-secondary); }
.kb-tree-icon { flex-shrink: 0; color: var(--text-tertiary); }
.kb-tree-label.l1 .kb-tree-icon { color: var(--brand-500); }
.kb-tree-label-text {
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.kb-tree-label-tag {
  font-size: 9px; color: var(--text-4); background: var(--bg-3);
  padding: 0 4px; border-radius: 3px; flex-shrink: 0;
}
.kb-tree-label-cnt {
  font-size: 10px; color: var(--text-4); flex-shrink: 0;
}

/* Node detail panel */
.kb-node-detail { padding: 12px; }
.kb-node-detail-header {
  display: flex; align-items: flex-start; justify-content: space-between;
  gap: 8px; margin-bottom: 8px;
}
.kb-node-detail-title {
  display: flex; align-items: center; gap: 6px; flex: 1; min-width: 0;
}
.kb-node-icon { flex-shrink: 0; font-size: 16px; }
.kb-node-title-text {
  font-size: 13px; font-weight: 600; line-height: 1.3;
}
.kb-node-meta {
  display: flex; gap: 8px; font-size: 11px; padding: 2px 0;
}
.kb-node-meta-label { color: var(--text-4); flex-shrink: 0; }
.kb-node-meta-val {
  color: var(--text-2); word-break: break-all;
  font-family: ui-monospace, monospace; font-size: 10.5px;
}

/* Statistics cards */
.kb-node-stats {
  display: flex; gap: 12px; padding: 10px 0; margin: 8px 0;
  border-top: 1px solid var(--border-2);
  border-bottom: 1px solid var(--border-2);
}
.kb-stat-item { text-align: center; min-width: 50px; }
.kb-stat-num { display: block; font-size: 18px; font-weight: 700; color: var(--accent); }
.kb-stat-label { font-size: 10px; color: var(--text-4); }

/* Sections in detail */
.kb-node-section { margin-top: 10px; }
.kb-node-section-title {
  font-size: 11px; font-weight: 600; color: var(--text-3);
  margin-bottom: 6px;
}
.kb-node-overview {
  font-size: 12px; color: var(--text-2); white-space: pre-wrap;
  line-height: 1.5; background: var(--bg-2); padding: 8px 10px;
  border-radius: 4px;
}
.kb-node-content-block { margin-bottom: 6px; }

/* Child rows in L2 detail */
.kb-node-child-row {
  display: flex; align-items: center; gap: 6px;
  padding: 5px 8px; border-radius: 4px; cursor: pointer;
  transition: background 0.15s;
}
.kb-node-child-row:hover { background: var(--bg-3); }
.kb-node-child-row.active { background: var(--bg-3); }
.kb-node-child-icon { font-size: 11px; color: var(--text-4); flex-shrink: 0; }
.kb-node-child-title {
  flex: 1; font-size: 12px; overflow: hidden; text-overflow: ellipsis;
  white-space: nowrap; min-width: 0;
}
.kb-node-child-meta { font-size: 10px; color: var(--text-4); flex-shrink: 0; }

/* Content pre blocks */
.kb-tree-pre {
  margin: 0; padding: 6px 8px; background: var(--bg-3); border-radius: 4px;
  font-size: 10.5px; line-height: 1.4; white-space: pre-wrap; word-break: break-word;
  max-height: 120px; overflow-y: auto; font-family: ui-monospace, monospace;
}

/* System config tab.
 *
 * PR #9 follow-up: refactored for the unified form rhythm.
 * The previous version forced a `height: 58vh` on the shell
 * to give the NCollapse groups room to breathe, but that
 * caused a visible empty band at the bottom of the settings
 * dialog when the content (3 NCollapse groups) was shorter
 * than 58vh. The fix: drop the fixed height, let the
 * NTabPane size naturally, and put the vertical breathing
 * room into the form rows + gap between NCollapse items
 * instead. Now the dialog fills the available space only
 * to the extent the content needs, and the scrollbar
 * appears only when content actually overflows.
 */
.system-config-shell {
  /* No fixed height — let the NTabPane's flex sizing
   * determine the height. The NTabPane already has
   * `flex: 1; min-height: 0` so this fills the available
   * layout dialog height. */
  display: flex;
  flex-direction: column;
}
.system-config-scroll {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
}
.system-config-body {
  padding: 4px 0;
}

/* Outer wrapper around the 3 NCollapse groups. Gap gives
 * 16px between groups (matching the standard form-row
 * gap) so they feel like distinct sections rather than
 * one glued-together block. */
.sys-collapse {
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-md);
  background: var(--surface-1);
  /* NCollapse itself has its own internal margin; we
   * tighten it to 0 so our .sys-collapse + {margin-top}
   * below is the only vertical rhythm. */
  --n-collapse-item-margin: 0;
  /* 16px between groups. */
  margin-bottom: 16px;
}
.sys-collapse:last-child {
  margin-bottom: 0;
}

/* NCollapse title row. The default has minimal padding
 * (12px 16px) which makes the title look cramped against
 * the border. Bump to 14px/18px to match our spacing
 * scale. Also tighten the title font to 13px medium
 * (instead of the default 14px) so it matches the
 * .settings-section-title rhythm. */
.sys-collapse :deep(.n-collapse-item__header-main) {
  font-size: 13.5px;
  font-weight: 600;
  color: var(--text-primary);
  letter-spacing: -0.005em;
}
.sys-collapse :deep(.n-collapse-item) {
  /* Header padding: more horizontal, more vertical. */
}
.sys-collapse :deep(.n-collapse-item__header) {
  padding: 12px 18px;
  border-color: var(--border-subtle);
}
.sys-collapse :deep(.n-collapse-item .n-collapse-item__content-inner) {
  /* Inner content area: 18px horizontal to align with the
   * header, 18px vertical for breathing room. The default
   * NCollapse is 16px which feels tight next to our 14px
   * form-label font. */
  padding: 16px 18px 18px;
}

/* Form grid + row inside the NCollapse items. PR #9
 * follow-up: 10px vertical padding per row, 16px row gap,
 * upgraded typography. Previously rows were crammed with
 * only 3px padding + 2px row gap, which made the
 * configuration dense and hard to scan. */
.sys-form-grid {
  display: flex;
  flex-direction: column;
  gap: 4px;  /* extra padding via .sys-form-row vertical handles the rest */
}
.sys-form-row {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 8px 0;
  /* Subtle divider between rows. The first row in each
   * group drops the top border via :first-child. */
  border-top: 1px solid var(--border-subtle);
}
.sys-form-row:first-child {
  border-top: none;
  padding-top: 4px;
}
.sys-form-row:last-child {
  padding-bottom: 4px;
}
.sys-label {
  /* Widened from 130px to 150px so longer Chinese labels
   * ("自动压缩缓冲区", "工具结果截断轮次") don't wrap. */
  font-size: 12.5px;
  color: var(--text-secondary);
  font-weight: 500;
  width: 160px;
  flex-shrink: 0;
  line-height: 1.4;
  letter-spacing: -0.003em;
}
.sys-hint {
  font-size: 11.5px;
  color: var(--text-tertiary);
  flex: 1;
  line-height: 1.5;
  /* Keep the hint on the same line as the input for
   * compact rows, but allow wrap if the message is
   * long (e.g. "bytes，默认 6000（含子代理/task 结果）") */
  white-space: normal;
  min-width: 0;
}

.sys-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  padding: 16px 0 8px;
  /* Subtle top border to separate from the content. */
  border-top: 1px solid var(--border-subtle);
  margin-top: 8px;
}

/* NCollapse arrow icon color — default is a hard
 * `--text-3`; we soften it to `--text-tertiary` for
 * visual consistency. */
.sys-collapse :deep(.n-collapse-item__header .n-base-icon) {
  color: var(--text-tertiary);
}

.browser-ext-download-link {
  color: var(--accent);
  text-decoration: underline;
}
.browser-ext-download-link:hover {
  opacity: 0.8;
}
.browser-server-url-box {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  margin-top: 4px;
  padding: 4px 8px;
  background: var(--surface-2, #1e1e1e);
  border: 1px solid var(--border, #333);
  border-radius: 4px;
  font-size: 12px;
}
.browser-server-url-box code {
  color: var(--accent);
  user-select: all;
}
</style>
