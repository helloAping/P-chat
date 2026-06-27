<script setup lang="ts">
// Nested card for a sub-agent's stream. The header
// shows the task description and the running / ok /
// err status. While the sub-agent is running, the
// header has a shimmer gradient animation; once
// finished, the user can click to expand the full
// sub-agent message stream (text + thinking +
// tool calls) — same structure as the parent
// bubble, just indented.
import { ref } from 'vue'
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

const statusLabel = () => {
  switch (props.part.status) {
    case 'start': return '运行中…'
    case 'ok':    return '已完成'
    case 'err':   return '失败'
    default:      return props.part.status
  }
}
</script>

<template>
  <div class="sub-agent-card" :class="'status-' + part.status">
    <button
      class="sub-header"
      :class="{ running: part.status === 'start' }"
      @click="toggle"
      :title="open ? '收起子代消息' : '展开子代消息'"
    >
      <span class="sub-icon">⌥</span>
      <span class="sub-task">{{ part.task }}</span>
      <span class="sub-status">{{ statusLabel() }}</span>
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
    </div>
  </div>
</template>

<style scoped>
.sub-agent-card {
  background: var(--bg-2);
  border: 1px solid var(--border-2);
  border-left: 3px solid var(--text-4);
  border-radius: 6px;
  margin: 6px 0;
  overflow: hidden;
  font-size: 12.5px;
}
.sub-agent-card.status-start { border-left-color: var(--accent); }
.sub-agent-card.status-ok    { border-left-color: var(--success); }
.sub-agent-card.status-err   { border-left-color: var(--error); }

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
  /* The "still working" gradient. Same shimmer as
   * ThinkingBlock so the visual language is
   * consistent. */
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
  width: 16px; height: 16px;
  border-radius: 4px;
  font-size: 12px;
  color: var(--accent);
  background: var(--accent-soft);
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
.sub-status { color: var(--text-3); font-size: 11px; flex-shrink: 0; }
.sub-elapsed { color: var(--text-4); font-size: 11px; flex-shrink: 0; }
.sub-caret { color: var(--text-3); font-size: 10px; flex-shrink: 0; }

.sub-body {
  border-top: 1px dashed var(--border-2);
  padding: 6px 10px 8px;
  /* Indent the inner stream so it visually reads as
   * "nested inside the parent". */
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
</style>
