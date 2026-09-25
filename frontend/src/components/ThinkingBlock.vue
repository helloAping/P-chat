<script setup lang="ts">
// Independent chain-of-thought block — not a tool event
// and not part of the execution timeline. Default-open
// while streaming.
import { ref, watch } from 'vue'
import type { ThinkingPart } from '../api/client'
import { Check, ChevronRight, Loader2 } from './icons'

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
  if (!userToggled.value) open.value = !!v
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
    <button class="thinking-header" type="button" @click="toggle" :aria-expanded="open">
      <span class="icon" :class="{ streaming: part.streaming }" aria-hidden="true">
        <Loader2 v-if="part.streaming" :size="11" class="spin" />
        <Check v-else :size="11" />
      </span>
      <span class="label">思考过程</span>
      <span v-if="part.streaming" class="status">思考中…</span>
      <span v-else-if="part.text" class="meta">{{ part.text.length }} 字</span>
      <ChevronRight
        :size="12"
        class="caret"
        :class="{ rotated: open }"
      />
    </button>
    <div v-if="open" class="thinking-body">
      <pre class="thinking-content">{{ part.text }}</pre>
    </div>
  </div>
</template>

<style scoped>
.thinking-block {
  margin: var(--space-2) 0;
  background: var(--thinking-bg);
  border: 1px solid var(--thinking-border);
  border-radius: var(--radius-md);
  overflow: hidden;
  font-size: 12.5px;
}

.thinking-header {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  width: 100%;
  padding: var(--space-2) var(--space-3);
  min-height: 32px;
  border: none;
  background: transparent;
  cursor: pointer;
  font-size: 12.5px;
  font-weight: 500;
  color: var(--text-secondary);
  text-align: left;
  user-select: none;
  border-radius: var(--radius-sm);
  transition: background var(--dur-fast) var(--ease-out),
              color var(--dur-fast) var(--ease-out);
}
.thinking-header:hover {
  background: color-mix(in srgb, var(--thinking-icon) 8%, transparent);
  color: var(--text-primary);
}
.icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  border-radius: 50%;
  background: var(--success-50);
  color: var(--success-500);
  flex-shrink: 0;
}
.icon.streaming {
  background: color-mix(in srgb, var(--thinking-icon) 12%, var(--surface-2));
  color: var(--thinking-icon);
}
.spin {
  animation: thinking-spin 1.1s linear infinite;
}
@keyframes thinking-spin {
  from { transform: rotate(0deg); }
  to   { transform: rotate(360deg); }
}
.label {
  flex: 1;
  min-width: 0;
  color: var(--text-primary);
  font-weight: 500;
}
.status {
  color: var(--text-tertiary);
  font-size: 11px;
  flex-shrink: 0;
}
.meta {
  color: var(--text-quaternary);
  font-size: 11px;
  font-weight: 400;
  font-variant-numeric: tabular-nums;
  flex-shrink: 0;
}
.caret {
  color: var(--text-quaternary);
  flex-shrink: 0;
  transition: transform var(--dur-fast) var(--ease-out);
}
.caret.rotated { transform: rotate(90deg); }

.thinking-body {
  border-top: 1px solid var(--border-subtle);
}
.thinking-content {
  margin: 0;
  padding: var(--space-3) var(--space-4) var(--space-4) 40px;
  white-space: pre-wrap;
  word-break: break-word;
  overflow-wrap: break-word;
  font-family: var(--font-sans);
  font-size: 12.5px;
  line-height: 1.6;
  color: var(--text-secondary);
  max-height: 280px;
  overflow: auto;
  background: transparent;
  border: none;
}
</style>
