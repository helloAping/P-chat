<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { NButton, NRadio, NCheckbox, NTag, NInput } from 'naive-ui'
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

const displayOptions = computed(() => {
  const q = currentQuestion.value
  if (!q) return []
  return [
    ...q.options.map(opt => ({ ...opt, value: opt.label, isCustom: false })),
    { label: '自定义', description: '输入自己的答案', value: CUSTOM_VALUE, isCustom: true },
  ]
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
          <MessageSquare :size="18" class="qmodal-header-icon" />
          <div class="qmodal-title-copy">
            <span id="qmodal-title">LLM 的提问</span>
            <span class="qmodal-subtitle">回答前仍可滚动查看上方上下文</span>
          </div>
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

      <div class="qnav">
        <NTag
          v-for="(q, i) in questions"
          :key="i"
          :type="i === currentIndex ? 'primary' : 'default'"
          size="small"
          class="qnav-tag"
          :class="{ 'qnav-answered': answers[q.question] || (q.multi_select && multiAnswers[q.question]?.length) }"
          @click="currentIndex = i"
        >
          {{ q.header }}
        </NTag>
      </div>

      <div v-if="currentQuestion" class="qbody">
        <div class="qtext">
          {{ currentQuestion.question }}
          <span v-if="currentQuestion.multi_select" class="qmulti">（多选）</span>
        </div>
        <div class="qopts">
          <div
            v-for="opt in displayOptions"
            :key="opt.value"
            class="qopt"
            :class="{ 'qopt-sel': isSelected(opt.value), 'qopt-custom-row': opt.isCustom }"
            @click="selectOption(opt.value)"
          >
            <NRadio
              v-if="!currentQuestion.multi_select"
              :checked="isSelected(opt.value)"
              class="qopt-radio"
            />
            <NCheckbox
              v-else
              :checked="isSelected(opt.value)"
              class="qopt-check"
            />
            <div class="qopt-body">
              <div class="qopt-label">{{ opt.label }}</div>
              <div class="qopt-desc">{{ opt.description }}</div>
              <div
                v-if="opt.isCustom && isSelected(opt.value)"
                class="qopt-custom"
                @click.stop
              >
                <NInput
                  v-model:value="customInputs[currentQuestion.question]"
                  size="small"
                  placeholder="请输入自定义内容..."
                  :autofocus="true"
                />
              </div>
            </div>
          </div>
        </div>
      </div>

      <footer class="qmodal-footer">
        <NButton v-if="currentIndex > 0" @click="prev" size="small">上一步</NButton>
        <NButton type="primary" @click="next" :disabled="!canProceed()" size="small">
          {{ isLast ? '提交' : '下一步' }}
        </NButton>
      </footer>
    </section>
  </Transition>
</template>

<style scoped>
.question-dock {
  flex: 0 0 auto;
  margin: 0 var(--space-4) var(--space-2);
  max-height: min(42vh, 420px);
  display: flex;
  flex-direction: column;
  padding: var(--space-4);
  background: var(--surface-1);
  border: 1px solid var(--border-default);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-md);
}
.qmodal-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-3);
  flex-shrink: 0;
}
.qmodal-title-wrap {
  min-width: 0;
  display: flex;
  align-items: center;
  gap: var(--space-2);
}
.qmodal-title-copy {
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 1px;
  font-size: 16px;
  font-weight: 600;
  color: var(--text-primary);
}
.qmodal-header-icon { color: var(--brand-500); }
.qmodal-subtitle {
  color: var(--text-tertiary);
  font-size: 11.5px;
  font-weight: 400;
  line-height: 1.35;
}
.qnav {
  display: flex;
  gap: var(--space-2);
  flex-wrap: wrap;
  margin: var(--space-3) 0;
  flex-shrink: 0;
}
.qnav-tag {
  cursor: pointer;
  opacity: 0.6;
}
.qnav-answered {
  opacity: 1;
}
.qbody {
  min-height: 0;
  overflow: auto;
  padding-right: var(--space-1);
}
.qtext {
  font-size: 14px;
  font-weight: 600;
  color: var(--text-primary);
  margin-bottom: var(--space-3);
}
.qmulti {
  font-weight: 400;
  color: var(--text-tertiary);
  font-size: 12px;
}
.qopts {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}
.qopt {
  display: flex;
  align-items: flex-start;
  gap: var(--space-3);
  padding: var(--space-3);
  border: 1px solid var(--border-default);
  border-radius: var(--radius-md);
  cursor: pointer;
  transition:
    border-color var(--dur-fast) var(--ease-out),
    background var(--dur-fast) var(--ease-out);
}
.qopt:hover,
.qopt-sel {
  border-color: var(--brand-500);
  background: var(--brand-50);
}
.qopt-custom-row {
  border-style: dashed;
}
.qopt-radio,
.qopt-check {
  flex-shrink: 0;
  margin-top: 1px;
}
.qopt-body {
  flex: 1;
  min-width: 0;
}
.qopt-label {
  font-size: 14px;
  font-weight: 500;
  color: var(--text-primary);
}
.qopt-desc {
  font-size: 12px;
  color: var(--text-tertiary);
  margin-top: 2px;
}
.qopt-custom {
  margin-top: var(--space-2);
}
.qmodal-footer {
  display: flex;
  justify-content: flex-end;
  gap: var(--space-2);
  margin-top: var(--space-3);
  flex-shrink: 0;
}
.question-dock-enter-active,
.question-dock-leave-active {
  transition:
    opacity var(--dur-base) var(--ease-out),
    transform var(--dur-base) var(--ease-out);
}
.question-dock-enter-from,
.question-dock-leave-to {
  opacity: 0;
  transform: translateY(var(--space-3));
}
</style>
