<script setup lang="ts">
import { ref, computed } from 'vue'
import { NModal, NButton, NInput, NSpace, useMessage } from 'naive-ui'
import { state, switchSession, getPendingPlanText, clearPendingPlan, appendStreamEvent, recoverMissingParts } from '../stores/chat'
import * as api from '../api/client'

const message = useMessage()
const editing = ref(false)
const editedText = ref('')

const id = computed(() => state.currentID)
const planText = computed(() => getPendingPlanText(id.value))
const show = computed(() => !!planText.value && !editing.value)
const showEdit = computed(() => !!planText.value && editing.value)

function startEdit() {
  editedText.value = planText.value
  editing.value = true
}

async function approve(planOverride?: string) {
  const text = planOverride ?? planText.value
  if (!text) return
  clearPendingPlan(id.value)
  editing.value = false
  try {
    await api.executePlan(id.value, text)
    await api.updateSessionMeta(id.value, { plan_mode: false })
    // Reload messages so the plan text appears in the chat.
    const r = await api.listMessages(id.value, { limit: 200 })
    state.sessionMessages[id.value] = r.messages
    // Submit the continuation message to trigger actual execution.
    await api.streamMessagesRetry(id.value, {
      message: '请按计划执行',
      style: state.sessionMeta[id.value]?.style || 'tech',
      onStreamDrop: ({ lastSeq, reason }) => {
        // P0-1: same recovery flow as the main input
        // path. The plan-execution stream can also drop
        // mid-turn; merge whatever landed via snapshot.
        recoverMissingParts(id.value, lastSeq, reason).catch(() => {})
      },
      onEvent: (ev) => appendStreamEvent(id.value, ev),
    })
  } catch (e: any) {
    message.error('执行计划失败: ' + e.message)
  }
}

function cancel() {
  clearPendingPlan(id.value)
  editing.value = false
}
</script>

<template>
  <NModal v-model:show="show" preset="card" title="计划审核" :closable="false" :mask-closable="false" style="width: 520px">
    <div class="plan-review">
      <div class="plan-text">{{ planText }}</div>
      <div class="plan-actions">
        <NButton @click="cancel">取消</NButton>
        <NButton @click="startEdit">编辑</NButton>
        <NButton type="primary" @click="approve()">批准执行</NButton>
      </div>
    </div>
  </NModal>
  <NModal v-model:show="showEdit" preset="card" title="编辑计划" :closable="false" :mask-closable="false" style="width: 560px">
    <div class="plan-edit">
      <NInput
        v-model:value="editedText"
        type="textarea"
        :autosize="{ minRows: 8, maxRows: 20 }"
        placeholder="编辑计划内容..."
      />
      <div class="plan-actions" style="margin-top: 16px">
        <NButton @click="editing = false">返回</NButton>
        <NButton type="primary" @click="approve(editedText)">保存并执行</NButton>
      </div>
    </div>
  </NModal>
</template>

<style scoped>
.plan-review { padding: 8px 0; }
.plan-text {
  white-space: pre-wrap; word-break: break-word;
  font-size: 13px; line-height: 1.6;
  max-height: 300px; overflow: auto;
  background: var(--bg-3); padding: 12px; border-radius: 6px;
  margin-bottom: 16px;
}
.plan-actions { display: flex; gap: 8px; justify-content: flex-end; }
.plan-edit { padding: 8px 0; }
</style>
