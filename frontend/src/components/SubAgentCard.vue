<script setup lang="ts">
// Nested card for a sub-agent's stream. The header
// shows the task description, the agent name (e.g.
// "explore"), the model in use, the status, and the
// running / ok / err indicator. While the sub-agent is
// running, the header has a shimmer gradient animation;
// once finished, the user can click to expand the full
// sub-agent message stream (text + thinking +
// tool calls) — same structure as the parent
// bubble, just indented.
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
import { marked } from 'marked'
import type { SubAgentPart, MessagePart } from '../api/client'
import { Check, X, Clipboard, ChevronDown, ChevronRight } from './icons'
import ThinkingBlock from './ThinkingBlock.vue'
import ToolCallCard from './ToolCallCard.vue'
import LoadingDots from './LoadingDots.vue'

const props = defineProps<{ part: SubAgentPart }>()

// Default-open while running, default-closed once
// finished. The user can override either way.
const open = ref(props.part.status === 'start')
const userToggled = ref(false)
function toggle() {
  userToggled.value = true
  open.value = !open.value
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
    '--sub-accent-soft': `color-mix(in srgb, ${c} 14%, var(--bg-2))`,
  }
})

const statusLabel = () => {
  switch (props.part.status) {
    case 'start': return '运行中…'
    case 'ok':    return '已完成'
    case 'err':   return '失败'
    default:      return props.part.status
  }
}

// task_id copy affordance. Shown as a small monospace
// chip in the footer; click to copy.
const copyState = ref<'idle' | 'copied' | 'err'>('idle')
async function copyTaskId() {
  const id = props.part.taskId
  if (!id) return
  try {
    await navigator.clipboard.writeText(id)
    copyState.value = 'copied'
    setTimeout(() => (copyState.value = 'idle'), 1200)
  } catch {
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
// (e.g. server crash mid-run). The server's actual hard
// timeout is 5 minutes (subagent.go:652 and
// internal/config SubAgent.TimeoutDuration, default 5m). We
// use 6 minutes here so the client never force-closes BEFORE
// the server's natural close event lands — otherwise the
// client would mark the sub-agent as failed even when the
// server actually completed successfully. If the server
// timeout is configured differently, the user's server
// already enforces it; this client-side timer is purely a
// backstop.
const STUCK_TIMEOUT_MS = 6 * 60 * 1000 // 6 minutes

// renderSubText runs the sub-agent's text part through the
// same markdown pipeline as the parent MessageBubble uses
// (.md-body). Keeps the sub-agent's prose visually
// consistent with the main chat: headings, code, lists,
// links, bold/italic all render the same way. Falls back
// to the raw escaped text on parse failure so a malformed
// markdown payload never blanks the card.
function renderSubText(raw: string | undefined): string {
  if (!raw) return ''
  try {
    return marked.parse(raw, { async: false, gfm: true, breaks: true }) as string
  } catch {
    return raw.replace(/[&<>"']/g, c => ({
      '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
    }[c] as string))
  }
}
watch(
  () => props.part.status,
  (s, prev) => {
    if (s === 'start' && prev !== 'start') {
      // Just transitioned to running — arm the safety net.
      const t = setTimeout(() => {
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
    } else if (s !== 'start' && prev === 'start') {
      // Close event arrived — clear the safety net.
      if (stuckTimer) {
        clearTimeout(stuckTimer)
        stuckTimer = null
      }
    }
  },
  { immediate: true },
)
let stuckTimer: ReturnType<typeof setTimeout> | null = null
onBeforeUnmount(() => {
  if (stuckTimer) clearTimeout(stuckTimer)
})
</script>

<template>
  <div class="sub-agent-card" :class="'status-' + part.status" :style="accentStyle">
    <button
      class="sub-header"
      :class="{ running: part.status === 'start' }"
      @click="toggle"
      :title="open ? '收起子代消息' : '展开子代消息'"
    >
      <span class="sub-icon">{{ agentLabel.charAt(0).toUpperCase() }}</span>
      <span
        v-if="part.agentType"
        class="sub-agent-name"
        :title="part.agentDescription || agentLabel"
      >{{ agentLabel }}</span>
      <span class="sub-task">{{ part.task }}</span>
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
      >{{ part.parts.length }}</span>
      <span
        class="sub-status"
        :title="part.status === 'err' && part.failureReason ? part.failureReason : statusLabel()"
      >{{ statusLabel() }}</span>
      <span v-if="part.elapsed" class="sub-elapsed">{{ part.elapsed }}</span>
      <component :is="open ? ChevronDown : ChevronRight" :size="12" class="sub-caret" />
    </button>
    <div v-if="open" class="sub-body">
      <LoadingDots v-if="part.status === 'start' && part.parts.length === 0" />
      <template v-for="(p, i) in part.parts" :key="i">
        <ThinkingBlock
          v-if="p.kind === 'thinking'"
          :part="p"
          :default-open="false"
        />
        <ToolCallCard v-else-if="p.kind === 'tool'" :part="p" />
        <QuestionTable v-else-if="p.kind === 'question'" :part="p" />
        <div v-else-if="p.kind === 'text'" class="sub-text" v-html="renderSubText(p.text)" />
      </template>
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
  gap: 6px;
  width: 100%;
  background: transparent;
  border: 0;
  padding: 5px 12px;
  text-align: left;
  cursor: pointer;
  color: var(--text-secondary);
  font-family: inherit;
  font-size: inherit;
  transition: background var(--dur-fast) var(--ease-out);
}
.sub-header:hover { background: var(--surface-3); }
.sub-header.running {
  background-image: linear-gradient(
    90deg,
    var(--surface-2) 0%,
    var(--surface-3) 50%,
    var(--surface-2) 100%
  );
  background-size: 200% 100%;
  animation: sub-shimmer 1.8s linear infinite;
}
@keyframes sub-shimmer {
  0%   { background-position: 100% 0; }
  100% { background-position: -100% 0; }
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
.sub-agent-name {
  font-size: 11px;
  font-weight: 500;
  color: var(--sub-accent, var(--text-tertiary));
  padding: 1px 6px;
  background: var(--sub-accent-soft, var(--surface-3));
  border-radius: 3px;
  flex-shrink: 0;
}
.sub-task {
  flex: 1;
  min-width: 0;
  color: var(--text-primary);
  font-weight: 500;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.sub-model {
  font-size: 10.5px;
  color: var(--text-tertiary);
  padding: 1px 5px;
  background: var(--surface-3);
  border-radius: 3px;
  font-family: var(--font-mono);
  flex-shrink: 0;
  max-width: 140px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.sub-status { color: var(--text-tertiary); font-size: 11px; flex-shrink: 0; }
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
  border-radius: 999px;
  background: var(--surface-3, rgba(0, 0, 0, 0.05));
  color: var(--text-tertiary);
  font-size: 10.5px;
  font-variant-numeric: tabular-nums;
  flex-shrink: 0;
  transition: background var(--dur-fast, 120ms) var(--ease-out, ease);
}
.sub-header.running .sub-parts-count {
  background: color-mix(in srgb, var(--sub-accent, var(--brand-500)) 16%, var(--surface-2));
  color: var(--sub-accent, var(--brand-500));
}
.sub-caret { color: var(--text-tertiary); flex-shrink: 0; display: inline-flex; }

.sub-body {
  border-top: 1px dashed var(--border-subtle);
  padding: 6px 12px 8px;
  margin-left: 4px;
  border-left: 1px solid var(--border-subtle);
  background: var(--surface-1);
}
/* Inner markdown text — same .md-body class as the parent
 * MessageBubble uses, scoped to this card. Keeps the
 * sub-agent's text aligned with the main chat's rendering
 * (headings, code blocks, lists, links). */
.sub-text {
  margin: 4px 0;
  color: var(--text-primary);
  font-size: 13px;
  line-height: 1.55;
}
.sub-text :deep(p) { margin: 0 0 6px 0; }
.sub-text :deep(p:last-child) { margin-bottom: 0; }
.sub-text :deep(code) {
  background: var(--surface-2);
  padding: 1px 4px;
  border-radius: 3px;
  font-family: var(--font-mono);
  font-size: 12px;
}
.sub-text :deep(pre) {
  background: var(--surface-0);
  border: 1px solid var(--border-subtle);
  border-radius: var(--radius-sm);
  padding: 8px 10px;
  overflow-x: auto;
  margin: 6px 0;
}
.sub-text :deep(pre code) { background: none; padding: 0; }
.sub-text :deep(a) { color: var(--brand-600); }
.sub-text :deep(ul),
.sub-text :deep(ol) { padding-left: 22px; margin: 4px 0; }
.sub-text :deep(strong) { color: var(--text-primary); font-weight: 600; }
.sub-text :deep(em) { color: var(--text-secondary); }
.sub-taskid {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 6px;
  padding: 4px 6px;
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
