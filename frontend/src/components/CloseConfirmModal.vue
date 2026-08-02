<script setup lang="ts">
import { onMounted, onUnmounted, ref } from 'vue'
import { NButton, NCheckbox, useMessage } from 'naive-ui'
import AppModal from './AppModal.vue'

type CloseRequestPayload = {
  default_action?: string
}

const show = ref(false)
const minimizeToTray = ref(true)
const noMoreReminders = ref(false)
const message = useMessage()

let cleanupRuntimeEvent: (() => void) | null = null

function normalizePayload(args: unknown[]): CloseRequestPayload {
  const first = args[0]
  if (first && typeof first === 'object') return first as CloseRequestPayload
  return {}
}

async function cancel() {
  show.value = false
  noMoreReminders.value = false
  try {
    const app = await import('../../wailsjs/go/main/App')
    await app.CancelWindowClose()
  } catch {
    // Browser preview has no Wails binding.
  }
}

async function confirm() {
  // 勾选"不再提醒"时强制托盘：语义是"以后直接驻留后台"，本次也按
  // 托盘处理，避免"驻留后台"与"本次退出"的矛盾。
  const choice = noMoreReminders.value || minimizeToTray.value ? 'tray' : 'exit'

  // 勾选"不再提醒"：仅告知 Go 侧在本次进程内跳过后续关闭确认弹窗。
  // 不持久化——进程退出（包括托盘后重启）即失效，下次启动重新弹出。
  if (noMoreReminders.value) {
    try {
      const app = await import('../../wailsjs/go/main/App')
      await app.SetNoMoreConfirm()
    } catch {
      // Browser preview has no Wails binding — ignore.
    }
  }

  show.value = false
  noMoreReminders.value = false
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
      // 默认选中"收缩到托盘，继续后台运行"：除非后端明确说
      // 关闭行为是 exit（用户显式配置退出），否则默认托盘。
      minimizeToTray.value = payload.default_action !== 'exit'
      noMoreReminders.value = false
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
      <!-- "不再提醒" — kept intentionally compact: a single line
           under the main choice, no card chrome. -->
      <label class="close-confirm__skip">
        <NCheckbox v-model:checked="noMoreReminders" size="small" />
        <span>不再提醒，下次关闭直接驻留后台</span>
      </label>
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

/* "不再提醒" — minimal single-line row, no card chrome so it
   doesn't compete with the main tray choice for attention. */
.close-confirm__skip {
  display: flex;
  align-items: center;
  gap: 8px;
  color: var(--text-tertiary);
  font-size: 12.5px;
  cursor: pointer;
  user-select: none;
  margin-top: -4px;
}
.close-confirm__skip:hover {
  color: var(--text-secondary);
}

.close-confirm__hint {
  color: var(--text-tertiary);
  font-size: 13px;
}
</style>
