<script setup lang="ts">
import { computed } from 'vue'
import { NCheckbox, NTag, NScrollbar } from 'naive-ui'
import { currentTodos } from '../stores/chat'

const activeTodos = computed(() => currentTodos.value.filter(t => t.status !== 'done' && t.status !== 'cancelled'))
const doneTodos = computed(() => currentTodos.value.filter(t => t.status === 'done'))
const cancelledTodos = computed(() => currentTodos.value.filter(t => t.status === 'cancelled'))

function statusColor(status: string): string {
  switch (status) {
    case 'in_progress': return '#f0a020'
    case 'done':        return '#18a058'
    case 'cancelled':   return '#999'
    default:            return 'default'
  }
}

function statusLabel(status: string): string {
  switch (status) {
    case 'in_progress': return '进行中'
    case 'done':        return '已完成'
    case 'cancelled':   return '已取消'
    default:            return '待处理'
  }
}
</script>

<template>
  <div v-if="activeTodos.length > 0" class="todo-panel">
    <div class="todo-header">📋 任务列表</div>
    <NScrollbar class="todo-scroll">
      <div v-for="t in activeTodos" :key="t.id" class="todo-item">
        <span class="todo-dot" :class="'dot-' + (t.status === 'in_progress' ? 'active' : 'pending')" />
        <span class="todo-text">{{ t.content }}</span>
        <NTag :color="{ color: statusColor(t.status), textColor: '#fff' }" size="tiny" :bordered="false">
          {{ statusLabel(t.status) }}
        </NTag>
      </div>
      <div v-if="doneTodos.length > 0 || cancelledTodos.length > 0" class="todo-section-title">已完成</div>
      <div v-for="t in doneTodos" :key="'d-' + t.id" class="todo-item todo-done">
        <span class="todo-check">✓</span>
        <span class="todo-text">{{ t.content }}</span>
      </div>
      <div v-for="t in cancelledTodos" :key="'c-' + t.id" class="todo-item todo-cancelled">
        <span class="todo-cancel">✗</span>
        <span class="todo-text">{{ t.content }}</span>
      </div>
    </NScrollbar>
  </div>
</template>

<style scoped>
.todo-panel {
  border-bottom: 1px solid var(--border);
  background: var(--bg-2);
  flex-shrink: 0;
  max-height: 200px;
  display: flex;
  flex-direction: column;
}
.todo-header {
  padding: 8px 16px 4px;
  font-size: 12px;
  font-weight: 600;
  color: var(--text-2);
  flex-shrink: 0;
}
.todo-scroll {
  flex: 1;
  min-height: 0;
}
.todo-scroll :deep(.n-scrollbar-content) {
  padding: 0 16px 8px;
}
.todo-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 3px 0;
  font-size: 13px;
}
.todo-dot {
  width: 8px;
  height: 8px;
  border-radius: 50%;
  flex-shrink: 0;
}
.dot-active { background: #f0a020; }
.dot-pending { background: var(--text-4); }
.todo-check { color: #18a058; flex-shrink: 0; width: 8px; }
.todo-cancel { color: #999; flex-shrink: 0; width: 8px; }
.todo-text { flex: 1; color: var(--text-1); min-width: 0; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
.todo-done .todo-text { color: var(--text-3); text-decoration: line-through; }
.todo-cancelled .todo-text { color: var(--text-4); text-decoration: line-through; }
.todo-section-title {
  font-size: 11px;
  color: var(--text-4);
  padding: 6px 0 2px;
}
</style>
