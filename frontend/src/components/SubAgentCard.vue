<script setup lang="ts">
// Nested card for a sub-agent's stream. The header is a compact
// process row: "agentType · task" plus model/status/progress chips.
// The detailed stream stays collapsed by default, including while
// running, so a busy sub-agent does not take over the chat surface.
//
// The agent's accent color (sub_agent_color) drives
// the left-border tint, the icon background, and the
// model chip. When no color is set we fall back to a
// neutral text-4 border.
//
// The card's task_id (when set) is shown as a small
// monospace badge in the footer; clicking copies it to
// the clipboard so the user can re-invoke the same
// sub-agent by passing it back as the `task_id` arg.
import { computed, onBeforeUnmount, ref, watch } from 'vue'
import { renderMarkdown } from '../utils/markdownCache'
import type { SubAgentPart, MessagePart } from '../api/client'
import { Check, X, Clipboard, ChevronDown, ChevronRight } from './icons'
import ThinkingBlock from './ThinkingBlock.vue'
import ToolCallCard from './ToolCallCard.vue'
import ToolCallGroup from './ToolCallGroup.vue'
import LoadingDots from './LoadingDots.vue'
import TypedText from './TypedText.vue'
import { groupConsecutiveToolParts } from '../utils/toolPartGrouping'
import { copyText } from '../utils/clipboard'

const props = defineProps<{ part: SubAgentPart }>()

// Keep sub-agent detail collapsed by default, even while running.
// The header shows live progress, and the user can open the body
// only when they need the full nested stream.
const open = ref(false)
const userToggled = ref(false)
function toggle() {
  userToggled.value = true
  open.value = !open.value
}

// Sub-agent runs can emit the same dense mix of thoughts and tool
// cards as the parent. Retain their full data in the message model,
// while keeping only a bounded tail mounted in the nested card.
const SUB_PART_RENDER_WINDOW = 80
const revealedEarlierParts = ref(0)
const partWindowStart = computed(() => {
  const total = props.part.parts.length
  const visible = Math.min(total, SUB_PART_RENDER_WINDOW + revealedEarlierParts.value)
  return Math.max(0, total - visible)
})
const hiddenPartCount = computed(() => partWindowStart.value)
const visibleParts = computed(() => {
  const start = partWindowStart.value
  return props.part.parts.slice(start).map((part, offset) => ({ part, index: start + offset }))
})
const visibleRenderEntries = computed(() => groupConsecutiveToolParts(visibleParts.value))
function showEarlierParts() {
  revealedEarlierParts.value += SUB_PART_RENDER_WINDOW
}

// Agent name display: prefer the explicit subagent_type
// (e.g. "explore", "plan", "general-purpose"), fall back
// to a generic label when unset.
const agentLabel = computed(() => {
  const t = props.part.agentType
  if (!t) return 'sub-agent'
  return t
})

// Accent color with safe fallback. The server sends
// either "#RRGGBB" or a CSS color name. We just inject
// it into CSS custom properties so the card's tint
// stays consistent across the header + body.
const accentStyle = computed(() => {
  const c = props.part.agentColor
  if (!c) return {}
  return {
    '--sub-accent': c,
    '--sub-accent-soft': `color-mix(in srgb, ${c} 14%, var(--surface-2))`,
  }
})

const titleLabel = computed(() => `${agentLabel.value} · ${props.part.task || '未命名子任务'}`)

const runModeLabel = computed(() => props.part.runMode === 'async' ? '后台子代理' : '常规子代理')

const toolCount = computed(() => props.part.parts.filter(p => p.kind === 'tool').length)

const latestActivity = computed(() => {
  const parts = props.part.parts
  const last = parts[parts.length - 1]
  if (!last) return props.part.status === 'start' ? '等待子代理输出' : ''
  switch (last.kind) {
    case 'tool':
      return last.status === 'start' ? `正在执行 ${last.name}` : `最近执行 ${last.name}`
    case 'thinking':
      return props.part.status === 'start' ? '正在分析任务' : '已完成分析'
    case 'text':
      return props.part.status === 'start' ? '正在生成内容' : '已生成回复'
    case 'question':
      return '等待用户选择'
    case 'sub_agent':
      return '子任务更新'
    default:
      return ''
  }
})

const statusLabel = () => {
  switch (props.part.status) {
    case 'start': return '运行中…'
    case 'ok':    return '已完成'
    case 'err':
      // An interrupted sub-agent (timeout / cancel) with partial
      // output shows "失败（部分内容）" so the user knows the
      // card body holds usable findings, not just an error.
      return props.part.parts.some(p => p.kind === 'text' && p.text) ? '失败（部分内容）' : '失败'
    default:      return props.part.status
  }
}

// task_id copy affordance. Shown as a small monospace
// chip in the footer; click to copy.
const copyState = ref<'idle' | 'copied' | 'err'>('idle')
async function copyTaskId() {
  const id = props.part.taskId
  if (!id) return
  const ok = await copyText(id)
  if (ok) {
    copyState.value = 'copied'
    setTimeout(() => (copyState.value = 'idle'), 1200)
  } else {
    copyState.value = 'err'
    setTimeout(() => (copyState.value = 'idle'), 1200)
  }
}

// Safety-net timeout. The runner emits a `sub_agent_ok` /
// `sub_agent_err` close event when the sub-agent's stream
// ends; the chat store flips `part.status` accordingly.
// If that close event is dropped (per-tool channel
// backpressure, SSE buffer full, client-side race) the
// card would stay in the "running" state forever.
//
// As a last-resort fallback, if the card has been in
// `start` state for more than `STUCK_TIMEOUT_MS` we
// force-close it as `err` from the client. The user can
// still read whatever text the sub-agent did produce.
// STUCK_TIMEOUT_MS is a last-resort client-side safety net
// for when the server never sends the sub-agent close event
// (e.g. server crash mid-run). The server does not install a
// default sub-agent wall-clock cap (`subagent.timeout` empty
// = none). Real hangs are caught by LLM stream idle (120s),
// per-tool timeouts, and the turn deadline (MaxTurnSeconds,
// default 15 min). 6 minutes sits above typical tool/LLM
// stalls so the client does not force-close a still-running
// sub-agent that the server will close itself. If the user
// sets `subagent.timeout`, the server already enforces it;
// this timer is purely a UI backstop.
const STUCK_TIMEOUT_MS = 6 * 60 * 1000 // 6 minutes

// renderSubText runs the sub-agent's *static* text part through the
// same markdown pipeline as the parent MessageBubble uses (.md-body),
// via the shared LRU cache. Keeps the sub-agent's prose visually
// consistent with the main chat: headings, code, lists, links,
// bold/italic all render the same way. Falls back to the raw escaped
// text on parse failure so a malformed markdown payload never blanks
// the card.
//
// NOTE: the *live* streaming text part does NOT go through here — it
// renders via <TypedText> (direct textContent) so a growing sub-agent
// reply never re-parses the full accumulated markdown per delta
// (that's O(n²) over the stream and shows up as renderer + raster CPU).
// Once the sub-agent closes, this markdown path takes over once.
function renderSubText(raw: string | undefined): string {
  if (!raw) return ''
  try {
    return renderMarkdown(raw)
  } catch {
    return raw.replace(/[&<>"']/g, c => ({
      '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
    }[c] as string))
  }
}

// isLiveTextPart mirrors MessageBubble's invariant: while the sub-agent
// is still running, only the very last part can be the one actively
// streaming, and only if it's a text part. It renders through TypedText;
// once the sub-agent closes (status leaves 'start'), this returns false
// and the static markdown render takes over.
function isLiveTextPart(idx: number, part: MessagePart): boolean {
  if (part.kind !== 'text') return false
  if (props.part.status !== 'start') return false
  return idx === props.part.parts.length - 1
}
let stuckTimer: ReturnType<typeof setTimeout> | null = null
// armStuckTimer (re)starts the force-close safety net. Only a
// sub-agent that is BOTH still 'start' AND has produced no new
// parts for STUCK_TIMEOUT_MS gets force-closed — an actively
// streaming run (server wall-clock now defaults to 30m for legit
// long tasks) resets the timer on every incoming part.
function armStuckTimer() {
  if (stuckTimer) clearTimeout(stuckTimer)
  const t = setTimeout(() => {
    stuckTimer = null
    // Read props at fire time (not capture time) so the
    // latest status wins.
    if (props.part.status === 'start') {
      // Mutating the parent's `part` directly is OK
      // because the chat store created the part and
      // owns the proxy. A no-op when the close event
      // has already arrived.
      ;(props.part as any).status = 'err'
    }
  }, STUCK_TIMEOUT_MS)
  stuckTimer = t
}
function clearStuckTimer() {
  if (stuckTimer) {
    clearTimeout(stuckTimer)
    stuckTimer = null
  }
}
watch(
  () => props.part.status,
  (s, prev) => {
    if (s === 'start' && prev !== 'start') {
      // Just transitioned to running — arm the safety net.
      armStuckTimer()
    } else if (s !== 'start' && prev === 'start') {
      // Close event arrived — clear the safety net.
      clearStuckTimer()
      if (!userToggled.value) open.value = false
    }
  },
  { immediate: true },
)
// Activity reset: a growing parts array means the sub-agent is
// still streaming (thinking / tool / text deltas). Reset the stuck
// timer so a legitimately long run (10-20m on a slow local model)
// is never force-closed as err while it is actively producing.
watch(
  () => props.part.parts.length,
  () => {
    if (props.part.status === 'start' && stuckTimer) armStuckTimer()
  },
)
onBeforeUnmount(() => clearStuckTimer())
</script>

<template>
  <div class="sub-agent-card" :class="'status-' + part.status" :style="accentStyle">
    <button
      class="sub-header"
      :class="{ running: part.status === 'start' }"
      @click="toggle"
      :title="open ? '收起子代理消息' : '展开子代理消息'"
    >
      <span class="sub-icon">{{ agentLabel.charAt(0).toUpperCase() }}</span>
      <span
        class="sub-run-mode"
        :class="'mode-' + (part.runMode === 'async' ? 'async' : 'sync')"
        :title="part.runMode === 'async' ? '后台异步执行的子代理' : '当前对话内执行的常规子代理'"
      >{{ runModeLabel }}</span>
      <span class="sub-title-wrap">
        <span
          class="sub-title"
          :title="part.agentDescription ? `${titleLabel} - ${part.agentDescription}` : titleLabel"
        >{{ titleLabel }}</span>
        <span v-if="!open && latestActivity" class="sub-activity">{{ latestActivity }}</span>
      </span>
      <span v-if="part.agentModel" class="sub-model" :title="'model: ' + part.agentModel">{{ part.agentModel }}</span>
      <!-- P1-2 live event counter. Shows the running parts
           count so the user sees the sub-agent making
           progress in real time (was previously only
           visible when the user expanded the body and
           even then no count). Counts text + thinking +
           tool + question parts — same array the body
           iterates over, so the chip is always in sync
           with what's rendered. -->
      <span
        v-if="part.parts.length > 0 || part.status === 'start'"
        class="sub-parts-count"
        :title="'已发出 ' + part.parts.length + ' 个 part'"
      >{{ part.parts.length }} parts</span>
      <span
        v-if="toolCount > 0"
        class="sub-tools-count"
        :title="'已调用 ' + toolCount + ' 个工具'"
      >{{ toolCount }} tools</span>
      <span
        class="sub-status"
        :class="'status-' + part.status"
        :title="part.status === 'err' && part.failureReason ? part.failureReason : statusLabel()"
      >{{ statusLabel() }}</span>
      <span v-if="part.elapsed" class="sub-elapsed">{{ part.elapsed }}</span>
      <component :is="open ? ChevronDown : ChevronRight" :size="12" class="sub-caret" />
    </button>
    <div v-if="open" class="sub-body">
      <div class="sub-body-summary">
        <div class="sub-summary-main">
          <span class="sub-summary-title">{{ titleLabel }}</span>
          <span class="sub-summary-status" :class="'status-' + part.status">{{ statusLabel() }}</span>
        </div>
        <div class="sub-summary-meta">
          <span>{{ runModeLabel }}</span>
          <span>{{ part.parts.length }} parts</span>
          <span v-if="toolCount > 0">{{ toolCount }} tools</span>
          <span v-if="part.elapsed">{{ part.elapsed }}</span>
          <span v-if="latestActivity">{{ latestActivity }}</span>
        </div>
      </div>
      <div v-if="part.status === 'start' && part.parts.length === 0" class="sub-empty-progress">
        <LoadingDots />
      </div>
      <button
        v-if="hiddenPartCount"
        type="button"
        class="sub-part-window-toggle"
        :title="`展开更早的 ${hiddenPartCount} 条子代理记录`"
        @click.stop="showEarlierParts"
      >已折叠 {{ hiddenPartCount }} 条过程记录</button>
      <div v-if="visibleRenderEntries.length" class="sub-stream">
        <div
          v-for="entry in visibleRenderEntries"
          :key="entry.kind === 'tool_group' ? `tool-group-${entry.startIndex}` : entry.index"
          class="sub-stream-item"
          :class="entry.kind === 'tool_group' ? 'stream-tool' : `stream-${entry.part.kind}`"
        >
          <div class="sub-stream-rail" aria-hidden="true">
            <span class="sub-stream-dot"></span>
          </div>
          <div class="sub-stream-content">
            <ToolCallGroup
              v-if="entry.kind === 'tool_group'"
              :parts="entry.parts"
            />
            <template v-else>
              <ThinkingBlock
                v-if="entry.part.kind === 'thinking'"
                :part="entry.part"
                :default-open="false"
              />
              <ToolCallCard v-else-if="entry.part.kind === 'tool'" :part="entry.part" />
              <QuestionTable v-else-if="entry.part.kind === 'question'" :part="entry.part" />
              <div
                v-else-if="entry.part.kind === 'text' && isLiveTextPart(entry.index, entry.part)"
                class="sub-text-block live"
              >
                <TypedText
                  :text="entry.part.text || ''"
                  :active="true"
                />
              </div>
              <div v-else-if="entry.part.kind === 'text'" class="sub-text-block">
                <div class="sub-text" v-html="renderSubText(entry.part.text)" />
              </div>
            </template>
          </div>
        </div>
      </div>
      <!-- task_id footer: stable identifier the LLM can pass back
           to resume this run. Click to copy. -->
      <div v-if="part.taskId" class="sub-taskid" @click.stop="copyTaskId">
        <span class="sub-taskid-label">task_id</span>
        <code class="sub-taskid-value">{{ part.taskId }}</code>
        <span class="sub-taskid-action">
          <template v-if="copyState === 'copied'">
            <Check :size="12" /> 已复制
          </template>
          <template v-else-if="copyState === 'err'">
            <X :size="12" /> 失败
          </template>
          <template v-else>
            <Clipboard :size="12" /> 复制
          </template>
        </span>
      </div>
    </div>
  </div>
</template>

<style scoped>
/* Sub-agent card. Same chrome as ToolCallCard (3px left
 * status rail, surface-2 body, radius-md) but the body
 * has a 1px left border + dashed border-top to nest it
 * visually inside the parent assistant message. The
 * agent's accent color (sub_agent_color) drives the rail
 * tint; falls back to a neutral quaternary border when
 * unset. */
.sub-agent-card {
  background: var(--surface-2);
  border: 1px solid var(--border-subtle);
  border-left: 3px solid var(--sub-accent, var(--border-strong));
  border-radius: var(--radius-md);
  margin: 6px 0;
  overflow: hidden;
  font-size: 12.5px;
}
.sub-agent-card.status-start { border-left-color: var(--sub-accent, var(--brand-500)); }
.sub-agent-card.status-ok    { border-left-color: var(--sub-accent, var(--success-500)); }
.sub-agent-card.status-err   { border-left-color: var(--sub-accent, var(--error-500)); }

.sub-header {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  width: 100%;
  background: transparent;
  border: 0;
  padding: var(--space-2) var(--space-3);
  text-align: left;
  cursor: pointer;
  color: var(--text-secondary);
  font-family: inherit;
  font-size: inherit;
  transition: background var(--dur-fast) var(--ease-out);
}
.sub-header:hover { background: var(--surface-3); }
/* The running state used to sweep a background gradient
 * (background-position animation = a full repaint + GPU raster pass
 * every frame). Since 2026-08-06 it's a static surface tint instead —
 * the running signal is carried by the left accent border, the status
 * text and the live parts counter, none of which cost per-frame work. */
.sub-header.running {
  background: var(--surface-3);
}
.sub-icon {
  display: inline-flex;
  align-items: center; justify-content: center;
  width: 18px; height: 18px;
  border-radius: var(--radius-sm);
  font-size: 11px;
  font-weight: 600;
  color: var(--sub-accent, var(--brand-500));
  background: var(--sub-accent-soft, var(--brand-50));
  flex-shrink: 0;
}
.sub-run-mode {
  display: inline-flex;
  align-items: center;
  padding: 1px 6px;
  border-radius: var(--radius-pill);
  border: 1px solid var(--border-subtle);
  background: var(--surface-3);
  color: var(--text-tertiary);
  font-size: 10.5px;
  line-height: 1.35;
  white-space: nowrap;
  flex-shrink: 0;
}
.sub-run-mode.mode-async {
  border-color: color-mix(in srgb, var(--sub-accent, var(--brand-500)) 24%, var(--border-subtle));
  background: color-mix(in srgb, var(--sub-accent, var(--brand-500)) 12%, var(--surface-2));
  color: var(--sub-accent, var(--brand-500));
}
.sub-title-wrap {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
  flex: 1;
  min-width: 0;
}
.sub-title {
  color: var(--text-primary);
  font-weight: 500;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
  line-height: 1.3;
}
.sub-activity {
  color: var(--text-tertiary);
  font-size: 11.5px;
  line-height: 1.3;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.sub-model {
  font-size: 10.5px;
  color: var(--text-tertiary);
  padding: 1px 5px;
  background: var(--surface-3);
  border-radius: var(--radius-sm);
  font-family: var(--font-mono);
  flex-shrink: 0;
  max-width: 140px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.sub-status {
  display: inline-flex;
  align-items: center;
  gap: var(--space-1);
  color: var(--text-tertiary);
  font-size: 11px;
  flex-shrink: 0;
}
.sub-status::before {
  content: "";
  width: 6px;
  height: 6px;
  border-radius: var(--radius-pill);
  background: var(--text-quaternary);
}
.sub-status.status-start::before { background: var(--brand-500); }
.sub-status.status-ok::before { background: var(--success-500); }
.sub-status.status-err::before { background: var(--error-500); }
.sub-elapsed {
  color: var(--text-quaternary);
  font-size: 11px;
  flex-shrink: 0;
  font-variant-numeric: tabular-nums;
}
/* P1-2 live event counter. Pill-shaped chip that lights
 * up while the sub-agent is running so the user sees
 * progress even when the body is collapsed. Style mirrors
 * the existing status / elapsed chips but with a subtle
 * brand tint when running. */
.sub-parts-count {
  display: inline-flex;
  align-items: center;
  padding: 1px 6px;
  border-radius: var(--radius-pill);
  background: var(--surface-3);
  color: var(--text-tertiary);
  font-size: 10.5px;
  font-variant-numeric: tabular-nums;
  flex-shrink: 0;
  transition: background var(--dur-fast) var(--ease-out),
              color var(--dur-fast) var(--ease-out);
}
.sub-tools-count {
  display: inline-flex;
  align-items: center;
  padding: 1px 6px;
  border-radius: var(--radius-pill);
  background: var(--surface-3);
  color: var(--text-tertiary);
  font-size: 10.5px;
  font-variant-numeric: tabular-nums;
  flex-shrink: 0;
}
.sub-header.running .sub-parts-count {
  background: color-mix(in srgb, var(--sub-accent, var(--brand-500)) 16%, var(--surface-2));
  color: var(--sub-accent, var(--brand-500));
}
.sub-caret { color: var(--text-tertiary); flex-shrink: 0; display: inline-flex; }

.sub-body {
  border-top: 1px dashed var(--border-subtle);
  padding: var(--space-3) var(--space-4) var(--space-4);
  background: var(--surface-1);
}
.sub-body-summary {
  display: grid;
  gap: var(--space-2);
  padding: var(--space-3);
  margin-bottom: var(--space-4);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-md);
  background: var(--surface-2);
}
.sub-summary-main,
.sub-summary-meta {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  min-width: 0;
}
.sub-summary-title {
  flex: 1;
  min-width: 0;
  color: var(--text-primary);
  font-size: 13px;
  font-weight: 600;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.sub-summary-status {
  display: inline-flex;
  align-items: center;
  gap: var(--space-1);
  color: var(--text-tertiary);
  font-size: 11px;
  flex-shrink: 0;
}
.sub-summary-status::before {
  content: "";
  width: 6px;
  height: 6px;
  border-radius: var(--radius-pill);
  background: var(--text-quaternary);
}
.sub-summary-status.status-start::before { background: var(--brand-500); }
.sub-summary-status.status-ok::before { background: var(--success-500); }
.sub-summary-status.status-err::before { background: var(--error-500); }
.sub-summary-meta {
  flex-wrap: wrap;
  color: var(--text-tertiary);
  font-size: 11.5px;
  line-height: 1.45;
}
.sub-summary-meta span {
  display: inline-flex;
  align-items: center;
  min-width: 0;
}
.sub-summary-meta span:not(:last-child)::after {
  content: "";
  width: 3px;
  height: 3px;
  margin-left: var(--space-2);
  border-radius: var(--radius-pill);
  background: var(--border-strong);
}
.sub-empty-progress {
  padding: var(--space-3) 0;
}
.sub-part-window-toggle {
  display: block;
  width: 100%;
  margin-bottom: var(--space-3);
  padding: var(--space-2) var(--space-3);
  border: 1px dashed var(--border-subtle);
  border-radius: var(--radius-sm);
  background: transparent;
  color: var(--text-tertiary);
  font: inherit;
  font-size: 12px;
  text-align: left;
  cursor: pointer;
}
.sub-part-window-toggle:hover {
  border-color: var(--border-default);
  color: var(--text-secondary);
}

.sub-stream {
  display: grid;
  gap: var(--space-4);
}
.sub-stream-item {
  display: grid;
  grid-template-columns: 18px minmax(0, 1fr);
  gap: var(--space-3);
  position: relative;
}
.sub-stream-rail {
  position: relative;
  display: flex;
  justify-content: center;
}
.sub-stream-rail::before {
  content: "";
  position: absolute;
  top: 14px;
  bottom: calc(-1 * var(--space-4));
  width: 1px;
  background: var(--border-subtle);
}
.sub-stream-item:last-child .sub-stream-rail::before {
  display: none;
}
.sub-stream-dot {
  width: 9px;
  height: 9px;
  margin-top: var(--space-2);
  border: 2px solid var(--surface-1);
  border-radius: var(--radius-pill);
  background: var(--text-quaternary);
  z-index: 1;
}
.stream-thinking .sub-stream-dot { background: var(--brand-500); }
.stream-tool .sub-stream-dot { background: var(--success-500); }
.stream-question .sub-stream-dot { background: var(--warn-500); }
.stream-text .sub-stream-dot { background: var(--sub-accent, var(--brand-500)); }
.sub-stream-content {
  min-width: 0;
}
.sub-text-block {
  padding: var(--space-3) var(--space-4);
  border: 1px solid var(--border-subtle);
  border-left: 3px solid var(--sub-accent, var(--brand-500));
  border-radius: var(--radius-md);
  background: var(--surface-0);
}
.sub-text-block.live {
  border-left-color: var(--brand-500);
}
.sub-text {
  margin: 0;
  color: var(--text-primary);
  font-size: 13.5px;
  line-height: 1.65;
}
.sub-text :deep(p) { margin: 0 0 var(--space-3) 0; }
.sub-text :deep(p:last-child) { margin-bottom: 0; }
.sub-text :deep(code) {
  background: var(--surface-2);
  padding: 1px var(--space-1);
  border-radius: var(--radius-sm);
  font-family: var(--font-mono);
  font-size: 12px;
}
.sub-text :deep(pre) {
  background: var(--surface-2);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  padding: var(--space-3);
  overflow-x: auto;
  margin: var(--space-3) 0;
}
.sub-text :deep(pre code) { background: none; padding: 0; }
.sub-text :deep(a) { color: var(--brand-600); }
.sub-text :deep(ul),
.sub-text :deep(ol) { padding-left: var(--space-6); margin: var(--space-2) 0 var(--space-3); }
.sub-text :deep(strong) { color: var(--text-primary); font-weight: 600; }
.sub-text :deep(em) { color: var(--text-secondary); }

.sub-stream-content :deep(.tool-group) {
  margin: 0;
  background: var(--surface-2);
}
.sub-stream-content :deep(.tool-group-header) {
  padding: var(--space-3) var(--space-4);
}
.sub-stream-content :deep(.tool-group-preview) {
  gap: var(--space-2);
  padding: var(--space-3) var(--space-4);
}
.sub-stream-content :deep(.tool-group-row) {
  min-height: 28px;
  gap: var(--space-3);
}
.sub-stream-content :deep(.tool-group-body) {
  padding: var(--space-3) var(--space-4);
}
.sub-stream-content :deep(.tool-card) {
  margin: 0 0 var(--space-2);
}
.sub-stream-content :deep(.tool-card:last-child) {
  margin-bottom: 0;
}
.sub-stream-content :deep(.tool-body) {
  padding: var(--space-3) var(--space-4);
}
.sub-stream-content :deep(.tool-args pre),
.sub-stream-content :deep(.tool-result pre),
.sub-stream-content :deep(.tool-error pre) {
  padding: var(--space-3);
  max-height: 160px;
}
.sub-taskid {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  margin-top: var(--space-4);
  padding: var(--space-2) var(--space-3);
  background: var(--surface-3);
  border-radius: var(--radius-sm);
  cursor: pointer;
  font-size: 10.5px;
  user-select: none;
}
.sub-taskid:hover { background: var(--surface-2); }
.sub-taskid-label {
  color: var(--text-tertiary);
  font-weight: 500;
  flex-shrink: 0;
}
.sub-taskid-value {
  flex: 1;
  color: var(--text-secondary);
  font-family: var(--font-mono);
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.sub-taskid-action {
  color: var(--text-tertiary);
  font-size: 10px;
  flex-shrink: 0;
  display: inline-flex;
  align-items: center;
  gap: 4px;
}
</style>
