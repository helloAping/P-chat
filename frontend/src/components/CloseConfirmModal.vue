<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { NButton, NCheckbox, useMessage } from 'naive-ui'
import AppModal from './AppModal.vue'

type CloseRequestPayload = {
  default_action?: string
}

const show = ref(false)
const minimizeToTray = ref(true)
const message = useMessage()

let cleanupRuntimeEvent: (() => void) | null = null

function normalizePayload(args: unknown[]): CloseRequestPayload {
  const first = args[0]
  if (first && typeof first === 'object') return first as CloseRequestPayload
  return {}
}

async function cancel() {
  show.value = false
  try {
    const app = await import('../../wailsjs/go/main/App')
    await app.CancelWindowClose()
  } catch {
    // Browser preview has no Wails binding.
  }
}

async function confirm() {
  const choice = minimizeToTray.value ? 'tray' : 'exit'
  show.value = false
  try {
    const app = await import('../../wailsjs/go/main/App')
    await app.ConfirmWindowClose(choice)
  } catch (e) {
    console.warn('ConfirmWindowClose failed', e)
    message.error('关闭操作没有完成')
    show.value = true
  }
}

onMounted(async () => {
  try {
    const runtime = await import('../../wailsjs/runtime/runtime')
    const off = runtime.EventsOn('app:close-request', (...args: unknown[]) => {
      const payload = normalizePayload(args)
      minimizeToTray.value = payload.default_action === 'tray'
      show.value = true
    })
    cleanupRuntimeEvent = off
  } catch {
    cleanupRuntimeEvent = null
  }
})

onUnmounted(() => {
  if (cleanupRuntimeEvent) {
    try { cleanupRuntimeEvent() } catch { /* ignore */ }
    cleanupRuntimeEvent = null
  }
})
</script>

<template>
  <AppModal
    v-model:show="show"
    title="关闭 P-Chat？"
    size="sm"
    :mask-closable="false"
    :close-on-esc="false"
    :closable="true"
    @close="cancel"
  >
    <div class="close-confirm">
      <p class="close-confirm__intro">选择这次关闭窗口后的行为。</p>
      <label class="close-confirm__choice">
        <NCheckbox v-model:checked="minimizeToTray" />
        <span>收缩到托盘，继续后台运行</span>
      </label>
      <p class="close-confirm__hint">
        取消勾选后，将直接关闭 P-Chat 并停止后台服务。
      </p>
    </div>

    <template #footer>
      <NButton @click="cancel">取消</NButton>
      <NButton type="primary" @click="confirm">确认</NButton>
    </template>
  </AppModal>
</template>

<style scoped>
.close-confirm {
  display: flex;
  flex-direction: column;
  gap: 14px;
  color: var(--text-primary);
}

.close-confirm__intro,
.close-confirm__hint {
  margin: 0;
  line-height: 1.6;
}

.close-confirm__intro {
  color: var(--text-secondary);
}

.close-confirm__choice {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 12px;
  border: 1px solid var(--border-default);
  border-radius: var(--radius-md);
  background: var(--surface-2);
  cursor: pointer;
}

.close-confirm__choice span {
  min-width: 0;
  line-height: 1.5;
}

.close-confirm__hint {
  color: var(--text-tertiary);
  font-size: 13px;
}
</style>
