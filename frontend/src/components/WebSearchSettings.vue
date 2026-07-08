<script setup lang="ts">
// Web-search settings panel.
//
// Rendered as a tab in AppSettingsModal. The component owns
// its own loading/saving state and round-trips with the
// three /api/v1/settings/web_search/* endpoints:
//
//   GET  /settings/web_search       — read current state
//   PUT  /settings/web_search       — partial update
//   POST /settings/web_search/test  — connection check
//
// Save semantics: the server treats every PUT field as
// optional. A blank `apiKey` is taken to mean "leave the
// existing key alone"; a separate `clearKey` flag deletes it.
// This matches the standard settings-modal pattern of not
// forcing the user to re-paste secrets on every save.

import { computed, onMounted, ref, watch } from 'vue'
import {
  NButton, NCollapse, NCollapseItem, NInput, NInputNumber, NSelect, NSwitch,
  NSpace, NTag, useMessage,
} from 'naive-ui'
import {
  getWebSearchSettings,
  updateWebSearchSettings,
  testWebSearchConnection,
  type WebSearchSettings,
} from '../api/client'

const message = useMessage()

// ---- Local reactive state ----
// `form` is the editable draft. We only push it back to the
// server when the user clicks 保存. `settings` holds the
// last server-snapshot for the "X / Y used today" status
// display — these values come from the server, not the form.

const settings = ref<WebSearchSettings | null>(null)
const loading = ref(false)
const saving = ref(false)
const testing = ref(false)

// Editable draft. Defaults to empty strings so the inputs
// don't flicker "undefined" on first load.
const form = ref({
  enabled: false,
  provider: 'tavily' as 'tavily' | 'openai_compat',
  apiKey: '',
  clearKey: false,
  baseUrl: '',
  path: '',
  topic: 'general' as '' | 'general' | 'news' | 'finance',
  dailyQuota: 0,
  requestTimeout: '20s',
})

// Provider enum. Empty string maps to "tavily" on save.
const providerOptions = [
  { label: 'Tavily (推荐)', value: 'tavily' },
  { label: 'OpenAI 兼容 (自配)', value: 'openai_compat' },
]

// Topic enum. Only used for tavily; the server silently
// ignores it for openai_compat.
const topicOptions = [
  { label: '通用', value: 'general' },
  { label: '新闻', value: 'news' },
  { label: '金融', value: 'finance' },
]

// isDirty is true when the form differs from the last
// server snapshot. The save button is disabled while
// not dirty (or while a save is in flight).
const isDirty = computed(() => {
  if (!settings.value) return false
  const s = settings.value
  if (form.value.enabled !== s.enabled) return true
  if (form.value.provider !== s.provider) return true
  if (form.value.clearKey) return true
  if (form.value.apiKey !== '') return true
  if (form.value.baseUrl !== (s.base_url || '')) return true
  if (form.value.path !== (s.path || '')) return true
  if (form.value.topic !== (s.topic || 'general')) return true
  if (form.value.dailyQuota !== s.daily_quota) return true
  if (form.value.requestTimeout !== (s.request_timeout || '20s')) return true
  return false
})

// quotaProgress derives "X / Y used today" + a percentage
// for the status bar. daily_quota=0 means unlimited, in
// which case the bar is hidden.
const quotaProgress = computed(() => {
  if (!settings.value) return { text: '...', percent: 0, unlimited: true }
  const used = settings.value.used_today
  const cap = settings.value.daily_quota
  if (cap === 0) {
    return { text: `${used} 已使用 (无上限)`, percent: 0, unlimited: true }
  }
  const pct = Math.min(100, Math.round((used / cap) * 100))
  return { text: `${used} / ${cap} 已使用`, percent: pct, unlimited: false }
})

// isOpenAICompat gates the visibility of the base_url
// and path fields (only relevant for that provider).
const isOpenAICompat = computed(() => form.value.provider === 'openai_compat')

// ====================================================================
// Load / Save / Test
// ====================================================================

async function loadSettings() {
  loading.value = true
  try {
    const s = await getWebSearchSettings()
    settings.value = s
    form.value = {
      enabled: s.enabled,
      provider: (s.provider as 'tavily' | 'openai_compat') || 'tavily',
      apiKey: '',
      clearKey: false,
      baseUrl: s.base_url || '',
      path: s.path || '',
      topic: (s.topic as 'general' | 'news' | 'finance') || 'general',
      dailyQuota: s.daily_quota,
      requestTimeout: s.request_timeout || '20s',
    }
  } catch (e: any) {
    message.error(`加载 web_search 设置失败: ${e?.message || e}`)
  } finally {
    loading.value = false
  }
}

async function save() {
  if (!isDirty.value) return
  saving.value = true
  try {
    const patch: any = {
      enabled: form.value.enabled,
      provider: form.value.provider,
      base_url: form.value.baseUrl,
      path: form.value.path,
      topic: form.value.topic,
      daily_quota: form.value.dailyQuota,
      request_timeout: form.value.requestTimeout,
    }
    if (form.value.clearKey) {
      patch.clear_api_key = true
    } else if (form.value.apiKey !== '') {
      patch.api_key = form.value.apiKey
    }
    const updated = await updateWebSearchSettings(patch)
    settings.value = updated
    // Clear the key field after a successful save — the
    // user shouldn't see the just-saved value lingering
    // in plaintext on the screen.
    form.value.apiKey = ''
    form.value.clearKey = false
    message.success('已保存')
  } catch (e: any) {
    message.error(`保存失败: ${e?.message || e}`)
  } finally {
    saving.value = false
  }
}

function reset() {
  if (!settings.value) return
  form.value = {
    enabled: settings.value.enabled,
    provider: (settings.value.provider as 'tavily' | 'openai_compat') || 'tavily',
    apiKey: '',
    clearKey: false,
    baseUrl: settings.value.base_url || '',
    path: settings.value.path || '',
    topic: (settings.value.topic as 'general' | 'news' | 'finance') || 'general',
    dailyQuota: settings.value.daily_quota,
    requestTimeout: settings.value.request_timeout || '20s',
  }
}

async function testConnection() {
  testing.value = true
  try {
    const res = await testWebSearchConnection()
    if (res.ok) {
      message.success(
        `连接正常 (provider: ${res.provider || '?'}, 返回 ${res.result_count ?? 0} 条结果)`,
      )
    } else {
      message.error(`连接失败: ${res.error || '未知错误'}`)
    }
  } catch (e: any) {
    message.error(`测试失败: ${e?.message || e}`)
  } finally {
    testing.value = false
  }
}

// Reset the quota display after a save so the user sees
// their new "used / total" without having to reload.
watch(() => settings.value?.used_today, () => {
  // No-op — computed quotaProgress already reads it.
})

onMounted(loadSettings)
</script>

<template>
  <div class="websearch-shell" v-if="!loading || settings">
    <!-- Status card: read-only summary of live state -->
    <div v-if="settings" class="status-card">
      <div class="status-row">
        <div class="status-label">当前状态</div>
        <NTag :type="settings.enabled ? 'success' : 'default'" size="small" :bordered="false">
          {{ settings.enabled ? '已启用' : '未启用' }}
        </NTag>
      </div>
      <div class="status-row">
        <div class="status-label">Provider</div>
        <div class="status-val">{{ settings.provider }}</div>
      </div>
      <div class="status-row">
        <div class="status-label">API Key</div>
        <div class="status-val">
          <NTag v-if="settings.has_key" type="success" size="small" :bordered="false">已配置</NTag>
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
      <div class="status-row">
        <div class="status-label">配额重置</div>
        <div class="status-val">
          <span v-if="settings.daily_quota === 0">永不 (无上限)</span>
          <span v-else>{{ new Date(settings.resets_at).toLocaleString() }}</span>
        </div>
      </div>
    </div>

    <!-- Edit form -->
    <NCollapse default-expanded-names="basic" class="form-collapse">
      <NCollapseItem title="基本设置" name="basic">
        <div class="form-grid">
          <!-- Enable toggle -->
          <div class="form-row">
            <div class="form-label">启用网络搜索</div>
            <NSwitch v-model:value="form.enabled" size="small" />
            <div class="form-hint">关闭后 LLM 完全看不到 web_search 工具</div>
          </div>

          <!-- Provider -->
          <div class="form-row">
            <div class="form-label">Provider</div>
            <NSelect
              v-model:value="form.provider"
              :options="providerOptions"
              size="small"
              style="width: 240px"
            />
            <div class="form-hint">
              Tavily: 1000 次/月免费 · OpenAI 兼容: 自配 endpoint
            </div>
          </div>

          <!-- Topic (tavily only) -->
          <div v-if="form.provider === 'tavily'" class="form-row">
            <div class="form-label">主题</div>
            <NSelect
              v-model:value="form.topic"
              :options="topicOptions"
              size="small"
              style="width: 160px"
            />
            <div class="form-hint">Tavily 专用 (general/news/finance)</div>
          </div>
        </div>
      </NCollapseItem>

      <NCollapseItem title="凭据" name="credentials">
        <div class="form-grid">
          <!-- API Key (tavily) -->
          <div v-if="form.provider === 'tavily'" class="form-row">
            <div class="form-label">API Key</div>
            <NInput
              v-model:value="form.apiKey"
              type="password"
              :placeholder="settings?.has_key ? '已配置 - 留空保留, 填写替换' : 'tvly-...'"
              size="small"
              show-password-on="click"
              style="width: 320px"
            />
            <NButton
              v-if="settings?.has_key"
              size="tiny"
              quaternary
              type="warning"
              :disabled="form.clearKey"
              @click="form.clearKey = !form.clearKey"
            >
              {{ form.clearKey ? '✓ 将删除' : '删除 Key' }}
            </NButton>
            <div class="form-hint">
              在 <a href="https://app.tavily.com/home" target="_blank" rel="noopener">app.tavily.com</a> 获取
            </div>
          </div>

          <!-- Base URL (openai_compat) -->
          <div v-if="isOpenAICompat" class="form-row">
            <div class="form-label">Base URL</div>
            <NInput
              v-model:value="form.baseUrl"
              placeholder="https://s.jina.ai"
              size="small"
              style="width: 320px"
            />
            <div class="form-hint">必须 https://（loopback 例外）</div>
          </div>

          <!-- Optional path override -->
          <div v-if="isOpenAICompat" class="form-row">
            <div class="form-label">Path</div>
            <NInput
              v-model:value="form.path"
              placeholder="/search"
              size="small"
              style="width: 200px"
            />
            <div class="form-hint">请求路径，留空默认 /search</div>
          </div>

          <!-- Optional API key (openai_compat) -->
          <div v-if="isOpenAICompat" class="form-row">
            <div class="form-label">API Key (可选)</div>
            <NInput
              v-model:value="form.apiKey"
              type="password"
              :placeholder="settings?.has_key ? '已配置 - 留空保留, 填写替换' : '部分服务不需要'"
              size="small"
              show-password-on="click"
              style="width: 320px"
            />
            <NButton
              v-if="settings?.has_key"
              size="tiny"
              quaternary
              type="warning"
              :disabled="form.clearKey"
              @click="form.clearKey = !form.clearKey"
            >
              {{ form.clearKey ? '✓ 将删除' : '删除 Key' }}
            </NButton>
          </div>
        </div>
      </NCollapseItem>

      <NCollapseItem title="配额与超时" name="quota">
        <div class="form-grid">
          <div class="form-row">
            <div class="form-label">每日配额</div>
            <NInputNumber
              v-model:value="form.dailyQuota"
              :min="0"
              :max="100000"
              :step="10"
              size="small"
              style="width: 140px"
            />
            <div class="form-hint">0 = 无限; 单日超过后 LLM 收到 E_QUOTA</div>
          </div>
          <div class="form-row">
            <div class="form-label">请求超时</div>
            <NInput
              v-model:value="form.requestTimeout"
              placeholder="20s"
              size="small"
              style="width: 140px"
            />
            <div class="form-hint">Go duration 格式 (如 20s, 30s)，上限 60s</div>
          </div>
        </div>
      </NCollapseItem>
    </NCollapse>

    <!-- Action bar -->
    <div class="action-bar">
      <NButton size="small" @click="reset" :disabled="!isDirty || saving">恢复</NButton>
      <NButton
        size="small"
        type="primary"
        :disabled="!isDirty"
        :loading="saving"
        @click="save"
      >保存</NButton>
      <NButton
        size="small"
        :loading="testing"
        @click="testConnection"
      >测试连接</NButton>
    </div>
  </div>
  <div v-else class="loading-hint">加载中…</div>
</template>

<style scoped>
.websearch-shell {
  display: flex; flex-direction: column; gap: 12px;
  padding: 4px 0;
  height: 58vh;
}

/* ---- Status card (read-only summary) ---- */
.status-card {
  display: flex; flex-direction: column; gap: 6px;
  padding: 12px 14px;
  border: 1px solid var(--border-2);
  border-radius: 8px;
  background: var(--bg-2);
}
.status-row {
  display: flex; align-items: center; gap: 12px;
  font-size: 12.5px;
  min-height: 22px;
}
.status-label {
  width: 80px; flex-shrink: 0;
  color: var(--text-3);
  font-size: 12px;
}
.status-val {
  color: var(--text-1);
}
.quota-cell { flex: 1; display: flex; align-items: center; gap: 8px; }
.quota-text { font-variant-numeric: tabular-nums; }
.quota-bar {
  flex: 1; height: 4px;
  border-radius: 2px;
  background: var(--bg-3);
  overflow: hidden;
  max-width: 200px;
}
.quota-bar-fill {
  height: 100%;
  background: var(--accent);
  transition: width 0.2s ease;
}

/* ---- Form ---- */
.form-collapse {
  flex: 1; min-height: 0; overflow-y: auto;
  border: 1px solid var(--border-2);
  border-radius: 8px;
}
.form-grid {
  display: flex; flex-direction: column; gap: 2px;
  padding: 2px 0;
}
.form-row {
  display: flex; align-items: center; gap: 8px;
  padding: 6px 0;
}
.form-label {
  font-size: 12px; color: var(--text-2);
  width: 110px; flex-shrink: 0;
  line-height: 1.4;
}
.form-hint {
  font-size: 11px; color: var(--text-4);
  flex: 1; line-height: 1.4;
}
.form-hint a { color: var(--accent); text-decoration: none; }
.form-hint a:hover { text-decoration: underline; }

/* ---- Action bar ---- */
.action-bar {
  display: flex; justify-content: flex-end; gap: 8px;
  padding: 4px 0 0;
  border-top: 1px solid var(--border-2);
  padding-top: 10px;
  flex-shrink: 0;
}

.loading-hint {
  padding: 40px;
  text-align: center;
  color: var(--text-3);
}
</style>
