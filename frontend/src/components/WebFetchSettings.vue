<script setup lang="ts">
import { Check } from './icons'
import { computed, onMounted, ref } from 'vue'
import {
  NButton, NCollapse, NCollapseItem, NInput, NInputNumber, NSelect,
  NTag, useMessage,
} from 'naive-ui'
import {
  getWebFetchSettings,
  updateWebFetchSettings,
  testWebFetchConnection,
  type WebFetchSettings,
} from '../api/client'

const message = useMessage()

type FetchProvider = 'local' | 'deepseek' | 'qwen'

const settings = ref<WebFetchSettings | null>(null)
const loading = ref(false)
const saving = ref(false)
const testing = ref(false)
const form = ref({
  provider: 'local' as FetchProvider,
  apiKey: '',
  clearKey: false,
  baseUrl: '',
  path: '',
  model: '',
  dailyQuota: 0,
  requestTimeout: '20s',
})

const providerOptions = [
  { label: '本机抓取', value: 'local' },
  { label: 'DeepSeek', value: 'deepseek' },
  { label: 'Qwen（百炼）', value: 'qwen' },
]

const showsCredentials = computed(() => form.value.provider !== 'local')
const showsPath = computed(() => form.value.provider === 'deepseek')
const showsModel = computed(() => form.value.provider === 'qwen')
const providerHint = computed(() => {
  switch (form.value.provider) {
    case 'deepseek':
      return '模型调用 web_fetch 时走 POST /v1/tools/fetch。失败、空正文或 5xx 会回退本机 GET。POST 始终本机发出。'
    case 'qwen':
      return '模型调用 web_fetch 时由百炼打开该 URL。没有按该地址返回正文时回退本机 GET。'
    default:
      return '由本机直接请求 URL。不消耗厂商配额。'
  }
})

const isDirty = computed(() => {
  if (!settings.value) return false
  const s = settings.value
  if (form.value.provider !== s.provider) return true
  if (form.value.clearKey) return true
  if (form.value.apiKey !== '') return true
  if (form.value.baseUrl !== (s.base_url || '')) return true
  if (form.value.path !== (s.path || '')) return true
  if (form.value.model !== (s.model || '')) return true
  if (form.value.dailyQuota !== s.daily_quota) return true
  if (form.value.requestTimeout !== (s.request_timeout || '20s')) return true
  return false
})

const quotaProgress = computed(() => {
  if (!settings.value) return { text: '...', percent: 0, unlimited: true }
  const used = settings.value.used_today
  const cap = settings.value.daily_quota
  if (cap === 0) return { text: `${used} 已使用 (无上限)`, percent: 0, unlimited: true }
  const pct = Math.min(100, Math.round((used / cap) * 100))
  return { text: `${used} / ${cap} 已使用`, percent: pct, unlimited: false }
})

function apply(s: WebFetchSettings) {
  settings.value = s
  form.value = {
    provider: (s.provider as FetchProvider) || 'local',
    apiKey: '',
    clearKey: false,
    baseUrl: s.base_url || '',
    path: s.path || '',
    model: s.model || '',
    dailyQuota: s.daily_quota,
    requestTimeout: s.request_timeout || '20s',
  }
}

async function loadSettings() {
  loading.value = true
  try {
    apply(await getWebFetchSettings())
  } catch (e: any) {
    message.error(`加载网页抓取设置失败: ${e?.message || e}`)
  } finally {
    loading.value = false
  }
}

async function save() {
  if (!isDirty.value) return
  saving.value = true
  try {
    const patch: any = {
      provider: form.value.provider,
      base_url: form.value.baseUrl,
      path: form.value.path,
      model: form.value.model,
      daily_quota: form.value.dailyQuota,
      request_timeout: form.value.requestTimeout,
    }
    if (form.value.clearKey) patch.clear_api_key = true
    else if (form.value.apiKey !== '') patch.api_key = form.value.apiKey
    apply(await updateWebFetchSettings(patch))
    message.success('抓取设置已保存')
  } catch (e: any) {
    message.error(`保存失败: ${e?.message || e}`)
  } finally {
    saving.value = false
  }
}

function reset() {
  if (settings.value) apply(settings.value)
}

async function testConnection() {
  testing.value = true
  try {
    const res = await testWebFetchConnection()
    if (res.ok) {
      message.success(res.provider === 'local' ? '本机抓取无需连接测试' : `连接正常 (${res.provider})`)
    } else {
      message.error(`连接失败: ${res.error || '未知错误'}`)
    }
  } catch (e: any) {
    message.error(`测试失败: ${e?.message || e}`)
  } finally {
    testing.value = false
  }
}

onMounted(loadSettings)
</script>

<template>
  <div class="webfetch-shell" v-if="!loading || settings">
    <div class="section-title">网络抓取</div>
    <div v-if="settings" class="status-card">
      <div class="status-row">
        <div class="status-label">渠道</div>
        <div class="status-val">{{ settings.provider }}</div>
      </div>
      <div class="status-row">
        <div class="status-label">API Key</div>
        <div class="status-val">
          <NTag v-if="settings.provider === 'local'" size="small" :bordered="false">不需要</NTag>
          <NTag v-else-if="settings.has_key" type="success" size="small" :bordered="false">已配置</NTag>
          <NTag v-else type="warning" size="small" :bordered="false">未配置</NTag>
        </div>
      </div>
      <div class="status-row">
        <div class="status-label">今日用量</div>
        <div class="status-val quota-cell">
          <span class="quota-text">{{ quotaProgress.text }}</span>
          <div v-if="!quotaProgress.unlimited" class="quota-bar">
            <div class="quota-bar-fill" :style="{ width: quotaProgress.percent + '%' }"></div>
          </div>
        </div>
      </div>
    </div>

    <NCollapse default-expanded-names="basic" class="settings-collapse">
      <NCollapseItem title="抓取渠道" name="basic">
        <div class="form-grid">
          <div class="form-row">
            <div class="form-label">Provider</div>
            <NSelect v-model:value="form.provider" :options="providerOptions" size="small" style="width: 240px" />
            <div class="form-hint">{{ providerHint }}</div>
          </div>
          <div v-if="showsCredentials" class="form-row">
            <div class="form-label">API Key</div>
            <NInput
              v-model:value="form.apiKey"
              type="password"
              :placeholder="settings?.has_key ? '已配置 - 留空保留, 填写替换' : '粘贴 API Key'"
              size="small"
              show-password-on="click"
              style="width: 320px"
            />
            <NButton
              v-if="settings?.has_key"
              size="tiny"
              quaternary
              type="warning"
              @click="form.clearKey = !form.clearKey"
            >
              <template v-if="form.clearKey"><Check :size="12" /> 将删除</template>
              <template v-else>删除 Key</template>
            </NButton>
          </div>
          <div v-if="showsCredentials" class="form-row">
            <div class="form-label">Base URL</div>
            <NInput
              v-model:value="form.baseUrl"
              :placeholder="form.provider === 'qwen' ? 'https://{WorkspaceId}.cn-beijing.maas.aliyuncs.com/compatible-mode/v1' : 'https://api.deepseek.com'"
              size="small"
              style="width: 420px"
            />
            <div class="form-hint">Qwen 必填。DeepSeek 留空使用 api.deepseek.com。</div>
          </div>
          <div v-if="showsPath" class="form-row">
            <div class="form-label">Path</div>
            <NInput v-model:value="form.path" placeholder="/v1/tools/fetch" size="small" style="width: 200px" />
          </div>
          <div v-if="showsModel" class="form-row">
            <div class="form-label">模型</div>
            <NInput v-model:value="form.model" placeholder="qwen3.8-flash" size="small" style="width: 240px" />
            <div class="form-hint">留空使用 qwen3.8-flash</div>
          </div>
          <div class="form-row">
            <div class="form-label">每日配额</div>
            <NInputNumber v-model:value="form.dailyQuota" :min="0" :max="100000" :step="10" size="small" style="width: 140px" />
            <div class="form-hint">只计算厂商抓取。超出后该次调用回退本机。0 = 无限。</div>
          </div>
          <div class="form-row">
            <div class="form-label">请求超时</div>
            <NInput v-model:value="form.requestTimeout" placeholder="20s" size="small" style="width: 140px" />
            <div class="form-hint">上限 60s</div>
          </div>
        </div>
      </NCollapseItem>
    </NCollapse>

    <div class="action-bar">
      <NButton size="small" :disabled="!isDirty || saving" @click="reset">恢复</NButton>
      <NButton size="small" type="primary" :disabled="!isDirty" :loading="saving" @click="save">保存</NButton>
      <NButton size="small" :loading="testing" @click="testConnection">测试连接</NButton>
    </div>
  </div>
  <div v-else class="loading-hint">加载中…</div>
</template>

<style scoped>
.webfetch-shell {
  display: flex;
  flex-direction: column;
  gap: 16px;
  margin-top: 8px;
  padding-top: 16px;
  border-top: 1px solid var(--border-subtle);
}
.section-title {
  font-size: 14px;
  font-weight: 600;
  color: var(--text-primary);
}
.status-card {
  display: flex;
  flex-direction: column;
  gap: 8px;
  padding: 14px 16px;
  background: var(--surface-1);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-md);
}
.status-row {
  display: flex;
  align-items: center;
  gap: 12px;
  font-size: 13px;
  padding: 4px 0;
  border-top: 1px solid var(--border-subtle);
}
.status-row:first-child { border-top: none; padding-top: 0; }
.status-label {
  width: 96px;
  flex-shrink: 0;
  color: var(--text-secondary);
  font-size: 12.5px;
  font-weight: 500;
}
.status-val {
  color: var(--text-primary);
  display: inline-flex;
  align-items: center;
  gap: 8px;
}
.quota-cell { flex: 1; display: flex; align-items: center; gap: 10px; }
.quota-text {
  font-variant-numeric: tabular-nums;
  font-size: 12.5px;
  color: var(--text-secondary);
  min-width: 160px;
}
.quota-bar {
  flex: 1;
  height: 4px;
  border-radius: 2px;
  background: var(--surface-2);
  overflow: hidden;
  max-width: 280px;
}
.quota-bar-fill { height: 100%; background: var(--brand-500); }
.form-grid { display: flex; flex-direction: column; }
.form-row {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 10px 0;
  border-top: 1px solid var(--border-subtle);
}
.form-row:first-child { border-top: none; padding-top: 2px; }
.form-label {
  width: 110px;
  flex-shrink: 0;
  font-size: 12.5px;
  font-weight: 500;
  color: var(--text-primary);
}
.form-hint {
  flex: 1;
  min-width: 0;
  font-size: 11.5px;
  line-height: 1.5;
  color: var(--text-tertiary);
}
.action-bar {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
  padding-top: 12px;
  border-top: 1px solid var(--border-subtle);
}
.loading-hint {
  padding: 24px;
  text-align: center;
  color: var(--text-tertiary);
  font-size: 13px;
}
</style>
