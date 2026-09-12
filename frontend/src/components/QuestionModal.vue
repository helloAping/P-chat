<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { NButton, NRadio, NCheckbox, NInput } from 'naive-ui'
import { submitQuestionAnswer, currentPendingQuestion } from '../stores/chat'
import { ArrowUp, MessageSquare } from './icons'

const CUSTOM_VALUE = '__pchat_custom__'

const currentIndex = ref(0)
const answers = ref<Record<string, string>>({})
const multiAnswers = ref<Record<string, string[]>>({})
const customInputs = ref<Record<string, string>>({})
const emit = defineEmits<{ (e: 'locate-question'): void }>()

const questions = computed(() => currentPendingQuestion.value?.questions || [])
const currentQuestion = computed(() => questions.value[currentIndex.value] || null)
const isLast = computed(() => currentIndex.value >= questions.value.length - 1)

watch(currentPendingQuestion, (q) => {
  if (q) {
    currentIndex.value = 0
    answers.value = {}
    multiAnswers.value = {}
    customInputs.value = {}
  }
})

function keyOfQuestion(): string {
  return currentQuestion.value?.question || ''
}

function selectOption(value: string) {
  const q = currentQuestion.value
  if (!q) return
  const key = keyOfQuestion()
  if (q.multi_select) {
    const arr = [...(multiAnswers.value[key] || [])]
    const idx = arr.indexOf(value)
    if (idx >= 0) arr.splice(idx, 1)
    else arr.push(value)
    if (value === CUSTOM_VALUE && idx >= 0) {
      customInputs.value[key] = ''
    }
    multiAnswers.value[key] = arr
    return
  }
  if (answers.value[key] === value) {
    answers.value[key] = ''
    if (value === CUSTOM_VALUE) customInputs.value[key] = ''
  } else {
    answers.value[key] = value
  }
}

function isSelected(value: string): boolean {
  const q = currentQuestion.value
  if (!q) return false
  const key = keyOfQuestion()
  if (q.multi_select) {
    return (multiAnswers.value[key] || []).includes(value)
  }
  return answers.value[key] === value
}

function ensureOption(value: string) {
  if (!isSelected(value)) selectOption(value)
}

function isQuestionAnswered(q: { question: string; multi_select?: boolean }): boolean {
  if (q.multi_select) return (multiAnswers.value[q.question] || []).length > 0
  return !!answers.value[q.question]
}

function canProceed(): boolean {
  const q = currentQuestion.value
  if (!q) return false
  const key = keyOfQuestion()
  if (q.multi_select) {
    const selected = multiAnswers.value[key] || []
    if (selected.length === 0) return false
    if (selected.includes(CUSTOM_VALUE) && !(customInputs.value[key] || '').trim()) return false
    return true
  }
  if (!answers.value[key]) return false
  if (answers.value[key] === CUSTOM_VALUE && !(customInputs.value[key] || '').trim()) return false
  return true
}

function next() {
  if (isLast.value) submit()
  else currentIndex.value++
}

function prev() {
  if (currentIndex.value > 0) currentIndex.value--
}

function submit() {
  const all: Record<string, string> = {}
  for (const q of questions.value) {
    const key = q.question
    let value = ''
    if (q.multi_select) {
      value = (multiAnswers.value[key] || []).map((item) => {
        if (item === CUSTOM_VALUE) return (customInputs.value[key] || '').trim()
        return item
      }).filter(Boolean).join(', ')
    } else {
      const raw = answers.value[key] || ''
      value = raw === CUSTOM_VALUE ? (customInputs.value[key] || '').trim() : raw
    }
    if (value) all[q.header] = value
  }
  submitQuestionAnswer(all)
}

const regularOptions = computed(() => {
  const q = currentQuestion.value
  if (!q) return []
  return q.options.map(opt => ({ ...opt, value: opt.label }))
})

/** Show question text once; skip duplicate header echo. */
const promptText = computed(() => {
  const q = currentQuestion.value
  if (!q) return ''
  const text = (q.question || '').trim()
  if (!text || text === q.header) return q.header
  return text
})
</script>

<template>
  <Transition name="question-dock">
    <section
      v-if="currentPendingQuestion"
      class="question-dock"
      role="region"
      aria-labelledby="qmodal-title"
    >
      <header class="qmodal-header">
        <div class="qmodal-title-wrap">
          <MessageSquare :size="14" class="qmodal-header-icon" />
          <span id="qmodal-title" class="qmodal-title">需要你确认</span>
          <span v-if="questions.length > 1" class="qmodal-progress">{{ currentIndex + 1 }}/{{ questions.length }}</span>
          <span v-if="currentQuestion?.multi_select" class="qmulti">多选</span>
        </div>
        <NButton
          size="tiny"
          quaternary
          title="定位到聊天中的提问"
          aria-label="定位到聊天中的提问"
          @click="emit('locate-question')"
        >
          <ArrowUp :size="14" />
        </NButton>
      </header>

      <div v-if="questions.length > 1" class="qnav">
        <button
          v-for="(q, i) in questions"
          :key="i"
          type="button"
          class="qnav-chip"
          :class="{ 'qnav-active': i === currentIndex, 'qnav-answered': isQuestionAnswered(q) }"
          :title="q.header"
          @click="currentIndex = i"
        >
          <span class="qnav-dot" aria-hidden="true" />
          {{ q.header }}
        </button>
      </div>

      <div v-if="currentQuestion" class="qbody">
        <div class="qprompt">{{ promptText }}</div>

        <div class="qopts">
          <div
            v-for="opt in regularOptions"
            :key="opt.value"
            class="qopt"
            :class="{ 'qopt-sel': isSelected(opt.value) }"
            role="button"
            tabindex="0"
            :title="opt.description || opt.label"
            @click="selectOption(opt.value)"
            @keydown.enter.prevent="selectOption(opt.value)"
            @keydown.space.prevent="selectOption(opt.value)"
          >
            <NRadio
              v-if="!currentQuestion.multi_select"
              :checked="isSelected(opt.value)"
              tabindex="-1"
              class="qopt-ctrl"
            />
            <NCheckbox
              v-else
              :checked="isSelected(opt.value)"
              tabindex="-1"
              class="qopt-ctrl"
            />
            <span class="qopt-label">{{ opt.label }}</span>
          </div>
        </div>

        <div
          class="qcustom"
          :class="{ 'qcustom-sel': isSelected(CUSTOM_VALUE) }"
          @click="ensureOption(CUSTOM_VALUE)"
        >
          <button
            type="button"
            class="qcustom-toggle"
            @click.stop="selectOption(CUSTOM_VALUE)"
          >
            <NRadio
              v-if="!currentQuestion.multi_select"
              :checked="isSelected(CUSTOM_VALUE)"
              tabindex="-1"
              class="qopt-ctrl"
            />
            <NCheckbox
              v-else
              :checked="isSelected(CUSTOM_VALUE)"
              tabindex="-1"
              class="qopt-ctrl"
            />
            <span class="qcustom-label">其他</span>
          </button>
          <NInput
            v-if="isSelected(CUSTOM_VALUE)"
            v-model:value="customInputs[currentQuestion.question]"
            size="tiny"
            placeholder="输入自定义回答…"
            :autofocus="true"
            class="qcustom-input"
            @click.stop
            @focus="ensureOption(CUSTOM_VALUE)"
            @keydown.enter.stop
          />
        </div>
      </div>

      <footer class="qmodal-footer">
        <NButton v-if="currentIndex > 0" size="tiny" @click="prev">上一步</NButton>
        <NButton type="primary" size="tiny" :disabled="!canProceed()" @click="next">
          {{ isLast ? '提交' : '下一步' }}
        </NButton>
      </footer>
    </section>
  </Transition>
</template>

<style scoped>
.question-dock {
  flex: 0 0 auto;
  margin: 0 var(--space-3) var(--space-2);
  max-height: min(38vh, 360px);
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  padding: var(--space-2) var(--space-3);
  background: var(--surface-1);
  border: 1px solid var(--border-default);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-sm);
}
.qmodal-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-2);
  flex-shrink: 0;
}
.qmodal-title-wrap {
  min-width: 0;
  display: flex;
  align-items: center;
  gap: var(--space-2);
  flex-wrap: wrap;
}
.qmodal-title {
  font-size: 12.5px;
  font-weight: 600;
  color: var(--text-primary);
}
.qmodal-header-icon { color: var(--brand-500); }
.qmodal-progress {
  display: inline-flex;
  align-items: center;
  height: 18px;
  padding: 0 6px;
  border-radius: var(--radius-pill);
  background: var(--surface-2);
  border: 1px solid var(--border-subtle);
  color: var(--text-tertiary);
  font: 500 11px/1 var(--font-mono);
}
.qmulti {
  display: inline-flex;
  align-items: center;
  height: 18px;
  padding: 0 6px;
  border-radius: var(--radius-pill);
  background: var(--brand-50);
  color: var(--brand-500);
  font-size: 10.5px;
  font-weight: 600;
  line-height: 1;
}
.qnav {
  display: flex;
  gap: 4px;
  flex-wrap: wrap;
  flex-shrink: 0;
}
.qnav-chip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  max-width: 120px;
  height: 22px;
  padding: 0 8px;
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-pill);
  background: transparent;
  color: var(--text-tertiary);
  font-family: inherit;
  font-size: 11px;
  line-height: 1;
  cursor: pointer;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  transition:
    color var(--dur-fast) var(--ease-out),
    border-color var(--dur-fast) var(--ease-out),
    background var(--dur-fast) var(--ease-out);
}
.qnav-chip:hover {
  border-color: var(--border-default);
  color: var(--text-secondary);
}
.qnav-chip.qnav-active {
  border-color: var(--brand-100);
  background: var(--brand-50);
  color: var(--brand-600);
}
.qnav-dot {
  width: 5px;
  height: 5px;
  border-radius: 50%;
  background: var(--border-default);
  flex-shrink: 0;
}
.qnav-answered .qnav-dot,
.qnav-active .qnav-dot {
  background: currentColor;
}
.qbody {
  min-height: 0;
  flex: 1 1 auto;
  overflow: auto;
  overscroll-behavior: contain;
}
.qprompt {
  margin: 0 0 var(--space-2);
  color: var(--text-primary);
  font-size: 13px;
  font-weight: 600;
  line-height: 1.4;
}

/* Dense option grid — single-line chips; desc via title tooltip. */
.qopts {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(140px, 1fr));
  gap: 6px;
}
.qopt {
  display: flex;
  align-items: center;
  gap: 6px;
  min-width: 0;
  min-height: 32px;
  padding: 4px 8px;
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  background: var(--surface-2);
  color: var(--text-primary);
  font-family: inherit;
  text-align: left;
  cursor: pointer;
  transition:
    border-color var(--dur-fast) var(--ease-out),
    background var(--dur-fast) var(--ease-out);
}
.qopt:hover {
  border-color: var(--border-default);
  background: var(--surface-3);
}
.qopt-sel {
  border-color: var(--brand-100);
  background: var(--brand-50);
}
.qopt-ctrl {
  flex-shrink: 0;
  pointer-events: none;
}
.qopt-label {
  min-width: 0;
  overflow: hidden;
  font-size: 12.5px;
  font-weight: 500;
  line-height: 1.3;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.qopt-sel .qopt-label { font-weight: 600; }

.qcustom {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  margin-top: 6px;
  min-height: 32px;
  padding: 4px 8px;
  border: 1px dashed var(--border-default);
  border-radius: var(--radius-sm);
  background: var(--surface-2);
  cursor: pointer;
  transition:
    border-color var(--dur-fast) var(--ease-out),
    background var(--dur-fast) var(--ease-out);
}
.qcustom:hover {
  border-color: var(--border-strong);
  background: var(--surface-3);
}
.qcustom.qcustom-sel {
  border-color: var(--brand-100);
  border-style: solid;
  background: var(--brand-50);
}
.qcustom-toggle {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  flex-shrink: 0;
  padding: 0;
  border: 0;
  background: transparent;
  color: var(--text-primary);
  font-family: inherit;
  cursor: pointer;
}
.qcustom-label {
  font-size: 12.5px;
  font-weight: 500;
  white-space: nowrap;
}
.qcustom-input {
  flex: 1;
  min-width: 0;
}
.qmodal-footer {
  display: flex;
  justify-content: flex-end;
  align-items: center;
  gap: var(--space-2);
  padding-top: var(--space-2);
  border-top: 1px solid var(--border-subtle);
  flex-shrink: 0;
}
@media (max-width: 720px) {
  .question-dock {
    margin-inline: var(--space-2);
    max-height: min(46vh, 400px);
  }
  .qopts {
    grid-template-columns: 1fr;
  }
}
.question-dock-enter-active {
  transition:
    opacity var(--dur-base) var(--ease-out),
    transform var(--dur-base) var(--ease-out);
}
.question-dock-leave-active {
  transition:
    opacity var(--dur-fast) var(--ease-in),
    transform var(--dur-fast) var(--ease-in);
}
.question-dock-enter-from,
.question-dock-leave-to {
  opacity: 0;
  transform: translateY(var(--space-3));
}
</style>
