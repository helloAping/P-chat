<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import {
  NButton, NCollapse, NCollapseItem, NInput, NModal, NSwitch, NTag, useMessage,
} from 'naive-ui'
import { Bot, Key, MessageSquare, RotateCw } from './icons'
import * as api from '../api/client'

const message = useMessage()

const loading = ref(false)
const saving = ref(false)
const healthLoading = ref(false)
const imConfig = ref<api.IMConfig | null>(null)
const health = ref<api.IMHealth | null>(null)
const events = ref<api.IMLifecycleEvent[]>([])
const eventError = ref('')
const testing = ref<Record<string, boolean>>({})
const feishuAppID = ref('')
const feishuAppSecret = ref('')
const feishuVerificationToken = ref('')
const feishuEncryptKey = ref('')
const qqBotSecret = ref('')
const showWechatQR = ref(false)
const wechatQRLoading = ref(false)
const wechatQRSession = ref<api.WeChatQRSession | null>(null)
const wechatQRError = ref('')
const platformsText = ref('[]')
const policiesText = ref('{}')
let eventAbort: AbortController | null = null
let wechatQRTimer: ReturnType<typeof setTimeout> | null = null

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

const feishu = computed(() => findPlatform('feishu'))
const wechat = computed(() => findPlatform('wechat'))
const qq = computed(() => findPlatform('qq'))
const wechatQRSource = computed(() => {
  const active = wechatQRSession.value?.qr_url || wechatQRSession.value?.qr_data || ''
  if (typeof active === 'string' && active) return active
  const extra = wechat.value?.extra || {}
  const raw = extra.qr_url || extra.qr_data || extra.qrcode || ''
  return typeof raw === 'string' ? raw : ''
})
const wechatQRStatus = computed(() => wechatQRSession.value?.status || (wechatQRSource.value ? 'waiting' : 'idle'))
const wechatQRStatusText = computed(() => {
  switch (wechatQRStatus.value) {
    case 'waiting': return '请使用微信扫码'
    case 'scanned': return '已扫码，等待手机确认'
    case 'confirmed': return '微信已连接'
    case 'expired': return '二维码已过期'
    case 'canceled': return '扫码已取消'
    default: return wechatQRSource.value ? '请使用微信扫码' : '准备二维码'
  }
})
const wechatQRHint = computed(() => {
  if (wechatQRError.value) return wechatQRError.value
  switch (wechatQRStatus.value) {
    case 'waiting': return '打开微信扫码登录，扫码后请在手机上确认。'
    case 'scanned': return '请在手机微信中确认登录，确认后这里会自动完成连接。'
    case 'confirmed': return '微信 Bot 登录态已保存，可继续启用消息收发能力。'
    case 'expired': return '二维码已过期，请点击重新获取。'
    case 'canceled': return '扫码已取消，请重新获取二维码。'
    default: return '点击获取二维码后，使用微信扫码即可连接。'
  }
})

function platformKey(type: string, variant?: string) {
  return variant ? `${type}:${variant}` : type
}

function healthFor(type: string, variant?: string) {
  return healthByKey.value[platformKey(type, variant)]
}

function statusLabel(status?: string) {
  switch (status) {
    case 'ok': return '已连接'
    case 'authenticated': return '已登录'
    case 'registered': return '可用'
    case 'configured': return '已配置'
    case 'disabled': return '未启用'
    case 'unavailable': return '未接入'
    case 'error': return '异常'
    case 'not_configured': return '未配置'
    case 'not_implemented': return '未接入'
    default: return status || '未连接'
  }
}

function statusType(status?: string): 'success' | 'warning' | 'error' | 'default' {
  if (status === 'ok' || status === 'registered' || status === 'authenticated') return 'success'
  if (status === 'configured' || status === 'disabled' || status === 'not_configured') return 'warning'
  if (status === 'error' || status === 'unavailable' || status === 'not_implemented') return 'error'
  return 'default'
}

function platformEnabled(type: string) {
  return !!findPlatform(type)?.enabled
}

function findPlatform(type: string) {
  return (imConfig.value?.platforms || []).find(p => p.type === type)
}

function ensurePlatform(p: api.IMPlatformConfig): api.IMPlatformConfig {
  if (!p.webhook) p.webhook = {}
  if (!p.out) p.out = {}
  if (!p.allowed_senders) p.allowed_senders = ['*']
  if (!p.extra) p.extra = {}
  return p
}

function defaultsFor(type: 'feishu' | 'wechat' | 'qq'): api.IMPlatformConfig {
  if (type === 'feishu') {
    return ensurePlatform({
      type: 'feishu',
      variant: 'bot',
      enabled: false,
      mode: 'webhook',
      allowed_senders: ['*'],
      out: { use_openapi: true, api_base: 'https://open.feishu.cn' },
    })
  }
  if (type === 'wechat') {
    return ensurePlatform({
      type: 'wechat',
      variant: 'wechatbot',
      enabled: false,
      mode: 'websocket',
      allowed_senders: ['*'],
    })
  }
  return ensurePlatform({
    type: 'qq',
    variant: 'guild',
    enabled: false,
    mode: 'websocket',
    allowed_senders: ['*'],
  })
}

function upsertPlatform(type: 'feishu' | 'wechat' | 'qq') {
  if (!imConfig.value) return null
  const current = findPlatform(type)
  if (current) return ensurePlatform(current)
  const next = defaultsFor(type)
  imConfig.value.platforms = [...(imConfig.value.platforms || []), next]
  return next
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

function syncEditors(cfg: api.IMConfig) {
  platformsText.value = toPrettyJSON(cfg.platforms || [])
  policiesText.value = toPrettyJSON({
    session: cfg.session,
    identity: cfg.identity,
    command: cfg.command,
    rate_limit: cfg.rate_limit || [],
    audit_log: cfg.audit_log,
    audit_local_only: cfg.audit_local_only,
    tools_allowlist_default: cfg.tools_allowlist_default || [],
    personas: cfg.personas || {},
    cron: cfg.cron,
    fallback: cfg.fallback || [],
    media: cfg.media,
  })
  const qqPlatform = (cfg.platforms || []).find(p => p.type === 'qq')
  const feishuPlatform = (cfg.platforms || []).find(p => p.type === 'feishu')
  feishuAppID.value = feishuPlatform?.app_id || ''
  feishuAppSecret.value = feishuPlatform?.app_secret || ''
  feishuVerificationToken.value = feishuPlatform?.verification_token || ''
  feishuEncryptKey.value = feishuPlatform?.encrypt_key || ''
  qqBotSecret.value = qqPlatform?.app_secret || qqPlatform?.token || qqPlatform?.api_key || ''
}

function syncPlatformEditor() {
  if (!imConfig.value) return
  platformsText.value = toPrettyJSON(imConfig.value.platforms || [])
}

async function refreshConfig() {
  loading.value = true
  try {
    const cfg = normalizeConfig(await api.getIMConfig())
    imConfig.value = cfg
    syncEditors(cfg)
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

function applyQQSecret(cfg: api.IMConfig) {
  const secret = qqBotSecret.value.trim()
  const existing = (cfg.platforms || []).find(p => p.type === 'qq')
  if (!secret && !existing) return
  const qqPlatform = existing || defaultsFor('qq')
  qqPlatform.app_secret = secret
  qqPlatform.enabled = !!secret || qqPlatform.enabled
  if (!existing) cfg.platforms = [...(cfg.platforms || []), qqPlatform]
}

function applyFeishuFields(cfg: api.IMConfig) {
  const existing = (cfg.platforms || []).find(p => p.type === 'feishu')
  const hasAnyValue = !!(
    feishuAppID.value.trim() ||
    feishuAppSecret.value.trim() ||
    feishuVerificationToken.value.trim() ||
    feishuEncryptKey.value.trim()
  )
  if (!hasAnyValue && !existing) return
  const feishuPlatform = existing || defaultsFor('feishu')
  feishuPlatform.app_id = feishuAppID.value.trim()
  feishuPlatform.app_secret = feishuAppSecret.value
  feishuPlatform.verification_token = feishuVerificationToken.value
  feishuPlatform.encrypt_key = feishuEncryptKey.value
  feishuPlatform.enabled = hasAnyValue || feishuPlatform.enabled
  if (!existing) cfg.platforms = [...(cfg.platforms || []), feishuPlatform]
}

async function saveConfig(options: { silent?: boolean } = {}) {
  if (!imConfig.value) return false
  saving.value = true
  try {
    const next = normalizeConfig({ ...imConfig.value })
    next.platforms = parseJSON<api.IMPlatformConfig[]>(platformsText.value, next.platforms || [], '平台连接').map(ensurePlatform)
    const policies = parseJSON<Record<string, any>>(policiesText.value, {}, '高级策略')
    next.session = policies.session || next.session
    next.identity = policies.identity || next.identity
    next.command = policies.command || next.command
    next.rate_limit = policies.rate_limit || []
    next.audit_log = typeof policies.audit_log === 'boolean' ? policies.audit_log : next.audit_log
    next.audit_local_only = typeof policies.audit_local_only === 'boolean' ? policies.audit_local_only : next.audit_local_only
    next.tools_allowlist_default = policies.tools_allowlist_default || []
    next.personas = policies.personas || {}
    next.cron = policies.cron || next.cron
    next.fallback = policies.fallback || []
    next.media = policies.media || next.media
    applyFeishuFields(next)
    applyQQSecret(next)

    const updated = normalizeConfig(await api.updateIMConfig(next))
    imConfig.value = updated
    syncEditors(updated)
    await refreshHealth()
    if (!options.silent) message.success('IM 配置已保存')
    return true
  } catch (err: any) {
    message.error(err?.message || String(err))
    return false
  } finally {
    saving.value = false
  }
}

function setPlatformEnabled(type: 'feishu' | 'wechat' | 'qq', enabled: boolean) {
  const platform = upsertPlatform(type)
  if (!platform) return
  platform.enabled = enabled
  syncPlatformEditor()
}

async function connectPlatform(type: 'feishu' | 'wechat' | 'qq') {
  const platform = upsertPlatform(type)
  if (!platform) return
  if (type === 'qq') {
    const secret = qqBotSecret.value.trim()
    if (!secret) {
      message.warning('请先填写 QQ Bot 密钥')
      return
    }
    platform.app_secret = secret
  }
  if (type === 'feishu') {
    platform.app_id = feishuAppID.value.trim()
    platform.app_secret = feishuAppSecret.value
    platform.verification_token = feishuVerificationToken.value
    platform.encrypt_key = feishuEncryptKey.value
  }
  platform.enabled = true
  syncPlatformEditor()
  const saved = await saveConfig({ silent: true })
  if (!saved) return
  if (type === 'wechat') {
    await startWechatQR()
    return
  }
  await testPlatform(platform)
}

async function startWechatQR() {
  stopWechatQRPoll()
  wechatQRLoading.value = true
  wechatQRError.value = ''
  showWechatQR.value = true
  try {
    const session = await api.startWeChatQR()
    wechatQRSession.value = session
    scheduleWechatQRPoll(session)
  } catch (err: any) {
    wechatQRSession.value = null
    wechatQRError.value = friendlyAPIError(err)
    message.error(`获取微信二维码失败: ${wechatQRError.value}`)
  } finally {
    wechatQRLoading.value = false
  }
}

async function pollWechatQR() {
  const id = wechatQRSession.value?.id
  if (!id) return
  try {
    const session = await api.pollWeChatQR(id)
    wechatQRSession.value = session
    if (session.status === 'confirmed') {
      message.success('微信已连接')
      await refreshAll()
      stopWechatQRPoll()
      return
    }
    if (session.status === 'expired' || session.status === 'canceled') {
      stopWechatQRPoll()
      return
    }
    scheduleWechatQRPoll(session)
  } catch (err: any) {
    wechatQRError.value = friendlyAPIError(err)
    stopWechatQRPoll()
  }
}

function scheduleWechatQRPoll(session: api.WeChatQRSession) {
  stopWechatQRPoll()
  if (!session.id || session.status === 'confirmed' || session.status === 'expired' || session.status === 'canceled') return
  const delay = Math.max(1000, Math.min(session.poll_after_ms || 2000, 5000))
  wechatQRTimer = setTimeout(() => {
    void pollWechatQR()
  }, delay)
}

function stopWechatQRPoll() {
  if (wechatQRTimer) {
    clearTimeout(wechatQRTimer)
    wechatQRTimer = null
  }
}

function friendlyAPIError(err: any) {
  const raw = err?.message || String(err)
  const match = raw.match(/HTTP\s+\d+:\s+(\{.*\})$/)
  if (match) {
    try {
      const body = JSON.parse(match[1])
      if (typeof body?.error === 'string' && body.error) return body.error
    } catch {
      // Keep the original message when the server did not return JSON.
    }
  }
  if (raw.includes('wechat_qr_unavailable') || raw.includes('no such host') || raw.includes('dial tcp')) {
    return '微信扫码服务暂时无法访问，请检查网络后重试'
  }
  return raw
}

async function testPlatform(p: api.IMPlatformConfig) {
  if (!p.type) return
  const key = platformKey(p.type, p.variant)
  testing.value = { ...testing.value, [key]: true }
  try {
    const result = await api.testIMConnection(p.type, p.variant)
    if (result.ok) message.success(`${platformName(p.type)} 已就绪`)
    else message.warning(`${platformName(p.type)} 未就绪: ${friendlyStatus(p.type, result.error || result.status)}`)
  } catch (err: any) {
    message.error(`连接检查失败: ${err?.message || err}`)
  } finally {
    testing.value = { ...testing.value, [key]: false }
  }
}

function friendlyStatus(type: string, raw?: string) {
  if (!raw) return '请检查连接状态'
  if (raw.includes('adapter not registered')) {
    if (type === 'wechat') return '请先启动微信连接服务，然后重新扫码'
    if (type === 'feishu') return '飞书连接服务未启动，请确认授权信息已填写'
    if (type === 'qq') return 'QQ Bot 连接服务未启动，请确认密钥已填写'
  }
  return raw
}

function platformName(type: string) {
  if (type === 'feishu') return '飞书'
  if (type === 'wechat') return '微信'
  if (type === 'qq') return 'QQ Bot'
  return type
}

function startEventStream() {
  if (eventAbort) eventAbort.abort()
  const controller = new AbortController()
  eventAbort = controller
  eventError.value = ''
  api.streamIMEvents((ev) => {
    events.value = [ev, ...events.value].slice(0, 12)
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

watch(showWechatQR, (show) => {
  if (!show) stopWechatQRPoll()
})

onBeforeUnmount(() => {
  if (eventAbort) eventAbort.abort()
  stopWechatQRPoll()
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
        </div>
        <div class="im-actions">
          <NButton size="small" quaternary :loading="healthLoading || loading" @click="refreshAll">
            <template #icon><RotateCw :size="14" /></template>
            刷新
          </NButton>
          <NButton size="small" type="primary" :loading="saving" :disabled="!imConfig" @click="saveConfig()">
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
          <span class="im-status-label">已启用</span>
          <span class="im-status-value">{{ enabledPlatforms }} / {{ imConfig.platforms?.length || 0 }}</span>
        </div>
      </div>
      <div v-else class="im-empty">
        {{ loading ? '正在加载 IM 配置...' : '暂无 IM 配置' }}
      </div>
    </div>

    <template v-if="imConfig">
      <div class="im-connect-grid">
        <section class="im-connect-card">
          <div class="im-connect-head">
            <div class="im-connect-icon"><Bot :size="18" /></div>
            <div class="im-connect-title">
              <strong>飞书</strong>
              <span>密钥授权连接</span>
            </div>
            <NTag
              size="small"
              :type="statusType(healthFor('feishu', feishu?.variant)?.status)"
              :bordered="false"
            >
              {{ statusLabel(healthFor('feishu', feishu?.variant)?.status) }}
            </NTag>
          </div>
          <div class="im-connect-actions">
            <NSwitch :value="platformEnabled('feishu')" size="small" @update:value="(v: boolean) => setPlatformEnabled('feishu', v)" />
            <NButton
              size="small"
              type="primary"
              :loading="!!testing[platformKey('feishu', feishu?.variant)]"
              @click="connectPlatform('feishu')"
            >
              保存并连接
            </NButton>
          </div>
          <div class="im-field-grid">
            <label class="im-field">
              <span>App ID</span>
              <NInput v-model:value="feishuAppID" size="small" placeholder="cli_xxx" />
            </label>
            <label class="im-field">
              <span>App Secret</span>
              <NInput v-model:value="feishuAppSecret" size="small" type="password" show-password-on="click" />
            </label>
            <label class="im-field">
              <span>Verification Token</span>
              <NInput v-model:value="feishuVerificationToken" size="small" type="password" show-password-on="click" />
            </label>
            <label class="im-field">
              <span>Encrypt Key</span>
              <NInput v-model:value="feishuEncryptKey" size="small" type="password" show-password-on="click" />
            </label>
          </div>
          <div v-if="healthFor('feishu', feishu?.variant)?.error" class="im-card-error">
            {{ friendlyStatus('feishu', healthFor('feishu', feishu?.variant)?.error) }}
          </div>
        </section>

        <section class="im-connect-card">
          <div class="im-connect-head">
            <div class="im-connect-icon"><MessageSquare :size="18" /></div>
            <div class="im-connect-title">
              <strong>微信</strong>
              <span>扫码登录</span>
            </div>
            <NTag
              size="small"
              :type="statusType(healthFor('wechat', wechat?.variant)?.status)"
              :bordered="false"
            >
              {{ statusLabel(healthFor('wechat', wechat?.variant)?.status) }}
            </NTag>
          </div>
          <div class="im-connect-actions">
            <NSwitch :value="platformEnabled('wechat')" size="small" @update:value="(v: boolean) => setPlatformEnabled('wechat', v)" />
            <NButton
              size="small"
              type="primary"
              ghost
              :loading="wechatQRLoading"
              @click="connectPlatform('wechat')"
            >
              扫码
            </NButton>
          </div>
          <div v-if="healthFor('wechat', wechat?.variant)?.error" class="im-card-error">
            {{ friendlyStatus('wechat', healthFor('wechat', wechat?.variant)?.error) }}
          </div>
        </section>

        <section class="im-connect-card im-connect-card--wide">
          <div class="im-connect-head">
            <div class="im-connect-icon"><Key :size="18" /></div>
            <div class="im-connect-title">
              <strong>QQ Bot</strong>
              <span>密钥连接</span>
            </div>
            <NTag
              size="small"
              :type="statusType(healthFor('qq', qq?.variant)?.status)"
              :bordered="false"
            >
              {{ statusLabel(healthFor('qq', qq?.variant)?.status) }}
            </NTag>
          </div>
          <div class="im-secret-row">
            <NInput
              v-model:value="qqBotSecret"
              size="small"
              type="password"
              show-password-on="click"
              placeholder="QQ Bot 密钥"
            />
            <NButton
              size="small"
              type="primary"
              :loading="!!testing[platformKey('qq', qq?.variant)]"
              @click="connectPlatform('qq')"
            >
              保存并连接
            </NButton>
          </div>
          <div v-if="healthFor('qq', qq?.variant)?.error" class="im-card-error">
            {{ friendlyStatus('qq', healthFor('qq', qq?.variant)?.error) }}
          </div>
        </section>
      </div>

      <div class="im-section">
        <div class="im-section-header">
          <h3 class="im-section-title">高级配置</h3>
        </div>
        <NCollapse arrow-placement="right">
          <NCollapseItem title="平台连接 JSON" name="platforms">
            <label class="im-json-field">
              <span>platforms</span>
              <NInput v-model:value="platformsText" type="textarea" size="small" :autosize="{ minRows: 7, maxRows: 18 }" />
            </label>
          </NCollapseItem>
          <NCollapseItem title="策略 JSON" name="policies">
            <label class="im-json-field">
              <span>policies</span>
              <NInput v-model:value="policiesText" type="textarea" size="small" :autosize="{ minRows: 8, maxRows: 20 }" />
            </label>
          </NCollapseItem>
        </NCollapse>
      </div>

      <div class="im-section">
        <div class="im-section-header">
          <h3 class="im-section-title">Gateway 事件</h3>
          <NButton size="small" ghost @click="startEventStream">重连</NButton>
        </div>
        <div v-if="eventError" class="im-card-error im-event-error">{{ eventError }}</div>
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

    <NModal
      v-model:show="showWechatQR"
      preset="card"
      title="微信扫码登录"
      style="width: min(420px, calc(100vw - 32px));"
      :bordered="false"
    >
      <div class="wechat-qr-panel">
        <div class="wechat-qr-box">
          <img v-if="wechatQRSource" :src="wechatQRSource" alt="微信登录二维码" />
          <div v-else class="wechat-qr-placeholder">
            <MessageSquare :size="40" />
            <span>{{ wechatQRLoading ? '正在获取二维码' : '等待微信二维码' }}</span>
          </div>
        </div>
        <div class="wechat-qr-status">
          <NTag
            size="small"
            :type="wechatQRStatus === 'confirmed' ? 'success' : (wechatQRError || wechatQRStatus === 'expired' ? 'error' : 'warning')"
            :bordered="false"
          >
            {{ wechatQRStatusText }}
          </NTag>
          <p>{{ wechatQRHint }}</p>
        </div>
      </div>
      <template #footer>
        <div class="im-modal-actions">
          <NButton size="small" @click="showWechatQR = false">关闭</NButton>
          <NButton size="small" type="primary" ghost :loading="wechatQRLoading" @click="startWechatQR">
            {{ wechatQRStatus === 'expired' || wechatQRError ? '重新获取' : '刷新二维码' }}
          </NButton>
        </div>
      </template>
    </NModal>
  </div>
</template>

<style scoped>
.im-settings {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
  padding: 20px;
}
.im-section,
.im-connect-card {
  background: var(--surface-1);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-md);
}
.im-section {
  margin-bottom: 18px;
  padding: 16px 18px;
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
.im-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-wrap: wrap;
  justify-content: flex-end;
}
.im-status-grid {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));
  gap: 12px;
}
.im-status-item {
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.im-status-label,
.im-json-field > span {
  color: var(--text-secondary);
  font-size: 12px;
  font-weight: 500;
}
.im-status-value {
  color: var(--text-primary);
  font-size: 13px;
  font-variant-numeric: tabular-nums;
}
.im-connect-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 12px;
  margin-bottom: 18px;
}
.im-connect-card {
  min-width: 0;
  padding: 14px;
  display: flex;
  flex-direction: column;
  gap: 12px;
}
.im-connect-card--wide {
  grid-column: 1 / -1;
}
.im-connect-head {
  min-width: 0;
  display: flex;
  align-items: center;
  gap: 10px;
}
.im-connect-icon {
  width: 32px;
  height: 32px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  color: var(--brand-600);
  background: var(--brand-50);
  border-radius: var(--radius-md);
  flex-shrink: 0;
}
.im-connect-title {
  min-width: 0;
  flex: 1;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.im-connect-title strong {
  color: var(--text-primary);
  font-size: 14px;
  line-height: 1.25;
}
.im-connect-title span {
  color: var(--text-tertiary);
  font-size: 12px;
}
.im-connect-actions,
.im-secret-row {
  display: flex;
  align-items: center;
  gap: 10px;
}
.im-field-grid {
  display: grid;
  grid-template-columns: repeat(2, minmax(0, 1fr));
  gap: 10px;
}
.im-field {
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 5px;
}
.im-field > span {
  color: var(--text-secondary);
  font-size: 12px;
  font-weight: 500;
}
.im-secret-row {
  align-items: stretch;
}
.im-secret-row :deep(.n-input) {
  flex: 1;
}
.im-card-error {
  color: var(--error-500);
  font-size: 11.5px;
  line-height: 1.45;
  word-break: break-word;
}
.im-json-field {
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 6px;
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
.im-event-error {
  margin-bottom: 8px;
}
.im-empty {
  padding: 22px 12px;
  color: var(--text-tertiary);
  text-align: center;
  font-size: 12.5px;
}
.wechat-qr-panel {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 14px;
}
.wechat-qr-box {
  width: 220px;
  height: 220px;
  display: flex;
  align-items: center;
  justify-content: center;
  background: var(--surface-2);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-md);
  overflow: hidden;
}
.wechat-qr-box img {
  width: 100%;
  height: 100%;
  object-fit: contain;
  background: #ffffff;
}
.wechat-qr-placeholder {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 10px;
  color: var(--text-tertiary);
  font-size: 13px;
}
.wechat-qr-status {
  display: flex;
  flex-direction: column;
  align-items: center;
  gap: 8px;
  text-align: center;
}
.wechat-qr-status p {
  margin: 0;
  max-width: 320px;
  color: var(--text-secondary);
  font-size: 12.5px;
  line-height: 1.55;
}
.im-modal-actions {
  display: flex;
  justify-content: flex-end;
  gap: 8px;
}
@media (max-width: 760px) {
  .im-settings {
    padding: 14px;
  }
  .im-section-header,
  .im-secret-row {
    flex-direction: column;
  }
  .im-actions {
    justify-content: flex-start;
  }
  .im-connect-grid {
    grid-template-columns: 1fr;
  }
  .im-field-grid {
    grid-template-columns: 1fr;
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
