<script setup lang="ts">
// App-level (software) settings. Split into two tabs by user request:
//
//   1. LLM 提供商 — provider / model / API key CRUD
//   2. 风格配置   — built-in + user-added style CRUD
//
// The previous incarnation conflated the two; tabbing them out gives
// each section enough room to be useful on its own and matches the
// way the user thinks about the data (one is per-session config, the
// other is global).

import { onMounted, ref } from 'vue'
import {
  NModal, NCard, NSelect, NButton, NSpace, NInput, NInputNumber, NSwitch,
  NTag, NTabs, NTabPane, NDataTable, NPopconfirm, useMessage,
} from 'naive-ui'
import * as api from '../api/client'

const message = useMessage()
const tab = ref<'providers' | 'styles'>('providers')

// --- Provider state (unchanged behaviour, just moved into its own tab) ---
const providers = ref<any[]>([])
const showAddProvider = ref(false)
const newName = ref('')
const newProtocol = ref<'openai' | 'anthropic'>('openai')
const newBaseURL = ref('')
const newAPIKey = ref('')
const newModel = ref('')
const activeProvider = ref<string | null>(null)
const editAPIKey = ref('')

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

async function refresh() {
  await Promise.all([refreshProviders(), refreshStyles()])
}

async function refreshProviders() {
  try {
    const p = await api.listProviders()
    providers.value = p.providers || []
  } catch (e: any) {
    message.error(`加载 providers 失败: ${e.message}`)
  }
}

async function refreshStyles() {
  try {
    const r = await api.getStyles()
    styles.value = r.styles || []
  } catch (e: any) {
    message.error(`加载 styles 失败: ${e.message}`)
  }
}

// --- Provider handlers (unchanged) ---

async function onAddProvider() {
  if (!newName.value.trim() || !newProtocol.value || !newModel.value.trim()) {
    message.warning('名称、协议、模型为必填')
    return
  }
  try {
    await api.addProvider({
      name: newName.value.trim(),
      protocol: newProtocol.value,
      base_url: newBaseURL.value.trim(),
      api_key: newAPIKey.value.trim(),
      model: newModel.value.trim(),
    })
    message.success('已添加')
    showAddProvider.value = false
    newName.value = ''; newBaseURL.value = ''; newAPIKey.value = ''; newModel.value = ''
    await refreshProviders()
  } catch (e: any) {
    message.error(`添加失败: ${e.message}`)
  }
}

async function onDeleteProvider(name: string) {
  if (!window.confirm(`确定删除 provider "${name}"?`)) return
  try {
    await api.deleteProvider(name)
    message.success('已删除')
    await refreshProviders()
  } catch (e: any) {
    message.error(`删除失败: ${e.message}`)
  }
}

async function onSetDefaultProvider(name: string) {
  try {
    await api.setDefaultProvider(name)
    message.success(`已设为默认: ${name}`)
    await refreshProviders()
  } catch (e: any) {
    message.error(`设置失败: ${e.message}`)
  }
}

async function onSaveAPIKey() {
  if (!activeProvider.value) return
  try {
    await api.setProviderAPIKey(activeProvider.value, editAPIKey.value)
    message.success('已保存 API Key')
    activeProvider.value = null
    editAPIKey.value = ''
  } catch (e: any) {
    message.error(`保存失败: ${e.message}`)
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

// Cancel/close handlers.

function close() { (window as any).closeAppSettings?.() }
function closeStyleEditor() { showAddStyle.value = false; resetNewStyle() }

// Built-in style ids that are read-only on the API; the UI mirrors
// that so the user doesn't get a misleading 400 when they click
// edit / delete on a built-in.
const builtInStyles = new Set(['cute', 'guofeng', 'tech'])
function isBuiltIn(id: string) { return builtInStyles.has(id) }
</script>

<template>
  <NModal :show="true" @update:show="close" preset="card" title="应用设置" style="width: 760px; max-height: 80vh; overflow: auto">
    <NTabs v-model:value="tab" type="line" animated>
      <NTabPane name="providers" tab="LLM 提供商">
        <NSpace vertical size="large">
          <div>
            <h3 class="section-title">已配置的 LLM 提供商</h3>
            <NSpace vertical size="small">
              <div v-for="p in providers" :key="p.name" class="provider-row">
                <div class="provider-meta">
                  <NTag :type="p.is_default ? 'success' : 'default'" size="small" style="margin-right: 6px">
                    {{ p.is_default ? '默认' : '备选' }}
                  </NTag>
                  <strong>{{ p.name }}</strong>
                  <span class="muted">({{ p.protocol }} · {{ p.model }})</span>
                </div>
                <NSpace size="small">
                  <NButton size="small" @click="activeProvider = p.name; editAPIKey = ''">修改 Key</NButton>
                  <NButton size="small" v-if="!p.is_default" @click="onSetDefaultProvider(p.name)">设为默认</NButton>
                  <NButton size="small" v-if="!p.is_default" type="error" ghost @click="onDeleteProvider(p.name)">删除</NButton>
                </NSpace>
              </div>
            </NSpace>
            <NSpace style="margin-top: 8px">
              <NButton size="small" @click="showAddProvider = !showAddProvider" type="primary" ghost>
                {{ showAddProvider ? '取消' : '+ 添加 Provider' }}
              </NButton>
            </NSpace>
            <div v-if="showAddProvider" class="add-form">
              <NSpace vertical size="small">
                <NInput v-model:value="newName" placeholder="名称 (例: my-openai)" size="small" />
                <NSelect
                  v-model:value="newProtocol"
                  :options="[{label:'openai',value:'openai'},{label:'anthropic',value:'anthropic'}]"
                  size="small"
                />
                <NInput v-model:value="newBaseURL" placeholder="Base URL (可空)" size="small" />
                <NInput v-model:value="newAPIKey" placeholder="API Key (可空)" type="password" size="small" show-password-on="click" />
                <NInput v-model:value="newModel" placeholder="默认模型名 (例: gpt-4o-mini)" size="small" />
                <NButton type="primary" size="small" @click="onAddProvider">提交</NButton>
              </NSpace>
            </div>
          </div>

          <div v-if="activeProvider">
            <h3 class="section-title">修改 API Key — {{ activeProvider }}</h3>
            <NSpace>
              <NInput v-model:value="editAPIKey" placeholder="新的 API Key" type="password" show-password-on="click" style="width: 360px" />
              <NButton type="primary" @click="onSaveAPIKey">保存</NButton>
              <NButton @click="activeProvider = null">取消</NButton>
            </NSpace>
          </div>
        </NSpace>
      </NTabPane>

      <NTabPane name="styles" tab="风格配置">
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
      </NTabPane>
    </NTabs>

    <template #footer>
      <NSpace justify="end">
        <NButton @click="close">关闭</NButton>
      </NSpace>
    </template>
  </NModal>
</template>

<style scoped>
.section-title { margin: 0 0 8px 0; font-size: 13px; font-weight: 600; }
.provider-row, .style-row {
  display: flex; justify-content: space-between; align-items: center;
  padding: 8px 10px;
  background: var(--bg-3);
  border-radius: 6px;
  margin-bottom: 6px;
  gap: 12px;
}
.provider-meta, .style-meta { display: flex; flex-direction: column; gap: 2px; min-width: 0; flex: 1; }
.style-desc {
  white-space: nowrap; overflow: hidden; text-overflow: ellipsis;
  max-width: 480px;
}
.muted { color: var(--text-3); font-size: 12px; }
.add-form {
  margin-top: 8px; padding: 8px;
  background: var(--bg-2); border: 1px solid var(--border-2); border-radius: 6px;
}
code {
  background: var(--bg-3); padding: 1px 6px; border-radius: 3px;
  font-family: ui-monospace, Menlo, monospace; font-size: 12px;
}
</style>
