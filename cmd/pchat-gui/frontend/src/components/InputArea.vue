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
  state, currentMeta, addAttachment, removeAttachment, clearAttachments,
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
watch(() => state.pendingAttachments.length, () => nextTick(resizeTextarea))

// Also sync after backspace / clear (send resets inputText to '').
onMounted(() => nextTick(resizeTextarea))
const sending = ref(false)
const showSessionConfig = ref(false)
const message = useMessage()
const fileInput = ref<HTMLInputElement | null>(null)
const reasoningEffort = ref('off')

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

async function onChangeReasoningEffort(val: string) {
  reasoningEffort.value = val
  if (state.currentID) {
    try { await api.setReasoningEffort(state.currentID, val) } catch {}
  }
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
    case 'clear': {
      if (state.currentID) {
        // Clear the local cache for the current session; the on-disk
        // history is preserved. Use a fresh session for a real reset.
        state.sessionMessages[state.currentID] = []
        appendSystemMessage('已清空当前对话视图(服务器历史已保留)。')
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
        try {
          const r = await api.compressConversation(state.currentID)
          if (r.compressed) {
            appendSystemMessage(`对话已压缩。\n\n摘要:\n${r.summary}`)
          } else {
            appendSystemMessage('对话消息数未达阈值，无需压缩。')
          }
        } catch (e: any) {
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
  const lines: string[] = ['可用命令:']
  const local: Array<[string, string]> = [
    ['/help', '显示此帮助'],
    ['/new', '开启新对话'],
    ['/clear', '清空当前对话视图'],
    ['/rename <标题>', '重命名当前会话'],
    ['/forget', '删除当前会话'],
    ['/compress', '压缩对话历史(LLM摘要)'],
  ]
  for (const [k, d] of local) lines.push(`  ${k.padEnd(20)} ${d}`)
  if (commandList.value.length > 0) {
    lines.push('')
    lines.push('更多命令(由服务器提供):')
    for (const c of commandList.value) {
      const a = c.args ? ` ${c.args}` : ''
      lines.push(`  /${c.name.padEnd(12)}${a.padEnd(20)} ${c.description}`)
    }
  }
  return lines.join('\n')
}

async function send() {
  const raw = inputText.value.trim()
  if (!raw || sending.value) return

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
  const inlineAttachments: api.InlineAttachment[] = []
  const bubbleAttachments: api.InlineAttachment[] = []
  for (const a of state.pendingAttachments) {
    if (a._error) continue
    const data = a._dataURL
    if (!data) continue
    if (a.kind === 'image') {
      inlineAttachments.push({ type: 'image_url', url: data, name: a.name, kind: a.kind, mime: a.mime })
      bubbleAttachments.push({ type: 'image_url', url: data, name: a.name, kind: a.kind, mime: a.mime })
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
          return
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
        'has-attachments': state.pendingAttachments.length > 0,
      }"
      @dragover="onDragOver"
      @dragleave="onDragLeave"
      @drop="onDrop"
    >
      <div v-if="state.pendingAttachments.length > 0" class="attach-strip">
        <div
          v-for="(a, i) in state.pendingAttachments"
          :key="i"
          class="attach-chip"
          :class="{ uploading: a._uploading, error: a._error }"
        >
          <div class="thumb">
            <img v-if="a.kind === 'image'" :src="a._previewURL" :alt="a.name" />
            <span v-else-if="a.kind === 'audio'">🔊</span>
            <span v-else-if="a.kind === 'text'">📝</span>
            <span v-else>📄</span>
          </div>
          <span class="name" :title="a.name">{{ a.name }}</span>
          <button class="rm" @click="removeAttachment(i)" title="移除">×</button>
        </div>
      </div>
      <textarea
        ref="inputEl"
        v-model="inputText"
        class="textarea"
        rows="1"
        :placeholder="isSlashLine() ? '输入 / 后跟命令 (例如 /help)' : '输入消息，Enter 发送，Shift+Enter 换行，Esc 停止，/ 前缀是命令'"
        @keydown="onKeyDown"
        @paste="onPaste"
      ></textarea>
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

    <!-- Two always-visible dropdowns (no toggle button): one for
         style, one for the (provider, model) pair. Both are
         per-session — they live next to the attach button so the
         user can switch context mid-conversation.

         The bottom row is split into two sub-rows:
           - controls: 📎 add + style + model (these can grow)
           - hints: keyboard shortcuts (compact, pushed right)
         Splitting prevents the row from overflowing on narrow
         chat windows, which previously pushed the entire input
         area — and the chat above it — out of the visible
         viewport. The full input-area is also bounded by
         `max-height` with internal scrolling so it can never
         exceed a sane share of the window. -->
    <div class="input-bottom">
      <input
        ref="fileInput"
        type="file"
        multiple
        style="display:none"
        accept="image/*,audio/*,text/*,.pdf,.json,.md,.txt,.csv,.yaml,.yml,.go,.py,.js,.ts"
        @change="onFiles(($event.target as HTMLInputElement).files)"
      />
      <div class="input-controls">
        <button class="attach-btn" @click="onPickFiles" title="添加附件(支持拖拽、剪贴板粘贴)">
          <span>📎</span><span>添加附件</span>
        </button>
        <NSelect
          v-model:value="currentStyleValue"
          :options="styleOptions"
          size="small"
          :disabled="!state.currentID"
          class="picker"
          title="选择人格风格"
          placeholder="选择风格"
        />
        <NSelect
          v-model:value="currentSelectionValue"
          :options="modelOptions"
          size="small"
          :disabled="!state.currentID"
          class="picker picker-wide"
          title="选择模型 (按提供商分组; ⭐ = 默认)"
          placeholder="选择模型"
        />
        <NSelect
          v-model:value="reasoningEffort"
          :options="reasoningEffortOptions"
          size="small"
          class="picker picker-narrow"
          title="推理等级 (off/low/medium/high/max)"
          placeholder="推理"
          @update:value="onChangeReasoningEffort"
        />
      </div>
      <div class="hints">
        <span><kbd>Enter</kbd> 发送</span>
        <span><kbd>Shift</kbd>+<kbd>Enter</kbd> 换行</span>
        <span><kbd>Esc</kbd> 停止</span>
        <span><kbd>/</kbd> 命令</span>
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
  position: relative;
  background: var(--bg-input);
  border: 1px solid var(--border-2);
  border-radius: 10px;
  padding: 0 48px 0 12px;
  transition: border-color 0.15s;
  display: flex;
  flex-direction: column;
}
.input-wrap.has-attachments { padding-top: 0; }
.input-wrap:focus-within { border-color: var(--accent); }
.input-wrap.dragover { border-color: var(--accent-2); background: var(--bg-3); }
.textarea {
  background: transparent;
  border: none;
  color: var(--text);
  font-size: 14px;
  outline: none;
  resize: none;
  width: 100%;
  /* Height is managed by resizeTextarea(). */
  font-family: inherit;
  line-height: 1.5;
  /* Tight top margin so the textarea sits right under the chip
   * strip; the strip's own padding-bottom provides the visual
   * gap, which keeps them joined to the same box. */
  margin: 0;
  padding: 8px 0 8px 0;
}
.send-btn, .stop-btn {
  position: absolute;
  right: 8px; bottom: 8px;
  width: 32px; height: 32px;
  border: none; border-radius: 8px;
  font-size: 14px; cursor: pointer;
  display: flex; align-items: center; justify-content: center;
  background: var(--accent); color: var(--on-accent);
}
.send-btn:disabled { background: var(--bg-3); color: var(--text-4); cursor: not-allowed; }
.send-btn:hover:not(:disabled) { background: var(--accent-2); }
.stop-btn { background: var(--error); }
.stop-btn:hover { background: var(--error); opacity: 0.85; }

.input-bottom {
  display: flex; justify-content: space-between; align-items: center;
  margin-top: 6px; gap: 6px;
}
.attach-btn {
  background: transparent; color: var(--text-2);
  border: 1px dashed var(--border-2);
  border-radius: 6px;
  padding: 4px 10px;
  font-size: 12px;
  cursor: pointer;
  display: flex; align-items: center; gap: 4px;
  transition: background 0.15s, border-color 0.15s, color 0.15s;
}
.attach-btn:hover { background: var(--bg-3); border-color: var(--accent-2); color: var(--accent-2); }
.attach-btn.active { background: var(--bg-3); border-style: solid; border-color: var(--accent); color: var(--accent); }
.hints { font-size: 10px; color: var(--text-4); display: flex; gap: 12px; margin-left: auto; }
.hints kbd {
  background: var(--bg-3); border: 1px solid var(--border-2);
  border-radius: 3px; padding: 0 4px; font-family: ui-monospace, Menlo, monospace; font-size: 9px;
}

/* Inline session-config dropdowns (always visible, no toggle). */
.picker {
  /* The two NSelects sit in the bottom bar; a fixed-ish width keeps
     them readable without crowding the hints on the right. */
  min-width: 110px;
  max-width: 160px;
  flex: 0 0 auto;
}
.picker-wide {
  min-width: 180px;
  max-width: 240px;
  flex: 1 1 180px;
}
.picker-narrow {
  min-width: 70px;
  max-width: 90px;
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
  flex-wrap: wrap;
  align-items: center;
  margin-top: 6px;
  gap: 6px 8px;
}
.input-controls {
  display: flex;
  flex-wrap: wrap;
  align-items: center;
  gap: 6px;
  flex: 1 1 auto;
  min-width: 0;
}
</style>
