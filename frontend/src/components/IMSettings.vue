<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import {
  NButton, NCollapse, NCollapseItem, NDynamicTags, NInput, NInputNumber,
  NPopconfirm, NSelect, NSwitch, NTag, useMessage,
} from 'naive-ui'
import { MessageSquare, Plus, RotateCw, Trash2 } from './icons'
import * as api from '../api/client'

const message = useMessage()

const loading = ref(false)
const saving = ref(false)
const healthLoading = ref(false)
const imConfig = ref<api.IMConfig | null>(null)
const health = ref<api.IMHealth | null>(null)
const events = ref<api.IMLifecycleEvent[]>([])
const eventError = ref('')
const testResults = ref<Record<string, api.IMTestResult>>({})
const testing = ref<Record<string, boolean>>({})
const personasText = ref('{}')
const rateLimitText = ref('[]')
const identityLinksText = ref('[]')
const cronJobsText = ref('[]')
const fallbackText = ref('[]')
const toolsText = ref('[]')
const platformExtraText = ref<Record<number, string>>({})
let eventAbort: AbortController | null = null

const platformOptions = [
  { label: '飞书', value: 'feishu' },
  { label: 'Telegram', value: 'telegram' },
  { label: '企业微信', value: 'wecom' },
  { label: 'QQ', value: 'qq' },
  { label: '微信', value: 'wechat' },
  { label: '钉钉', value: 'dingtalk' },
  { label: 'Slack', value: 'slack' },
]

const variantOptions = [
  { label: 'bot', value: 'bot' },
  { label: 'openapi', value: 'openapi' },
  { label: 'polling', value: 'polling' },
  { label: 'webhook', value: 'webhook' },
  { label: 'guild', value: 'guild' },
  { label: 'private', value: 'private' },
  { label: 'wechatbot', value: 'wechatbot' },
  { label: 'app', value: 'app' },
]

const modeOptions = [
  { label: 'webhook', value: 'webhook' },
  { label: 'websocket', value: 'websocket' },
  { label: 'polling', value: 'polling' },
]

const sessionScopeOptions = [
  { label: '按发送者', value: 'per_sender' },
  { label: '按群聊', value: 'per_chat' },
  { label: '按话题', value: 'per_thread' },
]

const trustOptions = [
  { label: 'manual', value: 'manual' },
  { label: 'high', value: 'high' },
  { label: 'none', value: 'none' },
]

const enabledPlatforms = computed(() =>
  (imConfig.value?.platforms || []).filter(p => p.enabled).length,
)

const healthByKey = computed(() => {
  const out: Record<string, api.IMHealthStatus> = {}
  for (const item of health.value?.platforms || []) {
    out[platformKey(item.platform, item.variant)] = item
  }
  return out
})

function platformKey(type: string, variant?: string) {
  return variant ? `${type}:${variant}` : type
}

function platformTitle(p: api.IMPlatformConfig) {
  const label = platformOptions.find(x => x.value === p.type)?.label || p.type || '未选择平台'
  return p.variant ? `${label} · ${p.variant}` : label
}

function healthFor(p: api.IMPlatformConfig) {
  return healthByKey.value[platformKey(p.type, p.variant)]
}

function statusLabel(status?: string) {
  switch (status) {
    case 'ok': return '已连接'
    case 'configured': return '已配置'
    case 'disabled': return '未启用'
    case 'unavailable': return '未注册'
    case 'error': return '异常'
    default: return status || '未知'
  }
}

function statusType(status?: string): 'success' | 'warning' | 'error' | 'default' {
  if (status === 'ok' || status === 'registered') return 'success'
  if (status === 'configured' || status === 'disabled' || status === 'not_configured') return 'warning'
  if (status === 'error' || status === 'unavailable' || status === 'not_implemented') return 'error'
  return 'default'
}

function ensurePlatform(p: api.IMPlatformConfig): api.IMPlatformConfig {
  if (!p.webhook) p.webhook = {}
  if (!p.out) p.out = {}
  if (!p.allowed_senders) p.allowed_senders = []
  if (!p.extra) p.extra = {}
  return p
}

function normalizeConfig(cfg: api.IMConfig): api.IMConfig {
  cfg.session ||= { scope: 'per_thread', record_sender: true, cross_platform: false }
  cfg.identity ||= { links: [], auto_link: { enabled: false, trust: 'manual' } }
  cfg.identity.auto_link ||= { enabled: false, trust: 'manual' }
  cfg.command ||= {
    prefix: '/',
    forward_unknown_to_agent: true,
    require_mention_in_group: true,
  }
  cfg.cron ||= { enabled: false, jobs: [] }
  cfg.media ||= {
    stt: {},
    tts: {},
    vision: { enabled: false, max_image_bytes: 5242880 },
    file_extract: { enabled: false, max_file_bytes: 20971520, types: [] },
  }
  cfg.platforms = (cfg.platforms || []).map(ensurePlatform)
  cfg.personas ||= { default: { style: 'tech', work_mode: 'daily' } }
  cfg.rate_limit ||= []
  cfg.fallback ||= []
  cfg.tools_allowlist_default ||= []
  return cfg
}

function syncJSONEditors(cfg: api.IMConfig) {
  personasText.value = toPrettyJSON(cfg.personas || {})
  rateLimitText.value = toPrettyJSON(cfg.rate_limit || [])
  identityLinksText.value = toPrettyJSON(cfg.identity?.links || [])
  cronJobsText.value = toPrettyJSON(cfg.cron?.jobs || [])
  fallbackText.value = toPrettyJSON(cfg.fallback || [])
  toolsText.value = toPrettyJSON(cfg.tools_allowlist_default || [])
  platformExtraText.value = {}
  ;(cfg.platforms || []).forEach((p, index) => {
    platformExtraText.value[index] = toPrettyJSON(p.extra || {})
  })
}

function toPrettyJSON(value: unknown) {
  return JSON.stringify(value ?? null, null, 2)
}

function parseJSON<T>(raw: string, fallback: T, label: string): T {
  const text = raw.trim()
  if (!text) return fallback
  try {
    return JSON.parse(text) as T
  } catch (err: any) {
    throw new Error(`${label} JSON 无效: ${err?.message || err}`)
  }
}

async function refreshConfig() {
  loading.value = true
  try {
    const cfg = normalizeConfig(await api.getIMConfig())
    imConfig.value = cfg
    syncJSONEditors(cfg)
  } catch (err: any) {
    message.error(`加载 IM 配置失败: ${err?.message || err}`)
  } finally {
    loading.value = false
  }
}

async function refreshHealth() {
  healthLoading.value = true
  try {
    health.value = await api.getIMHealth()
  } catch (err: any) {
    message.error(`刷新 IM 状态失败: ${err?.message || err}`)
  } finally {
    healthLoading.value = false
  }
}

async function refreshAll() {
  await Promise.all([refreshConfig(), refreshHealth()])
}

async function saveConfig() {
  if (!imConfig.value) return
  saving.value = true
  try {
    const next = normalizeConfig({ ...imConfig.value })
    next.personas = parseJSON<Record<string, api.IMPersona>>(personasText.value, {}, 'Persona')
    next.rate_limit = parseJSON<api.IMRateLimitRule[]>(rateLimitText.value, [], '限流规则')
    next.identity.links = parseJSON<api.IMIdentityLink[]>(identityLinksText.value, [], '身份链接')
    next.cron.jobs = parseJSON<api.IMCronConfig['jobs']>(cronJobsText.value, [], '调度任务')
    next.fallback = parseJSON<api.IMFallbackRule[]>(fallbackText.value, [], 'Fallback')
    next.tools_allowlist_default = parseJSON<string[]>(toolsText.value, [], '工具白名单')
    next.platforms = (next.platforms || []).map((p, index) => {
      const clone = ensurePlatform({ ...p })
      clone.webhook = { ...(p.webhook || {}) }
      clone.out = { ...(p.out || {}) }
      clone.allowed_senders = [...(p.allowed_senders || [])]
      clone.extra = parseJSON<Record<string, unknown>>(platformExtraText.value[index] || '{}', {}, `${platformTitle(p)} extra`)
      return clone
    })

    const updated = normalizeConfig(await api.updateIMConfig(next))
    imConfig.value = updated
    syncJSONEditors(updated)
    await refreshHealth()
    message.success('IM 配置已保存')
  } catch (err: any) {
    message.error(err?.message || String(err))
  } finally {
    saving.value = false
  }
}

function addPlatform(type = 'feishu') {
  if (!imConfig.value) return
  const platform = ensurePlatform({
    type,
    variant: type === 'feishu' ? 'bot' : '',
    enabled: false,
    mode: type === 'telegram' ? 'polling' : 'webhook',
    allowed_senders: ['*'],
    out: { use_openapi: type === 'feishu', api_base: type === 'feishu' ? 'https://open.feishu.cn' : '' },
  })
  imConfig.value.platforms = [...(imConfig.value.platforms || []), platform]
  platformExtraText.value[(imConfig.value.platforms.length || 1) - 1] = '{}'
}

function removePlatform(index: number) {
  if (!imConfig.value) return
  imConfig.value.platforms = (imConfig.value.platforms || []).filter((_, i) => i !== index)
  syncJSONEditors(imConfig.value)
}

function applyTemplate(p: api.IMPlatformConfig) {
  ensurePlatform(p)
  if (p.type === 'feishu') {
    p.variant ||= 'bot'
    p.mode ||= 'webhook'
    p.out!.use_openapi = true
    p.out!.api_base ||= 'https://open.feishu.cn'
  } else if (p.type === 'telegram') {
    p.mode ||= 'polling'
  } else if (p.type === 'wecom') {
    p.variant ||= 'app'
    p.mode ||= 'webhook'
  } else if (p.type === 'qq') {
    p.variant ||= 'guild'
    p.mode ||= 'websocket'
  } else if (p.type === 'wechat') {
    p.variant ||= 'wechatbot'
    p.mode ||= 'websocket'
  }
}

async function testPlatform(p: api.IMPlatformConfig) {
  if (!p.type) {
    message.warning('请先选择平台类型')
    return
  }
  const key = platformKey(p.type, p.variant)
  testing.value = { ...testing.value, [key]: true }
  try {
    const result = await api.testIMConnection(p.type, p.variant)
    testResults.value = { ...testResults.value, [key]: result }
    if (result.ok) message.success(`${platformTitle(p)} 自检通过`)
    else message.warning(`${platformTitle(p)} 自检未通过: ${result.error || result.status}`)
  } catch (err: any) {
    message.error(`测试连接失败: ${err?.message || err}`)
  } finally {
    testing.value = { ...testing.value, [key]: false }
  }
}

function startEventStream() {
  if (eventAbort) eventAbort.abort()
  const controller = new AbortController()
  eventAbort = controller
  eventError.value = ''
  api.streamIMEvents((ev) => {
    events.value = [ev, ...events.value].slice(0, 20)
  }, controller.signal).catch((err: any) => {
    if (controller.signal.aborted || eventAbort !== controller) return
    eventError.value = err?.message || String(err)
  })
}

function formatEventTime(t?: string) {
  if (!t) return ''
  const d = new Date(t)
  if (Number.isNaN(d.getTime())) return t
  return d.toLocaleTimeString()
}

onMounted(async () => {
  await refreshAll()
  startEventStream()
})

onBeforeUnmount(() => {
  if (eventAbort) eventAbort.abort()
})
</script>

<template>
  <div class="im-settings">
    <div class="im-section im-status-section">
      <div class="im-section-header">
        <div class="im-title-block">
          <div class="im-section-title">
            <MessageSquare :size="16" />
            IM 桥接
          </div>
          <p class="im-section-description">
            管理飞书、Telegram、企业微信、QQ、微信等 IM 平台连接。
          </p>
        </div>
        <div class="im-actions">
          <NButton size="small" quaternary :loading="healthLoading || loading" @click="refreshAll">
            <template #icon><RotateCw :size="14" /></template>
            刷新
          </NButton>
          <NButton size="small" type="primary" :loading="saving" :disabled="!imConfig" @click="saveConfig">
            保存
          </NButton>
        </div>
      </div>

      <div v-if="imConfig" class="im-status-grid">
        <div class="im-status-item">
          <span class="im-status-label">总开关</span>
          <NSwitch v-model:value="imConfig.enabled" size="small" />
        </div>
        <div class="im-status-item">
          <span class="im-status-label">Gateway</span>
          <NTag :type="health?.running ? 'success' : 'warning'" size="small" :bordered="false">
            {{ health?.running ? '运行中' : '未运行' }}
          </NTag>
        </div>
        <div class="im-status-item">
          <span class="im-status-label">启用平台</span>
          <span class="im-status-value">{{ enabledPlatforms }} / {{ imConfig.platforms?.length || 0 }}</span>
        </div>
        <div class="im-status-item">
          <span class="im-status-label">健康记录</span>
          <span class="im-status-value">{{ health?.platforms?.length || 0 }}</span>
        </div>
      </div>
      <div v-else class="im-empty">
        {{ loading ? '正在加载 IM 配置...' : '暂无 IM 配置' }}
      </div>
    </div>

    <template v-if="imConfig">
      <div class="im-section">
        <div class="im-section-header">
          <h3 class="im-section-title">全局策略</h3>
        </div>
        <div class="im-form-grid">
          <label class="im-field">
            <span>Session 粒度</span>
            <NSelect v-model:value="imConfig.session.scope" :options="sessionScopeOptions" size="small" />
          </label>
          <label class="im-field">
            <span>命令前缀</span>
            <NInput v-model:value="imConfig.command.prefix" size="small" placeholder="/" />
          </label>
          <label class="im-toggle">
            <NSwitch v-model:value="imConfig.session.record_sender" size="small" />
            <span>记录发送者</span>
          </label>
          <label class="im-toggle">
            <NSwitch v-model:value="imConfig.session.cross_platform" size="small" />
            <span>跨平台 session</span>
          </label>
          <label class="im-toggle">
            <NSwitch v-model:value="imConfig.command.require_mention_in_group" size="small" />
            <span>群聊需 @ 才响应</span>
          </label>
          <label class="im-toggle">
            <NSwitch v-model:value="imConfig.command.forward_unknown_to_agent" size="small" />
            <span>未知命令转给 Agent</span>
          </label>
          <label class="im-toggle">
            <NSwitch v-model:value="imConfig.audit_log" size="small" />
            <span>审计日志</span>
          </label>
          <label class="im-toggle">
            <NSwitch v-model:value="imConfig.audit_local_only" size="small" />
            <span>审计仅本地</span>
          </label>
        </div>
      </div>

      <div class="im-section">
        <div class="im-section-header">
          <h3 class="im-section-title">平台连接</h3>
          <div class="im-actions">
            <NButton size="small" type="primary" ghost @click="addPlatform('feishu')">
              <template #icon><Plus :size="14" /></template>
              添加飞书
            </NButton>
            <NButton size="small" ghost @click="addPlatform('telegram')">添加 Telegram</NButton>
          </div>
        </div>

        <div v-if="!(imConfig.platforms || []).length" class="im-empty">
          暂无平台连接，点击右上角添加。
        </div>
        <NCollapse v-else class="im-collapse" arrow-placement="right">
          <NCollapseItem
            v-for="(p, index) in imConfig.platforms"
            :key="`${index}-${p.type}-${p.variant}`"
            :name="String(index)"
          >
            <template #header>
              <div class="im-platform-header">
                <span class="im-platform-title">{{ platformTitle(p) }}</span>
                <NTag size="tiny" :type="p.enabled ? 'success' : 'default'" :bordered="false">
                  {{ p.enabled ? '启用' : '停用' }}
                </NTag>
                <NTag
                  size="tiny"
                  :type="statusType(healthFor(p)?.status)"
                  :bordered="false"
                >
                  {{ statusLabel(healthFor(p)?.status) }}
                </NTag>
                <span v-if="healthFor(p)?.error" class="im-inline-error">{{ healthFor(p)?.error }}</span>
              </div>
            </template>

            <div class="im-platform-body">
              <div class="im-platform-toolbar">
                <label class="im-toggle">
                  <NSwitch v-model:value="p.enabled" size="small" />
                  <span>启用平台</span>
                </label>
                <NButton size="tiny" ghost @click="applyTemplate(p)">套用默认</NButton>
                <NButton
                  size="tiny"
                  type="primary"
                  ghost
                  :loading="!!testing[platformKey(p.type, p.variant)]"
                  @click="testPlatform(p)"
                >
                  测试连接
                </NButton>
                <NPopconfirm positive-text="删除" negative-text="取消" @positive-click="removePlatform(index)">
                  <template #trigger>
                    <NButton size="tiny" quaternary type="error" title="删除平台" aria-label="删除平台">
                      <Trash2 :size="13" />
                    </NButton>
                  </template>
                  删除这个 IM 平台配置？
                </NPopconfirm>
              </div>

              <div v-if="testResults[platformKey(p.type, p.variant)]" class="im-test-result">
                <NTag
                  size="tiny"
                  :type="testResults[platformKey(p.type, p.variant)].ok ? 'success' : 'error'"
                  :bordered="false"
                >
                  {{ testResults[platformKey(p.type, p.variant)].status }}
                </NTag>
                <span v-if="testResults[platformKey(p.type, p.variant)].error">
                  {{ testResults[platformKey(p.type, p.variant)].error }}
                </span>
              </div>

              <div class="im-form-grid">
                <label class="im-field">
                  <span>平台</span>
                  <NSelect v-model:value="p.type" :options="platformOptions" size="small" @update:value="() => applyTemplate(p)" />
                </label>
                <label class="im-field">
                  <span>Variant</span>
                  <NSelect v-model:value="p.variant" :options="variantOptions" size="small" filterable tag clearable />
                </label>
                <label class="im-field">
                  <span>Mode</span>
                  <NSelect v-model:value="p.mode" :options="modeOptions" size="small" filterable tag clearable />
                </label>
                <label class="im-field">
                  <span>允许发送者</span>
                  <NDynamicTags v-model:value="p.allowed_senders" size="small" />
                </label>
              </div>

              <div class="im-subsection">
                <div class="im-subtitle">鉴权</div>
                <div class="im-form-grid">
                  <label class="im-field">
                    <span>App ID</span>
                    <NInput v-model:value="p.app_id" size="small" placeholder="cli_xxx / app_id" />
                  </label>
                  <label class="im-field">
                    <span>App Secret</span>
                    <NInput v-model:value="p.app_secret" size="small" type="password" show-password-on="click" />
                  </label>
                  <label class="im-field">
                    <span>Token</span>
                    <NInput v-model:value="p.token" size="small" type="password" show-password-on="click" />
                  </label>
                  <label class="im-field">
                    <span>API Key</span>
                    <NInput v-model:value="p.api_key" size="small" type="password" show-password-on="click" />
                  </label>
                  <label class="im-field">
                    <span>Verification Token</span>
                    <NInput v-model:value="p.verification_token" size="small" type="password" show-password-on="click" />
                  </label>
                  <label class="im-field">
                    <span>Encrypt Key</span>
                    <NInput v-model:value="p.encrypt_key" size="small" type="password" show-password-on="click" />
                  </label>
                  <label class="im-field">
                    <span>Corp ID</span>
                    <NInput v-model:value="p.corp_id" size="small" />
                  </label>
                  <label class="im-field">
                    <span>Corp Secret</span>
                    <NInput v-model:value="p.corp_secret" size="small" type="password" show-password-on="click" />
                  </label>
                  <label class="im-field">
                    <span>Callback AES Key</span>
                    <NInput v-model:value="p.callback_aes_key" size="small" type="password" show-password-on="click" />
                  </label>
                  <label class="im-field">
                    <span>Callback Token</span>
                    <NInput v-model:value="p.callback_token" size="small" type="password" show-password-on="click" />
                  </label>
                  <label class="im-field">
                    <span>Agent ID</span>
                    <NInputNumber :value="p.agent_id || null" :min="0" size="small" @update:value="(v: number | null) => { p.agent_id = v || undefined }" />
                  </label>
                  <label class="im-field">
                    <span>Endpoint</span>
                    <NInput v-model:value="p.endpoint" size="small" placeholder="http://127.0.0.1:9000" />
                  </label>
                </div>
              </div>

              <div class="im-subsection">
                <div class="im-subtitle">Webhook / Outbound</div>
                <div class="im-form-grid">
                  <label class="im-field">
                    <span>Webhook Listen</span>
                    <NInput v-model:value="p.webhook!.listen" size="small" placeholder=":9002" />
                  </label>
                  <label class="im-field">
                    <span>Webhook Path</span>
                    <NInput v-model:value="p.webhook!.path" size="small" placeholder="/im/telegram" />
                  </label>
                  <label class="im-toggle">
                    <NSwitch v-model:value="p.out!.use_openapi" size="small" />
                    <span>出站使用 OpenAPI</span>
                  </label>
                  <label class="im-field">
                    <span>OpenAPI Base</span>
                    <NInput v-model:value="p.out!.api_base" size="small" placeholder="https://open.feishu.cn" />
                  </label>
                </div>
              </div>

              <label class="im-json-field">
                <span>Extra JSON</span>
                <NInput v-model:value="platformExtraText[index]" type="textarea" size="small" :autosize="{ minRows: 3, maxRows: 8 }" />
              </label>
            </div>
          </NCollapseItem>
        </NCollapse>
      </div>

      <div class="im-section">
        <div class="im-section-header">
          <h3 class="im-section-title">高级策略</h3>
        </div>
        <NCollapse arrow-placement="right">
          <NCollapseItem title="Persona" name="personas">
            <label class="im-json-field">
              <span>personas</span>
              <NInput v-model:value="personasText" type="textarea" size="small" :autosize="{ minRows: 6, maxRows: 16 }" />
            </label>
          </NCollapseItem>
          <NCollapseItem title="限流与工具白名单" name="limits">
            <div class="im-json-grid">
              <label class="im-json-field">
                <span>rate_limit</span>
                <NInput v-model:value="rateLimitText" type="textarea" size="small" :autosize="{ minRows: 5, maxRows: 12 }" />
              </label>
              <label class="im-json-field">
                <span>tools_allowlist_default</span>
                <NInput v-model:value="toolsText" type="textarea" size="small" :autosize="{ minRows: 5, maxRows: 12 }" />
              </label>
            </div>
          </NCollapseItem>
          <NCollapseItem title="身份链接 / 调度 / Fallback" name="ops">
            <div class="im-form-grid im-form-grid--compact">
              <label class="im-toggle">
                <NSwitch v-model:value="imConfig.identity.auto_link.enabled" size="small" />
                <span>自动身份绑定</span>
              </label>
              <label class="im-field">
                <span>自动绑定信任级别</span>
                <NSelect v-model:value="imConfig.identity.auto_link.trust" :options="trustOptions" size="small" />
              </label>
              <label class="im-toggle">
                <NSwitch v-model:value="imConfig.cron.enabled" size="small" />
                <span>启用 IM 调度</span>
              </label>
            </div>
            <div class="im-json-grid">
              <label class="im-json-field">
                <span>identity.links</span>
                <NInput v-model:value="identityLinksText" type="textarea" size="small" :autosize="{ minRows: 5, maxRows: 12 }" />
              </label>
              <label class="im-json-field">
                <span>cron.jobs</span>
                <NInput v-model:value="cronJobsText" type="textarea" size="small" :autosize="{ minRows: 5, maxRows: 12 }" />
              </label>
              <label class="im-json-field">
                <span>fallback</span>
                <NInput v-model:value="fallbackText" type="textarea" size="small" :autosize="{ minRows: 5, maxRows: 12 }" />
              </label>
            </div>
          </NCollapseItem>
        </NCollapse>
      </div>

      <div class="im-section">
        <div class="im-section-header">
          <h3 class="im-section-title">Gateway 事件</h3>
          <NButton size="small" ghost @click="startEventStream">重连事件流</NButton>
        </div>
        <div v-if="eventError" class="im-inline-error im-event-error">{{ eventError }}</div>
        <div v-if="!events.length" class="im-empty">暂无事件</div>
        <div v-else class="im-events">
          <div v-for="(ev, index) in events" :key="`${ev.time || index}-${ev.type}`" class="im-event-row">
            <span class="im-event-time">{{ formatEventTime(ev.time) }}</span>
            <NTag size="tiny" :type="ev.error ? 'error' : 'default'" :bordered="false">{{ ev.type || 'event' }}</NTag>
            <span class="im-event-platform">{{ platformKey(ev.platform || '', ev.variant) }}</span>
            <span class="im-event-message">{{ ev.error || ev.message }}</span>
          </div>
        </div>
      </div>
    </template>
  </div>
</template>

<style scoped>
.im-settings {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: 20px;
}
.im-section {
  margin-bottom: 18px;
  padding: 16px 18px;
  background: var(--surface-1);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-md);
}
.im-section:last-child {
  margin-bottom: 0;
}
.im-section-header {
  display: flex;
  align-items: flex-start;
  justify-content: space-between;
  gap: 12px;
  margin-bottom: 12px;
}
.im-title-block {
  min-width: 0;
}
.im-section-title {
  display: flex;
  align-items: center;
  gap: 7px;
  margin: 0;
  color: var(--text-primary);
  font-size: 14px;
  font-weight: 600;
  line-height: 1.3;
}
.im-section-description {
  margin: 6px 0 0;
  color: var(--text-tertiary);
  font-size: 12.5px;
  line-height: 1.5;
}
.im-actions,
.im-platform-toolbar {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  justify-content: flex-end;
}
.im-status-grid,
.im-form-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(190px, 1fr));
  gap: 12px;
}
.im-form-grid--compact {
  margin-bottom: 12px;
}
.im-status-item,
.im-field,
.im-toggle,
.im-json-field {
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.im-status-label,
.im-field > span,
.im-json-field > span,
.im-subtitle {
  color: var(--text-secondary);
  font-size: 12px;
  font-weight: 500;
}
.im-status-value {
  color: var(--text-primary);
  font-size: 13px;
  font-variant-numeric: tabular-nums;
}
.im-toggle {
  flex-direction: row;
  align-items: center;
  min-height: 32px;
}
.im-toggle > span {
  color: var(--text-secondary);
  font-size: 12.5px;
}
.im-collapse {
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-md);
  overflow: hidden;
}
.im-platform-header {
  min-width: 0;
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
}
.im-platform-title {
  color: var(--text-primary);
  font-size: 13px;
  font-weight: 600;
}
.im-platform-body {
  display: flex;
  flex-direction: column;
  gap: 14px;
}
.im-subsection {
  padding-top: 12px;
  border-top: 1px solid var(--border-subtle);
}
.im-subtitle {
  margin-bottom: 8px;
}
.im-test-result {
  display: flex;
  align-items: center;
  gap: 8px;
  color: var(--text-secondary);
  font-size: 12px;
}
.im-json-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(260px, 1fr));
  gap: 12px;
}
.im-events {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.im-event-row {
  display: grid;
  grid-template-columns: 72px 120px minmax(100px, 160px) 1fr;
  align-items: center;
  gap: 8px;
  min-height: 30px;
  padding: 6px 8px;
  background: var(--surface-2);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  font-size: 12px;
}
.im-event-time,
.im-event-platform {
  color: var(--text-tertiary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.im-event-message {
  min-width: 0;
  color: var(--text-secondary);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.im-inline-error {
  min-width: 0;
  color: var(--error-500);
  font-size: 11.5px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.im-event-error {
  margin-bottom: 8px;
  white-space: normal;
}
.im-empty {
  padding: 22px 12px;
  color: var(--text-tertiary);
  text-align: center;
  font-size: 12.5px;
}
@media (max-width: 760px) {
  .im-settings {
    padding: 14px;
  }
  .im-section-header {
    flex-direction: column;
  }
  .im-actions {
    justify-content: flex-start;
  }
  .im-event-row {
    grid-template-columns: 68px 1fr;
  }
  .im-event-platform,
  .im-event-message {
    grid-column: 2;
  }
}
</style>
