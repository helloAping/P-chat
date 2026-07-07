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
import { computed, nextTick, useTemplateRef, watch } from 'vue'
import { marked } from 'marked'

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
import { useMessage } from 'naive-ui'
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
}>()

function onRollback() { emit('rollback') }
function onFork() { emit('fork') }
// toast is the Naive UI useMessage() handle. Used to
// surface "已复制"/"已下载" feedback at the top of the
// screen. Named `toast` rather than `message` so it
// doesn't shadow `props.message` (the chat Message).
const toast = useMessage()

// isLiveTextPart returns true for the trailing text
// part of an actively-streaming message. This is the
// one part that should render through `TypedText`
// (so the caret is visible). All other text parts
// (earlier in the same turn, or after streaming has
// ended) use the static markdown render.
function isLiveTextPart(idx: number, kind: string, parts: MessagePart[] | undefined): boolean {
  if (kind !== 'text') return false
  if (!props.streaming) return false
  if (!parts || parts.length === 0) return false
  // Find the last text part in the array.
  for (let i = parts.length - 1; i >= 0; i--) {
    if (parts[i].kind === 'text') return i === idx
  }
  return false
}

// isLiveThinkingPart mirrors isLiveTextPart for the
// thinking trace. While streaming, the trailing
// thinking part (if any) renders through
// `ThinkingBlock` with its shimmer effect; once
// streaming ends, it falls back to the collapsed
// static view.
function isLiveThinkingPart(idx: number, kind: string, parts: MessagePart[] | undefined): boolean {
  if (kind !== 'thinking') return false
  if (!props.streaming) return false
  if (!parts || parts.length === 0) return false
  for (let i = parts.length - 1; i >= 0; i--) {
    if (parts[i].kind === 'thinking') return i === idx
  }
  return false
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
    case 'image': return '🖼'
    case 'audio': return '🔊'
    case 'video': return '🎬'
    case 'text':  return '📝'
    default:      return '📄'
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
  const text = messageMarkdownText()
  if (!text) {
    toast.error('消息为空')
    return
  }
  const ok = await copyText(text)
  toast[ok ? 'success' : 'error'](ok ? '已复制整条消息' : '复制失败')
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
</script>

<template>
  <div class="msg" :class="message.role + (streaming && message.role === 'assistant' ? ' streaming' : '')">
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
                <button type="button" class="attach-action-btn" title="复制图片" @click="copyAttachment(a)">📋</button>
                <button type="button" class="attach-action-btn" title="下载图片" @click="downloadAttachment(a)">⬇</button>
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
                <button type="button" class="attach-action-btn" title="下载视频" @click="downloadAttachment(a)">⬇</button>
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
                <button type="button" class="attach-action-btn" title="下载音频" @click="downloadAttachment(a)">⬇</button>
              </div>
            </div>
            <div
              v-else-if="a.type === 'text' && a.kind === 'image_not_supported'"
              class="msg-image-warn"
              :title="a.text"
            >
              <span class="warn-icon">⚠</span>
              <span class="warn-text">{{ shortWarnText(a.text) }}</span>
            </div>
            <div v-else-if="a.type === 'text'" class="msg-file-wrap">
              <div class="msg-file" :title="a.text">
                {{ thumbText(a.kind) }} {{ a.name || '文件' }}
              </div>
              <div class="attach-actions attach-actions-inline">
                <button type="button" class="attach-action-btn" title="复制内容" @click="copyAttachment(a)">📋</button>
                <button type="button" class="attach-action-btn" title="下载" @click="downloadAttachment(a)">⬇</button>
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
          <span class="warn-icon">⚠</span>
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
          <span v-if="message.tokens_in || message.tokens_out">
            {{ message.tokens_in || 0 }}↓ / {{ message.tokens_out || 0 }}↑
          </span>
          <span v-if="message.elapsed" class="msg-elapsed">· {{ message.elapsed }}</span>
          <span v-if="message.model" class="msg-model">· {{ message.model }}</span>
        </div>
      </div>
    </div>
    <!-- Bubble actions: copy + rollback, shown below the bubble on hover -->
    <div class="bubble-actions" :data-role="message.role">
      <button
        type="button"
        class="bubble-action-btn"
        title="复制整条消息"
        @click="copyEntireMessage"
      >📋</button>
      <button
        v-if="message.role === 'user' && !streaming"
        type="button"
        class="bubble-action-btn bubble-action-fork"
        title="从此消息创建分支对话"
        @click="onFork"
      >⑂</button>
      <button
        v-if="message.role === 'user' && !streaming"
        type="button"
        class="bubble-action-btn bubble-action-rollback"
        title="撤回此消息及之后的回复"
        @click="onRollback"
      >↩</button>
    </div>
  </div>
</template>

<style scoped>
.msg {
  display: flex;
  flex-direction: column;
  margin: 6px 16px;
}
.msg.user { align-items: flex-end; }
.msg.assistant { align-items: flex-start; }
.msg.tool { align-items: flex-start; }
.bubble {
  max-width: 80%;
  padding: 8px 12px;
  border-radius: 10px;
  word-wrap: break-word;
  overflow-wrap: break-word;
}
.msg.user .bubble {
  background: var(--accent);
  color: var(--on-accent);
}
.msg.assistant .bubble {
  background: var(--bg-3);
  color: var(--text);
}
.msg.tool .bubble {
  background: var(--bg-2);
  color: var(--text-2);
  font-family: ui-monospace, Menlo, monospace;
  font-size: 12px;
  border: 1px solid var(--border);
}
.msg.system {
  margin: 6px 16px;
}
.msg.system .bubble {
  background: var(--bg-3);
  color: var(--text-2);
  font-size: 12.5px;
  max-width: 85%;
  display: flex; align-items: flex-start; gap: 8px;
  border: 1px solid var(--border-2);
  border-left: 3px solid var(--accent);
  border-radius: 6px;
  padding: 8px 12px;
}
.system-icon {
  color: var(--accent);
  font-family: ui-monospace, Menlo, monospace;
  font-weight: 700;
  font-size: 14px;
  flex-shrink: 0;
  line-height: 1.4;
}
.bubble-body { min-width: 0; flex: 1; }

/* Bubble-level actions: copy + rollback. Sit below the bubble,
 * visible on hover. No longer absolutely positioned so they
 * don't overlap the message text. */
.bubble-actions {
  display: flex;
  gap: 4px;
  opacity: 0;
  transition: opacity 0.15s ease;
  padding-top: 4px;
}
.msg:hover .bubble-actions { opacity: 1; }
.bubble-action-btn {
  width: 20px;
  height: 20px;
  display: flex;
  align-items: center;
  justify-content: center;
  border: 1px solid var(--border);
  border-radius: 4px;
  background: var(--bg-2);
  color: var(--text-2);
  cursor: pointer;
  font-size: 10px;
  line-height: 1;
  padding: 0;
  opacity: 0.85;
}
.bubble-action-btn:hover {
  background: var(--bg-3);
  color: var(--text);
  opacity: 1;
}
.msg.user .bubble-action-btn {
  background: rgba(255, 255, 255, 0.18);
  border-color: rgba(255, 255, 255, 0.3);
  color: var(--on-accent);
}
.msg.user .bubble-action-btn:hover {
  background: rgba(255, 255, 255, 0.3);
  color: var(--on-accent);
}
.bubble-action-rollback:hover {
  background: var(--warning-suppl, #fff3cd) !important;
  color: #b85c00 !important;
  border-color: var(--warning, #f0a020) !important;
}
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

/* attach-wrap is the container that holds the
 * attachment preview plus its hover-revealed action
 * toolbar. Position relative so the toolbar can anchor
 * to the corner without breaking layout. */
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
  background: rgba(0, 0, 0, 0.55);
  color: #fff;
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
  background: var(--bg-3);
  border: 1px solid var(--border-2);
  border-radius: 4px;
  padding: 2px 6px;
  font-size: 12px;
  max-width: 100%;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  display: inline-block;
  vertical-align: middle;
}
.msg-file-wrap { display: inline-flex; align-items: center; max-width: 100%; }
.msg-image-warn {
  display: inline-flex; align-items: center; gap: 6px;
  background: var(--warn-soft);
  border: 1px dashed var(--warn);
  border-radius: 6px;
  padding: 4px 8px;
  font-size: 12px;
  color: var(--text-2);
  max-width: 100%;
}
.warn-icon { color: var(--warn); }
.warn-text { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }

/* Full-width "image not supported" warning that sits under
 * a user message's attachments. Used when the LLM rejected
 * the image with a vision_unsupported error — the inline
 * msg-image-warn chip is per-attachment; this is the
 * higher-level "the whole image was skipped" notice that
 * the chat store tags on the message itself. */
.vision-warn {
  display: flex; align-items: center; gap: 6px;
  flex-wrap: wrap;
  background: var(--warn-soft);
  border: 1px dashed var(--warn);
  border-radius: 6px;
  padding: 6px 10px;
  font-size: 12px;
  color: var(--text-2);
  margin: 6px 0 4px 0;
}
.vision-warn .warn-icon { color: var(--warn); font-size: 14px; }
.vision-warn .warn-text { color: var(--text); font-weight: 500; }
.vision-warn .warn-hint { color: var(--text-3); font-size: 11px; }

/* The pulsing animation. The bubble's border stays
 * solid; only its opacity breathes. While the new
 * three-bouncing-dots placeholder is showing, this
 * animation is suppressed (the dots are lively
 * enough on their own). */
.msg.streaming .bubble { animation: pulse 1.5s infinite; }
@keyframes pulse { 0%,100% { opacity: 1; } 50% { opacity: 0.85; } }

.msg-meta {
  margin-top: 6px;
  padding-top: 4px;
  border-top: 1px dashed var(--border-2);
  font-size: 11px;
  color: var(--text-4);
  display: flex;
  gap: 4px;
  flex-wrap: wrap;
}
.msg-elapsed, .msg-model { color: var(--text-4); }
.stream-status {
  margin-bottom: 6px;
  padding: 4px 10px;
  background: var(--bg-3);
  border-radius: 6px;
  font-size: 11px;
  color: var(--text-3);
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
