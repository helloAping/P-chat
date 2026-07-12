<script setup lang="ts">
// MessageBubble renders one Message. The assistant
// message body is built from a flat parts array
// (text + thinking + tool calls + sub-agents), each
// rendered by a dedicated sub-component. User / system
// messages still go through the markdown pipeline.
//
// Streaming model: the chat UI doesn't run its own
// typewriter animation. The "typewriter" effect is
// the natural SSE stream itself — the LLM emits
// content chunk by chunk over the network, the chat
// store appends each chunk to the trailing text part,
// and the DOM re-renders on each tick. The user sees
// the text grow in real time, exactly like ChatGPT.
//
// Concretely:
//   - `TypedText` is a thin wrapper around
//     `marked.parse()` plus a blinking `::after` caret.
//     It renders the text verbatim and updates the
//     DOM as `props.text` grows. No rAF loop, no
//     artificial delay.
//   - The caret is shown only on the *trailing* text
//     part of an actively-streaming message. Earlier
//     text parts (e.g. before a tool call) and post-
//     stream text use the static markdown render.
//   - The placeholder `TypedText` (empty text) is
//     shown before the first SSE event arrives, so
//     the user sees a blinking caret alone in the
//     bubble as soon as they hit send.
import { computed, nextTick, ref, useTemplateRef, watch } from 'vue'
import { marked } from 'marked'
import {
  ImageIcon, Volume2, Film, FileText, File,
  Clipboard, Download, AlertTriangle, Undo2, GitBranch,
  ArrowDown, ArrowUp, RotateCcw, Pencil, MoreHorizontal, Sparkles,
  Check, Loader2, XCircle,
} from './icons'
import RoleAvatar from './RoleAvatar.vue'

// Markdown render cache. The MessageBubble template previously
// called `marked.parse(p.text || '')` on every render — for
// long sessions with many static text parts, this is a major
// source of jank because marked.parse is O(text length) and
// Vue re-evaluates the v-html expression any time any reactive
// dep in the component ticks. Cache by text content; cap the
// cache at 256 entries to bound memory for very long sessions.
//
// LRU-ish: a Map preserves insertion order, so we can pop the
// oldest entry when over the cap.
const MD_CACHE_MAX = 256
const mdCache = new Map<string, string>()
function renderMd(text: string): string {
  if (!text) return ''
  const cached = mdCache.get(text)
  if (cached !== undefined) {
    // Touch: move to end of Map to mark as recently used.
    mdCache.delete(text)
    mdCache.set(text, cached)
    return cached
  }
  const html = marked.parse(text, { async: false, breaks: true }) as string
  mdCache.set(text, html)
  if (mdCache.size > MD_CACHE_MAX) {
    const oldest = mdCache.keys().next().value
    if (oldest !== undefined) mdCache.delete(oldest)
  }
  return html
}
import { useMessage, useDialog } from 'naive-ui'
import type { Message, MessageAttachment, MessagePart } from '../api/client'
import { state } from '../stores/chat'
import ThinkingBlock from './ThinkingBlock.vue'
import ToolCallCard from './ToolCallCard.vue'
import SubAgentCard from './SubAgentCard.vue'
import QuestionTable from './QuestionTable.vue'
import ExecOutputCard from './ExecOutputCard.vue'
import TypedText from './TypedText.vue'
import {
  copyImageToClipboard, copyText, downloadBlob, downloadFromUrl,
  extensionForMime, fetchAsBlob,
} from '../utils/clipboard'

const props = defineProps<{ message: Message; streaming?: boolean }>()
const emit = defineEmits<{
  rollback: []
  fork: []
  /** Re-issue the same prompt to the LLM (assistant only).
   * The parent ChatWindow wires this to a `regenerate`
   * action that resets the trailing assistant message
   * and re-streams it. */
  regenerate: []
  /** Inline-edit a user message and re-send. */
  edit: []
}>()

function onRollback() {
  const d = useDialog()
  d.warning({
    title: '确认撤回',
    content: '确定撤回此消息及之后的所有回复？此操作可撤销。',
    positiveText: '确认撤回',
    negativeText: '取消',
    onPositiveClick: () => {
      emit('rollback')
    },
  })
}
function onFork() {
  pulseAction('fork')
  emit('fork')
}
function onRegenerate() {
  pulseAction('regen')
  emit('regenerate')
}
function onEdit() {
  pulseAction('edit')
  emit('edit')
}

// --- Action-button interaction state machine ---------------------
//
// Each button has 3 transient states that drive a different
// icon (and CSS colour):
//
//   idle     — default clipboard / pencil / undo2 icon.
//   feedback — operation succeeded; show a Check for 1.2s,
//              then auto-restore. Drives the copy button and
//              any other success-path button.
//   pending  — operation is awaiting a second click (rollback
//              uses this to require double-click confirmation).
//
// The timers are tracked so a rapid sequence of clicks
// doesn't leave stale timeouts firing after a newer one
// has already restored the state.
//
// `pulseKey` is a per-message counter bumped on every button
// press. It's bound to the `key` attribute of a hidden
// span so the CSS animation re-triggers on every press
// (CSS keyframes only re-fire when the element is created
// or the key changes — Vue's `:key` is the natural way to
// restart them).
type ActionState = 'idle' | 'feedback' | 'pending'
const actionState = ref<Record<string, ActionState>>({})
const actionTimers: Record<string, ReturnType<typeof setTimeout> | null> = {}
const pulseKey = ref(0)

function setActionState(key: string, next: ActionState, autoMs = 0) {
  if (actionTimers[key]) {
    clearTimeout(actionTimers[key]!)
    actionTimers[key] = null
  }
  actionState.value = { ...actionState.value, [key]: next }
  pulseKey.value++
  if (autoMs > 0) {
    actionTimers[key] = setTimeout(() => {
      actionState.value = { ...actionState.value, [key]: 'idle' }
      actionTimers[key] = null
    }, autoMs)
  }
}

import { onBeforeUnmount } from 'vue'
onBeforeUnmount(() => {
})

function pulseAction(key: string) {
  // Short tactile pulse: re-triggers the press animation
  // without changing the icon. Used for regen / fork /
  // edit — these are synchronous emits whose feedback is
  // the immediate downstream UI change (e.g. streaming
  // restart), not a visible icon swap.
  pulseKey.value++
}

function isAction(key: string, state: ActionState): boolean {
  return actionState.value[key] === state
}
// toast is the Naive UI useMessage() handle. Used to
// surface "已复制"/"已下载" feedback at the top of the
// screen. Named `toast` rather than `message` so it
// doesn't shadow `props.message` (the chat Message).
const toast = useMessage()

// isLiveTextPart returns true ONLY for the very last
// part of an actively-streaming message AND that part
// is a text part. This is the single part that should
// render through `TypedText` (so the caret is visible).
//
// Why "last part" not "last text part": in a multi-round
// turn like [text(r1), tool(r1), text(r2)], the trailing
// text is round 2's reply — it's streaming now but will
// become "static" once Done lands. The trailing-text check
// alone (the old behaviour) would incorrectly keep that
// part live even when a new question/confirm round starts
// after the reply finishes. Indexing on `parts.length - 1`
// is the correct invariant: only one part is ever
// actively streaming at a time.
function isLiveTextPart(idx: number, kind: string, parts: MessagePart[] | undefined): boolean {
  if (kind !== 'text') return false
  if (!props.streaming) return false
  if (!parts || parts.length === 0) return false
  return idx === parts.length - 1
}

// isLiveThinkingPart mirrors isLiveTextPart: only the
// very last part AND it must be a thinking part. While
// streaming, that trailing part renders through
// `ThinkingBlock` with its shimmer effect; once
// streaming ends or a new round appends a non-thinking
// part, this returns false and the block falls back to
// its collapsed static view.
function isLiveThinkingPart(idx: number, kind: string, parts: MessagePart[] | undefined): boolean {
  if (kind !== 'thinking') return false
  if (!props.streaming) return false
  if (!parts || parts.length === 0) return false
  return idx === parts.length - 1
}

// The role check: system messages get a special icon.
const isSystem = computed(() => (props.message.msg_type ?? 0) === 0 && props.message.role === 'system')

// For user / system messages, the markdown pipeline
// renders the whole `content` string. For assistant
// messages, we render `parts` instead. (We still
// support legacy assistant messages that have only
// `content` and no `parts` — the server's history
// endpoint returns content-only messages, so the
// fallback is important.)
const assistantHtml = computed(() => '')

const userHtml = computed(() => renderMd(props.message.content || ''))

// Attachments (images / files) — only used by user
// messages today, but kept general.
function openLightbox(src: string, alt: string, kind: 'image' | 'video' = 'image') {
  state.lightbox = { show: true, src, alt, kind }
}
function thumbText(kind?: string) {
  switch (kind) {
    case 'image': return ImageIcon
    case 'audio': return Volume2
    case 'video': return Film
    case 'text':  return FileText
    default:      return File
  }
}

// --- Copy / download for attachments ------------------------

// friendlyAttachmentName picks a sensible filename for a
// download when the original name is missing or weird.
function friendlyAttachmentName(a: MessageAttachment): string {
  if (a.name && a.name.trim()) return a.name
  const mime = a.mime || (a.kind === 'image' ? 'image/png'
    : a.kind === 'audio' ? 'audio/mpeg'
    : a.kind === 'video' ? 'video/mp4' : 'text/plain')
  const stem = a.kind && a.kind !== 'file' ? a.kind : 'attachment'
  return `${stem}-${Date.now()}${extensionForMime(mime)}`
}

async function copyAttachment(a: MessageAttachment) {
  if (a.type === 'text') {
    if (a.text) {
      const ok = await copyText(a.text)
      toast[ok ? 'success' : 'error'](ok ? '已复制' : '复制失败')
      return
    }
  }
  if (!a.url) {
    toast.error('没有可复制的内容')
    return
  }
  // Image attachments can go on the system clipboard
  // via the ClipboardItem API. Audio/video fall back to
  // a regular download (no browser-side clipboard
  // support for those).
  if (a.type === 'image_url') {
    try {
      const blob = await fetchAsBlob(a.url)
      const ok = await copyImageToClipboard(blob)
      if (ok) {
        toast.success('已复制到剪贴板')
        return
      }
    } catch { /* fall through to download */ }
    downloadFromUrl(a.url, friendlyAttachmentName(a))
    toast.info('剪贴板不支持图片，已改为下载')
    return
  }
  downloadFromUrl(a.url, friendlyAttachmentName(a))
  toast.success('已下载')
}

async function downloadAttachment(a: MessageAttachment) {
  if (a.type === 'text' && a.text) {
    const blob = new Blob([a.text], { type: a.mime || 'text/plain' })
    downloadBlob(blob, friendlyAttachmentName(a))
    toast.success('已下载')
    return
  }
  if (!a.url) {
    toast.error('没有可下载的内容')
    return
  }
  if (a.url.startsWith('data:')) {
    downloadFromUrl(a.url, friendlyAttachmentName(a))
    toast.success('已下载')
    return
  }
  try {
    const blob = await fetchAsBlob(a.url)
    downloadBlob(blob, friendlyAttachmentName(a))
    toast.success('已下载')
  } catch (e: any) {
    toast.error(`下载失败: ${e?.message || e}`)
  }
}

// --- Copy whole message -------------------------------------

// messageMarkdownText returns a clean text representation
// of the message: for user messages that's the raw
// `content`, for assistant messages it's the joined text
// parts. Attachments and tool calls are skipped.
function messageMarkdownText(): string {
  const m = props.message
  if (m.role === 'user' || m.role === 'system' || m.role === 'tool') {
    return m.content || ''
  }
  if (m.parts && m.parts.length) {
    return m.parts
      .filter((p: any) => p.kind === 'text')
      .map((p: any) => p.text || '')
      .join('\n\n')
  }
  return m.content || ''
}

async function copyEntireMessage() {
  // Bail out if we're already showing the success state —
  // a second click during the 1.2s feedback window
  // shouldn't cancel the feedback or restart the timer.
  if (isAction('copy', 'feedback')) return
  const text = messageMarkdownText()
  if (!text) {
    toast.error('消息为空')
    return
  }
  const ok = await copyText(text)
  if (ok) {
    // Local visual feedback (icon swap → Check, 1.2s).
    // The toast is kept as a secondary signal but the
    // icon swap is the primary cue — toasts can race with
    // the user's mouse movement (the toolbar fades out
    // on mouseleave), so the icon must be visible at the
    // moment of click without depending on the toast.
    setActionState('copy', 'feedback', 1200)
  } else {
    toast.error('复制失败')
  }
}

// --- Code-block toolbar injection ---------------------------

// mdBodyEl is the ref on every <div class="md-body"
// v-html="..."> in the template. We watch it and inject
// a copy / download toolbar into each <pre> child.
// Marked's output for fenced code blocks is
// <pre><code class="language-xxx">…</code></pre>; we leave
// the <pre>/<code> alone and prepend the toolbar.
const mdBodyEl = useTemplateRef<HTMLElement>('mdBodyEl')

// processedPres tracks <pre> nodes that already have a
// toolbar attached, so we don't double-inject on every
// watcher tick. WeakSet keeps it from preventing GC on
// the <pre> nodes (which get replaced when the message
// updates).
const processedPres = new WeakSet<HTMLPreElement>()

function injectCodeToolbars(root: HTMLElement | null) {
  if (!root) return
  const pres = root.querySelectorAll('pre')
  pres.forEach((pre) => {
    if (processedPres.has(pre)) return
    const wrapper = document.createElement('div')
    wrapper.className = 'code-block'
    const toolbar = document.createElement('div')
    toolbar.className = 'code-toolbar'
    const copyBtn = document.createElement('button')
    copyBtn.type = 'button'
    copyBtn.className = 'code-btn code-btn-copy'
    copyBtn.textContent = '复制'
    copyBtn.setAttribute('data-code-action', 'copy')
    const dlBtn = document.createElement('button')
    dlBtn.type = 'button'
    dlBtn.className = 'code-btn code-btn-download'
    dlBtn.textContent = '下载'
    dlBtn.setAttribute('data-code-action', 'download')
    toolbar.appendChild(copyBtn)
    toolbar.appendChild(dlBtn)
    pre.parentNode?.insertBefore(wrapper, pre)
    wrapper.appendChild(toolbar)
    wrapper.appendChild(pre)
    processedPres.add(pre)
  })
}

// onCodeClick handles clicks on the toolbar buttons via
// event delegation. One listener per md-body covers every
// <pre> inside.
async function onCodeClick(e: Event) {
  const target = e.target as HTMLElement
  const btn = target.closest<HTMLButtonElement>('[data-code-action]')
  if (!btn) return
  const action = btn.getAttribute('data-code-action')
  const pre = btn.closest('pre')
  if (!pre) return
  const code = pre.querySelector('code')
  const text = code?.textContent || pre.textContent || ''
  if (action === 'copy') {
    const ok = await copyText(text)
    btn.textContent = ok ? '已复制' : '失败'
    setTimeout(() => { btn.textContent = '复制' }, 1200)
  } else if (action === 'download') {
    const langClass = Array.from(code?.classList || []).find(c => c.startsWith('language-'))
    const lang = langClass ? langClass.slice('language-'.length) : ''
    const ext = langExt(lang) || '.txt'
    const blob = new Blob([text], { type: 'text/plain;charset=utf-8' })
    downloadBlob(blob, `snippet-${Date.now()}${ext}`)
  }
}

// langExt maps common marked.js language class names to
// their file extension.
function langExt(lang: string): string {
  const l = lang.toLowerCase()
  const map: Record<string, string> = {
    py: '.py', python: '.py',
    js: '.js', javascript: '.js', jsx: '.jsx',
    ts: '.ts', typescript: '.ts', tsx: '.tsx',
    go: '.go',
    rs: '.rs', rust: '.rs',
    java: '.java',
    rb: '.rb', ruby: '.rb',
    sh: '.sh', bash: '.sh', zsh: '.sh',
    json: '.json',
    yaml: '.yaml', yml: '.yml',
    toml: '.toml',
    xml: '.xml',
    html: '.html', htm: '.html',
    css: '.css', scss: '.scss', less: '.less',
    md: '.md', markdown: '.md',
    sql: '.sql',
    txt: '.txt', text: '.txt',
    vue: '.vue', svelte: '.svelte',
  }
  return map[l] || ''
}

// Re-inject toolbars whenever the markdown HTML changes
// (new SSE chunk, message re-render, etc.). The
// processedPres set keeps re-runs cheap — each <pre> is
// only touched once.
watch(mdBodyEl, async (el) => {
  await nextTick()
  injectCodeToolbars(el)
}, { flush: 'post' })

watch(userHtml, async () => {
  await nextTick()
  injectCodeToolbars(mdBodyEl.value)
})
function shortWarnText(t?: string): string {
  if (!t) return 'image skipped'
  const m = t.match(/\(attached image: ([^,]+)/)
  const name = m ? m[1].trim() : 'image'
  return `${name} · model does not support image input`
}

// showTypewriterPlaceholder is the "ready to receive
// SSE" indicator: a blinking caret (`▍`) sitting alone
// in the bubble while the assistant message has no
// parts yet. It replaces the old three-bouncing-dots
// loader — the typewriter cursor *is* the loader, and
// the natural SSE stream takes over the moment the
// first content chunk arrives.
const showTypewriterPlaceholder = computed(() => {
  if (props.streaming !== true) return false
  if (props.message.role !== 'assistant') return false
  const parts = props.message.parts
  if (!parts || parts.length === 0) return true
  return !parts.some(p =>
    p.kind === 'text' || p.kind === 'thinking' ||
    p.kind === 'tool' || p.kind === 'sub_agent',
  )
})

const statusLines = computed(() => {
  const m = props.message as any
  if (!m._statusText || !m._statusText.length) return []
  // Show only the last 5 lines to keep the bar compact.
  const lines = m._statusText as string[]
  return lines.slice(-5)
})

// Token usage badge — only show if we have it.
const hasTokens = computed(() =>
  props.message.role === 'assistant'
  && (props.message.tokens_in || props.message.tokens_out)
)

// "Image not supported" — when the LLM rejects the
// user's image with the "this model does not support
// image input" error, the chat store tags the trailing
// user message with `visionUnsupported: true`. We
// surface a dedicated chip under the attachments so
// the user can see *why* the image was ignored, even
// after the toast disappears.
const showVisionWarn = computed(() =>
  props.message.role === 'user' && props.message.visionUnsupported === true,
)

// showAssistantHeader — the small "Assistant · Claude 3.5
// · 2.3s" line that sits above the assistant's body. Only
// shown for assistant messages; user messages get a
// right-aligned bubble with the avatar as the only "label".
const showAssistantHeader = computed(() =>
  props.message.role === 'assistant' && !isSystem.value
)

// Action-bar visibility:
//   - copy:   always available (copies the visible text)
//   - regen:  assistant only, not while streaming
//   - edit:   user only, not the trailing message while
//             streaming (we don't let the user edit a
//             message that's still being sent)
//   - fork:   user only (PR #2 feature, kept)
//   - more:   reserved for future (model switch, etc.)
const canRegenerate = computed(() =>
  props.message.role === 'assistant' && !props.streaming
)
const canEdit = computed(() =>
  props.message.role === 'user' && !props.streaming
)
const canFork = computed(() =>
  props.message.role === 'user' && !props.streaming
)
const canRollback = computed(() =>
  props.message.role === 'user' && !props.streaming
)
</script>

<template>
  <div class="msg" :class="[message.role, { streaming }]">
    <!-- Avatar: 32px circle that identifies the role. The
         system role doesn't show an avatar — instead it
         uses a left accent bar to mark itself. -->
    <RoleAvatar v-if="!isSystem" :role="message.role" :size="32" />

    <div class="bubble-col">
      <!-- Assistant header: role label + model + elapsed.
           Anchors the conversation by showing which model
           the reply came from. Hidden for system / tool
           messages (system uses a different visual
           treatment; tool messages are uncommon). -->
      <div v-if="showAssistantHeader" class="bubble-header">
        <span class="bubble-role">
          <Sparkles :size="12" class="bubble-role-icon" />
          Assistant
        </span>
        <span v-if="message.model" class="bubble-sep">·</span>
        <span v-if="message.model" class="bubble-model">{{ message.model }}</span>
        <span v-if="message.elapsed" class="bubble-sep">·</span>
        <span v-if="message.elapsed" class="bubble-elapsed">{{ message.elapsed }}</span>
        <span v-if="streaming" class="bubble-stream-dot" :title="'正在生成'" aria-label="正在生成" />
      </div>

      <div class="bubble">
        <div v-if="isSystem" class="system-icon">›</div>
        <div class="bubble-body">
          <!-- Attachments (user / tool) -->
          <div v-if="message.attachments && message.attachments.length" class="attachments">
            <template v-for="(a, i) in message.attachments" :key="i">
              <div v-if="a.type === 'image_url' && a.url" class="attach-wrap">
                <img
                  class="msg-image"
                  :src="a.url"
                  :alt="a.name || 'image'"
                  loading="lazy"
                  @click="openLightbox(a.url, a.name || 'image', 'image')"
                />
                <div class="attach-actions">
                  <button type="button" class="attach-action-btn" title="复制图片" :aria-label="'复制图片'" @click="copyAttachment(a)">
                    <Clipboard :size="12" />
                  </button>
                  <button type="button" class="attach-action-btn" title="下载图片" :aria-label="'下载图片'" @click="downloadAttachment(a)">
                    <Download :size="12" />
                  </button>
                </div>
              </div>
              <div v-else-if="a.type === 'video_url' && a.url" class="attach-wrap">
                <video
                  class="msg-video"
                  :src="a.url"
                  controls
                  preload="metadata"
                  :title="a.name || 'video'"
                  @click.stop
                />
                <div class="attach-actions">
                  <button type="button" class="attach-action-btn" title="下载视频" :aria-label="'下载视频'" @click="downloadAttachment(a)">
                    <Download :size="12" />
                  </button>
                </div>
              </div>
              <div v-else-if="a.type === 'audio_url' && a.url" class="attach-wrap">
                <audio
                  class="msg-audio"
                  :src="a.url"
                  controls
                  preload="metadata"
                  :title="a.name || 'audio'"
                />
                <div class="attach-actions">
                  <button type="button" class="attach-action-btn" title="下载音频" :aria-label="'下载音频'" @click="downloadAttachment(a)">
                    <Download :size="12" />
                  </button>
                </div>
              </div>
              <div
                v-else-if="a.type === 'text' && a.kind === 'image_not_supported'"
                class="msg-image-warn"
                :title="a.text"
              >
                <AlertTriangle :size="14" class="warn-icon" />
                <span class="warn-text">{{ shortWarnText(a.text) }}</span>
              </div>
              <div v-else-if="a.type === 'text'" class="msg-file-wrap">
                <div class="msg-file" :title="a.text">
                  <component :is="thumbText(a.kind)" :size="12" class="msg-file-icon" />
                  {{ a.name || '文件' }}
                </div>
                <div class="attach-actions attach-actions-inline">
                  <button type="button" class="attach-action-btn" title="复制内容" :aria-label="'复制内容'" @click="copyAttachment(a)">
                    <Clipboard :size="12" />
                  </button>
                  <button type="button" class="attach-action-btn" title="下载" :aria-label="'下载'" @click="downloadAttachment(a)">
                    <Download :size="12" />
                  </button>
                </div>
              </div>
            </template>
          </div>

          <!-- Vision-not-supported warning chip. Shown when the
               LLM rejected the user's image with the "this
               model does not support image input" error. The
               chat store sets `message.visionUnsupported: true`
               on the trailing user message when that error
               arrives. Sits just below the attachments / text
               so the user can see *why* the image was ignored. -->
          <div v-if="showVisionWarn" class="vision-warn">
            <AlertTriangle :size="14" class="warn-icon" />
            <span class="warn-text">图片未处理：当前模型不支持图片输入</span>
            <span class="warn-hint">切换到支持视觉的模型（如 gpt-4o / claude-3.5+）后重新发送</span>
          </div>

          <!-- Command output: terminal-style panel -->
          <ExecOutputCard
            v-if="(message.msg_type ?? 0) === 5"
            :content="message.content"
            :command="message.name"
            :elapsed="message.elapsed"
          />

          <!-- User / system: markdown of `content` -->
          <div
            v-if="(message.msg_type ?? 0) === 0 && message.role !== 'assistant'"
            ref="mdBodyEl"
            class="md-body"
            v-html="userHtml"
            @click="onCodeClick"
          />

          <!-- Assistant: parts-driven render.
               Falls back to markdown of `content` for
               messages loaded from history (no parts
               were persisted server-side). -->
          <template v-if="(message.msg_type ?? 0) === 0 && message.role === 'assistant'">
            <!-- Live status bar during streaming -->
            <div v-if="statusLines.length" class="stream-status">
              <div v-for="(line, i) in statusLines" :key="i" class="status-line">{{ line }}</div>
            </div>
            <template v-if="message.parts && message.parts.length">
              <template v-for="(p, i) in message.parts" :key="i">
                <ThinkingBlock
                  v-if="p.kind === 'thinking'"
                  :part="p"
                  :default-open="isLiveThinkingPart(i, p.kind, message.parts)"
                />
                <ToolCallCard v-else-if="p.kind === 'tool'" :part="p" />
                <SubAgentCard v-else-if="p.kind === 'sub_agent'" :part="p" />
                <QuestionTable v-else-if="p.kind === 'question'" :part="p" />
                <TypedText
                  v-else-if="p.kind === 'text' && isLiveTextPart(i, p.kind, message.parts)"
                  :text="p.text || ''"
                  :active="true"
                />
                <div
                  v-else-if="p.kind === 'text'"
                  ref="mdBodyEl"
                  class="md-body"
                  v-html="renderMd(p.text || '')"
                  @click="onCodeClick"
                />
              </template>
            </template>
            <template v-else-if="message.content">
              <div
                ref="mdBodyEl"
                class="md-body"
                v-html="userHtml"
                @click="onCodeClick"
              ></div>
            </template>
            <!-- Streaming placeholder: a blinking caret
                 alone in the bubble, ready to receive the
                 first SSE content chunk. Visible only
                 while streaming and the message has no
                 renderable parts yet. -->
            <TypedText
              v-else-if="showTypewriterPlaceholder"
              :text="''"
              :active="true"
            />
          </template>

          <!-- Footer for assistant: tokens / elapsed -->
          <div v-if="hasTokens" class="msg-meta">
            <span v-if="message.tokens_in || message.tokens_out" class="msg-meta-tokens">
              {{ message.tokens_in || 0 }}<ArrowDown :size="10" class="msg-meta-arrow" /> / {{ message.tokens_out || 0 }}<ArrowUp :size="10" class="msg-meta-arrow" />
            </span>
            <span v-if="message.elapsed" class="msg-elapsed">· {{ message.elapsed }}</span>
            <span v-if="message.model" class="msg-model">· {{ message.model }}</span>
          </div>
        </div>

        <!-- Floating action bar: hovers above the bubble on
             hover, shown for non-streaming messages only. The
             user role mirrors the action bar to the left of
             the avatar; assistant / tool mirror to the right
             of the bubble. Hidden for the system role (no
             action needed). -->
        <div
          v-if="!isSystem && !streaming"
          class="bubble-actions"
          :data-role="message.role"
        >
          <button
            type="button"
            class="bubble-action-btn"
            :class="{
              'is-feedback': isAction('copy', 'feedback'),
            }"
            :title="isAction('copy', 'feedback') ? '已复制' : '复制整条消息'"
            :aria-label="isAction('copy', 'feedback') ? '已复制' : '复制整条消息'"
            @click="copyEntireMessage"
          >
            <Check v-if="isAction('copy', 'feedback')" :size="13" :key="`copy-ok-${pulseKey}`" class="bubble-action-icon" />
            <Clipboard v-else :size="13" :key="`copy-idle-${pulseKey}`" class="bubble-action-icon" />
          </button>
          <button
            v-if="canRegenerate"
            type="button"
            class="bubble-action-btn bubble-action-pulse"
            :key="`regen-${pulseKey}`"
            title="重新生成回答"
            aria-label="重新生成"
            @click="onRegenerate"
          >
            <RotateCcw :size="13" class="bubble-action-icon" />
          </button>
          <button
            v-if="canEdit"
            type="button"
            class="bubble-action-btn bubble-action-pulse"
            :key="`edit-${pulseKey}`"
            title="编辑并重新发送"
            aria-label="编辑消息"
            @click="onEdit"
          >
            <Pencil :size="13" class="bubble-action-icon" />
          </button>
          <button
            v-if="canFork"
            type="button"
            class="bubble-action-btn bubble-action-pulse"
            :key="`fork-${pulseKey}`"
            title="从此消息创建分支对话"
            aria-label="创建分支对话"
            @click="onFork"
          >
            <GitBranch :size="13" class="bubble-action-icon" />
          </button>
          <button
            v-if="canRollback"
            type="button"
            class="bubble-action-btn bubble-action-rollback"
            title="撤回此消息及之后的回复"
            aria-label="撤回消息"
            @click="onRollback"
          >
            <Undo2 :size="13" class="bubble-action-icon" />
          </button>
          <button
            type="button"
            class="bubble-action-btn"
            title="更多"
            aria-label="更多"
          >
            <MoreHorizontal :size="13" class="bubble-action-icon" />
          </button>
        </div>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* --- Message layout: avatar + bubble column -----------------------
 * The msg is a flex row. The avatar is a 32px circle that
 * anchors the left edge (assistant) or right edge (user).
 * The bubble-col holds the bubble and (for assistant) the
 * header row. System messages have no avatar and use a
 * different layout (no bubble-col, just a wider block).
 */
.msg {
  display: flex;
  gap: 12px;
  padding: 8px 16px;
  /* Allow bubble-col to grow but stay below the chat
   * canvas width minus a 16px gutter. */
  max-width: 100%;
  position: relative;
  /* Each message sits in its own stacking context so the
   * action toolbar (z-index 50 inside .msg) cannot bleed
   * out and overlap the action bar of the next message
   * above it in the scroll viewport. Without this, a
   * hover-induced opacity transition on the topmost
   * visible message's toolbar would briefly paint on
   * top of the message immediately above during the
   * transition. */
  z-index: 1;
}
.msg.user { flex-direction: row-reverse; }
.msg.system { padding-left: 16px; padding-right: 16px; }

.bubble-col {
  flex: 1;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
  /* The user/assistant asymmetry: user bubble is constrained
   * to a max width so the brand-color block doesn't span
   * the full chat width; assistant has no max width so
   * long code blocks / tables breathe. */
  max-width: 720px;
}
.msg.user .bubble-col { align-items: flex-end; }

/* --- Assistant header (above the bubble) --------------------------
 * Shows who answered, which model, and how long it took.
 * Hidden for system / user / tool messages. */
.bubble-header {
  display: flex;
  align-items: center;
  gap: 6px;
  padding: 0 4px;
  font-size: 12px;
  color: var(--text-tertiary);
  user-select: none;
}
.bubble-role {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-weight: 600;
  color: var(--text-secondary);
  font-size: 12.5px;
}
.bubble-role-icon {
  color: var(--ai-500);
  flex-shrink: 0;
}
.bubble-sep {
  color: var(--text-quaternary);
}
.bubble-model {
  font-family: var(--font-mono);
  font-size: 11.5px;
  color: var(--text-tertiary);
}
.bubble-elapsed {
  font-variant-numeric: tabular-nums;
  color: var(--text-tertiary);
  font-size: 11.5px;
}
.bubble-stream-dot {
  width: 6px;
  height: 6px;
  border-radius: 50%;
  background: var(--brand-500);
  animation: pulse 1.4s infinite;
  margin-left: 4px;
  flex-shrink: 0;
}
@keyframes pulse { 0%,100% { opacity: 1; } 50%, 80% { opacity: 0.35; } }

/* --- Bubble itself ------------------------------------------------
 * Three flavors:
 *   - user:    brand color, white text, larger radius, subtle
 *              shadow. The bubble floats on the right.
 *   - assistant: transparent, no border. The text content
 *                provides the visual mass; the role label /
 *                avatar carry the identity.
 *   - system:  gray surface, 3px brand bar on the left, full
 *              width, centered.
 *   - tool:    monospace, compact (rarely used today but kept
 *              for tool-result messages).
 */
.bubble {
  position: relative;
  word-wrap: break-word;
  overflow-wrap: break-word;
  min-width: 0;
}
.msg.user .bubble {
  background: var(--brand-500);
  color: var(--on-brand);
  padding: 10px 14px;
  border-radius: 14px;
  box-shadow: var(--shadow-sm);
  max-width: 80%;
}
.msg.assistant .bubble {
  background: transparent;
  color: var(--text-primary);
  padding: 0 4px;
}
.msg.tool .bubble {
  background: var(--surface-2);
  color: var(--text-secondary);
  padding: 8px 12px;
  border-radius: 8px;
  font-family: ui-monospace, Menlo, monospace;
  font-size: 12px;
  border: 1px solid var(--border);
}
.msg.system .bubble {
  background: var(--surface-2);
  color: var(--text-secondary);
  font-size: 12.5px;
  width: 100%;
  display: flex; align-items: flex-start; gap: 8px;
  border: 1px solid var(--border-subtle);
  border-left: 3px solid var(--brand-500);
  border-radius: var(--radius-md);
  padding: 8px 12px;
}
.system-icon {
  color: var(--brand-500);
  font-family: var(--font-mono);
  font-weight: 700;
  font-size: 14px;
  flex-shrink: 0;
  line-height: 1.4;
}
.bubble-body { min-width: 0; flex: 1; color: inherit; }

/* Force the markdown body inside a user bubble to inherit
 * the white text color. The default --text-primary would
 * win because of specificity, so we override here. */
.msg.user .bubble-body,
.msg.user .bubble-body * { color: inherit; }
.msg.user .md-body code { background: rgba(255, 255, 255, 0.18); color: inherit; }
.msg.user .md-body pre { background: rgba(0, 0, 0, 0.18); border-color: rgba(255, 255, 255, 0.18); }

/* --- Floating action bar -----------------------------------------
 * Anchored to the top-right of the assistant bubble (or
 * top-left of the user bubble, since user is row-reverse).
 * Pill-shaped, shadow-md, hidden until the message is
 * hovered. Renders inside .bubble (which is position:
 * relative) so the absolute offsets are scoped to the
 * bubble. */
.bubble-actions {
  position: absolute;
  top: -14px;
  display: flex;
  align-items: center;
  gap: 1px;
  padding: 3px;
  background: var(--surface-1);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-md);
  opacity: 0;
  transform: translateY(2px);
  transition: opacity var(--dur-fast) var(--ease-out),
              transform var(--dur-fast) var(--ease-out);
  /* z-index raised from 5 to 50 in 2026-07-09 to keep the
   * action bar above Naive UI's tooltip layer (~60) and
   * the chat window's stream-status bar that recently
   * started using a sticky positioning context. The
   * .msg parent (z-index 1) ensures this is scoped to the
   * current message and does not bleed into adjacent
   * messages. */
  z-index: 50;
  /* Light blur under the pill so it sits on top of the
   * message content cleanly when overlapping. */
  backdrop-filter: blur(4px);
}
.msg.assistant .bubble-actions { right: 8px; }
.msg.user .bubble-actions { left: 8px; }
.msg:hover .bubble-actions,
.bubble-actions:focus-within {
  opacity: 1;
  transform: translateY(0);
}
.bubble-action-btn {
  width: 26px;
  height: 24px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border: none;
  border-radius: 4px;
  background: transparent;
  color: var(--text-tertiary);
  cursor: pointer;
  padding: 0;
  transition: background var(--dur-fast) var(--ease-out),
              color var(--dur-fast) var(--ease-out),
              transform var(--dur-fast) var(--ease-out);
}
.bubble-action-btn:hover {
  background: var(--surface-3);
  color: var(--text-primary);
}
.bubble-action-btn:active {
  /* Press feedback: subtle scale-down + inset shadow so
   * the user feels the click. The transform is tiny
   * (0.92) — large scales would make the toolbar feel
   * jittery on rapid clicking. */
  transform: scale(0.92);
}
.bubble-action-btn:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: -2px;
}
/* Success feedback for the copy button: green icon
 * + soft green surface. The Check icon fades in via
 * a tiny key change + Vue's v-if swap. The green
 * colour is bound to --success-50 / --success-500
 * defined in style.css. */
.bubble-action-btn.is-feedback {
  background: var(--success-50);
  color: var(--success-500);
  animation: bubble-action-feedback var(--dur-base, 200ms) var(--ease-out, ease-out);
}
.bubble-action-btn.is-feedback:hover {
  background: var(--success-50);
  color: var(--success-500);
}
.bubble-action-rollback:hover {
  background: var(--warn-50);
  color: var(--warn-500);
}
/* Press pulse for regen / fork / edit: a tiny scale
 * bump that re-triggers on every :key bump from the
 * pulseKey ref. CSS animations don't restart on the
 * same element, so we rely on Vue's :key swap (handled
 * by the parent component) to recreate the element and
 * re-fire the keyframe. */
.bubble-action-pulse:active {
  animation: bubble-action-pulse 220ms var(--ease-out, ease-out);
}

/* Rollback countdown label. Renders inside the pending
 * rollback button so the user sees "撤回? 3s" → "2s" →
 * "1s" and knows a second click is required. The label
 * uses the warn color (inherited from .is-pending) and
 * a tabular-nums font-feature so the digits don't
 * jitter as the width changes between single and
 * double digits. */
.bubble-action-countdown {
  font-size: 11px;
  font-weight: 600;
  font-variant-numeric: tabular-nums;
  line-height: 1;
  letter-spacing: 0.2px;
  pointer-events: none;
}

@keyframes bubble-action-feedback {
  0%   { transform: scale(0.8); }
  60%  { transform: scale(1.08); }
  100% { transform: scale(1.0); }
}
@keyframes bubble-action-pending {
  0%, 100% { transform: scale(1.0); }
  50%      { transform: scale(1.06); }
}
@keyframes bubble-action-pulse {
  0%   { transform: scale(1.0); }
  40%  { transform: scale(0.88); }
  100% { transform: scale(1.0); }
}

/* The icon swap during feedback is just an opacity
 * cross-fade for visual smoothness. When Vue re-renders
 * the icon component (v-if swap), the new icon appears
 * at opacity 0 and fades to 1 over 120ms so the swap
 * doesn't feel like a hard cut. */
.bubble-action-icon {
  animation: bubble-action-icon-in 140ms var(--ease-out, ease-out);
}
@keyframes bubble-action-icon-in {
  from { opacity: 0; transform: scale(0.7); }
  to   { opacity: 1; transform: scale(1.0); }
}

/* --- Attachments, code blocks, vision-warn ---------------------- */
.attachments {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  margin-bottom: 6px;
}
.msg-image {
  max-width: 100%;
  max-height: 240px;
  border-radius: 6px;
  cursor: zoom-in;
  background: var(--bg);
  display: block;
}
.msg-image:hover { opacity: 0.92; }

.attach-wrap {
  position: relative;
  display: inline-block;
  max-width: 100%;
}
.attach-wrap:hover .attach-actions,
.attach-wrap:focus-within .attach-actions { opacity: 1; }
.attach-actions {
  position: absolute;
  top: 4px;
  right: 4px;
  display: flex;
  gap: 4px;
  opacity: 0;
  transition: opacity 0.15s ease;
  z-index: 2;
}
.attach-actions-inline {
  position: static;
  display: inline-flex;
  vertical-align: middle;
  margin-left: 4px;
  opacity: 1;
}
.attach-action-btn {
  width: 24px;
  height: 24px;
  display: flex;
  align-items: center;
  justify-content: center;
  border: 1px solid var(--border);
  border-radius: 4px;
  background: var(--surface-overlay);
  color: var(--on-brand);
  cursor: pointer;
  font-size: 12px;
  line-height: 1;
  padding: 0;
  backdrop-filter: blur(2px);
}
.attach-action-btn:hover {
  background: rgba(0, 0, 0, 0.8);
}
.msg-file {
  background: var(--surface-2);
  border: 1px solid var(--border-strong);
  border-radius: 4px;
  padding: 2px 6px;
  font-size: 12px;
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  display: inline-flex;
  align-items: center;
  gap: 4px;
  vertical-align: middle;
  color: var(--text-secondary);
}
.msg-file-icon { color: var(--text-tertiary); flex-shrink: 0; }
.msg-file-wrap { display: inline-flex; align-items: center; max-width: 100%; }
.msg-image-warn {
  display: inline-flex; align-items: center; gap: 6px;
  background: var(--warn-50);
  border: 1px dashed var(--warn-500);
  border-radius: 6px;
  padding: 4px 8px;
  font-size: 12px;
  color: var(--text-secondary);
  max-width: 100%;
}
.warn-icon { color: var(--warn-500); flex-shrink: 0; }
.warn-text { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }

.vision-warn {
  display: flex; align-items: center; gap: 6px;
  flex-wrap: wrap;
  background: var(--warn-50);
  border: 1px dashed var(--warn-500);
  border-radius: 6px;
  padding: 6px 10px;
  font-size: 12px;
  color: var(--text-secondary);
  margin: 6px 0 4px 0;
}
.vision-warn .warn-icon { color: var(--warn-500); font-size: 14px; }
.vision-warn .warn-text { color: var(--text-primary); font-weight: 500; }
.vision-warn .warn-hint { color: var(--text-tertiary); font-size: 11px; }

/* --- Streaming + meta -------------------------------------------- */
.msg.streaming .bubble { animation: pulse 1.5s infinite; }

.msg-meta {
  margin-top: 6px;
  padding-top: 4px;
  border-top: 1px dashed var(--border-subtle);
  font-size: 11px;
  color: var(--text-tertiary);
  display: flex;
  gap: 4px;
  flex-wrap: wrap;
}
.msg-elapsed, .msg-model { color: var(--text-quaternary); }
.msg-meta-tokens { display: inline-flex; align-items: center; gap: 1px; }
.msg-meta-arrow { color: var(--text-quaternary); margin: 0 1px; }
.stream-status {
  margin-bottom: 6px;
  padding: 4px 10px;
  background: var(--surface-2);
  border-radius: 6px;
  font-size: 11px;
  color: var(--text-tertiary);
  line-height: 1.6;
}
.status-line {
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
</style>

<style>
/* Code-block copy/download toolbar styles. These are global
 * (not scoped) because we inject the toolbar into the
 * marked-rendered <pre> elements via DOM manipulation —
 * scoped styles don't reach injected DOM. We use a
 * dedicated class prefix (.code-block / .code-toolbar /
 * .code-btn) so there's no collision with other code. */
.code-block {
  position: relative;
  margin: 8px 0;
}
.code-block > pre {
  margin: 0;
}
.code-toolbar {
  position: absolute;
  top: 4px;
  right: 4px;
  display: flex;
  gap: 4px;
  opacity: 0;
  transition: opacity 0.15s ease;
  z-index: 1;
}
.code-block:hover .code-toolbar,
.code-block:focus-within .code-toolbar { opacity: 1; }
.code-btn {
  font-size: 11px;
  padding: 2px 8px;
  border-radius: 3px;
  border: 1px solid var(--border);
  background: var(--bg-2);
  color: var(--text-2);
  cursor: pointer;
  line-height: 1.4;
  font-family: inherit;
  opacity: 0.92;
}
.code-btn:hover {
  background: var(--bg-3);
  color: var(--text);
  opacity: 1;
}
</style>
