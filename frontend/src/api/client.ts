// Lightweight HTTP client for the pchat-server API.
// All requests are JSON unless noted. The streaming endpoint
// (POST /sessions/:id/messages) is handled separately via
// streamMessages().

const BASE = '' // same origin; pchat-server serves both UI and API

// directBackendURL returns the pchat-server base URL for the
// streaming endpoint. In the Wails desktop app the webview
// normally talks to the server through the AssetServer proxy, but
// the proxy's response writer buffers the entire response body
// and only flushes when the request handler returns — useless for
// SSE streams that may park for minutes (the `question` tool
// flow). The webview calls window.go.main.App.GetBackendURL() to
// get the child's listen address and opens a direct connection
// for streaming.
//
// In the browser build the binding doesn't exist and we fall
// back to the same-origin BASE.
function directBackendURL(): string {
  if (typeof window === 'undefined') return BASE
  // Fast path: pchat-gui injects this from the Go side after the
  // child server passes its health check. Avoids a Go round-trip
  // for every stream.
  const injected = (window as any).__PCHAT_BACKEND__
  if (typeof injected === 'string' && injected) return injected
  // Slower path: ask the Wails binding directly. Returns "" if
  // the child hasn't announced its port yet.
  const wails = (window as any).go?.main?.App?.GetBackendURL
  if (typeof wails === 'function') {
    try {
      const v = wails()
      if (typeof v === 'string' && v) return v
    } catch { /* binding not ready */ }
  }
  return BASE
}

// waitForDirectBackend polls for the backend URL for up to ~5s.
// pchat-gui publishes the URL via the GetBackendURL binding after
// the child server passes its health check — the same moment the
// real UI takes over from the loading screen. The publish is fast
// but async: if the user hits Enter before it lands, we'd
// otherwise fall through to the Wails proxy and the SSE event
// would sit in the response-writer buffer. Waiting here is cheap
// and removes the race.
async function waitForDirectBackend(): Promise<string> {
  for (let i = 0; i < 50; i++) {
    const url = directBackendURL()
    if (url && url !== BASE) return url
    await new Promise<void>(r => setTimeout(r, 100))
  }
  return directBackendURL()
}

export interface Session {
  id: string
  title: string
  created_at: number
  updated_at: number
  // Resolved per-session picker state, included by the server
  // in the SessionResponse (see internal/server/handler.go
  // sessionToResponse). These may be empty when the session has
  // no override, in which case the client falls back to
  // "tech" / "" / "" as a safe default for the UI.
  style?: string
  provider?: string
  model?: string
  project_path?: string
  plan_mode?: boolean
  permission_level?: string
  reasoning_effort?: string
  vector_store?: string
  knowledge_base?: string
}

export interface Attachment {
  id: string
  name: string
  size: number
  mime: string
  kind: 'image' | 'audio' | 'video' | 'text' | 'file'
}

export interface MessageAttachment {
  // image_url  — image; rendered as <img> with click-to-zoom
  // audio_url  — audio; rendered as <audio controls>
  // video_url  — video; rendered as <video controls>
  // text       — anything else (file body, unsupported media
  //             with a text marker, etc.)
  type: 'image_url' | 'audio_url' | 'video_url' | 'text'
  url?: string
  text?: string
  name?: string
  mime?: string
  kind?: string
}

// MessagePart is one block of content inside a Message.
// The assistant message model is a flat list of parts in
// stream order: text + thinking + tool calls + sub-agents
// interleave freely as the upstream LLM emits them.
//
// User / system messages are still just a single text
// string under `content` — they don't have parts.
export type MessagePart =
  | { kind: 'text'; text: string }
  | { kind: 'thinking'; text: string; streaming?: boolean }
  | {
      kind: 'tool'
      // The tool call's stable id, e.g. "read_file".
      // For non-native calls parsed out of markdown, the
      // id is undefined and the call is keyed by index.
      id?: string
      name: string
      // JSON-encoded arguments string. Empty until the
      // call's args have been parsed.
      args?: string
      // 'start' | 'ok' | 'warn' | 'error'. Defaults to
      // 'start' on creation; updated when the matching
      // 'tool' event arrives.
      status: 'start' | 'ok' | 'warn' | 'error'
      result?: string
      error?: string
      elapsed?: string
    }
  | {
      kind: 'sub_agent'
      // The sub-agent's task description. Acts as the
      // card's primary label and the unique key (no id
      // on the wire).
      task: string
      // 'start' | 'ok' | 'err'.
      status: 'start' | 'ok' | 'err'
      // The sub-agent's own message stream — same
      // MessagePart union, recursively nested. (We don't
      // actually recurse in practice; sub-agents cannot
      // spawn sub-agents. The type just allows it.)
      parts: MessagePart[]
      elapsed?: string
      // The agent's name (e.g. "explore", "plan",
      // "general-purpose", or a custom agent from
      // .p-chat/agent/*.md). Surfaced in the card header.
      agentType?: string
      // The agent's accent color ("#RRGGBB" or CSS color
      // name). Tints the card border + badge.
      agentColor?: string
      // The model the sub-agent is using (e.g.
      // "gpt-4o-mini"). Shown as a small chip when set.
      agentModel?: string
      // The resume-by-id key. Surfaced as a monospace
      // badge in the footer; click to copy.
      taskId?: string
      // The agent's one-line "when to use" hint from the
      // registry. Surfaced as a hover tooltip on the
      // agent-name badge so the user can read the full
      // hint without expanding the card body.
      agentDescription?: string
      // The reason the sub-agent failed. Only set when
      // status === 'err'. Surfaced as a tooltip on the
      // "失败" status so the user can see *why* without
      // expanding the card body. Empty for soft-fail
      // (content produced) and for ok status.
      failureReason?: string
    }

export type SubAgentPart = Extract<MessagePart, { kind: 'sub_agent' }>
export type ToolPart = Extract<MessagePart, { kind: 'tool' }>
export type TextPart = Extract<MessagePart, { kind: 'text' }>
export type ThinkingPart = Extract<MessagePart, { kind: 'thinking' }>

export interface TodoItem {
  id: string
  content: string
  status: string
}

export interface Message {
  id?: number
  role: 'user' | 'assistant' | 'tool' | 'system'
  // For user / system messages this is the text body.
  // For assistant messages, prefer `parts` — but `content`
  // is kept in sync as a denormalized cache so the
  // markdown pipeline can render the whole thing without
  // walking the parts array.
  content: string
  // Structured parts (assistant messages only). May be
  // empty for older messages loaded from history (the
  // server only persists the final content text).
  parts?: MessagePart[]
  // The LLM's chain-of-thought (assistant messages only).
  // Rendered as a collapsible "thinking" panel. The
  // post-stream redactor may rewrite this if the LLM
  // produced a phantom error in the thinking block.
  thinking?: string
  created_at?: number
  tool_call_id?: string
  name?: string
  provider?: string
  model?: string
  attachments?: MessageAttachment[]
  // Final token usage + elapsed time, stamped on the
  // assistant message when the 'done' event arrives.
  tokens_in?: number
  tokens_out?: number
  elapsed?: string
  // visionUnsupported is set on a *user* message when the
  // LLM rejected the user's image with the "this model
  // does not support image input" error. The chat store
  // tags the trailing user message when the error event
  // (ErrorKind === "vision_unsupported") arrives. The
  // MessageBubble renders a clear warning chip below
  // the attachment so the user sees *why* the image was
  // ignored, even after the toast disappears. Only
  // meaningful on role==="user".
  visionUnsupported?: boolean
  // Live status text during streaming (populated by
  // appendStreamEvent from phase events).
  _statusText?: string[]
}

export interface SessionMeta {
  id: string
  title: string
  style: string
  provider: string
  model: string
  project_path?: string
  plan_mode?: boolean
  permission_level?: string
  created_at: number
  updated_at: number
}

export interface UpdateSessionMetaResponse {
  ok?: boolean
  id?: string
  title?: string
  style?: string
  provider?: string
  model?: string
  plan_mode?: boolean
  permission_level?: string
  reasoning_effort?: string
  vector_store?: string
  knowledge_base?: string
  created_at?: number
  updated_at?: number
}

export interface UploadMeta {
  id: string
  name: string
  size: number
  kind: string
  mime: string
}

export interface SearchResult {
  conversation_id: string
  conversation_title: string
  message_id: number
  role: string
  snippet: string
  created_at: number
}

export interface SearchResponse {
  results: SearchResult[]
}

export interface TokenStat {
  conversation_id: string
  conversation_title: string
  tokens_in: number
  tokens_out: number
  msg_count: number
  updated_at: number
}

export const fetchTokenStats = () =>
  jsonFetch<{ stats: TokenStat[] }>('/api/v1/token-stats')

async function jsonFetch<T>(url: string, init?: RequestInit): Promise<T> {
  const res = await fetch(BASE + url, {
    ...init,
    headers: { 'Content-Type': 'application/json', ...(init?.headers || {}) },
  })
  if (!res.ok) {
    const t = await res.text()
    throw new Error(`HTTP ${res.status}: ${t}`)
  }
  return res.json() as Promise<T>
}

// --- Health ---
export const health = () => jsonFetch<{ status: string }>('/api/v1/health')

// --- Search ---
export const searchMessages = (q: string, limit = 20) =>
  jsonFetch<SearchResponse>(
    `/api/v1/search?q=${encodeURIComponent(q)}&limit=${limit}`,
  )

// --- Sessions ---
export const listSessions = (projectPath: string) =>
  jsonFetch<{ sessions: Session[] }>(
    `/api/v1/sessions?project_path=${encodeURIComponent(projectPath)}`,
  )

export const getSession = (id: string) =>
  jsonFetch<Session>(`/api/v1/sessions/${encodeURIComponent(id)}`)

export const createSession = (projectPath?: string) =>
  jsonFetch<{ id: string }>(
    '/api/v1/sessions',
    { method: 'POST', body: JSON.stringify({ project_path: projectPath || '' }) },
  )

export const deleteSession = (id: string) =>
  jsonFetch<{ ok: boolean }>(`/api/v1/sessions/${id}`, { method: 'DELETE' })

export const renameSession = (id: string, title: string) =>
  jsonFetch<UpdateSessionMetaResponse>(`/api/v1/sessions/${id}`, {
    method: 'PATCH',
    body: JSON.stringify({ title }),
  })

export const updateSessionMeta = (
  id: string,
  fields: Partial<{ style: string; provider: string; model: string; title: string; plan_mode: boolean; permission_level: string; vector_store: string; knowledge_base: string }>,
) =>
  jsonFetch<UpdateSessionMetaResponse>(`/api/v1/sessions/${id}`, {
    method: 'PATCH',
    body: JSON.stringify(fields),
  })

export const compressConversation = (id: string) =>
  jsonFetch<{ compressed: boolean; summary: string }>(`/api/v1/sessions/${id}/compress`, { method: 'POST' })

export const setReasoningEffort = (id: string, level: string) =>
  jsonFetch<{ ok: boolean; reasoning_effort: string }>(`/api/v1/sessions/${id}/reasoning-effort`, {
    method: 'PATCH',
    body: JSON.stringify({ level }),
  })

export const saveSystemMessage = (id: string, content: string) =>
  jsonFetch<{ ok: boolean }>(`/api/v1/sessions/${id}/system-message`, {
    method: 'POST',
    body: JSON.stringify({ content }),
  })

export const getTodos = (id: string) =>
  jsonFetch<{ todos: TodoItem[] }>(`/api/v1/sessions/${id}/todos`)

export interface QuestionItem {
  question: string
  header: string
  options: { label: string; description: string }[]
  multi_select?: boolean
}

export interface QuestionResponsePayload {
  questions: QuestionItem[]
  answers: Record<string, string>
}

export const submitQuestionResponse = (id: string, resp: QuestionResponsePayload) =>
  jsonFetch<{ ok: boolean }>(`/api/v1/sessions/${id}/question-response`, {
    method: 'POST',
    body: JSON.stringify(resp),
  })

export const executePlan = (id: string, planText: string) =>
  jsonFetch<{ ok: boolean; id: string }>(`/api/v1/sessions/${encodeURIComponent(id)}/execute-plan`, {
    method: 'POST',
    body: JSON.stringify({ plan_text: planText }),
  })

// --- Projects ---
export interface ProjectItem {
  name: string
  path: string
}

export const listProjects = () =>
  jsonFetch<{ projects: ProjectItem[] }>('/api/v1/projects')

export const addProject = (name: string, path: string) =>
  jsonFetch<{ projects: ProjectItem[] }>('/api/v1/projects', {
    method: 'POST',
    body: JSON.stringify({ name, path }),
  })

export const removeProject = (path: string) =>
  jsonFetch<{ projects: ProjectItem[] }>('/api/v1/projects', {
    method: 'DELETE',
    body: JSON.stringify({ path }),
  })

// --- Dialog ---
export const pickFolder = () =>
  jsonFetch<{ path: string }>('/api/v1/dialog/folder', { method: 'POST' })

export const openExplorer = async (path: string) => {
  const { OpenExplorer } = await import('../../wailsjs/go/main/App')
  await OpenExplorer(path)
}

export const openTerminal = async (path: string) => {
  const { OpenTerminal } = await import('../../wailsjs/go/main/App')
  await OpenTerminal(path)
}

// --- Skills ---
export interface SkillItem {
  name: string
  description: string
  path: string
  content?: string
}

export interface SearchSkillItem {
  name: string
  description: string
  url: string
}

export const listSkills = () =>
  jsonFetch<{ skills: SkillItem[] }>('/api/v1/skills')

export const getSkill = (name: string) =>
  jsonFetch<{ skill: SkillItem }>(`/api/v1/skills/${encodeURIComponent(name)}`)

export const installSkill = (name: string, url: string) =>
  jsonFetch<{ ok: boolean; name: string }>('/api/v1/skills/install', {
    method: 'POST',
    body: JSON.stringify({ name, url }),
  })

export const deleteSkill = (name: string) =>
  jsonFetch<{ ok: boolean }>(`/api/v1/skills/${encodeURIComponent(name)}`, { method: 'DELETE' })

export const searchSkills = (q: string) =>
  jsonFetch<{ results: SearchSkillItem[] }>(`/api/v1/skills/search?q=${encodeURIComponent(q)}`)

export interface SavedRepo {
  name: string
  url: string
}

export const listSkillRepos = () =>
  jsonFetch<{ repos: SavedRepo[] }>('/api/v1/skills/repos')

export const addSkillRepo = (name: string, url: string) =>
  jsonFetch<{ repos: SavedRepo[] }>('/api/v1/skills/repos', {
    method: 'POST',
    body: JSON.stringify({ name, url }),
  })

export const removeSkillRepo = (url: string) =>
  jsonFetch<{ repos: SavedRepo[] }>('/api/v1/skills/repos', {
    method: 'DELETE',
    body: JSON.stringify({ url }),
  })

// --- MCP ---
export interface MCPServerInfo {
  name: string
  state: 'stopped' | 'starting' | 'running' | 'error'
  tool_count: number
  error?: string
}

export const listMCPServers = () =>
  jsonFetch<{ servers: MCPServerInfo[]; global_enabled: boolean }>('/api/v1/mcp/servers')

export const addMCPServer = (cfg: {
  name: string
  type?: string
  command?: string
  args?: string[]
  env?: Record<string, string>
  url?: string
  enabled?: boolean
  timeout?: string
}) =>
  jsonFetch<{ ok: boolean }>('/api/v1/mcp/servers', {
    method: 'POST',
    body: JSON.stringify(cfg),
  })

export const removeMCPServer = (name: string) =>
  jsonFetch<{ ok: boolean }>(`/api/v1/mcp/servers/${encodeURIComponent(name)}`, { method: 'DELETE' })

export const restartMCPServer = (name: string) =>
  jsonFetch<{ ok: boolean }>(`/api/v1/mcp/servers/${encodeURIComponent(name)}/restart`, { method: 'POST' })

export const setMCPGlobal = (enabled: boolean) =>
  jsonFetch<{ global_enabled: boolean }>('/api/v1/mcp/global', {
    method: 'PATCH',
    body: JSON.stringify({ enabled }),
  })

// --- Messages ---

// PageOpts controls infinite-scroll history loading.
// beforeId: the lowest row id from the previous page; pass 0
//   (or omit) for the most recent page.
// limit: page size. The server applies the per-session context
//   cap when 0 is passed.
export interface PageOpts {
  beforeId?: number
  limit?: number
}

export interface ListMessagesResult {
  messages: Message[]
  has_more: boolean
  // The id to pass as `beforeId` on the next page request.
  // Always the smallest row id in `messages`. 0 when the
  // returned page is empty.
  oldest_id: number
}

// listMessages fetches a page of session history. Omit
// `opts` to get the full history (first open after reload —
// the server applies the context-window cap automatically).
export const listMessages = (id: string, opts?: PageOpts) => {
  const q = new URLSearchParams()
  if (opts?.beforeId && opts.beforeId > 0) q.set('before_id', String(opts.beforeId))
  if (opts?.limit && opts.limit > 0) q.set('limit', String(opts.limit))
  const qs = q.toString()
  return jsonFetch<ListMessagesResult>(
    `/api/v1/sessions/${id}/messages${qs ? '?' + qs : ''}`,
  )
}

// --- Archive ---
export const archiveSession = (id: string) =>
  jsonFetch<{ ok: boolean }>(`/api/v1/sessions/${encodeURIComponent(id)}/archive`, { method: 'POST' })

export const unarchiveSession = (id: string) =>
  jsonFetch<{ ok: boolean }>(`/api/v1/sessions/${encodeURIComponent(id)}/unarchive`, { method: 'POST' })

export const listArchived = () =>
  jsonFetch<{ sessions: Session[] }>('/api/v1/sessions/archived')

export const permanentDeleteSession = (id: string) =>
  jsonFetch<{ deleted: string }>(`/api/v1/sessions/${encodeURIComponent(id)}/permanent`, { method: 'DELETE' })

export const clearSessionMessages = (id: string) =>
  jsonFetch<{ cleared: string }>(`/api/v1/sessions/${encodeURIComponent(id)}/messages`, { method: 'DELETE' })

// --- Rollback ---
export interface RollbackResult {
  deleted_count: number
  deleted_messages: Message[]
}

export const rollbackMessages = (sessionId: string, beforeId: number) =>
  jsonFetch<RollbackResult>(
    `/api/v1/sessions/${encodeURIComponent(sessionId)}/rollback`,
    { method: 'POST', body: JSON.stringify({ before_id: beforeId }) },
  )

export const undoRollback = (sessionId: string, messages: Message[]) =>
  jsonFetch<{ ok: boolean; restored_count: number }>(
    `/api/v1/sessions/${encodeURIComponent(sessionId)}/rollback/undo`,
    { method: 'POST', body: JSON.stringify({ messages }) },
  )

// --- Fork ---
export const forkSession = (sessionId: string, beforeId: number) =>
  jsonFetch<Session>(
    `/api/v1/sessions/${encodeURIComponent(sessionId)}/fork`,
    { method: 'POST', body: JSON.stringify({ before_id: beforeId }) },
  )

// --- Uploads ---
export async function uploadFile(file: File): Promise<UploadMeta> {
  const fd = new FormData()
  fd.append('file', file)
  const res = await fetch(BASE + '/api/v1/uploads', { method: 'POST', body: fd })
  if (!res.ok) {
    const t = await res.text()
    throw new Error(`HTTP ${res.status}: ${t}`)
  }
  return res.json() as Promise<UploadMeta>
}

export function uploadURL(id: string): string {
  return `${BASE}/api/v1/uploads/${encodeURIComponent(id)}`
}

// --- Providers / Models ---
export interface ModelInfo {
  name: string
  display_name?: string
  description?: string
  default?: boolean
  max_tokens_context?: number
  max_tokens_output?: number
  // Per-model hints. Mirrors config.Capabilities on the
  // server. The UI uses supports_vision to render a 👁
  // badge in the model picker; thinking_effort is shown
  // as a chip in the model edit form.
  capabilities?: {
    thinking_effort?: 'off' | 'low' | 'medium' | 'high' | ''
    context_window?: number
    supports_vision?: boolean
    supports_audio?: boolean
  }
}

export interface ProviderInfo {
  name: string
  protocol: 'openai' | 'anthropic' | string
  base_url: string
  api_key: string
  is_default: boolean
  // Slim view from GET /api/v1/providers.
  model: string
  models: ModelInfo[]
}

export const listProviders = () =>
  jsonFetch<{ providers: ProviderInfo[] }>('/api/v1/providers')

// Rich view (GET /api/v1/providers/:name) returns the same
// shape; the slim list view and the rich per-provider view
// use the same struct.
export const getProvider = (name: string) =>
  jsonFetch<ProviderInfo>(`/api/v1/providers/${encodeURIComponent(name)}`)

// --- Style management (app-level CRUD) ---
export interface StyleInfo {
  id: string
  label: string
  desc: string
}

export interface StyleDetail extends StyleInfo {
  prompt: string
  memory?: string
}

export const getStyles = () => jsonFetch<{ styles: StyleInfo[] }>('/api/v1/styles')

export const getStyle = (id: string) =>
  jsonFetch<StyleDetail>(`/api/v1/styles/${encodeURIComponent(id)}`)

export interface CreateStyleRequest {
  id: string
  label: string
  prompt: string
  memory?: string
}

export const createStyle = (req: CreateStyleRequest) =>
  jsonFetch<StyleInfo>('/api/v1/styles', {
    method: 'POST',
    body: JSON.stringify(req),
  })

export interface UpdateStyleRequest {
  label?: string
  prompt?: string
  memory?: string
}

export const updateStyle = (id: string, req: UpdateStyleRequest) =>
  jsonFetch<{ ok: boolean; id: string }>(`/api/v1/styles/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    body: JSON.stringify(req),
  })

export const deleteStyle = (id: string) =>
  jsonFetch<{ ok: boolean; id: string }>(`/api/v1/styles/${encodeURIComponent(id)}`, {
    method: 'DELETE',
  })

// --- Slash commands ---
export interface CommandSpec {
  name: string
  description: string
  args?: string
  group: string
  web_safe: boolean
}

export const listCommands = () =>
  jsonFetch<{ commands: CommandSpec[] }>('/api/v1/commands')

export const runCommand = (name: string, args: string) =>
  jsonFetch<{ output: string }>(`/api/v1/commands/${encodeURIComponent(name)}`, {
    method: 'POST',
    body: JSON.stringify({ args }),
  })

// --- App-level provider configuration ---
export interface AddProviderRequest {
  name: string
  protocol: 'openai' | 'anthropic'
  base_url: string
  api_key: string
  model: string
}

export const addProvider = (req: AddProviderRequest) =>
  jsonFetch<{ ok: boolean; name: string }>('/api/v1/providers', {
    method: 'POST',
    body: JSON.stringify(req),
  })

export const deleteProvider = (name: string) =>
  jsonFetch<{ ok: boolean }>(`/api/v1/providers/${encodeURIComponent(name)}`, {
    method: 'DELETE',
  })

export const setDefaultProvider = (name: string) =>
  jsonFetch<{ ok: boolean }>(`/api/v1/providers/${encodeURIComponent(name)}/default`, {
    method: 'POST',
  })

// UpdateProviderRequest is the body of the unified
// PATCH /api/v1/providers/:name. Every field is optional;
// the server only writes the non-empty ones. Pass set_default
// (not is_default) to promote a provider to the global default.
export interface UpdateProviderRequest {
  name?: string
  protocol?: 'openai' | 'anthropic'
  base_url?: string
  api_key?: string
  set_default?: boolean
}

export const updateProvider = (name: string, req: UpdateProviderRequest) =>
  jsonFetch<ProviderInfo>(`/api/v1/providers/${encodeURIComponent(name)}`, {
    method: 'PATCH',
    body: JSON.stringify(req),
  })

// --- Per-model CRUD ---
export interface AddModelRequest {
  name: string
  display_name?: string
  description?: string
  max_tokens_context?: number
  max_tokens_output?: number
}

export const addModel = (provider: string, req: AddModelRequest) =>
  jsonFetch<{ ok: boolean; name: string }>(
    `/api/v1/providers/${encodeURIComponent(provider)}/models`,
    { method: 'POST', body: JSON.stringify(req) },
  )

export interface UpdateModelRequest {
  display_name?: string
  description?: string
  max_tokens_context?: number
  max_tokens_output?: number
  clear_all?: boolean
}

export const updateModel = (provider: string, model: string, req: UpdateModelRequest) =>
  jsonFetch<{ ok: boolean; provider: string; model: string }>(
    `/api/v1/providers/${encodeURIComponent(provider)}/models/${encodeURIComponent(model)}`,
    { method: 'PUT', body: JSON.stringify(req) },
  )

export const deleteModel = (provider: string, model: string) =>
  jsonFetch<{ ok: boolean }>(
    `/api/v1/providers/${encodeURIComponent(provider)}/models/${encodeURIComponent(model)}`,
    { method: 'DELETE' },
  )

export const setDefaultModel = (provider: string, model: string) =>
  jsonFetch<{ ok: boolean }>(
    `/api/v1/providers/${encodeURIComponent(provider)}/models/${encodeURIComponent(model)}/default`,
    { method: 'POST' },
  )

export interface SetCapabilitiesRequest {
  thinking_effort?: 'off' | 'low' | 'medium' | 'high' | ''
  context_window?: number
  supports_vision?: boolean
  supports_audio?: boolean
}

export const setModelCapabilities = (
  provider: string,
  model: string,
  req: SetCapabilitiesRequest,
) =>
  jsonFetch<{ ok: boolean }>(
    `/api/v1/providers/${encodeURIComponent(provider)}/models/${encodeURIComponent(model)}/capabilities`,
    { method: 'PATCH', body: JSON.stringify(req) },
  )

// --- Upstream models ---
export interface UpstreamModelItem {
  id: string
  created: number
  owned_by: string
  added: boolean
}

export const fetchUpstreamModels = (provider: string) =>
  jsonFetch<{ models: UpstreamModelItem[] }>(
    `/api/v1/providers/${encodeURIComponent(provider)}/upstream-models`,
  )

// --- Streaming send ---
export interface InlineAttachment {
  // 'image_url' for images, 'audio_url' / 'video_url' for media
  // the chat bubble can preview, 'text' for file bodies the
  // model only gets to read as text.
  type: 'image_url' | 'audio_url' | 'video_url' | 'text'
  // For image_url / audio_url / video_url: the data: URL
  // (e.g. "data:image/png;base64,...") carrying the inline
  // file bytes. For text: undefined (the body is in `text`).
  url?: string
  // For text: the file body. For *_url: undefined.
  text?: string
  // Original filename, kept around for the chat bubble label and
  // for the backend's debug logs.
  name: string
  // 'image' | 'audio' | 'video' | 'text' | 'file'
  kind: string
  // MIME type, used to pick the right wrapping ("data:image/...;base64,"
  // vs raw text).
  mime: string
}

export interface SendOptions {
  message: string
  provider?: string
  model?: string
  style?: string
  // Inline attachments carry the bytes up front so the message
  // is self-contained: the chat bubble shows the image
  // immediately, the backend doesn't need to re-read the file
  // from disk, and the message is replayable after a server
  // restart (the data is in the SQLite row).
  attachments?: InlineAttachment[]
  signal?: AbortSignal
  onEvent: (ev: StreamEvent) => void
  skill_context?: string
}

export interface StreamEvent {
  type?: string
  phase?: string
  step?: string
  message?: string
  // Content is a text delta (assistant prose). Populated when
  // type === 'content'. The client appends it to the trailing
  // text part of the assistant message.
  //
  // When type === 'content_rewrite' (emitted by the agent's
  // post-stream redactor, e.g. when it strips a phantom vision
  // error), this field carries the *replacement* text. The client
  // should REPLACE the trailing text part's text with this value
  // rather than append it. The redactor runs after the model
  // stream ends and may swap out a model-fabricated error string
  // for a clean user-facing message.
  content?: string
  // Thinking is a reasoning / chain-of-thought delta
  // (DeepSeek reasoning_content, OpenAI o1 reasoning, Anthropic
  // thinking_delta). Populated when type === 'thinking'. The
  // client appends it to the trailing thinking part of the
  // assistant message.
  thinking?: string
  tool_name?: string
  tool_status?: string
  tool_result?: string
  // tool_result_full is the untruncated tool result for tools
  // whose output the chat store needs to JSON.parse (todo_write,
  // question). tool_result is a 300-char preview suitable for
  // human display; tool_result_full preserves the full payload
  // (newlines and all). The chat store uses tool_result_full in
  // preference to tool_result when present.
  tool_result_full?: string
  tool_error?: string
  tool_elapsed?: string
  // tool_args is the JSON-encoded arguments string the tool
  // was called with. Best-effort: LLM clients only surface this
  // once the call is complete.
  tool_args?: string
  // Sub-agent fields. When sub_agent is true, the event
  // originated from a `task` tool's child run. The client
  // routes such events into the matching nested card (keyed
  // by sub_agent_task).
  sub_agent?: boolean
  sub_agent_task?: string
  sub_agent_status?: 'start' | 'ok' | 'err' | string
  sub_agent_type?: string
  sub_agent_color?: string
  sub_agent_model?: string
  sub_agent_task_id?: string
  sub_agent_description?: string
  sub_agent_failure_reason?: string
  // thinking_rewrite is the post-stream redactor's
  // replacement text for the LLM's thinking block. The
  // UI should REPLACE the trailing thinking part's text
  // with this value (same pattern as content_rewrite for
  // the text body). Empty when no rewrite is needed.
  thinking_rewrite?: string
  tokens_in?: number
  tokens_out?: number
  elapsed?: string
  provider?: string
  model?: string
  error?: string
  suggestion?: string
  // error_kind is the classification of the error
  // ("auth_error", "rate_limit", "vision_unsupported", …).
  // Populated by the server's chunkToEvent when the
  // classifier identifies the error. The chat store
  // uses "vision_unsupported" specifically to tag the
  // trailing user message with visionUnsupported: true
  // so the MessageBubble can render a clear chip.
  error_kind?: string

  // question_json carries the serialized question payload
  // when type === 'question'. The chat store parses it
  // and surfaces a QuestionModal for the user to answer.
  question_json?: string

  // tool_confirm_json carries the serialized confirm request
  // when type === 'tool_confirm'. The chat store surfaces a
  // simple approve/reject dialog.
  tool_confirm_json?: string

  // session_status is the lifecycle signal for a chat turn:
  // "busy" at the start of the agent loop, "idle" when it
  // exits (any reason — success, error, cancel, max-rounds,
  // stuck, panic). The chat store uses this to maintain
  // state.sessionWorking[id]; the TodoPanel state machine
  // uses `live = state.sessionWorking[id]` to decide whether
  // to show or clear stale todos. Without this signal, the
  // dock can't tell "LLM is mid-turn, keep todos" from
  // "LLM stopped and forgot to clear them".
  session_status?: 'busy' | 'idle' | 'retry' | string

  // user_message_id is the SQLite row id of the user message
  // that started this turn. Set only on the "done" event.
  user_message_id?: number
  // last_message_id is the highest row id in this session
  // (typically the assistant reply just produced).
  last_message_id?: number
}

export async function submitConfirmResponse(sessionId: string, approved: boolean): Promise<void> {
  await jsonFetch(`/api/v1/sessions/${encodeURIComponent(sessionId)}/confirm-response`, {
    method: 'POST',
    body: JSON.stringify({ approved }),
  })
}

export async function streamMessages(sessionId: string, opts: SendOptions): Promise<void> {
  // Route the SSE stream through the Go side via the StreamMessages
  // Wails binding. The Wails AssetServer's response writer buffers
  // the entire body and only flushes when the request handler
  // returns, which doesn't happen for a 5-minute question tool
  // block. A direct fetch() to the backend hits CORS/Private
  // Network Access friction from the wails.localhost origin and
  // times out. The Go binding is a direct in-process call — no
  // CORS, no buffering beyond the standard chunked transfer.
  const { StreamMessages, CancelStream } = await import('../../wailsjs/go/main/App')
  const { EventsOn, EventsOff } = await import('../../wailsjs/runtime/runtime')

  const body = JSON.stringify({
    message: opts.message,
    provider: opts.provider,
    model: opts.model,
    style: opts.style,
    attachments: opts.attachments,
    skill_context: opts.skill_context || '',
  })

  const flush = () => new Promise<void>(r => setTimeout(r, 0))
  const offEvent = EventsOn('stream:event', (...args: any[]) => {
    const raw = args[0] as string
    try {
      const wrap = JSON.parse(raw) as { session: string; event: string; data: string }
      // Drop events that belong to a different session. Wails
      // EventsOn is process-global; two parallel StreamMessages
      // calls share the channel. Without this filter, session B's
      // events would land in session A's message list.
      if (wrap.session && wrap.session !== sessionId) return
      if (wrap.event && wrap.event !== 'message' && wrap.event !== '') return
      const data = (wrap.data || '').trim()
      if (!data || data === '[DONE]') return
      const ev = JSON.parse(data) as StreamEvent
      // Wrap the dispatch in its own try/catch. The handler mutates
      // Vue reactive state, which can synchronously run a render
      // flush; if any Naive UI internals (popover, tooltip, NMessage
      // instance) try to querySelectorAll a DOM element that's been
      // swapped out mid-flush, an unhandled TypeError escapes the
      // Vue scheduler and surfaces in the console as
      // "P.querySelectorAll is not a function" — with no other
      // recovery. We log and continue, so the next event still lands.
      try {
        opts.onEvent(ev)
      } catch (inner) {
        console.warn('[stream] event handler threw, continuing:', inner)
      }
    } catch (e) {
      console.warn('SSE parse error', e, 'raw:', (raw || '').slice(0, 200))
    }
  })
  const offEnd = EventsOn('stream:end', (...args: any[]) => {
    // stream:end carries the session id of the stream that
    // finished. Ignore ends from other concurrent streams.
    const sid = args[0] as string
    if (sid && sid !== sessionId) return
    // stream:end is informational; the Go binding's StreamMessages
    // promise resolving is the actual signal that the stream is
    // done. Nothing to do here.
  })

  try {
    await StreamMessages(sessionId, body)
    // Give the final event a tick to land in the JS event loop.
    await flush()
  } catch (e: any) {
    offEvent()
    offEnd()
    if (opts.signal?.aborted) return
    throw new Error(`stream: ${e?.message || e}`)
  }
  offEvent()
  offEnd()
}

// ---- Knowledge API ----

export interface KnowledgeConfig {
  enabled: boolean
  auto_index: boolean
  bases: KnowledgeBaseItem[]
}

export interface KnowledgeBaseItem {
  name: string
  path: string
  file_types?: string[]
  enabled: boolean
  scan_model?: string
  scan_media_types?: string[]
  auto_scan?: boolean
  exclude_patterns?: string[]
  max_file_size?: number
  status?: string
  doc_count?: number
}

export const getKnowledgeConfig = () =>
  jsonFetch<KnowledgeConfig>('/api/v1/knowledge/config')

export const updateKnowledgeConfig = (patch: Partial<KnowledgeConfig>) =>
  jsonFetch<KnowledgeConfig>('/api/v1/knowledge/config', {
    method: 'PATCH',
    body: JSON.stringify(patch),
  })

export interface KnowledgeModel {
  provider: string
  model: string
  supports_vision: boolean
}

export const listKnowledgeModels = () =>
  jsonFetch<KnowledgeModel[]>('/api/v1/knowledge/models')

export const getKnowledgeBases = () =>
  jsonFetch<KnowledgeBaseItem[]>('/api/v1/knowledge/bases')

export const addKnowledgeBase = (base: KnowledgeBaseItem) =>
  jsonFetch<{ ok: boolean }>('/api/v1/knowledge/bases', {
    method: 'POST',
    body: JSON.stringify(base),
  })

export const removeKnowledgeBase = (name: string) =>
  jsonFetch<{ ok: boolean }>(`/api/v1/knowledge/bases/${encodeURIComponent(name)}`, {
    method: 'DELETE',
  })

export const scanKnowledgeBase = (name: string) =>
  jsonFetch<{ ok: boolean; message?: string }>(
    `/api/v1/knowledge/bases/${encodeURIComponent(name)}/scan`,
    { method: 'POST' },
  )

export const getScanStatus = (name: string) =>
  jsonFetch<{ chunks: number; done: boolean; error?: string; current: number; total: number; message?: string }>(
    `/api/v1/knowledge/bases/${encodeURIComponent(name)}/scan/status`,
  )

export const cancelScan = (name: string) =>
  jsonFetch<{ ok: boolean; message?: string }>(
    `/api/v1/knowledge/bases/${encodeURIComponent(name)}/scan`,
    { method: 'DELETE' },
  )

export const searchKnowledge = (query: string, topK?: number) =>
  jsonFetch<{ query: string; results: Array<{ source: string; content: string; similarity: number; rank: number }> }>(
    '/api/v1/knowledge/search',
    {
      method: 'POST',
      body: JSON.stringify({ query, top_k: topK || 5 }),
    },
  )

// (removed: getAvailableEmbedders — vector embedding system deprecated)

// Knowledge section management
export const listKnowledgeSections = (baseName: string) =>
  jsonFetch<{ sections: Array<{ id: number; title: string; content: string; source: string; base: string }> }>(
    `/api/v1/knowledge/bases/${encodeURIComponent(baseName)}/sections`,
  )

export const getKnowledgeSection = (baseName: string, id: number) =>
  jsonFetch<{ id: number; title: string; content: string; source: string; base: string }>(
    `/api/v1/knowledge/bases/${encodeURIComponent(baseName)}/sections/${id}`,
  )

export const addKnowledgeSection = (baseName: string, body: { title: string; content: string; source: string }) =>
  jsonFetch<{ id: number; ok: boolean }>(
    `/api/v1/knowledge/bases/${encodeURIComponent(baseName)}/sections`,
    { method: 'POST', body: JSON.stringify(body) },
  )

export const deleteKnowledgeSection = (baseName: string, id: number) =>
  jsonFetch<{ ok: boolean }>(
    `/api/v1/knowledge/bases/${encodeURIComponent(baseName)}/sections/${id}`,
    { method: 'DELETE' },
  )
