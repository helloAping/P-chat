<script setup lang="ts">
import { computed } from 'vue'
import type { MessagePart } from '../api/client'

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
  try { return JSON.parse(props.part.name || '{}')?.answers || {} } catch { return {} }
})

const maxOptions = computed(() => {
  let m = 0
  for (const q of questions.value) {
    if (q.options && q.options.length > m) m = q.options.length
  }
  return m
})

function selected(header: string, label: string): boolean {
  const ans = answers.value[header] || ''
  return ans.split(', ').includes(label)
}
</script>

<template>
  <div class="question-table-card" v-if="questions.length">
    <div class="qt-header">LLM 提问</div>
    <div class="qt-table">
      <table>
        <thead>
          <tr>
            <th v-for="q in questions" :key="q.header">
              {{ q.header }}<span v-if="q.multi_select" class="qt-multi"> (多选)</span>
            </th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="rowIdx in maxOptions" :key="rowIdx">
            <td v-for="q in questions" :key="q.header" class="qt-cell">
              <template v-if="q.options[rowIdx - 1]">
                <span
                  class="qt-option"
                  :class="{ 'qt-selected': selected(q.header, q.options[rowIdx - 1].label) }"
                  :title="q.options[rowIdx - 1].description"
                >
                  {{ selected(q.header, q.options[rowIdx - 1].label) ? '●' : '○' }}
                  {{ q.options[rowIdx - 1].label }}
                </span>
              </template>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
  </div>
</template>

<style scoped>
.question-table-card {
  margin: 8px 0;
  border: 1px solid var(--border);
  border-radius: 8px;
  overflow: hidden;
  background: var(--bg-2);
}
.qt-header {
  padding: 6px 12px;
  font-size: 12px;
  font-weight: 600;
  color: var(--text-3);
  background: var(--bg-3);
  border-bottom: 1px solid var(--border);
}
.qt-table {
  overflow-x: auto;
  padding: 8px;
}
.qt-table table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.qt-table th {
  padding: 6px 10px;
  text-align: left;
  font-weight: 600;
  color: var(--text-2);
  border-bottom: 1px solid var(--border);
  white-space: nowrap;
}
.qt-multi { font-weight: 400; font-size: 11px; color: var(--text-4); }
.qt-cell {
  padding: 4px 10px;
  border-bottom: 1px solid var(--border);
  vertical-align: top;
}
.qt-option {
  display: block;
  padding: 2px 0;
  color: var(--text-3);
}
.qt-selected {
  color: var(--accent);
  font-weight: 600;
}
</style>
