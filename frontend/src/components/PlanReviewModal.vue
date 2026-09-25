<script setup lang="ts">
import { ref, computed } from 'vue'
import { NModal, NButton, NInput, useMessage } from 'naive-ui'
import { state, getPendingPlanText, clearPendingPlan, appendLocalUserMessage } from '../stores/chat'
import * as api from '../api/client'
import { submitConversationTurn } from '../composables/conversationTurn'

const message = useMessage()
const editing = ref(false)
const editedText = ref('')
const approving = ref(false)

const id = computed(() => state.currentID)
const planText = computed(() => getPendingPlanText(id.value))
const show = computed(() => !!planText.value && !editing.value)
const showEdit = computed(() => !!planText.value && editing.value)

function startEdit() {
  editedText.value = planText.value
  editing.value = true
}

async function approve(planOverride?: string) {
  const sessionId = id.value
  const originalPlan = planText.value
  const text = (planOverride ?? originalPlan).trim()
  if (!sessionId || !text || approving.value) return
  approving.value = true
  editing.value = false
  try {
    await api.executePlan(sessionId, text)
    clearPendingPlan(sessionId)
    const meta = state.sessionMeta[sessionId] || {}
    state.sessionMeta[sessionId] = {
      ...meta,
      plan_mode: false,
      turn_mode_policy: 'build',
    }
    const session = state.sessions.find(s => s.id === sessionId)
    if (session) {
      session.plan_mode = false
      session.turn_mode_policy = 'build'
    }

    const executionMessage = text === originalPlan.trim()
      ? '请按已批准计划执行'
      : `请按以下已批准计划执行：\n\n${text}`
    const clientMsgId = Date.now() * 1000 + Math.floor(Math.random() * 1000)
    appendLocalUserMessage(sessionId, {
      id: clientMsgId,
      role: 'user',
      content: executionMessage,
      created_at: Date.now() / 1000,
    })
    await submitConversationTurn({
      sessionId,
      message: executionMessage,
      clientMsgID: clientMsgId,
      provider: meta.provider,
      model: meta.model,
      style: meta.style || 'tech',
      workMode: meta.workMode,
      useImageRecognition: meta.use_image_recognition,
      subAgentModelEnabled: !!meta.sub_agent_model_enabled,
      subAgentProvider: meta.sub_agent_provider || '',
      subAgentModel: meta.sub_agent_model || '',
      turnModePolicy: 'build',
      todoMode: 'resume',
      onServerError: (event) => {
        if (event.suggestion) {
          message.error(`${event.error}\n${event.suggestion}`, { duration: 8000 })
        } else if (event.error) {
          message.error(event.error)
        }
      },
    })
  } catch (e: any) {
    message.error('执行计划失败: ' + e.message)
  } finally {
    approving.value = false
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
        <NButton :disabled="approving" @click="cancel">取消</NButton>
        <NButton :disabled="approving" @click="startEdit">编辑</NButton>
        <NButton type="primary" :loading="approving" @click="approve()">批准执行</NButton>
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
        <NButton :disabled="approving" @click="editing = false">返回</NButton>
        <NButton type="primary" :loading="approving" @click="approve(editedText)">保存并执行</NButton>
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
