<script setup lang="ts">
import { ref, watch } from 'vue'
import type { ThinkingPart } from '../api/client'

const props = defineProps<{
  part: ThinkingPart
  defaultOpen?: boolean
}>()

const open = ref(!!props.defaultOpen || !!props.part.streaming)
const userToggled = ref(false)

watch(() => props.defaultOpen, (v) => {
  if (!userToggled.value) open.value = !!v
})

watch(() => props.part.streaming, (v) => {
  if (!userToggled.value && v) open.value = true
})

function toggle() {
  open.value = !open.value
  userToggled.value = true
}
</script>

<template>
  <div
    class="thinking-block"
    :class="{ open, streaming: part.streaming }"
  >
    <button class="thinking-header" @click="toggle">
      <span class="caret">&#9654;</span>
      <span class="icon">
        <template v-if="part.streaming">&#9881;</template>
        <template v-else>&#128161;</template>
      </span>
      <span class="label">
        <template v-if="part.streaming">思考中…</template>
        <template v-else>思考过程</template>
      </span>
      <span class="meta" v-if="!part.streaming && part.text">{{ part.text.length }} 字</span>
    </button>
    <div class="thinking-body" v-show="open">
      <pre class="thinking-content">{{ part.text }}</pre>
    </div>
  </div>
</template>

<style scoped>
.thinking-block {
  margin: 8px 0;
  border-radius: 8px;
  background: transparent;
  border: 1px solid var(--border-2);
  overflow: hidden;
  font-size: 13px;
  transition: border-color 0.2s;
}
.thinking-block.open {
  border-color: var(--border);
  background: var(--bg-2);
}
.thinking-block.streaming {
  border-color: var(--accent-muted, var(--border));
}
.thinking-block.streaming.open {
  background: color-mix(in srgb, var(--accent) 4%, var(--bg-2));
}

.thinking-header {
  display: flex;
  align-items: center;
  gap: 8px;
  width: 100%;
  padding: 8px 12px;
  border: none;
  background: transparent;
  cursor: pointer;
  font-size: 13px;
  font-weight: 500;
  color: var(--text-2);
  text-align: left;
  user-select: none;
  transition: color 0.15s;
}
.thinking-header:hover {
  color: var(--text);
}
.caret {
  display: inline-block;
  font-size: 8px;
  transition: transform 0.2s;
  color: var(--text-4);
}
.open .caret {
  transform: rotate(90deg);
}
.icon {
  font-size: 13px;
  line-height: 1;
}
.label {
  flex: 1;
}
.meta {
  color: var(--text-4);
  font-size: 11px;
  font-weight: 400;
}

.thinking-body {
  border-top: 1px solid var(--border-2);
}
.thinking-content {
  margin: 0;
  padding: 10px 14px;
  white-space: pre-wrap;
  word-break: break-all;
  overflow-wrap: break-word;
  font-family: ui-monospace, Menlo, Consolas, monospace;
  font-size: 12.5px;
  line-height: 1.6;
  color: var(--text-3);
  max-height: 400px;
  overflow: auto;
  background: transparent;
  border: none;
}
</style>
