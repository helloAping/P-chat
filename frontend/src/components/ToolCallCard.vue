<script setup lang="ts">
// A single tool call. Compact card by default — the
// header shows the tool name + status icon (loading
// spinner / check / warn / x) + elapsed time. Clicking
// expands to show the args and the result / error.
import { computed, ref } from 'vue'
import type { ToolPart } from '../api/client'
import { Check, X, AlertTriangle, Loader2, ChevronRight, ChevronDown } from './icons'

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
    case 'start': return Loader2
    case 'ok':    return Check
    case 'warn':  return AlertTriangle
    case 'error': return X
    default:      return null
  }
})

const argsPretty = computed(() => {
  const a = props.part.args
  if (!a) return ''
  try { return JSON.stringify(JSON.parse(a), null, 2) } catch { return a }
})

// Detect browser screenshot data in the result. The
// extension returns images either as a raw data: URL
// (legacy pre-blob conversion) or as
// `{image: "data:image/jpeg;base64,..."}` JSON.
// After the store's convertAndStripScreenshots runs,
// these become `blob:` URLs which the browser <img>
// handles natively with the same `:src` binding.
const screenshotURL = computed(() => {
  const r = props.part.result
  if (!r) return ''
  if (r.startsWith('data:image/') || r.startsWith('blob:')) return r
  try {
    const obj = JSON.parse(r)
    if (typeof obj.image === 'string') {
      const img = obj.image as string
      if (img.startsWith('data:image/') || img.startsWith('blob:')) return img
    }
  } catch { /* not JSON */ }
  return ''
})
</script>

<template>
  <div class="tool-card" :class="'status-' + part.status">
    <button class="tool-header" @click="open = !open" :title="open ? '收起' : '展开'">
      <span class="tool-icon" :class="part.status">
        <component :is="statusIcon" v-if="statusIcon" :size="11" :class="part.status === 'start' ? 'spin' : ''" />
      </span>
      <span class="tool-name">{{ part.name }}</span>
      <span class="tool-status">{{ statusLabel }}</span>
      <span class="tool-elapsed" v-if="part.elapsed">{{ part.elapsed }}</span>
      <component :is="open ? ChevronDown : ChevronRight" :size="12" class="tool-caret" />
    </button>
    <div v-if="open" class="tool-body">
      <div v-if="part.args" class="tool-args">
        <div class="tool-section-label">参数</div>
        <pre>{{ argsPretty }}</pre>
      </div>
      <div v-if="part.result" class="tool-result">
        <div class="tool-section-label">结果</div>
        <img v-if="screenshotURL" :src="screenshotURL" class="tool-screenshot" loading="lazy" />
        <pre v-else>{{ part.result }}</pre>
      </div>
      <div v-if="part.error" class="tool-error">
        <div class="tool-section-label">错误</div>
        <pre>{{ part.error }}</pre>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* Tool call card. Matches the unified card spec in
 * frontend-design.md §3 — 3px left status rail, surface-2
 * body, var(--radius-md) corners, dashed border-top
 * separator between header and body. */
.tool-card {
  background: var(--surface-2);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-md);
  margin: 4px 0;
  overflow: hidden;
  font-size: 12.5px;
  transition: border-color var(--dur-fast) var(--ease-out);
}
.tool-card.status-start { border-left: 3px solid var(--brand-500); }
.tool-card.status-ok    { border-left: 3px solid var(--success-500); }
.tool-card.status-warn  { border-left: 3px solid var(--warn-500); }
.tool-card.status-error { border-left: 3px solid var(--error-500); }

.tool-header {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  background: transparent;
  border: 0;
  padding: 5px 12px;
  text-align: left;
  cursor: pointer;
  color: var(--text-secondary);
  font-family: inherit;
  font-size: inherit;
  transition: background var(--dur-fast) var(--ease-out);
}
.tool-header:hover { background: var(--surface-3); }
.tool-icon {
  display: inline-flex;
  width: 16px; height: 16px;
  align-items: center; justify-content: center;
  border-radius: 50%;
  flex-shrink: 0;
}
.tool-icon.start { background: var(--brand-50); color: var(--brand-500); }
.tool-icon.ok    { background: var(--success-50); color: var(--success-500); }
.tool-icon.warn  { background: var(--warn-50);    color: var(--warn-500); }
.tool-icon.error { background: var(--error-50);   color: var(--error-500); }
.spin {
  display: inline-block;
  animation: tool-spin 1.2s linear infinite;
}
@keyframes tool-spin {
  from { transform: rotate(0deg); }
  to   { transform: rotate(360deg); }
}
.tool-name {
  font-family: var(--font-mono);
  font-size: 12px;
  color: var(--text-primary);
}
.tool-status { color: var(--text-tertiary); font-size: 11px; }
.tool-elapsed {
  color: var(--text-quaternary);
  font-size: 11px;
  margin-left: 4px;
  font-variant-numeric: tabular-nums;
}
.tool-caret { margin-left: auto; color: var(--text-tertiary); flex-shrink: 0; }

.tool-body {
  border-top: 1px dashed var(--border-subtle);
  padding: 6px 12px 8px;
}
.tool-section-label {
  font-size: 10.5px;
  text-transform: uppercase;
  letter-spacing: 0.5px;
  color: var(--text-quaternary);
  margin: 4px 0 2px;
  font-weight: 500;
}
.tool-args pre, .tool-result pre, .tool-error pre {
  margin: 0;
  padding: 6px 8px;
  background: var(--surface-0);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  font-family: var(--font-mono);
  font-size: 11.5px;
  line-height: 1.45;
  color: var(--text-secondary);
  white-space: pre-wrap;
  word-wrap: break-word;
  max-height: 240px;
  overflow: auto;
}
.tool-error pre {
  color: var(--error-500);
  border-color: var(--error-500);
  background: var(--error-50);
}
.tool-screenshot {
  display: block;
  max-width: 100%;
  max-height: 400px;
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  margin-top: 4px;
  cursor: pointer;
  transition: transform var(--dur-fast) var(--ease-out);
}
.tool-screenshot:hover {
  transform: scale(1.02);
}
</style>
