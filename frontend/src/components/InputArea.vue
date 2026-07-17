<script setup lang="ts">
// Slash-command support: when the user types "/" at the start of the
// input (with no leading whitespace), we intercept Enter and dispatch
// the command locally. The catalog is fetched once from
// /api/v1/commands and cached; command name is matched case-
// insensitively. Unknown commands fall through to the normal send
// path so the LLM can answer "what is /foo?" questions naturally.

import { onMounted, ref, computed, watch, nextTick } from 'vue'
import { NInput, NButton, NSpace, NScrollbar, NPopover, NDropdown, useMessage } from 'naive-ui'
import CommandPalette, { type CmdSpec } from './CommandPalette.vue'
import ModelPicker from './ModelPicker.vue'
import {
  Paperclip, Send, Square, Clipboard, Volume2, VolumeX, Hammer,
  Undo2, FileText, File, Sparkles, ChevronDown, ChevronUp,
  Lock, Unlock, Key, Database,
} from './icons'
import * as api from '../api/client'
import {
  state, currentMeta, currentAttachments, addAttachment, removeAttachment, clearAttachments,
  isStreaming, startStream, stopStream, appendStreamEvent, endStream,
  switchSession, renameSession, createSession, deleteSessionById,
  currentMessages, appendSystemMessage, loadProviders,
  currentRollbackBanner, currentPendingInput, undoRollback, dismissRollback,
  recoverMissingParts,
} from '../stores/chat'
import { notifyManager } from '../utils/notify'

const inputEl = ref<HTMLTextAreaElement | null>(null)
const inputText = ref('')
// 本地 ref 代理静音状态，确保 Vue 模板能跟踪变化
const mute = ref(notifyManager.mute)

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

// Sync rollback pending input to the textarea.
watch(currentPendingInput, (val) => {
  if (val) {
    inputText.value = val
    nextTick(() => {
      inputEl.value?.focus()
      resizeTextarea()
    })
  }
})

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
  { label: '始终询问', value: 'ask' },
  { label: '替我审批', value: 'auto' },
  { label: '完全访问', value: 'full' },
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

function onToggleMute() {
  notifyManager.unlock()
  notifyManager.mute = !notifyManager.mute
  mute.value = notifyManager.mute
}

// --- knowledge base selector ---
const kbBases = ref<api.KnowledgeBaseItem[]>([])
const kbOptions = computed(() => [
  { label: '知识库 · 不使用', value: '__off__' },
  { label: '知识库 · 全部', value: '__all__' },
  ...kbBases.value.filter(b => b.enabled).map(b => ({ label: `知识库 · ${b.name}`, value: b.name })),
])
const kbBase = computed({
  get: () => {
    if (!state.currentID) return '__off__'
    return state.sessionMeta[state.currentID]?.knowledge_base || '__off__'
  },
  set: async (val: string) => {
    if (!state.currentID) return
    try {
      await api.updateSessionMeta(state.currentID, { knowledge_base: val })
      state.sessionMeta[state.currentID] = {
        ...state.sessionMeta[state.currentID],
        knowledge_base: val,
      }
    } catch {}
  },
})

async function loadKBases() {
  try { kbBases.value = await api.getKnowledgeBases() } catch {}
}

watch(() => state.kbConfigVersion, () => { loadKBases() })

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

// Build a friendly name for a clipboard file item. Chromium
// hands image screenshots back as File{name: 'image.png'} —
// a generic placeholder that would collide if the user
// pastes twice in a row. Real files copied from Explorer
// keep their original name. The rare empty-name case (e.g.
// files dragged into a copy-paste flow that loses the name)
// gets a generic timestamp + extension so downstream code
// can still infer a kind via guessKind(file.type, ...).
//
// The 3-arg File constructor (bits, name, options) lets us
// preserve the mime type through the rename. The naive
// `new File(...)` shadows against the locally-imported
// `File` icon component (see the `./icons` import at the
// top of this file), which is a Vue component, not the DOM
// constructor — so it throws "File is not a constructor"
// at runtime. We grab the DOM constructor off window
// explicitly to dodge the shadow. (This bug was present in
// the original onPaste too — every screenshot paste hit it,
// which is why the "添加附件(支持拖拽、剪贴板粘贴)"
// promise on the paperclip button was never actually
// honoured before this fix.)
function renameClipboardFile(f: File): File {
  let name = f.name
  if (!name || name === 'image.png') {
    const ts = Date.now()
    const ext = f.type.split('/')[1] || ''
    name = name === 'image.png'
      ? `clipboard-${ts}.png`
      : (ext ? `clipboard-${ts}.${ext}` : `clipboard-${ts}`)
  }
  const GlobalFile = (typeof window !== 'undefined' ? window.File : globalThis.File) as any
  return new GlobalFile([f], name, { type: f.type }) as File
}

function onPaste(e: ClipboardEvent) {
  const cd = e.clipboardData
  if (!cd) return
  // Collect every file item from the clipboard, not just
  // images. The previous version filtered on
  // `type.startsWith('image/')`, which meant PDFs/text/
  // audio copied from Explorer either got their file path
  // dropped into the textarea (Chrome's default for a
  // file paste on a non-input target) or were silently
  // dropped — neither matches the "添加附件(支持拖拽、
  // 剪贴板粘贴)" promise on the paperclip button.
  //
  // Text items (`kind === 'string'`) are intentionally
  // skipped so the default text-paste path still feeds
  // the textarea.
  const files: File[] = []
  for (let i = 0; i < cd.items.length; i++) {
    const it = cd.items[i]
    if (it.kind !== 'file') continue
    const f = it.getAsFile()
    if (!f) continue
    files.push(renameClipboardFile(f))
  }
  if (files.length === 0) return
  e.preventDefault()
  // addAttachment() pushes the placeholder to the store
  // synchronously, so the chip below the textarea appears
  // the moment this function returns — the upload +
  // readAsDataURL work happens in the background. Going
  // through addAttachment directly (instead of building a
  // synthetic FileList for onFiles()) keeps the call chain
  // one frame shorter, which matters because the chip's
  // appearance is the user's primary feedback that "the
  // paste worked". Also avoids the synthetic FileList +
  // Array.from round-trip — that path is fine, but the
  // direct call is more obviously correct.
  for (const f of files) {
    addAttachment(f).catch((err: any) => {
      message.error(`上传失败: ${err.message}`)
    })
  }
  // Toast the user. The chip below the textarea is the
  // primary indicator, but a screenshot paste is easy to
  // miss if the user's eye is on the textarea. The toast
  // names the first two files and rolls the rest up into
  // "等 N 个" so a 10-file paste doesn't render a 500-char
  // message.
  const preview = files.slice(0, 2).map(f => f.name).join(', ')
  const more = files.length > 2 ? ` 等 ${files.length} 个` : ''
  message.success(`已添加附件: ${preview}${more}`, { duration: 1800 })
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
  // Mint a row id for this user message at send time so
  // rollback and regenerate always have a valid `msg.id`
  // to target — even when the SSE `done` event never
  // arrives (LLM error, network drop, quota exhausted).
  // Format: Date.now() × 1000 + a 0..999 random suffix.
  // The combined 13-16 digit integer sits well outside
  // the SQLite AUTOINCREMENT range (1, 2, 3, …), so the
  // backend can insert it as the explicit row id without
  // colliding with anything autoincrement produces for
  // assistant messages later in the same session.
  const clientMsgId = Date.now() * 1000 + Math.floor(Math.random() * 1000)
  // Push the user message WITH id + attachments so the
  // bubble renders correctly without waiting for the
  // next history fetch, and so rollback can target the
  // exact row from the moment the message is sent.
  state.sessionMessages[id].push({
    id: clientMsgId,
    role: 'user',
    content: text,
    attachments: bubbleAttachments.length ? bubbleAttachments : undefined,
  })
  // Convert inline base64 data: URLs on user-sent image
  // attachments into blob: URLs. The base64 payload has
  // already been shipped to the server via the
  // `inlineAttachments` wire path (a separate Array that
  // owns the same string references until the stream
  // completes), but the message bubble renders from
  // `bubbleAttachments` — swapping those to blob URLs
  // here means the reactive state holds only a short
  // blob: reference going forward, not a multi-hundred-KB
  // base64 string. The Blob itself stays alive via the
  // `pendingAttachments` `_dataURL` until `clearAttachments`
  // runs in the finally block below.
  for (const att of bubbleAttachments) {
    if (att.url?.startsWith('data:image/')) {
      try {
        const commaIdx = att.url.indexOf(',')
        const b64 = att.url.slice(commaIdx + 1)
        const mime = att.url.slice(5, commaIdx)
        const byteChars = atob(b64)
        const bytes = new Uint8Array(byteChars.length)
        for (let i = 0; i < byteChars.length; i++) bytes[i] = byteChars.charCodeAt(i)
        att.url = URL.createObjectURL(new Blob([bytes], { type: mime }))
      } catch { /* keep original data URL */ }
    }
  }
  if (!meta.title) {
    api.renameSession(id, text.slice(0, 40)).then(() => {
      const s = state.sessions.find(s => s.id === id)
      if (s) s.title = text.slice(0, 40)
    }).catch(() => {})
  }
  inputText.value = ''
  clearAttachments()
  // 首次用户交互时解锁 Web Audio（浏览器自动播放策略要求）
  notifyManager.unlock()

  sending.value = true
  const ctrl = new AbortController()
  // Install the placeholder assistant message immediately so
  // the three-bouncing-dots spinner is reachable. The actual
  // mutation happens in the onEvent callback below.
  startStream(id, ctrl)
  try {
    await api.streamMessagesRetry(id, {
      message: text,
      // The integer id minted above (and stamped on the
      // local Message as `msg.id`) is shipped to the
      // backend as `client_msg_id` so the server inserts
      // this turn's user row with our id, not a fresh
      // autoincrement. That keeps the local msg.id and
      // the SQLite row id in lockstep, so rollback and
      // regenerate work the instant the user clicks them.
      client_msg_id: clientMsgId,
      provider: meta.provider,
      model: meta.model,
      style: meta.style,
      attachments: inlineAttachments,
      signal: ctrl.signal,
      skill_context: pendingSkillContext || undefined,
      // P0-1: when the SSE stream dies mid-turn, call
      // the snapshot endpoint to recover whatever
      // assistant content already landed. The recovery
      // flow is fire-and-forget from this call site; the
      // chat store mutates the trailing message
      // directly. NOT triggered on user abort (the stop
      // button sets signal.aborted; the underlying
      // fetch is then cancelled and the drop callback
      // is short-circuited in client.ts).
      onStreamDrop: ({ lastSeq, reason }) => {
        recoverMissingParts(id, lastSeq, reason).catch((err) => {
          console.warn('[stream] recovery failed:', err)
        })
      },
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

// --- Inline session config (style + advanced) --------------------

// The model picker moved to its own ModelPicker component
// (PR #6). The remaining per-session config in this file
// is just the style picker (small icon button + dropdown)
// and the advanced row (reasoning + KB) which lives behind
// the "更多" toggle.

const styleOptions = ref<{ label: string; value: string }[]>([])

async function loadConfig() {
  try {
    await loadProviders()
    loadKBases()
    const st = await api.getStyles()
    styleOptions.value = (st.styles || []).map((x: any) => ({
      label: x.label || x.id,
      value: x.id,
    }))
  } catch { /* ignore */ }
}

// onModelSelect is the only model-pick handler now (the
// ModelPicker emits a {provider, model} pair). PATCH 1
// posts the pair to /sessions/:id/meta and PATCH 2
// reconciles the local cache from the server's response
// (which may have applied a fallback model the user
// didn't actually request).
async function onModelSelect(sel: { provider: string; model: string }) {
  if (!state.currentID) return
  const resp = await api.updateSessionMeta(state.currentID, sel)
  const id = state.currentID
  state.sessionMeta[id] = {
    ...(state.sessionMeta[id] || currentMeta.value),
    provider: resp.provider ?? sel.provider,
    model: resp.model ?? sel.model,
  }
}

async function onStylePick(v: string) {
  if (!state.currentID) return
  const resp = await api.updateSessionMeta(state.currentID, { style: v })
  const id = state.currentID
  state.sessionMeta[id] = {
    ...(state.sessionMeta[id] || currentMeta.value),
    style: resp.style ?? v,
  }
}

const currentStyleValue = computed({
  get: () => currentMeta.value.style || 'tech',
  set: (v: string) => onStylePick(v),
})

// Display label for the reasoning picker. Maps the enum
// value (off/low/medium/high/max) to the Chinese label
// that reasoningEffortOptions uses, so the button shows
// the same string the dropdown would. Falls back to the
// raw value if it's an unknown enum (forward compat).
const REASONING_LABELS: Record<string, string> = {
  off: '关闭',
  low: '低',
  medium: '中',
  high: '高',
  max: '最高',
}
const currentReasoningLabel = computed(() => {
  const v = reasoningEffort.value || 'off'
  return REASONING_LABELS[v] || v
})

// Display label for the knowledge base picker. The "off"
// and "all" pseudo-bases get short labels so the button
// stays narrow; a real base name shows as-is.
const KB_LABELS: Record<string, string> = {
  __off__: '不使用',
  __all__: '全部',
}
const currentKBLabel = computed(() => {
  const v = kbBase.value || '__off__'
  if (KB_LABELS[v]) return KB_LABELS[v]
  return v
})

// Setter wrappers for the NDropdown @select handler.
// The handlers receive (key: string | number), but Vue's
// computed refs don't expose `.value` cleanly from the
// template (vue-tsc errors on it). These thin wrappers
// make the assignment explicit and keep the template
// `@select` line one expression.
function pickReasoning(v: string) {
  reasoningEffort.value = v
}
function pickKB(v: string) {
  kbBase.value = v
}

// showModelPicker drives the ModelPicker popover. Toggled by
// clicking the model badge in the bottom row. The ModelPicker
// itself emits `update:show=false` when the user picks a
// model or hits Esc, so we only need to handle the open
// direction here.
const showModelPicker = ref(false)
function openModelPicker() {
  if (!state.currentID) return
  showModelPicker.value = true
}

// showAdvanced toggles the "更多" secondary row that
// hosts the KB picker. Default collapsed: the KB is a
// less-touched setting (the user usually picks one KB
// per project and rarely changes it) so the row stays
// out of the way. Reasoning used to live here too but
// was promoted to the input-row in PR #10.
const showAdvanced = ref(false)

// Permission-level picker: small icon-only popover that
// shows a 3-option list (always-ask / auto-approve /
// full-access). Replaces the old NSelect which took a lot
// of horizontal space.
const showPermPicker = ref(false)
const permIcon = computed(() => {
  if (permissionLevel.value === 'full') return Key
  if (permissionLevel.value === 'auto') return Unlock
  return Lock
})
const permLabel = computed(() => {
  if (permissionLevel.value === 'full') return '完全访问'
  if (permissionLevel.value === 'auto') return '替我审批'
  return '始终询问'
})

// Load the model/style lists once on mount so the two dropdowns
// are populated even before the user opens any session.
onMounted(() => {
  loadConfig()
})
</script>

<template>
  <div class="input-area">
    <!-- Rollback undo banner -->
    <div v-if="currentRollbackBanner" class="rollback-banner">
      <Undo2 :size="14" class="rollback-banner-icon" />
      <span class="rollback-banner-text">已撤回 {{ currentRollbackBanner.count }} 条消息</span>
      <button class="rollback-banner-undo" @click="undoRollback(state.currentID)">撤销</button>
      <button class="rollback-banner-dismiss" @click="dismissRollback(state.currentID)" aria-label="关闭">×</button>
    </div>

    <!-- Attachments live INSIDE the same input-wrap as the
         textarea but BELOW the input-row, so a pasted image
         appears right under the user's cursor rather than
         being pushed up above the textarea (where it can
         scroll out of view on a short viewport). The dashed
         top border on the strip acts as a visual separator
         so the two regions read as "type → attach → controls"
         instead of one undifferentiated blob. -->
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
      <div class="input-row">
        <button class="attach-icon-btn" @click="onPickFiles" title="添加附件(支持拖拽、剪贴板粘贴)" aria-label="添加附件">
          <Paperclip :size="18" />
        </button>
        <textarea
          ref="inputEl"
          v-model="inputText"
          class="textarea"
          rows="1"
          :placeholder="isSlashLine() ? '输入 / 后跟命令 (例如 /help)' : '输入消息，Enter 发送，Shift+Enter 换行，Esc 停止，/ 前缀是命令'"
          @keydown="onKeyDown"
          @paste="onPaste"
        ></textarea>
        <!-- Session-level option pickers (style + reasoning).
             These all share the same visual treatment —
             a compact pill button with a small chevron that
             opens an NDropdown — so they read as a single
             "session settings" cluster on the right side of
             the input. The dropdown is preferred over
             NSelect here because:
               1. NSelect's chrome (border + chevron) would
                  fight the input-wrap's own border and the
                  attach/send button styling, producing a
                  visually busy row.
               2. The trigger is purely cosmetic — the actual
                  selection lives in the dropdown menu, so
                  the button just needs to look "pressable"
                  and show the current value.
             Reasoning was promoted from the "more" advanced
             row (PR #10) so the user doesn't have to expand
             a hidden section to reach a setting they touch
             on most tasks. -->
        <NDropdown
          trigger="click"
          placement="top-end"
          :options="styleOptions.map(o => ({ key: o.value, label: o.label }))"
          @select="(key: string | number) => onStylePick(String(key))"
        >
          <button
            type="button"
            class="opt-pick"
            :disabled="!state.currentID"
            :title="`当前风格: ${currentStyleValue}`"
            :aria-label="`当前风格: ${currentStyleValue}`"
          >
            <span class="opt-pick-label">{{ currentStyleValue }}</span>
            <component :is="ChevronDown" :size="11" class="opt-pick-caret" />
          </button>
        </NDropdown>
        <NDropdown
          trigger="click"
          placement="top-end"
          :options="(reasoningEffortOptions[0]?.children || []).map(o => ({ key: o.value, label: o.label }))"
          @select="(key: string | number) => pickReasoning(String(key))"
        >
          <button
            type="button"
            class="opt-pick opt-pick--narrow"
            :disabled="!state.currentID"
            :title="`推理等级: ${currentReasoningLabel}`"
            :aria-label="`推理等级: ${currentReasoningLabel}`"
          >
            <span class="opt-pick-label">{{ currentReasoningLabel }}</span>
            <component :is="ChevronDown" :size="11" class="opt-pick-caret" />
          </button>
        </NDropdown>
        <button
          v-if="!isStreaming"
          class="send-btn"
          :disabled="!inputText.trim()"
          @click="send"
          title="发送 (Enter)"
          aria-label="发送"
        >
          <Send :size="16" />
        </button>
        <button
          v-else
          class="stop-btn"
          @click="stop"
          title="停止 (Esc)"
          aria-label="停止"
        >
          <Square :size="14" fill="currentColor" />
        </button>
      </div>
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
            <Volume2 v-else-if="a.kind === 'audio'" :size="18" />
            <FileText v-else-if="a.kind === 'text'" :size="18" />
            <File v-else :size="18" />
          </div>
          <span class="name" :title="a.name">{{ a.name }}</span>
          <button class="rm" @click="removeAttachment(i)" title="移除" aria-label="移除附件">×</button>
        </div>
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

    <!-- Bottom row: compact high-frequency controls + collapsible
         "more" section. The four always-visible controls
         (model / plan / permission / mute) are the ones the
         user touches on most messages. Reasoning + KB
         live behind the ⋯ button by default. -->
    <div class="input-bottom">
      <div class="input-primary">
        <!-- Model badge: the current model name with a sparkle
             icon. Click opens the ModelPicker popover
             (PR #6) which replaces the old NSelect — see
             ModelPicker.vue for the command-palette-style
             search + grouped list + keyboard nav. -->
        <ModelPicker
          :show="showModelPicker"
          @update:show="(v) => showModelPicker = v"
          :provider="currentMeta.provider"
          :model="currentMeta.model"
          :providers="state.providers as any"
          @select="onModelSelect"
        >
          <template #trigger>
            <button
              type="button"
              class="model-badge"
              :class="{ 'model-badge--unset': !currentMeta.model }"
              :disabled="!state.currentID"
              :title="'选择模型'"
              :aria-label="currentMeta.model ? `当前模型 ${currentMeta.model}，点击更换` : '选择模型'"
              @click="openModelPicker"
            >
              <Sparkles :size="12" class="model-badge-icon" />
              <span class="model-badge-name">{{ currentMeta.model || '选择模型' }}</span>
              <ChevronDown :size="11" class="model-badge-caret" />
            </button>
          </template>
        </ModelPicker>

        <!-- Plan mode toggle: stays inline because the user
             switches it often (planning vs building a feature). -->
        <button
          type="button"
          class="ctrl-btn"
          :class="{ 'ctrl-btn--active': planMode }"
          :disabled="!state.currentID"
          :title="planMode ? '当前：计划模式' : '当前：构建模式'"
          :aria-label="planMode ? '切换到构建模式' : '切换到计划模式'"
          @click="togglePlanMode"
        >
          <component :is="planMode ? Clipboard : Hammer" :size="13" />
          <span class="ctrl-btn-label">{{ planMode ? '计划' : '构建' }}</span>
        </button>

        <!-- Permission picker: icon-only popover. Three
             states map to lock / unlock / key icons. Keeps
             the bottom row narrow. NPopover with
             trigger="click" + v-model:show handles the open /
             close state — no separate @click handler on the
             trigger button (that would race with the
             popover's own click listener). -->
        <NPopover
          v-model:show="showPermPicker"
          trigger="click"
          placement="top-start"
          :show-arrow="false"
          style="padding: 0; background: transparent; box-shadow: none;"
        >
          <template #trigger>
            <button
              type="button"
              class="ctrl-btn"
              :disabled="!state.currentID"
              :title="`权限: ${permLabel}`"
              :aria-label="`权限级别: ${permLabel}，点击更改`"
            >
              <component :is="permIcon" :size="13" />
              <span class="ctrl-btn-label">{{ permLabel }}</span>
            </button>
          </template>
          <div class="perm-popover">
            <div class="perm-popover-label">工具权限</div>
            <button
              v-for="opt in permissionOptions"
              :key="opt.value"
              type="button"
              class="perm-popover-item"
              :class="{ 'perm-popover-item--active': permissionLevel === opt.value }"
              @click="onChangePermissionLevel(opt.value); showPermPicker = false"
            >
              <span class="perm-popover-radio" />
              <span class="perm-popover-name">{{ opt.label }}</span>
            </button>
          </div>
        </NPopover>

        <!-- Mute toggle: same icon-only treatment as the
             other controls. Stays in the always-visible
             row because it's a one-click toggle. -->
        <button
          type="button"
          class="ctrl-btn"
          :class="{ 'ctrl-btn--active-warn': mute }"
          :disabled="!state.currentID"
          :title="mute ? '提示音已关闭' : '提示音已开启'"
          :aria-label="mute ? '开启提示音' : '关闭提示音'"
          @click="onToggleMute"
        >
          <component :is="mute ? VolumeX : Volume2" :size="13" />
        </button>

        <!-- More toggle: expands the secondary row with
             the KB picker. The chevron rotates to
             indicate the expanded state. Reasoning used
             to live here too but was promoted to the
             input-row in PR #10 (next to the style
             picker) because it's a setting the user
             touches on most tasks; KB stays in "more"
             because it's changed less often and the
             label can be long ("知识库 · {name}"),
             which would crowd the input-row. The
             expanded state uses the same .opt-pick
             styling as the input-row pickers so the
             three read as a coherent family. -->
        <button
          type="button"
          class="ctrl-btn ctrl-btn--more"
          :class="{ 'ctrl-btn--expanded': showAdvanced }"
          :title="showAdvanced ? '收起高级选项' : '展开高级选项'"
          :aria-label="showAdvanced ? '收起高级选项' : '展开高级选项'"
          :aria-expanded="showAdvanced"
          @click="showAdvanced = !showAdvanced"
        >
          <component :is="showAdvanced ? ChevronUp : ChevronDown" :size="13" />
          <span class="ctrl-btn-label">更多</span>
        </button>
      </div>

      <!-- Secondary row: KB picker. Collapsed by default.
           Uses the same .opt-pick visual treatment as the
           input-row style + reasoning pickers so it reads
           as part of the same family, not a different
           control. The .input-advanced wrapper still
           provides the surface-1 background + border so
           the row reads as visually subordinate (a
           "secondary" surface) even though its controls
           match the primary surface. -->
      <Transition name="row-slide">
        <div v-if="showAdvanced" class="input-advanced">
          <NDropdown
            trigger="click"
            placement="top-end"
            :options="kbOptions.map(o => ({ key: o.value, label: o.label }))"
            @select="(key: string | number) => pickKB(String(key))"
          >
            <button
              type="button"
              class="opt-pick"
              :disabled="!state.currentID"
              :title="`知识库: ${currentKBLabel}`"
              :aria-label="`知识库: ${currentKBLabel}`"
            >
              <Database :size="12" class="opt-pick-icon" />
              <span class="opt-pick-label">{{ currentKBLabel }}</span>
              <component :is="ChevronDown" :size="11" class="opt-pick-caret" />
            </button>
          </NDropdown>
        </div>
      </Transition>

      <!-- Keyboard hints: live at the very bottom, always
           visible. Aligns to the right so the rest of the
           row has the user's eye path. -->
      <div class="hints">
        <span><kbd>Enter</kbd> 发送</span>
        <span><kbd>Shift</kbd>+<kbd>Enter</kbd> 换行</span>
        <span><kbd>Esc</kbd> 停止</span>
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
/* The strip is rendered BELOW the input-row (see the
 * template comment), so its top edge meets the
 * textarea/button row instead of the input-wrap's top
 * border. The dashed top border is the visual separator
 * that hints at the parent/child relationship: type →
 * attach → controls reads as three regions, not one
 * undifferentiated blob. Larger max-height than the old
 * "above the input" version because the strip is no
 * longer competing with the textarea for vertical space. */
.attach-strip {
  display: flex;
  flex-wrap: wrap;
  gap: 6px;
  padding: 6px 0 2px 0;
  border-top: 1px dashed var(--border-subtle);
  margin-top: 4px;
  max-height: 160px;
  overflow-y: auto;
  overflow-x: hidden;
  align-content: flex-start;
  /* Smooth height transition when chips appear/disappear.
   * The chip-appear keyframe on individual chips is the
   * primary feedback; this just makes the wrap itself
   * glide rather than pop. */
  transition: max-height 0.2s var(--ease-out);
}
.attach-chip {
  display: inline-flex; align-items: center; gap: 6px;
  background: var(--bg-3);
  border: 1px solid var(--border-2);
  border-radius: 8px;
  padding: 4px 6px 4px 4px;
  font-size: 12px;
  flex: 0 0 auto;
  max-width: 100%;
  min-width: 0;
  /* fade-in: a chip that just appeared (e.g. from a paste)
   * animates in from slightly above + a touch of scale.
   * translateY direction is "from above" so the chip looks
   * like it dropped in from the input above it — coherent
   * with the new "below the input" position. */
  animation: chip-appear 0.22s var(--ease-out);
  transition: border-color 0.15s, background 0.15s;
}
.attach-chip:hover {
  border-color: var(--accent);
  background: var(--bg-2);
}
.attach-chip.uploading { opacity: 0.7; }
.attach-chip.error { border-color: var(--error); }
.thumb {
  width: 40px; height: 40px;
  background: var(--bg-2);
  border-radius: 6px;
  display: flex; align-items: center; justify-content: center;
  overflow: hidden;
  font-size: 18px;
  flex-shrink: 0;
}
.thumb img, .thumb video { width: 100%; height: 100%; object-fit: cover; }
.name { max-width: 140px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.rm {
  background: none; border: none; color: var(--text-3);
  cursor: pointer; padding: 0 4px; font-size: 16px; line-height: 1;
  flex-shrink: 0;
}
.rm:hover { color: var(--error); }

@keyframes chip-appear {
  from { opacity: 0; transform: translateY(-4px) scale(0.95); }
  to { opacity: 1; transform: translateY(0) scale(1); }
}

/* Rollback undo banner */
.rollback-banner {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 12px;
  margin-bottom: 8px;
  background: var(--warn-50);
  border: 1px solid var(--warn-500);
  border-radius: var(--radius-sm);
  font-size: 13px;
}
.rollback-banner-icon {
  font-size: 15px;
  color: var(--warn-500);
}
.rollback-banner-text {
  color: var(--text-primary);
  flex: 1;
}
.rollback-banner-undo {
  background: none;
  border: none;
  color: var(--warn-500);
  cursor: pointer;
  font-size: 13px;
  padding: 2px 8px;
  border-radius: var(--radius-sm);
}
.rollback-banner-undo:hover {
  background: var(--warn-500);
  color: var(--on-brand);
}
.rollback-banner-dismiss {
  background: none;
  border: none;
  color: var(--text-3);
  cursor: pointer;
  font-size: 16px;
  padding: 0 4px;
  line-height: 1;
}
.rollback-banner-dismiss:hover {
  color: var(--text);
}

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
.opt-pick {
  /* The session-level option pickers (style, reasoning, KB)
   * share the same visual treatment: a compact pill button
   * with the current value as a label and a small chevron
   * on the right. They live in the input-row next to the
   * textarea, so they need to be narrow and quiet — no
   * border, no NSelect chrome, just a text label that gets
   * a subtle background on hover.
   *
   * Originally named `.style-pick` (just for the style
   * picker); renamed to `.opt-pick` in PR #10 when
   * reasoning and KB were promoted from the "more" advanced
   * row to the input-row. The `.opt-pick--narrow` modifier
   * is used for reasoning because its labels (关闭/低/中/高/
   * 最高) are very short and a smaller min-width keeps the
   * three pickers visually balanced. */
  display: inline-flex;
  align-items: center;
  gap: 4px;
  height: 28px;
  padding: 0 8px;
  background: transparent;
  border: 1px solid transparent;
  border-radius: var(--radius-md);
  color: var(--text-secondary);
  font-size: 12px;
  font-family: var(--font-mono);
  cursor: pointer;
  flex-shrink: 0;
  white-space: nowrap;
  min-width: 56px;
  justify-content: center;
  transition: background var(--dur-fast) var(--ease-out),
              color var(--dur-fast) var(--ease-out);
}
.opt-pick:hover:not(:disabled) {
  background: var(--surface-3);
  color: var(--text-primary);
}
.opt-pick:disabled { opacity: 0.5; cursor: not-allowed; }
.opt-pick-caret { color: var(--text-tertiary); flex-shrink: 0; }
.opt-pick-label {
  /* Cap on label width so a long KB name (e.g.
   * "知识库 · 我的资料库") doesn't push the send button off
   * the row. When the label overflows, ellipsis kicks in
   * and the user can still read the full name in the
   * dropdown. */
  max-width: 72px;
  overflow: hidden;
  text-overflow: ellipsis;
}
.opt-pick--narrow {
  /* Reasoning labels are 1–2 characters, so the regular
   * 56px min-width looks oversized. Tighter min keeps the
   * three pickers visually balanced. */
  min-width: 36px;
}
.opt-pick--narrow .opt-pick-label {
  max-width: 28px;
}
/* Small inline icon (e.g. the database glyph on the KB
 * picker). Slightly muted so it doesn't compete with the
 * label. */
.opt-pick-icon {
  color: var(--text-tertiary);
  flex-shrink: 0;
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

/* Keyboard hints: live at the very bottom of the input area,
 * right-aligned. Always visible. */
.hints {
  font-size: 10.5px;
  color: var(--text-tertiary);
  display: flex;
  align-items: center;
  gap: 10px;
  margin-left: auto;
  white-space: nowrap;
}
.hints kbd {
  background: var(--surface-2);
  border: 1px solid var(--border-subtle);
  border-radius: 3px;
  padding: 1px 4px;
  font-family: var(--font-mono);
  font-size: 9.5px;
  color: var(--text-secondary);
  margin-right: 2px;
}

/* NSelects in the advanced row (reasoning + KB). */
/* The input area's height is determined by its content: the
 * textarea (capped at 4 lines by resizeTextarea()), the
 * attach-strip (capped at 96px internally), and the
 * bottom controls (primary row + the "更多" advanced row
 * when expanded). Each child caps itself, so the area as
 * a whole can grow to whatever's needed without clipping
 * the dialog or pushing the messages-scroll out of the
 * way. The `flex-shrink: 0` ensures the message list
 * above gets compressed first if the viewport is
 * genuinely too small (we'd rather show fewer messages
 * than hide the input). */
.input-area {
  border-top: 1px solid var(--border);
  background: var(--bg-2);
  padding: 8px 12px;
  flex-shrink: 0;
}
.input-bottom {
  display: flex;
  flex-direction: column;
  gap: 6px;
  margin-top: 6px;
}
.input-primary {
  display: flex;
  align-items: center;
  gap: 4px;
  flex-wrap: wrap;
}
.input-advanced {
  display: flex;
  align-items: center;
  gap: 6px;
  flex-wrap: wrap;
  padding: 6px 8px;
  background: var(--surface-1);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-md);
}

/* Slide-down transition for the advanced row. Tied to the
 * <Transition name="row-slide"> in the template. The classes
 * are named in the Vue 2 / 3 transition convention. */
.row-slide-enter-active,
.row-slide-leave-active {
  transition: max-height var(--dur-base) var(--ease-out),
              opacity var(--dur-base) var(--ease-out);
  overflow: hidden;
}
.row-slide-enter-from,
.row-slide-leave-to {
  max-height: 0;
  opacity: 0;
}
.row-slide-enter-to,
.row-slide-leave-from {
  max-height: 80px;
  opacity: 1;
}

/* --- Bottom-row buttons (plan / perm / mute / more) --------------- */
/* Generic pill-button style used for the always-visible
 * bottom-row controls. The button is transparent by
 * default and picks up an active background when the
 * underlying state is on (plan mode active, mute active).
 * Different active colors are applied via .ctrl-btn--active
 * (brand) and .ctrl-btn--active-warn (warn). */
.ctrl-btn {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  height: 28px;
  padding: 0 8px;
  background: transparent;
  border: 1px solid transparent;
  border-radius: var(--radius-md);
  color: var(--text-secondary);
  font-size: 12px;
  cursor: pointer;
  flex-shrink: 0;
  white-space: nowrap;
  transition: background var(--dur-fast) var(--ease-out),
              color var(--dur-fast) var(--ease-out),
              border-color var(--dur-fast) var(--ease-out);
}
.ctrl-btn:hover:not(:disabled) {
  background: var(--surface-3);
  color: var(--text-primary);
}
.ctrl-btn:disabled {
  opacity: 0.45;
  cursor: not-allowed;
}
.ctrl-btn--active {
  background: var(--brand-50);
  color: var(--brand-600);
  border-color: var(--brand-100);
}
.ctrl-btn--active:hover:not(:disabled) {
  background: var(--brand-100);
  color: var(--brand-700);
}
.ctrl-btn--active-warn {
  background: var(--warn-50);
  color: var(--warn-500);
}
.ctrl-btn--more.ctrl-btn--expanded {
  background: var(--surface-2);
  border-color: var(--border-subtle);
}
.ctrl-btn-label {
  font-size: 12px;
  font-weight: 500;
}

/* --- Model badge ------------------------------------------------ */
/* The "current model" trigger for the ModelPicker popover.
 * Shows the model name with a sparkle icon and a chevron to
 * hint that clicking opens a picker. Adopts an "unset"
 * state when no model is selected (no current session, or
 * no provider configured). */
.model-badge {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  height: 28px;
  padding: 0 8px 0 8px;
  background: var(--surface-1);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-md);
  color: var(--text-primary);
  font-size: 12px;
  font-weight: 500;
  cursor: pointer;
  flex-shrink: 0;
  max-width: 220px;
  transition: background var(--dur-fast) var(--ease-out),
              border-color var(--dur-fast) var(--ease-out);
}
.model-badge:hover:not(:disabled) {
  background: var(--surface-2);
  border-color: var(--border-default);
}
.model-badge:disabled { opacity: 0.5; cursor: not-allowed; }
.model-badge-icon { color: var(--ai-500); flex-shrink: 0; }
.model-badge-caret { color: var(--text-tertiary); flex-shrink: 0; }
.model-badge-name {
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  max-width: 160px;
  font-family: var(--font-mono);
}
.model-badge--unset .model-badge-name { color: var(--text-tertiary); font-style: italic; }

/* --- Permission popover ---------------------------------------- */
/* The permission picker is an NPopover that anchors to the
 * perm ctrl-btn. The popover body is a list of three
 * radio-style rows (always-ask / auto-approve / full-
 * access). Kept narrow — the user picks one and the
 * popover closes. */
.perm-popover {
  width: 200px;
  background: var(--surface-1);
  border: 1px solid var(--border-default);
  border-radius: var(--radius-md);
  box-shadow: var(--shadow-lg);
  padding: 6px;
  display: flex;
  flex-direction: column;
  gap: 2px;
}
.perm-popover-label {
  font-size: 10.5px;
  font-weight: 600;
  color: var(--text-tertiary);
  text-transform: uppercase;
  letter-spacing: 0.06em;
  padding: 4px 8px 2px;
}
.perm-popover-item {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 6px 8px;
  background: transparent;
  border: none;
  border-radius: var(--radius-sm);
  color: var(--text-primary);
  font-size: 13px;
  cursor: pointer;
  text-align: left;
  transition: background var(--dur-fast) var(--ease-out);
}
.perm-popover-item:hover {
  background: var(--surface-3);
}
.perm-popover-item--active {
  background: var(--brand-50);
  color: var(--brand-600);
}
.perm-popover-item--active:hover {
  background: var(--brand-100);
}
.perm-popover-radio {
  width: 12px; height: 12px;
  border-radius: 50%;
  border: 1.5px solid var(--border-strong);
  flex-shrink: 0;
  position: relative;
}
.perm-popover-item--active .perm-popover-radio {
  border-color: var(--brand-500);
}
.perm-popover-item--active .perm-popover-radio::after {
  content: '';
  position: absolute;
  inset: 2px;
  border-radius: 50%;
  background: var(--brand-500);
}
.perm-popover-name {
  flex: 1;
}
</style>
