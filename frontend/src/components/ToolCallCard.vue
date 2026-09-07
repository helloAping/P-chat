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
import {
  Check,
  X,
  AlertTriangle,
  Loader2,
  ChevronRight,
  ChevronDown,
  Clipboard,
  Download,
  Maximize2,
  ImageIcon,
  Film,
  Volume2,
} from './icons'
import { downloadFromUrl, extensionForMime } from '../utils/clipboard'

const props = defineProps<{ part: ToolPart }>()

// Auto-fold threshold: long shell/wiki/file output. Picked
// from observation — a `cat` of a 30-line file is ~1200
// chars and we want it folded; a JSON tool result that
// fits in a tooltip (~150 chars) is fine open.
const FOLD_RESULT_MIN_CHARS = 200
const FOLD_RESULT_MIN_LINES = 4
const isGenerationTool = computed(() => props.part.name.startsWith('generate_'))
const isBrowserScreenshot = computed(() => props.part.name === 'browser_screenshot')
const isMediaResultTool = computed(() => isGenerationTool.value || isBrowserScreenshot.value)

// Whether the result is "long enough" to warrant a
// default-collapsed state. The args block is not folded —
// it's typically 1-3 lines and not the noise.
const shouldFoldResult = computed(() => {
  // Generated media is the primary result, so keep it visible without making
  // the user expand a JSON-shaped tool response first.
  if (isMediaResultTool.value) return false
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
    case 'blocked': return '已关闭'
    default:      return props.part.status
  }
})

const statusIcon = computed(() => {
  switch (props.part.status) {
    case 'start': return Loader2
    case 'ok':    return Check
    case 'warn':  return AlertTriangle
    case 'error': return X
    case 'blocked': return X
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

type ToolMediaAsset = {
  id?: string
  kind: 'image' | 'video' | 'audio'
  mime_type?: string
  name?: string
  url: string
  source: 'generation' | 'browser_screenshot'
}

function isRenderableMediaURL(url: string): boolean {
  return url.startsWith('/api/v1/generated/') || url.startsWith('/api/v1/uploads/') ||
    url.startsWith('data:image/') || url.startsWith('blob:')
}

// Tool media has one presentation contract. New browser screenshots are
// materialized by the agent and arrive in the same {assets:[...]} envelope as
// generated media. The data:/blob: branches keep old conversation history
// readable without reintroducing inline bytes for new calls.
const toolMediaAssets = computed<ToolMediaAsset[]>(() => {
  const result = props.part.result
  if (!result || !isMediaResultTool.value) return []
  try {
    const parsed = JSON.parse(result)
    if (Array.isArray(parsed?.assets)) {
      return parsed.assets
        .filter((asset: any) => asset && ['image', 'video', 'audio'].includes(asset.kind) && typeof asset.url === 'string' && isRenderableMediaURL(asset.url))
        .map((asset: any) => ({
          ...asset,
          source: isBrowserScreenshot.value ? 'browser_screenshot' : 'generation',
        }))
    }
    if (isBrowserScreenshot.value && typeof parsed?.image === 'string' && isRenderableMediaURL(parsed.image)) {
      return [{ kind: 'image', mime_type: 'image/jpeg', name: 'browser-screenshot.jpg', url: parsed.image, source: 'browser_screenshot' }]
    }
  } catch { /* legacy raw screenshot result */ }
  if (isBrowserScreenshot.value && isRenderableMediaURL(result)) {
    const mime = result.startsWith('data:') ? result.slice(5, result.indexOf(';')) : 'image/jpeg'
    return [{ kind: 'image', mime_type: mime || 'image/jpeg', name: 'browser-screenshot.jpg', url: result, source: 'browser_screenshot' }]
  }
  return []
})

const generatedKindLabels: Record<ToolMediaAsset['kind'], string> = {
  image: '生成图片',
  video: '生成视频',
  audio: '生成音频',
}

function toolMediaAssetLabel(asset: ToolMediaAsset): string {
  return asset.source === 'browser_screenshot' ? '浏览器截图' : generatedKindLabels[asset.kind]
}

function toolMediaAssetIcon(asset: ToolMediaAsset) {
  if (asset.kind === 'video') return Film
  if (asset.kind === 'audio') return Volume2
  return ImageIcon
}

function toolMediaAssetExtension(asset: ToolMediaAsset): string {
  const mimeExtension = extensionForMime(asset.mime_type || '')
  if (mimeExtension !== '.bin') return mimeExtension
  const originalExtension = asset.name?.match(/\.[a-z0-9]{1,8}$/i)?.[0]
  if (originalExtension) return originalExtension.toLowerCase()
  if (asset.kind === 'video') return '.mp4'
  if (asset.kind === 'audio') return '.mp3'
  return '.png'
}

function toolMediaAssetFileName(asset: ToolMediaAsset): string {
  const identity = (asset.id || 'result').replace(/[^a-z0-9]/gi, '').slice(0, 8) || 'result'
  const type = asset.source === 'browser_screenshot' ? 'screenshot' : asset.kind
  return `pchat-${type}-${identity}${toolMediaAssetExtension(asset)}`
}

function toolMediaAssetMeta(asset: ToolMediaAsset): string {
  const format = (asset.mime_type?.split('/')[1] || toolMediaAssetExtension(asset).slice(1)).toUpperCase()
  return `${toolMediaAssetLabel(asset)} · ${format}`
}

function openToolMediaAsset(asset: ToolMediaAsset) {
  if (asset.kind === 'audio') return
  state.lightbox = {
    show: true,
    src: asset.url,
    alt: toolMediaAssetFileName(asset),
    kind: asset.kind,
  }
}

function downloadToolMediaAsset(asset: ToolMediaAsset) {
  downloadFromUrl(asset.url, toolMediaAssetFileName(asset))
}

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

// Truncated large results: the server omits the full body
// (>32 KiB) and the card shows a "查看完整输出" affordance.
// The full body is fetched on demand and held in a LOCAL ref
// (not the reactive store) so a multi-MB string never lands
// in the Vue state. The message id is resolved from the chat
// store's trailing assistant message on click.
import * as api from '../api/client'
import { state } from '../stores/chat'
const fetchState = ref<'idle' | 'loading' | 'ok' | 'err'>('idle')
const fullResult = ref('')
const resultTruncated = computed(() => !!(props.part as any).result_truncated)
const resultFullLen = computed(() => (props.part as any).result_full_len as number | undefined)
const truncatedLabel = computed(() => {
  const len = resultFullLen.value
  if (!len) return '查看完整输出'
  const kb = (len / 1024).toFixed(len >= 1024 * 1024 ? 1 : 0)
  return len >= 1024 * 1024 ? `查看完整输出 (${(len / 1048576).toFixed(1)} MB)` : `查看完整输出 (${kb} KB)`
})
async function fetchFullResult() {
  if (fetchState.value === 'loading' || fetchState.value === 'ok') return
  const sid = state.currentID
  const toolId = props.part.tool_id || props.part.id
  if (!sid || !toolId) return
  fetchState.value = 'loading'
  try {
    // Resolve the trailing assistant message id (the SSE done
    // event stamps it; fall back to 0 and let the server's
    // tool_id lookup carry the session check).
    let msgId = 0
    const msgs = state.sessionMessages[sid]
    if (msgs) {
      for (let i = msgs.length - 1; i >= 0; i--) {
        const mid = msgs[i].id
        if (msgs[i].role === 'assistant' && mid) {
          msgId = mid
          break
        }
      }
    }
    const resp = await api.getToolResult(sid, msgId, toolId)
    fullResult.value = resp.content
    fetchState.value = 'ok'
  } catch {
    fetchState.value = 'err'
    setTimeout(() => (fetchState.value = 'idle'), 2000)
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
      <details v-if="part.args && isGenerationTool && toolMediaAssets.length" class="generation-request-details">
        <summary>查看生成参数</summary>
        <pre>{{ argsPretty }}</pre>
      </details>
      <div v-else-if="part.args" class="tool-args">
        <div class="tool-section-label">参数</div>
        <pre>{{ argsPretty }}</pre>
      </div>
      <div v-if="part.result" class="tool-result">
        <div class="tool-section-label">结果</div>
        <div v-if="toolMediaAssets.length" class="generated-assets">
          <figure v-for="asset in toolMediaAssets" :key="asset.id || asset.url" class="generated-asset">
            <button
              v-if="asset.kind === 'image'"
              class="generated-asset-preview"
              type="button"
              :aria-label="`预览 ${toolMediaAssetFileName(asset)}`"
              @click="openToolMediaAsset(asset)"
            >
              <img :src="asset.url" :alt="toolMediaAssetFileName(asset)" loading="lazy" />
            </button>
            <div v-else-if="asset.kind === 'video'" class="generated-asset-preview generated-asset-preview--video">
              <video :src="asset.url" controls preload="metadata" />
            </div>
            <div v-else class="generated-asset-preview generated-asset-preview--audio">
              <audio :src="asset.url" controls preload="metadata" />
            </div>
            <figcaption class="generated-asset-footer">
              <span class="generated-asset-identity">
                <span class="generated-asset-icon" aria-hidden="true">
                  <component :is="toolMediaAssetIcon(asset)" :size="14" />
                </span>
                <span class="generated-asset-copy">
                  <span class="generated-asset-name" :title="toolMediaAssetFileName(asset)">
                    {{ toolMediaAssetFileName(asset) }}
                  </span>
                  <span class="generated-asset-meta">{{ toolMediaAssetMeta(asset) }}</span>
                </span>
              </span>
              <span class="generated-asset-actions">
                <button
                  v-if="asset.kind !== 'audio'"
                  class="generated-asset-action"
                  type="button"
                  @click="openToolMediaAsset(asset)"
                >
                  <Maximize2 :size="13" />
                  <span>查看</span>
                </button>
                <button
                  class="generated-asset-action generated-asset-action--download"
                  type="button"
                  @click="downloadToolMediaAsset(asset)"
                >
                  <Download :size="13" />
                  <span>下载</span>
                </button>
              </span>
            </figcaption>
          </figure>
        </div>
        <pre v-else-if="fetchState === 'ok'">{{ fullResult }}</pre>
        <pre v-else>{{ part.result }}</pre>
        <button
          v-if="resultTruncated"
          class="tool-full-result-btn"
          :disabled="fetchState === 'loading'"
          @click.stop="fetchFullResult"
        >
          <Loader2 v-if="fetchState === 'loading'" :size="11" class="spin" />
          <span v-else-if="fetchState === 'err'">加载失败，点击重试</span>
          <span v-else-if="fetchState === 'ok'">已加载完整输出</span>
          <span v-else>{{ truncatedLabel }}</span>
        </button>
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
.tool-card.status-blocked { border-left: 3px solid var(--warn-500); }
.tool-card.status-blocked { border-left: 3px solid var(--warn-500); }
.tool-card.status-blocked { border-left: 3px solid var(--warn-500); }

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

/* "查看完整输出" affordance for server-truncated results.
 * Compact ghost button under the result body. */
.tool-full-result-btn {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  margin-top: 6px;
  padding: 2px 10px;
  border: 1px solid var(--border-default);
  border-radius: 6px;
  background: transparent;
  color: var(--brand-600);
  font-size: 11.5px;
  cursor: pointer;
  transition: background var(--dur-fast) var(--ease-out);
}
.tool-full-result-btn:hover:not(:disabled) {
  background: var(--brand-50);
}
.tool-full-result-btn:disabled {
  opacity: 0.6;
  cursor: default;
}

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
.tool-args pre, .tool-result pre, .tool-error pre, .generation-request-details pre {
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
.generation-request-details {
  margin-bottom: var(--space-2);
  color: var(--text-tertiary);
  font-size: 11.5px;
}
.generation-request-details summary {
  width: fit-content;
  margin-bottom: var(--space-1);
  cursor: pointer;
  color: var(--text-secondary);
  transition: var(--transition-colors);
}
.generation-request-details summary:hover {
  color: var(--text-primary);
}
.generated-assets {
  display: grid;
  gap: var(--space-2);
}
.generated-asset {
  display: grid;
  margin: 0;
  overflow: hidden;
  background: var(--surface-1);
  border: 1px solid var(--border-default);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-sm);
}
.generated-asset-preview {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 100%;
  min-width: 0;
  padding: 0;
  overflow: hidden;
  appearance: none;
  background: var(--surface-0);
  border: 0;
  color: inherit;
  cursor: zoom-in;
}
.generated-asset-preview--video,
.generated-asset-preview--audio {
  cursor: default;
}
.generated-asset-preview img,
.generated-asset-preview video {
  display: block;
  width: 100%;
  max-height: calc(var(--space-8) * 11);
  object-fit: contain;
  background: var(--surface-0);
}
.generated-asset-preview img {
  transition: transform var(--dur-base) var(--ease-out);
}
.generated-asset-preview:hover img {
  transform: scale(1.01);
}
.generated-asset-preview--audio {
  padding: var(--space-4);
}
.generated-asset-preview audio {
  width: 100%;
}
.generated-asset-footer {
  display: flex;
  align-items: center;
  justify-content: space-between;
  flex-wrap: wrap;
  gap: var(--space-2);
  padding: var(--space-2) var(--space-3);
  background: var(--surface-1);
  border-top: 1px solid var(--border-subtle);
}
.generated-asset-identity {
  display: flex;
  align-items: center;
  flex: 1 1 auto;
  min-width: 0;
  gap: var(--space-2);
}
.generated-asset-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: calc(var(--space-7) - var(--space-1));
  height: calc(var(--space-7) - var(--space-1));
  flex: 0 0 auto;
  border-radius: var(--radius-sm);
  background: var(--brand-50);
  color: var(--brand-600);
}
.generated-asset-copy {
  display: grid;
  min-width: 0;
  gap: var(--space-1);
}
.generated-asset-name {
  overflow: hidden;
  color: var(--text-primary);
  font-family: var(--font-mono);
  font-size: 11.5px;
  font-weight: 500;
  line-height: 1.3;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.generated-asset-meta {
  color: var(--text-tertiary);
  font-size: 11px;
  line-height: 1.3;
}
.generated-asset-actions {
  display: flex;
  align-items: center;
  flex: 0 0 auto;
  gap: var(--space-1);
  margin-left: auto;
}
.generated-asset-action {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  height: calc(var(--space-7) - var(--space-1));
  gap: var(--space-1);
  padding: 0 var(--space-2);
  border: 1px solid var(--border-default);
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--text-secondary);
  font-family: var(--font-sans);
  font-size: 11.5px;
  cursor: pointer;
  transition: var(--transition-colors);
}
.generated-asset-action:hover {
  background: var(--surface-3);
  color: var(--text-primary);
}
.generated-asset-action--download {
  border-color: var(--brand-100);
  background: var(--brand-50);
  color: var(--brand-600);
}
.generated-asset-action--download:hover {
  background: var(--brand-100);
  color: var(--brand-600);
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
  transition: background var(--dur-fast) var(--ease-out);
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
