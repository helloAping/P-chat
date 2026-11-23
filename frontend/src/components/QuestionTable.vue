<script setup lang="ts">
// Answered questions stay in the transcript as a short confirmed list.
// Options are not repeated here; selection happens in QuestionModal.
import { computed, ref } from 'vue'
import type { MessagePart } from '../api/client'
import { Check } from './icons'

const props = defineProps<{ part: Extract<MessagePart, { kind: 'question' }> }>()
const PREVIEW_COUNT = 3

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
const expanded = ref(true)
const showAll = ref(false)

const rows = computed(() => questions.value.map((question) => ({
  header: question.header || question.question,
  value: (answers.value[question.header] || '').trim(),
})).filter((row) => row.value))

const visibleRows = computed(() => showAll.value ? rows.value : rows.value.slice(0, PREVIEW_COUNT))
const hiddenCount = computed(() => Math.max(0, rows.value.length - PREVIEW_COUNT))
</script>

<template>
  <div
    v-if="questions.length"
    class="question-card"
    :class="{ 'status-open': isOpen, 'status-error': isErrored }"
  >
    <button type="button" class="qt-header" @click="expanded = !expanded">
      <span class="qt-header-icon" :class="isOpen ? 'pending' : isErrored ? 'errored' : 'done'">
        <Check v-if="!isOpen && !isErrored" :size="10" />
      </span>
      <span class="qt-header-title">{{ isErrored ? '未回答' : isOpen ? '等待确认' : '已确认' }}</span>
      <span class="qt-header-count">{{ isOpen || isErrored ? questions.length : rows.length }} 项</span>
      <span class="qt-caret">{{ expanded ? '⌄' : '›' }}</span>
    </button>

    <div v-if="expanded && !isOpen && !isErrored" class="qt-body">
      <div v-for="row in visibleRows" :key="row.header" class="qt-row">
        <span class="qt-label">{{ row.header }}</span>
        <span class="qt-value">{{ row.value }}</span>
      </div>
      <button
        v-if="hiddenCount > 0"
        type="button"
        class="qt-more"
        @click="showAll = !showAll"
      >
        {{ showAll ? '收起' : `展开其余 ${hiddenCount} 项` }} {{ showAll ? '⌃' : '⌄' }}
      </button>
    </div>
  </div>
</template>

<style scoped>
.question-card {
  margin: var(--space-2) 0;
  font-size: 12.5px;
}
.qt-header {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  width: 100%;
  margin: 0;
  padding: 6px 0;
  border: none;
  background: transparent;
  color: var(--text-primary);
  font: inherit;
  text-align: left;
  cursor: pointer;
}
.qt-header-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  border-radius: 50%;
  flex-shrink: 0;
  background: var(--surface-3);
  color: var(--text-tertiary);
}
.qt-header-icon.pending { background: var(--surface-3); color: var(--question-green); }
.qt-header-icon.done {
  background: var(--question-green);
  color: var(--question-on-green);
}
.qt-header-icon.errored { background: var(--warn-50); color: var(--warn-500); }
.qt-header-title { font-weight: 600; }
.qt-header-count {
  color: var(--text-tertiary);
  font-size: 12px;
}
.qt-caret {
  margin-left: auto;
  color: var(--text-quaternary);
  font-size: 12px;
}
.qt-row {
  display: grid;
  grid-template-columns: 88px minmax(0, 1fr);
  gap: var(--space-4);
  padding: 11px 0;
  border-top: 1px solid var(--border-subtle);
}
.qt-label {
  color: var(--text-secondary);
  font-size: 12.5px;
}
.qt-value {
  color: var(--text-primary);
  font-size: 12.5px;
  font-weight: 600;
  line-height: 1.4;
  word-break: break-word;
}
.qt-more {
  width: 100%;
  margin: 0;
  padding: 10px 0 4px;
  border: none;
  border-top: 1px solid var(--border-subtle);
  background: transparent;
  color: var(--text-secondary);
  font: inherit;
  font-size: 13px;
  text-align: left;
  cursor: pointer;
}
</style>
