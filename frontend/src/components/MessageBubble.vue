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
import { computed, h, nextTick, ref, useTemplateRef, watch, type Component } from 'vue'
import { renderMarkdown } from '../utils/markdownCache'
import { formatMessageTime } from '../utils/format'
import {
  ImageIcon, Volume2, Film, FileText, File,
  Clipboard, Download, AlertTriangle, Undo2, GitBranch,
  ArrowDown, ArrowUp, MoreHorizontal, Sparkles,
  Check, Loader2, XCircle, CornerDownLeft, Maximize2, Play,
} from './icons'
import RoleAvatar from './RoleAvatar.vue'

// Markdown rendering is cached in a shared LRU (`renderMarkdown`) so
// static text parts never re-parse across re-renders. The live streaming
// part routes through TypedText (textContent), not this path.
//
// MAX_MD_INLINE_CHARS bounds how much of a single text part is
// parsed into markdown DOM at once. LLM replies are normally a few
// KB; a pathological part (a tool echoing a whole file, a stuck
// model dumping context) can be 100s of KB — marked.parse on that
// is O(n²) and the resulting DOM dwarfs everything else in the
// message. Text beyond the cap is truncated for rendering and the
// full body is shown via the "展开全文" toggle below (rendered
// once, on demand).
const MAX_MD_INLINE_CHARS = 32 * 1024

const renderMd = renderMarkdown
import { NDropdown, useMessage, useDialog, type DropdownMenuProps, type DropdownOption } from 'naive-ui'
import type { Message, MessageAttachment, MessagePart } from '../api/client'
import * as api from '../api/client'
import { state, regenerateMessage, fetchReplies, activateReply } from '../stores/chat'
import ThinkingBlock from './ThinkingBlock.vue'
import SkillCallCard from './SkillCallCard.vue'
import ToolCallCard from './ToolCallCard.vue'
import ToolCallGroup from './ToolCallGroup.vue'
import SubAgentCard from './SubAgentCard.vue'
import QuestionTable from './QuestionTable.vue'
import ExecOutputCard from './ExecOutputCard.vue'
import TypedText from './TypedText.vue'
import {
  copyImageToClipboard, copyText, downloadBlob, downloadFromUrl,
  extensionForMime, fetchAsBlob,
} from '../utils/clipboard'
import { groupConsecutiveToolParts } from '../utils/toolPartGrouping'

const dialog = useDialog()

const props = defineProps<{ message: Message; streaming?: boolean }>()
const emit = defineEmits<{
  rollback: []
  fork: []
  /** Inline-edit a user message and re-send. */
}>()

function onRollback() {
  dialog.warning({
    title: '确认撤回',
    content: '确定撤回此消息及之后的所有回复？文字和附件将回到输入框，此操作可撤销。',
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
  hideMessageContextMenu()
  for (const key of Object.keys(actionTimers)) {
    if (actionTimers[key]) clearTimeout(actionTimers[key]!)
    actionTimers[key] = null
  }
})

function pulseAction(key: string) {
  // Short tactile pulse: re-triggers the press animation
  // without changing the icon. Used for fork /
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

const messageContextMenuVisible = ref(false)
const messageContextMenuX = ref(0)
const messageContextMenuY = ref(0)
const messageContextMenuSelection = ref('')
const messageContextMenuOptions: DropdownOption[] = [
  {
    label: '复制',
    key: 'copy',
    icon: () => h(Clipboard, { size: 14 }),
  },
]

function hideMessageContextMenu() {
  messageContextMenuVisible.value = false
}

async function onMessageContextMenu(e: MouseEvent) {
  e.preventDefault()
  // 菜单显示后焦点可能改变，先保存当前选区。
  // Snapshot the selection before the menu can change focus.
  messageContextMenuSelection.value = window.getSelection()?.toString() ?? ''
  messageContextMenuVisible.value = false
  messageContextMenuX.value = e.clientX
  messageContextMenuY.value = e.clientY
  await nextTick()
  messageContextMenuVisible.value = true
}

async function onMessageContextMenuSelect(key: string | number) {
  hideMessageContextMenu()
  if (key === 'copy') {
    if (messageContextMenuSelection.value) {
      const ok = await copyText(messageContextMenuSelection.value)
      if (ok) toast.success('已复制')
      else toast.error('复制失败')
      return
    }
    await copyEntireMessage()
  }
}

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

// Keep the full part history in Pinia and SQLite, but bound the
// amount of nested card DOM one long-running assistant message can
// keep alive. The newest parts always stay visible so live output
// and the active tool remain in view; earlier records can be added
// back in small batches on demand.
const PART_RENDER_WINDOW = 120
const revealedEarlierParts = ref(0)
const partWindowStart = computed(() => {
  const total = props.message.parts?.length || 0
  const visible = Math.min(total, PART_RENDER_WINDOW + revealedEarlierParts.value)
  return Math.max(0, total - visible)
})
const hiddenPartCount = computed(() => partWindowStart.value)
const visibleParts = computed(() => {
  const parts = props.message.parts || []
  const start = partWindowStart.value
  return parts.slice(start).map((part, offset) => ({ part, index: start + offset }))
})
const visibleRenderEntries = computed(() => groupConsecutiveToolParts(visibleParts.value))
function showEarlierParts() {
  revealedEarlierParts.value += PART_RENDER_WINDOW
}

// The role check: system messages get a special icon.
const isSystem = computed(() => (props.message.msg_type ?? 0) === 0 && props.message.role === 'system')
const hasAttachments = computed(() => (props.message.attachments?.length || 0) > 0)
const hasVisibleUserContent = computed(() => !!props.message.content?.trim())

// For user / system messages, the markdown pipeline
// renders the whole `content` string. For assistant
// messages, we render `parts` instead. (We still
// support legacy assistant messages that have only
// `content` and no `parts` — the server's history
// endpoint returns content-only messages, so the
// fallback is important.)
const assistantHtml = computed(() => '')

const userHtml = computed(() => renderMd(props.message.content || ''))

// --- Long text part handling ----------------------------------
// A text part longer than MAX_MD_INLINE_CHARS is truncated for
// rendering (marked.parse on 100s of KB is O(n²) and the DOM
// dwarfs the message); the full body renders once, on demand,
// via the "展开全文" button.
const expandedTextParts = ref(new Set<number>())
function textPartTruncated(p: MessagePart): boolean {
  return p.kind === 'text' && (p.text?.length ?? 0) > MAX_MD_INLINE_CHARS
}
function textPartExpanded(index: number): boolean {
  return expandedTextParts.value.has(index)
}
function textPartFullLen(p: MessagePart): number {
  return p.kind === 'text' ? (p.text?.length ?? 0) : 0
}
function textPartForRender(p: MessagePart): string {
  if (p.kind !== 'text') return ''
  const t = p.text || ''
  if (t.length <= MAX_MD_INLINE_CHARS) return t
  return t.slice(0, MAX_MD_INLINE_CHARS) + '\n\n…(内容过长，已截断)…'
}
function expandTextPart(index: number) {
  expandedTextParts.value = new Set(expandedTextParts.value).add(index)
}

// Attachments (images / files) — only used by user
// messages today, but kept general.
function openLightbox(src: string, alt: string, kind: 'image' | 'video' = 'image') {
  state.lightbox = { show: true, src, alt, kind }
}

type AttachmentVisualKind = 'image' | 'video' | 'audio' | 'text' | 'file' | 'image_not_supported'

const imageExtensions = new Set([
  'avif', 'bmp', 'gif', 'heic', 'ico', 'jpeg', 'jpg', 'png', 'svg', 'tif', 'tiff', 'webp',
])
const videoExtensions = new Set(['avi', 'm4v', 'mkv', 'mov', 'mp4', 'mpeg', 'mpg', 'webm'])
const audioExtensions = new Set(['aac', 'flac', 'm4a', 'mp3', 'ogg', 'opus', 'pcm', 'wav', 'wma'])

function attachmentExtension(attachment: MessageAttachment): string {
  const name = attachment.name?.trim().toLowerCase() || ''
  const lastDot = name.lastIndexOf('.')
  if (lastDot < 0 || lastDot === name.length - 1) return ''
  return name.slice(lastDot + 1)
}

// Attachment records come from optimistic messages, persisted history and
// older compatibility payloads. Their wire `type` is not always specific, so
// rendering must use every available semantic hint before falling back to a
// generic file card.
function attachmentVisualKind(attachment: MessageAttachment): AttachmentVisualKind {
  const kind = attachment.kind?.toLowerCase() || ''
  if (kind === 'image_not_supported') return 'image_not_supported'
  if (kind === 'image' || kind === 'video' || kind === 'audio') return kind

  if (attachment.mime?.startsWith('image/')) return 'image'
  if (attachment.mime?.startsWith('video/')) return 'video'
  if (attachment.mime?.startsWith('audio/')) return 'audio'

  const extension = attachmentExtension(attachment)
  if (imageExtensions.has(extension)) return 'image'
  if (videoExtensions.has(extension)) return 'video'
  if (audioExtensions.has(extension)) return 'audio'

  if (attachment.type === 'image_url') return 'image'
  if (attachment.type === 'video_url') return 'video'
  if (attachment.type === 'audio_url') return 'audio'
  if (kind === 'text') return 'text'
  return attachment.text ? 'text' : 'file'
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

function attachmentTypeLabel(attachment: MessageAttachment): string {
  const kind = attachmentVisualKind(attachment)
  if ((kind === 'image' || kind === 'video') && !attachment.url) return '预览不可用'
  if (kind === 'image') return '图片'
  if (kind === 'video') return '视频'
  if (kind === 'audio') return '音频'

  const extension = attachmentExtension(attachment)
  const labels: Record<string, string> = {
    pdf: 'PDF 文档',
    doc: 'Word 文档', docx: 'Word 文档',
    ppt: '演示文稿', pptx: '演示文稿',
    xls: '电子表格', xlsx: '电子表格', csv: '电子表格',
    md: 'Markdown 文档', txt: '文本附件', json: 'JSON 文件',
    zip: '压缩文件', rar: '压缩文件', '7z': '压缩文件',
  }
  if (labels[extension]) return labels[extension]
  if (extension) return `${extension.toUpperCase()} 文件`
  return kind === 'text' ? '文本附件' : '文件附件'
}

// --- Copy / download for attachments ------------------------

// friendlyAttachmentName picks a sensible filename for a
// download when the original name is missing or weird.
function friendlyAttachmentName(a: MessageAttachment): string {
  if (a.name && a.name.trim()) return a.name
  const kind = attachmentVisualKind(a)
  const mime = a.mime || (kind === 'image' ? 'image/png'
    : kind === 'audio' ? 'audio/mpeg'
    : kind === 'video' ? 'video/mp4' : 'text/plain')
  const stem = kind !== 'file' && kind !== 'image_not_supported' ? kind : 'attachment'
  return `${stem}-${Date.now()}${extensionForMime(mime)}`
}

async function copyAttachment(a: MessageAttachment) {
  const kind = attachmentVisualKind(a)
  if (kind === 'text') {
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
  if (kind === 'image') {
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
   // parts. Attachments and tool calls are skipped here —
// image attachments are picked up separately by
// copyEntireMessage so the clipboard can carry both the
// text and the image bytes via the ClipboardItem API.
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

// imageAttachmentsOf returns the image_url attachments
// of the message with non-empty URLs, type-narrowed so
// the caller can use `.url` without a null check.
function imageAttachmentsOf(): Array<MessageAttachment & { url: string }> {
  return (props.message.attachments || []).filter(
    (a): a is MessageAttachment & { url: string } =>
      attachmentVisualKind(a) === 'image' && !!a.url,
  )
}

// MAX_RICH_IMAGES caps the number of images we pack into
// a single clipboard write. The ClipboardItem API works
// with multiple items, but pasting >5 images at once is
// rarely what the user wants; extras are skipped silently
// (they can still be downloaded individually via the
// per-attachment toolbar).
const MAX_RICH_IMAGES = 5

// tryCopyRich assembles a multi-item clipboard payload
// (text + N images) and writes it via the ClipboardItem
// API. Returns true on success, false if the API is
// unavailable, all image fetches failed, or the write
// itself rejected. The caller is expected to fall back
// to text-only copy on false.
async function tryCopyRich(
  text: string,
  images: Array<MessageAttachment & { url: string }>,
): Promise<boolean> {
  // ClipboardItem isn't always present in lib.dom
  // typings — fall back to (window as any), same pattern
  // as copyImageToClipboard in utils/clipboard.ts.
  const ClipboardItemCtor =
    (window as any).ClipboardItem ||
    (window as { ClipboardItem?: any }).ClipboardItem
  if (!ClipboardItemCtor || !navigator.clipboard?.write) return false

  try {
    // Fetch every image in parallel; tolerate per-image
    // failures (e.g. one of the data URLs is malformed)
    // by dropping just the broken ones.
    type Fetched = { att: MessageAttachment & { url: string }; blob: Blob | null }
    const fetched: Fetched[] = await Promise.all(
      images.slice(0, MAX_RICH_IMAGES).map(async (a): Promise<Fetched> => {
        try {
          return { att: a, blob: await fetchAsBlob(a.url) }
        } catch {
          return { att: a, blob: null }
        }
      }),
    )
    const valid = fetched.filter(
      (f): f is { att: MessageAttachment & { url: string }; blob: Blob } =>
        f.blob !== null,
    )
    if (valid.length === 0) return false

    // A single ClipboardItem can carry multiple MIME-type
    // representations of the same logical content. We pack
    // the text and the *first* image into one item — that's
    // the most common case (one screenshot plus the user's
    // caption) and gives the best paste experience in chat
    // apps that pick the richest representation available.
    const first = valid[0]
    const firstPayload: Record<string, Blob> = {}
    if (text) firstPayload['text/plain'] = new Blob([text], { type: 'text/plain' })
    firstPayload[first.blob.type || 'image/png'] = first.blob
    const items: any[] = [new ClipboardItemCtor(firstPayload)]

    // Additional images become their own ClipboardItem
    // each — paste targets can then pick whichever they
    // want instead of being forced into a single bundle.
    for (let i = 1; i < valid.length; i++) {
      const b = valid[i]
      items.push(
        new ClipboardItemCtor({
          [b.blob.type || 'image/png']: b.blob,
        }),
      )
    }

    // @ts-ignore — write() overload taking a ClipboardItem[]
    // is missing in some TS lib.dom.d.ts versions.
    await navigator.clipboard.write(items)

    if (valid.length === 1) {
      toast.success('已复制文本和图片')
    } else {
      toast.success(`已复制文本和 ${valid.length} 张图片`)
    }
    return true
  } catch {
    return false
  }
}

async function copyEntireMessage() {
  // Bail out if we're already showing the success state —
  // a second click during the 1.2s feedback window
  // shouldn't cancel the feedback or restart the timer.
  if (isAction('copy', 'feedback')) return

  const text = messageMarkdownText()
  const images = imageAttachmentsOf()

  // Pure-text path: no images on this message, keep the
  // original behavior (text-only copy).
  if (images.length === 0) {
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
    return
  }

  // Rich path: text + one or more images. Falls back to
  // text-only on any failure so the user always gets at
  // least the text portion of the message.
  const rich = await tryCopyRich(text, images)
  if (rich) {
    setActionState('copy', 'feedback', 1200)
    return
  }
  if (!text) {
    // Image-only message (no text body) and the rich
    // copy failed — nothing useful to put on the
    // clipboard, surface the failure.
    toast.error('复制失败')
    return
  }
  const textOk = await copyText(text)
  if (textOk) {
    setActionState('copy', 'feedback', 1200)
    toast.info('剪贴板不支持图片，已复制文本')
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
const rootEl = useTemplateRef<HTMLElement>('rootEl')

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
  // The toolbar button lives inside the .code-block wrapper,
  // next to (not inside) the <pre> — so walk up to the
  // wrapper and query the code block from there. A bare
  // btn.closest('pre') would always miss (returns null),
  // silently swallowing the click.
  const wrapper = btn.closest<HTMLElement>('.code-block')
  const pre = wrapper?.querySelector('pre')
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

async function onMarkdownClick(e: MouseEvent) {
  const target = e.target
  if (target instanceof Element) {
    const anchor = target.closest<HTMLAnchorElement>('a[href]')
    if (anchor) {
      const href = normalizeExternalURL(anchor.getAttribute('href') || '')
      if (href) {
        e.preventDefault()
        e.stopPropagation()
        try {
          await api.openExternalURL(href)
        } catch (err: any) {
          toast.error(`打开链接失败: ${err?.message || err}`)
        }
        return
      }
    }
  }
  await onCodeClick(e)
}

function normalizeExternalURL(href: string): string | null {
  const raw = href.trim()
  if (!/^https?:\/\//i.test(raw)) return null
  try {
    const parsed = new URL(raw)
    if (parsed.protocol !== 'http:' && parsed.protocol !== 'https:') return null
    return parsed.href
  } catch {
    return null
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
//
// Note: we scan *every* .md-body inside this bubble instead
// of the single `mdBodyEl` ref. In a v-for (multi-part
// message) the template ref only ever holds the LAST text
// part's element, so parts before it would never get their
// toolbars. Scanning the root element covers all parts.
watch(
  [mdBodyEl, userHtml, () => props.message.parts],
  async () => {
    await nextTick()
    const root = rootEl.value
    if (!root) return
    root.querySelectorAll<HTMLElement>('.md-body').forEach((el) => injectCodeToolbars(el))
  },
  { flush: 'post', deep: true },
)
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
    p.kind === 'skill' || p.kind === 'tool' || p.kind === 'sub_agent',
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

// traceIdChip is the P3-3 surface for the correlation id.
// We only render the chip on an assistant message that has
// been tagged by the chat store (which happens when an
// error event arrives), so normal successful turns don't
// show noise.
const traceIdChip = computed(() => {
  if (props.message.role !== 'assistant') return ''
  return props.message.traceId || ''
})

// copyTraceId writes the trace id to the system clipboard
// and fires a one-off toast. Called from the chip's click
// handler so the user can paste the id into a support
// ticket — paired with the same id the server logs in
// server-debug.log.
async function copyTraceId() {
  const id = traceIdChip.value
  if (!id) return
  if (await copyText(id)) {
    toast.success('已复制 trace id')
  } else {
    toast.error('复制失败')
  }
}

// showAssistantHeader — the small "Assistant · Claude 3.5
// · 2.3s" line that sits above the assistant's body. Only
// shown for assistant messages; user messages get a
// right-aligned bubble with the avatar as the only "label".
const showAssistantHeader = computed(() =>
  props.message.role === 'assistant' && !isSystem.value
)

// userTimeText renders the send time for USER messages only,
// shown below the bubble. History messages carry the server's
// created_at; optimistic rows get a local stamp at send time
// (InputArea). Assistant / system / tool messages never show a
// time footer — the assistant already shows model + elapsed in
// its header / meta.
const userTimeText = computed(() => {
  if (props.message.role !== 'user') return ''
  const t = props.message.created_at
  if (!t) return ''
  return formatMessageTime(t)
})

// Action-bar visibility:
//   - copy:   always available (copies the visible text)
//   - fork:   user only (PR #2 feature, kept)
//   - more:   reserved for future (model switch, etc.)
//   - regenerate: trailing assistant message only (P1-3)
const canFork = computed(() =>
  props.message.role === 'user' && !props.streaming
)
const canRollback = computed(() =>
  props.message.role === 'user' && !props.streaming
)

function messageActionOption(
  key: string,
  label: string,
  icon: Component,
  extra: Partial<DropdownOption> = {},
): DropdownOption {
  return {
    key,
    label: () =>
      h('span', { class: 'app-action-item' }, [
        h(icon, { size: 16, class: 'app-action-item__icon' }),
        h('span', { class: 'app-action-item__label' }, label),
      ]),
    ...extra,
  }
}

const messageActionMenuOptions = computed<DropdownOption[]>(() => {
  const options: DropdownOption[] = []
  if (canFork.value) {
    options.push(messageActionOption('fork', '创建分支对话', GitBranch))
  }
  if (canRollback.value) {
    options.push(messageActionOption('rollback', '撤回消息及后续回复', Undo2, {
      props: { class: 'app-action-danger' },
    }))
  }
  return options
})

const messageActionMenuProps: DropdownMenuProps = () => ({
  class: 'app-action-menu',
})

function onMessageActionMenuSelect(key: string | number) {
  if (key === 'fork') onFork()
  if (key === 'rollback') onRollback()
}
// P1-3: regenerate is only meaningful on the trailing
// assistant message — regenerating an older reply would
// leave newer messages in an inconsistent state (the
// server truncates from the user_message_id+1, which
// would clobber everything newer). We use `isLast` as
// a proxy for "trailing" because MessageBubble doesn't
// have a direct trailing-message prop.
const isLast = computed(() => {
  const sid = state.currentID
  const msgs = state.sessionMessages[sid]
  if (!msgs || msgs.length === 0) return false
  return msgs[msgs.length - 1] === props.message
})
const canRegenerate = computed(() =>
  props.message.role === 'assistant' && isLast.value && !props.streaming
)
// P1-4: regen siblings are loaded on demand the first
// time the user hovers a paginated bubble. The fetch
// is debounced via a per-bubble ref so rapid hover
// events don't stampede the API.
//
// replies is the full RepliesResponse from the server
// (active + archived + the user message summary). It
// stays empty for the common single-shot case — the
// computed replySiblings short-circuits to [] until
// the first hover triggers the fetch.
const replies = ref<api.RepliesResponse | null>(null)
const repliesLoading = ref(false)
let repliesFetchToken = 0

// loadReplies kicks off the on-demand fetch. Idempotent:
// a second call while the first is in flight returns the
// same promise (no double-fetch). After completion the
// result is cached on the bubble-local `replies` ref AND
// on the global state.sessionReplies / sessionUserMsgs
// caches (so the chip / anchor-FAB paths can read it
// without re-walking the full replies payload).
async function loadReplies() {
  if (repliesLoading.value || replies.value) return
  if (!props.message.regen_group_id) return
  const sid = state.currentID
  const userMsgId = Number(props.message.regen_group_id)
  if (!sid || !userMsgId) return
  repliesLoading.value = true
  const myToken = ++repliesFetchToken
  try {
    const r = await fetchReplies(sid, userMsgId)
    // Guard against a stale fetch: if the user has
    // clicked ◀/▶ (which bumps repliesFetchToken via
    // activateReply) while this fetch was in flight,
    // drop the late result.
    if (myToken !== repliesFetchToken) return
    replies.value = r
  } finally {
    repliesLoading.value = false
  }
}

// replySiblings is the local view of the sibling set,
// oldest-first. Returns [] when no regen history exists
// or the fetch hasn't completed yet — the pager UI is
// gated on `replySiblings.length > 1` so the empty case
// is invisible to the user.
const replySiblings = computed(() => replies.value?.replies || [])

// activeIdx is the index in replySiblings of the row
// that is currently visible (is_archived = false). The
// pager uses this for the N/M position and to disable
// ◀/▶ at the boundaries.
const activeIdx = computed(() => {
  const sibs = replySiblings.value
  for (let i = 0; i < sibs.length; i++) {
    if (!sibs[i].is_archived) return i
  }
  // No active row (transient state during a regen) —
  // fall back to "newest" so the pager still shows a
  // meaningful N/M rather than 0/0.
  return Math.max(0, sibs.length - 1)
})

// userMsgPreview is the user-message summary text used
// in the pager (`◀ 2/3 · "请帮我..." ▶`) and the
// "上一版回答" chip's anchor. Falls back to the
// cached sessionUserMsgs entry (in case the user
// navigates ◀/▶ before loadReplies completes) and to
// the empty string when nothing is known yet.
const userMsgPreview = computed(() => {
  if (replies.value?.user_message?.content_preview) {
    return replies.value.user_message.content_preview
  }
  const sid = state.currentID
  const userMsgId = Number(props.message.regen_group_id)
  if (!sid || !userMsgId) return ''
  return state.sessionUserMsgs[sid]?.[String(userMsgId)]?.content_preview || ''
})

// truncatedPreview: the pager's inline preview is
// capped at 12 chars (so `◀ 2/3 · "请帮我写一个 todo" ▶`
// doesn't overflow on a 720px chat column). Falls back
// to the empty string when no preview is available.
const truncatedPreview = computed(() => {
  const p = userMsgPreview.value
  if (!p) return ''
  return p.length > 12 ? p.slice(0, 12) + '…' : p
})

// onPrevReply / onNextReply: ◀/▶ click handlers. Both
// call the global activateReply action which talks to
// the server and mutates the local state. We compute
// the target row from the current sibling set + active
// index — the server response carries the fresh
// active_reply_id and full siblings, which the store
// mirrors onto the bubble's `replies` ref via the
// fetchReplies cache invalidation on regen (so a
// subsequent loadReplies sees the new set).
async function onPrevReply() {
  if (activeIdx.value <= 0) return
  const sid = state.currentID
  const userMsgId = Number(props.message.regen_group_id)
  const target = replySiblings.value[activeIdx.value - 1]
  if (!sid || !userMsgId || target.id == null) return
  await activateReply(sid, userMsgId, target.id)
  // Bump the fetch token so any in-flight loadReplies
  // drops its late result. The store just wrote the
  // fresh RepliesResponse into the cache, so the next
  // loadReplies picks it up via the cache hit.
  repliesFetchToken++
  // Refresh the local `replies` ref from the store
  // cache so the UI re-renders with the new active
  // row without an extra round-trip.
  replies.value = state.sessionReplies[sid]?.[String(userMsgId)] || null
}

async function onNextReply() {
  if (activeIdx.value >= replySiblings.value.length - 1) return
  const sid = state.currentID
  const userMsgId = Number(props.message.regen_group_id)
  const target = replySiblings.value[activeIdx.value + 1]
  if (!sid || !userMsgId || target.id == null) return
  await activateReply(sid, userMsgId, target.id)
  repliesFetchToken++
  replies.value = state.sessionReplies[sid]?.[String(userMsgId)] || null
}

// P1-4 lazy-load: the pager is rendered with the
// current `replies` ref (which is empty for the first
// render). Trigger the fetch on mouseenter so the
// first hover pre-populates the cache; subsequent
// ◀/▶ clicks use the cache directly.
function onPagerHover() {
  if (!replies.value && !repliesLoading.value) {
    loadReplies()
  }
}

// regenerating is true while the streamRegenerate call
// is in flight; we lock the button to prevent double-
// clicks and show a spinner instead of the static icon.
const regenerating = ref(false)
async function onRegenerate() {
  if (regenerating.value) return
  // P1-4: 二次确认 via Naive UI dialog. The user has
  // to explicitly approve the regen — it is the most
  // destructive routine action in the chat (every
  // prior answer in the regen group will be archived
  // — only reversible by switching sessions and
  // re-loading). The dialog copy mirrors the
  // rollback dialog's tone.
  const userMsgId = findPrecedingUserMessageId()
  if (!userMsgId) return
  dialog.warning({
    title: '重新生成回答？',
    content: '当前回答将被归档为历史版本，可通过消息下方的分页条左右查看。',
    positiveText: '确认重答',
    negativeText: '取消',
    onPositiveClick: () => {
      // Fire-and-forget: the actual stream lives in
      // the store action below. The dialog promise
      // resolves on click; we don't await the
      // stream so the user can keep interacting
      // with the chat (the new assistant bubble
      // appears as the stream lands).
      void doRegenerate(userMsgId)
    },
  })
}

async function doRegenerate(userMessageId: number) {
  if (regenerating.value) return
  regenerating.value = true
  try {
    await regenerateMessage(state.currentID, userMessageId)
  } catch (e: any) {
    console.warn('[regen] failed:', e?.message || e)
  } finally {
    regenerating.value = false
  }
}

// findPrecedingUserMessageId walks back through the
// session's message list and returns the id of the
// nearest preceding user message. The regen target
// is the user message — the server archives every
// assistant sibling in that user message's regen
// group and re-runs the agent loop. Returns 0 when no
// such user message exists (legacy pre-id schema);
// the caller bails in that case.
function findPrecedingUserMessageId(): number {
  const sid = state.currentID
  const msgs = state.sessionMessages[sid]
  if (!msgs) return 0
  // Find the index of this message in the local list.
  // The regen button only appears on the trailing
  // assistant message, so the user message is the
  // one immediately before it. We still walk back
  // through all preceding messages for safety (in
  // case the bubble is somehow rendered off-trailing).
  const myIdx = msgs.findIndex((m) => m === props.message || m.id === props.message.id)
  if (myIdx < 0) return 0
  for (let i = myIdx - 1; i >= 0; i--) {
    if (msgs[i].role === 'user' && msgs[i].id) {
      return msgs[i].id as number
    }
  }
  return 0
}
</script>

<template>
  <div
    class="msg"
    ref="rootEl"
    :class="[message.role, { streaming }]"
    :data-msg-id="message.id"
    @contextmenu="onMessageContextMenu"
  >
    <NDropdown
      trigger="manual"
      placement="bottom-start"
      :show="messageContextMenuVisible"
      :x="messageContextMenuX"
      :y="messageContextMenuY"
      :options="messageContextMenuOptions"
      @select="onMessageContextMenuSelect"
      @clickoutside="hideMessageContextMenu"
    />
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

      <div class="bubble" :class="{ 'bubble--attachments': hasAttachments }">
        <div v-if="isSystem" class="system-icon">›</div>
        <div class="bubble-body">
          <!-- Attachments (user / tool) -->
          <div v-if="message.attachments && message.attachments.length" class="attachments">
            <template v-for="(a, i) in message.attachments" :key="i">
              <div
                v-if="attachmentVisualKind(a) === 'image' && a.url"
                class="media-thumbnail media-thumbnail--image"
                role="button"
                tabindex="0"
                :aria-label="`查看图片：${a.name || '图片'}`"
                @click="openLightbox(a.url, a.name || 'image', 'image')"
                @keydown.enter.prevent="openLightbox(a.url, a.name || 'image', 'image')"
                @keydown.space.prevent="openLightbox(a.url, a.name || 'image', 'image')"
              >
                <img
                  class="msg-image"
                  :src="a.url"
                  :alt="a.name || 'image'"
                  loading="lazy"
                />
                <div class="media-thumbnail-hint">
                  <Maximize2 :size="14" />
                  <span>查看大图</span>
                </div>
                <div class="attachment-action-bar attachment-action-bar--image">
                  <button type="button" class="attach-action-btn" title="复制图片" aria-label="复制图片" @click.stop="copyAttachment(a)">
                    <Clipboard :size="14" />
                  </button>
                  <button type="button" class="attach-action-btn" title="下载图片" aria-label="下载图片" @click.stop="downloadAttachment(a)">
                    <Download :size="14" />
                  </button>
                </div>
              </div>
              <div
                v-else-if="attachmentVisualKind(a) === 'video' && a.url"
                class="media-thumbnail media-thumbnail--video"
                role="button"
                tabindex="0"
                :aria-label="`全屏播放：${a.name || '视频'}`"
                @click="openLightbox(a.url, a.name || 'video', 'video')"
                @keydown.enter.prevent="openLightbox(a.url, a.name || 'video', 'video')"
                @keydown.space.prevent="openLightbox(a.url, a.name || 'video', 'video')"
              >
                <video
                  class="msg-video"
                  :src="a.url"
                  muted
                  playsinline
                  preload="auto"
                  :title="a.name || 'video'"
                />
                <span class="media-thumbnail-play" aria-hidden="true">
                  <Play :size="22" fill="currentColor" />
                </span>
                <div class="media-thumbnail-hint">
                  <Maximize2 :size="14" />
                  <span>全屏播放</span>
                </div>
                <div class="attachment-action-bar">
                  <button type="button" class="attach-action-btn" title="下载视频" aria-label="下载视频" @click.stop="downloadAttachment(a)">
                    <Download :size="14" />
                  </button>
                </div>
              </div>
              <div v-else-if="attachmentVisualKind(a) === 'audio' && a.url" class="attachment-audio-card">
                <div class="attachment-card-heading">
                  <span class="attachment-card-icon"><Volume2 :size="18" /></span>
                  <span class="attachment-card-info">
                    <span class="attachment-card-name">{{ a.name || '音频' }}</span>
                    <span class="attachment-card-meta">{{ attachmentTypeLabel(a) }}</span>
                  </span>
                  <button type="button" class="attach-action-btn" title="下载音频" aria-label="下载音频" @click="downloadAttachment(a)">
                    <Download :size="14" />
                  </button>
                </div>
                <audio
                  class="msg-audio"
                  :src="a.url"
                  controls
                  preload="metadata"
                  :title="a.name || 'audio'"
                />
              </div>
              <div
                v-else-if="attachmentVisualKind(a) === 'image_not_supported'"
                class="msg-image-warn"
                :title="a.text"
              >
                <AlertTriangle :size="14" class="warn-icon" />
                <span class="warn-text">{{ shortWarnText(a.text) }}</span>
              </div>
              <div v-else class="msg-file-wrap">
                <div class="attachment-file-card" :title="a.text">
                  <span class="attachment-card-icon">
                    <component :is="thumbText(attachmentVisualKind(a))" :size="18" />
                  </span>
                  <span class="attachment-card-info">
                    <span class="attachment-card-name">{{ a.name || '文件' }}</span>
                    <span class="attachment-card-meta">{{ attachmentTypeLabel(a) }}</span>
                  </span>
                  <span class="attachment-card-actions">
                    <button v-if="a.text" type="button" class="attach-action-btn" title="复制内容" aria-label="复制内容" @click="copyAttachment(a)">
                      <Clipboard :size="14" />
                    </button>
                    <button v-if="a.text || a.url" type="button" class="attach-action-btn" title="下载附件" aria-label="下载附件" @click="downloadAttachment(a)">
                      <Download :size="14" />
                    </button>
                  </span>
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

          <!-- P3-3: trace id chip. Only rendered on
               assistant messages that the chat store has
               tagged with a `traceId` (set when the error
               event arrives). Click to copy the id to the
               clipboard so the user can paste it into a
               bug report — server-debug.log uses the same
               id for greppability. -->
          <button
            v-if="traceIdChip"
            type="button"
            class="trace-id-chip"
            :title="'点击复制 trace id'"
            @click="copyTraceId"
          >
            <Clipboard :size="12" />
            <span class="trace-id-text">{{ traceIdChip }}</span>
          </button>

          <!-- Command output: terminal-style panel -->
          <ExecOutputCard
            v-if="(message.msg_type ?? 0) === 5"
            :content="message.content"
            :command="message.name"
            :elapsed="message.elapsed"
          />

          <!-- User / system: markdown of `content` -->
          <div
            v-if="(message.msg_type ?? 0) === 0 && message.role !== 'assistant' && (message.role !== 'user' || hasVisibleUserContent)"
            ref="mdBodyEl"
            class="md-body"
            :class="{ 'user-message-caption': message.role === 'user' && hasAttachments }"
            v-html="userHtml"
            @click="onMarkdownClick"
          />

          <!-- Assistant: parts-driven render.
               Falls back to markdown of `content` for
               messages loaded from history (no parts
               were persisted server-side). -->
          <template v-if="(message.msg_type ?? 0) === 0 && message.role === 'assistant'">
            <!-- Live status bar during streaming -->
            <div v-if="statusLines.length" class="stream-status">
              <div v-for="(line, i) in statusLines" :key="i" :class="['status-line', { 'status-line-auto-continue': line.includes('自动续 LLM') }]">{{ line }}</div>
            </div>
            <template v-if="message.parts && message.parts.length">
              <button
                v-if="hiddenPartCount"
                type="button"
                class="part-window-toggle"
                :title="`展开更早的 ${hiddenPartCount} 条过程记录`"
                @click="showEarlierParts"
              >
                已折叠 {{ hiddenPartCount }} 条过程记录
              </button>
              <template
                v-for="entry in visibleRenderEntries"
                :key="entry.kind === 'tool_group' ? `tool-group-${entry.startIndex}` : entry.index"
              >
                <ToolCallGroup
                  v-if="entry.kind === 'tool_group'"
                  :parts="entry.parts"
                />
                <template v-else>
                  <ThinkingBlock
                    v-if="entry.part.kind === 'thinking'"
                    :part="entry.part"
                    :default-open="isLiveThinkingPart(entry.index, entry.part.kind, message.parts)"
                  />
                  <SkillCallCard v-else-if="entry.part.kind === 'skill'" :part="entry.part" />
                  <ToolCallCard v-else-if="entry.part.kind === 'tool'" :part="entry.part" />
                  <SubAgentCard v-else-if="entry.part.kind === 'sub_agent'" :part="entry.part" />
                  <QuestionTable v-else-if="entry.part.kind === 'question'" :part="entry.part" />
                  <TypedText
                    v-else-if="entry.part.kind === 'text' && isLiveTextPart(entry.index, entry.part.kind, message.parts)"
                    :text="entry.part.text || ''"
                    :active="true"
                  />
                  <div v-else-if="entry.part.kind === 'text'">
                    <div
                      ref="mdBodyEl"
                      class="md-body"
                      v-html="renderMd(textPartForRender(entry.part))"
                      @click="onMarkdownClick"
                    ></div>
                    <button
                      v-if="textPartTruncated(entry.part) && !textPartExpanded(entry.index)"
                      class="md-expand-btn"
                      @click.stop="expandTextPart(entry.index)"
                    >展开全文 ({{ textPartFullLen(entry.part) }} 字)</button>
                  </div>
                </template>
              </template>
            </template>
            <template v-else-if="message.content">
              <div
                ref="mdBodyEl"
                class="md-body"
                v-html="userHtml"
                @click="onMarkdownClick"
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

          <!-- P1-4: 上一版回答 chip. Only on archived
               assistant rows — the active row sits
               directly under its user message, so adding
               the chip would be redundant. User-message
               preview lives in the pager below, not the
               chip, so the chip itself stays at 2 tokens
               (icon + label) and never crowds the bubble. -->
          <div v-if="message.role === 'assistant' && message.is_archived" class="bubble-archived-chip">
            <CornerDownLeft :size="12" />
            <span class="bubble-archived-label">上一版回答</span>
          </div>

          <!-- Footer for assistant: tokens / elapsed -->
          <div v-if="hasTokens" class="msg-meta">
            <span v-if="message.tokens_in || message.tokens_out" class="msg-meta-tokens">
              {{ message.tokens_in || 0 }}<ArrowDown :size="10" class="msg-meta-arrow" /> / {{ message.tokens_out || 0 }}<ArrowUp :size="10" class="msg-meta-arrow" />
            </span>
            <span v-if="message.elapsed" class="msg-elapsed">· {{ message.elapsed }}</span>
            <span v-if="message.model" class="msg-model">· {{ message.model }}</span>
          </div>

          <!-- P1-4: ◀ N/M ▶ reply pager. Gated on
               replySiblings.length > 1 so the common
               single-shot case renders nothing here.
               Lazy-load: loadReplies fires on the first
               mouseenter, so the first render is free;
               only paginated bubbles pay the fetch
               cost. The user-message preview is inlined
               in the pager (e.g. `◀ 2/3 · "请帮我..." ▶`)
               so the user always knows which user
               message the sibling set belongs to. -->
          <div
            v-if="message.role === 'assistant' && replySiblings.length > 1"
            class="bubble-reply-pager"
            @mouseenter="onPagerHover"
          >
            <button
              type="button"
              class="bubble-reply-pager-btn"
              :disabled="activeIdx === 0"
              title="更早的回复"
              aria-label="更早的回复"
              @click="onPrevReply"
            >◀</button>
            <span class="bubble-reply-pager-pos">{{ activeIdx + 1 }}/{{ replySiblings.length }}</span>
            <span v-if="truncatedPreview" class="bubble-reply-pager-context">· "{{ truncatedPreview }}"</span>
            <button
              type="button"
              class="bubble-reply-pager-btn"
              :disabled="activeIdx >= replySiblings.length - 1"
              title="更新的回复"
              aria-label="更新的回复"
              @click="onNextReply"
            >▶</button>
          </div>
        </div>
      </div>

      <!-- 消息级操作统一停靠在正文下方；附件只保留预览、复制和下载。
           Message-level actions share one quiet footer below the content;
           attachment cards keep only preview/copy/download controls. -->
      <div
        v-if="(!isSystem && !streaming) || userTimeText"
        class="message-footer"
        :data-role="message.role"
      >
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
          <!-- P1-3 regenerate. Only shown on the trailing
               assistant message (we don't allow re-running
               a reply that's no longer the latest one).
               Click pops the local bubble, calls the
               /regenerate endpoint, and re-streams the new
               reply through the regular chat store path.

               Visual states (see .bubble-action-regenerate
               + .bubble-action-regenerate--busy in <style>):
                 - idle: pill shape, brand-coloured surface,
                   icon + "重答" text (auto-sized to content).
                 - busy: collapses to a 26×24 icon square with
                   a transparent surface, so the spinner
                   doesn't draw the eye away from the
                   streaming reply. Width transitions smoothly
                   via the existing --dur-fast easing. -->
          <button
            v-if="canRegenerate"
            type="button"
            class="bubble-action-btn bubble-action-regenerate"
            :class="{ 'bubble-action-regenerate--busy': regenerating }"
            :disabled="regenerating"
            title="重新生成回复"
            aria-label="重新生成回复"
            @click="onRegenerate"
          >
            <Loader2 :size="13" :class="regenerating ? 'bubble-action-icon bubble-action-icon--spin' : 'bubble-action-icon'" />
            <span v-if="!regenerating" class="bubble-action-text">重答</span>
          </button>
          <NDropdown
            v-if="messageActionMenuOptions.length"
            trigger="click"
            placement="bottom-end"
            size="small"
            :options="messageActionMenuOptions"
            :menu-props="messageActionMenuProps"
            @select="onMessageActionMenuSelect"
          >
            <button
              type="button"
              class="bubble-action-btn"
              title="更多操作"
              aria-label="更多操作"
              @click.stop
            >
              <MoreHorizontal :size="13" class="bubble-action-icon" />
            </button>
          </NDropdown>
        </div>

        <!-- Send time for user messages only. It shares the footer baseline
             with the message tools so neither can cover a small text bubble. -->
        <div v-if="userTimeText" class="msg-time">{{ userTimeText }}</div>
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
.msg.user .bubble.bubble--attachments {
  background: transparent;
  padding: 0;
  border-radius: 0;
  box-shadow: none;
  max-width: 100%;
  width: min(100%, calc(var(--space-8) * 12));
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

.part-window-toggle {
  display: block;
  width: 100%;
  margin: 6px 0;
  padding: 6px 10px;
  border: 1px dashed var(--border-subtle);
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--text-tertiary);
  font: inherit;
  font-size: 12px;
  line-height: 1.4;
  text-align: left;
  cursor: pointer;
}
.part-window-toggle:hover {
  border-color: var(--border-default);
  color: var(--text-secondary);
}

/* Force the markdown body inside a user bubble to inherit
 * the white text color (--on-brand). The default --text-primary
 * would win because of specificity, so we override here.
 * NOTE: this only reaches template-owned elements (the .md-body
 * container itself). Code blocks inside are injected via v-html
 * and carry no [data-v] attribute, so scoped rules never reach
 * their <pre>/<code> — those overrides live in the GLOBAL
 * <style> block below (see .msg.user .md-body pre). */
.msg.user .bubble-body,
.msg.user .bubble-body * { color: inherit; }
.msg.user .bubble--attachments .bubble-body {
  display: flex;
  flex-direction: column;
  align-items: flex-end;
  gap: var(--space-2);
  color: var(--text-primary);
}
.msg.user .bubble--attachments .attachments {
  display: grid;
  grid-template-columns: repeat(auto-fit, minmax(min(calc(var(--space-8) * 5), 100%), 1fr));
  align-items: start;
  gap: var(--space-2);
  margin-bottom: 0;
  width: 100%;
}
.msg.user .bubble--attachments .attachments > :only-child {
  grid-column: 1 / -1;
}
.msg.user .bubble--attachments .user-message-caption {
  align-self: flex-end;
  padding: var(--space-2) var(--space-3);
  background: var(--brand-500);
  color: var(--on-brand);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-sm);
  max-width: 80%;
}

/* "展开全文" affordance for truncated text parts. */
.md-expand-btn {
  margin-top: 6px;
  padding: 2px 10px;
  border: 1px solid var(--border-default);
  border-radius: 6px;
  background: transparent;
  color: var(--brand-600);
  font-size: 11.5px;
  cursor: pointer;
}
.md-expand-btn:hover { background: var(--brand-50); }

/* 消息操作与时间使用同一静态底栏，不再覆盖文字或附件。
 * Message actions and time share one static footer, never overlaying content. */
.message-footer {
  display: flex;
  align-items: center;
  gap: var(--space-1);
  min-height: var(--space-6);
  width: 100%;
}
.message-footer[data-role="user"] { justify-content: flex-end; }
.message-footer[data-role="assistant"] { justify-content: flex-start; }
.bubble-actions {
  display: flex;
  align-items: center;
  gap: var(--space-1);
  padding: 0;
  opacity: 0;
  transform: translateY(calc(-1 * var(--space-1)));
  pointer-events: none;
  transition: opacity var(--dur-fast) var(--ease-out),
              transform var(--dur-fast) var(--ease-out);
}
.msg:hover .bubble-actions,
.bubble-actions:focus-within {
  opacity: 1;
  transform: translateY(0);
  pointer-events: auto;
}
.bubble-action-btn {
  width: var(--space-6);
  height: var(--space-6);
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border: 1px solid var(--border-default);
  border-radius: var(--radius-sm);
  background: var(--surface-1);
  color: var(--text-tertiary);
  box-shadow: var(--shadow-sm);
  cursor: pointer;
  padding: 0;
  transition: background var(--dur-fast) var(--ease-out),
              color var(--dur-fast) var(--ease-out),
              transform var(--dur-fast) var(--ease-out);
}
.bubble-action-btn:hover {
  background: var(--surface-2);
  border-color: var(--border-strong);
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
  animation: bubble-action-feedback var(--dur-base) var(--ease-out);
}
.bubble-action-btn.is-feedback:hover {
  background: var(--success-50);
  color: var(--success-500);
}
.bubble-action-rollback:hover {
  background: var(--warn-50);
  color: var(--warn-500);
}

/* Regenerate is the only "primary action" in the toolbar —
 * the other buttons are icon-only utilities. To mark it as
 * the affordance the user is most likely to want next, it
 * gets a brand-coloured surface in the idle state. The
 * width: auto / padding: 0 8px / gap: 4px makes the pill
 * snug around "重答" + icon, and the min-width: 26px keeps
 * it from collapsing to nothing if the text is ever
 * localised to a wider string. The "transition: width"
 * line is what makes the pill shrink smoothly back to a
 * 26×24 square when it goes busy. */
.bubble-action-btn.bubble-action-regenerate {
  width: auto;
  min-width: 26px;
  height: 24px;
  padding: 0 8px;
  gap: 4px;
  background: var(--brand-50);
  color: var(--brand-600);
  border: 1px solid var(--brand-100);
  font-size: 12px;
  font-weight: 500;
  transition: width var(--dur-fast) var(--ease-out),
              padding var(--dur-fast) var(--ease-out),
              background var(--dur-fast) var(--ease-out),
              color var(--dur-fast) var(--ease-out),
              border-color var(--dur-fast) var(--ease-out),
              transform var(--dur-fast) var(--ease-out);
}
.bubble-action-btn.bubble-action-regenerate:hover {
  background: var(--brand-100);
  color: var(--brand-700);
  border-color: var(--brand-200);
}
.bubble-action-btn.bubble-action-regenerate--busy {
  /* Collapse to the same 26×24 square the other icon-only
   * buttons use, but keep the surface transparent so the
   * spinner blends in rather than competing with the
   * streaming reply. */
  width: 26px;
  padding: 0;
  background: transparent;
  color: var(--text-tertiary);
  border-color: transparent;
  gap: 0;
}
.bubble-action-btn.bubble-action-regenerate--busy:hover {
  background: var(--surface-3);
  color: var(--text-primary);
}
.bubble-action-text {
  line-height: 1;
  /* Tiny slide+fade when the text reappears after a regen
   * completes — matches the icon's own fade-in
   * (bubble-action-icon-in) so the icon+text come back as
   * a single unit rather than the text popping in late. */
  animation: bubble-action-text-in var(--dur-fast) var(--ease-out);
}
@keyframes bubble-action-text-in {
  from { opacity: 0; transform: translateX(-2px); }
  to   { opacity: 1; transform: translateX(0); }
}
/* Press pulse for fork: a tiny scale
 * bump that re-triggers on every :key bump from the
 * pulseKey ref. CSS animations don't restart on the
 * same element, so we rely on Vue's :key swap (handled
 * by the parent component) to recreate the element and
 * re-fire the keyframe. */
.bubble-action-pulse:active {
  animation: bubble-action-pulse var(--dur-base) var(--ease-out);
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
  animation: bubble-action-icon-in var(--dur-fast) var(--ease-out);
}
@keyframes bubble-action-icon-in {
  from { opacity: 0; transform: scale(0.7); }
  to   { opacity: 1; transform: scale(1.0); }
}

/* Regenerate's busy state spins the Loader2 icon. The
 * bare `.spin` class that the previous code referenced
 * was only defined in ThinkingBlock.vue's scoped CSS,
 * so it silently did nothing here. Defining the modifier
 * locally keeps the regenerate spinner self-contained
 * — the keyframe name is also namespaced so we don't
 * conflict with the (also-scoped) thinking-spin
 * definition in ThinkingBlock.vue. */
.bubble-action-icon--spin {
  animation: bubble-action-icon-spin 1.1s linear infinite;
}
@keyframes bubble-action-icon-spin {
  from { transform: rotate(0deg); }
  to   { transform: rotate(360deg); }
}

/* --- Attachments, code blocks, vision-warn ---------------------- */
.attachments {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
  margin-bottom: var(--space-2);
}
.media-thumbnail {
  position: relative;
  display: flex;
  align-items: center;
  justify-content: center;
  width: 100%;
  min-width: 0;
  aspect-ratio: 4 / 3;
  box-sizing: border-box;
  padding: var(--space-1);
  background: var(--surface-1);
  border: 1px solid var(--border-default);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-sm);
  overflow: hidden;
  cursor: zoom-in;
  transition: border-color var(--dur-fast) var(--ease-out),
              box-shadow var(--dur-fast) var(--ease-out),
              transform var(--dur-fast) var(--ease-out);
}
.media-thumbnail--video {
  aspect-ratio: 16 / 9;
  cursor: pointer;
}
.media-thumbnail:hover,
.media-thumbnail:focus-visible {
  border-color: var(--border-strong);
  box-shadow: var(--shadow-md);
}
.media-thumbnail:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: 2px;
}
.media-thumbnail:active { transform: scale(0.995); }
.msg-image,
.msg-video {
  display: block;
  width: 100%;
  height: 100%;
  border-radius: var(--radius-md);
  background: var(--surface-2);
}
.msg-image { object-fit: contain; }
.msg-video { object-fit: cover; }
.media-thumbnail-hint {
  position: absolute;
  left: var(--space-2);
  bottom: var(--space-2);
  display: inline-flex;
  align-items: center;
  gap: var(--space-1);
  padding: var(--space-1) var(--space-2);
  border: 1px solid var(--border-default);
  border-radius: var(--radius-pill);
  background: var(--surface-overlay);
  color: var(--text-primary);
  box-shadow: var(--shadow-sm);
  font-size: 11px;
  font-weight: 600;
  opacity: 0;
  transform: translateY(var(--space-1));
  transition: opacity var(--dur-fast) var(--ease-out),
              transform var(--dur-fast) var(--ease-out);
  pointer-events: none;
}
.msg.user .bubble-body .media-thumbnail-hint { color: var(--text-primary); }
.media-thumbnail:hover .media-thumbnail-hint,
.media-thumbnail:focus-visible .media-thumbnail-hint,
.media-thumbnail:focus-within .media-thumbnail-hint {
  opacity: 1;
  transform: translateY(0);
}
.media-thumbnail-play {
  position: absolute;
  left: 50%;
  top: 50%;
  display: flex;
  align-items: center;
  justify-content: center;
  width: calc(var(--space-8) + var(--space-2));
  height: calc(var(--space-8) + var(--space-2));
  border: 1px solid var(--border-default);
  border-radius: var(--radius-pill);
  background: var(--surface-overlay);
  color: var(--text-primary);
  box-shadow: var(--shadow-md);
  transform: translate(-50%, -50%);
  transition: transform var(--dur-fast) var(--ease-out);
  pointer-events: none;
}
.msg.user .bubble-body .media-thumbnail-play { color: var(--text-primary); }
.media-thumbnail--video:hover .media-thumbnail-play {
  transform: translate(-50%, -50%) scale(1.06);
}
.msg.user .media-thumbnail,
.msg.user .attachment-audio-card,
.msg.user .msg-file-wrap,
.msg.user .msg-image-warn {
  justify-self: end;
}
.msg.user .attachment-audio-card,
.msg.user .msg-file-wrap,
.msg.user .msg-image-warn {
  grid-column: 1 / -1;
}
.media-thumbnail:hover .attachment-action-bar,
.media-thumbnail:focus-within .attachment-action-bar {
  opacity: 1;
  transform: translateY(0);
  pointer-events: auto;
}
.attachment-action-bar {
  position: absolute;
  top: var(--space-1);
  right: var(--space-1);
  display: flex;
  align-items: center;
  gap: var(--space-1);
  padding: var(--space-1);
  background: var(--surface-1);
  border: 1px solid var(--border-strong);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-md);
  opacity: 0;
  transform: translateY(calc(-1 * var(--space-1)));
  pointer-events: none;
  transition: opacity var(--dur-fast) var(--ease-out),
              transform var(--dur-fast) var(--ease-out);
  z-index: 3;
}
.attachment-action-bar--image {
  top: var(--space-2);
  right: var(--space-2);
  bottom: auto;
  transform: translateY(calc(-1 * var(--space-1)));
}
.media-thumbnail--video .attachment-action-bar {
  top: var(--space-2);
  right: var(--space-2);
  bottom: auto;
  transform: translateY(calc(-1 * var(--space-1)));
}
.attach-action-btn {
  width: var(--space-7);
  height: var(--space-7);
  display: flex;
  align-items: center;
  justify-content: center;
  border: 1px solid var(--border-default);
  border-radius: var(--radius-sm);
  background: var(--surface-2);
  color: var(--text-primary);
  cursor: pointer;
  font-size: 12px;
  line-height: 1;
  padding: 0;
  transition: background var(--dur-fast) var(--ease-out),
              border-color var(--dur-fast) var(--ease-out),
              color var(--dur-fast) var(--ease-out),
              transform var(--dur-fast) var(--ease-out);
}
.attach-action-btn:hover {
  background: var(--brand-50);
  border-color: var(--brand-100);
  color: var(--brand-600);
}
.msg.user .bubble-body .attach-action-btn {
  color: var(--text-primary);
}
.msg.user .bubble-body .attach-action-btn:hover {
  color: var(--brand-600);
}
.attach-action-btn:active { transform: scale(0.92); }
.attach-action-btn:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: -2px;
}
.attachment-audio-card,
.attachment-file-card {
  box-sizing: border-box;
  width: min(calc(var(--space-8) * 10), 100%);
  background: var(--surface-1);
  border: 1px solid var(--border-default);
  border-radius: var(--radius-lg);
  box-shadow: var(--shadow-sm);
  color: var(--text-primary);
}
.attachment-audio-card {
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
  padding: var(--space-3);
}
.attachment-card-heading,
.attachment-file-card {
  display: flex;
  align-items: center;
  gap: var(--space-2);
}
.attachment-file-card { padding: var(--space-2); }
.attachment-card-icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: var(--space-8);
  height: var(--space-8);
  background: var(--surface-2);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-md);
  color: var(--text-secondary);
  flex-shrink: 0;
}
.msg.user .bubble-body .attachment-card-icon { color: var(--text-secondary); }
.attachment-card-info {
  display: flex;
  flex: 1;
  flex-direction: column;
  gap: 2px;
  min-width: 0;
}
.attachment-card-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  color: var(--text-primary);
  font-size: 12.5px;
  font-weight: 600;
}
.attachment-card-meta {
  color: var(--text-tertiary);
  font-size: 11px;
}
.msg.user .bubble-body .attachment-card-name { color: var(--text-primary); }
.msg.user .bubble-body .attachment-card-meta { color: var(--text-tertiary); }
.attachment-card-actions {
  display: inline-flex;
  align-items: center;
  gap: var(--space-1);
  flex-shrink: 0;
}
.msg-audio {
  display: block;
  width: 100%;
  height: var(--space-8);
  color-scheme: light dark;
}
.msg-file-wrap {
  display: flex;
  justify-content: flex-end;
  width: 100%;
  max-width: 100%;
}
.msg.user .bubble-body .attachment-audio-card,
.msg.user .bubble-body .attachment-file-card {
  color: var(--text-primary);
}
.attachment-file-card:hover,
.attachment-audio-card:hover {
  border-color: var(--border-strong);
}
.attachment-file-card .attach-action-btn,
.attachment-audio-card .attach-action-btn {
  flex-shrink: 0;
}
@media (hover: none) {
  .bubble-actions,
  .attachment-action-bar {
    opacity: 1;
    transform: translateY(0);
    pointer-events: auto;
  }
  .media-thumbnail-hint {
    opacity: 1;
    transform: translateY(0);
  }
}
@media (max-width: 560px) {
  .attachment-audio-card,
  .attachment-file-card {
    width: 100%;
  }
  .media-thumbnail-hint span { display: none; }
}
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

/* --- P1-4 regen-history UI: 上一版回答 chip + reply pager ----- */

/* The chip sits at the very top of the bubble body
 * (above the assistant header's normal flow? No — below
 * the header, above the parts). For an archived
 * (is_archived=true) row the chip is the user's anchor
 * to "this isn't the latest answer". Two tokens:
 * icon + label, deliberately tight so it doesn't
 * compete with the bubble body for visual weight. The
 * user-message preview lives in the pager (not the
 * chip) so the chip itself never crowds long user
 * content. */
.bubble-archived-chip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  margin-bottom: 6px;
  padding: 2px 8px;
  background: var(--surface-2);
  border: 1px solid var(--border-subtle);
  border-radius: 4px;
  font-size: 11px;
  color: var(--text-tertiary);
  user-select: none;
  align-self: flex-start;
}
.bubble-archived-label {
  color: var(--text-secondary);
  font-weight: 500;
  letter-spacing: 0.2px;
}

/* The reply pager: ◀ N/M · "preview" ▶ pill. Brand-tinted
 * to match the rest of the assistant's color story. The
 * user-message preview is inlined so the user can
 * identify the sibling set at a glance (the chip
 * doesn't carry the preview to keep it small). ◀/▶
 * are 16×16 round buttons; the position text uses
 * tabular-nums so the width doesn't jitter between
 * single- and double-digit positions. */
.bubble-reply-pager {
  display: inline-flex;
  align-items: center;
  gap: 2px;
  margin-top: 6px;
  padding: 2px 4px;
  background: var(--brand-50);
  border: 1px solid var(--brand-100);
  border-radius: 10px;
  font-size: 11px;
  font-variant-numeric: tabular-nums;
  color: var(--brand-700);
  user-select: none;
  align-self: flex-start;
  max-width: 100%;
  /* P1-4 lazy-load: a tiny slide-in when the pager first
   * mounts so the user notices it appeared (most
   * bubbles never had a pager before). Subtle — 8px
   * over 140ms — and uses the same --dur-fast easing
   * as the rest of the toolbar. */
  animation: bubble-reply-pager-in var(--dur-fast) var(--ease-out);
}
.bubble-reply-pager-btn {
  border: none;
  background: transparent;
  color: inherit;
  width: 18px;
  height: 18px;
  display: inline-flex;
  align-items: center;
  justify-content: center;
  border-radius: 50%;
  cursor: pointer;
  padding: 0;
  font-size: 12px;
  line-height: 1;
  transition: background var(--dur-fast) var(--ease-out),
              color var(--dur-fast) var(--ease-out);
}
.bubble-reply-pager-btn:hover:not(:disabled) {
  background: var(--brand-100);
  color: var(--brand-700);
}
.bubble-reply-pager-btn:focus-visible {
  outline: 2px solid var(--accent);
  outline-offset: -2px;
}
.bubble-reply-pager-btn:disabled {
  opacity: 0.35;
  cursor: not-allowed;
}
.bubble-reply-pager-pos {
  min-width: 28px;
  text-align: center;
  font-weight: 500;
  padding: 0 2px;
}
.bubble-reply-pager-context {
  /* User-message preview in the pager. Truncated at
   * 12 chars by the truncatedPreview computed; the
   * title attribute on the parent button (or a
   * future tooltip) could surface the full text. */
  color: var(--brand-600);
  font-style: italic;
  margin: 0 4px 0 2px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 220px;
}
@keyframes bubble-reply-pager-in {
  from { opacity: 0; transform: translateY(-2px); }
  to   { opacity: 1; transform: translateY(0); }
}

/* --- Streaming + meta -------------------------------------------- */
/* The whole-bubble opacity pulse was removed (2026-08-06): animating
 * opacity on a large message layer every frame keeps the WebView2 GPU
 * process (compositor) busy for the entire stream — 60fps for minutes
 * — with no info gain. Streaming is already signalled cheaply by the
 * 6px stream-dot, the TypedText caret, and the thinking spinner. */


/* --- Send-time footer (user messages only) -----------------------
 * Sits below the user bubble; right-aligned because the user
 * bubble-col is row-reverse / align-items: flex-end. Uses the
 * quietest text tier + tabular numerals so it never competes
 * with the message body. */
.msg-time {
  font-size: 10.5px;
  line-height: 1.4;
  color: var(--text-quaternary);
  font-variant-numeric: tabular-nums;
  display: flex;
  align-items: center;
  padding: 0 var(--space-1);
  user-select: none;
}
.msg.user .msg-time { align-self: flex-end; }

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
/* P0-3 auto-continue: highlight the status line so the
 * user notices that the agent re-prompted itself rather
 * than the LLM actually finishing. Triggers when the
 * line contains "自动续 LLM" (the message we emit from
 * agent.go ChatWithTools when the todo-incomplete
 * guard fires). */
.status-line-auto-continue {
  color: var(--accent);
  font-weight: 600;
}

/* P3-3: trace id chip. Small monospace button under the
   message body so the user can copy the correlation id
   when reporting an error. Subtle visual treatment —
   not an alert, not a warning — so successful turns
   don't carry any UI debt. */
.trace-id-chip {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  margin-top: 6px;
  padding: 2px 8px;
  font-size: 11px;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  color: var(--text-2);
  background: var(--bg-2);
  border: 1px solid var(--border);
  border-radius: 4px;
  cursor: pointer;
  transition: var(--transition-colors);
}
.trace-id-chip:hover {
  background: var(--bg-3, var(--bg-2));
  border-color: var(--accent);
  color: var(--accent);
}
.trace-id-text {
  letter-spacing: 0.02em;
}
</style>

<style>
/* User-bubble code blocks. The user message's markdown is injected
 * via v-html, so the <pre>/<code> elements carry no [data-v]
 * attribute and the scoped styles above never reach them. These
 * global rules force a dark translucent surface + white text
 * (--on-brand) on the brand-coloured user bubble, and override the
 * highlight.js token colours (dark in light mode — unreadable on
 * the brand-blue bubble). Assistant bubbles are NOT affected: they
 * stay on the theme's --code-bg surface with token-coloured text. */
.msg.user .md-body pre {
  background: rgba(0, 0, 0, 0.25);
  border-color: rgba(255, 255, 255, 0.18);
}
.msg.user .md-body pre code {
  background: transparent;
  color: var(--on-brand);
}
.msg.user .md-body pre code.hljs span {
  color: var(--on-brand);
}
.msg.user .md-body code:not(pre code) {
  background: rgba(255, 255, 255, 0.18);
  color: var(--on-brand);
}

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
  opacity: 1;
  transition: opacity var(--dur-fast) var(--ease-out);
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
