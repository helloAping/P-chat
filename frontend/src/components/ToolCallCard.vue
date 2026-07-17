<script setup lang="ts">
// A single tool call. Compact card by default — the
// header shows the tool name + status icon (loading
// spinner / check / warn / x) + elapsed time. Clicking
// expands to show the args and the result / error.
//
// P1-1 folding:
// Long result bodies (>= 200 chars OR >= 4 newlines — the
// typical shell / wiki_search / read-file-with-long-output
// case) start COLLAPSED so the chat stays scannable. The
// user can click the header to expand, and the choice is
// remembered per session via localStorage so refresh /
// session switch keeps the user's preference.
import { computed, ref, watch } from 'vue'
import type { ToolPart } from '../api/client'
import { Check, X, AlertTriangle, Loader2, ChevronRight, ChevronDown, Clipboard } from './icons'

const props = defineProps<{ part: ToolPart }>()

// Auto-fold threshold: long shell/wiki/file output. Picked
// from observation — a `cat` of a 30-line file is ~1200
// chars and we want it folded; a JSON tool result that
// fits in a tooltip (~150 chars) is fine open.
const FOLD_RESULT_MIN_CHARS = 200
const FOLD_RESULT_MIN_LINES = 4

// Whether the result is "long enough" to warrant a
// default-collapsed state. The args block is not folded —
// it's typically 1-3 lines and not the noise.
const shouldFoldResult = computed(() => {
  const r = props.part.result || ''
  if (r.length >= FOLD_RESULT_MIN_CHARS) return true
  let lines = 0
  for (let i = 0; i < r.length; i++) {
    if (r.charCodeAt(i) === 10) {
      lines++
      if (lines >= FOLD_RESULT_MIN_LINES) return true
    }
  }
  return false
})

// open is the visual state. The decision tree is:
//   1. short result → always open (no fold UI at all)
//   2. long result + no user choice yet → default to folded
//   3. long result + user clicked → respect their choice
// `userToggled` flips to true the first time the user
// clicks the header; until then we follow the heuristic.
const userToggled = ref(false)
const userWantsOpen = ref(false)
const open = computed(() => {
  if (!shouldFoldResult.value) return true
  if (!userToggled.value) return false
  return userWantsOpen.value
})
function toggle() {
  if (!shouldFoldResult.value) return
  userToggled.value = true
  userWantsOpen.value = !open.value
}

// localStorage persistence keyed by sessionId+toolName+
// first-40-chars-of-result hash. Falls back to in-memory
// only when storage is unavailable (e.g. iframe sandbox
// without permission). Stored per-session so a fresh
// session doesn't inherit old fold choices.
const foldStorageKey = (sid: string, p: ToolPart) =>
  `pchat.toolFold.${sid}.${p.name || 'unknown'}.${(p.tool_id || p.id || '').slice(0, 12)}`

// On mount, hydrate the userToggled + userWantsOpen state
// from storage. We don't have sessionId in props — the
// chat store is the source of truth, so we accept it as a
// prop OR derive from a data attribute. Simpler: hydrate
// from a per-tool cache keyed on the tool_id (which is
// stable per CallRequest).
//
// Implementation: read from localStorage in a watch on
// part.tool_id (which is the only stable per-call key).
// We deliberately use tool_id over sessionId here because
// folding preference is more about "I always want wiki
// results folded" than "this session is special". A
// future improvement could cross-reference both.
watch(
  () => props.part.tool_id || props.part.id,
  (id) => {
    if (!id || typeof localStorage === 'undefined') return
    try {
      const raw = localStorage.getItem(foldStorageKey('', props.part))
      if (raw === '1') {
        userToggled.value = true
        userWantsOpen.value = true
      } else if (raw === '0') {
        userToggled.value = true
        userWantsOpen.value = false
      }
    } catch { /* localStorage may throw in private mode */ }
  },
  { immediate: true },
)

// Persist toggle. Wrap in try/catch for the same private
// mode reason. Fire on every toggle; cheap (one key write).
watch([userToggled, userWantsOpen], () => {
  if (!userToggled.value) return
  if (typeof localStorage === 'undefined') return
  try {
    localStorage.setItem(foldStorageKey('', props.part), userWantsOpen.value ? '1' : '0')
  } catch { /* ignore */ }
})

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

// P2-4: a dry-run call is signalled by a `dry_run: true`
// arg. The handler returns a preview string starting
// with "[dry-run] would …" without actually executing
// anything. We surface that distinction as a small
// chip on the card header so the user can tell at a
// glance that the tool was inspected but not run.
const isDryRun = computed(() => {
  const a = props.part.args
  if (!a) return false
  try {
    const parsed = JSON.parse(a)
    return !!parsed?.dry_run
  } catch {
    return false
  }
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

// Copy result to clipboard. Used both as a header
// affordance (so the user can grab a long result without
// expanding it) and as an in-body button on the
// expanded view. Stops propagation so clicking the copy
// button doesn't toggle the fold.
const copyState = ref<'idle' | 'copied' | 'err'>('idle')
async function copyResult() {
  const r = props.part.result
  if (!r) return
  try {
    await navigator.clipboard.writeText(r)
    copyState.value = 'copied'
    setTimeout(() => (copyState.value = 'idle'), 1200)
  } catch {
    copyState.value = 'err'
    setTimeout(() => (copyState.value = 'idle'), 1200)
  }
}
</script>

<template>
  <div class="tool-card" :class="['status-' + part.status, { foldable: shouldFoldResult, collapsed: !open }]">
    <button class="tool-header" @click="toggle" :title="open ? '收起' : '展开'">
      <span class="tool-icon" :class="part.status">
        <component :is="statusIcon" v-if="statusIcon" :size="11" :class="part.status === 'start' ? 'spin' : ''" />
      </span>
      <span class="tool-name">{{ part.name }}</span>
      <span v-if="isDryRun" class="tool-dry-run" title="仅预览,未实际执行">dry-run</span>
      <span class="tool-status">{{ statusLabel }}</span>
      <span class="tool-elapsed" v-if="part.elapsed">{{ part.elapsed }}</span>
      <span
        v-if="part.result && shouldFoldResult"
        class="tool-copy"
        :title="'复制结果'"
        @click.stop="copyResult"
      >
        <component :is="Clipboard" :size="11" v-if="copyState === 'idle'" />
        <span v-else-if="copyState === 'copied'" class="tool-copy-state">已复制</span>
        <span v-else class="tool-copy-state">失败</span>
      </span>
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
/* P2-4 dry-run chip. Pill-shaped, brand-50
 * background so it reads as "informational" — the
 * user should know this tool was NOT executed. The
 * chip is on the header next to the tool name so
 * it's visible at a glance even when the body is
 * collapsed. */
.tool-dry-run {
  display: inline-flex;
  align-items: center;
  padding: 1px 6px;
  border-radius: 999px;
  background: var(--brand-50);
  color: var(--brand-600);
  font-size: 10.5px;
  font-weight: 500;
  margin-left: 4px;
  flex-shrink: 0;
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

/* P1-1 fold affordances. The foldable class is set when
 * the result body is "long enough" to default-collapse.
 * The collapsed class is purely visual: it removes the
 * body slot from layout when the user has folded the
 * card. (The body itself is `v-if="open"` so it's not
 * rendered at all in the collapsed state — the class is
 * belt-and-suspenders for screen-reader / focus state
 * styling.) The caret in the header is what the user
 * clicks to expand/collapse. */
.tool-card.collapsed .tool-body { display: none; }
.tool-copy {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  margin-left: 4px;
  padding: 2px 4px;
  border-radius: var(--radius-sm, 4px);
  color: var(--text-tertiary);
  font-size: 10.5px;
  line-height: 1;
  cursor: pointer;
  transition: background var(--dur-fast, 120ms) var(--ease-out, ease);
}
.tool-copy:hover {
  background: var(--surface-3, rgba(0, 0, 0, 0.05));
  color: var(--text-primary, inherit);
}
.tool-copy-state {
  font-size: 10px;
  padding: 0 2px;
  color: var(--text-secondary, inherit);
}
</style>
