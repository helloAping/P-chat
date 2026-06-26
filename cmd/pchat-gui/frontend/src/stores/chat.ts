// Reactive state for the chat UI. A single Pinia-free composable
// keeps the surface small — we don't need time-travel debugging
// or modular stores for a chat app.

import { reactive, ref, computed } from 'vue'
import * as api from '../api/client'
import type { Message, Session, UploadMeta, MessageAttachment } from '../api/client'

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
    onEvent: (ev: api.StreamEvent) => void
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

export function startStream(id: string, ctrl: AbortController, onEvent: (ev: api.StreamEvent) => void) {
  state.streaming[id] = { ctrl, asstContent: '', onEvent }
}

export function stopStream(id: string) {
  const s = state.streaming[id]
  if (s) {
    s.ctrl.abort()
    delete state.streaming[id]
  }
}

export function appendAssistantChunk(id: string, chunk: string) {
  if (!state.sessionMessages[id]) state.sessionMessages[id] = []
  const msgs = state.sessionMessages[id]
  if (msgs.length === 0 || msgs[msgs.length - 1].role !== 'assistant') {
    msgs.push({ role: 'assistant', content: '' })
  }
  msgs[msgs.length - 1].content += chunk
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
  delete state.streaming[id]
}

export const isStreaming = computed(() => !!state.streaming[state.currentID])
