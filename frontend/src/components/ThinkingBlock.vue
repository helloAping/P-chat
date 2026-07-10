<script setup lang="ts">
// Collapsible block for the model's chain-of-thought.
// Default-open while streaming, default-closed once
// finished. The header swaps between Loader2 (spinning,
// while streaming) and Lightbulb (done) so the user can
// tell at a glance whether the model is still reasoning.
// Both icons come from lucide-vue-next (consistent with
// the rest of the app's icon system — earlier versions
// used raw Unicode escapes for these, which broke the
// icon system and rendered inconsistently across OSes).
import { ref, watch } from 'vue'
import type { ThinkingPart } from '../api/client'
import { ChevronRight, Lightbulb, Loader2 } from './icons'

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
    <button class="thinking-header" @click="toggle" :aria-expanded="open">
      <ChevronRight
        :size="12"
        class="caret"
        :class="{ rotated: open }"
      />
      <span class="icon">
        <Loader2 v-if="part.streaming" :size="13" class="spin" />
        <Lightbulb v-else :size="13" />
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
/* Thinking block. Same chrome as the other "structural"
 * cards (ToolCall, SubAgent, Question) but with no left
 * status rail — thinking is always a neutral preview,
 * not a status-bearing control. Surface flips from
 * transparent → surface-2 when expanded, so the user has
 * a clear visual cue for the open/closed state. */
.thinking-block {
  margin: 8px 0;
  border-radius: var(--radius-md);
  background: transparent;
  border: 1px solid var(--border-subtle);
  overflow: hidden;
  font-size: 13px;
  transition: border-color var(--dur-fast) var(--ease-out),
              background var(--dur-fast) var(--ease-out);
}
.thinking-block.open {
  border-color: var(--border-default);
  background: var(--surface-2);
}
.thinking-block.streaming {
  border-color: var(--border-default);
}
.thinking-block.streaming.open {
  /* Subtle brand-tinted wash so the user knows reasoning
   * is in-flight. 4% opacity keeps the tint from looking
   * like a hard error background. */
  background: color-mix(in srgb, var(--brand-500) 4%, var(--surface-2));
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
  color: var(--text-secondary);
  text-align: left;
  user-select: none;
  transition: color var(--dur-fast) var(--ease-out);
}
.thinking-header:hover {
  color: var(--text-primary);
}
.caret {
  color: var(--text-quaternary);
  flex-shrink: 0;
  transition: transform var(--dur-fast) var(--ease-out);
}
.caret.rotated { transform: rotate(90deg); }
.icon {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  color: var(--text-tertiary);
  flex-shrink: 0;
}
.thinking-block.streaming .icon { color: var(--brand-500); }
.spin {
  animation: thinking-spin 1.2s linear infinite;
}
@keyframes thinking-spin {
  from { transform: rotate(0deg); }
  to   { transform: rotate(360deg); }
}
.label {
  flex: 1;
}
.meta {
  color: var(--text-quaternary);
  font-size: 11px;
  font-weight: 400;
  font-variant-numeric: tabular-nums;
}

.thinking-body {
  border-top: 1px solid var(--border-subtle);
}
.thinking-content {
  margin: 0;
  padding: 10px 14px;
  white-space: pre-wrap;
  word-break: break-all;
  overflow-wrap: break-word;
  font-family: var(--font-mono);
  font-size: 12.5px;
  line-height: 1.6;
  color: var(--text-tertiary);
  max-height: 400px;
  overflow: auto;
  background: transparent;
  border: none;
}
</style>
