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

// Expose state on window for in-browser debugging
// (pchat-gui hides this via a build-time flag — we don't
// strip it because the chat store is already globally
// accessible through the Vue devtools anyway).
if (typeof window !== 'undefined') {
  ;(window as any).__pchatDebug = {
    get state() { return state },
  }
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
  // (loaded by switchSession) sets oldestSeq/oldestId to
  // the corresponding cursor from the response; subsequent
  // pages are fetched with before_seq=oldestSeq (preferred,
  // stable across rollback/undo) or before_id=oldestId
  // (legacy, only for older server versions that don't
  // emit oldest_seq). When hasMore is false, the user
  // has scrolled to the start of the conversation.
  sessionPaging: {} as Record<string, {
    oldestSeq: number
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
  sessionMeta: {} as Record<string, { style: string; provider: string; model: string; title: string; plan_mode?: boolean; permission_level?: string; reasoning_effort?: string; vector_store?: string; knowledge_base?: string }>,
  kbConfigVersion: 0, // bumped by settings modal after config changes, watched by InputArea
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
  pendingQuestion: {} as Record<string, { questions: QuestionItem[] }>,
  pendingConfirm: {} as Record<string, Array<{
  toolName: string
  args: string
  reason: string
  // 2026-07: extended ConfirmRequest fields. All optional
  // for backward-compat with old server versions that
  // don't emit them.
  resolvedPath?: string
  pathClass?: string
  riskLevel?: string
  resolve: (approved: boolean) => void
}>>,
  pendingPlanText: {} as Record<string, string>,
  lightbox: { show: false, src: '', alt: '', kind: 'image' as 'image' | 'video' },
  showSettings: false,
  projects: [] as ProjectItem[],
  activeProjectPath: '' as string,
  // Rollback undo buffer — only the most recent rollback per session.
  rollbackUndo: {} as Record<string, { messages: Message[]; fromIndex: number } | null>,
  // Pending text to fill into the input area after a rollback.
  pendingInput: {} as Record<string, string>,
  // P0-1: transient banner shown for ~3s when the
  // recoverMissingParts flow successfully merged
  // server-side parts into the trailing assistant
  // bubble. ChatWindow watches this and renders the
  // <RecoveryBanner> pill. Null when no banner is
  // active.
  recoveryBanner: null as null | {
    sessionId: string
    recovered: number
    reason: string
    shownAt: number
  },
})

export const currentMessages = computed(() =>
  state.sessionMessages[state.currentID] || [],
)

export const currentTodos = computed(() =>
  state.sessionTodos[state.currentID] || [],
)

// currentRecoveryBanner is the P0-1 banner payload for
// the active session. Null when no banner is active or
// when the banner belongs to a different session.
export const currentRecoveryBanner = computed(() => {
  const b = state.recoveryBanner
  if (!b) return null
  if (b.sessionId !== state.currentID) return null
  return b
})

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

export function bumpKBConfigVersion() {
  state.kbConfigVersion++
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
    vector_store: '',
    knowledge_base: '',
  }
})

// Global phantom scrub. Walks newly-added messages on
// session change to catch stored phantoms from pre-fix
// versions or phantom injections through unaudited paths.
// The per-part scrub in `appendTextPart` handles the
// streaming path; the `done` handler does a final pass.
// We track per-session message counts and only scrub
// unseen messages — avoiding the O(all_messages) deep
// walk on every SSE tick that `deep: true` incurred.
//
// We scrub in place — the `m.parts` arrays are
// reactive so the Vue components re-render
// automatically. We only assign when the scrub
// actually changes a value, to avoid spurious
// reactive triggers.
watch(
  () => {
    const counts: Record<string, number> = {}
    const sessions = state.sessionMessages
    if (!sessions) return counts
    for (const id in sessions) {
      counts[id] = sessions[id]?.length ?? 0
    }
    return counts
  },
  (counts, oldCounts) => {
    if (!counts || !oldCounts) return
    for (const id in counts) {
      const newLen = counts[id]
      const oldLen = oldCounts[id] ?? 0
      if (newLen <= oldLen) continue
      const msgs = state.sessionMessages[id]
      if (!msgs) continue
      for (let i = oldLen; i < newLen; i++) {
        scrubMessagePhantoms(msgs[i])
      }
    }
  },
  { immediate: true },
)

// --- Session management ---

// Max loaded sessions kept in the in-memory cache. When a
// new session is loaded, the one that was touched longest
// ago (and is not the current session) is evicted. This
// caps the number of messages + screenshot blob URLs that
// can accumulate across session switches within the same
// project. 4 keeps the "most recent + 3 others" pattern
// typical of a user switching between a handful of active
// conversations — enough to avoid the cost of reloading
// history on every switch, small enough to bound memory.
const MAX_LOADED_SESSIONS = 4
const _loadedSessions: Record<string, number> = {}
let _loadedSeq = 0

function markSessionHot(id: string) {
  if (!id) return
  _loadedSessions[id] = ++_loadedSeq
}

function evictColdSessions() {
  const keys = Object.keys(_loadedSessions)
  if (keys.length <= MAX_LOADED_SESSIONS) return
  keys.sort((a, b) => _loadedSessions[a] - _loadedSessions[b])
  const toEvict = keys.length - MAX_LOADED_SESSIONS
  for (let i = 0; i < toEvict; i++) {
    const sid = keys[i]
    if (sid === state.currentID) continue
    revokeSessionBlobUrls(sid)
    delete state.sessionMessages[sid]
    delete _loadedSessions[sid]
  }
}

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
  // Revoke screenshot blob URLs for ALL sessions currently
  // cached in the store before wiping it. Otherwise those
  // blob URLs accumulate in WebView2 until page reload.
  for (const sid of Object.keys(state.sessionMessages)) {
    revokeSessionBlobUrls(sid)
  }
  state.sessionMessages = {}
  // Reset the LRU heat map along with the messages map —
  // stale entries would trigger a no-op eviction of sessions
  // that no longer exist on the next switch.
  for (const k of Object.keys(_loadedSessions)) {
    delete _loadedSessions[k]
  }
  state.sessionMeta = {}
  state.sessionTodos = {}
  state.sessionWorking = {}
  state.sessionPaging = {}
  // Revoke blob URLs for any staged attachments in the
  // sessions we're discarding, then drop the maps entirely.
  // Question / confirm dialogs keyed to those sessions get
  // cleared so any awaiters unblock. (We no longer carry
  // a Promise resolve on pendingQuestion — answers are sent
  // to the server synchronously through api.submitQuestionResponse,
  // so there's nothing to resolve here. We just drop the
  // entries so a subsequent switchSession doesn't see stale
  // modal data for a session that no longer exists.)
  for (const arr of Object.values(state.pendingAttachments)) {
    for (const a of arr) {
      if (a._blobURL) URL.revokeObjectURL(a._blobURL)
    }
  }
  state.pendingAttachments = {}
  for (const id of Object.keys(state.pendingQuestion)) {
    delete state.pendingQuestion[id]
  }
  for (const [id, items] of Object.entries(state.pendingConfirm)) {
    for (const pc of items) pc.resolve(false)
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

// dedupMessagesByKey folds a freshly-loaded page into the
// existing in-memory list, dropping any rows we already
// have. The dedup key is `seq` when available (the new
// stable per-conversation identity) and `id` as a
// fallback for older messages / pre-seq DBs.
//
// Newer entries (from `incoming`) win on key collision
// so a re-fetch picks up server-side state changes
// (e.g. the redactor rewrote `content` on the same row).
// The merged result is sorted ascending by seq (or id
// when seq is 0) so the oldest-first invariant holds for
// the renderer.
//
// Idempotency: a stale local state from before the server
// cursor fix, or a future server bug that returns the same
// page twice in a row, is collapsed silently. The user
// never sees the duplicate.
function dedupMessagesByKey(existing: Message[], incoming: Message[]): Message[] {
  if (incoming.length === 0) return existing
  const byKey = new Map<number, Message>()
  const keyOf = (m: Message): number => (m.seq != null && m.seq > 0) ? m.seq : -(m.id ?? 0)
  for (const m of existing) {
    byKey.set(keyOf(m), m)
  }
  for (const m of incoming) {
    byKey.set(keyOf(m), m)
  }
  return Array.from(byKey.values()).sort((a, b) => keyOf(a) - keyOf(b))
}

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
    // Defensive dedup: if a future server bug ever returns
    // an overlapping page on the very first load, the user
    // won't see the duplicate bubble. The server's cursor
    // fix (handler.go: oldestID=rowIDs[0]) eliminates the
    // most common trigger but dedup here is the
    // belt-and-braces guarantee.
    state.sessionMessages[id] = dedupMessagesByKey([], r.messages)
    state.sessionPaging[id] = {
      // Prefer the seq-based cursor (stable across
      // rollback/undo). Fall back to the legacy id-based
      // cursor when the server is older and didn't return
      // oldest_seq.
      oldestSeq: r.oldest_seq ?? 0,
      oldestId: r.oldest_id,
      hasMore: r.has_more,
      loading: false,
    }
    // Convert any base64 screenshot data in the newly
    // loaded history into blob URLs, then strip old ones
    // so the session opens with a bounded memory footprint.
    convertAndStripScreenshots(id)
  }
  // Mark this session as most-recently-used so evictCold
  // keeps it in memory. Eviction runs here because a
  // switch is the highest-frequency trigger for "new
  // loaded session + one to evict".
  markSessionHot(id)
  evictColdSessions()
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
      vector_store: s.vector_store || '',
      knowledge_base: s.knowledge_base || '',
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
    // Prefer the seq-based cursor when the server
    // returned it. Fall back to the id-based cursor for
    // older servers that only emit oldest_id. The seq
    // cursor is stable across rollback/undo — a
    // scroll-in-progress survives an undo, which the
    // id cursor could not guarantee (the restored
    // messages have new ids).
    const opts: api.PageOpts = { limit: initialHistoryLimit }
    if (paging.oldestSeq > 0) {
      opts.beforeSeq = paging.oldestSeq
    } else if (paging.oldestId > 0) {
      opts.beforeId = paging.oldestId
    }
    const r = await api.listMessages(id, opts)
    if (r.messages.length > 0) {
      // Prepend the new (older) page to the front of the
      // existing message list. The server returns messages
      // oldest-first within the page. We dedup by seq
      // (or id fallback) and re-sort so an overlapping
      // page — e.g. the pre-2026-07-10 server cursor bug
      // where oldest_id was the page's max id and the
      // next page overlapped by all-but-one row — doesn't
      // add visible duplicates. The server-side fix
      // (handler.go: rowIDs[0]) makes the overlap empty;
      // this client dedup is the safety net for any
      // future regression.
      const existing = state.sessionMessages[id] || []
      state.sessionMessages[id] = dedupMessagesByKey(existing, r.messages)
      // Older pages may contain base64 screenshots persisted
      // by pre-blob-URL versions. Convert them + enforce
      // the global cap so scroll-up doesn't grow memory.
      convertAndStripScreenshots(id)
    }
    paging.oldestSeq = r.oldest_seq ?? paging.oldestSeq
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
  // Revoke all screenshot blob URLs owned by this session
  // before clearing the messages, otherwise those URLs
  // remain referenced until the next page reload.
  revokeSessionBlobUrls(id)
  delete state.sessionMessages[id]
  delete _loadedSessions[id]
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
  delete state.pendingQuestion[id]
  const cfms = state.pendingConfirm[id]
  if (cfms && cfms.length > 0) {
    for (const pc of cfms) pc.resolve(false)
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
      vector_store: resp.vector_store ?? state.sessionMeta[id].vector_store,
      knowledge_base: resp.knowledge_base ?? state.sessionMeta[id].knowledge_base,
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

// --- Blob URL helpers ---
//
// Browser screenshot tool results arrive as large base64
// data: URLs (~200–500 KB each). Storing them directly in
// the reactive Vue store keeps a giant decoded bitmap in
// WebView2's DOM / decoded-image cache and eventually
// crashes the renderer. We convert every screenshot to a
// blob: URL up front so:
//   1. The base64 string lives only inside the Blob, not in
//      the reactive map.
//   2. The <img> in ToolCallCard uses a blob URL that
//      Chromium can GC independently of the JS heap.
//   3. Session-level revocation is a simple walkParts over
//      the messages map rather than hunting for data URLs.
function dataUrlToBlobUrl(input: string | undefined): string | undefined {
  if (!input || !input.startsWith('data:image/')) return input
  try {
    const commaIdx = input.indexOf(',')
    const b64 = input.slice(commaIdx + 1)
    const mime = input.slice(5, commaIdx)
    const byteChars = atob(b64)
    const bytes = new Uint8Array(byteChars.length)
    for (let i = 0; i < byteChars.length; i++) bytes[i] = byteChars.charCodeAt(i)
    return URL.createObjectURL(new Blob([bytes], { type: mime }))
  } catch {
    return input
  }
}

// convertAndStripScreenshots walks ALL messages in the given
// session, (1) converts any residual base64 screenshot data
// URLs into blob: URLs (this happens on the very first load
// of a history that was persisted with raw base64), and
// (2) strips all but the last `keep` screenshot results —
// globally across the session, not per message — to cap the
// number of live blob URLs / decoded bitmaps.
//
// This is the SINGLE point of entry for screenshot memory
// management. Called from:
//   - switchSession (after history load)
//   - loadMoreMessages (after page load)
//   - the 'done' SSE event (after stream end)
const MAX_PRESERVED_SCREENSHOTS = 3
const PLACEHOLDER_SCREENSHOT = '[截图已省略]'

function isScreenshotResult(r: string | undefined): boolean {
  if (!r) return false
  if (r.startsWith('data:image/') || r.startsWith('blob:')) return true
  try {
    const obj = JSON.parse(r)
    if (typeof obj.image === 'string' &&
        ((obj.image as string).startsWith('data:image/') || (obj.image as string).startsWith('blob:'))) {
      return true
    }
  } catch { /* not JSON */ }
  return false
}

export function convertAndStripScreenshots(sessionId: string, keep = MAX_PRESERVED_SCREENSHOTS) {
  const msgs = state.sessionMessages[sessionId]
  if (!msgs) return
  const screenshotTargets: (ToolPart | MessageAttachment)[] = []
  const isB64 = (u: string | undefined): u is string => !!u && u.startsWith('data:image/')
  const isBlob = (u: string | undefined): u is string => !!u && u.startsWith('blob:')
  for (const m of msgs) {
    if (m.parts) {
      walkParts(m.parts, (p) => {
        if (p.kind !== 'tool' || !p.result) return
        const r = p.result as string
        if (isB64(r)) {
          p.result = dataUrlToBlobUrl(r)
        } else {
          try {
            const obj = JSON.parse(r)
            if (typeof obj.image === 'string' && isB64(obj.image as string)) {
              obj.image = dataUrlToBlobUrl(obj.image as string)
              p.result = JSON.stringify(obj)
            } else if (typeof obj.image === 'string' && obj.image === PLACEHOLDER_SCREENSHOT) {
              return
            }
          } catch { /* not JSON */ }
        }
        if (isScreenshotResult(p.result)) screenshotTargets.push(p)
      })
    }
    // Handle image attachments (user-uploaded images and
    // history-reloaded messages). These come back from
    // SQLite as 'data:image/jpeg;base64,...' URLs and sit
    // in the Vue reactive store as ~250 KB strings until
    // converted. Convert to blob: URLs so they're
    // independently managed by the browser and reclaimable.
    if (m.attachments) {
      for (const att of m.attachments) {
        if (!isB64(att.url) && !isBlob(att.url)) continue
        if (isB64(att.url)) {
          att.url = dataUrlToBlobUrl(att.url)
        }
        if (isBlob(att.url)) screenshotTargets.push(att)
      }
    }
  }
  const stripCount = Math.max(0, screenshotTargets.length - keep)
  for (let i = 0; i < stripCount; i++) {
    const t = screenshotTargets[i]
    if ('kind' in t) {
      // ToolPart — replace the result payload with the
      // placeholder so the card still renders.
      (t as ToolPart).result = PLACEHOLDER_SCREENSHOT
    } else {
      // MessageAttachment — revoke the blob and replace the
      // URL with the placeholder so the image no longer
      // occupies browser memory.
      const att = t as MessageAttachment
      if (isBlob(att.url)) URL.revokeObjectURL(att.url!)
      att.url = PLACEHOLDER_SCREENSHOT
    }
  }
}

// revokeSessionBlobUrls frees every blob: URL owned by
// the given session. Must be called before the session's
// messages leave the in-memory store — i.e. on session
// eviction (LRU), deletion, or project switch — otherwise
// the browser accumulates blob URLs until the page is
// reloaded.
function revokeSessionBlobUrls(sessionId: string) {
  const msgs = state.sessionMessages[sessionId]
  if (msgs) {
    for (const m of msgs) {
      if (!m.parts) continue
      walkParts(m.parts, (p) => {
        if (p.kind !== 'tool') return
        const r = p.result
        if (typeof r === 'string' && r.startsWith('blob:')) {
          URL.revokeObjectURL(r)
          return
        }
        try {
          const obj = JSON.parse(r as string)
          if (typeof obj.image === 'string' && obj.image.startsWith('blob:')) {
            URL.revokeObjectURL(obj.image as string)
          }
        } catch { /* not JSON */ }
      })
      if (m.attachments) {
        for (const att of m.attachments) {
          if (att.url?.startsWith('blob:')) URL.revokeObjectURL(att.url)
        }
      }
    }
  }
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

// findTrailingOpenQuestion walks the trailing assistant
// message's parts from the end and returns the first
// question part with `question_status === 'open'` (or
// unset, which is treated as "open" for legacy data).
// Returns null if there isn't one.
//
// Used by the question SSE handler: when the tool result
// comes back with `answers` filled in, we update the
// existing question part in place rather than appending a
// duplicate.
function findTrailingOpenQuestion(id: string): any | null {
  const msgs = state.sessionMessages[id]
  if (!msgs || msgs.length === 0) return null
  const m = msgs[msgs.length - 1]
  if (!m || m.role !== 'assistant' || !m.parts) return null
  return findOpenQuestionInParts(m.parts)
}

// findOpenQuestionInParts is the part-array-only
// counterpart of findTrailingOpenQuestion. Used by the
// question handler for sub-agent flows where the trailing
// part list is `sub.parts`, not the parent message's parts.
function findOpenQuestionInParts(parts: MessagePart[] | undefined): any | null {
  if (!parts) return null
  for (let i = parts.length - 1; i >= 0; i--) {
    const p: any = parts[i]
    if (p.kind !== 'question') continue
    if (!p.question_status || p.question_status === 'open') return p
  }
  return null
}

// updateQuestionStatusInParts walks `parts` from the end
// and stamps every still-open question part with the
// provided answer map + status. Used by the question
// tool's result event (which carries the full
// `{questions, answers}` payload) so the QuestionTable
// in the chat can render the user's picks highlighted.
//
// The mapping uses the `header` field as the key — that
// is what the question tool's answer side (`answers` map)
// keys against. Question parts that have no matching
// answer in the map (e.g. the LLM added a question mid-
// stream that the user never saw) are left alone so we
// don't accidentally mark a phantom answer.
function updateQuestionStatusInParts(
  parts: MessagePart[],
  answers: Record<string, string>,
  status: 'ok' | 'error',
): number {
  let updated = 0
  for (let i = parts.length - 1; i >= 0; i--) {
    const p: any = parts[i]
    if (p.kind !== 'question') continue
    if (p.question_status && p.question_status !== 'open') continue
    // Match by header (the question tool keys answers by
    // header, not by full question text). If a part has no
    // match, leave it alone — it might be a stale or
    // out-of-band question.
    let matched = false
    if (p.text) {
      try {
        const payload = JSON.parse(p.text)
        const qs = payload?.questions || []
        for (const q of qs) {
          if (q && q.header && Object.prototype.hasOwnProperty.call(answers, q.header)) {
            matched = true
            break
          }
        }
      } catch { /* malformed text, skip */ }
    }
    if (!matched) continue
    p.name = JSON.stringify(answers)
    p.question_status = status
    updated++
  }
  return updated
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
// user." (case-insensitive, dotall, GLOBAL). The `g` flag is
// critical: the LLM can emit the phantom multiple times in a
// single text part (e.g. after a stuck retry loop) and the user
// used to see the first one replaced while 2+ raw phantoms
// leaked through. With `g` every match in the string is
// replaced in a single pass.
const PHANTOM_RE = /Cannot read[\s\S]{0,400}?Inform the user\.?/gi
const PHANTOM_REPLACEMENT =
  '（当前模型不支持读取图片。请在「设置 → 提供商/模型」中切换到支持视觉的模型后重新发送。）'

// Secondary pattern: the LLM sometimes paraphrases the phantom
// as "This model does not support <X>" without ever saying
// "Cannot read" — usually when reporting an unsupported
// capability to the user. Caught by the same redactor so the
// user doesn't see a half-formed version of the original
// message.
const PHANTOM_RE_ALT = /This model does not support[\s\S]{0,200}?\./gi

/** Returns true if `s` contains a phantom error pattern. */
function containsPhantomError(s: string): boolean {
  if (!s) return false
  // Reset lastIndex on the global regex before testing —
  // PHANTOM_RE has the `g` flag, and `RegExp.test()` is
  // stateful: after a successful match, lastIndex is
  // updated and the next test() call resumes from there.
  // Without this reset, repeated calls with different
  // strings would alternate between true/false depending
  // on whether the last test advanced past the input's
  // length. Use exec + reset for a stateless check.
  const m = PHANTOM_RE.exec(s)
  PHANTOM_RE.lastIndex = 0
  return m !== null
}

/** Replaces every phantom-error match in `s` with the
 *  user-facing Chinese message. Returns the (possibly
 *  unchanged) string. Both patterns are applied globally
 *  (the `g` flag on each regex) so multiple occurrences in
 *  the same chunk are all replaced — the previous
 *  non-global version only caught the first one, which is
 *  how a stuck LLM retry loop would leak 2-3 raw phantoms
 *  through per turn. */
function scrubPhantomError(s: string): string {
  if (!s) return s
  if (!containsPhantomError(s)) return s
  return s.replace(PHANTOM_RE, PHANTOM_REPLACEMENT).replace(PHANTOM_RE_ALT, PHANTOM_REPLACEMENT)
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
  //  2. AFTER appending, check the last ~1500 chars of the
  //     trailing text part. This catches the case where
  //     the phantom is split across multiple deltas
  //     (e.g. "Cannot read" arrives in delta N, and
  //     " Inform the user." arrives in delta N+1 — the
  //     per-delta scrub misses it, but the buffer
  //     catches it once both pieces are assembled).
  //
  // The buffer was raised from 600 → 1500 chars to catch
  // multi-phantom bursts: a stuck LLM retry loop can emit
  // "ERROR: ... Inform the user." three times in a row,
  // each separated by ~400 chars of boilerplate ("Let me
  // try again. "). With a 600-char buffer only the last
  // phantom would be caught, leaking the first two.
  //
  // We append the raw delta first (so the user sees
  // text streaming in real time), then scrub the buffer
  // globally. The scrub is synchronous so Vue's next
  // tick won't render the phantom.
  if (parts.length === 0 || parts[parts.length - 1].kind !== 'text') {
    parts.push({ kind: 'text', text: scrubPhantomError(delta) })
  } else {
    const last = parts[parts.length - 1] as any
    last.text = (last.text || '') + delta
    // Buffer-based scrub for split phantoms. Use the
    // scrub helper (which is already global) on the buffer
    // so all matches in the visible window are caught in
    // one pass, instead of stopping at the first match.
    const buf = last.text.length > 1500 ? last.text.slice(-1500) : last.text
    if (containsPhantomError(buf)) {
      const scrubbedBuf = scrubPhantomError(buf)
      if (scrubbedBuf !== buf) {
        last.text = last.text.slice(0, last.text.length - buf.length) + scrubbedBuf
      }
    }
  }
  // `m.content` is intentionally NOT updated from the
  // delta. The MessageBubble component renders assistant
  // messages off `message.parts` exclusively (see
  // MessageBubble.vue:601-623), and reload brings the
  // parts back from the server's `meta["parts"]` blob —
  // so the live-streaming `m.content` is never read by
  // the UI. Maintaining it here would be a no-op that
  // costs a `scrubPhantomError` regex per chunk. The
  // legacy code path that DID read `m.content` (the
  // v-else-if "message.content" markdown fallback) only
  // fires when `message.parts` is empty, which never
  // happens for messages that have ever streamed
  // through appendTextPart (parts is always seeded
  // before the first content event).
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
      //
      // Multi-round note: when the agent loop runs several
      // rounds in a single response, each round's text lives
      // in its own text part. The backend's per-round
      // `redactPhantomErrors` only cleans the current round,
      // so earlier rounds' phantoms (if any slipped through
      // the per-delta scrub) would still be visible. We walk
      // every text/thinking part in the message and run
      // scrubPhantomError on each — cheap (regex only runs
      // when containsPhantomError returns true) and closes
      // the multi-phantom leak that was producing 3 raw
      // errors in a single user-facing bubble.
      if (!ev.content) break
      const cleanedContent = scrubPhantomError(ev.content)
      const parts = sub ? sub.parts : m.parts!
      // Apply the explicit rewrite to the trailing text part.
      const last = parts[parts.length - 1]
      if (last && last.kind === 'text') {
        last.text = cleanedContent
      } else {
        parts.push({ kind: 'text', text: cleanedContent })
      }
      // Defensive: re-scrub every text/thinking part in the
      // message in case earlier rounds slipped a phantom
      // past the per-delta scrub.
      for (const p of parts) {
        if (p.kind === 'text' && p.text) {
          const c = scrubPhantomError(p.text)
          if (c !== p.text) p.text = c
        } else if (p.kind === 'thinking' && p.text) {
          const c = scrubPhantomError(p.text)
          if (c !== p.text) p.text = c
        }
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
      const parts = sub ? sub.parts : m.parts!
      if (!ev.tool_name) break
      if (ev.tool_status === 'start') {
        // When tool_id is present, use it as the unique key
        // and never reuse the last part (two calls to the
        // same tool name are distinct). For legacy streams
        // without tool_id, fall back to name-based reuse.
        if (ev.tool_id) {
          if (parts.length > 0) {
            const last = parts[parts.length - 1]
            if (last.kind === 'tool' && last.status === 'start' && last.tool_id === ev.tool_id) {
              last.args = ev.tool_args
            } else {
              parts.push({
                kind: 'tool',
                id: ev.tool_name,
                tool_id: ev.tool_id,
                name: ev.tool_name,
                args: ev.tool_args,
                status: 'start',
              })
            }
          } else {
            parts.push({
              kind: 'tool',
              id: ev.tool_name,
              tool_id: ev.tool_id,
              name: ev.tool_name,
              args: ev.tool_args,
              status: 'start',
            })
          }
        } else {
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
        }
      } else {
        // 'ok' / 'warn' / 'error' — exact match by tool_id,
        // fall back to name for legacy streams.
        let found = false
        for (let i = parts.length - 1; i >= 0; i--) {
          const p = parts[i]
          if (p.kind !== 'tool' || p.status !== 'start') continue
          if ((ev.tool_id && p.tool_id === ev.tool_id) ||
              (!ev.tool_id && p.name === ev.tool_name)) {
            p.status = (ev.tool_status as any) || 'ok'
            // Convert screenshot base64 data URLs to blob URLs
            // before storing, so the reactive map never sees the
            // raw base64 payload (~200–500 KB per screenshot).
            p.result = dataUrlToBlobUrl(ev.tool_result_full || ev.tool_result)
            p.error = ev.tool_error
            p.elapsed = ev.tool_elapsed
            if (ev.tool_args) p.args = ev.tool_args
            found = true
            break
          }
        }
        if (!found) {
          parts.push({
            kind: 'tool',
            id: ev.tool_name,
            tool_id: ev.tool_id,
            name: ev.tool_name,
            args: ev.tool_args,
            status: (ev.tool_status as any) || 'ok',
            result: dataUrlToBlobUrl(ev.tool_result_full || ev.tool_result),
            error: ev.tool_error,
            elapsed: ev.tool_elapsed,
          })
        }
        // Question tool answer carry-through: the question
        // tool returns `{questions, answers}` JSON via the
        // tool result, NOT via the question event (the
        // question event is only fired for the prompt).
        // Mirror the answers onto the trailing open question
        // part so the QuestionTable in the chat shows the
        // user's picks highlighted. Without this the question
        // card would sit there as "等待回答" forever (the live
        // message has no way to know the user answered
        // until the server emits a content_rewrite or the
        // page is reloaded).
        //
        // The match is "trailing open question part" because
        // the question tool always pairs with a question
        // event that pushed an open part into the same parts
        // list (main or sub-agent). Multi-question turns are
        // handled by updateQuestionStatusInParts walking
        // back through all open question parts and assigning
        // the answer map by header — the question tool's
        // result is the FULL answer set across all questions
        // in the call.
        //
        // CRITICAL: the question tool's answers sit at the
        // END of the result JSON (after questions + options),
        // and the server truncates `tool_result` to 300
        // chars (see agent.go:1860-1864). A 4-option question
        // already blows past 300 chars on its own, so the
        // `answers` map almost always falls off the end of
        // the truncated preview. We must use the untruncated
        // `tool_result_full` (see client.ts:861-867) — it
        // carries the full payload with answers intact. Fall
        // back to tool_result only if the full payload is
        // missing (older server versions that don't emit it).
        if (ev.tool_name === 'question') {
          const raw = ev.tool_result_full || ev.tool_result
          if (raw) {
            const targetParts = sub ? sub.parts : m.parts
            if (targetParts) {
              try {
                const payload = JSON.parse(raw) as { questions?: any[]; answers?: Record<string, string> }
                if (payload && payload.answers && Object.keys(payload.answers).length > 0) {
                  updateQuestionStatusInParts(targetParts, payload.answers, 'ok')
                }
              } catch {
                // Malformed result — leave the question part
                // alone; it will be re-decoded on reload.
              }
            }
          }
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
      // Cap at 20 entries to bound memory for long-running
      // sessions that emit many phase events. Older entries
      // are dropped first; the live status bar only shows the
      // tail anyway.
      if (ev.message) {
        if (!m._statusText) (m as any)._statusText = []
        const arr: string[] = (m as any)._statusText
        arr.push(ev.message)
        if (arr.length > 20) arr.shift()
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
      // Free the per-message live-status array. The stream
      // is over; the live status bar is no longer shown.
      // Without this, _statusText would stay around for
      // the lifetime of the message in the in-memory map.
      delete (m as any)._statusText
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
      // scrubMessagePhantoms(m) is already called above.
      // Global screenshot memory management: convert any
      // base64 screenshot data URLs accumulated in the stream
      // to blob URLs, and strip all but the last few across
      // the entire session. See convertAndStripScreenshots().
      convertAndStripScreenshots(id)
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

      // Plan mode: when the stream ends in plan mode, capture
      // the plan text for review before execution.
      if (!sub && state.sessionMeta[id]?.plan_mode) {
        const planText = assembleTextContent(m.parts)
        if (planText) {
          state.pendingPlanText[id] = planText
        }
      }
      break
    case 'question':
      // The LLM is asking the user a question. Two emit
      // shapes from the server (see internal/tool/question.go
      // WaitForAnswer + agent.go's partsAcc.update for the
      // gory details):
      //
      //   Prompt (before user answers):
      //     sendFn(json.Marshal(questions))  →  "[{...},{...}]"  (array)
      //
      //   Result (after user answers, re-sent with
      //   answers filled in via QuestionResponse):
      //     sendFn(json.Marshal(QuestionResponse))  →
      //     '{"questions":[{...}], "answers":{...}}'  (object)
      //
      // We normalise both shapes to { questions, answers }
      // and store the question part's `text` field in the
      // canonical {questions:[...]} object shape so
      // QuestionTable.vue's `JSON.parse(text)?.questions`
      // keeps working on reload.
      if (!ev.question_json) {
        console.warn('[chat] question event with no question_json')
        break
      }
      try {
        const raw = JSON.parse(ev.question_json) as
          | QuestionItem[]
          | { questions?: QuestionItem[]; answers?: Record<string, string> }
        const isObject = raw && typeof raw === 'object' && !Array.isArray(raw)
        const payload = isObject
          ? { questions: (raw as any).questions || [], answers: (raw as any).answers }
          : { questions: raw as QuestionItem[], answers: undefined }
        const partText = JSON.stringify({ questions: payload.questions })
        const hasAnswers = !!(payload.answers && Object.keys(payload.answers).length > 0)

        // The question part lives in the trailing
        // assistant message (the `m` returned by
        // findOrCreateLastAssistant at the top of
        // appendStreamEvent). If the question came from a
        // sub-agent, we ALSO push it into the sub-agent's
        // parts so the question card sits next to the
        // sub-agent's other output. Either way the modal
        // pops — sub-agent questions still need a user
        // answer to unblock the tool call, and the
        // submitQuestionAnswer posts to api.submitQuestionResponse
        // keyed by sessionId, so the answer routes back to
        // whichever question is pending on that session.
        const targetParts = sub ? sub.parts : m.parts

        if (hasAnswers) {
          // ── Result-with-answers: update the trailing
          // open question part in place. Don't pop the
          // modal — submitQuestionAnswer() already
          // resolved the user's intent and dismissed
          // it.
          const tail = sub
            ? findOpenQuestionInParts(sub.parts)
            : findTrailingOpenQuestion(id)
          if (tail) {
            tail.text = partText
            tail.name = JSON.stringify(payload.answers)
            tail.question_status = 'ok'
          }
        } else {
          // ── Question prompt: push a new question part
          // (status = "open") and surface the modal. We
          // always assign a FRESH object so Vue's
          // computed `currentPendingQuestion` re-evaluates
          // even if an earlier pendingQuestion for the
          // same session had an identical-shaped payload
          // (a fresh object reference forces the
          // computed to produce a new value).
          if (targetParts) {
            targetParts.push({
              kind: 'question',
              text: partText,
              question_status: 'open',
            } as any)
          }
          // Surface the modal regardless of sub-agent —
          // sub-agent questions are still interactive.
          // The modal is keyed off state.currentID, so a
          // question from a background session won't pop
          // the modal on the foreground view.
          state.pendingQuestion[id] = {
            questions: payload.questions || [],
          }
          notifyManager.play('question')
          notifyManager.notify('P-Chat', '向您提问')
        }
      } catch (e) {
        console.error('[question] failed to parse question_json:', ev.question_json?.slice(0, 200), e)
      }
      break
    case 'tool_confirm':
      if (ev.tool_confirm_json) {
        try {
          // 2026-07: the server now emits more fields on
          // the confirm payload. We parse the optional
          // ones defensively so an older server that
          // doesn't send them still works.
          const cfm = JSON.parse(ev.tool_confirm_json) as {
            tool_name: string
            args: string
            reason: string
            resolved_path?: string
            path_class?: string
            risk_level?: string
          }
          new Promise<boolean>((resolve) => {
            if (!state.pendingConfirm[id]) state.pendingConfirm[id] = []
            state.pendingConfirm[id].push({
              toolName: cfm.tool_name,
              args: cfm.args,
              reason: cfm.reason,
              resolvedPath: cfm.resolved_path,
              pathClass: cfm.path_class,
              riskLevel: cfm.risk_level,
              resolve,
            })
          }).then((approved) => {
            submitConfirmResponseInner(id, approved)
          })
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

function assembleTextContent(parts: MessagePart[] | undefined): string {
  if (!parts) return ''
  let out = ''
  walkParts(parts, p => {
    if (p.kind === 'text') out += p.text
  })
  return out
}

// recoverMissingParts is the P0-1 entry point. Called
// from InputArea.vue's streamMessagesRetry onStreamDrop
// callback when the SSE stream dies mid-turn. It:
//
//   1. Calls getSessionSnapshot(sessionId, afterSeq) to
//      pull the assistant messages that landed in the DB
//      while the stream was alive.
//   2. Walks the returned messages and merges their
//      parts[] into the trailing assistant bubble by
//      tool_id / part-text dedup (so we never double a
//      part the local store already has).
//   3. Shows a RecoveryBanner ("已恢复 N 条消息") for 3
//      seconds so the user knows the gap was repaired.
//
// The function is fire-and-forget from the call site; it
// never throws. On any failure (network, parse, missing
// session) it logs and returns. The next send already
// gets a fresh stream so the gap is "self-healing" — the
// recovery is just a UX nicety.
//
// `lastSeq` is the per-stream seq the client observed
// when the stream dropped (-1 if unknown, e.g. Wails
// path). The snapshot endpoint takes the seq cursor and
// returns rows with seq > cursor. Pass 0 to mean "no
// cursor" — the server will return all assistant rows,
// which is the safe default when the cursor is unknown.
export async function recoverMissingParts(
  sessionId: string,
  lastSeq: number,
  reason: string,
): Promise<void> {
  if (!sessionId) return
  if (state.streaming[sessionId]) {
    // Still streaming per local state — the drop is a
    // transient reconnect, not a real failure. Skip.
    return
  }
  const afterSeq = lastSeq >= 0 ? lastSeq : 0
  let snap: api.SnapshotRecovery
  try {
    snap = await api.getSessionSnapshot(sessionId, afterSeq)
  } catch (e: any) {
    console.warn('[recovery] snapshot fetch failed:', e?.message || e)
    return
  }
  if (!snap.messages || snap.messages.length === 0) {
    // No new assistant rows landed. Nothing to merge.
    return
  }

  const msgs = state.sessionMessages[sessionId]
  if (!msgs || msgs.length === 0) {
    // No local message list — the session was switched
    // out between drop and recovery. Bail; the next
    // switchSession / list-load will pick up the rows.
    return
  }

  // Find the trailing assistant message — that's the
  // bubble that was being streamed. The DB may have
  // MULTIPLE new assistant rows (one per ReAct round),
  // but locally the chat store merges them into a
  // single bubble, so the strategy is: take the LAST
  // returned assistant message's parts and merge them
  // into the trailing local bubble. Earlier rounds'
  // parts are already present (they were streamed live
  // before the drop), so the local bubble should
  // already reflect them.
  const last = snap.messages[snap.messages.length - 1]
  if (!last.parts || last.parts.length === 0) {
    return
  }

  const trailing = findTrailingAssistant(sessionId)
  if (!trailing) {
    return
  }
  // Build a set of "already-present" fingerprints from
  // the local trailing bubble, then add any returned
  // part whose fingerprint is new. Fingerprint =
  // `${kind}:${tool_id || name || first 40 chars of
  // text}`. This is best-effort: if the LLM produced
  // identical text in two parts (rare), the second
  // would be dedup'd. That's an acceptable trade for
  // never double-painting.
  const localFP = new Set<string>()
  const fp = (p: any): string => {
    if (p.kind === 'tool') return `tool:${p.tool_id || p.id || p.name || ''}`
    if (p.kind === 'text') return `text:${(p.text || '').slice(0, 40)}`
    if (p.kind === 'thinking') return `think:${(p.text || '').slice(0, 40)}`
    if (p.kind === 'sub_agent') return `sub:${p.task || ''}`
    if (p.kind === 'question') return `q:${(p.text || '').slice(0, 40)}`
    return `${p.kind || '?'}:${JSON.stringify(p).slice(0, 60)}`
  }
  walkParts(trailing.parts || [], p => localFP.add(fp(p)))

  let merged = 0
  for (const p of last.parts as any[]) {
    const key = fp(p)
    if (localFP.has(key)) continue
    if (!trailing.parts) trailing.parts = []
    trailing.parts.push(p)
    localFP.add(key)
    merged++
  }
  if (merged > 0) {
    // Refresh the cached content string from parts so
    // the markdown body matches.
    trailing.content = assembleTextContent(trailing.parts || [])
    showRecoveryBanner(sessionId, merged, reason)
  }
}

// showRecoveryBanner flips a transient state flag for
// 3 seconds. The ChatWindow watches the flag and
// renders the actual <RecoveryBanner> pill.
function showRecoveryBanner(sessionId: string, recovered: number, reason: string) {
  state.recoveryBanner = {
    sessionId,
    recovered,
    reason: reason || 'stream dropped',
    shownAt: Date.now(),
  }
  setTimeout(() => {
    if (state.recoveryBanner && state.recoveryBanner.shownAt === state.recoveryBanner.shownAt) {
      state.recoveryBanner = null
    }
  }, 3000)
}

// findTrailingAssistant returns the trailing
// assistant message in the local sessionMessages list,
// or null when there are no messages / the trailing
// message is not an assistant. Used by the P0-1
// recovery flow to know which bubble to merge parts
// into. The find-or-create variant already exists at
// line 948 (used by appendStreamEvent) — this version
// is strictly the "look up, don't create" path so the
// recovery flow can detect a missing trailing
// assistant (e.g. the user switched sessions mid-drop).
function findTrailingAssistant(sessionId: string) {
  const msgs = state.sessionMessages[sessionId]
  if (!msgs || msgs.length === 0) return null
  for (let i = msgs.length - 1; i >= 0; i--) {
    if (msgs[i].role === 'assistant') return msgs[i]
  }
  return null
}

// partsToContentString removed in P0-1: the equivalent
// helper already exists as assembleTextContent (which
// handles the same parts[] → markdown string shape, with
// sub_agent nesting). The recovery flow now calls
// assembleTextContent directly.

export function clearPendingPlan(id: string) {
  delete state.pendingPlanText[id]
}

export function getPendingPlanText(id: string): string {
  return state.pendingPlanText[id] || ''
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
  const pq = state.pendingQuestion[state.currentID]
  if (pq) {
    const id = state.currentID
    delete state.pendingQuestion[id]
    // Update the trailing open question part in the chat
    // immediately so the user sees their picks highlighted
    // the moment they submit. The tool result event will
    // (eventually) re-stamp the same part, but waiting for
    // it is racy in pchat-gui: the question event and the
    // tool result event can race, and the tool result can
    // even be dropped if the SSE stream is interrupted. By
    // stamping here, the chat UI is correct the instant the
    // modal closes, regardless of what happens downstream.
    const msgs = state.sessionMessages[id]
    if (msgs) {
      for (let i = msgs.length - 1; i >= 0; i--) {
        const m = msgs[i]
        if (m.role === 'assistant' && m.parts) {
          updateQuestionStatusInParts(m.parts, answers, 'ok')
          break
        }
      }
    }
    api.submitQuestionResponse(id, {
      questions: pq.questions,
      answers,
    })
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
  const items = state.pendingConfirm[state.currentID]
  if (items && items.length > 0) {
    const pc = items.shift()!
    if (items.length === 0) delete state.pendingConfirm[state.currentID]
    pc.resolve(approved)
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
export const currentPendingConfirm = computed(() => {
  const list = state.pendingConfirm[state.currentID]
  return list && list.length > 0 ? list[0] : null
})

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
//
// id wait: the user message that triggered the current turn has
// its row id (`user_message_id`) stamped onto the local copy only
// when the SSE `done` event lands — see appendStreamEvent, which
// walks the trailing user message and sets `msgs[i].id`. Until
// that event arrives (i.e. while the LLM is still streaming, or
// in the brief window after the stream ends before the final
// chunk reaches us), the local `id` is `undefined` and the
// rollback endpoint would reject `before_id <= 0`. We poll for
// the id with a short timeout before giving up; in practice the
// `done` event usually lands within ~100ms, so the user almost
// never sees the wait. The previous behaviour was a silent
// `return` here, which made the dialog close but did nothing —
// the user reported "点击撤销按钮弹出了二次确认，但是二次确认框
// 点击确定后撤回不生效" because they had no idea the rollback
// was dropped on the floor.
const ROLLBACK_ID_WAIT_MS = 3000
const ROLLBACK_ID_POLL_MS = 50

async function waitForMessageId(msg: Message, ms: number): Promise<boolean> {
  const deadline = Date.now() + ms
  while (!msg.id && Date.now() < deadline) {
    await new Promise<void>(r => setTimeout(r, ROLLBACK_ID_POLL_MS))
  }
  return !!msg.id
}

// Tiny bus for the chat store to surface user-visible errors.
// Naive UI's useMessage() requires a component context, so the
// store can't call it directly. ChatWindow registers a handler
// once on mount; the store invokes it whenever rollback fails
// for a reason the user should know about.
type UIMessage = { kind: 'error' | 'info'; text: string }
let _uiMessageHandler: ((m: UIMessage) => void) | null = null
export function setUIMessageHandler(fn: ((m: UIMessage) => void) | null) {
  _uiMessageHandler = fn
}
function uiError(text: string) {
  console.error(text)
  _uiMessageHandler?.({ kind: 'error', text })
}

export async function rollbackTo(sessionId: string, messageIndex: number) {
  const msgs = state.sessionMessages[sessionId]
  if (!msgs) return
  const msg = msgs[messageIndex]
  if (!msg) {
    uiError('撤回失败：消息不存在')
    return
  }
  // Wait for the server-assigned row id if the user message is
  // still in the "row id pending" state (freshly sent during the
  // current turn). Without this wait, the rollback endpoint
  // rejects the call with 400 ("before_id is required and must be
  // > 0") and the UI does nothing — see the comment block above.
  if (!msg.id) {
    const got = await waitForMessageId(msg, ROLLBACK_ID_WAIT_MS)
    if (!got) {
      uiError('撤回失败：消息尚未完成发送，请稍后再试')
      return
    }
  }

  // If currently streaming, abort first.
  if (state.streaming[sessionId]) {
    stopStream(sessionId)
  }

  let result
  try {
    result = await api.rollbackMessages(sessionId, msg.id!)
  } catch (e: any) {
    // Network / server error: surface it instead of silently
    // dropping the request. The user would otherwise see the
    // dialog close and no messages disappear, with no clue why.
    uiError(`撤回失败：${e?.message || e}`)
    return
  }

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
