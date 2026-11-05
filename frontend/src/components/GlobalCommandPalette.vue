<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import {
  filterGlobalCommands,
  type GlobalCommand,
} from '../utils/globalCommands'
import {
  ChevronsUpDown, FolderOpen, MessageSquare, Moon, PanelLeft,
  PanelRight, RotateCcw, Sun,
} from './icons'

const props = defineProps<{
  open: boolean
  commands: GlobalCommand[]
  theme: 'dark' | 'light'
}>()

const emit = defineEmits<{
  close: []
  run: [id: string]
}>()

const query = ref('')
const selected = ref(0)
const inputEl = ref<HTMLInputElement | null>(null)

const filtered = computed(() => filterGlobalCommands(props.commands, query.value))

watch(() => props.open, (open) => {
  if (!open) return
  query.value = ''
  selected.value = 0
  nextTick(() => inputEl.value?.focus())
})

watch(filtered, (list) => {
  if (selected.value >= list.length) selected.value = 0
})

function iconFor(id: string) {
  if (id === 'new-chat') return MessageSquare
  if (id === 'continue') return RotateCcw
  if (id === 'projects') return ChevronsUpDown
  if (id === 'add-project') return FolderOpen
  if (id === 'sidebar') return PanelLeft
  if (id === 'inspector') return PanelRight
  return props.theme === 'dark' ? Sun : Moon
}

function move(delta: number) {
  const list = filtered.value
  if (!list.length) return
  let next = selected.value
  for (let step = 0; step < list.length; step++) {
    next = (next + delta + list.length) % list.length
    if (list[next]?.enabled) {
      selected.value = next
      return
    }
  }
}

function choose(index = selected.value) {
  const command = filtered.value[index]
  if (!command?.enabled) return
  emit('run', command.id)
}

function onKeydown(event: KeyboardEvent) {
  if (event.key === 'ArrowDown') {
    event.preventDefault()
    event.stopPropagation()
    move(1)
  } else if (event.key === 'ArrowUp') {
    event.preventDefault()
    event.stopPropagation()
    move(-1)
  } else if (event.key === 'Enter') {
    event.preventDefault()
    event.stopPropagation()
    choose()
  } else if (event.key === 'Escape') {
    event.preventDefault()
    event.stopPropagation()
    emit('close')
  }
}
</script>

<template>
  <div
    v-if="open"
    class="palette-mask"
    @mousedown.self="emit('close')"
  >
    <div
      class="palette"
      role="dialog"
      aria-modal="true"
      aria-label="命令"
      @keydown="onKeydown"
    >
      <input
        ref="inputEl"
        v-model="query"
        class="palette-input"
        type="text"
        placeholder="搜索命令"
        aria-label="搜索命令"
        @keydown="onKeydown"
      />
      <div class="palette-list" role="listbox" aria-label="命令列表">
        <button
          v-for="(command, index) in filtered"
          :key="command.id"
          type="button"
          class="palette-item"
          role="option"
          :aria-selected="index === selected"
          :disabled="!command.enabled"
          :class="{ 'palette-item--active': index === selected }"
          @mouseenter="selected = index"
          @click="choose(index)"
        >
          <component :is="iconFor(command.id)" :size="14" class="palette-icon" />
          <span class="palette-copy">
            <span class="palette-label">{{ command.label }}</span>
            <span class="palette-hint">{{ command.hint }}</span>
          </span>
        </button>
        <div v-if="!filtered.length" class="palette-empty">没有匹配的命令</div>
      </div>
      <div class="palette-foot">上下键选择 · Enter 执行 · Esc 关闭 · Ctrl+K 开关</div>
    </div>
  </div>
</template>

<style scoped>
.palette-mask {
  position: fixed;
  inset: 0;
  z-index: 2100;
  display: flex;
  justify-content: center;
  align-items: flex-start;
  padding-top: 18vh;
  background: var(--surface-overlay);
}
.palette {
  width: min(32rem, calc(100vw - var(--space-8)));
  background: var(--glass-panel-bg);
  border: 1px solid var(--glass-border);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-lg);
  overflow: hidden;
}
.palette-input {
  width: 100%;
  box-sizing: border-box;
  border: none;
  border-bottom: 1px solid var(--border-subtle);
  background: transparent;
  color: var(--text-primary);
  font: inherit;
  font-size: 14px;
  padding: var(--space-3) var(--space-4);
  outline: none;
}
.palette-input::placeholder { color: var(--text-quaternary); }
.palette-list {
  max-height: 320px;
  overflow-y: auto;
  padding: var(--space-1);
}
.palette-item {
  width: 100%;
  display: flex;
  align-items: center;
  gap: var(--space-3);
  text-align: left;
  border: none;
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--text-secondary);
  padding: var(--space-2) var(--space-3);
  cursor: pointer;
}
.palette-item:disabled {
  cursor: default;
  color: var(--text-quaternary);
}
.palette-item--active:not(:disabled) {
  background: color-mix(in srgb, var(--brand-500) 8%, var(--surface-2));
  color: var(--text-primary);
}
.palette-icon { flex: 0 0 auto; color: var(--text-tertiary); }
.palette-copy {
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.palette-label {
  font-size: 13px;
  font-weight: 500;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.palette-hint {
  font-size: 11px;
  color: var(--text-tertiary);
}
.palette-empty {
  padding: var(--space-4);
  text-align: center;
  color: var(--text-tertiary);
  font-size: 13px;
}
.palette-foot {
  border-top: 1px solid var(--border-subtle);
  padding: var(--space-2) var(--space-4);
  color: var(--text-quaternary);
  font-size: 11px;
}
</style>
