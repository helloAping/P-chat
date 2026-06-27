<script setup lang="ts">
// DeepSeek-style "thinking" block. Default-collapsed,
// shows the model's chain-of-thought / reasoning text
// when expanded. While streaming, the block has a
// shimmer animation and a "思考中…" hint to make it
// obvious the model is still working.
//
// Used both for top-level thinking (parent agent's
// reasoning) and for sub-agent thinking (rendered
// inside the sub-agent card).
import { computed, ref } from 'vue'
import type { ThinkingPart } from '../api/client'

const props = defineProps<{
  part: ThinkingPart
  // When true, force the block open. Used by the
  // assistant bubble while the assistant is still
  // streaming — keeps the user oriented to what's
  // happening.
  defaultOpen?: boolean
}>()

const open = ref(!!props.defaultOpen)

const html = computed(() => {
  // Plain-text pre-wrap rendering. The model's
  // reasoning is rarely markdown-formatted, and we
  // don't want to leak any tool-call syntax the
  // LLM emitted mid-thought. Just escape and
  // preserve newlines.
  const text = props.part.text || ''
  return text
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
})
</script>

<template>
  <details
    class="thinking-block"
    :class="{ streaming: part.streaming }"
    :open="open"
  >
    <summary @click.prevent="open = !open">
      <span class="caret">{{ open ? '▾' : '▸' }}</span>
      <span class="label">
        <template v-if="part.streaming">思考中…</template>
        <template v-else>思考过程</template>
      </span>
      <span class="meta" v-if="part.text">{{ part.text.length }} 字</span>
    </summary>
    <pre class="thinking-body" v-if="open" v-html="html" />
  </details>
</template>

<style scoped>
.thinking-block {
  background: var(--bg-2);
  border: 1px solid var(--border-2);
  border-left: 3px solid var(--text-4);
  border-radius: 6px;
  margin: 6px 0;
  font-size: 12.5px;
  color: var(--text-2);
  overflow: hidden;
}
.thinking-block.streaming {
  /* The "currently streaming" visual: a soft shimmer
   * sweep across the left border. Doubles as a
   * loading indicator. */
  background-image: linear-gradient(
    90deg,
    var(--bg-2) 0%,
    var(--bg-3) 50%,
    var(--bg-2) 100%
  );
  background-size: 200% 100%;
  animation: shimmer 1.8s linear infinite;
  border-left-color: var(--accent);
}
@keyframes shimmer {
  0%   { background-position: 100% 0; }
  100% { background-position: -100% 0; }
}
.thinking-block > summary {
  list-style: none;
  cursor: pointer;
  padding: 5px 10px;
  display: flex;
  align-items: center;
  gap: 6px;
  user-select: none;
  font-weight: 500;
}
.thinking-block > summary::-webkit-details-marker { display: none; }
.caret { color: var(--text-3); font-size: 10px; }
.label { color: var(--text-2); }
.meta { margin-left: auto; color: var(--text-4); font-size: 11px; }
.thinking-body {
  margin: 0;
  padding: 8px 12px;
  border-top: 1px dashed var(--border-2);
  white-space: pre-wrap;
  word-wrap: break-word;
  font-family: ui-monospace, Menlo, Consolas, monospace;
  font-size: 12px;
  line-height: 1.55;
  color: var(--text-3);
  max-height: 360px;
  overflow: auto;
}
</style>
