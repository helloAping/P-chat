<script setup lang="ts">
// ThinkingBlock renders the <details> block for the
// assistant's reasoning trace. The text inside is
// shown verbatim as it streams in via SSE — no
// artificial typewriter animation. The "typewriter"
// feel comes from the natural streaming: each SSE
// content chunk adds to `part.text`, the DOM
// re-renders, and the user sees the text grow
// chunk-by-chunk as the model emits it.
//
// `defaultOpen` controls whether the disclosure is
// open on first render; once the user clicks the
// summary, the open/closed state is owned by the
// local `open` ref (so subsequent prop changes
// don't yank it back).

import { ref, watch } from 'vue'
import type { ThinkingPart } from '../api/client'

const props = defineProps<{
  part: ThinkingPart
  defaultOpen?: boolean
}>()

const open = ref(!!props.defaultOpen)
const userToggled = ref(false)

watch(() => props.defaultOpen, (v) => {
  // Only follow the parent's default while the user
  // hasn't interacted yet. We treat `open` as sticky
  // after the first user click.
  if (!userToggled.value) open.value = !!v
})

function toggle() {
  open.value = !open.value
  userToggled.value = true
}

const html = ref('')
watch(() => props.part.text, (t) => {
  html.value = (t || '')
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
}, { immediate: true })
</script>

<template>
  <details
    class="thinking-block"
    :class="{ streaming: part.streaming }"
    :open="open"
  >
    <summary @click.prevent="toggle">
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
