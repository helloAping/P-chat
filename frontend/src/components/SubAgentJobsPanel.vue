<script setup lang="ts">
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { useMessage } from 'naive-ui'
import { Activity, AlertCircle, Bot, CheckCircle2, Loader2, RotateCw, X, XCircle } from './icons'
import {
  cancelSubAgentJob,
  listSubAgentJobs,
  type SubAgentJob,
} from '../api/client'

const props = defineProps<{
  sessionId?: string
}>()

const message = useMessage()
const jobs = ref<SubAgentJob[]>([])
const loading = ref(false)
const error = ref('')
const cancelling = ref<Record<string, boolean>>({})
let pollTimer: number | null = null
let requestSeq = 0

const visibleJobs = computed(() => jobs.value.slice(0, 5))
const activeJobs = computed(() => jobs.value.filter(isActiveJob))
const hasJobs = computed(() => jobs.value.length > 0)

function isActiveJob(job: SubAgentJob): boolean {
  return job.status === 'queued' || job.status === 'running'
}

function jobLabel(job: SubAgentJob): string {
  return job.description || job.subagent_type || job.task_id
}

function statusLabel(status: string): string {
  switch (status) {
    case 'queued':
      return '排队中'
    case 'running':
      return '运行中'
    case 'succeeded':
      return '已完成'
    case 'failed':
      return '失败'
    case 'cancelled':
      return '已取消'
    default:
      return status || '未知'
  }
}

function statusClass(status: string): string {
  if (status === 'queued' || status === 'running') return 'is-active'
  if (status === 'succeeded') return 'is-ok'
  if (status === 'failed') return 'is-error'
  if (status === 'cancelled') return 'is-cancelled'
  return ''
}

function statusIcon(status: string) {
  if (status === 'queued' || status === 'running') return Loader2
  if (status === 'succeeded') return CheckCircle2
  if (status === 'failed') return XCircle
  if (status === 'cancelled') return X
  return AlertCircle
}

function latestProgress(job: SubAgentJob): string {
  if (!job.progress_json) return ''
  try {
    const progress = JSON.parse(job.progress_json) as Record<string, string>
    return progress.error || progress.tool || progress.phase || progress.status || ''
  } catch {
    return ''
  }
}

function formatTime(value?: string): string {
  if (!value || value.startsWith('0001-')) return ''
  const t = new Date(value)
  if (Number.isNaN(t.getTime())) return ''
  return t.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })
}

async function refresh(silent = false) {
  const seq = ++requestSeq
  const sessionId = props.sessionId
  if (!sessionId) {
    jobs.value = []
    error.value = ''
    loading.value = false
    return
  }
  if (!silent) loading.value = true
  error.value = ''
  try {
    const res = await listSubAgentJobs(sessionId, 20)
    if (seq !== requestSeq) return
    jobs.value = res.jobs || []
  } catch (e: any) {
    if (seq !== requestSeq) return
    error.value = e?.message || String(e)
  } finally {
    if (seq === requestSeq) loading.value = false
  }
}

async function cancelJob(job: SubAgentJob) {
  const sessionId = props.sessionId
  if (!sessionId || !isActiveJob(job)) return
  cancelling.value = { ...cancelling.value, [job.task_id]: true }
  try {
    const res = await cancelSubAgentJob(sessionId, job.task_id)
    jobs.value = jobs.value.map((j) => j.task_id === job.task_id ? res.job : j)
    message.info('已取消后台子代理任务')
  } catch (e: any) {
    message.error(`取消失败：${e?.message || e}`)
  } finally {
    const next = { ...cancelling.value }
    delete next[job.task_id]
    cancelling.value = next
  }
}

function clearPoll() {
  if (pollTimer !== null) {
    window.clearInterval(pollTimer)
    pollTimer = null
  }
}

function ensurePoll() {
  clearPoll()
  if (!props.sessionId) return
  pollTimer = window.setInterval(() => {
    refresh(true)
  }, activeJobs.value.length > 0 ? 2500 : 8000)
}

watch(
  () => props.sessionId,
  () => {
    clearPoll()
    refresh(true).finally(ensurePoll)
  },
  { immediate: true },
)

watch(
  () => activeJobs.value.length,
  () => ensurePoll(),
)

onBeforeUnmount(() => {
  clearPoll()
  requestSeq++
})
</script>

<template>
  <Transition name="subagent-jobs">
    <section v-if="hasJobs || error" class="subagent-jobs" aria-label="后台子代理任务">
      <div class="jobs-header">
        <div class="jobs-title">
          <Bot :size="15" />
          <span>后台子代理</span>
          <span v-if="activeJobs.length" class="active-count">{{ activeJobs.length }}</span>
        </div>
        <button
          class="icon-btn"
          type="button"
          aria-label="刷新后台子代理任务"
          title="刷新"
          :disabled="loading"
          @click="refresh(false)"
        >
          <RotateCw :size="15" :class="{ spinning: loading }" />
        </button>
      </div>
      <div v-if="error" class="jobs-error">
        <AlertCircle :size="14" />
        <span>{{ error }}</span>
      </div>
      <div v-else class="jobs-list">
        <div
          v-for="job in visibleJobs"
          :key="job.id"
          class="job-row"
          :class="statusClass(job.status)"
        >
          <component
            :is="statusIcon(job.status)"
            :size="15"
            class="job-status-icon"
            :class="{ spinning: job.status === 'running' || job.status === 'queued' }"
          />
          <div class="job-main">
            <div class="job-line">
              <span class="job-name">{{ jobLabel(job) }}</span>
              <code class="job-id">{{ job.task_id }}</code>
            </div>
            <div class="job-meta">
              <span>{{ statusLabel(job.status) }}</span>
              <span v-if="job.subagent_type">{{ job.subagent_type }}</span>
              <span v-if="job.model">{{ job.model }}</span>
              <span v-if="latestProgress(job)">{{ latestProgress(job) }}</span>
              <span v-if="formatTime(job.created_at)">{{ formatTime(job.created_at) }}</span>
            </div>
          </div>
          <button
            v-if="isActiveJob(job)"
            class="cancel-btn"
            type="button"
            aria-label="取消后台子代理任务"
            title="取消"
            :disabled="!!cancelling[job.task_id]"
            @click="cancelJob(job)"
          >
            <Activity v-if="cancelling[job.task_id]" :size="14" class="spinning" />
            <X v-else :size="14" />
          </button>
        </div>
      </div>
    </section>
  </Transition>
</template>

<style scoped>
.subagent-jobs {
  flex-shrink: 0;
  border-top: 1px solid var(--border-subtle);
  background: var(--surface-1);
  padding: 8px 14px;
}

.jobs-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
  margin-bottom: 6px;
}

.jobs-title {
  display: flex;
  align-items: center;
  gap: 7px;
  min-width: 0;
  color: var(--text-primary);
  font-size: 12.5px;
  font-weight: 600;
}

.active-count {
  min-width: 18px;
  height: 18px;
  padding: 0 5px;
  border-radius: 9px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  background: var(--brand-50);
  color: var(--brand-700);
  font-size: 11px;
  font-weight: 700;
}

.icon-btn,
.cancel-btn {
  width: 28px;
  height: 28px;
  border-radius: 6px;
  border: 1px solid var(--border-subtle);
  background: var(--surface-2);
  color: var(--text-secondary);
  display: inline-flex;
  align-items: center;
  justify-content: center;
  cursor: pointer;
}

.icon-btn:hover,
.cancel-btn:hover {
  background: var(--surface-3);
  color: var(--text-primary);
}

.icon-btn:disabled,
.cancel-btn:disabled {
  cursor: default;
  opacity: 0.55;
}

.jobs-error {
  display: flex;
  align-items: center;
  gap: 7px;
  color: var(--error-500);
  font-size: 12px;
}

.jobs-list {
  display: flex;
  flex-direction: column;
  gap: 5px;
}

.job-row {
  display: grid;
  grid-template-columns: 18px minmax(0, 1fr) 28px;
  align-items: center;
  gap: 8px;
  min-height: 38px;
  border-radius: 7px;
  padding: 5px 6px;
  color: var(--text-secondary);
}

.job-row.is-active {
  background: var(--surface-2);
}

.job-row.is-ok .job-status-icon {
  color: var(--success-500);
}

.job-row.is-error .job-status-icon {
  color: var(--error-500);
}

.job-row.is-cancelled .job-status-icon {
  color: var(--text-tertiary);
}

.job-main {
  min-width: 0;
}

.job-line,
.job-meta {
  display: flex;
  align-items: center;
  gap: 8px;
  min-width: 0;
}

.job-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--text-primary);
  font-size: 12.5px;
}

.job-id {
  flex: 0 1 auto;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--text-tertiary);
  font-size: 10.5px;
  background: transparent;
}

.job-meta {
  margin-top: 2px;
  color: var(--text-tertiary);
  font-size: 11px;
}

.job-meta span {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}

.spinning {
  animation: subagent-spin 1.1s linear infinite;
}

@keyframes subagent-spin {
  to { transform: rotate(360deg); }
}

.subagent-jobs-enter-active,
.subagent-jobs-leave-active {
  transition: opacity 0.16s var(--ease-out, ease), transform 0.16s var(--ease-out, ease);
}

.subagent-jobs-enter-from,
.subagent-jobs-leave-to {
  opacity: 0;
  transform: translateY(6px);
}
</style>
