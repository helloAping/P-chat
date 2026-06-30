<script setup lang="ts">
// Slash-command support: when the user types "/" at the start of the
// input (with no leading whitespace), we intercept Enter and dispatch
// the command locally. The catalog is fetched once from
// /api/v1/commands and cached; command name is matched case-
// insensitively. Unknown commands fall through to the normal send
// path so the LLM can answer "what is /foo?" questions naturally.

import { onMounted, ref, computed, watch, nextTick } from 'vue'
import { NInput, NButton, NSpace, NScrollbar, useMessage } from 'naive-ui'
import CommandPalette, { type CmdSpec } from './CommandPalette.vue'
import * as api from '../api/client'
import {
  state, currentMeta, currentAttachments, addAttachment, removeAttachment, clearAttachments,
  isStreaming, startStream, stopStream, appendStreamEvent, endStream,
  switchSession, renameSession, createSession, deleteSessionById,
  currentMessages, appendSystemMessage, loadProviders,
} from '../stores/chat'

const inputEl = ref<HTMLTextAreaElement | null>(null)
const inputText = ref('')

// Textarea auto-resize: grow with content, cap at 4 lines, then scroll.
const TEXTAREA_MAX_LINES = 4
function resizeTextarea() {
  const el = inputEl.value
  if (!el) return
  el.style.height = 'auto'
  const lineHeight = parseFloat(getComputedStyle(el).lineHeight)
  const paddingTop = parseFloat(getComputedStyle(el).paddingTop)
  const paddingBottom = parseFloat(getComputedStyle(el).paddingBottom)
  const maxH = lineHeight * TEXTAREA_MAX_LINES + paddingTop + paddingBottom
  const scrollH = el.scrollHeight
  el.style.height = Math.min(scrollH, maxH) + 'px'
  el.style.overflowY = scrollH > maxH ? 'auto' : 'hidden'
}

watch(inputText, () => nextTick(resizeTextarea))
watch(() => currentAttachments.value.length, () => nextTick(resizeTextarea))

// Also sync after backspace / clear (send resets inputText to '').
onMounted(() => nextTick(resizeTextarea))
const sending = ref(false)
const showSessionConfig = ref(false)
const message = useMessage()
const fileInput = ref<HTMLInputElement | null>(null)
const reasoningEffort = computed({
  get: () => {
    if (!state.currentID) return 'off'
    return state.sessionMeta[state.currentID]?.reasoning_effort || 'off'
  },
  set: async (val: string) => {
    if (!state.currentID) return
    state.sessionMeta[state.currentID] = {
      ...state.sessionMeta[state.currentID],
      reasoning_effort: val,
    }
    try { await api.setReasoningEffort(state.currentID, val) } catch {}
  },
})

const reasoningEffortOptions = [
  {
    type: 'group' as const,
    label: '推理',
    children: [
      { label: '关闭', value: 'off' },
      { label: '低', value: 'low' },
      { label: '中', value: 'medium' },
      { label: '高', value: 'high' },
      { label: '最高', value: 'max' },
    ],
  },
]

const planMode = computed(() => {
  if (!state.currentID) return false
  return state.sessionMeta[state.currentID]?.plan_mode || false
})

async function togglePlanMode() {
  if (!state.currentID) return
  const next = !planMode.value
  try {
    await api.updateSessionMeta(state.currentID, { plan_mode: next })
    state.sessionMeta[state.currentID] = {
      ...state.sessionMeta[state.currentID],
      plan_mode: next,
    }
  } catch {}
}

const permissionLevel = computed(() => {
  if (!state.currentID) return 'ask'
  return state.sessionMeta[state.currentID]?.permission_level || 'ask'
})

const permissionOptions = [
  { label: '🔒 始终询问', value: 'ask' },
  { label: '🔓 替我审批', value: 'auto' },
  { label: '🔑 完全访问', value: 'full' },
]

async function onChangePermissionLevel(val: string) {
  if (!state.currentID) return
  try {
    await api.updateSessionMeta(state.currentID, { permission_level: val })
    state.sessionMeta[state.currentID] = {
      ...state.sessionMeta[state.currentID],
      permission_level: val,
    }
  } catch {}
}

// CmdSpec is imported from CommandPalette.vue

const commandList = ref<CmdSpec[]>([])
const skillCommands = ref<CmdSpec[]>([])
let pendingSkillContext = ''

// Merge local commands that aren't in the server list.
const LOCAL_COMMANDS: CmdSpec[] = [
  { name: 'help', description: '显示可用命令列表', group: 'info', web_safe: true },
  { name: 'new', description: '开启新对话', group: 'session', web_safe: true },
  { name: 'clear', description: '清空当前对话视图', group: 'session', web_safe: true },
  { name: 'rename', description: '重命名当前会话', args: '<标题>', group: 'session', web_safe: true },
  { name: 'forget', description: '删除当前会话', group: 'session', web_safe: true },
  { name: 'compress', description: '压缩对话历史(LLM摘要)', group: 'session', web_safe: true },
]

const allCommands = computed(() => {
  const localNames = new Set(LOCAL_COMMANDS.map(c => c.name))
  const merged = [...LOCAL_COMMANDS]
  for (const c of commandList.value) {
    if (!localNames.has(c.name)) merged.push(c)
  }
  for (const c of skillCommands.value) {
    if (!localNames.has(c.name)) merged.push(c)
  }
  return merged
})

onMounted(async () => {
  try {
    const r = await api.listCommands()
    commandList.value = r.commands || []
  } catch {
    // Non-fatal: slash palette will simply be empty.
  }
  loadSkillCommands()
})

async function loadSkillCommands() {
  try {
    const r = await api.listSkills()
    skillCommands.value = (r.skills || []).map(s => ({
      name: s.name,
      description: s.description || '加载技能上下文',
      group: 'skill' as const,
      web_safe: true,
    }))
  } catch { /* ignore */ }
}

function onPickFiles() {
  fileInput.value?.click()
}

async function onFiles(files: FileList | null) {
  if (!files) return
  for (const f of Array.from(files)) {
    try {
      await addAttachment(f)
    } catch (e: any) {
      message.error(`上传失败: ${e.message}`)
    }
  }
  if (fileInput.value) fileInput.value.value = ''
}

function onPaste(e: ClipboardEvent) {
  const items = e.clipboardData?.items
  if (!items) return
  for (const it of Array.from(items)) {
    if (it.kind === 'file' && it.type.startsWith('image/')) {
      const f = it.getAsFile()
      if (f) {
        e.preventDefault()
        const name = f.name && f.name !== 'image.png' ? f.name : `clipboard-${Date.now()}.png`
        const renamed = new File([f], name, { type: f.type })
        onFiles({ 0: renamed, length: 1, item: () => renamed } as unknown as FileList)
      }
    }
  }
}

// --- Slash command palette ---

const showPalette = ref(false)
const paletteFilter = ref('')
const paletteIndex = ref(0)

// Watch input: show palette when user types / at start of line.
watch(inputText, () => {
  const m = inputText.value.match(/^\s*\/(\S*)$/)
  if (m) {
    showPalette.value = true
    paletteFilter.value = m[1]
    paletteIndex.value = 0
  } else {
    showPalette.value = false
  }
})

// Position the palette above the textarea in viewport coords.
const paletteStyle = computed(() => {
  const el = inputEl.value
  if (!el) return {}
  const r = el.getBoundingClientRect()
  return {
    position: 'fixed',
    left: r.left + 'px',
    width: r.width + 'px',
    bottom: (window.innerHeight - r.top + 4) + 'px',
    maxHeight: Math.min(240, r.top - 12) + 'px',
  }
})

function onSelectCommand(c: CmdSpec) {
  inputText.value = '/' + c.name + ' '
  showPalette.value = false
  inputEl.value?.focus()
}

function onPaletteKeyDown(e: KeyboardEvent) {
  if (!showPalette.value) return
  const total = allCommands.value.filter(c =>
    !paletteFilter.value ||
    c.name.toLowerCase().includes(paletteFilter.value.toLowerCase()) ||
    c.description.toLowerCase().includes(paletteFilter.value.toLowerCase()),
  ).length
  if (total === 0) return
  if (e.key === 'ArrowDown') {
    e.preventDefault()
    paletteIndex.value = (paletteIndex.value + 1) % total
  } else if (e.key === 'ArrowUp') {
    e.preventDefault()
    paletteIndex.value = (paletteIndex.value - 1 + total) % total
  } else if (e.key === 'Enter' && showPalette.value) {
    e.preventDefault()
    const filtered = allCommands.value.filter(c =>
      !paletteFilter.value ||
      c.name.toLowerCase().includes(paletteFilter.value.toLowerCase()) ||
      c.description.toLowerCase().includes(paletteFilter.value.toLowerCase()),
    )
    if (filtered.length > 0 && paletteIndex.value < filtered.length) {
      onSelectCommand(filtered[paletteIndex.value])
    }
  } else if (e.key === 'Escape') {
    showPalette.value = false
  }
}

function isSlashLine() {
  // Only treat the line as a command if "/" is the first non-ws char.
  return /^\s*\//.test(inputText.value)
}

function parseSlashLine(): { name: string; args: string } | null {
  const m = inputText.value.match(/^\s*\/([A-Za-z0-9_]+)\s*([\s\S]*)$/)
  if (!m) return null
  return { name: m[1].toLowerCase(), args: m[2].trim() }
}

async function runSlash(name: string, args: string): Promise<boolean> {
  // Built-in local commands first; everything else is forwarded to
  // /api/v1/commands/:name and rendered as a system-style bubble.
  switch (name) {
    case 'help':
    case '?':
      appendSystemMessage(renderHelp())
      return true
    case 'provider':
    case 'providers':
      appendSystemMessage(await renderProviders())
      return true
    case 'model':
    case 'models':
      appendSystemMessage(await renderModels(args))
      return true
    case 'style':
    case 'styles':
      appendSystemMessage(await renderStyles())
      return true
    case 'skills':
      appendSystemMessage(await renderSkills())
      return true
    case 'config':
      appendSystemMessage(await renderConfig())
      return true
    case 'clear': {
      if (state.currentID) {
        try {
          await api.clearSessionMessages(state.currentID)
          state.sessionMessages[state.currentID] = []
          state.sessionTodos = {}
          appendSystemMessage('已清空当前对话历史。')
        } catch (e: any) {
          appendSystemMessage(`清空失败: ${e.message}`)
        }
      }
      return true
    }
    case 'new':
    case 'newchat': {
      await createSession()
      appendSystemMessage('已开启新对话。')
      return true
    }
    case 'forget':
    case 'delete': {
      if (state.currentID) {
        await deleteSessionById(state.currentID)
        appendSystemMessage('当前会话已删除。')
      }
      return true
    }
    case 'rename': {
      if (state.currentID && args) {
        await renameSession(state.currentID, args)
        appendSystemMessage(`已重命名为: ${args}`)
      } else {
        appendSystemMessage('用法: /rename <新标题>')
      }
      return true
    }
    case 'compress': {
      if (state.currentID) {
        const id = state.currentID
        const msgIndex = pushLoadingMessage('⏳ 正在压缩对话历史…')
        try {
          const r = await api.compressConversation(id)
          removeMessage(id, msgIndex)
          if (r.compressed) {
            pushAssistantMessage(id, `## 📋 对话压缩摘要\n\n${r.summary}`)
          } else {
            appendSystemMessage('对话消息数未达阈值，无需压缩。')
          }
        } catch (e: any) {
          removeMessage(id, msgIndex)
          appendSystemMessage(`压缩失败: ${e.message}`)
        }
      }
      return true
    }
  }
  // Fall back to the server's command endpoint for anything else.
  try {
    const r = await api.runCommand(name, args)
    appendSystemMessage(r.output || '(无输出)')
    return true
  } catch (e: any) {
    appendSystemMessage(`命令 /${name} 执行失败: ${e.message}`)
    return true
  }
}

function renderHelp(): string {
  const groups: Record<string, { name: string; args?: string; description: string }[]> = {}
  for (const c of allCommands.value) {
    const g = c.group || 'other'
    if (!groups[g]) groups[g] = []
    groups[g].push({ name: c.name, args: c.args, description: c.description })
  }
  const groupLabels: Record<string, string> = {
    session: '会话', info: '信息', config: '配置', skill: '技能', other: '其他',
  }
  let html = '<div class="cmd-help">'
  for (const [g, items] of Object.entries(groups)) {
    html += `<div class="cmd-group"><div class="cmd-group-label">${groupLabels[g] || g}</div>`
    for (const c of items) {
      const cmd = c.args ? `/${c.name} ${c.args}` : `/${c.name}`
      html += `<div class="cmd-row"><code class="cmd-name">${cmd}</code><span class="cmd-desc">${c.description}</span></div>`
    }
    html += '</div>'
  }
  html += '</div>'
  return html
}

async function renderProviders(): Promise<string> {
  try {
    const r = await api.listProviders()
    const ps = r.providers || []
    if (!ps.length) return '<div class="cmd-info">暂无已配置的提供商。</div>'
    let html = '<div class="cmd-providers">'
    for (const p of ps) {
      const modelCount = (p.models && p.models.length) ? `${p.models.length} 个模型` : ''
      const defTag = p.is_default ? ' <span class="cmd-tag tag-def">默认</span>' : ''
      html += `<div class="cmd-card">
        <div class="cmd-card-head"><strong>${p.name}</strong>${defTag}<span class="cmd-tag tag-prot">${p.protocol}</span></div>
        <div class="cmd-card-meta">${p.base_url || ''}${modelCount ? ' · ' + modelCount : ''}</div>
      </div>`
    }
    html += '</div>'
    return html
  } catch (e: any) {
    return `获取提供商信息失败: ${e.message}`
  }
}

async function renderModels(args: string): Promise<string> {
  try {
    const r = await api.listProviders()
    const ps = r.providers || []
    if (!ps.length) return '<div class="cmd-info">暂无已配置的提供商。使用 /provider 查看。</div>'
    let html = '<div class="cmd-models">'
    for (const p of ps) {
      const models = p.models && p.models.length ? p.models : []
      html += `<div class="cmd-section"><div class="cmd-section-label">${p.name}</div>`
      if (!models.length) {
        html += `<div class="cmd-item">${p.model || '(默认模型未设置)'}</div>`
      } else {
        for (const m of models) {
          const defTag = m.default ? ' <span class="cmd-tag tag-def">★</span>' : ''
          const visionTag = m.capabilities?.supports_vision ? ' <span class="cmd-tag tag-vis">视觉</span>' : ''
          const ctx = m.max_tokens_context ? `${fmtK(m.max_tokens_context)} ctx` : ''
          const out = m.max_tokens_output ? `${fmtK(m.max_tokens_output)} out` : ''
          const meta = [ctx, out].filter(Boolean).join(' · ')
          html += `<div class="cmd-item">${m.name}${defTag}${visionTag}${meta ? ` <span class="cmd-muted">${meta}</span>` : ''}</div>`
        }
      }
      html += '</div>'
    }
    html += '</div>'
    return html
  } catch (e: any) {
    return `获取模型列表失败: ${e.message}`
  }
}

async function renderStyles(): Promise<string> {
  try {
    const r = await api.getStyles()
    const styles = r.styles || []
    if (!styles.length) return '<div class="cmd-info">暂无风格配置。</div>'
    let html = '<div class="cmd-styles">'
    for (const s of styles) {
      html += `<div class="cmd-card">
        <div class="cmd-card-head"><strong>${s.label}</strong> <span class="cmd-muted">${s.id}</span></div>
        <div class="cmd-card-meta">${s.desc || ''}</div>
      </div>`
    }
    html += '</div>'
    return html
  } catch (e: any) {
    return `获取风格列表失败: ${e.message}`
  }
}

async function renderSkills(): Promise<string> {
  try {
    const r = await api.listSkills()
    const skills = r.skills || []
    if (!skills.length) return '<div class="cmd-info">暂无已安装的技能。使用 /skills 搜索安装。</div>'
    let html = '<div class="cmd-skills">'
    for (const s of skills) {
      html += `<div class="cmd-card">
        <div class="cmd-card-head"><strong>/${s.name}</strong></div>
        <div class="cmd-card-meta">${s.description || ''}</div>
      </div>`
    }
    html += '</div>'
    return html
  } catch (e: any) {
    return `获取技能列表失败: ${e.message}`
  }
}

async function renderConfig(): Promise<string> {
  try {
    const r = await api.listProviders()
    const ps = r.providers || []
    let html = '<div class="cmd-config">'
    html += `<div class="cmd-section"><div class="cmd-section-label">已配置提供商 (${ps.length})</div>`
    for (const p of ps) {
      const model = p.models && p.models.length ? p.models.find(m => m.default)?.name || p.models[0]?.name : p.model
      html += `<div class="cmd-item"><strong>${p.name}</strong> <span class="cmd-tag tag-prot">${p.protocol}</span> → ${model || '—'}</div>`
    }
    html += '</div></div>'
    return html
  } catch (e: any) {
    return `获取配置信息失败: ${e.message}`
  }
}

function fmtK(n: number): string {
  if (n >= 1000) return (n / 1000).toFixed(0) + 'k'
  return String(n)
}

function pushLoadingMessage(text: string): number {
  const id = state.currentID
  if (!id) return -1
  if (!state.sessionMessages[id]) state.sessionMessages[id] = []
  const idx = state.sessionMessages[id].length
  state.sessionMessages[id].push({ role: 'system', content: text } as any)
  return idx
}

function removeMessage(sessionId: string, index: number) {
  const msgs = state.sessionMessages[sessionId]
  if (msgs && index >= 0 && index < msgs.length) {
    msgs.splice(index, 1)
  }
}

function pushAssistantMessage(sessionId: string, content: string) {
  if (!state.sessionMessages[sessionId]) state.sessionMessages[sessionId] = []
  state.sessionMessages[sessionId].push({ role: 'assistant', content, parts: [] })
}

async function send() {
  const raw = inputText.value.trim()
  if (!raw) return
  // NOTE: we intentionally do NOT gate on `sending.value`
  // here. That ref is local to this InputArea instance, but
  // multiple conversations can stream in parallel. If session
  // A is mid-stream, `sending` is true; the user switching to
  // session B (which is not streaming) should still be able to
  // send. The send/stop button is already gated on
  // `isStreaming` (per-session), so double-clicks within the
  // same session are already impossible.

  if (isSlashLine()) {
    const parsed = parseSlashLine()
    if (parsed) {
      // Skill commands: load content, merge with user args,
      // then send as a single message to the LLM.
      if (skillCommands.value.some(c => c.name === parsed.name)) {
        sending.value = true
        try {
          const r = await api.getSkill(parsed.name)
          const skillContent = r.skill.content || ''
          const userInput = parsed.args || ''
          pendingSkillContext = skillContent
          // Show a clean system note instead of dumping skill content.
          appendSystemMessage(`已激活技能「${parsed.name}」` + (userInput ? `: ${userInput}` : ''))
          // Use only the user's input as the visible message.
          inputText.value = userInput || `请根据技能「${parsed.name}」的内容提供帮助`
          sending.value = false
          // Fall through to normal send below.
        } catch (e: any) {
          appendSystemMessage(`技能 /${parsed.name} 加载失败: ${e.message}`)
          sending.value = false
          inputText.value = ''
          return
        }
      } else {
        inputText.value = ''
        sending.value = true
        try { await runSlash(parsed.name, parsed.args) }
        finally { sending.value = false }
        return
      }
    } else {
      inputText.value = ''
      return
    }
  }

  const text = inputText.value.trim()
  inputText.value = ''
  if (!text) return

  if (!state.currentID) {
    // Use the store's createSession() — it both creates the
    // session on the server AND inserts it into state.sessions
    // so switchSession() can find it for picker hydration.
    // The previous bare api.createSession() call bypassed the
    // store and left the new session invisible to currentMeta,
    // which manifested as "model selection empty after first
    // message" in the chat NSelect.
    await createSession()
  }
  const id = state.currentID
  const meta = currentMeta.value
  // Build the attachment payload in two directions at once:
  //   - inlineAttachments: the data URLs/text bodies that we
  //     send to the server (so the message is self-contained and
  //     the LLM gets bytes without another disk read).
  //   - bubbleAttachments: the same data shaped for the chat
  //     bubble (the data URL goes into `url`, the original file
  //     name into `name`) so the user sees the image right
  //     away, not after a server round-trip.
  //
  // Attachments are read from the *current* session's pending
  // list — per-session storage means staging files in one
  // conversation doesn't leak into another when the user
  // switches.
  const inlineAttachments: api.InlineAttachment[] = []
  const bubbleAttachments: api.InlineAttachment[] = []
  for (const a of currentAttachments.value) {
    if (a._error) continue
    const data = a._dataURL
    if (!data) continue
    if (a.kind === 'image') {
      inlineAttachments.push({ type: 'image_url', url: data, name: a.name, kind: a.kind, mime: a.mime })
      bubbleAttachments.push({ type: 'image_url', url: data, name: a.name, kind: a.kind, mime: a.mime })
    } else if (a.kind === 'audio' || a.kind === 'video') {
      // Audio and video ride the same wire path as images:
      // base64 data URL on a *_url attachment type. The LLM
      // can't actually hear/watch them today (no native
      // adapter), but the chat bubble renders a player.
      const wire = a.kind === 'audio' ? 'audio_url' : 'video_url'
      inlineAttachments.push({ type: wire, url: data, name: a.name, kind: a.kind, mime: a.mime })
      bubbleAttachments.push({ type: wire, url: data, name: a.name, kind: a.kind, mime: a.mime })
    } else {
      inlineAttachments.push({ type: 'text', text: data, name: a.name, kind: a.kind, mime: a.mime })
      bubbleAttachments.push({ type: 'text', text: data, name: a.name, kind: a.kind, mime: a.mime })
    }
  }
  if (!state.sessionMessages[id]) state.sessionMessages[id] = []
  // Push the user message WITH attachments so the bubble
  // renders correctly without waiting for the next history
  // fetch.
  state.sessionMessages[id].push({ role: 'user', content: text, attachments: bubbleAttachments.length ? bubbleAttachments : undefined })
  if (!meta.title) {
    api.renameSession(id, text.slice(0, 40)).then(() => {
      const s = state.sessions.find(s => s.id === id)
      if (s) s.title = text.slice(0, 40)
    }).catch(() => {})
  }
  inputText.value = ''
  clearAttachments()
  sending.value = true
  const ctrl = new AbortController()
  // Install the placeholder assistant message immediately so
  // the three-bouncing-dots spinner is reachable. The actual
  // mutation happens in the onEvent callback below.
  startStream(id, ctrl)
  try {
    await api.streamMessages(id, {
      message: text,
      provider: meta.provider,
      model: meta.model,
      style: meta.style,
      attachments: inlineAttachments,
      signal: ctrl.signal,
      skill_context: pendingSkillContext || undefined,
      onEvent: (ev) => {
        pendingSkillContext = ''
        // Surface top-level errors (auth, network) as
        // toast notifications. Per-event errors
        // (e.g. tool execution failure) flow through
        // appendStreamEvent and are rendered inline.
        // Errors with a suggestion get a longer-duration
        // toast so the user has time to read the fix
        // hint (especially the vision_unsupported case
        // where the toast is the first place they see
        // the actionable advice).
        if (ev.type === 'error' && ev.error) {
          if (ev.suggestion) {
            message.error(`${ev.error}\n${ev.suggestion}`, { duration: 8000 })
          } else {
            message.error(ev.error)
          }
          // Still call appendStreamEvent so the error text
          // renders inline in the assistant bubble — the
          // user sees context for the failure.
        }
        appendStreamEvent(id, ev)
      },
    })
  } catch (e: any) {
    if (e.name !== 'AbortError') {
      message.error(`发送失败: ${e.message}`)
    }
  } finally {
    endStream(id)
    sending.value = false
  }
}

function stop() {
  if (state.currentID) stopStream(state.currentID)
  sending.value = false
}

function onKeyDown(e: KeyboardEvent) {
  // Palette navigation takes priority when open.
  if (showPalette.value && (e.key === 'ArrowDown' || e.key === 'ArrowUp' || e.key === 'Enter' || e.key === 'Escape')) {
    onPaletteKeyDown(e)
    return
  }
  if (e.key === 'Enter' && !e.shiftKey) {
    e.preventDefault()
    send()
  } else if (e.key === 'Escape') {
    stop()
  }
}

const isDragging = ref(false)
function onDragOver(e: DragEvent) {
  if (e.dataTransfer?.types && Array.from(e.dataTransfer.types).includes('Files')) {
    e.preventDefault()
    isDragging.value = true
  }
}
function onDragLeave() { isDragging.value = false }
function onDrop(e: DragEvent) {
  e.preventDefault()
  isDragging.value = false
  onFiles(e.dataTransfer?.files || null)
}

onMounted(() => {
  inputEl.value?.focus()
})

// --- Inline session config (model picker, grouped by provider) ---

// `modelOptions` is the grouped NSelect option list. The shape is:
//
//   [
//     { type: 'group', label: 'openai', children: [
//       { label: 'gpt-4o · 👁',           value: 'openai::gpt-4o' },
//       { label: 'gpt-4o-mini',           value: 'openai::gpt-4o-mini' },
//     ]},
//     { type: 'group', label: 'cs', children: [
//       { label: 'doubao-seed-2.0-lite',  value: 'cs::doubao-seed-2.0-lite' },
//       ...
//     ]},
//   ]
//
// The encoded value ("<provider>::<model>") lets one NSelect drive
// both the active provider and the active model in a single
// onChange — which is what makes the "merged" picker feel native.
interface ModelOption {
  // Single-line label: display_name (preferred) or raw model
  // id. The dropdown is intentionally simple — one row, one
  // model — so the user can scan a long list of models
  // without having to parse a noisy 2-line layout per row.
  // The default-model marker is shown as a small ⭐ suffix
  // in the label so it's visible at a glance.
  label: string
  value: string
}
interface GroupOption {
  type: 'group'
  label: string
  children: ModelOption[]
}
type SelectOption = ModelOption | GroupOption

// Use the store-level providers list (loaded once at app
// boot) so the chat NSelect can read the same model list
// the picker on the right of the input shows. We keep
// modelOptions as a local computed off state.providers.
const modelOptions = computed<SelectOption[]>(() => {
  const groups: GroupOption[] = []
  for (const p of state.providers as any[]) {
    const ms: ModelOption[] = []
    for (const m of (p.models || [])) {
      const name = m.name
      const primary = m.display_name || name
      const label = m.default ? `⭐ ${primary}` : primary
      ms.push({ label, value: `${p.name}::${name}` })
    }
    if (ms.length === 0 && p.model) {
      ms.push({ label: p.model, value: `${p.name}::${p.model}` })
    }
    if (ms.length > 0) {
      groups.push({ type: 'group', label: p.name, children: ms })
    }
  }
  return groups
})

const styleOptions = ref<{ label: string; value: string }[]>([])

async function loadConfig() {
  try {
    // Providers live in the store so other components (the
    // chat NSelect, the AppSettingsModal) all read from one
    // source of truth. loadProviders() is idempotent.
    await loadProviders()
    // Styles are still local to this component; only the
    // chat input shows them.
    const st = await api.getStyles()
    styleOptions.value = (st.styles || []).map((x: any) => ({
      label: x.label || x.id,
      value: x.id,
    }))
  } catch { /* ignore */ }
}

// The model picker uses a flat single-line label per option.
// No custom render-function is needed — the default NSelect
// label rendering handles both the dropdown row and the
// closed selected-pill display, and both show the same
// `display_name` (or raw `name`) with a ⭐ prefix for the
// default model.

function currentSelection(): string {
  const m = currentMeta.value
  if (!m.provider) return ''
  if (!m.model) return `${m.provider}::`
  return `${m.provider}::${m.model}`
}

async function onModelPick(v: string) {
  if (!state.currentID || !v) return
  // v is "<provider>::<model>" — split and persist both.
  const idx = v.indexOf('::')
  const provider = idx >= 0 ? v.slice(0, idx) : v
  const model = idx >= 0 ? v.slice(idx + 2) : ''
  const resp = await api.updateSessionMeta(state.currentID, { provider, model })
  // The server returns the resolved session (including any
  // fallback model it picked), so we sync the local cache from
  // it instead of trusting the body we just sent.
  const id = state.currentID
  state.sessionMeta[id] = {
    ...(state.sessionMeta[id] || currentMeta.value),
    provider: resp.provider ?? provider,
    model: resp.model ?? model,
  }
}

async function onStylePick(v: string) {
  if (!state.currentID || !v) return
  const resp = await api.updateSessionMeta(state.currentID, { style: v })
  const id = state.currentID
  state.sessionMeta[id] = {
    ...(state.sessionMeta[id] || currentMeta.value),
    style: resp.style ?? v,
  }
}

const currentSelectionValue = computed({
  get: () => currentSelection(),
  set: (v: string) => onModelPick(v),
})
const currentStyleValue = computed({
  get: () => currentMeta.value.style || 'tech',
  set: (v: string) => onStylePick(v),
})

// Load the model/style lists once on mount so the two dropdowns
// are populated even before the user opens any session.
onMounted(() => {
  loadConfig()
})
</script>

<template>
  <div class="input-area">
    <!-- Attachments live INSIDE the same input-wrap as the
         textarea. They wrap to multiple rows as the dialog
         width changes; we don't use a horizontal scrollbar
         because wrap gives a more predictable layout and
         removes the "list floating above the dialog with a big
         gap" the user reported. The wrap inherits the wrap's
         padding so the chips look "stitched on" to the dialog
         rather than free-floating. -->
    <div
      class="input-wrap"
      :class="{
        dragover: isDragging,
        'has-attachments': currentAttachments.length > 0,
      }"
      @dragover="onDragOver"
      @dragleave="onDragLeave"
      @drop="onDrop"
    >
      <div v-if="currentAttachments.length > 0" class="attach-strip">
        <div
          v-for="(a, i) in currentAttachments"
          :key="i"
          class="attach-chip"
          :class="{ uploading: a._uploading, error: a._error }"
        >
          <div class="thumb">
            <img v-if="a.kind === 'image'" :src="a._previewURL" :alt="a.name" />
            <video v-else-if="a.kind === 'video'" :src="a._previewURL" muted preload="metadata" />
            <span v-else-if="a.kind === 'audio'">🔊</span>
            <span v-else-if="a.kind === 'text'">📝</span>
            <span v-else>📄</span>
          </div>
          <span class="name" :title="a.name">{{ a.name }}</span>
          <button class="rm" @click="removeAttachment(i)" title="移除">×</button>
        </div>
      </div>
      <div class="input-row">
        <button class="attach-icon-btn" @click="onPickFiles" title="添加附件(支持拖拽、剪贴板粘贴)">📎</button>
        <textarea
          ref="inputEl"
          v-model="inputText"
          class="textarea"
          rows="1"
          :placeholder="isSlashLine() ? '输入 / 后跟命令 (例如 /help)' : '输入消息，Enter 发送，Shift+Enter 换行，Esc 停止，/ 前缀是命令'"
          @keydown="onKeyDown"
          @paste="onPaste"
        ></textarea>
        <NSelect
          v-model:value="currentStyleValue"
          :options="styleOptions"
          size="tiny"
          :disabled="!state.currentID"
          class="style-pick"
          title="选择人格风格"
          placeholder="风格"
        />
        <button
          v-if="!isStreaming"
          class="send-btn"
          :disabled="!inputText.trim()"
          @click="send"
          title="发送 (Enter)"
        >➤</button>
        <button
          v-else
          class="stop-btn"
          @click="stop"
          title="停止 (Esc)"
        >■</button>
      </div>
    </div>
    <input
      ref="fileInput"
      type="file"
      multiple
      style="display:none"
      accept="image/*,audio/*,video/*,text/*,.pdf,.json,.md,.txt,.csv,.yaml,.yml,.go,.py,.js,.ts"
      @change="onFiles(($event.target as HTMLInputElement).files)"
    />

    <!-- Bottom row: model picker + quick settings + hints -->
    <div class="input-bottom">
      <div class="input-main">
        <NSelect
          v-model:value="currentSelectionValue"
          :options="modelOptions"
          size="small"
          :disabled="!state.currentID"
          class="picker picker-wide"
          title="选择模型 (按提供商分组; ⭐ = 默认)"
          placeholder="选择模型"
        />
      </div>
      <div class="input-secondary">
        <NSelect
          v-model:value="reasoningEffort"
          :options="reasoningEffortOptions"
          size="small"
          class="picker picker-narrow"
          title="推理等级 (off/low/medium/high/max)"
          placeholder="推理"
        />
        <NButton
          size="small"
          :type="planMode ? 'primary' : 'default'"
          :disabled="!state.currentID"
          @click="togglePlanMode"
          title="切换计划/构建模式"
        >{{ planMode ? '📋 计划' : '🔨 构建' }}</NButton>
        <NSelect
          v-model:value="permissionLevel"
          :options="permissionOptions"
          size="small"
          :disabled="!state.currentID"
          class="picker picker-perm"
          title="权限级别"
          @update:value="onChangePermissionLevel"
        />
        <div class="hints">
          <span><kbd>Enter</kbd> 发送</span>
          <span><kbd>Shift</kbd>+<kbd>Enter</kbd> 换行</span>
          <span><kbd>Esc</kbd> 停止</span>
        </div>
      </div>
    </div>
  </div>
  <!-- Teleported to body to avoid clipping by .input-area overflow -->
  <Teleport to="body">
    <CommandPalette
      v-if="showPalette"
      :commands="allCommands"
      :filter="paletteFilter"
      :selected-index="paletteIndex"
      :style="paletteStyle"
      @select="onSelectCommand"
    />
  </Teleport>
</template>

<style scoped>
/* Attachment strip lives inside the same rounded box as the
 * textarea so the chips appear "stitched" to the dialog with
 * no visible gap. Chips wrap to multiple rows as the dialog
 * width changes; the parent container grows to fit. */
.attach-strip {
  display: flex;
  flex-wrap: wrap;
  gap: 4px;
  /* Small padding so the chips don't touch the box's top
   * border. The bottom padding is provided by the textarea
   * margin so the strip feels joined to it. */
  padding: 6px 0 4px 0;
  /* Hard cap on the vertical area the chips can occupy. When
   * this is exceeded the wrap scrolls rather than pushing the
   * whole input area out of the viewport. */
  max-height: 96px;
  overflow-y: auto;
  overflow-x: hidden;
  align-content: flex-start;
}
.attach-chip {
  display: inline-flex; align-items: center; gap: 4px;
  background: var(--bg-3);
  border: 1px solid var(--border-2);
  border-radius: 6px;
  padding: 2px 4px 2px 4px;
  font-size: 12px;
  flex: 0 0 auto;
  max-width: 100%;
  min-width: 0;
}
.attach-chip.uploading { opacity: 0.6; }
.attach-chip.error { border-color: var(--error); }
.thumb {
  width: 22px; height: 22px;
  background: var(--bg-2);
  border-radius: 4px;
  display: flex; align-items: center; justify-content: center;
  overflow: hidden;
  font-size: 14px;
  flex-shrink: 0;
}
.thumb img { width: 100%; height: 100%; object-fit: cover; }
.name { max-width: 100px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.rm {
  background: none; border: none; color: var(--text-3);
  cursor: pointer; padding: 0 4px; font-size: 14px; line-height: 1;
  flex-shrink: 0;
}
.rm:hover { color: var(--error); }

/* The dialog "box": textarea + optional attach strip share a
 * single rounded container. The flex column lets the strip
 * sit on top of the textarea with no gap (the strip's bottom
 * padding + textarea's top margin provides the spacing, and
 * the box's outer border wraps both). */
.input-wrap {
  background: var(--bg-input);
  border: 1px solid var(--border-2);
  border-radius: 10px;
  padding: 0 12px 0 12px;
  transition: border-color 0.15s;
  display: flex;
  flex-direction: column;
}
.input-wrap.has-attachments { padding-top: 0; }
.input-wrap:focus-within { border-color: var(--accent); }
.input-wrap.dragover { border-color: var(--accent-2); background: var(--bg-3); }
.input-row {
  display: flex;
  align-items: center;
  gap: 6px;
}
.attach-icon-btn {
  width: 32px; height: 32px;
  border: none; border-radius: 8px;
  background: transparent;
  color: var(--text-3);
  font-size: 16px; cursor: pointer;
  display: flex; align-items: center; justify-content: center;
  flex-shrink: 0;
  transition: color 0.15s, background 0.15s;
}
.attach-icon-btn:hover { color: var(--accent); background: var(--bg-3); }
.textarea {
  background: transparent;
  border: none;
  color: var(--text);
  font-size: 14px;
  outline: none;
  resize: none;
  flex: 1;
  /* Height is managed by resizeTextarea(). */
  font-family: inherit;
  line-height: 1.5;
  margin: 0;
  padding: 8px 0 8px 0;
}
.style-pick {
  --n-border: none !important;
  --n-border-hover: none !important;
  --n-border-focus: none !important;
  --n-box-shadow-focus: none !important;
  flex: 0 0 auto;
  min-width: 72px;
  max-width: 100px;
}
.style-pick :deep(.n-base-selection) {
  background: transparent !important;
  border: none !important;
  box-shadow: none !important;
}
.style-pick :deep(.n-base-selection:hover) {
  background: var(--bg-3) !important;
}
.send-btn, .stop-btn {
  width: 32px; height: 32px;
  border: none; border-radius: 8px;
  font-size: 14px; cursor: pointer;
  display: flex; align-items: center; justify-content: center;
  background: var(--accent); color: var(--on-accent);
  flex-shrink: 0;
  margin-left: 8px;
}
.send-btn:disabled { background: var(--bg-3); color: var(--text-4); cursor: not-allowed; }
.send-btn:hover:not(:disabled) { background: var(--accent-2); }
.stop-btn { background: var(--error); }
.stop-btn:hover { background: var(--error); opacity: 0.85; }

.hints { font-size: 10px; color: var(--text-4); display: flex; gap: 10px; margin-left: auto; white-space: nowrap; }
.hints kbd {
  background: var(--bg-3); border: 1px solid var(--border-2);
  border-radius: 3px; padding: 0 4px; font-family: ui-monospace, Menlo, monospace; font-size: 9px;
}

/* Inline session-config dropdowns (always visible, no toggle). */
.picker {
  min-width: 110px;
  max-width: 150px;
  flex: 0 0 auto;
}
.picker-wide {
  min-width: 170px;
  max-width: 260px;
  flex: 1 1 180px;
}
.picker-narrow {
  min-width: 72px;
  max-width: 100px;
  flex: 0 0 auto;
}
.picker-perm {
  min-width: 120px;
  max-width: 150px;
  flex: 0 0 auto;
}

/* The input area must be height-bounded: with many attachments
 * the attach strip + the textarea + the control row used to
 * grow tall enough to push the chat above it out of the
 * viewport. We split the bottom into a controls row and a
 * separate hints row so the controls can wrap to a second
 * line on narrow windows without bumping the hints. */
.input-area {
  border-top: 1px solid var(--border);
  background: var(--bg-2);
  padding: 8px 12px 8px 12px;
  flex-shrink: 0;
  /* Keep the input area to a sane share of the chat window.
   * Internal scroll (on the attach strip) keeps overflow
   * contained. */
  max-height: 50vh;
  overflow: auto;
}
.input-bottom {
  display: flex;
  align-items: center;
  margin-top: 6px;
  gap: 6px;
}
.input-main {
  display: flex;
  align-items: center;
  gap: 6px;
}
.input-secondary {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-left: auto;
  flex: 1 1 auto;
  min-width: 0;
  justify-content: flex-end;
}
</style>
