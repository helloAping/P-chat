<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { NInput, NTooltip } from 'naive-ui'
import { submitQuestionAnswer, currentPendingQuestion } from '../stores/chat'

const CUSTOM_VALUE = '__pchat_custom__'

type DisplayOption = {
  label: string
  description: string
  value: string
  recommended: boolean
  free: boolean
}

const currentIndex = ref(0)
const collapsed = ref(false)
const answers = ref<Record<string, string>>({})
const multiAnswers = ref<Record<string, string[]>>({})
const customInputs = ref<Record<string, string>>({})

const questions = computed(() => currentPendingQuestion.value?.questions || [])
const currentQuestion = computed(() => questions.value[currentIndex.value] || null)
const isLast = computed(() => currentIndex.value >= questions.value.length - 1)

function questionSignature(q: { questions?: { header?: string; question?: string }[] } | null): string {
  if (!q?.questions) return ''
  return q.questions.map((item) => `${item.header || ''}\n${item.question || ''}`).join('\n---\n')
}

watch(currentPendingQuestion, (q, prev) => {
  if (!q) return
  // A repeated prompt for the same questions must not pop the panel
  // back open after the user has collapsed it.
  if (questionSignature(q) === questionSignature(prev)) return
  currentIndex.value = 0
  collapsed.value = false
  answers.value = {}
  multiAnswers.value = {}
  customInputs.value = {}
})

function keyOfQuestion(): string {
  return currentQuestion.value?.question || ''
}

/** LLM 习惯把「（推荐）」写在 label 末尾；拆出来渲染成小字，正文不再重复。 */
function splitLabel(label: string): { text: string; recommended: boolean } {
  const matched = (label || '').match(/^(.*?)\s*[（(]\s*推荐\s*[)）]\s*$/)
  if (matched && matched[1]) return { text: matched[1].trim(), recommended: true }
  return { text: label, recommended: false }
}

function isFreeTextLabel(label: string): boolean {
  const text = splitLabel(label).text
  return text === '其他' || text === '自定义'
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
    if ((value === CUSTOM_VALUE || isFreeTextLabel(value)) && idx >= 0) {
      customInputs.value[key] = ''
    }
    multiAnswers.value[key] = arr
    return
  }
  if (answers.value[key] === value) {
    answers.value[key] = ''
    if (value === CUSTOM_VALUE || isFreeTextLabel(value)) customInputs.value[key] = ''
  } else {
    answers.value[key] = value
  }
}

function isSelected(value: string): boolean {
  const q = currentQuestion.value
  if (!q) return false
  const key = keyOfQuestion()
  if (q.multi_select) return (multiAnswers.value[key] || []).includes(value)
  return answers.value[key] === value
}

function selectedValues(): string[] {
  const q = currentQuestion.value
  if (!q) return []
  const key = keyOfQuestion()
  if (q.multi_select) return multiAnswers.value[key] || []
  return answers.value[key] ? [answers.value[key]] : []
}

function needsCustomText(): boolean {
  return selectedValues().some((value) => value === CUSTOM_VALUE || isFreeTextLabel(value))
}

function canProceed(): boolean {
  const selected = selectedValues().filter(Boolean)
  if (selected.length === 0) return false
  if (needsCustomText() && !(customInputs.value[keyOfQuestion()] || '').trim()) return false
  return true
}

function next() {
  if (!canProceed()) return
  if (isLast.value) submit()
  else currentIndex.value++
}

function prev() {
  if (currentIndex.value > 0) currentIndex.value--
}

function answerFor(key: string, multi: boolean): string {
  if (multi) {
    return (multiAnswers.value[key] || []).map((item) => {
      if (item === CUSTOM_VALUE || isFreeTextLabel(item)) return (customInputs.value[key] || '').trim()
      return item
    }).filter(Boolean).join(', ')
  }
  const raw = answers.value[key] || ''
  if (raw === CUSTOM_VALUE || isFreeTextLabel(raw)) return (customInputs.value[key] || '').trim()
  return raw
}

function submit() {
  const all: Record<string, string> = {}
  for (const q of questions.value) {
    const value = answerFor(q.question, !!q.multi_select)
    if (value) all[q.header] = value
  }
  submitQuestionAnswer(all)
}

const optionGroups = computed(() => {
  const q = currentQuestion.value
  if (!q) return [] as { on: boolean; items: DisplayOption[] }[]
  const rows: DisplayOption[] = q.options.map((opt) => {
    const split = splitLabel(opt.label)
    return {
      label: split.text,
      description: opt.description || '',
      value: opt.label,
      recommended: split.recommended,
      free: isFreeTextLabel(opt.label),
    }
  })
  if (!rows.some((row) => row.free)) {
    rows.push({
      label: '其他',
      description: '没有合适选项时自己写一句',
      value: CUSTOM_VALUE,
      recommended: false,
      free: true,
    })
  }
  const groups: { on: boolean; items: DisplayOption[] }[] = []
  for (const row of rows) {
    const on = isSelected(row.value)
    const prev = groups[groups.length - 1]
    if (prev && prev.on === on) prev.items.push(row)
    else groups.push({ on, items: [row] })
  }
  return groups
})

const titleText = computed(() => currentQuestion.value?.header || currentQuestion.value?.question || '')
const promptText = computed(() => {
  const q = currentQuestion.value
  if (!q) return ''
  const text = (q.question || '').trim()
  if (!text || text === q.header) return ''
  return text
})
</script>

<template>
  <button
    v-if="currentPendingQuestion && collapsed"
    type="button"
    class="question-float"
    aria-label="展开确认"
    @click.stop="collapsed = false"
  >
    <span class="question-float-mark">?</span>
    <span class="question-particles" aria-hidden="true">
      <i v-for="n in 8" :key="n" :style="{ '--i': String(n - 1) }" />
    </span>
  </button>

  <section
    v-if="currentPendingQuestion && !collapsed && currentQuestion"
    class="question-panel"
    role="region"
    aria-labelledby="qmodal-title"
  >
    <header class="qmodal-header">
      <span class="qmodal-mark" aria-hidden="true">?</span>
      <span id="qmodal-title" class="qmodal-title">需要你确认</span>
      <span class="qmodal-progress">{{ currentIndex + 1 }} / {{ questions.length }}</span>
      <button type="button" class="qmodal-collapse" @click.stop="collapsed = true">收起</button>
    </header>

    <div class="qbody">
      <div class="qtitle-row">
        <div id="qmodal-question" class="qtitle">{{ titleText }}</div>
        <span v-if="currentQuestion.multi_select" class="qmulti">可多选</span>
      </div>
      <p v-if="promptText" class="qprompt" :title="promptText">{{ promptText }}</p>

      <div class="qgroups">
        <div
          v-for="(group, groupIndex) in optionGroups"
          :key="groupIndex"
          class="qgroup"
          :class="{ 'qgroup-on': group.on }"
        >
          <div v-for="(opt, optIndex) in group.items" :key="opt.value">
            <button
              type="button"
              class="qopt"
              :class="{ 'qopt-split': group.on && optIndex > 0 }"
              :title="opt.description ? `${opt.label}\n${opt.description}` : opt.label"
              @click="selectOption(opt.value)"
            >
              <span class="qcheck" :class="{ 'qcheck-on': group.on, 'qcheck-round': !currentQuestion.multi_select }" />
              <span class="qopt-copy">
                <span class="qopt-label">
                  <span class="qopt-label-text">{{ opt.label }}</span>
                  <span v-if="opt.recommended" class="qopt-rec">推荐</span>
                </span>
                <NTooltip v-if="opt.description" placement="top-start">
                  <template #trigger>
                    <span class="qopt-desc">{{ opt.description }}</span>
                  </template>
                  {{ opt.description }}
                </NTooltip>
              </span>
            </button>
            <div v-if="group.on && opt.free" class="qcustom" @click.stop>
              <NInput
                v-model:value="customInputs[currentQuestion.question]"
                size="small"
                :placeholder="opt.label === '自定义' ? '输入自定义内容' : '输入其他内容'"
                :autofocus="true"
              />
            </div>
          </div>
        </div>
      </div>
    </div>

    <footer class="qmodal-footer">
      <button type="button" class="qnav-btn" :disabled="currentIndex === 0" @click="prev">上一题</button>
      <button type="button" class="qnav-btn qnav-btn-primary" :disabled="!canProceed()" @click="next">
        {{ isLast ? '确认' : '下一题 →' }}
      </button>
    </footer>
  </section>
</template>

<style scoped>
.question-float {
  position: absolute;
  left: var(--space-3);
  bottom: calc(100% + var(--space-1) + var(--control-height) - 4px + var(--space-2));
  z-index: 6;
  width: 28px;
  height: 28px;
  margin: 0;
  padding: 0;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  background: var(--surface-1);
  color: var(--text-secondary);
  font: inherit;
  font-size: 14px;
  line-height: 1;
  cursor: pointer;
  pointer-events: auto;
}
.question-float-mark { position: relative; z-index: 1; }
.question-float::before,
.question-float::after {
  content: "";
  position: absolute;
  inset: -1px;
  border-radius: inherit;
  border: 1px solid var(--question-green);
  pointer-events: none;
  animation: question-edge 1.8s ease-out infinite;
}
.question-float::after { animation-delay: 0.9s; }
.question-particles {
  position: absolute;
  inset: 0;
  pointer-events: none;
}
.question-particles i {
  position: absolute;
  left: 50%;
  top: 50%;
  width: 3px;
  height: 3px;
  margin: -1.5px 0 0 -1.5px;
  border-radius: 50%;
  background: var(--question-green);
  animation: question-particle 1.8s ease-out infinite;
  animation-delay: calc(var(--i) * -0.22s);
}
@keyframes question-edge {
  0% { transform: scale(1); opacity: 0.85; }
  100% { transform: scale(1.7); opacity: 0; }
}
@keyframes question-particle {
  0% { transform: rotate(calc(var(--i) * 45deg)) translateY(12px); opacity: 0.95; }
  100% { transform: rotate(calc(var(--i) * 45deg)) translateY(22px); opacity: 0; }
}

.question-panel {
  position: absolute;
  left: var(--space-3);
  /* Sit above the composer capsules. The toolbar is already
     offset by one gap; add its height so the card floats over
     the message list instead of pushing that row upward. */
  bottom: calc(100% + var(--space-1) + var(--control-height) - 4px + var(--space-2));
  z-index: 4;
  width: min(420px, calc(100% - var(--space-6)));
  margin: 0;
  display: flex;
  flex-direction: column;
  max-height: min(46vh, 420px);
  background: var(--surface-1);
  color: var(--text-primary);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-lg);
  overflow: hidden;
  pointer-events: auto;
}
.qmodal-header {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  padding: 8px 12px;
}
.qmodal-mark {
  width: 22px;
  height: 22px;
  flex: 0 0 auto;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  color: var(--text-tertiary);
  font-size: 13px;
}
.qmodal-title {
  font-size: 14px;
  font-weight: 600;
}
.qmodal-progress {
  margin-left: auto;
  padding: 3px 8px;
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-pill);
  color: var(--text-tertiary);
  font-size: 12px;
  line-height: 1;
}
.qmodal-collapse {
  margin: 0;
  height: 24px;
  padding: 0 8px;
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  background: var(--surface-3);
  color: var(--text-primary);
  font: inherit;
  font-size: 12px;
  cursor: pointer;
}
.qbody {
  min-height: 0;
  overflow: auto;
  padding: 2px 12px 6px;
  border-top: 1px solid var(--border-subtle);
}
.qtitle-row {
  display: flex;
  align-items: baseline;
  gap: var(--space-2);
  margin: 8px 0 4px;
}
.qtitle {
  min-width: 0;
  overflow: hidden;
  font-size: 14px;
  font-weight: 600;
  line-height: 1.35;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.qmulti,
.qopt-rec {
  color: var(--text-tertiary);
  font-size: 12px;
  font-weight: 500;
}
.qopt-rec { color: var(--question-green-ink); }
.qprompt {
  display: -webkit-box;
  margin: 0 0 6px;
  overflow: hidden;
  color: var(--text-tertiary);
  font-size: 12px;
  line-height: 1.35;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
}
.qgroups {
  display: flex;
  flex-direction: column;
  gap: 4px;
}
.qgroup-on {
  background: var(--surface-3);
  border-radius: var(--radius-sm);
  overflow: hidden;
}
.qopt {
  display: flex;
  align-items: flex-start;
  gap: 8px;
  width: 100%;
  margin: 0;
  padding: 6px 8px;
  border: none;
  background: transparent;
  color: var(--text-primary);
  font: inherit;
  font-size: 13px;
  text-align: left;
  cursor: pointer;
}
.qopt-split { border-top: 1px solid var(--border-subtle); }
.qcheck {
  width: 14px;
  height: 14px;
  margin-top: 2px;
  flex: 0 0 auto;
  border: 1.5px solid var(--text-quaternary);
  border-radius: 4px;
  background: transparent;
}
.qcheck-round { border-radius: 99px; }
.qcheck-on {
  position: relative;
  border: none;
  background: var(--question-green);
}
.qcheck-on::after {
  content: "";
  position: absolute;
  left: 3px;
  top: 2px;
  width: 6px;
  height: 3px;
  border-left: 1.5px solid var(--question-on-green);
  border-bottom: 1.5px solid var(--question-on-green);
  transform: rotate(-45deg);
}
.qopt-copy {
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.qopt-label {
  display: flex;
  align-items: flex-start;
  gap: 6px;
  min-width: 0;
  line-height: 1.35;
}
.qopt-label-text {
  display: -webkit-box;
  min-width: 0;
  overflow: hidden;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
}
.qopt-desc {
  display: -webkit-box;
  overflow: hidden;
  color: var(--text-tertiary);
  font-size: 11px;
  line-height: 1.35;
  -webkit-line-clamp: 1;
  -webkit-box-orient: vertical;
}
.qcustom { padding: 0 8px 6px 30px; }
.qmodal-footer {
  display: flex;
  justify-content: space-between;
  gap: var(--space-2);
  padding: 8px 12px;
  border-top: 1px solid var(--border-subtle);
}
.qnav-btn {
  height: var(--control-height);
  margin: 0;
  padding: 0 12px;
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--text-primary);
  font: inherit;
  font-size: 12px;
  cursor: pointer;
}
.qnav-btn:disabled {
  color: var(--text-quaternary);
  cursor: default;
}
.qnav-btn-primary {
  border: none;
  background: var(--question-green);
  color: var(--question-on-green);
  font-weight: 600;
}
.qnav-btn-primary:disabled {
  background: var(--surface-3);
  color: var(--text-quaternary);
}
</style>
