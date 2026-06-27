<script setup lang="ts">
// A single tool call. Compact card by default — the
// header shows the tool name + status icon (loading
// spinner / check / warn / x) + elapsed time. Clicking
// expands to show the args and the result / error.
import { computed, ref } from 'vue'
import type { ToolPart } from '../api/client'

const props = defineProps<{ part: ToolPart }>()

const open = ref(false)

const statusLabel = computed(() => {
  switch (props.part.status) {
    case 'start': return '执行中…'
    case 'ok':    return '完成'
    case 'warn':  return '完成 (有警告)'
    case 'error': return '失败'
    default:      return props.part.status
  }
})

const statusIcon = computed(() => {
  switch (props.part.status) {
    case 'start': return '◐'
    case 'ok':    return '✓'
    case 'warn':  return '!'
    case 'error': return '✗'
    default:      return '·'
  }
})

// Pretty-print the args JSON. If parsing fails, fall
// back to the raw string.
const argsPretty = computed(() => {
  const a = props.part.args
  if (!a) return ''
  try { return JSON.stringify(JSON.parse(a), null, 2) } catch { return a }
})
</script>

<template>
  <div class="tool-card" :class="'status-' + part.status">
    <button class="tool-header" @click="open = !open" :title="open ? '收起' : '展开'">
      <span class="tool-icon" :class="part.status">
        <span v-if="part.status === 'start'" class="spin">◐</span>
        <span v-else>{{ statusIcon }}</span>
      </span>
      <span class="tool-name">{{ part.name }}</span>
      <span class="tool-status">{{ statusLabel }}</span>
      <span class="tool-elapsed" v-if="part.elapsed">{{ part.elapsed }}</span>
      <span class="tool-caret">{{ open ? '▾' : '▸' }}</span>
    </button>
    <div v-if="open" class="tool-body">
      <div v-if="part.args" class="tool-args">
        <div class="tool-section-label">参数</div>
        <pre>{{ argsPretty }}</pre>
      </div>
      <div v-if="part.result" class="tool-result">
        <div class="tool-section-label">结果</div>
        <pre>{{ part.result }}</pre>
      </div>
      <div v-if="part.error" class="tool-error">
        <div class="tool-section-label">错误</div>
        <pre>{{ part.error }}</pre>
      </div>
    </div>
  </div>
</template>

<style scoped>
.tool-card {
  background: var(--bg-2);
  border: 1px solid var(--border-2);
  border-radius: 6px;
  margin: 4px 0;
  overflow: hidden;
  font-size: 12.5px;
}
.tool-card.status-start { border-left: 3px solid var(--accent); }
.tool-card.status-ok    { border-left: 3px solid var(--success); }
.tool-card.status-warn  { border-left: 3px solid var(--warn); }
.tool-card.status-error { border-left: 3px solid var(--error); }

.tool-header {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  background: transparent;
  border: 0;
  padding: 5px 10px;
  text-align: left;
  cursor: pointer;
  color: var(--text-2);
  font-family: inherit;
  font-size: inherit;
}
.tool-header:hover { background: var(--bg-3); }
.tool-icon {
  display: inline-flex;
  width: 16px; height: 16px;
  align-items: center; justify-content: center;
  border-radius: 50%;
  font-size: 11px;
  font-weight: 700;
  flex-shrink: 0;
}
.tool-icon.start { background: var(--accent-soft); color: var(--accent); }
.tool-icon.ok    { background: var(--success-soft); color: var(--success); }
.tool-icon.warn  { background: var(--warn-soft);    color: var(--warn); }
.tool-icon.error { background: var(--error-soft);   color: var(--error); }
.spin {
  display: inline-block;
  animation: tool-spin 1.2s linear infinite;
}
@keyframes tool-spin {
  from { transform: rotate(0deg); }
  to   { transform: rotate(360deg); }
}
.tool-name {
  font-family: ui-monospace, Menlo, Consolas, monospace;
  font-size: 12px;
  color: var(--text);
}
.tool-status { color: var(--text-3); font-size: 11px; }
.tool-elapsed { color: var(--text-4); font-size: 11px; margin-left: 4px; }
.tool-caret { margin-left: auto; color: var(--text-3); font-size: 10px; }

.tool-body {
  border-top: 1px dashed var(--border-2);
  padding: 6px 10px 8px;
}
.tool-section-label {
  font-size: 10.5px;
  text-transform: uppercase;
  letter-spacing: 0.5px;
  color: var(--text-4);
  margin: 4px 0 2px;
}
.tool-args pre, .tool-result pre, .tool-error pre {
  margin: 0;
  padding: 6px 8px;
  background: var(--bg);
  border: 1px solid var(--border-2);
  border-radius: 4px;
  font-family: ui-monospace, Menlo, Consolas, monospace;
  font-size: 11.5px;
  line-height: 1.45;
  color: var(--text-2);
  white-space: pre-wrap;
  word-wrap: break-word;
  max-height: 240px;
  overflow: auto;
}
.tool-error pre { color: var(--error); border-color: var(--error); }
</style>
