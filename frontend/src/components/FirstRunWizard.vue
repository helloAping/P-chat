<script setup lang="ts">
/**
 * Delight-2 first-run setup — a short path from empty providers
 * to a usable default model. Advanced options stay in App Settings.
 */
import { computed, onMounted, ref, watch } from 'vue'
import { NButton, NInput, NSelect, useMessage } from 'naive-ui'
import AppModal from './AppModal.vue'
import * as api from '../api/client'
import { loadProviders, state } from '../stores/chat'

const props = defineProps<{ show: boolean }>()
const emit = defineEmits<{
  (e: 'update:show', v: boolean): void
  (e: 'done'): void
  (e: 'dismiss'): void
}>()

const message = useMessage()
const step = ref<1 | 2 | 3>(1)
const loading = ref(false)
const testing = ref(false)
const presets = ref<api.ProviderPreset[]>([])
const presetID = ref('openai')
const name = ref('default')
const protocol = ref<api.ProviderProtocol>('openai')
const baseURL = ref('')
const apiKey = ref('')
const model = ref('')
const testOK = ref(false)
const testHint = ref('')

const presetOptions = computed(() => {
  const rows = presets.value.map(p => ({
    label: p.display_name,
    value: p.id,
  }))
  if (!rows.some(r => r.value === 'custom')) {
    rows.push({ label: '自定义', value: 'custom' })
  }
  return rows
})

const protocolOptions = computed(() => {
  const preset = presets.value.find(p => p.id === presetID.value)
  if (!preset?.protocols?.length) {
    return [
      { label: 'OpenAI 兼容', value: 'openai' },
      { label: 'Anthropic', value: 'anthropic' },
    ]
  }
  return preset.protocols
    .filter(p => !p.disabled_reason)
    .map(p => ({ label: p.display_name, value: p.id }))
})

const canContinueStep2 = computed(() =>
  !!name.value.trim()
  && !!protocol.value
  && /^https?:\/\//i.test(baseURL.value.trim())
  && !!apiKey.value.trim()
  && !!model.value.trim(),
)

watch(() => props.show, (show) => {
  if (show) {
    step.value = 1
    testOK.value = false
    testHint.value = ''
    void loadPresets()
  }
})

onMounted(() => {
  if (props.show) void loadPresets()
})

async function loadPresets() {
  try {
    const result = await api.fetchProviderPresets()
    presets.value = result.presets || []
    if (presets.value.length && !presets.value.some(p => p.id === presetID.value)) {
      presetID.value = presets.value[0].id
    }
    applyPresetDefaults()
  } catch {
    presets.value = []
  }
}

function applyPresetDefaults() {
  const preset = presets.value.find(p => p.id === presetID.value)
  const firstProto = preset?.protocols?.find(p => !p.disabled_reason) || preset?.protocols?.[0]
  protocol.value = firstProto?.id || 'openai'
  baseURL.value = firstProto?.default_base_url || ''
  if (presetID.value !== 'custom' && !name.value.trim()) {
    name.value = preset?.display_name || presetID.value
  }
  if (presetID.value !== 'custom' && name.value === 'default' && preset?.display_name) {
    name.value = preset.display_name
  }
}

function onPresetChange(id: string) {
  presetID.value = id
  applyPresetDefaults()
}

function onProtocolChange(id: string) {
  protocol.value = id
  const preset = presets.value.find(p => p.id === presetID.value)
  const proto = preset?.protocols?.find(p => p.id === id)
  if (proto?.default_base_url) baseURL.value = proto.default_base_url
}

function close(dismiss = false) {
  emit('update:show', false)
  if (dismiss) emit('dismiss')
}

async function onCreate() {
  if (!canContinueStep2.value) {
    message.warning('请填完整名称、Base URL、API Key 和模型 ID')
    return
  }
  loading.value = true
  testOK.value = false
  testHint.value = ''
  try {
    const providerName = name.value.trim()
    await api.addProvider({
      name: providerName,
      provider_id: presetID.value === 'custom' ? 'custom' : presetID.value,
      protocol: protocol.value,
      base_url: baseURL.value.trim(),
      api_key: apiKey.value.trim(),
      model: model.value.trim(),
    })
    await api.setDefaultProvider(providerName)
    await loadProviders()
    step.value = 3
    message.success('供应商已添加')
  } catch (e: any) {
    message.error(`添加失败：${e?.message || e}`)
  } finally {
    loading.value = false
  }
}

async function onTest() {
  const providerName = name.value.trim()
  if (!providerName) return
  testing.value = true
  testOK.value = false
  testHint.value = ''
  try {
    const result = await api.testProvider(providerName, model.value.trim() || undefined)
    testOK.value = !!result.ok
    testHint.value = result.ok
      ? `连通正常（${result.elapsed_ms} ms）`
      : (result.response || '连接失败')
    if (result.ok) message.success('连接测试通过')
    else message.warning(testHint.value)
  } catch (e: any) {
    testOK.value = false
    testHint.value = e?.message || String(e)
    message.error(`测试失败：${testHint.value}`)
  } finally {
    testing.value = false
  }
}

function onFinish() {
  emit('done')
  close(false)
}
</script>

<template>
  <AppModal
    :show="show"
    title="欢迎使用 P-Chat"
    size="md"
    :mask-closable="false"
    accent-top
    accent-variant="primary"
    @update:show="(v) => !v && close(true)"
  >
    <div class="wizard">
      <p class="wizard-lead">
        还没有可用模型。三步配好默认供应商，就可以开始对话。
      </p>

      <div class="wizard-steps" aria-hidden="true">
        <span :class="{ active: step >= 1 }">1 选模板</span>
        <span :class="{ active: step >= 2 }">2 填密钥</span>
        <span :class="{ active: step >= 3 }">3 验证</span>
      </div>

      <div v-if="step === 1" class="wizard-pane">
        <label class="field">
          <span>供应商模板</span>
          <NSelect
            :value="presetID"
            :options="presetOptions"
            @update:value="onPresetChange"
          />
        </label>
        <p class="hint">选一个常用模板会自动填好协议和 Base URL；不确定就选「自定义」。</p>
      </div>

      <div v-else-if="step === 2" class="wizard-pane">
        <label class="field">
          <span>显示名称</span>
          <NInput v-model:value="name" placeholder="例如 openai / deepseek" />
        </label>
        <label class="field">
          <span>协议</span>
          <NSelect
            :value="protocol"
            :options="protocolOptions"
            @update:value="onProtocolChange"
          />
        </label>
        <label class="field">
          <span>Base URL</span>
          <NInput v-model:value="baseURL" placeholder="https://..." />
        </label>
        <label class="field">
          <span>API Key</span>
          <NInput
            v-model:value="apiKey"
            type="password"
            show-password-on="click"
            placeholder="不会回显到设置列表"
          />
        </label>
        <label class="field">
          <span>默认模型 ID</span>
          <NInput v-model:value="model" placeholder="例如 gpt-4o-mini" />
        </label>
      </div>

      <div v-else class="wizard-pane">
        <p class="done-title">已添加「{{ name }}」</p>
        <p class="hint">建议先测一下连通性；也可以稍后在应用设置里再测。</p>
        <div class="test-row">
          <NButton size="small" :loading="testing" @click="onTest">测试连接</NButton>
          <span v-if="testHint" class="test-hint" :class="{ ok: testOK }">{{ testHint }}</span>
        </div>
        <p v-if="state.defaultModel" class="hint">
          当前默认：{{ state.defaultModel.provider }} / {{ state.defaultModel.model }}
        </p>
      </div>
    </div>

    <template #footer>
      <NButton quaternary @click="close(true)">稍后再说</NButton>
      <NButton v-if="step === 1" type="primary" @click="step = 2">下一步</NButton>
      <template v-else-if="step === 2">
        <NButton quaternary @click="step = 1">上一步</NButton>
        <NButton type="primary" :loading="loading" :disabled="!canContinueStep2" @click="onCreate">
          创建并继续
        </NButton>
      </template>
      <NButton v-else type="primary" @click="onFinish">开始使用</NButton>
    </template>
  </AppModal>
</template>

<style scoped>
.wizard {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
.wizard-lead {
  margin: 0;
  font-size: 13px;
  color: var(--text-3);
  line-height: 1.5;
}
.wizard-steps {
  display: flex;
  gap: var(--space-2);
  flex-wrap: wrap;
  font-size: 12px;
  color: var(--text-4);
}
.wizard-steps .active {
  color: var(--text-2);
  font-weight: 600;
}
.wizard-pane {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
.field {
  display: flex;
  flex-direction: column;
  gap: 6px;
  font-size: 12px;
  color: var(--text-3);
}
.hint {
  margin: 0;
  font-size: 12px;
  color: var(--text-4);
  line-height: 1.45;
}
.done-title {
  margin: 0;
  font-size: 14px;
  color: var(--text-2);
  font-weight: 600;
}
.test-row {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  flex-wrap: wrap;
}
.test-hint {
  font-size: 12px;
  color: var(--text-4);
}
.test-hint.ok {
  color: var(--success-500, var(--text-2));
}
</style>
