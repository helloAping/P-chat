<script setup lang="ts">
// Turn plan docked to the user bubble's bottom-right corner. Three pieces,
// ported from .tmp-todo-stack-sample.html:
//
//  * badge — a small "3/6" pill with a pulsing amber mark while the turn
//    is live (solid green when everything is done), straddling the
//    bubble's bottom border. Click it to open the popover card.
//  * tip — an amber pill to the badge's left naming the task in flight,
//    with the little cat ambling along the text's baseline (HeaderCat in
//    tip mode). Only shown while there is an active task and the sheet
//    is closed. Clicking the tip also opens the sheet.
//  * sheet — the full item list, shown as a floating popover card anchored
//    to the badge (same NPopover pattern as the composer scene picker).
import { computed, ref } from 'vue'
import { NPopover } from 'naive-ui'
import type { TodoItem } from '../api/client'
import HeaderCat from './HeaderCat.vue'

const props = defineProps<{
  todos: TodoItem[]
}>()

const open = ref(false)

const total = computed(() => props.todos.length)
const finished = computed(() => props.todos.filter(item => item.status === 'done' || item.status === 'cancelled').length)
const allDone = computed(() => total.value > 0 && finished.value === total.value)
const active = computed(() =>
  props.todos.find(item => item.status === 'in_progress')
  ?? props.todos.find(item => item.status === 'pending'),
)
const progress = computed(() => `${finished.value}/${total.value}`)
// Tip hides while the sheet is open — the popover already lists every
// item, so the amber pill would only fight it for attention.
const showTip = computed(() => !allDone.value && !!active.value && !open.value)

function statusLabel(status: string): string {
  switch (status) {
    case 'in_progress': return '进行中'
    case 'done': return '已完成'
    case 'cancelled': return '已取消'
    default: return '待处理'
  }
}

function dotClass(status: string): string {
  if (status === 'in_progress') return 'dot go'
  if (status === 'done') return 'dot'
  return 'dot empty'
}

function openSheet() {
  open.value = true
}
</script>

<template>
  <div class="todo-stack" :class="{ live: !allDone }">
    <div class="todo-dock" :class="{ 'todo-dock--tip': showTip }">
      <button
        v-if="showTip"
        type="button"
        class="todo-tip"
        :title="active!.content"
        aria-label="查看本回合任务"
        @click="openSheet"
      >
        <span class="todo-tip-track">
          <span class="todo-tip-text">{{ active!.content }}</span>
          <span class="todo-tip-cat" aria-hidden="true"><HeaderCat tip /></span>
        </span>
        <span class="todo-tip-caret" aria-hidden="true" />
      </button>
      <!-- Absolute wrapper owns the badge's position on the bubble edge;
           the NPopover trigger inside stays in normal flow so the popover
           can measure its anchor rect correctly. -->
      <div class="todo-badge-anchor">
        <NPopover
          v-model:show="open"
          trigger="click"
          placement="bottom-end"
          :show-arrow="false"
          style="padding: 0; background: transparent; box-shadow: none;"
        >
          <template #trigger>
            <button
              class="todo-badge"
              type="button"
              :aria-expanded="open"
              :title="open ? '收起任务列表' : '展开任务列表'"
            >
              <span class="todo-badge-mark" :class="{ done: allDone }" aria-hidden="true" />
              {{ progress }}
            </button>
          </template>
          <div class="todo-sheet" role="list" aria-label="本回合任务">
            <p class="todo-sheet-caption">本回合任务 · {{ progress }}</p>
            <div
              v-for="item in todos"
              :key="item.id || item.content"
              class="todo-row"
              :class="`todo-row--${item.status}`"
              role="listitem"
            >
              <span :class="dotClass(item.status)" aria-hidden="true" />
              <span class="text">{{ item.content }}</span>
              <span class="tag">{{ statusLabel(item.status) }}</span>
            </div>
          </div>
        </NPopover>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* The dock is a thin strip pulled up over the bubble's bottom border so
 * the badge straddles the line (centre exactly on it, like the sample).
 * bubble-col already right-aligns user-message children, so the dock's
 * right edge coincides with the bubble's right edge. */
.todo-stack {
  position: relative;
  align-self: flex-end;
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  z-index: 3;
  max-width: 100%;
}
.todo-dock {
  position: relative;
  width: 100%;
  /* Badge-only: the badge's lower half sticks 9px below the border and
   * the dock's top sits 5px above it (4px flex gap − 9px margin), so
   * 14px leaves a single pixel of air under the badge. When the tip is
   * up its bottom reaches 12px below the border, needing 20px total. */
  height: 14px;
  margin-top: -9px;
}
.todo-dock--tip {
  height: 20px;
}

/* Positioning shell for the badge + its popover. Keeps the absolute
 * placement on this shell so the NPopover trigger itself stays in flow. */
.todo-badge-anchor {
  position: absolute;
  right: 8px;
  top: -4px;
  z-index: 2;
}

/* Badge: 18px pill centred on the bubble's bottom border. */
.todo-badge {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  height: 18px;
  padding: 0 6px;
  border: 1px solid var(--border-subtle);
  border-radius: 999px;
  background: var(--surface-0);
  color: var(--text-secondary);
  font-size: 11px;
  cursor: pointer;
}
.todo-badge:hover {
  color: var(--text-primary);
  border-color: var(--text-quaternary);
}
.todo-badge-mark {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--warn-500);
  animation: todo-pulse 1.6s ease-out infinite;
  flex-shrink: 0;
}
.todo-badge-mark.done {
  background: var(--success-500);
  animation: none;
}
@keyframes todo-pulse {
  0% { box-shadow: 0 0 0 0 color-mix(in srgb, var(--warn-500) 45%, transparent); }
  100% { box-shadow: 0 0 0 7px color-mix(in srgb, var(--warn-500) 0%, transparent); }
}

/* Tip: amber pill left of the badge. Overflow stays hidden so the tip
 * cat cannot paint past the stadium curve; the caret is a sibling so it
 * is not clipped with the track. Click opens the same sheet as the badge. */
.todo-tip {
  position: absolute;
  right: 52px;
  top: -11px;
  z-index: 1;
  display: flex;
  align-items: center;
  max-width: 220px;
  height: 28px;
  padding: 0 12px 0 10px;
  border: none;
  border-radius: 999px;
  background: color-mix(in srgb, var(--warn-500) 18%, var(--surface-0));
  color: color-mix(in srgb, var(--warn-500) 62%, var(--text-primary));
  font: inherit;
  font-size: 11px;
  line-height: 1;
  text-align: left;
  white-space: nowrap;
  cursor: pointer;
  box-shadow: 0 6px 16px color-mix(in srgb, var(--warn-500) 16%, transparent);
  transform-origin: calc(100% - 4px) 50%;
  animation:
    todo-tip-pop 0.72s cubic-bezier(0.34, 1.56, 0.5, 1) backwards,
    todo-tip-glow 3.4s ease-in-out 0.72s infinite;
}
.todo-tip-caret {
  position: absolute;
  right: -3px;
  width: 8px;
  height: 8px;
  border-radius: 2px;
  background: color-mix(in srgb, var(--warn-500) 18%, var(--surface-0));
  transform: rotate(45deg);
  pointer-events: none;
}
.todo-tip:hover {
  transform: translateX(-3px) scale(1.04);
  transition: transform 0.4s cubic-bezier(0.2, 1.3, 0.3, 1);
}
.todo-tip:focus-visible {
  outline: 2px solid color-mix(in srgb, var(--warn-500) 55%, transparent);
  outline-offset: 2px;
}
@keyframes todo-tip-pop {
  0% { opacity: 0; transform: translateX(16px) scale(0.55); }
  60% { opacity: 1; transform: translateX(-4px) scale(1.06); }
  100% { opacity: 1; transform: none; }
}
@keyframes todo-tip-glow {
  0%, 100% { box-shadow: 0 4px 12px color-mix(in srgb, var(--warn-500) 12%, transparent); }
  50% { box-shadow: 0 8px 18px color-mix(in srgb, var(--warn-500) 28%, transparent); }
}
.todo-tip-track {
  position: relative;
  display: block;
  min-width: 0;
  max-width: 100%;
  height: 28px;
  overflow: hidden;
  border-radius: 999px;
}
.todo-tip-text {
  display: block;
  overflow: hidden;
  text-overflow: ellipsis;
  line-height: 28px;
}
/* Cat lane: lifted 2px off the pill floor so feet clear the curve, and
 * clipped by the track so a mis-planned segment cannot escape. */
.todo-tip-cat {
  position: absolute;
  left: 0;
  right: 0;
  bottom: 3px;
  height: 14px;
  z-index: 2;
  pointer-events: none;
  overflow: hidden;
}
.todo-tip-cat :deep(.header-cat) {
  position: absolute;
  left: 0;
  right: 0;
  bottom: 0;
  height: 14px;
}
</style>

/* The sheet is teleported into the NPopover float (body), so these
 * styles must be global. NPopover chrome is stripped (transparent /
 * no padding) — the sheet itself is the card. */
<style>
.todo-sheet {
  width: min(320px, 70vw);
  max-height: 280px;
  overflow-y: auto;
  padding: 8px 10px 6px;
  border: 1px solid var(--border-subtle);
  border-radius: 12px;
  background: var(--surface-0);
  box-shadow: 0 8px 24px color-mix(in srgb, var(--text-primary) 12%, transparent);
}
.todo-sheet-caption {
  margin: 0 0 4px;
  color: var(--text-tertiary);
  font-size: 11px;
}
.todo-sheet .todo-row {
  display: flex;
  align-items: center;
  gap: 8px;
  min-height: 24px;
  color: var(--text-secondary);
  font-size: 12.5px;
}
.todo-sheet .dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--success-500);
  flex: none;
}
.todo-sheet .dot.go { background: var(--warn-500); }
.todo-sheet .dot.empty {
  background: transparent;
  box-shadow: inset 0 0 0 1.5px var(--text-quaternary);
}
.todo-sheet .text {
  flex: 1;
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.todo-sheet .todo-row--done .text,
.todo-sheet .todo-row--cancelled .text {
  color: var(--text-tertiary);
}
.todo-sheet .tag {
  flex: none;
  color: var(--text-quaternary);
  font-size: 11px;
}
.todo-sheet .todo-row--in_progress .tag { color: var(--text-secondary); }
</style>
