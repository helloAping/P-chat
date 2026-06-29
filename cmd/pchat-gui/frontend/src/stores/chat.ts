// Reactive state for the chat UI. A single Pinia-free composable
// keeps the surface small — we don't need time-travel debugging
// or modular stores for a chat app.

import { reactive, ref, computed } from 'vue'
import * as api from '../api/client'
import type { Message, Session, UploadMeta, MessageAttachment, MessagePart, SubAgentPart, ToolPart, TodoItem, ProjectItem } from '../api/client'

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
  // Resolved default model from the providers list. Set by
  // loadProviders() so the per-session fallback (when the
  // server hasn't told us yet) has something meaningful to
  // show — otherwise the chat NSelect renders empty and the
  // "no model selected" symptom is indistinguishable from
  // "no providers configured".
  defaultModel: null as { provider: string; model: string } | null,
  sessionMeta: {} as Record<string, { style: string; provider: string; model: string; title: string; plan_mode?: boolean }>,
  sessionTodos: {} as Record<string, TodoItem[]>,
  lightbox: { show: false, src: '', alt: '' },
  showSettings: false,
  projects: [] as ProjectItem[],
  activeProjectPath: '' as string,
})

export const currentMessages = computed(() =>
  state.sessionMessages[state.currentID] || [],
)

export const currentTodos = computed(() =>
  state.sessionTodos[state.currentID] || [],
)

export const activeProjectName = computed(() => {
  if (!state.activeProjectPath) return '全局'
  const p = state.projects.find(p => p.path === state.activeProjectPath)
  return p?.name || state.activeProjectPath
})

// currentMeta resolves the per-session picker state
// (style / provider / model / title). When the server
// hasn't told us yet (newly created session, or a
// switchSession race before listMessages returns),
// fall back to the resolved default model so the chat
// NSelect shows a real value instead of being empty.
export const currentMeta = computed(() => {
  const m = state.sessionMeta[state.currentID]
  if (m) return m
  const def = state.defaultModel
  return {
    style: 'tech',
    provider: def?.provider || '',
    model: def?.model || '',
    title: '',
  }
})

// --- Session management ---

export async function loadSessions() {
  const { sessions } = await api.listSessions(state.activeProjectPath)
  state.sessions = sessions
  if (!state.currentID && sessions.length > 0) {
    await switchSession(sessions[0].id)
  }
}

export async function loadProjects() {
  try {
    const r = await api.listProjects()
    state.projects = r.projects || []
  } catch {
    state.projects = []
  }
}

export async function setActiveProject(path: string) {
  state.activeProjectPath = path
  state.currentID = ''
  state.sessions = []
  state.sessionMessages = {}
  state.sessionMeta = {}
  state.sessionTodos = {}
  await loadSessions()
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
  //
  // The session might be brand-new (just created via
  // createSession in send()) and therefore not yet in
  // state.sessions; the find() below is purely a label
  // fallback. If we miss, we leave sessionMeta[id] alone —
  // currentMeta() will fall back to the resolved default
  // model.
  const s = state.sessions.find(s => s.id === id)
  if (s) {
    state.sessionMeta[id] = {
      style:     s.style || 'tech',
      provider:  s.provider || '',
      model:     s.model || '',
      title:     s.title || '',
      plan_mode: s.plan_mode || false,
    }
  }
  // Load per-session todos.
  if (!state.sessionTodos[id]) {
    try {
      const t = await api.getTodos(id)
      state.sessionTodos[id] = t.todos || []
    } catch { /* ignore — server may not have todos yet */ }
  }
}

// loadProviders fetches the provider list and resolves
// the "default" model — the first provider with
// is_default: true, else the first provider; within that
// provider, the first model with default: true, else the
// first model. This is the same fallback chain the
// server applies server-side, computed once on the client
// so the chat NSelect can show a real value before any
// session is created / before any user pick.
export async function loadProviders() {
  try {
    const r = await api.listProviders()
    const ps = r.providers || []
    state.providers = ps
    if (ps.length === 0) {
      state.defaultModel = null
      return
    }
    const def = ps.find(p => p.is_default) || ps[0]
    const m = (def.models || []).find(x => x.default) || (def.models || [])[0]
    if (def && m) {
      state.defaultModel = { provider: def.name, model: m.name }
    } else if (def && def.model) {
      // Legacy single-model form.
      state.defaultModel = { provider: def.name, model: def.model }
    } else {
      state.defaultModel = null
    }
  } catch {
    state.defaultModel = null
  }
}

export async function createSession(): Promise<string> {
  const projectPath = state.activeProjectPath || undefined
  const { id } = await api.createSession(projectPath)
  // Fetch the freshly created session's resolved meta from
  // the server. The server's sessionToResponse applies the
  // per-session override → global default chain for
  // provider/model, so this is the only place we get a
  // guaranteed-correct default value to seed state.sessions
  // with. Without this round-trip the chat NSelect would
  // stay empty until the user manually picked a model.
  let resolved: Session | null = null
  try {
    resolved = await api.getSession(id)
  } catch {
    // Non-fatal: switchSession will still set up a
    // sessionMeta entry; currentMeta falls back to
    // state.defaultModel.
  }
  const fresh: Session = resolved || {
    id,
    title: '(新会话)',
    created_at: Date.now() / 1000,
    updated_at: Date.now() / 1000,
  }
  // If the server returned a session with the
  // already-resolved title (it does — sessionToResponse
  // always returns the persisted title), use it; otherwise
  // keep the placeholder.
  state.sessions.unshift(fresh)
  await switchSession(id)
  return id
}

export async function deleteSessionById(id: string) {
  await api.archiveSession(id)
  state.sessions = state.sessions.filter(s => s.id !== id)
  delete state.sessionMessages[id]
  delete state.sessionMeta[id]
  delete state.sessionTodos[id]
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
      // Sync todo list from todo_write tool results.
      if (ev.tool_name === 'todo_write' && ev.tool_status === 'ok' && ev.tool_result) {
        try {
          const todos: TodoItem[] = JSON.parse(ev.tool_result)
          state.sessionTodos[id] = todos
        } catch { /* not JSON, ignore */ }
      }
      break
    }
    case 'phase':
      // Sub-agent lifecycle: open / close the nested card.
      if (ev.sub_agent_status) {
        if (!sub) break
        sub.status = ev.sub_agent_status as any
        if (ev.sub_agent_status !== 'start' && ev.elapsed) sub.elapsed = ev.elapsed
      }
      // Surface phase messages as a live status bar.
      if (ev.message) {
        if (!m._statusText) (m as any)._statusText = []
        ;(m as any)._statusText.push(ev.message)
      }
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
      // Vision-unsupported errors get extra treatment:
      // tag the trailing user message so the bubble
      // shows a dedicated warning chip under the
      // attachments. The chip outlives the toast and
      // tells the user *which* image was ignored and
      // how to fix it.
      if (ev.error_kind === 'vision_unsupported') {
        markVisionUnsupported(id)
      }
      break
  }
}

// markVisionUnsupported finds the trailing user message in
// the given session and sets `visionUnsupported: true`. Used
// when the LLM rejects the user's image with the
// "model does not support image input" error. Idempotent —
// if the message is already tagged, this is a no-op so
// re-rendering the same error event is safe.
function markVisionUnsupported(id: string) {
  const msgs = state.sessionMessages[id]
  if (!msgs) return
  for (let i = msgs.length - 1; i >= 0; i--) {
    const m = msgs[i]
    if (m.role === 'user') {
      m.visionUnsupported = true
      return
    }
    if (m.role === 'assistant') {
      // Walk past the assistant turn that produced the
      // error; the user message that triggered it is
      // somewhere above. If we hit the top of the
      // session without finding one, the message has
      // been pruned and we drop the tag silently.
    }
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
  api.saveSystemMessage(id, text)
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
