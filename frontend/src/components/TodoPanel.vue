<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { NScrollbar } from 'naive-ui'
import { ChevronRight } from './icons'
import {
  currentTodos,
  currentSessionWorking,
  currentPendingQuestion,
  state,
  clearSessionTodos,
} from '../stores/chat'
import type { TodoItem } from '../api/client'

type DockState = 'hide' | 'clear' | 'open' | 'close'

const expanded = ref(false)
const dockVisible = ref(false)
const closing = ref(false)
let closeTimer: ReturnType<typeof setTimeout> | null = null

const visibleTodos = computed(() => currentTodos.value)
const activeTodos = computed(() => visibleTodos.value.filter(t => t.status !== 'done' && t.status !== 'cancelled'))
const doneTodos = computed(() => visibleTodos.value.filter(t => t.status === 'done'))
const cancelledTodos = computed(() => visibleTodos.value.filter(t => t.status === 'cancelled'))

const total = computed(() => visibleTodos.value.length)
const doneCount = computed(() => doneTodos.value.length)
const progressLabel = computed(() => `${doneCount.value} / ${total.value}`)
const isLive = computed(() => currentSessionWorking.value)
const hasPendingQuestion = computed(() => !!currentPendingQuestion.value)

const allDone = computed(() => {
  const list = visibleTodos.value
  return list.length > 0 && list.every(t => t.status === 'done' || t.status === 'cancelled')
})

function todoState(input: { count: number; done: boolean; live: boolean }): DockState {
  if (input.count === 0) return 'hide'
  if (!input.live && input.done) return 'clear'
  if (!input.live) return 'open'
  if (!input.done) return 'open'
  return 'close'
}

function clearCloseTimer() {
  if (!closeTimer) return
  clearTimeout(closeTimer)
  closeTimer = null
}

function scheduleClose(ms = 1200) {
  clearCloseTimer()
  closeTimer = setTimeout(() => {
    closing.value = false
    dockVisible.value = false
    closeTimer = null
  }, ms)
}

watch(
  [() => state.currentID, () => total.value, () => isLive.value, () => allDone.value, () => hasPendingQuestion.value],
  () => {
    if (hasPendingQuestion.value) {
      expanded.value = false
    }
    const id = state.currentID
    if (!id) {
      dockVisible.value = false
      closing.value = false
      return
    }
    const next = todoState({ count: total.value, done: allDone.value, live: isLive.value })
    if (next === 'hide') {
      clearCloseTimer()
      dockVisible.value = false
      closing.value = false
      return
    }
    if (next === 'clear') {
      clearCloseTimer()
      clearSessionTodos(id)
      dockVisible.value = false
      closing.value = false
      return
    }
    if (next === 'open') {
      clearCloseTimer()
      dockVisible.value = true
      closing.value = false
      return
    }
    dockVisible.value = true
    closing.value = true
    scheduleClose()
  },
  { immediate: true },
)

const active = computed<TodoItem | undefined>(() => {
  const list = visibleTodos.value
  if (list.length === 0) return undefined
  return list.find(t => t.status === 'in_progress')
    ?? list.find(t => t.status === 'pending')
    ?? list[0]
})

function statusLabel(status: string): string {
  switch (status) {
    case 'in_progress': return '进行中'
    case 'done': return '已完成'
    case 'cancelled': return '已取消'
    default: return '待处理'
  }
}

function toggleExpand() {
  if (hasPendingQuestion.value) {
    expanded.value = false
    return
  }
  expanded.value = !expanded.value
}
</script>

<template>
  <div
    v-if="dockVisible"
    class="todo-dock"
    :class="{
      'todo-dock--expanded': expanded,
      'todo-dock--closing': closing,
      'todo-dock--question-pending': hasPendingQuestion,
    }"
  >
    <div class="todo-dock-header" @click="toggleExpand" :title="expanded ? '点击收起' : '点击展开'">
      <ChevronRight :size="12" class="todo-dock-caret" :class="{ 'todo-dock-caret--open': expanded }" />
      <span v-if="active" class="todo-dock-active">
        <span v-if="active.status === 'in_progress'" class="todo-dock-pulse" aria-hidden="true" />
        <span class="todo-dock-active-text">{{ active.content }}</span>
      </span>
      <span v-else class="todo-dock-active todo-dock-active--empty">暂无任务</span>
      <span class="todo-dock-progress">{{ progressLabel }}</span>
    </div>

    <NScrollbar v-if="expanded" class="todo-dock-scroll">
      <div class="todo-dock-list">
        <div
          v-for="t in activeTodos"
          :key="t.id"
          class="todo-row"
          :class="`todo-row--${t.status}`"
        >
          <span
            class="todo-row-mark"
            :class="`todo-row-mark--${t.status}`"
            :title="`状态由 LLM 控制 · 当前: ${statusLabel(t.status)}`"
            aria-hidden="true"
          >
            <span v-if="t.status === 'in_progress'" class="todo-row-pulse" />
            <svg v-else viewBox="0 0 16 16" width="14" height="14">
              <circle cx="8" cy="8" r="6.5" fill="none" stroke="currentColor" stroke-width="1.2" opacity="0.45" />
            </svg>
          </span>
          <span
            class="todo-row-text"
            :class="{ 'todo-row-text--done': t.status === 'done' }"
            :title="t.content"
          >{{ t.content }}</span>
          <span class="todo-row-status" :class="`todo-row-status--${t.status}`">
            {{ statusLabel(t.status) }}
          </span>
        </div>

        <div v-if="doneTodos.length > 0" class="todo-section-label">已完成</div>
        <div
          v-for="t in doneTodos"
          :key="'d-' + t.id"
          class="todo-row todo-row--done"
        >
          <span
            class="todo-row-mark todo-row-mark--done"
            :title="`状态由 LLM 控制 · 当前: ${statusLabel(t.status)}`"
            aria-hidden="true"
          >
            <svg viewBox="0 0 16 16" width="14" height="14">
              <circle cx="8" cy="8" r="7" fill="currentColor" opacity="0.15" />
              <path d="M4 8.5l2.8 2.8L12 5.5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
            </svg>
          </span>
          <span class="todo-row-text todo-row-text--done" :title="t.content">
            {{ t.content }}
          </span>
        </div>

        <div v-if="cancelledTodos.length > 0" class="todo-section-label">已取消</div>
        <div
          v-for="t in cancelledTodos"
          :key="'c-' + t.id"
          class="todo-row todo-row--cancelled"
        >
          <span
            class="todo-row-mark todo-row-mark--cancelled"
            :title="`状态由 LLM 控制 · 当前: ${statusLabel(t.status)}`"
            aria-hidden="true"
          >
            <svg viewBox="0 0 16 16" width="14" height="14">
              <circle cx="8" cy="8" r="6.5" fill="none" stroke="currentColor" stroke-width="1.2" opacity="0.45" />
              <path d="M5 5l6 6M11 5l-6 6" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" />
            </svg>
          </span>
          <span class="todo-row-text todo-row-text--cancelled" :title="t.content">{{ t.content }}</span>
        </div>
      </div>
    </NScrollbar>
  </div>
</template>

<style scoped>
.todo-dock {
  border-top: 1px solid var(--border);
  background: var(--bg-2);
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
  max-height: 36px;
  transition: max-height 0.32s cubic-bezier(0.4, 0, 0.2, 1);
  overflow: hidden;
}
.todo-dock--expanded {
  max-height: min(50vh, 320px);
}
.todo-dock--closing {
  max-height: 36px;
}
.todo-dock--question-pending .todo-dock-header {
  cursor: default;
}
.todo-dock--question-pending .todo-dock-caret {
  opacity: 0.45;
}
.todo-dock-header {
  display: flex;
  align-items: center;
  gap: 8px;
  height: 36px;
  padding: 0 12px;
  font-size: 12px;
  color: var(--text-2);
  cursor: pointer;
  user-select: none;
  flex-shrink: 0;
}
.todo-dock-header:hover {
  background: var(--bg-3, rgba(255, 255, 255, 0.04));
}
.todo-dock-caret {
  color: var(--text-tertiary);
  transition: transform var(--dur-fast) var(--ease-out);
  display: inline-flex;
  flex-shrink: 0;
}
.todo-dock-caret--open {
  transform: rotate(90deg);
}
.todo-dock-active {
  flex: 1;
  min-width: 0;
  display: flex;
  align-items: center;
  gap: 8px;
  overflow: hidden;
}
.todo-dock-active-text {
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  color: var(--text-1);
  font-size: 13px;
}
.todo-dock-active--empty {
  color: var(--text-3);
  font-style: italic;
}
.todo-dock-pulse,
.todo-row-pulse {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  background: var(--warn-500);
  flex-shrink: 0;
  animation: todo-pulse 1.2s ease-in-out infinite;
}
@keyframes todo-pulse {
  0%, 100% { opacity: 0.4; transform: scale(0.85); }
  50% { opacity: 1; transform: scale(1.15); }
}
.todo-dock-progress {
  flex-shrink: 0;
  font-size: 11px;
  color: var(--text-3);
  font-variant-numeric: tabular-nums;
  background: var(--bg-3, rgba(255, 255, 255, 0.06));
  padding: 2px 8px;
  border-radius: 10px;
}
.todo-dock-scroll {
  flex: 1;
  min-height: 0;
}
.todo-dock-scroll :deep(.n-scrollbar-content) {
  padding: 4px 12px 10px;
}
.todo-dock-list {
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.todo-row {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 4px 4px;
  font-size: 13px;
  border-radius: 4px;
  min-width: 0;
}
.todo-row:hover {
  background: var(--bg-3, rgba(255, 255, 255, 0.04));
}
.todo-row--done {
  opacity: 0.7;
}
.todo-row--cancelled {
  opacity: 0.5;
}
.todo-row-mark {
  flex-shrink: 0;
  width: 14px;
  height: 14px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  cursor: default;
  user-select: none;
}
.todo-row-mark svg {
  display: block;
}
.todo-row-mark--pending { color: var(--text-tertiary); }
.todo-row-mark--in_progress { color: var(--warn-500); }
.todo-row-mark--done { color: var(--success-500); }
.todo-row-mark--cancelled { color: var(--text-quaternary); }
.todo-row:hover .todo-row-mark { color: inherit; }
.todo-row-text {
  flex: 1;
  min-width: 0;
  color: var(--text-1);
  cursor: default;
  word-break: break-word;
  line-height: 1.45;
}
.todo-row-text--done {
  color: var(--text-3);
  text-decoration: line-through;
}
.todo-row-text--cancelled {
  color: var(--text-4);
  text-decoration: line-through;
}
.todo-row-status {
  flex-shrink: 0;
  font-size: 10px;
  padding: 1px 6px;
  border-radius: 8px;
  background: var(--bg-3, rgba(255, 255, 255, 0.08));
  color: var(--text-3);
}
.todo-row-status--in_progress {
  background: var(--warn-50);
  color: var(--warn-500);
}
.todo-row-status--done {
  background: var(--success-50);
  color: var(--success-500);
}
.todo-row-status--cancelled {
  background: var(--surface-3);
  color: var(--text-quaternary);
}
.todo-section-label {
  font-size: 10px;
  color: var(--text-4);
  text-transform: uppercase;
  letter-spacing: 0.5px;
  padding: 8px 4px 2px;
}
</style>
