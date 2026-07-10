<script setup lang="ts">
import { ref, computed, watch } from 'vue'
import { NModal, NButton, NRadio, NCheckbox, NSpace, NTag, NScrollbar, NInput } from 'naive-ui'
import { state, submitQuestionAnswer, currentPendingQuestion } from '../stores/chat'
import type { QuestionItem } from '../api/client'
import { MessageSquare } from './icons'

const currentIndex = ref(0)
const answers = ref<Record<string, string>>({})
const multiAnswers = ref<Record<string, string[]>>({})
const customInputs = ref<Record<string, string>>({})

const OTHER_LABEL = '其他'

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

function selectOption(value: string) {
  if (!currentQuestion.value) return
  const key = currentQuestion.value.question
  if (value === OTHER_LABEL) {
    if (currentQuestion.value.multi_select) {
      const arr = multiAnswers.value[key] || []
      const idx = arr.indexOf(OTHER_LABEL)
      if (idx >= 0) {
        arr.splice(idx, 1)
        customInputs.value[key] = ''
      } else {
        arr.push(OTHER_LABEL)
      }
      multiAnswers.value[key] = [...arr]
    } else {
      if (answers.value[key] === OTHER_LABEL) {
        answers.value[key] = ''
        customInputs.value[key] = ''
      } else {
        answers.value[key] = OTHER_LABEL
      }
    }
  } else if (currentQuestion.value.multi_select) {
    const arr = multiAnswers.value[key] || []
    const idx = arr.indexOf(value)
    if (idx >= 0) arr.splice(idx, 1)
    else arr.push(value)
    // Deselect "其他" if any normal option is selected (for single select behavior)
    const otherIdx = arr.indexOf(OTHER_LABEL)
    if (otherIdx >= 0) {
      arr.splice(otherIdx, 1)
      customInputs.value[key] = ''
    }
    multiAnswers.value[key] = [...arr]
  } else {
    if (answers.value[key] === value) {
      answers.value[key] = ''
    } else {
      answers.value[key] = value
    }
    if (value !== OTHER_LABEL) {
      customInputs.value[key] = ''
    }
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
    const selected = multiAnswers.value[key] || []
    if (selected.length === 0) return false
    if (selected.includes(OTHER_LABEL) && !(customInputs.value[key] || '').trim()) return false
    return true
  }
  if (!answers.value[key]) return false
  if (answers.value[key] === OTHER_LABEL && !(customInputs.value[key] || '').trim()) return false
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
    let v = ''
    if (q.multi_select) {
      const selected = (multiAnswers.value[key] || []).map(v2 =>
        v2 === OTHER_LABEL ? (customInputs.value[key] || '其他') : v2
      )
      v = selected.join(', ')
    } else {
      const raw = answers.value[key] || ''
      v = raw === OTHER_LABEL ? (customInputs.value[key] || '其他') : raw
    }
    // Skip questions the user did not actually answer
    // (defensive — canProceed() should already block the
    // submit button in that case, but multi-step flows
    // can race if the user navigates back and clears a
    // previous selection).
    if (!v) continue
    // Use `q.header` as the wire key, NOT `q.question`.
    // The server-side `tool.QuestionResponse.Answers` is
    // a flat map keyed by `header` (the Anthropic API
    // convention the LLM is trained on). The chat store's
    // in-place updater `updateQuestionStatusInParts`
    // also matches by `q.header`. Keying by question
    // text would cause the LLM to receive answers it
    // cannot map back to questions, and the question
    // part in the chat to remain in "等待回答" forever.
    // A duplicate `header` would be a server-side bug;
    // a duplicate `question` text is common (LLM often
    // reuses boilerplate), so header is the safer key.
    all[q.header] = v
  }
  submitQuestionAnswer(all)
}

// Merge predefined options with dynamic "其他" entry
const displayOptions = computed(() => {
  const q = currentQuestion.value
  if (!q) return []
  const key = q.question
  return q.options.map(opt => ({
    ...opt,
    isOther: opt.label === OTHER_LABEL,
    isOtherSelected: q.multi_select
      ? (multiAnswers.value[key] || []).includes(OTHER_LABEL)
      : answers.value[key] === OTHER_LABEL,
  }))
})
</script>

<template>
  <NModal
    v-if="currentPendingQuestion"
    :show="true"
    preset="card"
    :closable="false"
    :mask-closable="false"
    title="LLM 的提问"
    style="max-width: 560px"
  >
    <template #header>
      <div class="qmodal-header">
        <MessageSquare :size="18" class="qmodal-header-icon" />
        <span>LLM 的提问</span>
      </div>
    </template>
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
          v-for="opt in displayOptions"
          :key="opt.label"
          class="qopt"
          :class="{ 'qopt-sel': isSelected(opt.label), 'qopt-other': opt.label === OTHER_LABEL }"
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
            <div
              v-if="opt.label === OTHER_LABEL && opt.isOtherSelected"
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
.qmodal-header {
  display: flex;
  align-items: center;
  gap: 8px;
  font-size: 16px;
  font-weight: 600;
}
.qmodal-header-icon { color: var(--brand-500); }
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
.qopt-other {
  border-style: dashed;
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
.qopt-custom {
  margin-top: 8px;
}
</style>
