// Reactive state for the chat UI. A single Pinia-free composable
// keeps the surface small — we don't need time-travel debugging
// or modular stores for a chat app.

import { reactive, ref, computed, watch } from 'vue'
import * as api from '../api/client'
import { notifyManager } from '../utils/notify'
import type { Message, Session, UploadMeta, MessageAttachment, MessagePart, SubAgentPart, ToolPart, TodoItem, ProjectItem, QuestionItem } from '../api/client'

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
  // Per-session history-paging cursor. The first page
  // (loaded by switchSession) sets oldestId to the id of the
  // last (oldest) message in the page; subsequent pages are
  // fetched with before_id=oldestId. When hasMore is false,
  // the user has scrolled to the start of the conversation.
  sessionPaging: {} as Record<string, {
    oldestId: number
    hasMore: boolean
    loading: boolean
  }>,
  // Attachments staged in the InputArea. Per-session so
  // switching conversations mid-edit keeps each session's
  // staged files intact — the previous global array would
  // either lose them on switch or leak across sessions.
  pendingAttachments: {} as Record<string, PendingAttachment[]>,
  providers: [] as any[],
  // Resolved default model from the providers list. Set by
  // loadProviders() so the per-session fallback (when the
  // server hasn't told us yet) has something meaningful to
  // show — otherwise the chat NSelect renders empty and the
  // "no model selected" symptom is indistinguishable from
  // "no providers configured".
  defaultModel: null as { provider: string; model: string } | null,
  sessionMeta: {} as Record<string, { style: string; provider: string; model: string; title: string; plan_mode?: boolean; permission_level?: string; reasoning_effort?: string }>,
  sessionTodos: {} as Record<string, TodoItem[]>,
  // sessionWorking is the per-session "is the LLM mid-turn"
  // flag, derived from the `session_status` SSE event. The
  // TodoPanel state machine reads this to decide whether
  // stale todos should be kept (live) or cleared (idle).
  // Default false (idle) — sessions that have never been
  // streamed to aren't busy.
  sessionWorking: {} as Record<string, boolean>,
  // Pending question from the LLM's question tool, keyed by
  // session id. Background sessions may have a question open
  // while the user is viewing another session, so the global
  // flag had to go.
  pendingQuestion: {} as Record<string, { questions: QuestionItem[]; resolve: (answers: Record<string, string>) => void }>,
  pendingConfirm: {} as Record<string, { toolName: string; args: string; reason: string; resolve: (approved: boolean) => void }>,
  lightbox: { show: false, src: '', alt: '', kind: 'image' as 'image' | 'video' },
  showSettings: false,
  projects: [] as ProjectItem[],
  activeProjectPath: '' as string,
  // Rollback undo buffer — only the most recent rollback per session.
  rollbackUndo: {} as Record<string, { messages: Message[]; fromIndex: number } | null>,
  // Pending text to fill into the input area after a rollback.
  pendingInput: {} as Record<string, string>,
})

export const currentMessages = computed(() =>
  state.sessionMessages[state.currentID] || [],
)

export const currentTodos = computed(() =>
  state.sessionTodos[state.currentID] || [],
)

// currentSessionWorking — true while the LLM is mid-turn
// for the current session. The TodoPanel state machine
// combines this with currentTodos to decide whether to
// show, hide, or clear the dock.
export const currentSessionWorking = computed(() =>
  !!state.sessionWorking[state.currentID],
)

// clearSessionTodos wipes the local todo list for a session.
// Used by the TodoPanel's "stale-clear" hack when the
// session goes idle without the LLM writing `todos: []`.
// Mirrors opencode's `session-composer-state.ts:113-118`:
// "Keep stale turn todos from reopening if the model
// never clears them." Server-side state in SQLite is NOT
// touched — only the in-memory cache. Reloading the
// session re-hydrates from SQLite.
export function clearSessionTodos(id: string) {
  if (!id) return
  state.sessionTodos[id] = []
}

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

// Global phantom scrub. Walks every message in every
// session whenever the messages map changes. The
// per-part scrub in `appendTextPart` catches the common
// case, but a stored phantom from a pre-fix version
// (or a phantom injected through a path we haven't
// audited) would still be visible until the user
// reloads. This deep watcher makes the scrub a
// no-op-when-clean, side-effect-when-dirty background
// job that runs on every reactive change. Performance
// is fine in practice (sessions have <100 messages,
// each with <20 parts, the regex is cheap).
//
// We scrub in place — the `m.parts` arrays are
// reactive so the Vue components re-render
// automatically. We only assign when the scrub
// actually changes a value, to avoid spurious
// reactive triggers.
watch(
  () => state.sessionMessages,
  (sessions) => {
    if (!sessions) return
    for (const id in sessions) {
      const msgs = sessions[id]
      if (!msgs) continue
      for (const m of msgs) {
        scrubMessagePhantoms(m)
      }
    }
  },
  { deep: true },
)

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
  state.sessionWorking = {}
  state.sessionPaging = {}
  // Revoke blob URLs for any staged attachments in the
  // sessions we're discarding, then drop the maps entirely.
  // Question / confirm dialogs keyed to those sessions get
  // resolved with no-op values so any awaiters unblock.
  for (const arr of Object.values(state.pendingAttachments)) {
    for (const a of arr) {
      if (a._blobURL) URL.revokeObjectURL(a._blobURL)
    }
  }
  state.pendingAttachments = {}
  for (const [id, pq] of Object.entries(state.pendingQuestion)) {
    pq.resolve({})
    delete state.pendingQuestion[id]
  }
  for (const [id, pc] of Object.entries(state.pendingConfirm)) {
    pc.resolve(false)
    delete state.pendingConfirm[id]
  }
  for (const [id, s] of Object.entries(state.streaming)) {
    s.ctrl.abort()
    delete state.streaming[id]
  }
  await loadSessions()
}

// initialHistoryLimit is the page size for the first history
// load when switching to a session. Picked to cover a typical
// long conversation (50 messages = ~25 turns) so the user
// rarely needs to scroll up to see more. Subsequent pages
// (loaded by loadMoreMessages) use the same size.
const initialHistoryLimit = 50

export async function switchSession(id: string) {
  state.currentID = id
  if (!state.sessionMessages[id]) {
    // First visit to this session: load the most recent page
    // only. Older pages are fetched on-demand by
    // loadMoreMessages when the user scrolls to the top of
    // the message list. This keeps switch-to-session latency
    // bounded regardless of how long the conversation is.
    const r = await api.listMessages(id, { limit: initialHistoryLimit })
    // Client-side phantom scrub. The server's
    // post-stream redactor normally catches these, but a
    // session loaded from disk may contain a phantom that
    // was persisted before the redactor existed (e.g. a
    // user upgrading from a version without the fix).
    // Walking the parts here and scrubbing any matching
    // text keeps the chat history clean.
    for (const m of r.messages) {
      if (m.parts) scrubMessagePhantoms(m)
    }
    state.sessionMessages[id] = r.messages
    state.sessionPaging[id] = {
      oldestId: r.oldest_id,
      hasMore: r.has_more,
      loading: false,
    }
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
      permission_level: s.permission_level || 'ask',
      reasoning_effort: s.reasoning_effort || 'off',
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

// loadMoreMessages fetches the next page of older history
// for the given session and prepends the result to the
// in-memory message list. Returns true if more pages remain
// (caller can chain another loadMoreMessages). Returns false
// when the session is at the start of its history or when
// a load is already in flight.
//
// Idempotency: if a load is already in flight (e.g. the
// user scrolled to the top multiple times in quick
// succession) the second call returns false without making
// an HTTP request — protects against request amplification
// from rapid scroll events.
export async function loadMoreMessages(id: string): Promise<boolean> {
  const paging = state.sessionPaging[id]
  if (!paging || !paging.hasMore || paging.loading) return false
  paging.loading = true
  try {
    const r = await api.listMessages(id, {
      beforeId: paging.oldestId,
      limit: initialHistoryLimit,
    })
    if (r.messages.length > 0) {
      // Prepend the new (older) page to the front of the
      // existing message list. The server returns messages
      // oldest-first within the page, so concatenation in
      // the order [older..., existing...] preserves the
      // global oldest-first ordering.
      const existing = state.sessionMessages[id] || []
      state.sessionMessages[id] = [...r.messages, ...existing]
    }
    paging.oldestId = r.oldest_id
    paging.hasMore = r.has_more
    return r.has_more
  } catch (e) {
    console.warn('loadMoreMessages failed:', e)
    return false
  } finally {
    paging.loading = false
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
  delete state.sessionWorking[id]
  delete state.sessionPaging[id]
  // Per-session state must also be torn down so a session with
  // the same id later (after createSession) doesn't see the
  // previous session's staged attachments or stale question /
  // confirm dialogs.
  for (const a of (state.pendingAttachments[id] || [])) {
    if (a._blobURL) URL.revokeObjectURL(a._blobURL)
  }
  delete state.pendingAttachments[id]
  if (state.pendingQuestion[id]) {
    state.pendingQuestion[id].resolve({})  // unblock any awaiter
    delete state.pendingQuestion[id]
  }
  if (state.pendingConfirm[id]) {
    state.pendingConfirm[id].resolve(false)
    delete state.pendingConfirm[id]
  }
  if (state.streaming[id]) {
    state.streaming[id].ctrl.abort()
    delete state.streaming[id]
  }
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
      permission_level: resp.permission_level ?? state.sessionMeta[id].permission_level,
      reasoning_effort: resp.reasoning_effort ?? state.sessionMeta[id].reasoning_effort,
    }
  }
}

// --- Attachments ---

export function guessKind(name: string, mime: string): string {
  const ext = (name || '').split('.').pop()?.toLowerCase() || ''
  const imageExts = ['png', 'jpg', 'jpeg', 'gif', 'webp', 'bmp', 'svg', 'ico', 'tiff', 'tif']
  const audioExts = ['mp3', 'wav', 'm4a', 'ogg', 'flac', 'opus', 'aac', 'pcm', 'wma']
  const videoExts = ['mp4', 'webm', 'mov', 'mkv', 'm4v', 'avi']
  const textExts  = ['txt', 'md', 'csv', 'json', 'yaml', 'yml', 'xml', 'html', 'htm',
    'js', 'ts', 'tsx', 'jsx', 'go', 'py', 'rs', 'java', 'c', 'cpp',
    'h', 'hpp', 'cs', 'rb', 'php', 'sh', 'bash', 'zsh', 'ps1',
    'ini', 'toml', 'env', 'log', 'sql', 'css', 'scss', 'less',
    'vue', 'svelte', 'swift', 'kt', 'scala', 'r', 'm', 'mm']
  if (imageExts.includes(ext)) return 'image'
  if (audioExts.includes(ext)) return 'audio'
  if (videoExts.includes(ext)) return 'video'
  if (textExts.includes(ext)) return 'text'
  if (mime?.startsWith('image/')) return 'image'
  if (mime?.startsWith('audio/')) return 'audio'
  if (mime?.startsWith('video/')) return 'video'
  if (mime && (mime.startsWith('text/') || mime === 'application/json')) return 'text'
  return 'file'
}

export async function addAttachment(file: File) {
  const id = state.currentID
  if (!id) return
  if (!state.pendingAttachments[id]) state.pendingAttachments[id] = []
  const guessedKind = guessKind(file.name, file.type || '')
  const blobURL = URL.createObjectURL(file)
  const placeholder: PendingAttachment = {
    id: '', name: file.name, size: file.size, mime: file.type || '',
    kind: guessedKind,
    _file: file, _blobURL: blobURL, _uploading: true, _error: false,
    _previewURL: blobURL,
  }
  state.pendingAttachments[id].push(placeholder)
  // Cache a base64 data URL up-front so the message can be
  // displayed + sent without re-reading the file from disk.
  // For text attachments this is just the utf-8 text; for binary
  // (images/audio/video) it's the data: URL the LLM wants anyway.
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
  const id = state.currentID
  if (!id) return
  const arr = state.pendingAttachments[id]
  if (!arr) return
  const a = arr[idx]
  if (a?._blobURL) URL.revokeObjectURL(a._blobURL)
  arr.splice(idx, 1)
}

export function clearAttachments() {
  const id = state.currentID
  if (!id) return
  for (const a of (state.pendingAttachments[id] || [])) {
    if (a._blobURL) URL.revokeObjectURL(a._blobURL)
  }
  state.pendingAttachments[id] = []
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
//
// If `ev` is provided and the part is being created
// (not just looked up), the new metadata fields
// (agentType / agentColor / agentModel / taskId) are
// seeded from the event so the card's header can
// render the agent name on the very first frame.
function findOrCreateSubAgent(
  m: Message,
  task: string,
  ev?: api.StreamEvent,
): MessagePart & { kind: 'sub_agent' } {
  if (!m.parts) m.parts = []
  for (let i = m.parts.length - 1; i >= 0; i--) {
    const p = m.parts[i]
    if (p.kind === 'sub_agent' && p.task === task) {
      // Backfill any metadata that arrived on a later
      // event (e.g. the close event may carry the
      // resolved model name).
      if (ev) backfillSubAgentMetadata(p, ev)
      return p
    }
  }
  const sub: MessagePart = {
    kind: 'sub_agent',
    task,
    status: 'start',
    parts: [],
  }
  if (ev) backfillSubAgentMetadata(sub, ev)
  m.parts.push(sub)
  return sub as any
}

// backfillSubAgentMetadata copies the sub-agent
// metadata fields from the SSE event onto the part.
// Idempotent: missing fields on the event leave the
// existing values alone. Called from both the part
// creation path (initial seed) and from
// findOrCreateSubAgent's match path (in case the
// metadata arrived on a later event).
function backfillSubAgentMetadata(
  p: MessagePart & { kind: 'sub_agent' },
  ev: api.StreamEvent,
) {
  if (ev.sub_agent_type && !p.agentType) p.agentType = ev.sub_agent_type
  if (ev.sub_agent_color && !p.agentColor) p.agentColor = ev.sub_agent_color
  if (ev.sub_agent_model && !p.agentModel) p.agentModel = ev.sub_agent_model
  if (ev.sub_agent_task_id && !p.taskId) p.taskId = ev.sub_agent_task_id
  if (ev.sub_agent_description && !p.agentDescription) p.agentDescription = ev.sub_agent_description
  if (ev.sub_agent_failure_reason && !p.failureReason) p.failureReason = ev.sub_agent_failure_reason
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
// PHANTOM_RE mirrors the server-side `phantomVisionErrorRe` regex
// in `internal/agent/agent.go` (which the post-stream redactor
// uses to scrub "Cannot read ... Inform the user" from the
// LLM's text). The server-side redactor should always catch
// these, but LLMs sometimes emit the phantom in positions the
// server can't see (e.g. a `m.content` rebuild on session reload,
// or a chat-store replay during recovery). This client-side
// filter is the last line of defence: it scrubs any text
// chunk that matches the same pattern, in place, before
// the Vue render pass.
//
// Matches: "Cannot read <anything-up-to-400-chars> Inform the
// user." (case-insensitive, dotall).
const PHANTOM_RE = /Cannot read[\s\S]{0,400}?Inform the user\.?/i
const PHANTOM_REPLACEMENT =
  '（当前模型不支持读取图片。请在「设置 → 提供商/模型」中切换到支持视觉的模型后重新发送。）'

/** Returns true if `s` contains a phantom error pattern. */
function containsPhantomError(s: string): boolean {
  if (!s) return false
  return PHANTOM_RE.test(s)
}

/** Replaces every phantom-error match in `s` with the
 *  user-facing Chinese message. Returns the (possibly
 *  unchanged) string. */
function scrubPhantomError(s: string): string {
  if (!s) return s
  if (!containsPhantomError(s)) return s
  return s.replace(PHANTOM_RE, PHANTOM_REPLACEMENT)
}

/** Walks a message and every nested part, scrubbing any
 *  phantom error patterns from text fields. Mutates the
 *  message in place. Used on session load and on the
 *  safety-net `done` handler so a stored phantom from an
 *  older version doesn't reappear. */
function scrubMessagePhantoms(m: Message) {
  const walk = (parts: MessagePart[] | undefined) => {
    if (!parts) return
    for (const p of parts) {
      if (p.kind === 'text' && p.text) {
        const scrubbed = scrubPhantomError(p.text)
        if (scrubbed !== p.text) p.text = scrubbed
      } else if (p.kind === 'thinking' && p.text) {
        const scrubbed = scrubPhantomError(p.text)
        if (scrubbed !== p.text) p.text = scrubbed
      } else if (p.kind === 'sub_agent') {
        walk(p.parts)
      }
    }
  }
  walk(m.parts)
  if (m.content) {
    const scrubbed = scrubPhantomError(m.content)
    if (scrubbed !== m.content) m.content = scrubbed
  }
  if (m.thinking) {
    const scrubbed = scrubPhantomError(m.thinking)
    if (scrubbed !== m.thinking) m.thinking = scrubbed
  }
}

function appendTextPart(m: Message, delta: string, target?: MessagePart[] | null) {
  const parts = (target ?? m.parts)!
  // Two-pass scrub:
  //
  //  1. scrub the incoming delta (catches the case where
  //     a single delta *is* the phantom).
  //  2. AFTER appending, check the last ~600 chars of the
  //     trailing text part. This catches the case where
  //     the phantom is split across multiple deltas
  //     (e.g. "Cannot read" arrives in delta N, and
  //     " Inform the user." arrives in delta N+1 — the
  //     per-delta scrub misses it, but the buffer
  //     catches it once both pieces are assembled).
  //
  // We append the raw delta first (so the user sees
  // text streaming in real time), then scrub the last
  // 600 chars. The scrub is synchronous so Vue's next
  // tick won't render the phantom.
  if (parts.length === 0 || parts[parts.length - 1].kind !== 'text') {
    parts.push({ kind: 'text', text: scrubPhantomError(delta) })
  } else {
    const last = parts[parts.length - 1] as any
    last.text = (last.text || '') + delta
    // Buffer-based scrub for split phantoms.
    const buf = last.text.length > 600 ? last.text.slice(-600) : last.text
    const m = PHANTOM_RE.exec(buf)
    if (m) {
      const matchStartInBuf = m.index
      const matchEndInBuf = m.index + m[0].length
      const totalLen = last.text.length
      const bufStartInText = totalLen - buf.length
      const absStart = bufStartInText + matchStartInBuf
      const absEnd = bufStartInText + matchEndInBuf
      last.text = last.text.slice(0, absStart) + PHANTOM_REPLACEMENT + last.text.slice(absEnd)
    }
  }
  m.content += scrubPhantomError(delta)
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
// ★ appendStreamEvent — SSE 事件的单一分发入口。
//
// 每一个流事件都调用此函数，用于：
//   - content/thinking 增量 → 追加到尾部 text/thinking part
//   - tool 事件 → 创建/更新 ToolCallCard
//   - phase + sub_agent_status → 创建/关闭 SubAgentCard
//   - done → 设置 token 计数、清除 streaming flags、安全网检查
//   - question → 弹出 QuestionModal
//   - error → 追加错误文本、标记 vision_unsupported
//
// 子代理事件通过 ev.sub_agent + ev.sub_agent_task 路由到匹配的嵌套 SubAgentCard。
//
// 修改指南 → docs/modules/frontend.md
export function appendStreamEvent(id: string, ev: api.StreamEvent) {
  const m = findOrCreateLastAssistant(id)
  if (!m.parts) m.parts = []

  // Locate / create the sub-agent part if applicable.
  // The same event is passed in so the part can be
  // seeded with the agent's metadata (type, color,
  // model, task_id) on first creation, or backfilled
  // on subsequent events.
  let sub: (MessagePart & { kind: 'sub_agent' }) | null = null
  if (ev.sub_agent && ev.sub_agent_task) {
    sub = findOrCreateSubAgent(m, ev.sub_agent_task, ev)
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
    case 'content_rewrite': {
      // Post-stream redactor rewrote the assistant's trailing
      // text. Replace the existing trailing text part in place
      // rather than appending a duplicate. If there's no text
      // part yet (unlikely but possible if rewrite arrives
      // before any content deltas), create one. The replacement
      // is a full rewrite — the stream is already over by the
      // time the redactor runs, so no streaming flag is needed.
      if (!ev.content) break
      const cleanedContent = scrubPhantomError(ev.content)
      const parts = sub ? sub.parts : m.parts!
      const last = parts[parts.length - 1]
      if (last && last.kind === 'text') {
        last.text = cleanedContent
      } else {
        parts.push({ kind: 'text', text: cleanedContent })
      }
      break
    }
    case 'thinking_rewrite': {
      // Same as content_rewrite but for the LLM's
      // chain-of-thought. The phantom sometimes appears in
      // the thinking block instead of the text response
      // (the LLM is "thinking out loud" about the phantom
      // pattern) and we want it stripped there too so the
      // collapsible thinking panel doesn't show the
      // fabricated error.
      if (!ev.thinking) break
      const cleanedThinking = scrubPhantomError(ev.thinking)
      const parts = sub ? sub.parts : m.parts!
      const last = parts[parts.length - 1]
      if (last && last.kind === 'thinking') {
        last.text = cleanedThinking
        last.streaming = false
      } else {
        parts.push({ kind: 'thinking', text: cleanedThinking, streaming: false })
      }
      break
    }
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
      // Sync todo list from todo_write tool results. Prefer
      // the untruncated tool_result_full (newlines intact, no
      // 300-char cap) so JSON.parse succeeds for lists with
      // many todos or long content. Fall back to the
      // display-only tool_result preview for older server
      // versions that don't emit tool_result_full.
      if (ev.tool_name === 'todo_write' && ev.tool_status === 'ok') {
        const payload = ev.tool_result_full || ev.tool_result
        if (payload) {
          try {
            const todos: TodoItem[] = JSON.parse(payload)
            if (Array.isArray(todos)) {
              state.sessionTodos[id] = todos
            }
          } catch { /* not JSON, ignore */ }
        }
      }
      break
    }
    case 'phase':
      // Sub-agent lifecycle: open / close the nested card.
      // The sub-agent runner emits synthetic start/ok/err
      // phase events with sub_agent=true and
      // sub_agent_status set. These are routed through
      // the same findOrCreateSubAgent path as the live
      // content stream above, so the card's metadata
      // (type/color/model/task_id) is seeded on the
      // start event and stamped on the close event.
      if (ev.sub_agent_status) {
        if (!sub) break
        sub.status = ev.sub_agent_status as any
        if (ev.sub_agent_status !== 'start' && ev.elapsed) sub.elapsed = ev.elapsed
        if (ev.sub_agent_model && !sub.agentModel) sub.agentModel = ev.sub_agent_model
      }
      // Surface phase messages as a live status bar.
      if (ev.message) {
        if (!m._statusText) (m as any)._statusText = []
        ;(m as any)._statusText.push(ev.message)
      }
      break
    case 'session_status':
      // Lifecycle signal from the agent loop:
      //   "busy"  — turn just started, more chunks coming.
      //             Set state.sessionWorking[id] = true.
      //   "idle"  — turn exited (any reason). Set to false.
      //             If the LLM never wrote `todos: []`, the
      //             TodoPanel state machine's "clear" path
      //             handles stale-cleanup; we don't wipe
      //             here.
      //   "retry" — same as busy (treat as live).
      // The TodoPanel reads `currentSessionWorking` to
      // decide show vs hide vs clear.
      if (ev.session_status === 'busy' || ev.session_status === 'retry') {
        state.sessionWorking[id] = true
      } else if (ev.session_status === 'idle') {
        state.sessionWorking[id] = false
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
      // Safety net: if the parent's turn ended (Done) and any
      // sub-agent part is still in 'start' state, the close
      // event must have been dropped (channel backpressure or
      // out-of-order delivery). Force-close it as 'err' so
      // the card never stays spinning forever. The user can
      // still read whatever text the subagent did produce.
      walkParts(m.parts!, (p) => {
        if (p.kind === 'sub_agent' && p.status === 'start') {
          p.status = 'err'
        }
      })
      // Final phantom scrub. By the time the parent's
      // stream ends, all content + thinking + sub-agent
      // parts have been appended. Walk the message and
      // scrub any phantom the server-side redactor may
      // have missed (e.g. a phantom inside a tool result
      // string the parent LLM echoed back, or a phantom
      // the LLM produced AFTER the redactor's
      // content_rewrite event was processed).
      scrubMessagePhantoms(m)
      // Stamp the server-assigned row id on the user message
      // that started this turn and on the assistant reply so
      // fork/rollback can target either.
      if (ev.user_message_id || ev.last_message_id) {
        const msgs = state.sessionMessages[id]
        if (msgs) {
          if (ev.user_message_id) {
            for (let i = msgs.length - 1; i >= 0; i--) {
              if (msgs[i].role === 'user') { msgs[i].id = ev.user_message_id; break }
            }
          }
          if (ev.last_message_id) {
            for (let i = msgs.length - 1; i >= 0; i--) {
              if (msgs[i].role === 'assistant') { msgs[i].id = ev.last_message_id; break }
            }
          }
        }
      }
      // 提示音 + 系统通知：对话完成
      notifyManager.play('done')
      if (!sub) notifyManager.notify('P-Chat', '对话已完成')
      break
    case 'question':
      // LLM is asking the user a question. Parse the
      // question JSON and surface it via QuestionModal.
      // Keyed by session id so a background session's
      // question doesn't appear (or get answered) on the
      // session the user happens to be viewing.
      console.log('[chat] received question event, question_json length:', ev.question_json?.length ?? 0)
      if (ev.question_json) {
        try {
          const questions: QuestionItem[] = JSON.parse(ev.question_json)
          console.log('[chat] parsed %d questions', questions.length)
          // Create a Promise that resolves when the user answers.
          const answerPromise = new Promise<Record<string, string>>((resolve) => {
            state.pendingQuestion[id] = { questions, resolve }
          })
          // 提示音 + 系统通知：LLM 提问
          notifyManager.play('question')
          notifyManager.notify('P-Chat', '向您提问')
          // The answer will be submitted via submitQuestionAnswer().
        } catch {
          console.error('[question] failed to parse question_json:', ev.question_json?.slice(0, 200))
        }
      } else {
        console.warn('[chat] question event with no question_json')
      }
      break
    case 'tool_confirm':
      if (ev.tool_confirm_json) {
        try {
          const cfm = JSON.parse(ev.tool_confirm_json) as { tool_name: string; args: string; reason: string }
          new Promise<boolean>((resolve) => {
            state.pendingConfirm[id] = {
              toolName: cfm.tool_name,
              args: cfm.args,
              reason: cfm.reason,
              resolve,
            }
          }).then((approved) => {
            submitConfirmResponseInner(id, approved)
          })
          // 提示音 + 系统通知：请求确认
          notifyManager.play('confirm')
          notifyManager.notify('沙箱请求', `批准 ${cfm.tool_name}?`)
        } catch { /* ignore */ }
      }
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
      // 提示音：对话出错
      notifyManager.play('error')
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
  const cid = state.currentID
  if (!cid) return
  if (!state.sessionMessages[cid]) state.sessionMessages[cid] = []
  // Scrub phantom errors from system messages too (the
  // /compress / /status slash commands could echo one if
  // a prior turn failed).
  state.sessionMessages[cid].push({ role: 'system', content: scrubPhantomError(text) })
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

export function submitQuestionAnswer(answers: Record<string, string>) {
  // Always answer the question that belongs to the *session
  // that asked it* — not whatever session the user happens to
  // be viewing. The sessionID is captured in the question
  // event's key; we just look it up here.
  for (const [sid, pq] of Object.entries(state.pendingQuestion)) {
    if (pq) {
      const id = sid
      delete state.pendingQuestion[id]
      // Resolve the Promise so the LLM continues.
      pq.resolve(answers)
      // POST the answer to the server so the blocked tool handler
      // receives it and the agent loop continues.
      api.submitQuestionResponse(id, {
        questions: pq.questions,
        answers,
      })
      return
    }
  }
}

async function submitConfirmResponseInner(id: string, approved: boolean) {
  try {
    await api.submitConfirmResponse(id, approved)
  } catch {
    // server already unblocked via resolve
  }
}

export function submitToolConfirm(approved: boolean) {
  // Same multi-session reasoning as submitQuestionAnswer:
  // the confirm belongs to whichever session requested it.
  for (const [sid, pc] of Object.entries(state.pendingConfirm)) {
    if (pc) {
      const id = sid
      delete state.pendingConfirm[id]
      pc.resolve(approved)
      submitConfirmResponseInner(id, approved)
      return
    }
  }
}

// isStreaming answers "is the *current* session streaming?"
// — the InputArea uses this to decide between the Send and
// Stop buttons. Background sessions may also be streaming
// (see isAnyStreaming) without affecting the current view.
export const isStreaming = computed(() => !!state.streaming[state.currentID])

// isAnyStreaming is true if *any* session is generating.
// SessionSidebar uses this to surface the per-session dot
// indicator regardless of which session the user is on.
export const isAnyStreaming = computed(() => Object.keys(state.streaming).length > 0)

// isSessionStreaming lets callers check a specific session
// without coupling to state.currentID. The stop() handler
// uses it to operate on the session the user is actually
// viewing even if its streaming entry is keyed differently
// than expected.
export function isSessionStreaming(id: string): boolean {
  return !!state.streaming[id]
}

// currentPendingQuestion / currentPendingConfirm resolve the
// pending question / tool confirm for the session the user is
// currently viewing. Background sessions can have their own
// pendingQuestion / pendingConfirm — those won't show up here
// and won't be visible to the user until they switch to that
// session. Submitting an answer (in submitQuestionAnswer /
// submitToolConfirm) is keyed off the session that asked, not
// state.currentID.
export const currentPendingQuestion = computed(() =>
  state.pendingQuestion[state.currentID] || null,
)
export const currentPendingConfirm = computed(() =>
  state.pendingConfirm[state.currentID] || null,
)

// currentAttachments returns the staged attachments for the
// session the user is currently viewing. Per-session storage
// lets users start editing a message in one conversation,
// switch to another, and have their staged files preserved
// when they switch back.
export const currentAttachments = computed(() =>
  state.pendingAttachments[state.currentID] || [],
)

// --- Rollback ---

export const currentRollbackBanner = computed(() => {
  const undo = state.rollbackUndo[state.currentID]
  if (!undo || !undo.messages.length) return null
  return { count: undo.messages.length }
})

export const currentPendingInput = computed(() =>
  state.pendingInput[state.currentID] || '',
)

// rollbackTo deletes the message at the given index (and all later
// messages) from the session. It calls the server API, saves the
// deleted messages for undo, and auto-fills the input with the
// last rolled-back user message.
export async function rollbackTo(sessionId: string, messageIndex: number) {
  const msgs = state.sessionMessages[sessionId]
  if (!msgs) return
  const msg = msgs[messageIndex]
  if (!msg?.id) return

  // If currently streaming, abort first.
  if (state.streaming[sessionId]) {
    stopStream(sessionId)
  }

  const result = await api.rollbackMessages(sessionId, msg.id)

  state.rollbackUndo[sessionId] = {
    messages: result.deleted_messages,
    fromIndex: messageIndex,
  }

  msgs.splice(messageIndex)

  const lastUser = [...result.deleted_messages].reverse().find(m => m.role === 'user')
  state.pendingInput[sessionId] = lastUser?.content || ''
}

// undoRollback restores the messages deleted by the most recent
// rollback in the given session.
export async function undoRollback(sessionId: string) {
  const undo = state.rollbackUndo[sessionId]
  if (!undo || !undo.messages.length) return

  await api.undoRollback(sessionId, undo.messages)

  const msgs = state.sessionMessages[sessionId]
  if (msgs) {
    msgs.splice(undo.fromIndex, 0, ...undo.messages)
  }
  state.rollbackUndo[sessionId] = null
  state.pendingInput[sessionId] = ''
}

// dismissRollback clears the rollback undo buffer and pending
// input without restoring the deleted messages.
export function dismissRollback(sessionId: string) {
  state.rollbackUndo[sessionId] = null
  state.pendingInput[sessionId] = ''
}

// --- Fork ---

export async function forkSession(sourceId: string, messageIndex: number) {
  const msgs = state.sessionMessages[sourceId]
  if (!msgs) return
  const msg = msgs[messageIndex]
  if (!msg?.id) return

  const session = await api.forkSession(sourceId, msg.id)

  state.sessions.unshift(session)
  await switchSession(session.id)
}
