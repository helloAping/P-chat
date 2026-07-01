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
import type { SubAgentPart, MessagePart } from '../api/client'
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
const STUCK_TIMEOUT_MS = 5 * 60 * 1000 // 5 minutes
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
      <span
        class="sub-status"
        :title="part.status === 'err' && part.failureReason ? part.failureReason : statusLabel()"
      >{{ statusLabel() }}</span>
      <span v-if="part.elapsed" class="sub-elapsed">{{ part.elapsed }}</span>
      <span class="sub-caret">{{ open ? '▾' : '▸' }}</span>
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
        <div v-else-if="p.kind === 'text'" class="sub-text">{{ p.text }}</div>
      </template>
      <!-- task_id footer: stable identifier the LLM can pass back
           to resume this run. Click to copy. -->
      <div v-if="part.taskId" class="sub-taskid" @click.stop="copyTaskId">
        <span class="sub-taskid-label">task_id</span>
        <code class="sub-taskid-value">{{ part.taskId }}</code>
        <span class="sub-taskid-action">
          {{ copyState === 'copied' ? '✓ 已复制' : copyState === 'err' ? '✗ 失败' : '📋 复制' }}
        </span>
      </div>
    </div>
  </div>
</template>

<style scoped>
.sub-agent-card {
  background: var(--bg-2);
  border: 1px solid var(--border-2);
  border-left: 3px solid var(--sub-accent, var(--text-4));
  border-radius: 6px;
  margin: 6px 0;
  overflow: hidden;
  font-size: 12.5px;
}
.sub-agent-card.status-start { border-left-color: var(--sub-accent, var(--accent)); }
.sub-agent-card.status-ok    { border-left-color: var(--sub-accent, var(--success)); }
.sub-agent-card.status-err   { border-left-color: var(--sub-accent, var(--error)); }

.sub-header {
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
  background: transparent;
  border: 0;
  padding: 5px 10px;
  text-align: left;
  cursor: pointer;
  color: var(--text-2);
  font-family: inherit;
  font-size: inherit;
}
.sub-header:hover { background: var(--bg-3); }
.sub-header.running {
  background-image: linear-gradient(
    90deg,
    var(--bg-2) 0%,
    var(--bg-3) 50%,
    var(--bg-2) 100%
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
  border-radius: 4px;
  font-size: 11px;
  font-weight: 600;
  color: var(--sub-accent, var(--accent));
  background: var(--sub-accent-soft, var(--accent-soft));
  flex-shrink: 0;
}
.sub-agent-name {
  font-size: 11px;
  font-weight: 500;
  color: var(--sub-accent, var(--text-3));
  padding: 1px 6px;
  background: var(--sub-accent-soft, var(--bg-3));
  border-radius: 3px;
  flex-shrink: 0;
}
.sub-task {
  flex: 1;
  min-width: 0;
  color: var(--text);
  font-weight: 500;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.sub-model {
  font-size: 10.5px;
  color: var(--text-3);
  padding: 1px 5px;
  background: var(--bg-3);
  border-radius: 3px;
  font-family: ui-monospace, monospace;
  flex-shrink: 0;
  max-width: 140px;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.sub-status { color: var(--text-3); font-size: 11px; flex-shrink: 0; }
.sub-elapsed { color: var(--text-4); font-size: 11px; flex-shrink: 0; }
.sub-caret { color: var(--text-3); font-size: 10px; flex-shrink: 0; }

.sub-body {
  border-top: 1px dashed var(--border-2);
  padding: 6px 10px 8px;
  margin-left: 4px;
  border-left: 1px solid var(--border-2);
  background: var(--bg);
}
.sub-text {
  white-space: pre-wrap;
  word-wrap: break-word;
  color: var(--text);
  line-height: 1.5;
  margin: 4px 0;
}
.sub-taskid {
  display: flex;
  align-items: center;
  gap: 6px;
  margin-top: 6px;
  padding: 4px 6px;
  background: var(--bg-3);
  border-radius: 4px;
  cursor: pointer;
  font-size: 10.5px;
  user-select: none;
}
.sub-taskid:hover { background: var(--bg-2); }
.sub-taskid-label {
  color: var(--text-3);
  font-weight: 500;
  flex-shrink: 0;
}
.sub-taskid-value {
  flex: 1;
  color: var(--text-2);
  font-family: ui-monospace, monospace;
  overflow: hidden;
  text-overflow: ellipsis;
  white-space: nowrap;
}
.sub-taskid-action {
  color: var(--text-3);
  font-size: 10px;
  flex-shrink: 0;
}
</style>
