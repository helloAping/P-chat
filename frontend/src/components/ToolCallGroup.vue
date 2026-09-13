<script setup lang="ts">
import { computed, ref } from 'vue'
import type { ToolPart } from '../api/client'
import {
  AlertTriangle,
  Check,
  ChevronDown,
  ChevronRight,
  Loader2,
  Terminal,
  X,
} from './icons'
import ToolCallCard from './ToolCallCard.vue'
import { toolGeneratedAssetsFromPart } from '../utils/attachmentArtifacts'

const props = defineProps<{ parts: ToolPart[] }>()

const userToggled = ref(false)
const userOpen = ref(false)

const hasRunning = computed(() => props.parts.some(part => part.status === 'start'))
const hasError = computed(() => props.parts.some(part => part.status === 'error' || part.status === 'blocked'))
const hasWarn = computed(() => props.parts.some(part => part.status === 'warn'))
const hasMediaResult = computed(() => props.parts.some(partHasMediaResult))

const open = computed(() => {
  if (!userToggled.value) return hasRunning.value || hasMediaResult.value
  return userOpen.value
})

function toggle() {
  userToggled.value = true
  userOpen.value = !open.value
}

const groupStatus = computed<'start' | 'error' | 'warn' | 'ok'>(() => {
  if (hasRunning.value) return 'start'
  if (hasError.value) return 'error'
  if (hasWarn.value) return 'warn'
  return 'ok'
})

const statusLabel = computed(() => {
  switch (groupStatus.value) {
    case 'start': return '执行中'
    case 'error': return '有失败'
    case 'warn': return '有警告'
    default: return '已完成'
  }
})

const statusIcon = computed(() => {
  switch (groupStatus.value) {
    case 'start': return Loader2
    case 'error': return X
    case 'warn': return AlertTriangle
    default: return Check
  }
})

const toolNames = computed(() => {
  const seen = new Set<string>()
  const names: string[] = []
  for (const part of props.parts) {
    if (!part.name || seen.has(part.name)) continue
    seen.add(part.name)
    names.push(part.name)
  }
  return names
})

const summary = computed(() => {
  const names = toolNames.value
  if (names.length === 0) return ''
  const head = names.slice(0, 3).join(' / ')
  return names.length > 3 ? `${head} +${names.length - 3}` : head
})

const elapsedLabel = computed(() => {
  const elapsed = [...props.parts].reverse().find(part => part.elapsed)?.elapsed
  return elapsed || ''
})

function compactArgs(args: string | undefined): string {
  if (!args) return ''
  try {
    const parsed = JSON.parse(args)
    if (typeof parsed?.command === 'string') return parsed.command
    if (typeof parsed?.cmd === 'string') return parsed.cmd
    if (typeof parsed?.query === 'string') return parsed.query
    if (typeof parsed?.pattern === 'string') return parsed.pattern
    if (typeof parsed?.path === 'string') return parsed.path
    if (typeof parsed?.url === 'string') return parsed.url
    return JSON.stringify(parsed)
  } catch {
    return args
  }
}

function rowLabel(part: ToolPart): string {
  const label = compactArgs(part.args).replace(/\s+/g, ' ').trim()
  if (label.length <= 96) return label
  return `${label.slice(0, 96)}...`
}

function partHasMediaResult(part: ToolPart): boolean {
  return toolGeneratedAssetsFromPart(part).length > 0
}
</script>

<template>
  <div class="tool-group" :class="['status-' + groupStatus, { open }]">
    <button
      type="button"
      class="tool-group-header"
      :title="open ? '收起工具调用' : '展开工具调用'"
      @click="toggle"
    >
      <span class="tool-group-icon" :class="groupStatus">
        <component :is="statusIcon" :size="11" :class="groupStatus === 'start' ? 'spin' : ''" />
      </span>
      <span class="tool-group-title">
        <Terminal :size="13" />
        <span>执行记录</span>
      </span>
      <span v-if="summary" class="tool-group-summary">{{ summary }}</span>
      <span class="tool-group-status">{{ parts.length }} 项</span>
      <span class="tool-group-status">{{ statusLabel }}</span>
      <span v-if="elapsedLabel" class="tool-group-elapsed">{{ elapsedLabel }}</span>
      <component :is="open ? ChevronDown : ChevronRight" :size="12" class="tool-group-caret" />
    </button>

    <div v-if="!open" class="tool-group-preview">
      <div
        v-for="(part, i) in parts"
        :key="`preview-${i}-${part.tool_id || part.id || part.name}`"
        class="tool-group-row"
      >
        <span class="tool-row-dot" :class="part.status"></span>
        <span class="tool-row-name">{{ part.name }}</span>
        <span v-if="rowLabel(part)" class="tool-row-args">{{ rowLabel(part) }}</span>
        <span class="tool-row-status">{{ part.status === 'start' ? '执行中' : part.status === 'blocked' ? '已关闭' : part.status === 'error' ? '失败' : part.status === 'warn' ? '警告' : '完成' }}</span>
      </div>
    </div>

    <div v-else class="tool-group-body">
      <ToolCallCard
        v-for="(part, i) in parts"
        :key="`detail-${i}-${part.tool_id || part.id || part.name}`"
        :part="part"
      />
    </div>
  </div>
</template>

<style scoped>
.tool-group {
  background: transparent;
  border: none;
  border-radius: 0;
  margin: 0;
  overflow: hidden;
  font-size: 12.5px;
}

.tool-group-header {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  width: 100%;
  background: transparent;
  border: 0;
  padding: var(--space-2) var(--space-3);
  text-align: left;
  cursor: pointer;
  color: var(--text-secondary);
  font-family: inherit;
  font-size: inherit;
  transition: background var(--dur-fast) var(--ease-out);
}
.tool-group-header:hover {
  background: var(--surface-3);
}
.tool-group-icon {
  display: inline-flex;
  width: 16px;
  height: 16px;
  align-items: center;
  justify-content: center;
  border-radius: var(--radius-pill);
  flex-shrink: 0;
}
.tool-group-icon.start { background: var(--brand-50); color: var(--brand-500); }
.tool-group-icon.ok { background: var(--success-50); color: var(--success-500); }
.tool-group-icon.warn { background: var(--warn-50); color: var(--warn-500); }
.tool-group-icon.error { background: var(--error-50); color: var(--error-500); }

.spin {
  display: inline-block;
  animation: tool-group-spin 1.2s linear infinite;
}
@keyframes tool-group-spin {
  from { transform: rotate(0deg); }
  to { transform: rotate(360deg); }
}

.tool-group-title {
  display: inline-flex;
  align-items: center;
  gap: var(--space-1);
  color: var(--text-primary);
  font-weight: 500;
  min-width: 0;
  flex-shrink: 0;
}
.tool-group-summary {
  min-width: 0;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--text-tertiary);
  font-family: var(--font-mono);
  font-size: 11.5px;
}
.tool-group-status {
  color: var(--text-tertiary);
  font-size: 11px;
  flex-shrink: 0;
}
.tool-group-elapsed {
  color: var(--text-quaternary);
  font-size: 11px;
  font-variant-numeric: tabular-nums;
  flex-shrink: 0;
}
.tool-group-caret {
  margin-left: auto;
  color: var(--text-tertiary);
  flex-shrink: 0;
}

.tool-group-preview {
  display: grid;
  gap: var(--space-1);
  border-top: 1px dashed var(--border-subtle);
  padding: var(--space-2) var(--space-3);
  background: color-mix(in srgb, var(--surface-2) 42%, transparent);
}
.tool-group-row {
  display: grid;
  grid-template-columns: 10px minmax(80px, max-content) minmax(0, 1fr) max-content;
  align-items: center;
  gap: var(--space-2);
  min-height: 22px;
  color: var(--text-tertiary);
}
.tool-row-dot {
  width: 6px;
  height: 6px;
  border-radius: var(--radius-pill);
  background: var(--success-500);
}
.tool-row-dot.start { background: var(--brand-500); }
.tool-row-dot.warn { background: var(--warn-500); }
.tool-row-dot.error { background: var(--error-500); }
.tool-row-name {
  color: var(--text-secondary);
  font-family: var(--font-mono);
  font-size: 11.5px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.tool-row-args {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--text-quaternary);
  font-family: var(--font-mono);
  font-size: 11px;
}
.tool-row-status {
  color: var(--text-quaternary);
  font-size: 11px;
}

.tool-group-body {
  border-top: 1px dashed var(--border-subtle);
  padding: var(--space-2) var(--space-3);
  background: color-mix(in srgb, var(--surface-2) 42%, transparent);
}
.tool-group-body :deep(.tool-card) {
  margin: var(--space-1) 0;
  background: transparent;
}
</style>
