<script setup lang="ts">
// Card for an LLM-issued `question` tool call. The card
// shows the question grid (one column per question, one
// row per option) and highlights the user's picks.
//
// Per-option visual: a Circle for single-select (radio
// look), a Square for multi-select (checkbox look). Both
// pickers use the same component but the icon swaps based
// on the question's `multi_select` flag — the LLM may
// mix multi/single questions in the same call.
//
// Below the question grid, an "answer chip" footer shows
// the human-readable summary of the user's picks. The
// chip is a system-style visual (smaller, neutral color)
// so the question + answer reads as one cohesive card
// rather than two separate UI events.
import { computed } from 'vue'
import type { MessagePart } from '../api/client'
import { Circle, Square, Check } from './icons'

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

// Per-question max options drives the table row count.
const maxOptions = computed(() => {
  let m = 0
  for (const q of questions.value) {
    if (q.options && q.options.length > m) m = q.options.length
  }
  return m
})

// part status drives the header chip + footer visibility.
const isOpen = computed(() => !props.part.question_status || props.part.question_status === 'open')
const isErrored = computed(() => props.part.question_status === 'error')

function selected(header: string, label: string): boolean {
  const ans = answers.value[header] || ''
  return ans.split(', ').includes(label)
}

// Compact "你选了: A, B" footer. Multi-select values are
// joined with comma; single-select values are shown as-is.
const answerSummary = computed(() => {
  const parts: string[] = []
  for (const q of questions.value) {
    const ans = answers.value[q.header]
    if (ans) parts.push(`${q.header}: ${ans}`)
  }
  return parts.join(' · ')
})

// Per-question "is this question answered" flag.
function isAnswered(q: QuestionItem): boolean {
  return !!answers.value[q.header]
}
</script>

<template>
  <div v-if="questions.length" class="question-card" :class="{ 'status-open': isOpen, 'status-error': isErrored }">
    <div class="qt-header">
      <span class="qt-header-icon" :class="isOpen ? 'pending' : isErrored ? 'errored' : 'done'">
        <Check v-if="!isOpen && !isErrored" :size="11" />
        <Circle v-else-if="isOpen" :size="11" />
      </span>
      <span class="qt-header-title">LLM 提问</span>
      <span class="qt-header-status" v-if="isOpen">等待回答</span>
      <span class="qt-header-status done" v-else-if="isErrored">未回答</span>
      <span class="qt-header-status done" v-else>已回答</span>
    </div>
    <div class="qt-body">
      <table>
        <thead>
          <tr>
            <th v-for="q in questions" :key="q.header" :class="{ 'is-multi': q.multi_select }">
              <span class="qt-q-title">{{ q.header }}</span>
              <span v-if="q.multi_select" class="qt-multi">多选</span>
            </th>
          </tr>
        </thead>
        <tbody>
          <tr v-for="rowIdx in maxOptions" :key="rowIdx">
            <td v-for="q in questions" :key="q.header" class="qt-cell">
              <template v-if="q.options[rowIdx - 1]">
                <span
                  class="qt-option"
                  :class="{
                    'qt-selected': selected(q.header, q.options[rowIdx - 1].label),
                    'is-multi': q.multi_select,
                  }"
                  :title="q.options[rowIdx - 1].description"
                >
                  <!-- Single-select → Circle (radio). Multi-select → Square
                       (checkbox). Filled when the option is picked. -->
                  <component
                    :is="q.multi_select ? Square : Circle"
                    :size="12"
                    :fill="selected(q.header, q.options[rowIdx - 1].label) ? 'currentColor' : 'none'"
                    class="qt-pick-icon"
                  />
                  <span class="qt-option-label">{{ q.options[rowIdx - 1].label }}</span>
                </span>
              </template>
            </td>
          </tr>
        </tbody>
      </table>
    </div>
    <!-- Answer footer: shown only when the user has picked at
         least one option. Lives inside the same card so the
         question + answer read as one cohesive block. -->
    <div v-if="answerSummary" class="qt-answer">
      <Check :size="12" class="qt-answer-icon" />
      <span class="qt-answer-label">你选了</span>
      <span class="qt-answer-summary">{{ answerSummary }}</span>
    </div>
  </div>
</template>

<style scoped>
/* Card chrome. Matches the other "structural" cards in the
 * chat (ToolCallCard, SubAgentCard) so they all read as
 * one family: surface-2 body, subtle border, left status
 * rail. Radius and padding follow the unified spec
 * (frontend-design.md §3.3 / §4). */
.question-card {
  margin: 8px 0;
  border: 1px solid var(--border-subtle);
  border-left: 3px solid var(--border-default);
  border-radius: var(--radius-md);
  background: var(--surface-2);
  overflow: hidden;
  font-size: 13px;
  transition: border-color var(--dur-fast) var(--ease-out);
}
.question-card.status-open { border-left-color: var(--brand-500); }
.question-card.status-error { border-left-color: var(--error-500); }

/* Header — same role as the other cards: a 28-32px row with
 * a small status icon + a title + a meta badge on the
 * right. The status badge text shifts between 等待回答 /
 * 已回答 / 未回答 so the user always knows the state
 * without opening the table. */
.qt-header {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 12px;
  background: var(--surface-1);
  border-bottom: 1px solid var(--border-subtle);
  font-size: 12px;
  color: var(--text-secondary);
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
.qt-header-icon.pending { background: var(--brand-50); color: var(--brand-500); }
.qt-header-icon.done { background: var(--success-50); color: var(--success-500); }
.qt-header-icon.errored { background: var(--error-50); color: var(--error-500); }
.qt-header-title { font-weight: 600; color: var(--text-primary); flex: 1; }
.qt-header-status { font-size: 11px; color: var(--text-tertiary); }
.qt-header-status.done { color: var(--success-500); }

/* Body — the question grid. Single column per question,
 * one row per option. Column widths are even and
 * auto-sized. */
.qt-body { overflow-x: auto; padding: 8px 10px 10px; }
.qt-body table {
  width: 100%;
  border-collapse: collapse;
  font-size: 13px;
}
.qt-body th {
  padding: 6px 10px 4px;
  text-align: left;
  font-weight: 600;
  font-size: 12.5px;
  color: var(--text-primary);
  border-bottom: 1px solid var(--border-subtle);
  white-space: nowrap;
  line-height: 1.4;
}
.qt-body th.is-multi { color: var(--text-secondary); }
.qt-q-title { margin-right: 6px; }
.qt-multi {
  display: inline-block;
  font-size: 10px;
  font-weight: 500;
  padding: 1px 5px;
  border-radius: 3px;
  background: var(--surface-3);
  color: var(--text-tertiary);
  vertical-align: middle;
  line-height: 1.4;
}
.qt-cell {
  padding: 4px 10px;
  border-bottom: 1px solid var(--border-subtle);
  vertical-align: top;
}
.qt-cell:last-child { border-bottom: none; }

/* Per-option row. Same treatment for single and multi;
 * only the picker icon swaps (Circle vs Square). The
 * .is-multi modifier tints the picker so multi-select
 * answers are visually distinguishable when scanning
 * the grid. */
.qt-option {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 3px 0;
  color: var(--text-secondary);
  font-size: 12.5px;
  transition: color var(--dur-fast) var(--ease-out);
}
.qt-option.is-multi .qt-pick-icon { color: var(--text-tertiary); }
.qt-option:hover { color: var(--text-primary); }
.qt-option.qt-selected { color: var(--brand-500); font-weight: 600; }
.qt-option.qt-selected .qt-pick-icon { color: var(--brand-500); }
.qt-pick-icon { flex-shrink: 0; color: var(--text-tertiary); }
.qt-option-label { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }

/* Answer footer — "你选了: A · B · C". Sits inside the
 * same card so the question + answer reads as one block.
 * Color: brand tone for "completed" feedback. */
.qt-answer {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
  padding: 6px 12px;
  background: var(--brand-50);
  border-top: 1px solid var(--border-subtle);
  font-size: 12px;
  color: var(--text-secondary);
}
.qt-answer-icon { color: var(--brand-500); flex-shrink: 0; }
.qt-answer-label { color: var(--text-tertiary); font-weight: 500; }
.qt-answer-summary { color: var(--text-primary); font-weight: 500; }
</style>
