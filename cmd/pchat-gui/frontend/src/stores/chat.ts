// Reactive state for the chat UI. A single Pinia-free composable
// keeps the surface small — we don't need time-travel debugging
// or modular stores for a chat app.

import { reactive, ref, computed } from 'vue'
import * as api from '../api/client'
import type { Message, Session, UploadMeta, MessageAttachment, MessagePart, SubAgentPart, ToolPart } from '../api/client'

export interface PendingAttachment {
  // Server-side metadata returned from /uploads.
  id: string
  name: string
  size: number
  mime: string
  kind: string
  // Local browser-side state.
  _file?: File
  _blobURL?: string
  _uploading: boolean
  _error: boolean
  // Local URL for immediate preview (blob: during upload,
  // server URL after).
  _previewURL: string
  // Cached data: URL (base64) for the file. Populated on add so
  // the send path can inline the bytes without an extra read.
  // For images this is "data:image/...;base64,..." which the
  // MessageBubble renders directly and the backend can pass
  // straight through to the LLM.
  _dataURL?: string
}

export const state = reactive({
  sessions: [] as Session[],
  currentID: '' as string,
  sessionMessages: {} as Record<string, Message[]>,
  streaming: {} as Record<string, {
    ctrl: AbortController
    asstContent: string
  }>,
  pendingAttachments: [] as PendingAttachment[],
  providers: [] as any[],
  sessionMeta: {} as Record<string, { style: string; provider: string; model: string; title: string }>,
  lightbox: { show: false, src: '', alt: '' },
  showSettings: false,
})

export const currentMessages = computed(() =>
  state.sessionMessages[state.currentID] || [],
)

export const currentMeta = computed(() =>
  state.sessionMeta[state.currentID] || { style: 'tech', provider: '', model: '', title: '' },
)

// --- Session management ---

export async function loadSessions() {
  const { sessions } = await api.listSessions()
  state.sessions = sessions
  if (!state.currentID && sessions.length > 0) {
    await switchSession(sessions[0].id)
  }
}

export async function switchSession(id: string) {
  state.currentID = id
  if (!state.sessionMessages[id]) {
    const r = await api.listMessages(id)
    state.sessionMessages[id] = r.messages
  }
  // Hydrate per-session meta. The server's SessionResponse
  // (GET /api/v1/sessions) already resolves style/provider/model
  // through the per-session override → global default chain
  // (see server.sessionToResponse), so we trust those values
  // directly. The old code read s?.metadata which the server
  // never sends — that's why switching sessions always wiped
  // the pickers back to "tech" / empty.
  const s = state.sessions.find(s => s.id === id)
  state.sessionMeta[id] = {
    style: s?.style || 'tech',
    provider: s?.provider || '',
    model: s?.model || '',
    title: s?.title || '',
  }
}

export async function createSession(): Promise<string> {
  const { id } = await api.createSession()
  state.sessions.unshift({ id, title: '(新会话)', created_at: Date.now() / 1000, updated_at: Date.now() / 1000 })
  await switchSession(id)
  return id
}

export async function deleteSessionById(id: string) {
  await api.deleteSession(id)
  state.sessions = state.sessions.filter(s => s.id !== id)
  delete state.sessionMessages[id]
  delete state.sessionMeta[id]
  if (state.currentID === id) {
    state.currentID = state.sessions[0]?.id || ''
    if (state.currentID) await switchSession(state.currentID)
  }
}

export async function renameSession(id: string, title: string) {
  const resp = await api.renameSession(id, title)
  const s = state.sessions.find(s => s.id === id)
  // PATCH /sessions/:id returns a full SessionResponse for both
  // the legacy {title} body and the meta-update body, so we use
  // it as the canonical source instead of the body we just sent.
  if (s) {
    s.title = resp.title ?? title
    s.style = resp.style ?? s.style
    s.provider = resp.provider ?? s.provider
    s.model = resp.model ?? s.model
  }
  if (state.sessionMeta[id]) {
    state.sessionMeta[id] = {
      ...state.sessionMeta[id],
      title: resp.title ?? title,
      style: resp.style ?? state.sessionMeta[id].style,
      provider: resp.provider ?? state.sessionMeta[id].provider,
      model: resp.model ?? state.sessionMeta[id].model,
    }
  }
}

// --- Attachments ---

export function guessKind(name: string, mime: string): string {
  const ext = (name || '').split('.').pop()?.toLowerCase() || ''
  const imageExts = ['png', 'jpg', 'jpeg', 'gif', 'webp', 'bmp', 'svg', 'ico', 'tiff', 'tif']
  const audioExts = ['mp3', 'wav', 'm4a', 'ogg', 'flac', 'opus', 'aac', 'pcm', 'wma']
  const textExts  = ['txt', 'md', 'csv', 'json', 'yaml', 'yml', 'xml', 'html', 'htm',
    'js', 'ts', 'tsx', 'jsx', 'go', 'py', 'rs', 'java', 'c', 'cpp',
    'h', 'hpp', 'cs', 'rb', 'php', 'sh', 'bash', 'zsh', 'ps1',
    'ini', 'toml', 'env', 'log', 'sql', 'css', 'scss', 'less',
    'vue', 'svelte', 'swift', 'kt', 'scala', 'r', 'm', 'mm']
  if (imageExts.includes(ext)) return 'image'
  if (audioExts.includes(ext)) return 'audio'
  if (textExts.includes(ext)) return 'text'
  if (mime?.startsWith('image/')) return 'image'
  if (mime?.startsWith('audio/')) return 'audio'
  if (mime && (mime.startsWith('text/') || mime === 'application/json')) return 'text'
  return 'file'
}

export async function addAttachment(file: File) {
  const idx = state.pendingAttachments.length
  const guessedKind = guessKind(file.name, file.type || '')
  const blobURL = URL.createObjectURL(file)
  const placeholder: PendingAttachment = {
    id: '', name: file.name, size: file.size, mime: file.type || '',
    kind: guessedKind,
    _file: file, _blobURL: blobURL, _uploading: true, _error: false,
    _previewURL: blobURL,
  }
  state.pendingAttachments.push(placeholder)
  // Cache a base64 data URL up-front so the message can be
  // displayed + sent without re-reading the file from disk.
  // For text attachments this is just the utf-8 text; for binary
  // (images/audio) it's the data: URL the LLM wants anyway.
  try {
    placeholder._dataURL = await readAsDataURL(file, guessedKind)
  } catch {
    // Non-fatal: the upload fallback still gives us a server URL.
  }
  try {
    const meta = await api.uploadFile(file)
    placeholder.id = meta.id
    placeholder.kind = meta.kind || guessedKind
    placeholder.mime = meta.mime
    placeholder._uploading = false
    // Keep showing the local data: URL / blob URL for the input
    // strip — the server URL is for the persistent store only,
    // and switching to it mid-render causes a visible flicker.
    // (See: previous bug where the preview jumped from blob to
    // server URL after upload completed.)
  } catch (e: any) {
    placeholder._uploading = false
    placeholder._error = true
    throw e
  }
}

// readAsDataURL returns a string suitable for the image_url/url
// field of an OpenAI multi-part content. For binary files it's
// the file's data: URL; for text files it's the file's contents
// wrapped in a synthetic blob: URL so the LLM can read it as a
// text part without an image_data payload.
function readAsDataURL(file: File, kind: string): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader()
    reader.onerror = () => reject(reader.error)
    reader.onload = () => resolve(String(reader.result || ''))
    if (kind === 'text') {
      reader.readAsText(file)
    } else {
      reader.readAsDataURL(file)
    }
  })
}

export function removeAttachment(idx: number) {
  const a = state.pendingAttachments[idx]
  if (a?._blobURL) URL.revokeObjectURL(a._blobURL)
  state.pendingAttachments.splice(idx, 1)
}

export function clearAttachments() {
  for (const a of state.pendingAttachments) {
    if (a._blobURL) URL.revokeObjectURL(a._blobURL)
  }
  state.pendingAttachments = []
}

// --- Streaming ---
//
// The streaming layer used to be three primitives:
//   - startStream(id, ctrl, cb)        — install a callback
//   - appendAssistantChunk(id, text)  — concat text
//   - endStream(id)                   — uninstall
//
// The chat UI now needs to render a lot more than just text
// (thinking, tool calls, sub-agents), and each kind of event
// arrives in its own `type` field. We've collapsed the three
// primitives into a single dispatch:
//
//   - startStream(id)        — install a placeholder
//   - appendStreamEvent(id, ev) — dispatch by ev.type
//   - endStream(id)          — flush final state
//
// `startStream` creates the trailing assistant message
// eagerly (so the three-bouncing-dots placeholder is
// reachable), and `appendStreamEvent` is the only entry
// point that mutates the message body. Anything that wants
// to feed a stream into the chat goes through this.

export function startStream(id: string, ctrl: AbortController) {
  if (!state.sessionMessages[id]) state.sessionMessages[id] = []
  // Push a placeholder assistant message immediately. The
  // MessageBubble's loading-dots placeholder requires the
  // message object to exist *before* the first content
  // event — without this, the spinner is unreachable.
  state.sessionMessages[id].push({ role: 'assistant', content: '', parts: [] })
  state.streaming[id] = { ctrl, asstContent: '' }
}

export function stopStream(id: string) {
  const s = state.streaming[id]
  if (s) {
    s.ctrl.abort()
    delete state.streaming[id]
  }
}

// findOrCreateLastAssistant walks the trailing message
// list and returns the last assistant message, creating
// one if the list is empty or the last message isn't an
// assistant. Used by appendStreamEvent to guarantee a
// target for content / thinking deltas.
function findOrCreateLastAssistant(id: string): Message {
  if (!state.sessionMessages[id]) state.sessionMessages[id] = []
  const msgs = state.sessionMessages[id]
  if (msgs.length === 0 || msgs[msgs.length - 1].role !== 'assistant') {
    msgs.push({ role: 'assistant', content: '', parts: [] })
  }
  const m = msgs[msgs.length - 1]
  if (!m.parts) m.parts = []
  return m
}

// findOrCreateSubAgent locates the trailing sub_agent
// part matching `task` inside an assistant message,
// creating it if missing. Sub-agents are not nested
// further (a sub-agent can't spawn sub-agents), so the
// parts list we operate on is always at the top level.
function findOrCreateSubAgent(m: Message, task: string): MessagePart & { kind: 'sub_agent' } {
  if (!m.parts) m.parts = []
  for (let i = m.parts.length - 1; i >= 0; i--) {
    const p = m.parts[i]
    if (p.kind === 'sub_agent' && p.task === task) return p
  }
  const sub: MessagePart = {
    kind: 'sub_agent',
    task,
    status: 'start',
    parts: [],
  }
  m.parts.push(sub)
  return sub as any
}

// appendToSubAgent routes a content / thinking delta
// into the matching sub_agent part, or into the parent
// message itself if there's no sub-agent. Tool events
// inside a sub-agent are attached to its inner parts.
function appendToSubAgent(
  sub: SubAgentPart | null,
  m: Message,
  mutator: (target: Message) => void,
) {
  if (sub) {
    // The sub-agent's parts are wrapped in a synthetic
    // Message shape so the same mutator can work on
    // either. We pass a thin stand-in; mutator should
    // only touch the parts + content. We re-assign
    // `sub.parts` afterwards so the changes propagate
    // back to the parent's parts array.
    const standin: Message = { role: 'assistant', content: sub.task, parts: sub.parts }
    mutator(standin)
    sub.parts = standin.parts ?? []
  } else {
    mutator(m)
  }
}

// appendTextPart appends a text delta to the trailing
// text part of the message, creating a new text part
// first if the last part isn't text. Mirrors the
// behaviour of DeepSeek-style UIs that show thinking
// and text side by side: streaming text never overwrites
// a thinking block.
function appendTextPart(m: Message, delta: string, target?: MessagePart[] | null) {
  const parts = (target ?? m.parts)!
  if (parts.length === 0 || parts[parts.length - 1].kind !== 'text') {
    parts.push({ kind: 'text', text: delta })
  } else {
    ;(parts[parts.length - 1] as any).text += delta
  }
  m.content += delta
}

function appendThinkingPart(m: Message, delta: string, target?: MessagePart[] | null) {
  const parts = (target ?? m.parts)!
  if (parts.length === 0 || parts[parts.length - 1].kind !== 'thinking') {
    parts.push({ kind: 'thinking', text: delta, streaming: true })
  } else {
    const p = parts[parts.length - 1] as any
    p.text += delta
    p.streaming = true
  }
}

// appendStreamEvent is the single dispatch point for
// every stream chunk. It mutates the trailing assistant
// message in place and never re-creates it.
//
// Routing rules:
//   - sub_agent=true events are routed into the matching
//     sub_agent part (keyed by sub_agent_task). If no
//     sub_agent_start has been seen yet (e.g. we missed
//     the start event because of buffering), the part is
//     created lazily.
//   - 'tool' events with sub_agent=true go into the
//     sub-agent's inner parts.
//   - 'content' / 'thinking' deltas: routed by sub_agent
//     flag as above.
//   - 'phase' / 'done' / 'error': top-level only (the
//     sub-agent emits its own start/ok/err phases).
//   - 'done' stamps the message with token counts.
export function appendStreamEvent(id: string, ev: api.StreamEvent) {
  const m = findOrCreateLastAssistant(id)
  if (!m.parts) m.parts = []

  // Locate / create the sub-agent part if applicable.
  let sub: (MessagePart & { kind: 'sub_agent' }) | null = null
  if (ev.sub_agent && ev.sub_agent_task) {
    sub = findOrCreateSubAgent(m, ev.sub_agent_task)
  }

  switch (ev.type) {
    case 'content':
      if (ev.content) {
        if (sub) {
          appendTextPart(m, ev.content, sub.parts)
        } else {
          appendTextPart(m, ev.content)
        }
      }
      break
    case 'thinking':
      if (ev.thinking) {
        if (sub) {
          appendThinkingPart(m, ev.thinking, sub.parts)
        } else {
          appendThinkingPart(m, ev.thinking)
        }
      }
      break
    case 'tool': {
      // Sub-agent tools render inside the sub-agent card;
      // parent tools render at the top level. Either way
      // they're appended to the matching part list.
      const parts = sub ? sub.parts : m.parts!
      if (!ev.tool_name) break
      if (ev.tool_status === 'start') {
        // Push a new tool part for this call. If the
        // last part is already an unfinished tool with
        // the same name, reuse it (defensive — usually
        // there's a clear "start" then "ok" pair).
        const last = parts[parts.length - 1]
        if (last && last.kind === 'tool' && last.status === 'start' && last.name === ev.tool_name) {
          last.args = ev.tool_args
        } else {
          parts.push({
            kind: 'tool',
            id: ev.tool_name,
            name: ev.tool_name,
            args: ev.tool_args,
            status: 'start',
          })
        }
      } else {
        // 'ok' / 'warn' / 'error' — find the matching
        // tool part (most recent unfished one with the
        // same name) and update it.
        let found = false
        for (let i = parts.length - 1; i >= 0; i--) {
          const p = parts[i]
          if (p.kind === 'tool' && p.name === ev.tool_name && p.status === 'start') {
            p.status = (ev.tool_status as any) || 'ok'
            p.result = ev.tool_result
            p.error = ev.tool_error
            p.elapsed = ev.tool_elapsed
            if (ev.tool_args) p.args = ev.tool_args
            found = true
            break
          }
        }
        if (!found) {
          // No matching start event — just append a
          // completed tool part.
          parts.push({
            kind: 'tool',
            id: ev.tool_name,
            name: ev.tool_name,
            args: ev.tool_args,
            status: (ev.tool_status as any) || 'ok',
            result: ev.tool_result,
            error: ev.tool_error,
            elapsed: ev.tool_elapsed,
          })
        }
      }
      break
    }
    case 'phase':
      // Sub-agent lifecycle: open / close the nested card.
      if (ev.sub_agent_status) {
        if (!sub) break // unknown task — drop
        sub.status = ev.sub_agent_status as any
        if (ev.sub_agent_status !== 'start' && ev.elapsed) sub.elapsed = ev.elapsed
      }
      // Other phases (system / memory / plan) are
      // intentionally not rendered — they were
      // implementation chatter and the user only
      // complained about noise, never about missing
      // progress indicators. (If we want to bring them
      // back as a status strip, the place to hook is
      // here.)
      break
    case 'done':
      // Final token counts. We don't reset streaming
      // flags here — that's the consumer's job (the
      // bubble stops pulsing once isStreaming flips off,
      // and thinking flags get cleared in endStream).
      if (ev.tokens_in != null) m.tokens_in = ev.tokens_in
      if (ev.tokens_out != null) m.tokens_out = ev.tokens_out
      if (ev.elapsed) m.elapsed = ev.elapsed
      if (ev.provider) m.provider = ev.provider
      if (ev.model) m.model = ev.model
      // Mark all open thinking parts as no longer streaming.
      walkParts(m.parts!, (p) => {
        if (p.kind === 'thinking' && p.streaming) p.streaming = false
      })
      break
    case 'error':
      // Render the error in the message content so the
      // user sees it. The MessageBubble also styles
      // it as a soft-error variant.
      appendTextPart(m, ev.error ? `\n\n⚠ ${ev.error}\n` : '\n\n⚠ (stream error)\n')
      break
  }
}

function walkParts(parts: MessagePart[], fn: (p: MessagePart) => void) {
  for (const p of parts) {
    fn(p)
    if (p.kind === 'sub_agent') walkParts(p.parts, fn)
  }
}

// appendSystemMessage pushes a UI-only "system" message bubble
// (e.g. slash-command output) into the current session's view.
// It's not sent to the LLM and is purely informational. The role
// "system" is reused for render purposes; the markdown pipeline
// handles it the same as assistant messages.
export function appendSystemMessage(text: string) {
  const id = state.currentID
  if (!id) return
  if (!state.sessionMessages[id]) state.sessionMessages[id] = []
  state.sessionMessages[id].push({ role: 'system', content: text })
}

export function endStream(id: string) {
  const msgs = state.sessionMessages[id]
  if (msgs) {
    const last = msgs[msgs.length - 1]
    if (last && last.role === 'assistant' && last.parts) {
      // Clear any leftover streaming flags on thinking
      // parts so the bubble doesn't keep its shimmer.
      walkParts(last.parts, (p) => {
        if (p.kind === 'thinking' && p.streaming) p.streaming = false
      })
    }
  }
  delete state.streaming[id]
}

export const isStreaming = computed(() => !!state.streaming[state.currentID])
