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
  NTag, NTabs, NTabPane, NDataTable, NPopconfirm, NTooltip, NIcon, useMessage,
} from 'naive-ui'
import * as api from '../api/client'
import { loadProviders, loadSessions } from '../stores/chat'
import type { Session } from '../api/client'

const message = useMessage()
const tab = ref<'providers' | 'styles' | 'archive'>('providers')

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

// --- Style state ---
const styles = ref<api.StyleInfo[]>([])
const showAddStyle = ref(false)
const editingStyle = ref<api.StyleDetail | null>(null)
const newStyleId = ref('')
const newStyleLabel = ref('')
const newStyleIdentity = ref('')
const newStyleSoul = ref('')
const isEdit = ref(false)

onMounted(async () => {
  await refresh()
})

watch(tab, (v) => {
  if (v === 'archive' && !archivedSessions.value.length) {
    loadArchived()
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
  if (!window.confirm(`确定删除 provider "${name}"? 该 provider 下的所有模型配置也会被删除。`)) return
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
  if (!window.confirm(`确定删除模型 "${model}"?`)) return
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

// --- Style handlers ---

function resetNewStyle() {
  newStyleId.value = ''
  newStyleLabel.value = ''
  newStyleIdentity.value = ''
  newStyleSoul.value = ''
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
      identity: newStyleIdentity.value,
      soul: newStyleSoul.value,
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
    newStyleLabel.value = s.label
    newStyleIdentity.value = s.identity
    newStyleSoul.value = s.soul
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
      identity: newStyleIdentity.value,
      soul: newStyleSoul.value,
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
  if (!window.confirm(`确定删除风格 "${id}"? 删除后相关会话将回退到默认风格。`)) return
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
                    <NPopconfirm
                      v-if="!selected.is_default"
                      @positive-click="onDeleteProvider(selected.name)"
                      positive-text="删除"
                      negative-text="取消"
                    >
                      <template #trigger>
                        <NButton size="small" type="error" ghost>删除 provider</NButton>
                      </template>
                      确定删除 provider "{{ selected.name }}" 及其下所有模型？
                    </NPopconfirm>
                    <NTag v-else size="small" :bordered="false" type="default">默认 provider 不可删除</NTag>
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
                  <NButton size="small" type="primary" ghost @click="onShowAddModel">
                    {{ showAddModel ? '取消' : '+ 添加模型' }}
                  </NButton>
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
                <table v-if="selected.models && selected.models.length" class="model-table">
                  <thead>
                    <tr>
                      <th>模型</th>
                      <th>显示名</th>
                      <th>上下文</th>
                      <th>输出上限</th>
                      <th>能力</th>
                      <th>操作</th>
                    </tr>
                  </thead>
                  <tbody>
                    <tr v-for="m in selected.models" :key="m.name" :class="{ 'is-default': m.default }">
                      <td>
                        <code class="model-id">{{ m.name }}</code>
                        <NTag v-if="m.default" type="success" size="tiny" :bordered="false" style="margin-left: 6px">默认</NTag>
                      </td>
                      <td>{{ m.display_name || '—' }}</td>
                      <td><span class="muted">{{ fmtContext(m.max_tokens_context) }}</span></td>
                      <td><span class="muted">{{ m.max_tokens_output || '—' }}</span></td>
                      <td>
                        <NTag v-if="m.capabilities?.supports_vision" type="info" size="tiny" :bordered="false">👁 视觉</NTag>
                        <span v-else class="muted">—</span>
                      </td>
                      <td>
                        <NSpace size="small">
                          <NButton size="tiny" @click="onEditModel(m)">编辑</NButton>
                          <NButton v-if="!m.default" size="tiny" @click="onSetDefaultModel(m.name)">设为默认</NButton>
                          <NButton size="tiny" type="error" ghost @click="onDeleteModel(m.name)">删除</NButton>
                        </NSpace>
                      </td>
                    </tr>
                  </tbody>
                </table>
                <div v-else class="muted empty-hint">还没有模型。点击「+ 添加模型」配置。</div>
              </div>
            </template>
          </div>
        </div>
      </NTabPane>

      <NTabPane name="styles" tab="风格配置">
        <div class="styles-tab-body">
        <NSpace vertical size="large">
          <div>
            <h3 class="section-title">已配置的风格</h3>
            <div v-for="s in styles" :key="s.id" class="style-row">
              <div class="style-meta">
                <div>
                  <NTag size="small" :type="isBuiltIn(s.id) ? 'success' : 'info'" style="margin-right: 6px">
                    {{ isBuiltIn(s.id) ? '内置' : '自定义' }}
                  </NTag>
                  <strong>{{ s.label }}</strong>
                  <span class="muted">(<code>{{ s.id }}</code>)</span>
                </div>
                <div class="muted style-desc">{{ s.desc || '（无描述）' }}</div>
              </div>
              <NSpace size="small">
                <NButton size="small" @click="onEditStyle(s.id)">查看/编辑</NButton>
                <NPopconfirm
                  v-if="!isBuiltIn(s.id)"
                  @positive-click="onDeleteStyle(s.id)"
                  positive-text="删除"
                  negative-text="取消"
                >
                  <template #trigger>
                    <NButton size="small" type="error" ghost>删除</NButton>
                  </template>
                  确定删除风格 "{{ s.id }}" ? 该会话将回退到默认风格。
                </NPopconfirm>
                <NTag v-else size="small" :bordered="false" type="default">只读</NTag>
              </NSpace>
            </div>
            <NSpace style="margin-top: 8px">
              <NButton size="small" @click="() => { showAddStyle = !showAddStyle; if (showAddStyle) resetNewStyle() }" type="primary" ghost>
                {{ showAddStyle ? '取消' : '+ 新增风格' }}
              </NButton>
            </NSpace>

            <div v-if="showAddStyle" class="add-form">
              <NSpace vertical size="small">
                <NInput v-model:value="newStyleId" placeholder="id (英文/数字/下划线, 例: warm)" size="small" :disabled="isEdit" />
                <NInput v-model:value="newStyleLabel" placeholder="显示名 (例: 温暖)" size="small" />
                <NInput
                  v-model:value="newStyleIdentity"
                  placeholder="Identity (系统提示中的「你是谁」部分, 支持 markdown)"
                  type="textarea"
                  :rows="4"
                  size="small"
                />
                <NInput
                  v-model:value="newStyleSoul"
                  placeholder="Soul (性格、说话风格)"
                  type="textarea"
                  :rows="2"
                  size="small"
                />
                <NSpace>
                  <NButton type="primary" size="small" @click="isEdit ? onUpdateStyle() : onCreateStyle()">
                    {{ isEdit ? '保存修改' : '创建风格' }}
                  </NButton>
                  <NButton size="small" @click="closeStyleEditor">取消</NButton>
                </NSpace>
              </NSpace>
            </div>
          </div>
        </NSpace>
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

/* Model table */
.model-table {
  width: 100%;
  border-collapse: collapse;
  font-size: 12px;
}
.model-table th {
  text-align: left;
  padding: 6px 8px;
  font-weight: 600;
  color: var(--text-2);
  border-bottom: 1px solid var(--border-2);
  background: var(--bg-2);
}
.model-table td {
  padding: 8px;
  border-bottom: 1px solid var(--border-2);
  vertical-align: middle;
}
.model-table tr.is-default {
  background: var(--success-soft);
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
.style-row {
  display: flex; justify-content: space-between; align-items: center;
  padding: 8px 10px;
  background: var(--bg-3);
  border-radius: 6px;
  margin-bottom: 6px;
  gap: 12px;
}
.style-meta { display: flex; flex-direction: column; gap: 2px; min-width: 0; flex: 1; }
.style-desc {
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
  max-width: 480px;
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
</style>
