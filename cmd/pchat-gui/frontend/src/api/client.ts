// Lightweight HTTP client for the pchat-server API.
// All requests are JSON unless noted. The streaming endpoint
// (POST /sessions/:id/messages) is handled separately via
// streamMessages().

const BASE = '' // same origin; pchat-server serves both UI and API

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
}

export interface Attachment {
  id: string
  name: string
  size: number
  mime: string
  kind: 'image' | 'audio' | 'text' | 'file'
}

export interface MessageAttachment {
  type: 'image_url' | 'text'
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
}

export interface SessionMeta {
  id: string
  title: string
  style: string
  provider: string
  model: string
  project_path?: string
  created_at: number
  updated_at: number
}

export interface UpdateSessionMetaResponse {
  ok?: boolean
  // When the server resolves fallbacks (e.g. the user picked a
  // provider but the request body didn't include a model), the
  // resolved values come back as a full SessionResponse so the
  // client can sync its picker state.
  id?: string
  title?: string
  style?: string
  provider?: string
  model?: string
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
  fields: Partial<{ style: string; provider: string; model: string; title: string }>,
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

// --- Skills ---
export interface SkillItem {
  name: string
  description: string
  path: string
}

export interface SearchSkillItem {
  name: string
  description: string
  url: string
}

export const listSkills = () =>
  jsonFetch<{ skills: SkillItem[] }>('/api/v1/skills')

export const installSkill = (name: string, url: string) =>
  jsonFetch<{ ok: boolean; name: string }>('/api/v1/skills/install', {
    method: 'POST',
    body: JSON.stringify({ name, url }),
  })

export const deleteSkill = (name: string) =>
  jsonFetch<{ ok: boolean }>(`/api/v1/skills/${encodeURIComponent(name)}`, { method: 'DELETE' })

export const searchSkills = (q: string) =>
  jsonFetch<{ results: SearchSkillItem[] }>(`/api/v1/skills/search?q=${encodeURIComponent(q)}`)

// --- Messages ---
export const listMessages = (id: string) =>
  jsonFetch<{ messages: Message[] }>(`/api/v1/sessions/${id}/messages`)

// --- Archive ---
export const archiveSession = (id: string) =>
  jsonFetch<{ ok: boolean }>(`/api/v1/sessions/${encodeURIComponent(id)}/archive`, { method: 'POST' })

export const unarchiveSession = (id: string) =>
  jsonFetch<{ ok: boolean }>(`/api/v1/sessions/${encodeURIComponent(id)}/unarchive`, { method: 'POST' })

export const listArchived = () =>
  jsonFetch<{ sessions: Session[] }>('/api/v1/sessions/archived')

export const permanentDeleteSession = (id: string) =>
  jsonFetch<{ deleted: string }>(`/api/v1/sessions/${encodeURIComponent(id)}/permanent`, { method: 'DELETE' })

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
  identity: string
  soul: string
}

export const getStyles = () => jsonFetch<{ styles: StyleInfo[] }>('/api/v1/styles')

export const getStyle = (id: string) =>
  jsonFetch<StyleDetail>(`/api/v1/styles/${encodeURIComponent(id)}`)

export interface CreateStyleRequest {
  id: string
  label: string
  identity: string
  soul: string
}

export const createStyle = (req: CreateStyleRequest) =>
  jsonFetch<StyleInfo>('/api/v1/styles', {
    method: 'POST',
    body: JSON.stringify(req),
  })

export interface UpdateStyleRequest {
  label?: string
  identity?: string
  soul?: string
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

// --- Streaming send ---
export interface InlineAttachment {
  // 'image_url' for image parts, 'text' for file bodies.
  type: 'image_url' | 'text'
  // For image_url: the data: URL (e.g. "data:image/png;base64,...")
  // carrying the inline file bytes. For text: undefined (the
  // text body is in `text`).
  url?: string
  // For text: the file body. For image_url: undefined.
  text?: string
  // Original filename, kept around for the chat bubble label and
  // for the backend's debug logs.
  name: string
  // 'image' | 'audio' | 'text' | 'file'
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
}

export interface StreamEvent {
  type?: string
  phase?: string
  step?: string
  message?: string
  // Content is a text delta (assistant prose). Populated when
  // type === 'content'. The client appends it to the trailing
  // text part of the assistant message.
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
}

export async function streamMessages(sessionId: string, opts: SendOptions): Promise<void> {
  const res = await fetch(`${BASE}/api/v1/sessions/${sessionId}/messages`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      message: opts.message,
      provider: opts.provider,
      model: opts.model,
      style: opts.style,
      attachments: opts.attachments,
    }),
    signal: opts.signal,
  })
  if (!res.ok || !res.body) {
    const t = await res.text()
    throw new Error(`HTTP ${res.status}: ${t}`)
  }
  const reader = res.body.getReader()
  const decoder = new TextDecoder()
  let buf = ''
  while (true) {
    const { value, done } = await reader.read()
    if (done) break
    buf += decoder.decode(value, { stream: true })
    let idx
    while ((idx = buf.indexOf('\n\n')) >= 0) {
      const chunk = buf.slice(0, idx)
      buf = buf.slice(idx + 2)
      const line = chunk.split('\n').find(l => l.startsWith('data: '))
      if (!line) continue
      const data = line.slice(6).trim()
      if (!data || data === '[DONE]') continue
      try {
        const ev = JSON.parse(data) as StreamEvent
        opts.onEvent(ev)
      } catch {
        // ignore malformed event
      }
    }
  }
}
