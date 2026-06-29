<script setup lang="ts">
import { computed, ref, watch, onUnmounted } from 'vue'
import type { ThinkingPart } from '../api/client'

const props = defineProps<{
  part: ThinkingPart
  defaultOpen?: boolean
}>()

const open = ref(!!props.defaultOpen)

// Typewriter animation for thinking text while streaming.
const displayed = ref(props.part.streaming ? '' : (props.part.text || ''))
let raf = 0
let lastT = 0
let carry = 0

function step(t: number) {
  if (!props.part.streaming) {
    displayed.value = props.part.text || ''
    raf = 0
    return
  }
  const full = props.part.text || ''
  if (displayed.value.length >= full.length) {
    raf = 0
    return
  }
  const dt = lastT ? t - lastT : 0
  lastT = t
  carry += (dt / 1000) * 120 // chars/sec for thinking
  const n = Math.floor(carry)
  if (n > 0) {
    displayed.value = full.slice(0, displayed.value.length + n)
    carry -= n
  }
  raf = requestAnimationFrame(step)
}

function startAnim() {
  if (raf) return
  lastT = 0
  carry = 0
  raf = requestAnimationFrame(step)
}

function stopAnim() {
  if (raf) {
    cancelAnimationFrame(raf)
    raf = 0
  }
}

watch(() => props.part.text, () => {
  if (props.part.streaming) {
    startAnim()
  } else {
    displayed.value = props.part.text || ''
  }
})

watch(() => props.part.streaming, (v) => {
  if (!v) {
    displayed.value = props.part.text || ''
    stopAnim()
  }
})

onUnmounted(stopAnim)

const html = computed(() => {
  const text = displayed.value
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
