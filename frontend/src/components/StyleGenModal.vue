<script setup lang="ts">
/**
 * StyleGenModal — 从当前对话生成 / 优化 AI 风格。
 *
 * Opens from the TopBar's「生成风格」button. Two modes:
 *   - create   — a name (label) + optional requirements → a brand-new style
 *   - optimize — pick an existing style + "想改哪里" → refine it
 *                (built-in styles are read-only, so optimizing them saves
 *                a NEW copy instead of overwriting)
 *
 * The source conversation is always the current session (chat store
 * `state.currentID`) — there is deliberately NO conversation picker.
 * The job runs in the background on the server; this modal consumes the
 * SSE progress stream (stage checklist + streamed prompt/memory text +
 * thinking) and shows a result card with「应用到当前会话」.
 *
 * Reuses existing pieces only — never reimplements them:
 *   getStyles / updateSessionMeta / streamStyleGen (api/client.ts),
 *   consumeSSEStream (api/sse.ts), state.currentID (stores/chat.ts).
 */
import { computed, ref, watch } from 'vue'
import {
  NButton, NInput, NSelect, NRadioGroup, NRadioButton, NSpace, NSpin, useMessage,
} from 'naive-ui'
import { state, currentMeta } from '../stores/chat'
import * as api from '../api/client'
import type { StyleGenResult, StyleGenEvent } from '../api/client'
import { Check, Loader2, Palette, X } from './icons'

const show = defineModel<boolean>('show', { default: false })
const message = useMessage()

// ─── form state ──────────────────────────────────────────────────────
const mode = ref<'create' | 'optimize'>('create')
const label = ref('')
const requirement = ref('')
const styleID = ref<string | null>(null)
const styles = ref<api.StyleInfo[]>([])

// ─── job state ───────────────────────────────────────────────────────
const phase = ref<'idle' | 'running' | 'done' | 'error'>('idle')
const stages = ref<{ stage: string; label: string; done: boolean }[]>([])
const content = ref('')
const thinking = ref('')
const thinkingOpen = ref(false)
const result = ref<StyleGenResult | null>(null)
const errorMsg = ref('')
const errorKind = ref('')
const applying = ref(false)

let abortCtrl: AbortController | null = null
// runSeq guards against stale stream callbacks. Every submit / stop /
// close bumps it; a stream whose seq no longer matches the current value
// is superseded and its onEvent writes are ignored. Without this, closing
// the modal mid-run (which never aborted) and re-submitting would let two
// SSE streams write into the same shared refs (content/thinking/phase).
let runSeq = 0

const STAGE_META: { stage: string; label: string }[] = [
  { stage: 'reading', label: '读取当前对话' },
  { stage: 'analyzing', label: '分析对话与要求' },
  { stage: 'generating', label: '生成人格与记忆' },
  { stage: 'saving', label: '保存风格' },
]

const styleOptions = computed(() =>
  styles.value.map(s => ({ label: s.label || s.id, value: s.id })),
)
const conversationID = computed(() => state.currentID)
const hasSession = computed(() => !!conversationID.value)

const canSubmit = computed(() => {
  if (phase.value === 'running') return false
  if (!hasSession.value) return false
  // A name is required only when creating a brand-new style; optimize
  // keeps the target's current name when the field is left empty.
  if (mode.value === 'create' && !label.value.trim()) return false
  if (mode.value === 'optimize' && !styleID.value) return false
  return true
})

const currentStageIdx = computed(() => {
  // index of the running stage (last done + 1), used to mark "in progress".
  let done = -1
  stages.value.forEach((s, i) => { if (s.done) done = i })
  return done + 1
})

// ─── lifecycle ───────────────────────────────────────────────────────
watch(show, (open) => {
  if (open) {
    reset()
    loadStyles()
    return
  }
  // Modal closed: cancel any in-flight stream so its callbacks can't keep
  // writing into the shared refs after the modal is reopened.
  cancelRun()
})

watch(mode, () => {
  if (mode.value === 'optimize') loadStyles()
})

// cancelRun invalidates the current run (bumps the seq so stale stream
// callbacks are ignored) and aborts its network request.
function cancelRun() {
  runSeq++
  abortCtrl?.abort()
  abortCtrl = null
}

async function loadStyles() {
  try {
    const r = await api.getStyles()
    styles.value = r.styles || []
  } catch (e: any) {
    // non-fatal: the optimize dropdown just stays empty
    if (styles.value.length === 0) {
      message.warning('加载风格列表失败: ' + e.message)
    }
  }
}

function reset() {
  mode.value = 'create'
  label.value = ''
  requirement.value = ''
  styleID.value = null
  phase.value = 'idle'
  stages.value = []
  content.value = ''
  thinking.value = ''
  thinkingOpen.value = false
  result.value = null
  errorMsg.value = ''
  errorKind.value = ''
}

// ─── submit + stream ─────────────────────────────────────────────────
async function submit() {
  if (!canSubmit.value) return
  const convID = conversationID.value
  if (!convID) {
    message.error('当前没有活跃会话')
    return
  }
  // Each run gets a fresh seq + AbortController. Any previous run is
  // superseded (its callbacks are ignored via the seq guard).
  const seq = ++runSeq
  abortCtrl = new AbortController()
  phase.value = 'running'
  stages.value = STAGE_META.map(s => ({ stage: s.stage, label: s.label, done: false }))
  content.value = ''
  thinking.value = ''
  result.value = null
  errorMsg.value = ''
  errorKind.value = ''

  try {
    const { job_id } = await api.startStyleGen({
      mode: mode.value,
      style_id: mode.value === 'optimize' ? (styleID.value || undefined) : undefined,
      // create always has a label (canSubmit enforces it); optimize may
      // leave it empty — the server keeps the target style's current name.
      label: label.value.trim(),
      requirement: requirement.value.trim() || undefined,
      conversation_id: convID,
    })

    await api.streamStyleGen(job_id, onRunEvent(seq), abortCtrl.signal)

    // The server always sends a terminal result/error event before closing
    // the stream; if we somehow finish without one (e.g. dropped socket),
    // fall back to a final status poll.
    if (seq !== runSeq) return // superseded while streaming
    if (phase.value === 'running') {
      const st = await api.fetchStyleGenStatus(job_id)
      if (seq !== runSeq) return
      if (st?.status === 'error') {
        phase.value = 'error'
        errorMsg.value = st.error || '生成失败'
        errorKind.value = st.error_kind || ''
      } else if (st?.result) {
        phase.value = 'done'
        result.value = st.result
      } else {
        phase.value = 'error'
        errorMsg.value = '连接中断，未收到生成结果'
      }
    }
  } catch (e: any) {
    if (seq !== runSeq) {
      // superseded (new run / stop / modal closed): swallow quietly
      phase.value = 'idle'
      return
    }
    phase.value = 'error'
    errorMsg.value = e?.message || String(e)
    errorKind.value = ''
  }
}

// onRunEvent binds an SSE event handler to a specific run. Events from a
// superseded run are dropped so they can't corrupt the current run's state.
function onRunEvent(seq: number) {
  return (ev: StyleGenEvent) => {
    if (seq !== runSeq) return
    if (ev.type === 'stage' && ev.stage) {
      const hit = stages.value.find(s => s.stage === ev.stage)
      if (hit) hit.done = true
      else stages.value.push({ stage: ev.stage, label: ev.label || ev.stage, done: true })
    } else if (ev.type === 'content' && ev.content) {
      content.value += ev.content
    } else if (ev.type === 'thinking' && ev.thinking) {
      thinking.value += ev.thinking
    } else if (ev.type === 'result' && ev.result_json) {
      phase.value = 'done'
      result.value = ev.result_json
    } else if (ev.type === 'error') {
      phase.value = 'error'
      errorMsg.value = ev.error || '生成失败'
      errorKind.value = ev.error_kind || ''
    }
  }
}

function stop() {
  cancelRun()
  phase.value = 'idle'
}

// ─── result actions ──────────────────────────────────────────────────
async function applyToCurrentSession() {
  const r = result.value
  const id = conversationID.value
  if (!r || !id) return
  applying.value = true
  try {
    const resp = await api.updateSessionMeta(id, { style: r.id })
    state.sessionMeta[id] = {
      ...(state.sessionMeta[id] || currentMeta.value),
      style: resp.style ?? r.id,
    }
    const s = state.sessions.find(x => x.id === id)
    if (s) s.style = resp.style ?? r.id
    message.success(`已应用风格「${r.label || r.id}」，下一轮回复将使用它`)
    show.value = false
  } catch (e: any) {
    message.error('应用失败: ' + e.message)
  }
  applying.value = false
}

function errorHint(kind: string): string {
  switch (kind) {
    case 'E_EMPTY': return '当前对话为空：先聊几句，或补一个要求再生成。'
    case 'E_NOT_FOUND': return '要优化的风格不存在，请重新选择。'
    case 'E_LLM': return 'LLM 调用失败，请检查提供商/模型配置后重试。'
    case 'E_DUP': return '生成的风格 id 已存在，换个名称重试。'
    case 'E_ARGS': return '参数不正确。'
    default: return ''
  }
}
</script>

<template>
  <NModal
    v-model:show="show"
    preset="card"
    title="生成风格"
    style="width: 620px"
    :mask-closable="false"
  >
    <div class="sg-body">
      <!-- ─── form (before running) ─── -->
      <template v-if="phase === 'idle'">
        <NRadioGroup v-model:value="mode" size="small">
          <NRadioButton value="create">新建风格</NRadioButton>
          <NRadioButton value="optimize">优化现有</NRadioButton>
        </NRadioGroup>

        <div class="sg-field">
          <label class="sg-label" :class="{ 'sg-required': true }">
            {{ mode === 'create' ? '风格名称' : '优化后的名称' }}
          </label>
          <NInput
            v-model:value="label"
            :placeholder="mode === 'create' ? '如：温柔老师、代码导师' : '可留空（默认沿用原名）'"
            size="small"
          />
        </div>

        <div v-if="mode === 'optimize'" class="sg-field">
          <label class="sg-label sg-required">要优化的风格</label>
          <NSelect
            v-model:value="styleID"
            :options="styleOptions"
            placeholder="选择一个现有风格（内置风格会另存为新风格）"
            size="small"
          />
        </div>

        <div class="sg-field">
          <label class="sg-label">
            {{ mode === 'create' ? '补充内容 / 要求（可选）' : '想改哪里（可选）' }}
          </label>
          <NInput
            v-model:value="requirement"
            type="textarea"
            :autosize="{ minRows: 2, maxRows: 4 }"
            :placeholder="mode === 'create'
              ? '语气 / 人设 / 要记住的事，例如「语气温柔，称呼我为主人」'
              : '例如「更简洁一点」「语气再冷一些」'"
          />
        </div>

        <div class="sg-source-hint">
          <Palette :size="13" />
          <span>来源对话：当前会话（{{ conversationID ? conversationID.slice(0, 12) + '…' : '无活跃会话' }}），只读，不会改动原对话</span>
        </div>

        <div class="sg-actions">
          <NButton size="small" @click="show = false">取消</NButton>
          <NButton
            type="primary"
            size="small"
            :disabled="!canSubmit"
            @click="submit"
          >
            {{ mode === 'create' ? '开始生成' : '开始优化' }}
          </NButton>
        </div>
      </template>

      <!-- ─── running: progress + streamed content ─── -->
      <template v-else-if="phase === 'running'">
        <NSpin size="small" :show="true">
          <div class="sg-progress">
            <div
              v-for="(s, i) in stages"
              :key="s.stage"
              class="sg-stage"
              :class="{
                'sg-stage-done': s.done,
                'sg-stage-active': !s.done && i === currentStageIdx,
              }"
            >
              <span class="sg-stage-icon">
                <Check v-if="s.done" :size="13" />
                <Loader2 v-else-if="i === currentStageIdx" :size="13" class="sg-spin" />
                <span v-else class="sg-stage-dot" />
              </span>
              <span class="sg-stage-label">{{ s.label }}</span>
            </div>
          </div>
        </NSpin>

        <div v-if="thinking" class="sg-thinking">
          <button
            type="button"
            class="sg-thinking-toggle"
            @click="thinkingOpen = !thinkingOpen"
          >
            <span>思考过程</span>
            <span class="sg-thinking-arrow">{{ thinkingOpen ? '▾' : '▸' }}</span>
          </button>
          <pre v-if="thinkingOpen" class="sg-thinking-body">{{ thinking }}</pre>
        </div>

        <div class="sg-stream">
          <div v-if="content" class="sg-stream-title">生成内容</div>
          <pre v-if="content" class="sg-stream-body">{{ content }}</pre>
          <div v-else class="sg-stream-empty">正在等待模型输出…</div>
        </div>

        <div class="sg-actions">
          <NButton size="small" @click="stop">停止</NButton>
        </div>
      </template>

      <!-- ─── result card ─── -->
      <template v-else-if="phase === 'done' && result">
        <div class="sg-result">
          <div class="sg-result-head">
            <span class="sg-result-badge">
              {{ mode === 'optimize' && !result.updated
                ? '内置只读 · 已另存新风格'
                : mode === 'optimize' ? '已原地更新' : '新风格已创建' }}
            </span>
            <span class="sg-result-id">
              <code>{{ result.id }}</code>
            </span>
          </div>
          <div class="sg-result-label">{{ result.label || result.id }}</div>
          <div class="sg-result-meta">
            <span>prompt {{ result.prompt.length }} 字符</span>
            <span>memory {{ result.memory?.length || 0 }} 字符</span>
            <span>可去「设置 → 风格」进一步编辑</span>
          </div>
          <pre class="sg-result-preview">{{ result.prompt }}</pre>
        </div>

        <div class="sg-actions">
          <NButton size="small" @click="show = false">关闭</NButton>
          <NButton size="small" @click="reset">再生成一个</NButton>
          <NButton
            type="primary"
            size="small"
            :loading="applying"
            :disabled="!hasSession"
            @click="applyToCurrentSession"
          >
            应用到当前会话
          </NButton>
        </div>
      </template>

      <!-- ─── error ─── -->
      <template v-else-if="phase === 'error'">
        <div class="sg-error">
          <X :size="16" class="sg-error-icon" />
          <div>
            <div class="sg-error-title">生成失败</div>
            <div class="sg-error-msg">{{ errorMsg }}</div>
            <div v-if="errorHint(errorKind)" class="sg-error-hint">{{ errorHint(errorKind) }}</div>
          </div>
        </div>
        <div class="sg-actions">
          <NButton size="small" @click="reset">返回</NButton>
          <NButton size="small" type="primary" @click="submit">重试</NButton>
        </div>
      </template>
    </div>
  </NModal>
</template>

<style scoped>
.sg-body {
  display: flex;
  flex-direction: column;
  gap: var(--space-4);
  padding: 2px 0;
}
.sg-field {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}
.sg-label {
  font-size: 12.5px;
  color: var(--text-secondary);
}
.sg-required::after {
  content: ' *';
  color: var(--error-500);
}
.sg-source-hint {
  display: flex;
  align-items: center;
  gap: 6px;
  font-size: 12px;
  color: var(--text-tertiary);
  background: var(--surface-2);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  padding: var(--space-2) var(--space-3);
}
.sg-actions {
  display: flex;
  justify-content: flex-end;
  gap: var(--space-2);
  margin-top: var(--space-1);
}

/* progress checklist */
.sg-progress {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
  background: var(--surface-2);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-md);
  padding: var(--space-3);
}
.sg-stage {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12.5px;
  color: var(--text-quaternary);
}
.sg-stage-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
}
.sg-stage-dot {
  width: 7px;
  height: 7px;
  border-radius: var(--radius-pill);
  background: var(--border-strong);
}
.sg-stage-active {
  color: var(--brand-500);
}
.sg-stage-active .sg-stage-icon svg {
  color: var(--brand-500);
}
.sg-stage-done {
  color: var(--success-500);
}
.sg-spin {
  animation: sg-spin 1s linear infinite;
}
@keyframes sg-spin {
  to { transform: rotate(360deg); }
}

/* thinking + streamed content */
.sg-thinking {
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-md);
  overflow: hidden;
}
.sg-thinking-toggle {
  width: 100%;
  display: flex;
  align-items: center;
  justify-content: space-between;
  background: var(--surface-2);
  border: none;
  color: var(--text-tertiary);
  font-size: 12.5px;
  padding: var(--space-2) var(--space-3);
  cursor: pointer;
}
.sg-thinking-body {
  max-height: 160px;
  overflow: auto;
  margin: 0;
  padding: var(--space-3);
  font-family: var(--font-mono);
  font-size: 12px;
  color: var(--text-tertiary);
  white-space: pre-wrap;
  word-break: break-word;
  background: var(--surface-1);
}
.sg-stream {
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-md);
  background: var(--surface-1);
  max-height: 240px;
  overflow: auto;
}
.sg-stream-title {
  font-size: 12px;
  color: var(--text-tertiary);
  padding: var(--space-2) var(--space-3) 0;
}
.sg-stream-body {
  margin: 0;
  padding: var(--space-3);
  font-family: var(--font-mono);
  font-size: 12px;
  line-height: 1.5;
  color: var(--text-primary);
  white-space: pre-wrap;
  word-break: break-word;
}
.sg-stream-empty {
  padding: var(--space-4);
  font-size: 12.5px;
  color: var(--text-quaternary);
  text-align: center;
}

/* result card */
.sg-result {
  border: 1px solid var(--border-default);
  border-radius: var(--radius-lg);
  background: var(--surface-2);
  padding: var(--space-4);
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}
.sg-result-head {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-2);
}
.sg-result-badge {
  font-size: 12px;
  color: var(--success-500);
  background: var(--success-50);
  border-radius: var(--radius-sm);
  padding: 2px 8px;
}
.sg-result-id code {
  font-family: var(--font-mono);
  font-size: 12px;
  color: var(--text-secondary);
  background: var(--surface-3);
  border-radius: var(--radius-sm);
  padding: 1px 6px;
}
.sg-result-label {
  font-size: 15px;
  font-weight: 600;
  color: var(--text-primary);
}
.sg-result-meta {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2) var(--space-4);
  font-size: 12px;
  color: var(--text-tertiary);
}
.sg-result-preview {
  margin: 0;
  max-height: 180px;
  overflow: auto;
  font-family: var(--font-mono);
  font-size: 12px;
  line-height: 1.5;
  color: var(--text-secondary);
  white-space: pre-wrap;
  word-break: break-word;
  background: var(--surface-1);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  padding: var(--space-3);
}

/* error */
.sg-error {
  display: flex;
  gap: var(--space-3);
  align-items: flex-start;
  background: var(--error-50);
  border: 1px solid var(--error-500);
  border-radius: var(--radius-md);
  padding: var(--space-4);
}
.sg-error-icon {
  color: var(--error-500);
  flex-shrink: 0;
  margin-top: 2px;
}
.sg-error-title {
  font-size: 13px;
  font-weight: 600;
  color: var(--error-500);
}
.sg-error-msg {
  font-size: 12.5px;
  color: var(--text-primary);
  margin-top: 2px;
  word-break: break-word;
}
.sg-error-hint {
  font-size: 12px;
  color: var(--text-tertiary);
  margin-top: 4px;
}
</style>
