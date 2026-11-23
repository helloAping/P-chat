<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { NButton, NSpace, NTag } from 'naive-ui'
import {
  ShieldAlert, ShieldCheck, FolderOpen, Folder, Globe, Lock,
} from './icons'
import { currentPendingConfirm, submitToolConfirm } from '../stores/chat'
import * as api from '../api/client'
import AppModal from './AppModal.vue'

const argsExpanded = ref(false)
const rememberStyles = ref<api.StyleInfo[]>([])
const rememberStyleId = ref('')
const rememberNewLabel = ref('')
const isRemember = computed(() => currentPendingConfirm.value?.toolName === 'remember')
const rememberReady = computed(() => {
  if (!isRemember.value) return true
  return !!rememberNewLabel.value.trim() || !!rememberStyleId.value
})

watch(isRemember, async (on) => {
  rememberStyleId.value = ''
  rememberNewLabel.value = ''
  if (!on) return
  try {
    const resp = await api.getStyles()
    rememberStyles.value = (resp.styles || []).filter(item => item.id && item.id !== 'off')
    rememberStyleId.value = rememberStyles.value[0]?.id || ''
  } catch {
    rememberStyles.value = []
  }
})

function approveRemember() {
  const label = rememberNewLabel.value.trim()
  if (label) {
    submitToolConfirm('once', { newStyleLabel: label })
    return
  }
  if (rememberStyleId.value) {
    submitToolConfirm('once', { styleId: rememberStyleId.value })
  }
}

const pathClassMeta = computed(() => {
  const cls = currentPendingConfirm.value?.pathClass
  switch (cls) {
    case 'project':
      return { label: '项目内', type: 'success' as const, Icon: Folder }
    case 'allowed':
      return { label: '白名单', type: 'info' as const, Icon: ShieldCheck }
    case 'global':
      return { label: '项目外', type: 'warning' as const, Icon: Globe }
    case 'external':
      return { label: '全局模式', type: 'warning' as const, Icon: FolderOpen }
    case 'protected':
      return { label: '受保护', type: 'error' as const, Icon: Lock }
    case 'browser':
      return { label: '浏览器', type: 'warning' as const, Icon: Globe }
    default:
      return null
  }
})

const riskMeta = computed(() => {
  const r = currentPendingConfirm.value?.riskLevel
  switch (r) {
    case 'high':
      return { label: '高风险', type: 'error' as const, Icon: ShieldAlert }
    case 'medium':
      return { label: '中风险', type: 'warning' as const, Icon: ShieldAlert }
    case 'low':
    default:
      return { label: '低风险', type: 'info' as const, Icon: ShieldCheck }
  }
})

const titleText = computed(() => {
  if (isRemember.value) return '记到风格'
  const tool = currentPendingConfirm.value?.toolName
  return tool ? `沙箱请求：${tool}（${riskMeta.value.label}）` : '沙箱请求'
})

const argsPreview = computed(() => {
  const args = currentPendingConfirm.value?.args || ''
  if (argsExpanded.value || args.length <= 240) return args
  return args.slice(0, 240) + `\n... [共 ${args.length} 字符，点击下方按钮展开]`
})
</script>

<template>
  <AppModal
    :show="!!currentPendingConfirm"
    :title="titleText"
    size="md"
    :closable="false"
    :mask-closable="false"
    :close-on-esc="false"
  >
    <div class="tcm-body">
      <div class="tcm-chips">
        <NTag :type="riskMeta.type" size="small" round>
          <template #icon><component :is="riskMeta.Icon" :size="12" /></template>
          {{ riskMeta.label }}
        </NTag>
        <NTag
          v-if="pathClassMeta"
          :type="pathClassMeta.type"
          size="small"
          round
        >
          <template #icon><component :is="pathClassMeta.Icon" :size="12" /></template>
          {{ pathClassMeta.label }}
        </NTag>
      </div>

      <div v-if="currentPendingConfirm?.resolvedPath" class="tcm-path">
        <span class="tcm-label">{{ currentPendingConfirm?.pathClass === 'browser' ? '目标页面' : '目标路径' }}</span>
        <code class="tcm-path-value">{{ currentPendingConfirm.resolvedPath }}</code>
      </div>

      <div class="tcm-args">
        <span class="tcm-label">参数</span>
        <pre class="tcm-pre">{{ argsPreview }}</pre>
        <button
          v-if="(currentPendingConfirm?.args?.length || 0) > 240"
          type="button"
          class="tcm-expand"
          @click="argsExpanded = !argsExpanded"
        >
          {{ argsExpanded ? '收起' : '查看完整' }}
        </button>
      </div>

      <div v-if="isRemember" class="tcm-path">
        <span class="tcm-label">记入已有风格</span>
        <select v-model="rememberStyleId" class="tcm-style-select" :disabled="!!rememberNewLabel.trim()">
          <option v-for="item in rememberStyles" :key="item.id" :value="item.id">{{ item.label || item.id }}</option>
        </select>
        <span class="tcm-label">或新建风格</span>
        <input v-model="rememberNewLabel" class="tcm-style-select" maxlength="40" placeholder="新风格的名字" />
      </div>

      <div v-if="currentPendingConfirm?.reason" class="tcm-reason">
        <span class="tcm-label">原因</span>
        <span class="tcm-reason-text">{{ currentPendingConfirm.reason }}</span>
      </div>
    </div>

    <template #footer>
      <NSpace justify="end">
        <NButton @click="submitToolConfirm('reject')">拒绝</NButton>
        <NButton v-if="!isRemember" @click="submitToolConfirm('always')">始终允许</NButton>
        <NButton v-if="!isRemember" type="primary" @click="submitToolConfirm('once')">允许一次</NButton>
        <NButton v-else type="primary" :disabled="!rememberReady" @click="approveRemember">记下来</NButton>
      </NSpace>
    </template>
  </AppModal>
</template>

<style scoped>
.tcm-body {
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
.tcm-chips {
  display: flex;
  gap: var(--space-2);
  flex-wrap: wrap;
}
.tcm-label {
  display: block;
  font-size: 11px;
  font-weight: 600;
  color: var(--text-tertiary);
  text-transform: uppercase;
  letter-spacing: 0.5px;
  margin-bottom: 4px;
}
.tcm-path {
  display: flex;
  flex-direction: column;
}
.tcm-style-select {
  width: 100%;
  margin-bottom: var(--space-2);
  padding: var(--space-2) var(--space-3);
  background: var(--surface-2);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  color: var(--text-primary);
  font: inherit;
}
.tcm-path-value {
  display: block;
  padding: var(--space-2) var(--space-3);
  background: var(--surface-2);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  font-family: var(--font-mono);
  font-size: 12px;
  color: var(--text-primary);
  word-break: break-all;
  user-select: text;
}
.tcm-args {
  display: flex;
  flex-direction: column;
}
.tcm-pre {
  margin: 0;
  padding: var(--space-2) var(--space-3);
  background: var(--surface-2);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  font-family: var(--font-mono);
  font-size: 11.5px;
  color: var(--text-secondary);
  max-height: calc(var(--space-8) * 4);
  overflow: auto;
  white-space: pre-wrap;
  word-break: break-all;
}
.tcm-expand {
  margin-top: var(--space-1);
  align-self: flex-end;
  background: none;
  border: none;
  padding: calc(var(--space-1) / 2) var(--space-1);
  font-size: 11.5px;
  color: var(--brand-500);
  cursor: pointer;
}
.tcm-expand:hover {
  text-decoration: underline;
}
.tcm-reason {
  display: flex;
  flex-direction: column;
  padding: var(--space-2) var(--space-3);
  background: var(--warn-50);
  border: 1px dashed var(--warn-500);
  border-radius: var(--radius-sm);
}
.tcm-reason-text {
  font-size: 12px;
  color: var(--text-primary);
  font-family: var(--font-mono);
  word-break: break-all;
}
</style>
