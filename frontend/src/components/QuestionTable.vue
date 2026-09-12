<script setup lang="ts">
// Compact card for an LLM `question` tool call.
// Options render as single-line chips (label only; description
// lives in the title tooltip) so multi-option questions stay
// scannable instead of stacking tall two-line cards.
import { computed } from 'vue'
import type { MessagePart } from '../api/client'
import { Circle, Square, Check, TextCursorInput } from './icons'

const props = defineProps<{ part: Extract<MessagePart, { kind: 'question' }> }>()

interface QuestionItem {
  question: string
  header: string
  options: { label: string; description: string }[]
  multi_select?: boolean
}

interface AnswerMap { [key: string]: string }

const questions = computed<QuestionItem[]>(() => {
  try { return JSON.parse(props.part.text)?.questions || [] } catch { return [] }
})

const answers = computed<AnswerMap>(() => {
  try { return JSON.parse(props.part.name || '{}') || {} } catch { return {} }
})

const isOpen = computed(() => !props.part.question_status || props.part.question_status === 'open')
const isErrored = computed(() => props.part.question_status === 'error')

function selected(header: string, label: string): boolean {
  return answerValues(header).includes(label)
}

function answerValues(header: string): string[] {
  const ans = answers.value[header] || ''
  return ans.split(/\s*,\s*/).map(v => v.trim()).filter(Boolean)
}

function customAnswersFor(q: QuestionItem): string[] {
  const labels = new Set((q.options || []).map(opt => opt.label))
  return answerValues(q.header).filter(value => !labels.has(value))
}

function isAnswered(q: QuestionItem): boolean {
  return !!answers.value[q.header]
}

/** Prefer a one-line prompt; skip when it only repeats the header. */
function promptLine(q: QuestionItem): string {
  const prompt = (q.question || '').trim()
  if (!prompt || prompt === q.header) return ''
  return prompt
}
</script>

<template>
  <div
    v-if="questions.length"
    class="question-card"
    :class="{ 'status-open': isOpen, 'status-error': isErrored }"
  >
    <div class="qt-header">
      <span class="qt-header-icon" :class="isOpen ? 'pending' : isErrored ? 'errored' : 'done'">
        <Check v-if="!isOpen && !isErrored" :size="10" />
        <Circle v-else-if="isOpen" :size="10" />
      </span>
      <span class="qt-header-title">LLM 提问</span>
      <span v-if="questions.length > 1" class="qt-header-count">{{ questions.length }} 题</span>
      <span v-if="isOpen" class="qt-header-status">等待回答</span>
      <span v-else-if="isErrored" class="qt-header-status error">未回答</span>
      <span v-else class="qt-header-status done">已回答</span>
    </div>

    <div class="qt-body">
      <section
        v-for="q in questions"
        :key="q.header"
        class="qt-group"
        :class="{ 'qt-group-answered': isAnswered(q) }"
      >
        <div class="qt-group-head">
          <span class="qt-q-title">{{ q.header }}</span>
          <span v-if="q.multi_select" class="qt-multi">多选</span>
        </div>
        <p v-if="promptLine(q)" class="qt-q-prompt" :title="promptLine(q)">{{ promptLine(q) }}</p>

        <div class="qt-options">
          <span
            v-for="opt in q.options"
            :key="opt.label"
            class="qt-option"
            :class="{ 'qt-selected': selected(q.header, opt.label) }"
            :title="opt.description || opt.label"
          >
            <component
              :is="q.multi_select ? Square : Circle"
              :size="11"
              :fill="selected(q.header, opt.label) ? 'currentColor' : 'none'"
              class="qt-pick-icon"
            />
            <span class="qt-option-label">{{ opt.label }}</span>
          </span>

          <span
            v-for="answer in customAnswersFor(q)"
            :key="`custom:${answer}`"
            class="qt-option qt-option-custom qt-selected"
            :title="answer"
          >
            <TextCursorInput :size="11" class="qt-pick-icon" />
            <span class="qt-option-label">{{ answer }}</span>
          </span>
        </div>
      </section>
    </div>
  </div>
</template>

<style scoped>
.question-card {
  margin: var(--space-2) 0;
  border: 1px solid var(--border-subtle);
  border-left: 3px solid var(--border-default);
  border-radius: var(--radius-md);
  background: var(--surface-1);
  overflow: hidden;
  font-size: 12.5px;
}
.question-card.status-open { border-left-color: var(--brand-500); }
.question-card.status-error { border-left-color: var(--warn-500); }

.qt-header {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: 6px var(--space-3);
  border-bottom: 1px solid var(--border-subtle);
  font-size: 12px;
  color: var(--text-secondary);
}
.qt-header-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 14px;
  height: 14px;
  border-radius: 50%;
  flex-shrink: 0;
  background: var(--surface-3);
  color: var(--text-tertiary);
}
.qt-header-icon.pending { background: var(--brand-50); color: var(--brand-500); }
.qt-header-icon.done { background: var(--success-50); color: var(--success-500); }
.qt-header-icon.errored { background: var(--warn-50); color: var(--warn-500); }
.qt-header-title {
  color: var(--text-primary);
  font-weight: 600;
}
.qt-header-count {
  color: var(--text-tertiary);
  font: 500 11px/1 var(--font-mono);
}
.qt-header-status {
  margin-left: auto;
  padding: 0 var(--space-2);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-pill);
  color: var(--text-tertiary);
  font-size: 11px;
  line-height: 18px;
}
.qt-header-status.done {
  border-color: color-mix(in srgb, var(--success-500) 35%, var(--border-subtle));
  background: var(--success-50);
  color: var(--success-500);
}
.qt-header-status.error {
  border-color: color-mix(in srgb, var(--warn-500) 35%, var(--border-subtle));
  background: var(--warn-50);
  color: var(--warn-500);
}

.qt-body {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  padding: var(--space-2) var(--space-3) var(--space-3);
}
.qt-group {
  min-width: 0;
  padding: var(--space-2) 0 0;
  border-top: 1px solid var(--border-subtle);
}
.qt-group:first-child {
  padding-top: 0;
  border-top: 0;
}
.qt-group-head {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  min-width: 0;
  margin-bottom: 2px;
}
.qt-q-title {
  min-width: 0;
  overflow: hidden;
  color: var(--text-primary);
  font-size: 12.5px;
  font-weight: 600;
  line-height: 1.3;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.qt-q-prompt {
  margin: 0 0 var(--space-1);
  color: var(--text-tertiary);
  font-size: 11.5px;
  line-height: 1.35;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.qt-multi {
  display: inline-flex;
  align-items: center;
  flex-shrink: 0;
  height: 16px;
  padding: 0 6px;
  border-radius: var(--radius-pill);
  background: var(--brand-50);
  color: var(--brand-500);
  font-size: 10px;
  font-weight: 600;
  line-height: 1;
}

/* Dense wrapping chips — label only, desc via title tooltip. */
.qt-options {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
}
.qt-option {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  max-width: 100%;
  min-width: 0;
  height: 26px;
  padding: 0 8px 0 6px;
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  background: var(--surface-2);
  color: var(--text-secondary);
  font-size: 12px;
  line-height: 1;
  transition:
    border-color var(--dur-fast) var(--ease-out),
    color var(--dur-fast) var(--ease-out),
    background var(--dur-fast) var(--ease-out);
}
.qt-option.qt-selected {
  border-color: var(--brand-100);
  background: var(--brand-50);
  color: var(--brand-600);
}
.qt-option.qt-selected .qt-option-label { font-weight: 600; }
.qt-option.qt-selected .qt-pick-icon { color: var(--brand-500); }
.qt-option-custom {
  border-style: dashed;
  max-width: min(100%, 280px);
}
.qt-pick-icon {
  flex-shrink: 0;
  color: var(--text-tertiary);
}
.qt-option-label {
  min-width: 0;
  overflow: hidden;
  color: inherit;
  text-overflow: ellipsis;
  white-space: nowrap;
}
</style>
