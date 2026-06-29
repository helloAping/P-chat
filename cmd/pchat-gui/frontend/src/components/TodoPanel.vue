<script setup lang="ts">
import { computed, ref } from 'vue'
import { NCheckbox, NTag, NScrollbar, NButton, NInput } from 'naive-ui'
import { currentTodos, state, appendSystemMessage } from '../stores/chat'
import type { TodoItem } from '../api/client'

const activeTodos = computed(() => currentTodos.value.filter(t => t.status !== 'done' && t.status !== 'cancelled'))
const doneTodos = computed(() => currentTodos.value.filter(t => t.status === 'done'))
const cancelledTodos = computed(() => currentTodos.value.filter(t => t.status === 'cancelled'))

const editingId = ref<string | null>(null)
const editingContent = ref('')

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

function toggleTodo(t: TodoItem) {
  const id = state.currentID
  if (!id) return
  const todos = (state.sessionTodos[id] || []).map(ti => {
    if (ti.id === t.id) {
      const nextStatus = t.status === 'done' ? 'pending' : 'done'
      return { ...ti, status: nextStatus }
    }
    return ti
  })
  state.sessionTodos[id] = todos
  appendSystemMessage(`[用户操作] 任务 "${t.content}" 状态: ${t.status === 'done' ? '重新打开' : '完成'}`)
}

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
  }
}

function removeTodo(t: TodoItem) {
  const id = state.currentID
  if (!id) return
  const todos = (state.sessionTodos[id] || []).map(ti => {
    if (ti.id === t.id) return { ...ti, status: 'cancelled' as const }
    return ti
  })
  state.sessionTodos[id] = todos
  appendSystemMessage(`[用户操作] 取消任务: "${t.content}"`)
}
</script>

<template>
  <div v-if="activeTodos.length > 0" class="todo-panel">
    <div class="todo-header">📋 任务列表</div>
    <NScrollbar class="todo-scroll">
      <div v-for="t in activeTodos" :key="t.id" class="todo-item" :title="'点击切换完成状态 | 双击编辑'">
        <NCheckbox
          :checked="t.status === 'done'"
          size="small"
          class="todo-checkbox"
          @update:checked="toggleTodo(t)"
        />
        <span v-if="editingId !== t.id" class="todo-text" @dblclick="startEdit(t)">{{ t.content }}</span>
        <NInput
          v-else
          size="tiny"
          :value="editingContent"
          class="todo-edit-input"
          @keyup="(e: KeyboardEvent) => onEditKeyup(e, t)"
          @blur="cancelEdit"
        />
        <NTag :color="{ color: statusColor(t.status), textColor: '#fff' }" size="tiny" :bordered="false">
          {{ statusLabel(t.status) }}
        </NTag>
        <NButton text size="tiny" class="todo-remove" @click="removeTodo(t)">✕</NButton>
      </div>
      <div v-if="doneTodos.length > 0 || cancelledTodos.length > 0" class="todo-section-title">已完成</div>
      <div v-for="t in doneTodos" :key="'d-' + t.id" class="todo-item todo-done">
        <NCheckbox :checked="true" size="small" class="todo-checkbox" @update:checked="toggleTodo(t)" />
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
.todo-checkbox {
  flex-shrink: 0;
}
.todo-cancel { color: #999; flex-shrink: 0; width: 8px; }
.todo-text { flex: 1; color: var(--text-1); min-width: 0; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; cursor: pointer; }
.todo-done .todo-text { color: var(--text-3); text-decoration: line-through; }
.todo-cancelled .todo-text { color: var(--text-4); text-decoration: line-through; }
.todo-section-title {
  font-size: 11px;
  color: var(--text-4);
  padding: 6px 0 2px;
}
.todo-remove {
  opacity: 0;
  flex-shrink: 0;
}
.todo-item:hover .todo-remove {
  opacity: 0.5;
}
.todo-item:hover .todo-remove:hover {
  opacity: 1;
}
.todo-edit-input {
  flex: 1;
  min-width: 0;
}
</style>
