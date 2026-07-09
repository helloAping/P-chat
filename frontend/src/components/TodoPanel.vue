<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { NScrollbar, NInput } from 'naive-ui'
import { ChevronRight } from './icons'
import {
  currentTodos,
  currentSessionWorking,
  state,
  appendSystemMessage,
  clearSessionTodos,
} from '../stores/chat'
import type { TodoItem } from '../api/client'

// Opencode-style four-state todo dock.
//
// Why a state machine (not just "show when there are todos"):
// The LLM frequently forgets to write `todos: []` when a turn
// ends. Without `live` awareness, the dock stays visible
// forever, showing stale todos that the LLM never actually
// finished. opencode's solution is to track a `session.status`
// signal ("busy" / "idle") on the client and clear the local
// todo array the moment the session goes idle.
//
// State machine (mirrors `session-composer-state.ts:13-22`):
//
//   count=0                  → "hide"   (nothing to show)
//   count>0  && !live        → "clear"  (stale — wipe local array, then hide)
//   count>0  &&  live && !done → "open"  (working, pending — show)
//   count>0  &&  live &&  done → "close" (working, all done — brief show, then hide)
//
// `live`  = `currentSessionWorking` (derived from the
//           `session_status` SSE event: "busy" sets true,
//           "idle" sets false)
// `done`  = every todo's status is "done" or "cancelled"
//
// `clear` triggers a local wipe via `clearSessionTodos(id)`,
// which is the "stale-clear hack" from
// `session-composer-state.ts:113-118`. The server-side SQLite
// state is NOT touched — reloading the session re-hydrates.
//
// Inner dock state (separate concern):
//   `expanded` — user clicks the header to expand the full
//                list. Independent of the outer state machine.

type DockState = 'hide' | 'clear' | 'open' | 'close'

const expanded = ref(false)

const visibleTodos = computed(() => currentTodos.value)
const activeTodos = computed(() => visibleTodos.value.filter(t => t.status !== 'done' && t.status !== 'cancelled'))
const doneTodos = computed(() => visibleTodos.value.filter(t => t.status === 'done'))
const cancelledTodos = computed(() => visibleTodos.value.filter(t => t.status === 'cancelled'))

const total = computed(() => visibleTodos.value.length)
const doneCount = computed(() => doneTodos.value.length)
const progressLabel = computed(() => `${doneCount.value} / ${total.value}`)

const isLive = computed(() => currentSessionWorking.value)

// "All done" = every todo is done or cancelled (no pending
// or in_progress left). When the LLM finishes its turn with
// `todos: [{...status: "done"}]`, this flips to true and the
// state machine emits "close" (briefly show, then animate
// closed).
const allDone = computed(() => {
  const list = visibleTodos.value
  return list.length > 0 && list.every(t => t.status === 'done' || t.status === 'cancelled')
})

// Pure function — exported for unit tests. Mirrors
// `session-composer-state.ts:13-22`.
function todoState(input: { count: number; done: boolean; live: boolean }): DockState {
  if (input.count === 0) return 'hide'
  if (!input.live) return 'clear'
  if (!input.done) return 'open'
  return 'close'
}

// `dock` reflects whether the outer dock should be visible.
// `closing` is true during the "close" state's brief delay
// before unmount. We keep the dock rendered while closing
// is true so the CSS transition has time to play.
const dockVisible = ref(false)
const closing = ref(false)
let closeTimer: ReturnType<typeof setTimeout> | null = null

function scheduleClose(ms = 1200) {
  if (closeTimer) {
    clearTimeout(closeTimer)
    closeTimer = null
  }
  closeTimer = setTimeout(() => {
    closing.value = false
    dockVisible.value = false
    closeTimer = null
  }, ms)
}

// Reactive driver. Re-runs whenever the upstream state
// (currentTodos, currentSessionWorking) or the local
// `expanded` ref changes. Mirrors opencode's
// `createEffect(on(...))` at `session-composer-state.ts:120-176`.
watch(
  [() => state.currentID, () => total.value, () => isLive.value, () => allDone.value],
  () => {
    const id = state.currentID
    if (!id) {
      dockVisible.value = false
      closing.value = false
      return
    }
    const next = todoState({ count: total.value, done: allDone.value, live: isLive.value })
    if (next === 'hide') {
      if (closeTimer) { clearTimeout(closeTimer); closeTimer = null }
      dockVisible.value = false
      closing.value = false
      return
    }
    if (next === 'clear') {
      // The LLM finished and didn't write `todos: []`. Wipe
      // the local array so the next pass computes `hide`.
      // Mirrors opencode's stale-clear hack.
      if (closeTimer) { clearTimeout(closeTimer); closeTimer = null }
      clearSessionTodos(id)
      dockVisible.value = false
      closing.value = false
      return
    }
    if (next === 'open') {
      if (closeTimer) { clearTimeout(closeTimer); closeTimer = null }
      dockVisible.value = true
      closing.value = false
      return
    }
    // next === 'close': show briefly, then animate closed.
    dockVisible.value = true
    closing.value = true
    scheduleClose()
  },
  { immediate: true },
)

// "Active" todo = first in_progress, else first pending, else
// last completed, else just the first todo. Matches opencode's
// `session-todo-dock.tsx:64-70` so the collapsed preview is the
// most informative single line.
const active = computed<TodoItem | undefined>(() => {
  const list = visibleTodos.value
  if (list.length === 0) return undefined
  return list.find(t => t.status === 'in_progress')
    ?? list.find(t => t.status === 'pending')
    ?? [...list].reverse().find(t => t.status === 'completed')
    ?? list[0]
})

const editingId = ref<string | null>(null)
const editingContent = ref('')

function statusLabel(status: string): string {
  switch (status) {
    case 'in_progress': return '进行中'
    case 'done':        return '已完成'
    case 'cancelled':   return '已取消'
    default:            return '待处理'
  }
}

// Task status is owned by the LLM, not the user. The mark on the
// left of each row is a *display* indicator only — clicking it
// does nothing. The LLM is the only writer to `state.sessionTodos`
// for status transitions, via the todo_write tool result. Users
// can still edit a task's text (the description) inline, but the
// completion state is exclusively the LLM's decision.

function startEdit(t: TodoItem) {
  editingId.value = t.id
  editingContent.value = t.content
}

function cancelEdit() {
  editingId.value = null
  editingContent.value = ''
}

function commitEdit(t: TodoItem) {
  const id = state.currentID
  if (!id) return
  if (editingContent.value === t.content) {
    cancelEdit()
    return
  }
  const todos = (state.sessionTodos[id] || []).map(ti => {
    if (ti.id === t.id) return { ...ti, content: editingContent.value }
    return ti
  })
  state.sessionTodos[id] = todos
  appendSystemMessage(`[用户操作] 任务 #${t.id} 内容改为: "${editingContent.value}"`)
  cancelEdit()
}

function onEditKeyup(e: KeyboardEvent, t: TodoItem) {
  if (e.key === 'Enter') {
    editingContent.value = (e.target as HTMLInputElement).value
    commitEdit(t)
  } else if (e.key === 'Escape') {
    cancelEdit()
  }
}

function toggleExpand() {
  expanded.value = !expanded.value
}
</script>

<template>
  <!--
    Two visual modes:
      collapsed: 36-px single-row strip with the active todo
                 content + a 状态 badge + X/Y progress
      expanded:  bordered panel with NScrollbar listing every
                 todo; new rows can be edited inline
  -->
  <div
    v-if="dockVisible"
    class="todo-dock"
    :class="{
      'todo-dock--expanded': expanded,
      'todo-dock--closing': closing,
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
            <svg v-else-if="t.status === 'done'" viewBox="0 0 16 16" width="14" height="14">
              <circle cx="8" cy="8" r="7" fill="currentColor" opacity="0.15" />
              <path d="M4 8.5l2.8 2.8L12 5.5" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round" stroke-linejoin="round" />
            </svg>
            <svg v-else viewBox="0 0 16 16" width="14" height="14">
              <circle cx="8" cy="8" r="6.5" fill="none" stroke="currentColor" stroke-width="1.2" opacity="0.45" />
            </svg>
          </span>
          <span
            v-if="editingId !== t.id"
            class="todo-row-text"
            :class="{ 'todo-row-text--done': t.status === 'done' }"
            @dblclick="startEdit(t)"
            :title="`双击编辑内容 · 状态由 LLM 控制`"
          >{{ t.content }}</span>
          <NInput
            v-else
            size="tiny"
            :value="editingContent"
            class="todo-row-edit"
            placeholder="编辑任务内容"
            @keyup="(e: KeyboardEvent) => onEditKeyup(e, t)"
            @blur="cancelEdit"
          />
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
          <span class="todo-row-text todo-row-text--done" :title="`双击编辑内容 · 状态由 LLM 控制`" @dblclick="startEdit(t)">
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
          <span class="todo-row-text todo-row-text--cancelled" :title="`双击编辑内容 · 状态由 LLM 控制`" @dblclick="startEdit(t)">{{ t.content }}</span>
        </div>
      </div>
    </NScrollbar>
  </div>
</template>

<style scoped>
/*
 * The dock sits directly above the InputArea (opencode pattern:
 * `session-todo-dock.tsx:96-100`). The header is always rendered
 * so the user can collapse/expand; the expanded list scrolls
 * inside its own NScrollbar so the chat above isn't pushed up.
 */
.todo-dock {
  border-top: 1px solid var(--border);
  background: var(--bg-2);
  flex-shrink: 0;
  display: flex;
  flex-direction: column;
  /* Animate the dock in/out via max-height. We use a generous
     upper bound (50vh) so the "open" state isn't clipped, and
     collapse to 36px in the "close" state before unmount. The
     `--closing` modifier shrinks further to fully retract. */
  max-height: 36px;
  transition: max-height 0.32s cubic-bezier(0.4, 0, 0.2, 1);
  overflow: hidden;
}
.todo-dock--expanded {
  max-height: min(50vh, 320px);
}
.todo-dock--closing {
  /* The "close" state: live + all done. Briefly hold the
     expanded body, then collapse the whole dock. CSS
     transition handles the height; the JS scheduleClose()
     timer flips --closing off and dockVisible=false to
     fully unmount. */
  max-height: 36px;
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
  50%      { opacity: 1;   transform: scale(1.15); }
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
.todo-row-check {
  flex-shrink: 0;
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
  /* Visual hint that this is a status indicator, not a button */
  pointer-events: auto;
}
.todo-row-mark svg {
  display: block;
}
.todo-row-mark--pending { color: var(--text-tertiary); }
.todo-row-mark--in_progress { color: var(--warn-500); }
.todo-row-mark--done { color: var(--success-500); }
.todo-row-mark--cancelled { color: var(--text-quaternary); }
/* Row hover must NOT trigger any mark change. We override the
   generic .todo-row:hover background that lights up other
   affordances so the mark itself stays inert. */
.todo-row:hover .todo-row-mark { color: inherit; }
.todo-row-text {
  flex: 1;
  min-width: 0;
  color: var(--text-1);
  cursor: text;
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
.todo-row-edit {
  flex: 1;
  min-width: 0;
}
.todo-row-edit :deep(input) {
  font-size: 13px;
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
.todo-row-remove {
  /* legacy: removal action was user-driven, now LLM-driven
     via todo_write. Hide any remaining instances defensively. */
  display: none;
}
.todo-row-cancel-mark {
  width: 16px;
  flex-shrink: 0;
  text-align: center;
  color: #999;
  font-size: 12px;
  display: none; /* legacy — removal action is now LLM-driven */
}
.todo-section-label {
  font-size: 10px;
  color: var(--text-4);
  text-transform: uppercase;
  letter-spacing: 0.5px;
  padding: 8px 4px 2px;
}
</style>
