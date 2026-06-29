<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { NModal, NButton, NRadio, NCheckbox, NSpace, NTag, NScrollbar } from 'naive-ui'
import { state, submitQuestionAnswer } from '../stores/chat'
import type { QuestionItem } from '../api/client'

const currentIndex = ref(0)
const answers = ref<Record<string, string>>({})
const multiAnswers = ref<Record<string, string[]>>({})

const questions = computed(() => state.pendingQuestion?.questions || [])
const currentQuestion = computed(() => questions.value[currentIndex.value] || null)
const isLast = computed(() => currentIndex.value >= questions.value.length - 1)

watch(() => state.pendingQuestion, (q) => {
  if (q) {
    currentIndex.value = 0
    answers.value = {}
    multiAnswers.value = {}
  }
})

function selectOption(value: string) {
  if (!currentQuestion.value) return
  const key = currentQuestion.value.question
  if (currentQuestion.value.multi_select) {
    const arr = multiAnswers.value[key] || []
    const idx = arr.indexOf(value)
    if (idx >= 0) arr.splice(idx, 1)
    else arr.push(value)
    multiAnswers.value[key] = [...arr]
  } else {
    answers.value[key] = value
  }
}

function isSelected(value: string): boolean {
  if (!currentQuestion.value) return false
  const key = currentQuestion.value.question
  if (currentQuestion.value.multi_select) {
    return (multiAnswers.value[key] || []).includes(value)
  }
  return answers.value[key] === value
}

function canProceed(): boolean {
  if (!currentQuestion.value) return false
  const key = currentQuestion.value.question
  if (currentQuestion.value.multi_select) {
    return (multiAnswers.value[key] || []).length > 0
  }
  return !!answers.value[key]
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
    if (q.multi_select) {
      all[key] = (multiAnswers.value[key] || []).join(', ')
    } else {
      all[key] = answers.value[key] || ''
    }
  }
  submitQuestionAnswer(all)
}
</script>

<template>
  <NModal
    v-if="state.pendingQuestion"
    :show="true"
    preset="card"
    :closable="false"
    :mask-closable="false"
    title="💬 LLM 的提问"
    style="max-width: 560px"
  >
    <div class="qnav">
      <NTag
        v-for="(q, i) in questions"
        :key="i"
        :type="i === currentIndex ? 'primary' : 'default'"
        size="small"
        class="qnav-tag"
        :class="{ 'qnav-answered': i < currentIndex || answers[q.question] || (q.multi_select && multiAnswers[q.question]?.length) }"
        @click="currentIndex = i"
      >
        {{ q.header }}
      </NTag>
    </div>
    <div v-if="currentQuestion" class="qbody">
      <div class="qtext">{{ currentQuestion.question }}<span v-if="currentQuestion.multi_select" class="qmulti"> (多选)</span></div>
      <div class="qopts">
        <div
          v-for="opt in currentQuestion.options"
          :key="opt.label"
          class="qopt"
          :class="{ 'qopt-sel': isSelected(opt.label) }"
          @click="selectOption(opt.label)"
        >
          <NRadio
            v-if="!currentQuestion.multi_select"
            :checked="isSelected(opt.label)"
            class="qopt-radio"
          />
          <NCheckbox
            v-else
            :checked="isSelected(opt.label)"
            class="qopt-check"
          />
          <div class="qopt-body">
            <div class="qopt-label">{{ opt.label }}</div>
            <div class="qopt-desc">{{ opt.description }}</div>
          </div>
        </div>
      </div>
    </div>
    <template #footer>
      <NSpace justify="end">
        <NButton v-if="currentIndex > 0" @click="prev" size="small">上一步</NButton>
        <NButton type="primary" @click="next" :disabled="!canProceed()" size="small">
          {{ isLast ? '提交' : '下一步' }}
        </NButton>
      </NSpace>
    </template>
  </NModal>
</template>

<style scoped>
.qnav {
  display: flex;
  gap: 6px;
  flex-wrap: wrap;
  margin-bottom: 16px;
}
.qnav-tag {
  cursor: pointer;
  opacity: 0.6;
}
.qnav-answered {
  opacity: 1;
}
.qbody {
  min-height: 120px;
}
.qtext {
  font-size: 14px;
  font-weight: 600;
  color: var(--text-1);
  margin-bottom: 12px;
}
.qmulti {
  font-weight: 400;
  color: var(--text-3);
  font-size: 12px;
}
.qopts {
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.qopt {
  display: flex;
  align-items: flex-start;
  gap: 10px;
  padding: 10px 12px;
  border: 1px solid var(--border);
  border-radius: 8px;
  cursor: pointer;
  transition: border-color .15s, background .15s;
}
.qopt:hover {
  border-color: var(--accent);
  background: var(--bg-2);
}
.qopt-sel {
  border-color: var(--accent);
  background: var(--bg-2);
}
.qopt-radio, .qopt-check {
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
  color: var(--text-1);
}
.qopt-desc {
  font-size: 12px;
  color: var(--text-3);
  margin-top: 2px;
}
</style>
